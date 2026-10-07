package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// CCR（claude-code-router）的管理服务只监听宿主回环，拒绝非回环 Host，并要求每次启动轮换的
// x-ccr-web-auth。console 容器没有宿主进程权限，所以这里能驱动的只有 CCR 自己的模型网关
// （startGateway / stopGateway），不是 ccr 后台服务本身。以下测试钉住这两条边界。

type ccrRPCSeen struct {
	host   string
	auth   string
	method string
}

type ccrStub struct {
	path  string
	token string
	port  string
	mu    sync.Mutex
	calls []ccrRPCSeen
}

func (s *ccrStub) serviceURL() string {
	return "http://127.0.0.1:" + s.port + "/?ccr_web_token=" + s.token
}

func (s *ccrStub) write(t *testing.T) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"pid":            424840,
		"profileManaged": true,
		"serviceToken":   "must-never-be-used",
		"startGateway":   true,
		"startedAt":      "2026-10-07T01:22:25.643Z",
		"url":            s.serviceURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (s *ccrStub) seen(method string) []ccrRPCSeen {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ccrRPCSeen
	for _, call := range s.calls {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

func (s *ccrStub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ccrStatusBody 是真机 getGatewayStatus 的响应形状（2026-10-07 容器内实测）。
func ccrStatusBody(state string) (int, string) {
	return 200, `{"ok":true,"value":{"coreEndpoint":"http://127.0.0.1:3456","endpoint":"http://127.0.0.1:3456","networkEndpoints":[],"state":"` + state + `","lastStartedAt":"2026-10-07T01:22:25.643Z","pid":455672}}`
}

// newCCRStub 起一个假 CCR 管理服务，并把 console 配成「CCR 已启用」。
// 配置里故意写 localhost 而不是 127.0.0.1，这样 Host 改写才有可证伪的断言。
func newCCRStub(t *testing.T, rpc func(method string) (int, string)) (http.Handler, *ccrStub) {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	stub := &ccrStub{token: strings.Repeat("t", 43)}
	ccr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ccr/rpc" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		stub.mu.Lock()
		stub.calls = append(stub.calls, ccrRPCSeen{host: r.Host, auth: r.Header.Get("x-ccr-web-auth"), method: in.Method})
		stub.mu.Unlock()
		status, body := rpc(in.Method)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ccr.Close)
	stub.port = strings.TrimPrefix(ccr.URL, "http://127.0.0.1:")
	stub.path = filepath.Join(t.TempDir(), "service.json")
	stub.write(t)
	cfg := testConfig(core.URL)
	cfg.CCRURL = mustParseURL(t, "http://localhost:"+stub.port)
	cfg.CCRTokenFile = stub.path
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, stub
}

func TestCCRLoopbackHost(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http://host.docker.internal:3458", "127.0.0.1:3458"},
		{"http://host.docker.internal", "127.0.0.1"},
		{"http://localhost:3458", "127.0.0.1:3458"},
		{"https://ccr.example.test:8443", "127.0.0.1:8443"},
	} {
		parsed := mustParseURL(t, tc.raw)
		if got := ccrLoopbackHost(parsed); got != tc.want {
			t.Fatalf("%s => %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseCCRWebAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	write := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"查询里的 token", `{"url":"http://127.0.0.1:3458/?ccr_web_token=abc123"}`, "abc123", true},
		{"token 含需要转义的字符", `{"url":"http://127.0.0.1:3458/?ccr_web_token=a%2Bb%2Fc%3D"}`, "a+b/c=", true},
		{"多个查询参数", `{"url":"http://127.0.0.1:3458/?x=1&ccr_web_token=zzz&y=2"}`, "zzz", true},
		{"serviceToken 不是管理 Token", `{"serviceToken":"wrong","url":"http://127.0.0.1:3458/?ccr_web_token=right"}`, "right", true},
		{"没有 token 查询参数", `{"url":"http://127.0.0.1:3458/"}`, "", false},
		{"参数为空", `{"url":"http://127.0.0.1:3458/?ccr_web_token="}`, "", false},
		{"缺 url 字段", `{"pid":1}`, "", false},
		{"url 非法", `{"url":":::"}`, "", false},
		{"不是 JSON", `service.json`, "", false},
		{"多余内容", `{"url":"http://127.0.0.1:3458/?ccr_web_token=abc"} trailing`, "", false},
	} {
		write(t, tc.body)
		got, ok := parseCCRWebAuth(path)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%s: got (%q,%v), want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	write(t, `{"url":"http://127.0.0.1:3458/?ccr_web_token=abc"}`)
	if got, ok := parseCCRWebAuth(filepath.Join(dir, "missing.json")); ok || got != "" {
		t.Fatalf("missing file: got (%q,%v)", got, ok)
	}
	huge := filepath.Join(dir, "huge.json")
	if err := os.WriteFile(huge, []byte(strings.Repeat("{}", 40000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := parseCCRWebAuth(huge); ok || got != "" {
		t.Fatalf("oversized file: got (%q,%v)", got, ok)
	}
}

func TestCCRDisabledWithoutConfig(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	})
	cookie, csrf := login(t, h)
	if w := adminRequest(h, "GET", "/admin/session", "", cookie, csrf); !strings.Contains(w.Body.String(), `"ccr_enabled":false`) {
		t.Fatalf("session must report ccr_enabled:false: %s", w.Body.String())
	}
	w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status without config: %d %s", w.Code, w.Body.String())
	}
	var status map[string]any
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if status["enabled"] != false {
		t.Fatalf("expected enabled:false, got %+v", status)
	}
	if w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, csrf); w.Code != 409 {
		t.Fatalf("action without config must be rejected, got %d %s", w.Code, w.Body.String())
	}
	if w := adminRequest(h, "GET", "/admin/ccr/link", "", cookie, csrf); w.Code != 409 {
		t.Fatalf("link without config must be rejected, got %d %s", w.Code, w.Body.String())
	}
}

func TestCCRRequestsRequireSessionAndCSRF(t *testing.T) {
	h, stub := newCCRStub(t, func(string) (int, string) { return ccrStatusBody("running") })
	for _, probe := range []struct{ method, path, body string }{
		{"GET", "/admin/ccr", ""},
		{"GET", "/admin/ccr/link", ""},
		{"POST", "/admin/ccr/gateway", `{"action":"start"}`},
	} {
		if w := adminRequest(h, probe.method, probe.path, probe.body, nil, ""); w.Code != 401 {
			t.Fatalf("%s %s without session: %d", probe.method, probe.path, w.Code)
		}
	}
	cookie, _ := login(t, h)
	if w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, ""); w.Code != 403 {
		t.Fatalf("missing csrf accepted: %d", w.Code)
	}
	if got := stub.total(); got != 0 {
		t.Fatalf("unauthenticated requests must not reach CCR: %+v", stub.calls)
	}
}

func TestCCRSessionReportsEnabled(t *testing.T) {
	h, _ := newCCRStub(t, func(string) (int, string) { return ccrStatusBody("running") })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/session", "", cookie, csrf)
	if !strings.Contains(w.Body.String(), `"ccr_enabled":true`) {
		t.Fatalf("session must report ccr_enabled:true: %s", w.Body.String())
	}
}

// 真机 CCR 对非回环 Host 一律 403（2026-10-07 容器内实测），所以发出去的请求必须把 Host
// 改写成回环形式；管理 Token 必须取 service.json 的 url 查询参数，不是 serviceToken 字段。
func TestCCRRPCUsesLoopbackHostAndQueryToken(t *testing.T) {
	h, stub := newCCRStub(t, func(string) (int, string) { return ccrStatusBody("running") })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	calls := stub.seen("getGatewayStatus")
	if len(calls) != 1 {
		t.Fatalf("expected one getGatewayStatus call, got %+v", stub.calls)
	}
	if calls[0].host != "127.0.0.1:"+stub.port {
		t.Fatalf("CCR rejects non-loopback Host, sent %q", calls[0].host)
	}
	if calls[0].auth != stub.token {
		t.Fatalf("auth header must be the query token, got %q", calls[0].auth)
	}
	if strings.Contains(w.Body.String(), stub.token) {
		t.Fatal("status response leaked the CCR management token")
	}
	if strings.Contains(w.Body.String(), "must-never-be-used") {
		t.Fatal("status response leaked service.json contents")
	}
	var status struct {
		Enabled   bool   `json:"enabled"`
		AuthReady bool   `json:"auth_ready"`
		Reachable bool   `json:"reachable"`
		State     string `json:"state"`
		Endpoint  string `json:"endpoint"`
		PID       int    `json:"pid"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if !status.Enabled || !status.AuthReady || !status.Reachable {
		t.Fatalf("unexpected flags: %+v", status)
	}
	if status.State != "running" || status.Endpoint != "http://127.0.0.1:3456" || status.PID != 455672 {
		t.Fatalf("status must mirror CCR value: %+v", status)
	}
}

// Token 每次启动轮换：不能缓存，改文件后下一次调用必须用新值。
func TestCCRWebAuthIsRereadAfterRotation(t *testing.T) {
	h, stub := newCCRStub(t, func(string) (int, string) { return ccrStatusBody("running") })
	cookie, csrf := login(t, h)
	if w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf); w.Code != 200 {
		t.Fatalf("first status: %d %s", w.Code, w.Body.String())
	}
	stub.token = strings.Repeat("r", 43)
	stub.write(t)
	if w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf); w.Code != 200 {
		t.Fatalf("second status: %d %s", w.Code, w.Body.String())
	}
	calls := stub.seen("getGatewayStatus")
	if len(calls) != 2 {
		t.Fatalf("expected a second call, got %+v", calls)
	}
	if calls[1].auth != stub.token {
		t.Fatalf("stale token cached: sent %q", calls[1].auth)
	}
}

func TestCCRStatusWithoutTokenFile(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	ccr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"value":{"state":"running"}}`)
	}))
	t.Cleanup(ccr.Close)
	cfg := testConfig(core.URL)
	cfg.CCRURL = mustParseURL(t, ccr.URL)
	cfg.CCRTokenFile = filepath.Join(t.TempDir(), "absent.json")
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("missing token file must still answer status: %d %s", w.Code, w.Body.String())
	}
	var status struct {
		Enabled   bool `json:"enabled"`
		AuthReady bool `json:"auth_ready"`
		Reachable bool `json:"reachable"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if !status.Enabled || status.AuthReady || status.Reachable {
		t.Fatalf("unexpected flags: %+v", status)
	}
	if w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, csrf); w.Code != 409 {
		t.Fatalf("action without token must be rejected, got %d", w.Code)
	}
	if w := adminRequest(h, "GET", "/admin/ccr/link", "", cookie, csrf); w.Code != 409 {
		t.Fatalf("link without token must be rejected, got %d", w.Code)
	}
}

// 管理服务不可达是状态答案，不是错误：页面要能告诉管理员「去宿主机 ccr ui」。
func TestCCRStatusUnreachableIsNotAnError(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	port := strings.TrimPrefix(closed.URL, "http://127.0.0.1:")
	closed.Close()
	stub := &ccrStub{token: "tok", port: port}
	stub.path = filepath.Join(t.TempDir(), "service.json")
	stub.write(t)
	cfg := testConfig(core.URL)
	cfg.CCRURL = mustParseURL(t, "http://localhost:"+port)
	cfg.CCRTokenFile = stub.path
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("unreachable must be 200: %d %s", w.Code, w.Body.String())
	}
	var status struct {
		Enabled   bool `json:"enabled"`
		AuthReady bool `json:"auth_ready"`
		Reachable bool `json:"reachable"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if !status.Enabled || !status.AuthReady || status.Reachable {
		t.Fatalf("unexpected flags: %+v", status)
	}
	if w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, csrf); w.Code != 503 {
		t.Fatalf("unreachable action must be 503, got %d %s", w.Code, w.Body.String())
	}
}

