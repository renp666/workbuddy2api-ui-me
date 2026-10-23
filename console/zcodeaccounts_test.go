package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// fakeZcodeControl 模拟 zcode-proxy 的 /control：只需要 status / stopProxy / startProxy /
// setConfig 四个命令，但必须真实反映「proxy 是否在跑」——切换逻辑的停/起顺序全靠它。
type fakeZcodeControl struct {
	mu        sync.Mutex
	running   bool
	provider  string
	failStart bool
	cmds      []string
}

func (f *fakeZcodeControl) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Cmd      string `json:"cmd"`
			Provider string `json:"provider"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cmds = append(f.cmds, in.Cmd)
		switch in.Cmd {
		case "status":
			port := 0
			if f.running {
				port = 8080
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "state": "running", "provider": f.provider,
				"plan": "start-plan", "proxyPort": port, "loggedIn": true,
			})
		case "stopProxy":
			if !f.running {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not_running"})
				return
			}
			f.running = false
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "event": "proxyStopped"})
		case "startProxy":
			if f.failStart {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "boom"})
				return
			}
			if f.running {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "already_running"})
				return
			}
			f.running = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "event": "proxyStarted", "port": 8080})
		case "setConfig":
			if f.running {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "stop_proxy_first"})
				return
			}
			if in.Provider != "" {
				f.provider = in.Provider
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "event": "configUpdated", "provider": f.provider, "plan": "start-plan"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "unknown_cmd"})
		}
	}
}

// sawSince 只看 from 之后的命令。保存新建档案时会顺带重启通道，全局 saw 会把
// 「保存时的重启」算进「切换时的重启」，断言就失去意义了。
func sawSince(ctl *fakeZcodeControl, from int, cmd string) bool {
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	for _, c := range ctl.cmds[from:] {
		if c == cmd {
			return true
		}
	}
	return false
}

func (f *fakeZcodeControl) saw(cmd string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if c == cmd {
			return true
		}
	}
	return false
}

// newZcodeAccountsServer 造一个挂了账号目录的 console，凭据文件由调用方预置。
func newZcodeAccountsServer(t *testing.T, ctl *fakeZcodeControl) (*server, string) {
	t.Helper()
	dir := t.TempDir()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(core.Close)
	control := httptest.NewServer(ctl.handler())
	t.Cleanup(control.Close)

	cfg := testConfig(core.URL)
	cfg.ZCodeURL = mustParseURL(t, core.URL)
	cfg.ZCodeControlURL = mustParseURL(t, control.URL)
	cfg.ZCodeDataDir = dir
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := handler.(*server)
	if !ok {
		t.Fatalf("NewServer 返回 %T，want *server", handler)
	}
	return h, dir
}

func writeCredential(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, zcodeCredentialsFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCredential(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, zcodeCredentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func postJSON(t *testing.T, h *server, fn func(http.ResponseWriter, *http.Request), body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// 保存两份凭据后能来回切，且切换期间通道先停后起（否则 /v1 会继续花旧账号的额度）。
func TestZcodeAccountsSaveAndSwitch(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	if rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A 主号"}); rec.Code != 200 {
		t.Fatalf("保存 A: %d %s", rec.Code, rec.Body)
	}

	// 换一个账号的凭据再存一份（真实场景是重新登录后覆盖了 credentials.json）。
	writeCredential(t, dir, `{"encrypted":"BBB"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "B 备用"})
	if rec.Code != 200 {
		t.Fatalf("保存 B: %d %s", rec.Code, rec.Body)
	}
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	bID := saved.Account.ID

	idx, err := readZcodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Accounts) != 2 {
		t.Fatalf("档案数 = %d，want 2", len(idx.Accounts))
	}
	aID := ""
	for _, a := range idx.Accounts {
		if a.Label == "A 主号" {
			aID = a.ID
		}
	}
	if aID == "" {
		t.Fatal("找不到 A 主号档案")
	}

	// 切回 A：文件内容必须变回 AAA。
	if rec := postJSON(t, h, h.adminZcodeAccountSwitch, map[string]string{"id": aID}); rec.Code != 200 {
		t.Fatalf("切到 A: %d %s", rec.Code, rec.Body)
	}
	if got := readCredential(t, dir); got != `{"encrypted":"AAA"}` {
		t.Errorf("切换后凭据 = %q，want AAA", got)
	}
	if !ctl.saw("stopProxy") || !ctl.saw("startProxy") {
		t.Errorf("切换必须走 stopProxy→startProxy，实际命令序列 = %v", ctl.cmds)
	}

	// 再切回 B。
	if rec := postJSON(t, h, h.adminZcodeAccountSwitch, map[string]string{"id": bID}); rec.Code != 200 {
		t.Fatalf("切到 B: %d %s", rec.Code, rec.Body)
	}
	if got := readCredential(t, dir); got != `{"encrypted":"BBB"}` {
		t.Errorf("切换后凭据 = %q，want BBB", got)
	}

	// 视图里的 active 必须跟着走。
	view, err := h.zcodeAccountsView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if view["active"] != bID {
		t.Errorf("active = %v，want %s", view["active"], bID)
	}
}

