package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 账号档案（手工轮换）。
//
// zcode-proxy 是单凭据架构：credentials.json 里只有一份加密凭据，登录第二个账号会
// 直接覆盖第一个，控制面也没有任何账号维度命令（见上游 TriDefender/zcode-api
// src/auth/store.ts、manager.ts、android/control.ts）。所以「多账号」只能由 console
// 在文件层实现：把每份凭据留档，切换时把选中那份覆盖回 credentials.json。
//
// 目录布局（与 zcode-proxy 共享 runtime/zcode）：
//
//	<ZCodeDataDir>/credentials.json   sidecar 唯一读取的当前凭据
//	<ZCodeDataDir>/accounts.json      档案索引
//	<ZCodeDataDir>/auths/<id>.json    账号档案
//
// 关键前提：凭据是 sidecar 用「容器内恒定密钥」加密的 blob（store.ts 的
// sha256(homedir-platform-arch)），同一容器内可直接整文件搬运，console 不需要
// 解密也不需要重新加密。这也意味着 console 看不到账号是哪个、额度剩多少——
// 标签由用户自己起，本模块只做文件级切换。
//
// 生效方式：我们跑的是 android 入口，它没有 serve 面板那条 syncAuthWithDisk 热加载
// （index.ts 的 startServePanel 才有），凭据只在 startProxy 里 setOAuthCredential。
// 因此切换必须 stopProxy → 换文件 → startProxy，否则 /v1 会继续花旧账号的额度。

// zcodeAccount 是一条账号档案的元数据。凭据本体在 auths/<id>.json。
type zcodeAccount struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Provider / Plan 取自保存那一刻的控制面状态。zai 与 bigmodel 的凭据不能互换，
	// 切换时若与当前不同就要走「先停用 → setConfig → 再启动」。
	Provider string `json:"provider,omitempty"`
	Plan     string `json:"plan,omitempty"`
	SavedAt  string `json:"saved_at"`
	// Digest 是凭据文件内容的 sha256 前 16 位，用来判断「当前生效的是哪个档案」。
	// 用摘要而不是 id 是因为用户可能直接改过 credentials.json（比如手工导入）。
	Digest string `json:"digest"`
}

// zcodeAccountIndex 是 accounts.json 的结构。Active 记录当前生效档案的 id，
// 仅作展示用——真实判据永远是 credentials.json 的摘要。
type zcodeAccountIndex struct {
	Active   string         `json:"active"`
	Accounts []zcodeAccount `json:"accounts"`
}

const (
	zcodeCredentialsFile = "credentials.json"
	zcodeAccountsFile    = "accounts.json"
	zcodeAuthsDir        = "auths"
	// 自动快照的标签前缀：登录新账号前把「还没归档的当前凭据」留一份，
	// 避免一次登录把上一个账号永久冲掉。
	zcodeAutoLabelPrefix = "自动备份 "
)

// errZcodeAccountsOff 表示没有挂载账号目录（WB2A_ZCODE_DATA_DIR 为空）。
var errZcodeAccountsOff = errors.New("zcode 账号档案未启用")

// zcodeDataDir 返回账号目录；未配置时返回 errZcodeAccountsOff。
func (h *server) zcodeDataDir() (string, error) {
	dir := strings.TrimSpace(h.cfg.ZCodeDataDir)
	if dir == "" {
		return "", errZcodeAccountsOff
	}
	return dir, nil
}

// readZcodeIndex 读取索引。文件不存在视为空索引（首次使用），损坏则报错——
// 悄悄重置成空会让用户以为账号丢了，而实际是 JSON 写坏了。
func readZcodeIndex(dir string) (zcodeAccountIndex, error) {
	var idx zcodeAccountIndex
	raw, err := os.ReadFile(filepath.Join(dir, zcodeAccountsFile))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return idx, fmt.Errorf("accounts.json 解析失败: %w", err)
	}
	return idx, nil
}

