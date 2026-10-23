package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
// sha256(homedir-platform-arch)），同一容器内可直接整文件搬运。
//
// console 只解密「读身份」，从不重新加密：解密是为了拿到凭据里的 userId——
// 它是唯一稳定的账号标识。access token 每次刷新都会让文件内容变（摘要跟着变），
// 早期拿摘要当身份判据，结果同一个账号刷几次就堆出一串「自动备份」档案。
// 写回文件时仍然是整份拷贝，加密格式、密钥一律不碰。
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
	// Digest 是凭据文件内容的 sha256 前 16 位。它**不是**账号身份——token 一刷新就变，
	// 只用来判断「磁盘上这份文件有没有被换过」。账号身份看 UserID。
	Digest string `json:"digest"`
	// UserID 是凭据里 Z.AI 的用户标识（OAuth 才有），跨 token 刷新保持不变，
	// 是判断「两个档案是不是同一个账号」的唯一可靠依据。
	UserID string `json:"user_id,omitempty"`
	// APIKeyHint 是凭据里 API key 的掩码（前 6 后 4），同账号的 key 通常不变，
	// 用来让用户在界面上认人。
	APIKeyHint string `json:"api_key_hint,omitempty"`
	// JWTIat 是 start-plan token 的签发时间（Unix 秒），近似「这个账号最近一次登录」。
	JWTIat int64 `json:"jwt_iat,omitempty"`
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
	// zcodeDefaultProxyHome 是 zcode-proxy 镜像里 bun 用户的 HOME。
	// 凭据加密种子由它派生（见 zcodeCredentialSeed），默认值取自镜像实测。
	zcodeDefaultProxyHome = "/home/bun"
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

// zcodeProxyUID/GID 是 zcode-proxy 容器里 bun 用户的身份。
//
// 为什么必须显式 chown：console 为了能读 sidecar 写的 0600 凭据，以 root 运行
// （deploy/compose.zcode.yml 的 user: "0:0"），于是它落盘的文件属主也是 root。
// sidecar 以 uid 1000 运行，读不到 root 的 0600 文件，startProxy 直接报
// not_logged_in —— 表现为「切换/回滚之后通道再也起不来」，而文件内容其实完好。
// 所以每一次落盘都要把属主交还给 sidecar。
const (
	zcodeProxyUID = 1000
	zcodeProxyGID = 1000
)