// 切到同一份凭据应幂等：不做 stop/start，也不重复建档。
func TestZcodeAccountsSwitchAlreadyActiveIsNoop(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)

	before := len(ctl.cmds)
	rec = postJSON(t, h, h.adminZcodeAccountSwitch, map[string]string{"id": saved.Account.ID})
	if rec.Code != 200 {
		t.Fatalf("重复切换: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["already_active"] != true {
		t.Errorf("应返回 already_active=true，得到 %v", out)
	}
	if sawSince(ctl, before, "stopProxy") || sawSince(ctl, before, "startProxy") {
		t.Errorf("同一账号不应重启通道，命令 = %v", ctl.cmds[before:])
	}
}

// 切换前必须把「还没归档的当前凭据」留档，否则切走再切不回来。
func TestZcodeAccountsAutoSnapshotBeforeSwitch(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)

	// 用户直接改写了凭据但没保存（例如手工导入），此时切到 A 会覆盖它。
	writeCredential(t, dir, `{"encrypted":"UNFILED"}`)
	if rec := postJSON(t, h, h.adminZcodeAccountSwitch, map[string]string{"id": saved.Account.ID}); rec.Code != 200 {
		t.Fatalf("切换: %d %s", rec.Code, rec.Body)
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Accounts) != 2 {
		t.Fatalf("档案数 = %d，want 2（应自动留档未归档凭据）", len(idx.Accounts))
	}
	found := false
	for _, a := range idx.Accounts {
		if strings.HasPrefix(a.Label, zcodeAutoLabelPrefix) {
			found = true
			raw, err := os.ReadFile(filepath.Join(dir, zcodeAuthsDir, a.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != `{"encrypted":"UNFILED"}` {
				t.Errorf("自动留档内容 = %q，want UNFILED", raw)
			}
		}
	}
	if !found {
		t.Error("没有生成自动备份档案")
	}
}

// 当前生效的账号不许删：删了以后通道还在花它的额度，页面上却再也看不到它。
func TestZcodeAccountsDeleteActiveRejected(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)

	if rec := postJSON(t, h, h.adminZcodeAccountDelete, map[string]string{"id": saved.Account.ID}); rec.Code != 409 {
		t.Errorf("删除当前生效账号应返回 409，得到 %d %s", rec.Code, rec.Body)
	}
	// 换成另一份凭据后，A 变成非生效，就可以删了。
	writeCredential(t, dir, `{"encrypted":"BBB"}`)
	if rec := postJSON(t, h, h.adminZcodeAccountDelete, map[string]string{"id": saved.Account.ID}); rec.Code != 200 {
		t.Errorf("删除非生效账号应成功，得到 %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, zcodeAuthsDir, saved.Account.ID+".json")); !os.IsNotExist(err) {
		t.Error("档案文件应已删除")
	}
}

// 重新启用失败时必须回滚凭据并尽力恢复运行，否则通道停在半路、凭据还是新的。
func TestZcodeAccountsSwitchRollsBackOnStartFailure(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	aID := saved.Account.ID

	writeCredential(t, dir, `{"encrypted":"BBB"}`)
	postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "B"})

	ctl.mu.Lock()
	ctl.failStart = true
	ctl.mu.Unlock()

	if rec := postJSON(t, h, h.adminZcodeAccountSwitch, map[string]string{"id": aID}); rec.Code == 200 {
		t.Fatalf("startProxy 失败时不应返回成功：%s", rec.Body)
	}
	if got := readCredential(t, dir); got != `{"encrypted":"BBB"}` {
		t.Errorf("回滚后凭据 = %q，want BBB（保持切换前的状态）", got)
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range idx.Accounts {
		if a.ID == aID && idx.Active == aID {
			t.Error("切换失败不应把 active 改成目标账号")
		}
	}
}