// writeZcodeIndex 原子写索引：先写同目录临时文件再 rename，避免半个 JSON 落盘。
func writeZcodeIndex(dir string, idx zcodeAccountIndex) error {
	sort.Slice(idx.Accounts, func(i, j int) bool {
		return idx.Accounts[i].SavedAt > idx.Accounts[j].SavedAt
	})
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(dir, zcodeAccountsFile), raw, 0o600)
}

// atomicWriteFile 写临时文件再 rename。权限统一 0600：凭据与索引都不该给别的用户看。
func atomicWriteFile(target string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后 Remove 是 no-op
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// digestFile 返回文件内容的 sha256 前 16 位；文件不存在返回空串（不报错，
// 「还没有凭据」是正常状态而不是故障）。
func digestFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return ""
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

// copyFile 复制凭据：先落临时文件再 rename，保证 sidecar 永远读不到半个文件。
func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return atomicWriteFile(dst, raw, 0o600)
}

func newZcodeAccountID() string {
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(buf)
}

// byID 按 id 查找档案；返回指针便于就地更新。
func (idx *zcodeAccountIndex) byID(id string) *zcodeAccount {
	for i := range idx.Accounts {
		if idx.Accounts[i].ID == id {
			return &idx.Accounts[i]
		}
	}
	return nil
}

// byDigest 按凭据摘要查找档案，用于识别「当前生效的是哪个档案」。
func (idx *zcodeAccountIndex) byDigest(digest string) *zcodeAccount {
	if digest == "" {
		return nil
	}
	for i := range idx.Accounts {
		if idx.Accounts[i].Digest == digest {
			return &idx.Accounts[i]
		}
	}
	return nil
}

// zcodeAccountsSnapshot 把当前凭据存为新档案。label 为空时用时间戳兜底。
// 调用方需保证当前凭据存在（否则存一份空档案毫无意义）。
// created 表示这次是否真的新建了档案：同一份凭据重复保存返回 false，调用方据此判断
// 「磁盘上是不是刚换了新账号」。
func (h *server) zcodeAccountsSnapshot(dir string, idx zcodeAccountIndex, label string, status map[string]any) (zcodeAccountIndex, zcodeAccount, bool, error) {
	credPath := filepath.Join(dir, zcodeCredentialsFile)
	digest := digestFile(credPath)
	if digest == "" {
		return idx, zcodeAccount{}, false, errors.New("当前没有可保存的凭据")
	}
	// 同一份凭据已经归档过就直接返回旧档案，避免同一账号堆出一串重复条目。
	if existing := idx.byDigest(digest); existing != nil {
		return idx, *existing, false, nil
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "账号 " + time.Now().Format("01-02 15:04")
	}
	acc := zcodeAccount{
		ID:      newZcodeAccountID(),
		Label:   label,
		SavedAt: time.Now().Format(time.RFC3339),
		Digest:  digest,
	}
	if v, ok := status["provider"].(string); ok {
		acc.Provider = v
	}
	if v, ok := status["plan"].(string); ok {
		acc.Plan = v
	}
	if err := copyFile(credPath, filepath.Join(dir, zcodeAuthsDir, acc.ID+".json")); err != nil {
		return idx, zcodeAccount{}, false, err
	}
	idx.Accounts = append(idx.Accounts, acc)
	idx.Active = acc.ID
	if err := writeZcodeIndex(dir, idx); err != nil {
		return idx, zcodeAccount{}, false, err
	}
	return idx, acc, true, nil
}

// autoSnapshotUnfiled 在切换/登录前把「还没归档的当前凭据」留一份。
// 返回更新后的索引；当前凭据为空或已归档时原样返回。
func (h *server) autoSnapshotUnfiled(dir string, idx zcodeAccountIndex, status map[string]any) (zcodeAccountIndex, error) {
	digest := digestFile(filepath.Join(dir, zcodeCredentialsFile))
	if digest == "" || idx.byDigest(digest) != nil {
		return idx, nil
	}
	next, _, _, err := h.zcodeAccountsSnapshot(dir, idx, zcodeAutoLabelPrefix+time.Now().Format("01-02 15:04"), status)
	return next, err
}