// atomicWriteFile 写临时文件再 rename。权限统一 0600：凭据与索引都不该给别的用户看。
func atomicWriteFile(target string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	// 目录也交给 sidecar：它只需读 credentials.json，但属主统一能省掉一类难查的 EACCES。
	_ = os.Chown(filepath.Dir(target), zcodeProxyUID, zcodeProxyGID)
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
	// chown 失败不致命：没有 zcode overlay 时 console 以非 root 运行，此时也没有
	// sidecar 需要读这个文件，忽略 EPERM 即可。
	_ = tmp.Chown(zcodeProxyUID, zcodeProxyGID)
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

// zcodeCredential 是解密后凭据里我们关心的部分。sidecar 的完整结构还有 secret，
// 这里不取——console 不需要、也不该持有密钥材料。
type zcodeCredential struct {
	APIKey   string `json:"apiKey"`
	Provider string `json:"provider"`
	UserID   string `json:"userId"`
	JWT      string `json:"jwt"`
}

// zcodeCredentialSeed 复刻 sidecar 的密钥种子 `{homedir}-{platform}-{arch}`（store.ts）。
//
// platform/arch 固定写 linux/x64：sidecar 是 linux/amd64 的 bun，而 Node 的
// process.arch 报的是 "x64" 不是 "amd64"，写错一个字符就解不开。
// homedir 是镜像里 bun 用户的 /home/bun，可用 WB2A_ZCODE_HOME 覆盖。
func (h *server) zcodeCredentialSeed() string {
	home := strings.TrimSpace(h.cfg.ZCodeHome)
	if home == "" {
		home = zcodeDefaultProxyHome
	}
	return home + "-linux-x64"
}

// zcodeReadCredentialIdentity 解密凭据读出账号身份。任何一步失败都返回 nil，
// 调用方降级回「只看文件摘要」的老行为——解不开不该让整个账号功能不可用
// （比如上游某天换了 KDF，页面还得能用，只是认不出账号是谁）。
func (h *server) zcodeReadCredentialIdentity(path string) *zcodeCredential {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var env struct {
		Encrypted string `json:"encrypted"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Encrypted == "" {
		return nil
	}
	blob, err := base64.StdEncoding.DecodeString(env.Encrypted)
	if err != nil || len(blob) < 13 { // 12 字节 IV + 至少 1 字节密文
		return nil
	}
	key := sha256.Sum256([]byte(h.zcodeCredentialSeed()))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	plain, err := gcm.Open(nil, blob[:12], blob[12:], nil)
	if err != nil {
		return nil
	}
	var cred zcodeCredential
	if json.Unmarshal(plain, &cred) != nil {
		return nil
	}
	if cred.UserID == "" && cred.APIKey == "" {
		return nil
	}
	return &cred
}

// jwtIssuedAt 取 JWT payload 的 iat（Unix 秒）。只解码不验签——我们只读展示信息。
func (c *zcodeCredential) jwtIssuedAt() int64 {
	parts := strings.Split(c.JWT, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Iat int64 `json:"iat"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return 0
	}
	return claims.Iat
}

// maskSecret 保留头尾便于认人，中间隐去。太短的值整体隐去，避免「掩码等于明文」。
func maskSecret(value string) string {
	if len(value) < 12 {
		if value == "" {
			return ""
		}
		return "***"
	}
	return value[:6] + "…" + value[len(value)-4:]
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

// byIdentity 按「服务商 + 上游用户标识」查找档案，这是判断「是不是同一个账号」
// 的唯一可靠依据。userID 为空时返回 nil，调用方回退到摘要判定。
func (idx *zcodeAccountIndex) byIdentity(provider, userID string) *zcodeAccount {
	if userID == "" {
		return nil
	}
	for i := range idx.Accounts {
		if idx.Accounts[i].UserID != userID {
			continue
		}
		// provider 未知时不参与比较，避免旧档案缺这个字段就匹配不上。
		if provider != "" && idx.Accounts[i].Provider != "" && idx.Accounts[i].Provider != provider {
			continue
		}
		return &idx.Accounts[i]
	}
	return nil
}

// byDigest 按凭据摘要查找档案。摘要会随 token 刷新变化，所以它只能回答
// 「这份文件归档过没有」，不能回答「这个账号归档过没有」——后者用 byIdentity。
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
	cred := h.zcodeReadCredentialIdentity(credPath)
	provider := ""
	if v, ok := status["provider"].(string); ok {
		provider = v
	}
	if provider == "" && cred != nil {
		provider = cred.Provider
	}
	userID := ""
	if cred != nil {
		userID = cred.UserID
	}
	label = strings.TrimSpace(label)

	// 同一账号已经归档过：刷新那份档案的内容和时间，不新建条目。
	// 判据必须是 userId 而不是文件摘要——token 每次刷新都会让摘要变，
	// 用摘要判身份会让同一个账号堆出一串「自动备份」（线上真发生过：一个账号 3 条）。
	if userID != "" {
		if existing := idx.byIdentity(provider, userID); existing != nil {
			if err := copyFile(credPath, filepath.Join(dir, zcodeAuthsDir, existing.ID+".json")); err != nil {
				return idx, zcodeAccount{}, false, err
			}
			existing.Digest = digest
			existing.SavedAt = time.Now().Format(time.RFC3339)
			existing.UserID = userID
			existing.APIKeyHint = maskSecret(cred.APIKey)
			if iat := cred.jwtIssuedAt(); iat > 0 {
				existing.JWTIat = iat
			}
			if provider != "" {
				existing.Provider = provider
			}
			if v, ok := status["plan"].(string); ok && v != "" {
				existing.Plan = v
			}
			// 自动备份档案一旦被用户起了名，就升级成正式档案（保留用户起的名字）。
			if label != "" && strings.HasPrefix(existing.Label, zcodeAutoLabelPrefix) {
				existing.Label = label
			}
			idx.Active = existing.ID
			if err := writeZcodeIndex(dir, idx); err != nil {
				return idx, zcodeAccount{}, false, err
			}
			return idx, *existing, false, nil
		}
	} else if existing := idx.byDigest(digest); existing != nil {
		// 解不开凭据（上游换了 KDF 之类）时退回摘要判重，宁可少建一条也不要崩。
		return idx, *existing, false, nil
	}

	if label == "" {
		label = "账号 " + time.Now().Format("01-02 15:04")
	}
	acc := zcodeAccount{
		ID:      newZcodeAccountID(),
		Label:   label,
		SavedAt: time.Now().Format(time.RFC3339),
		Digest:  digest,
		UserID:  userID,
	}
	if cred != nil {
		acc.APIKeyHint = maskSecret(cred.APIKey)
		acc.JWTIat = cred.jwtIssuedAt()
	}
	if provider != "" {
		acc.Provider = provider
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
// 返回更新后的索引；当前凭据为空、或这个账号已经归档过时原样返回。
func (h *server) autoSnapshotUnfiled(dir string, idx zcodeAccountIndex, status map[string]any) (zcodeAccountIndex, error) {
	credPath := filepath.Join(dir, zcodeCredentialsFile)
	digest := digestFile(credPath)
	if digest == "" {
		return idx, nil
	}
	// 先按账号身份判断：token 刷新会让摘要变，但账号没变，不该再留一份备份。
	if cred := h.zcodeReadCredentialIdentity(credPath); cred != nil && cred.UserID != "" {
		provider := cred.Provider
		if v, ok := status["provider"].(string); ok && v != "" {
			provider = v
		}
		if idx.byIdentity(provider, cred.UserID) != nil {
			return idx, nil
		}
	} else if idx.byDigest(digest) != nil {
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
			resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"})
			if err != nil || !resp.OK {
				// 回滚也没把通道拉起来时，必须说清楚：否则用户以为只是「切换失败」，
				// 实际上通道已经停了，/v1 会一直 503，而页面上看不出原因。
				detail := "控制面不可达"
				if err == nil {
					detail = resp.Error
				}
				return nil, fmt.Errorf("%s；且回滚未能恢复通道（%s），请到「通道状态」重新启用", reason, detail)
			}
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
//
// 生效判据是「上游 userId」而不是索引里的 active，也不是文件摘要：
//   - 索引的 active 会过期（用户手工替换过 credentials.json 时）；
//   - 文件摘要会随 token 刷新变化，同一个账号会被认成不同账号。
func (h *server) zcodeAccountsView(dir string) (map[string]any, error) {
	idx, err := readZcodeIndex(dir)
	if err != nil {
		return nil, err
	}
	// 早期档案没记 userId（那时只用文件摘要）。先回填身份，再谈合并：
	// 不回填的话 dedupe 看到「都是空 userId」会跳过，用户还是看到 3 个账号。
	if filled, changed, ferr := h.backfillZcodeIdentities(dir, idx); ferr == nil && changed {
		idx = filled
	}
	// 合并历史遗留的重复档案（同一个账号多条「自动备份」），否则用户看到的
	// 账号数会比真实账号多。失败不阻塞视图：合并是清理，不是功能前提。
	if merged, changed, derr := h.dedupeZcodeAccounts(dir, idx); derr == nil && changed {
		idx = merged
	}

	credPath := filepath.Join(dir, zcodeCredentialsFile)
	curDigest := digestFile(credPath)
	cur := h.zcodeReadCredentialIdentity(credPath)
	activeID := ""
	if cur != nil && cur.UserID != "" {
		if acc := idx.byIdentity(cur.Provider, cur.UserID); acc != nil {
			activeID = acc.ID
		}
	}
	if activeID == "" {
		if acc := idx.byDigest(curDigest); acc != nil {
			activeID = acc.ID
		}
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
			// 账号身份：userId 是跨 token 刷新稳定的上游用户标识，界面用它区分账号。
			"user_id":      maskUserID(acc.UserID),
			"api_key_hint": acc.APIKeyHint,
			"jwt_iat":      acc.JWTIat,
		})
	}
	// 磁盘上有一份凭据还没归档（比如刚登录完）时 unfiled=true，页签据此提示先保存。
	// 判据同样优先看账号身份：token 刷新后摘要变了，但账号已归档就不该提示「未保存」。
	unfiled := false
	if curDigest != "" {
		if cur != nil && cur.UserID != "" {
			unfiled = idx.byIdentity(cur.Provider, cur.UserID) == nil
		} else {
			unfiled = idx.byDigest(curDigest) == nil
		}
	}
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
		// current_user_id 是当前凭据的账号身份（掩码）。前端据此判断「换回来的是不是
		// 同一个账号」——只比摘要会在 token 刷新后误判成「换了个新账号」。
		"current_user_id": maskUserID(func() string {
			if cur == nil {
				return ""
			}
			return cur.UserID
		}()),
	}, nil
}

// maskUserID 把上游 userId（UUID）掩码成可辨认的短码。
func maskUserID(userID string) string {
	if userID == "" {
		return ""
	}
	if len(userID) <= 12 {
		return userID
	}
	return userID[:8] + "…" + userID[len(userID)-4:]
}

// backfillZcodeIdentities 给「早期没记 userId 的档案」补上身份。
//
// 这些档案是在「只看文件摘要」的版本里建的，索引里没有 user_id，于是 dedupe
// 看到一串空值会全部跳过，用户依然看到多个重复账号。补法是解密各自的档案文件——
// 档案就是当时的凭据副本，能解出同一个 userId。解不开的（更早的旧格式）留着不动。
func (h *server) backfillZcodeIdentities(dir string, idx zcodeAccountIndex) (zcodeAccountIndex, bool, error) {
	changed := false
	for i := range idx.Accounts {
		acc := &idx.Accounts[i]
		if acc.UserID != "" {
			continue
		}
		cred := h.zcodeReadCredentialIdentity(filepath.Join(dir, zcodeAuthsDir, acc.ID+".json"))
		if cred == nil || cred.UserID == "" {
			continue
		}
		acc.UserID = cred.UserID
		acc.APIKeyHint = maskSecret(cred.APIKey)
		if iat := cred.jwtIssuedAt(); iat > 0 {
			acc.JWTIat = iat
		}
		if acc.Provider == "" {
			acc.Provider = cred.Provider
		}
		changed = true
	}
	if !changed {
		return idx, false, nil
	}
	if err := writeZcodeIndex(dir, idx); err != nil {
		return idx, false, err
	}
	return idx, true, nil
}

// dedupeZcodeAccounts 合并「同一账号的多份档案」。
//
// 背景：早期用文件摘要判断账号身份，而 access token 每次刷新都会重写 credentials.json，
// 摘要跟着变，于是同一个账号被反复当成「新账号」留档。线上真发生过：一个账号堆出
// 3 条档案（zai 主号 + 自动备份 ×2），用户以为有 3 个账号。
//
// 归并规则：
//   - 只在 userId 相同且 provider 相同（或任一方为空）时合并；
//   - 保留「用户命名过」的那条（label 不带自动备份前缀），都是自动备份则留最新的；
//   - 保留者的凭据内容取组内 JWT 签发时间最新的那份（就是当前有效 token）；
//   - 其余档案文件删除，索引条目移除。
func (h *server) dedupeZcodeAccounts(dir string, idx zcodeAccountIndex) (zcodeAccountIndex, bool, error) {
	groups := make(map[string][]int)
	var order []string
	for i, acc := range idx.Accounts {
		if acc.UserID == "" {
			continue // 身份未知的档案不参与合并，宁可留着让用户自己判断
		}
		key := acc.Provider + "\x00" + acc.UserID
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}

	dropped := make(map[int]bool)
	keepFor := make(map[int]int) // 被删索引 → 保留索引
	for _, key := range order {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, i := range members[1:] {
			keepAcc, candAcc := idx.Accounts[keep], idx.Accounts[i]
			keepAuto := strings.HasPrefix(keepAcc.Label, zcodeAutoLabelPrefix)
			candAuto := strings.HasPrefix(candAcc.Label, zcodeAutoLabelPrefix)
			switch {
			case keepAuto && !candAuto:
				keep = i
			case keepAuto == candAuto && candAcc.SavedAt > keepAcc.SavedAt:
				keep = i
			}
		}
		// 内容取组内最新（JWT 签发时间最大）的那份：它才是当前可用的 token。
		newest := members[0]
		for _, i := range members[1:] {
			if idx.Accounts[i].JWTIat > idx.Accounts[newest].JWTIat {
				newest = i
			}
		}
		if newest != keep {
			from := filepath.Join(dir, zcodeAuthsDir, idx.Accounts[newest].ID+".json")
			to := filepath.Join(dir, zcodeAuthsDir, idx.Accounts[keep].ID+".json")
			if _, err := os.Stat(from); err == nil {
				if err := copyFile(from, to); err != nil {
					return idx, false, err
				}
			}
			idx.Accounts[keep].Digest = idx.Accounts[newest].Digest
			idx.Accounts[keep].JWTIat = idx.Accounts[newest].JWTIat
			idx.Accounts[keep].APIKeyHint = idx.Accounts[newest].APIKeyHint
			idx.Accounts[keep].SavedAt = idx.Accounts[newest].SavedAt
		}
		for _, i := range members {
			if i == keep {
				continue
			}
			dropped[i] = true
			keepFor[i] = keep
			_ = os.Remove(filepath.Join(dir, zcodeAuthsDir, idx.Accounts[i].ID+".json"))
		}
	}
	if len(dropped) == 0 {
		return idx, false, nil
	}

	kept := make([]zcodeAccount, 0, len(idx.Accounts)-len(dropped))
	for i, acc := range idx.Accounts {
		if dropped[i] {
			if idx.Active == acc.ID {
				// active 指向被删的那条时改指保留者，避免索引里留一个不存在的 id。
				idx.Active = idx.Accounts[keepFor[i]].ID
			}
			continue
		}
		kept = append(kept, acc)
	}
	idx.Accounts = kept
	if err := writeZcodeIndex(dir, idx); err != nil {
		return idx, false, err
	}
	return idx, true, nil
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