// active 判据是凭据摘要而不是索引里的 active：用户手工替换过 credentials.json 时，
// 索引会过期，摘要才是事实。
func TestZcodeAccountsViewUsesDigestNotStaleIndex(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	var saved struct {
		Account zcodeAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)

	// 手工把凭据换成档案里没有的内容：索引里 active 仍写着 A，但实际已经不是 A 了。
	writeCredential(t, dir, `{"encrypted":"MANUAL"}`)
	view, err := h.zcodeAccountsView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if view["active"] != "" {
		t.Errorf("active = %v，want 空（当前凭据不在档案里）", view["active"])
	}
	if view["unfiled"] != true {
		t.Errorf("unfiled = %v，want true", view["unfiled"])
	}
	if saved.Account.ID == "" {
		t.Fatal("A 档案应已建立")
	}
}

// 没挂载目录时功能整体关闭，页签只显示 available=false，不报错。
func TestZcodeAccountsUnavailableWithoutDir(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, _ := newZcodeAccountsServer(t, ctl)
	h.cfg.ZCodeDataDir = ""

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.adminZcodeAccounts(rec, req)
	if rec.Code != 200 {
		t.Fatalf("状态码 = %d，want 200", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["available"] != false {
		t.Errorf("available = %v，want false", out["available"])
	}
	// 写入类端点应明确拒绝，而不是静默成功。
	if rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "x"}); rec.Code != 409 {
		t.Errorf("保存应返回 409，得到 %d", rec.Code)
	}
}

// 首个档案是「给当前在用的账号起名字」，磁盘凭据就是内存里那份——不该为此中断通道。
func TestZcodeAccountsSaveFirstAccountDoesNotRestart(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	writeCredential(t, dir, `{"encrypted":"AAA"}`)

	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})
	if rec.Code != 200 {
		t.Fatalf("保存: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["applied"] != false {
		t.Errorf("首个档案不应重启通道，applied = %v", out["applied"])
	}
	if ctl.saw("stopProxy") {
		t.Errorf("首个档案不该停用通道，命令 = %v", ctl.cmds)
	}
}

// 新增档案意味着磁盘刚被一次新登录覆盖过，而 sidecar 内存里还是旧账号（android 入口
// 没有热加载）。此时必须停再起，否则用户以为换成了新账号、/v1 还在花旧账号的额度。
func TestZcodeAccountsSaveNewAccountRestartsProxy(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})

	writeCredential(t, dir, `{"encrypted":"BBB"}`)
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "B"})
	if rec.Code != 200 {
		t.Fatalf("保存 B: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["applied"] != true {
		t.Errorf("新增档案应让新凭据生效，applied = %v（%v）", out["applied"], out["apply_error"])
	}
	if !ctl.saw("stopProxy") || !ctl.saw("startProxy") {
		t.Errorf("新增档案应停再起，命令 = %v", ctl.cmds)
	}
}

// 重启失败时档案仍要留下，并如实回报 apply_error——不能悄悄假装成功。
func TestZcodeAccountsSaveReportsApplyFailure(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, `{"encrypted":"AAA"}`)
	postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "A"})

	writeCredential(t, dir, `{"encrypted":"BBB"}`)
	ctl.mu.Lock()
	ctl.failStart = true
	ctl.mu.Unlock()
	rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]string{"label": "B"})
	if rec.Code != 200 {
		t.Fatalf("保存应仍成功（档案已落盘）: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["applied"] != false || out["apply_error"] == nil {
		t.Errorf("重启失败要如实回报，得到 applied=%v apply_error=%v", out["applied"], out["apply_error"])
	}
	idx, err := readZcodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Accounts) != 2 {
		t.Errorf("档案数 = %d，want 2（重启失败不该回滚档案）", len(idx.Accounts))
	}
}