// zcodeRestartProxy 让 sidecar 重新读取磁盘凭据。
// android 入口没有 serve 面板那条 syncAuthWithDisk 热加载，凭据只在 startProxy 里注入，
// 所以换完文件必须停再起；否则磁盘是新账号、内存还是旧账号，/v1 继续花旧账号的额度。
func (h *server) zcodeRestartProxy(ctx context.Context) error {
	if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "stopProxy"}); err != nil {
		return errControlUnreachable
	} else if !resp.OK && resp.Error != "not_running" {
		return fmt.Errorf("停用通道失败: %s", resp.Error)
	}
	if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"}); err != nil {
		return errControlUnreachable
	} else if !resp.OK {
		return fmt.Errorf("重新启用通道失败: %s", resp.Error)
	}
	return nil
}

// zcodeSwitchAccount 执行「A 开 B 停」：同一时刻只有一份凭据生效。
//
// 顺序不能反：必须先 stopProxy 再换文件，否则 /v1 继续用内存里的旧凭据花额度
// （android 入口没有 syncAuthWithDisk 热加载）。provider 变更更严格——setConfig
// 在 proxy 运行时直接返回 stop_proxy_first。
//
// 失败一律回滚到切换前的凭据并尽力恢复运行，避免「切到一半通道死了」。
func (h *server) zcodeSwitchAccount(ctx context.Context, dir string, id string) (map[string]any, error) {
	idx, err := readZcodeIndex(dir)
	if err != nil {
		return nil, err
	}
	target := idx.byID(id)
	if target == nil {
		return nil, errors.New("账号档案不存在")
	}
	credPath := filepath.Join(dir, zcodeCredentialsFile)
	authPath := filepath.Join(dir, zcodeAuthsDir, id+".json")
	if _, err := os.Stat(authPath); err != nil {
		return nil, errors.New("账号档案文件缺失")
	}

	status := h.zcodeControlStatus(ctx)
	// 切换前把未归档的当前凭据留档，这样「切走再切回」永远回得来。
	idx, err = h.autoSnapshotUnfiled(dir, idx, status)
	if err != nil {
		return nil, err
	}
	if cur := idx.byDigest(digestFile(credPath)); cur != nil && cur.ID == id {
		idx.Active = id
		if err := writeZcodeIndex(dir, idx); err != nil {
			return nil, err
		}
		return map[string]any{"switched": false, "already_active": true, "account": target}, nil
	}

	wasRunning, _ := status["proxy_running"].(bool)
	curProvider, _ := status["provider"].(string)
	needProvider := target.Provider != "" && curProvider != "" && target.Provider != curProvider
	if needProvider && !wasRunning {
		// 停着的通道也要确认 config 已指向目标 provider，否则 startProxy 会用错上游。
		if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "setConfig", "provider": target.Provider}); err != nil {
			return nil, errControlUnreachable
		} else if !resp.OK {
			return nil, fmt.Errorf("切换服务商失败: %s", resp.Error)
		}
	}

	// 备份当前凭据，供失败回滚。
	prevCred, prevErr := os.ReadFile(credPath)
	stopped := false
	if wasRunning {
		if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "stopProxy"}); err != nil {
			return nil, errControlUnreachable
		} else if !resp.OK && resp.Error != "not_running" {
			return nil, fmt.Errorf("停用通道失败: %s", resp.Error)
		}
		stopped = true
	}

	rollback := func(reason string) (map[string]any, error) {
		if prevErr == nil {
			_ = atomicWriteFile(credPath, prevCred, 0o600)
		}
		if stopped {
			_, _ = h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"})
		}
		return nil, errors.New(reason)
	}

	if needProvider {
		if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "setConfig", "provider": target.Provider}); err != nil {
			return rollback("切换服务商时通道不可达，已回滚")
		} else if !resp.OK {
			return rollback(fmt.Sprintf("切换服务商失败（%s），已回滚", resp.Error))
		}
	}
	if err := copyFile(authPath, credPath); err != nil {
		return rollback("写入凭据失败，已回滚")
	}
	if stopped {
		if resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"}); err != nil {
			return rollback("重新启用通道时不可达，已回滚")
		} else if !resp.OK {
			return rollback(fmt.Sprintf("重新启用通道失败（%s），已回滚", resp.Error))
		}
	}

	// 用控制面回读确认：换了文件但读不出凭据，说明档案本身是坏的。
	after := h.zcodeControlStatus(ctx)
	if loggedIn, _ := after["logged_in"].(bool); !loggedIn {
		return rollback("切换后读不到凭据（档案可能已损坏），已回滚")
	}
	idx.Active = id
	if err := writeZcodeIndex(dir, idx); err != nil {
		return nil, err
	}
	return map[string]any{
		"switched":      true,
		"account":       target,
		"provider":      after["provider"],
		"plan":          after["plan"],
		"proxy_running": after["proxy_running"],
	}, nil
}