func TestCCRGatewayActionPostsNamedMethod(t *testing.T) {
	state := "stopped"
	var mu sync.Mutex
	h, stub := newCCRStub(t, func(method string) (int, string) {
		switch method {
		case "getGatewayStatus":
			mu.Lock()
			defer mu.Unlock()
			return ccrStatusBody(state)
		case "startGateway":
			mu.Lock()
			state = "running"
			mu.Unlock()
			return 200, `{"ok":true,"value":{"state":"running"}}`
		case "stopGateway":
			mu.Lock()
			state = "stopped"
			mu.Unlock()
			return 200, `{"ok":true,"value":{"state":"stopped"}}`
		}
		return 404, `{"ok":false,"error":"not found"}`
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if len(stub.seen("startGateway")) != 1 {
		t.Fatalf("startGateway not posted: %+v", stub.calls)
	}
	var status struct {
		State string `json:"state"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "running" {
		t.Fatalf("start must return the refreshed state: %s", w.Body.String())
	}
	w = adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"stop"}`, cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	if len(stub.seen("stopGateway")) != 1 {
		t.Fatalf("stopGateway not posted: %+v", stub.calls)
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != "stopped" {
		t.Fatalf("stop must return the refreshed state: %s", w.Body.String())
	}
	for _, call := range stub.calls {
		if call.host != "127.0.0.1:"+stub.port || call.auth != stub.token {
			t.Fatalf("action call not authenticated over loopback Host: %+v", call)
		}
	}
}

func TestCCRGatewayActionRejectsBadInput(t *testing.T) {
	h, stub := newCCRStub(t, func(method string) (int, string) {
		if method == "getGatewayStatus" {
			return ccrStatusBody("running")
		}
		return 404, `{"ok":false,"error":"nope"}`
	})
	cookie, csrf := login(t, h)
	for _, body := range []string{`{"action":"restart"}`, `{"action":""}`, `{}`, `{"action":"start","extra":1}`, `not json`} {
		w := adminRequest(h, "POST", "/admin/ccr/gateway", body, cookie, csrf)
		if w.Code != 400 {
			t.Fatalf("body %s: expected 400, got %d %s", body, w.Code, w.Body.String())
		}
	}
	if got := stub.total(); got != 0 {
		t.Fatalf("rejected bodies must not reach CCR, saw %d calls: %+v", got, stub.calls)
	}
}

func TestCCRGatewayActionMapsRPCFailureTo502(t *testing.T) {
	h, _ := newCCRStub(t, func(method string) (int, string) {
		if method == "getGatewayStatus" {
			return ccrStatusBody("running")
		}
		return 500, `{"ok":false,"error":"gateway refused"}`
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"stop"}`, cookie, csrf)
	if w.Code != 502 {
		t.Fatalf("RPC-level failure: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "关停") {
		t.Fatalf("failure message must name the action: %s", w.Body.String())
	}
}

func TestCCRLinkPointsAtLoopbackWithCurrentToken(t *testing.T) {
	h, stub := newCCRStub(t, func(string) (int, string) { return ccrStatusBody("running") })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/ccr/link", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("link: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		UIURL string `json:"ui_url"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("link not JSON: %s", w.Body.String())
	}
	want := "http://127.0.0.1:" + stub.port + "/?ccr_web_token=" + stub.token
	if out.UIURL != want {
		t.Fatalf("link = %q, want %q", out.UIURL, want)
	}
}

func TestCCRConfigFromEnv(t *testing.T) {
	t.Setenv("WB2A_CORE_URL", "")
	t.Setenv("WB2A_ZCODE_URL", "")
	t.Setenv("WB2A_QODER_URL", "")
	t.Setenv("WB2A_OPENCODE_URL", "")
	t.Setenv("WB2A_KEY_FILE", "")
	t.Setenv("WB2A_CCR_URL", "")
	t.Setenv("WB2A_CCR_TOKEN_FILE", "")
	cfg, _, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CCRURL != nil || cfg.CCRTokenFile != "" {
		t.Fatalf("unset env must leave CCR disabled: %+v", cfg)
	}
	t.Setenv("WB2A_CCR_URL", "http://host.docker.internal:3458")
	t.Setenv("WB2A_CCR_TOKEN_FILE", "/run/ccr/service.json")
	cfg, _, err = configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CCRURL == nil || cfg.CCRURL.String() != "http://host.docker.internal:3458" || cfg.CCRTokenFile != "/run/ccr/service.json" {
		t.Fatalf("CCR env not read: %+v", cfg)
	}
	// 带查询的 URL 由 NewServer 拒绝：管理 Token 只能来自挂载的 service.json，不走配置。
	t.Setenv("WB2A_CCR_URL", "http://127.0.0.1:3458/?ccr_web_token=abc")
	cfg, _, err = configFromEnv()
	if err != nil {
		t.Fatalf("query-bearing URL parses fine at env level: %v", err)
	}
	if _, err = NewServer(cfg); err == nil || !strings.Contains(err.Error(), "WB2A_CCR_URL") {
		t.Fatalf("query-bearing CCR URL must fail with a named error, got %v", err)
	}
	if _, err = NewServer(Config{CoreURL: mustParseURL(t, "http://127.0.0.1:1"), AdminKey: strings.Repeat("a", 32), APIKey: strings.Repeat("p", 32), BridgeKey: strings.Repeat("b", 32), CCRURL: mustParseURL(t, "http://127.0.0.1:3458/x")}); err == nil || !strings.Contains(err.Error(), "WB2A_CCR_URL") {
		t.Fatalf("path-bearing CCR URL must fail with a named error, got %v", err)
	}
}

// CCR 管理服务重启后 Token 会轮换。挂的是单个文件时容器可能仍读到旧的一份，
// 这条路径必须报「Token 对不上」，不能报「请先执行 ccr ui」——服务明明在线。
func TestCCRStatusReportsTokenMismatchOn401(t *testing.T) {
	h, _ := newCCRStub(t, func(string) (int, string) { return 401, `{"ok":false,"error":"unauthorized"}` })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/ccr", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("token mismatch must still be a readable status: %d %s", w.Code, w.Body.String())
	}
	var status struct {
		AuthReady   bool `json:"auth_ready"`
		Reachable   bool `json:"reachable"`
		AuthMismatch bool `json:"auth_mismatch"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if !status.AuthReady || status.Reachable || !status.AuthMismatch {
		t.Fatalf("want auth_ready+mismatch, no reachable: %+v", status)
	}
}

func TestCCRGatewayActionRejectsStaleToken(t *testing.T) {
	h, _ := newCCRStub(t, func(method string) (int, string) {
		if method == "getGatewayStatus" {
			return ccrStatusBody("stopped")
		}
		return 401, `{"ok":false,"error":"unauthorized"}`
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/ccr/gateway", `{"action":"start"}`, cookie, csrf)
	if w.Code != 409 {
		t.Fatalf("stale token must be 409, got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Token") || strings.Contains(w.Body.String(), "ccr ui") {
		t.Fatalf("stale token message must not send the admin to ccr ui: %s", w.Body.String())
	}
}