// 凭据文件必须对 sidecar 可读：console 以 root 运行（为了读 sidecar 写的 0600 文件），
// 它落盘的属主默认也是 root，而 sidecar 是 uid 1000。属主错了的后果不是「写失败」，
// 而是切换之后 startProxy 报 not_logged_in，通道再也起不来，文件内容却完好无损。
func TestZcodeWritesGiveOwnershipBackToSidecar(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("chown 语义只在 linux 上可验证")
	}
	if os.Geteuid() != 0 {
		t.Skip("需要 root 才能验证 chown；非 root 环境下 chown 被有意忽略")
	}
	dir := t.TempDir()
	if err := atomicWriteFile(filepath.Join(dir, zcodeCredentialsFile), []byte(`{"encrypted":"x"}`), 0o600); err != nil {
		t.Fatalf("写入凭据失败: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, zcodeCredentialsFile))
	if err != nil {
		t.Fatalf("读取凭据失败: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("当前平台没有 Stat_t")
	}
	if int(stat.Uid) != zcodeProxyUID || int(stat.Gid) != zcodeProxyGID {
		t.Fatalf("凭据属主 = %d:%d，期望 %d:%d：sidecar 读不到这个文件", stat.Uid, stat.Gid, zcodeProxyUID, zcodeProxyGID)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("凭据权限 = %o，期望 600", perm)
	}
}

// jwtWithIat 造一个只有 payload 有意义的 JWT：console 只解码不验签。
func jwtWithIat(iat int64) string {
	payload, _ := json.Marshal(map[string]any{"iat": iat})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// encryptZcodeCredential 按 sidecar 的格式加密凭据（base64(iv12 + AES-GCM)），
// 让测试走「解密读身份」的真实路径，而不是拿明文 JSON 蒙混过关。
func encryptZcodeCredential(t *testing.T, h *server, cred zcodeCredential) string {
	t.Helper()
	plain, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(h.zcodeCredentialSeed()))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	blob := gcm.Seal(iv, iv, plain, nil)
	env, err := json.Marshal(map[string]string{"encrypted": base64.StdEncoding.EncodeToString(blob)})
	if err != nil {
		t.Fatal(err)
	}
	return string(env)
}

// 同一个账号被重新登录/刷新 token 后，密文变了、摘要变了，但 userId 没变。
// 早期按摘要判身份，线上因此把一个账号堆成 3 条档案，用户以为有 3 个账号。
func TestZcodeAccountsRefreshSameUserKeepsOneEntry(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	const userID = "bc61b617-6b0f-4bfb-a0cf-e74f0ed0fddc"

	writeCredential(t, dir, encryptZcodeCredential(t, h, zcodeCredential{
		APIKey: "8bb5f1aaabbbcc5c0a", Provider: "zai", UserID: userID, JWT: jwtWithIat(1000),
	}))
	if rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]any{"label": "主号"}); rec.Code != 200 {
		t.Fatalf("首次保存失败: %d %s", rec.Code, rec.Body.String())
	}

	// 同一账号的新 token：密文不同 → 摘要不同，但 userId 相同。
	writeCredential(t, dir, encryptZcodeCredential(t, h, zcodeCredential{
		APIKey: "8bb5f1aaabbbcc5c0a", Provider: "zai", UserID: userID, JWT: jwtWithIat(2000),
	}))
	if rec := postJSON(t, h, h.adminZcodeAccountSave, map[string]any{"label": ""}); rec.Code != 200 {
		t.Fatalf("二次保存失败: %d %s", rec.Code, rec.Body.String())
	}

	idx, err := readZcodeIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Accounts) != 1 {
		t.Fatalf("同一账号刷新 token 后堆出 %d 条档案，期望 1 条", len(idx.Accounts))
	}
	if idx.Accounts[0].JWTIat != 2000 {
		t.Fatalf("档案没有刷新到最新 token：jwt_iat=%d", idx.Accounts[0].JWTIat)
	}
	if idx.Accounts[0].Label != "主号" {
		t.Fatalf("重复保存把用户起的名字改掉了：%q", idx.Accounts[0].Label)
	}
	if idx.Accounts[0].UserID != userID {
		t.Fatalf("档案没有记下 userId：%q", idx.Accounts[0].UserID)
	}
}