// zcodeAccountsView 组装页签需要的账号列表：每个档案标注是否为当前生效。
// 判据是凭据摘要而不是索引里的 active——用户手工替换过 credentials.json 时，
// 索引的 active 会过期，摘要才是事实。
func (h *server) zcodeAccountsView(dir string) (map[string]any, error) {
	idx, err := readZcodeIndex(dir)
	if err != nil {
		return nil, err
	}
	curDigest := digestFile(filepath.Join(dir, zcodeCredentialsFile))
	activeID := ""
	if cur := idx.byDigest(curDigest); cur != nil {
		activeID = cur.ID
	}
	out := make([]map[string]any, 0, len(idx.Accounts))
	for _, acc := range idx.Accounts {
		out = append(out, map[string]any{
			"id":       acc.ID,
			"label":    acc.Label,
			"provider": acc.Provider,
			"plan":     acc.Plan,
			"saved_at": acc.SavedAt,
			"active":   acc.ID == activeID,
		})
	}
	// 磁盘上有一份凭据还没归档（比如刚登录完）时 unfiled=true，页签据此提示先保存。
	unfiled := curDigest != "" && idx.byDigest(curDigest) == nil
	return map[string]any{
		"available": true,
		"accounts":  out,
		"active":    activeID,
		"unfiled":   unfiled,
		"count":     len(out),
		// current_digest 是「磁盘上那份凭据」的指纹。换账号时页签记下发起登录前的值，
		// 轮询发现它变了就说明新账号已经登录完成——这是唯一可靠的判据：logged_in 在
		// 「A 已登录、正在登录 B」的全程都是 true，用它判断永远看不出变化。
		"current_digest": curDigest,
	}, nil
}

func (h *server) adminZcodeAccounts(w http.ResponseWriter, r *http.Request) {
	dir, err := h.zcodeDataDir()
	if err != nil {
		writeJSON(w, 200, map[string]any{"available": false, "accounts": []any{}})
		return
	}
	view, err := h.zcodeAccountsView(dir)
	if err != nil {
		adminError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, view)
}

