package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// testConsoleWithTrae 建一个带 trae sidecar stub 的 console；core 只答 /internal/v1/info。
func testConsoleWithTrae(t *testing.T, trae http.HandlerFunc) http.Handler {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	sidecar := httptest.NewServer(trae)
	t.Cleanup(sidecar.Close)
	cfg := testConfig(core.URL)
	cfg.TraeURL = mustParseURL(t, sidecar.URL)
	cfg.TraeKey = strings.Repeat("t", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TRAE 授权页在服务端（SSR）对 auth_callback_url 做形状校验：host 必须是字面量
// 127.0.0.1、path 必须是 /authorize（端口任意）。早期 console 发的是
// localhost:<port>/admin/trae/authorize，会让授权页返回空 SSR 数据并渲染
// 「登录失败 网络错误，请刷新页面重试。」。此测试锁定正确形状，防止回归。
func TestTraeCallbackURLShape(t *testing.T) {
	h := &server{}
	r := httptest.NewRequest("POST", "http://localhost:17863/admin/trae/login", nil)
	r.Host = "localhost:17863"
	if got := h.traeCallbackBase(r); got != "http://127.0.0.1:17863" {
		t.Fatalf("callback base = %q", got)
	}
	rewritten := h.rewriteTraeCallback(
		"https://www.trae.cn/authorization?client_id=x&auth_callback_url=http%3A%2F%2F127.0.0.1%3A18080%2Fauthorize", r)
	if !strings.Contains(rewritten, "auth_callback_url=http%3A%2F%2F127.0.0.1%3A17863%2Fauthorize") {
		t.Fatalf("rewritten login url = %q", rewritten)
	}
}

// Host 不带端口时回落默认端口，不能拼出 http://127.0.0.1:/authorize。
func TestTraeCallbackURLPortFallback(t *testing.T) {
	h := &server{}
	r := httptest.NewRequest("POST", "http://gateway.test/admin/trae/login", nil)
	r.Host = "gateway.test"
	if got := h.traeCallbackBase(r); got != "http://127.0.0.1:"+traeDefaultCallbackPort {
		t.Fatalf("callback base = %q", got)
	}
}

// 登录起始接口必须把 sidecar 的 127.0.0.1:18080/authorize 改写成 console 自己的
// /authorize 落点，并按约定注入桥接密钥。
func TestAdminTraeLoginStartRewritesCallback(t *testing.T) {
	var mu sync.Mutex
	var gotAuth string
	h := testConsoleWithTrae(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/api/login" {
			t.Errorf("sidecar path = %s", r.URL.Path)
		}
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		// 用 %s 而非 Fprint 的裸串：URL 编码里的 %2F 会被 go vet 的 printf 检查
		// 判成疑似格式化指令，导致镜像内 `go test ./...` 构建失败。
		const loginJSON = `{"login_url":"https://www.trae.cn/authorization?client_id=x&auth_callback_url=http%3A%2F%2F127.0.0.1%3A18080%2Fauthorize","pending_id":"p1","callback_url":"http://127.0.0.1:18080/authorize"}`
		fmt.Fprintf(w, "%s", loginJSON)
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/trae/login", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("login start: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		LoginURL    string `json:"login_url"`
		CallbackURL string `json:"callback_url"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer "+strings.Repeat("t", 32) {
		t.Fatalf("sidecar auth = %q", auth)
	}
	want := "http://127.0.0.1:" + traeDefaultCallbackPort + "/authorize"
	if out.CallbackURL != want {
		t.Fatalf("callback_url = %q, want %q", out.CallbackURL, want)
	}
	if !strings.Contains(out.LoginURL, "auth_callback_url="+url.QueryEscape(want)) {
		t.Fatalf("login_url callback not rewritten: %s", out.LoginURL)
	}
}

// 回调落在 console 根路径 /authorize，且不带管理会话也能到达处理器（TRAE 域跳转
// 不带 Cookie）；TraeURL 未配置时返回 409 而非 401/404。
func TestTraeAuthorizeRouteIsPublic(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	})
	w := adminRequest(h, "GET", "/authorize?refreshToken=x", "", nil, "")
	if w.Code != 409 {
		t.Fatalf("expected 409 from public /authorize, got %d: %s", w.Code, w.Body.String())
	}
}