// 不同账号必须各自成条，且视图里的 active 按 userId 判定（不能按摘要）。
func TestZcodeAccountsDistinctUsersStaySeparateAndActiveFollowsUser(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)

	writeCredential(t, dir, encryptZcodeCredential(t, h, zcodeCredential{
		APIKey: "aaaaaaaaaaaabbbb", Provider: "zai", UserID: "user-aaaa-1111", JWT: jwtWithIat(100),
	}))
	postJSON(t, h, h.adminZcodeAccountSave, map[string]any{"label": "A"})
	writeCredential(t, dir, encryptZcodeCredential(t, h, zcodeCredential{
		APIKey: "ccccccccccccdddd", Provider: "zai", UserID: "user-bbbb-2222", JWT: jwtWithIat(200),
	}))
	postJSON(t, h, h.adminZcodeAccountSave, map[string]any{"label": "B"})

	idx, _ := readZcodeIndex(dir)
	if len(idx.Accounts) != 2 {
		t.Fatalf("两个不同账号被合并成 %d 条", len(idx.Accounts))
	}
	view, err := h.zcodeAccountsView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := view["count"]; got != 2 {
		t.Fatalf("count=%v，期望 2", got)
	}
	if got, _ := view["unfiled"].(bool); got {
		t.Fatal("当前凭据已归档，unfiled 不该为 true")
	}
	// 当前磁盘上是 B，active 必须是 B 那条。
	var activeLabel string
	for _, item := range view["accounts"].([]map[string]any) {
		if item["active"] == true {
			activeLabel = item["label"].(string)
		}
	}
	if activeLabel != "B" {
		t.Fatalf("active 指向 %q，期望 B", activeLabel)
	}
}

// 线上事故的复现：同一个 userId 已经堆了多条历史档案，打开页面时必须自动合并，
// 否则用户看到的账号数永远比真实账号多。
func TestZcodeAccountsViewMergesLegacyDuplicates(t *testing.T) {
	ctl := &fakeZcodeControl{running: true, provider: "zai"}
	h, dir := newZcodeAccountsServer(t, ctl)
	const userID = "bc61b617-6b0f-4bfb-a0cf-e74f0ed0fddc"

	auths := filepath.Join(dir, zcodeAuthsDir)
	if err := os.MkdirAll(auths, 0o700); err != nil {
		t.Fatal(err)
	}
	mk := func(id, label string, iat int64) zcodeAccount {
		body := encryptZcodeCredential(t, h, zcodeCredential{
			APIKey: "8bb5f1aaabbbcc5c0a", Provider: "zai", UserID: userID, JWT: jwtWithIat(iat),
		})
		if err := os.WriteFile(filepath.Join(auths, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return zcodeAccount{ID: id, Label: label, Provider: "zai", SavedAt: "2026-10-23T22:00:00+08:00",
			Digest: id, UserID: userID, JWTIat: iat}
	}
	idx := zcodeAccountIndex{Active: "dup-2", Accounts: []zcodeAccount{
		mk("main-1", "zai 主号", 1000),
		mk("dup-2", "自动备份 10-23 22:03", 2000),
		mk("dup-3", "自动备份 10-23 22:20", 3000),
	}}
	if err := writeZcodeIndex(dir, idx); err != nil {
		t.Fatal(err)
	}
	// 磁盘上的当前凭据是 dup-3 那份。
	writeCredential(t, dir, encryptZcodeCredential(t, h, zcodeCredential{
		APIKey: "8bb5f1aaabbbcc5c0a", Provider: "zai", UserID: userID, JWT: jwtWithIat(3000),
	}))

	view, err := h.zcodeAccountsView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := view["count"]; got != 1 {
		t.Fatalf("三条同账号档案没有合并：count=%v", got)
	}
	item := view["accounts"].([]map[string]any)[0]
	if item["label"] != "zai 主号" {
		t.Fatalf("合并后没保留用户命名的那条：%v", item["label"])
	}
	if item["active"] != true {
		t.Fatal("合并后 active 没落到保留者身上")
	}
	// 内容要取最新 token（iat=3000），否则切回去会用过期凭据。
	merged, _ := readZcodeIndex(dir)
	if merged.Accounts[0].JWTIat != 3000 {
		t.Fatalf("合并后没有采用最新 token：jwt_iat=%d", merged.Accounts[0].JWTIat)
	}
	if _, err := os.Stat(filepath.Join(auths, "dup-3.json")); !os.IsNotExist(err) {
		t.Fatal("重复档案文件没有被清理")
	}
}