// adminZcodeAccountSave 把当前生效的凭据存为一条档案。
// 这是「登录完新账号 → 起个名字存下来」的那一步，也是轮换的前提。
//
// 新建档案时顺带让新凭据生效：登录是直接覆盖 credentials.json 的，而 sidecar 内存里
// 还是旧账号（android 入口无热加载）。不重启的话，用户以为已经换成新账号，实际 /v1
// 还在花旧账号的额度——这种「看起来成功了」的不一致比直接报错更糟。
func (h *server) adminZcodeAccountSave(w http.ResponseWriter, r *http.Request) {
	dir, err := h.zcodeDataDir()
	if err != nil {
		adminError(w, 409, "账号档案未启用：console 未挂载 runtime/zcode")
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		adminError(w, 500, err.Error())
		return
	}
	status := h.zcodeControlStatus(r.Context())
	// 第一个档案是「给当前正在用的账号起个名字」：磁盘凭据就是内存里那份，重启纯属多余中断。
	// 之后每新增一个档案，才意味着磁盘刚被一次新登录覆盖过。
	firstAccount := len(idx.Accounts) == 0
	idx, acc, created, err := h.zcodeAccountsSnapshot(dir, idx, in.Label, status)
	if err != nil {
		adminError(w, 400, err.Error())
		return
	}
	out := map[string]any{"saved": true, "account": acc, "applied": false}
	if created && !firstAccount {
		if running, _ := status["proxy_running"].(bool); running {
			if err := h.zcodeRestartProxy(r.Context()); err != nil {
				// 档案已经存好了，重启失败不回滚——凭据在磁盘上是对的，用户可以手动停用再启用。
				out["apply_error"] = err.Error()
			} else {
				out["applied"] = true
			}
		}
	}
	writeJSON(w, 200, out)
}

func (h *server) adminZcodeAccountSwitch(w http.ResponseWriter, r *http.Request) {
	dir, err := h.zcodeDataDir()
	if err != nil {
		adminError(w, 409, "账号档案未启用：console 未挂载 runtime/zcode")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.ID) == "" {
		adminError(w, 400, "缺少账号 id")
		return
	}
	out, err := h.zcodeSwitchAccount(r.Context(), dir, in.ID)
	if err != nil {
		status := 502
		switch {
		case err == errControlUnreachable:
			status = 503
		case strings.Contains(err.Error(), "不存在"), strings.Contains(err.Error(), "缺失"):
			status = 404
		}
		adminError(w, status, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// adminZcodeAccountDelete 删除档案。当前生效的那条不允许删——否则「删完通道还在花
// 这个账号的额度，但页面上再也看不到它」，比拒绝更糟。
func (h *server) adminZcodeAccountDelete(w http.ResponseWriter, r *http.Request) {
	dir, err := h.zcodeDataDir()
	if err != nil {
		adminError(w, 409, "账号档案未启用：console 未挂载 runtime/zcode")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		adminError(w, 500, err.Error())
		return
	}
	acc := idx.byID(in.ID)
	if acc == nil {
		adminError(w, 404, "账号档案不存在")
		return
	}
	if cur := idx.byDigest(digestFile(filepath.Join(dir, zcodeCredentialsFile))); cur != nil && cur.ID == in.ID {
		adminError(w, 409, "当前生效的账号不能删除，请先切换到其它账号")
		return
	}
	kept := idx.Accounts[:0]
	for _, a := range idx.Accounts {
		if a.ID != in.ID {
			kept = append(kept, a)
		}
	}
	idx.Accounts = kept
	if idx.Active == in.ID {
		idx.Active = ""
	}
	if err := os.Remove(filepath.Join(dir, zcodeAuthsDir, in.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		adminError(w, 500, "删除档案文件失败")
		return
	}
	if err := writeZcodeIndex(dir, idx); err != nil {
		adminError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (h *server) adminZcodeAccountRename(w http.ResponseWriter, r *http.Request) {
	dir, err := h.zcodeDataDir()
	if err != nil {
		adminError(w, 409, "账号档案未启用：console 未挂载 runtime/zcode")
		return
	}
	var in struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		adminError(w, 400, "标签不能为空")
		return
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		adminError(w, 500, err.Error())
		return
	}
	acc := idx.byID(in.ID)
	if acc == nil {
		adminError(w, 404, "账号档案不存在")
		return
	}
	acc.Label = label
	if err := writeZcodeIndex(dir, idx); err != nil {
		adminError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"renamed": true, "account": acc})
}
