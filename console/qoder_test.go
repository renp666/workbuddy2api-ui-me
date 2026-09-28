package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type qoderPair struct {
	coreHandler   http.HandlerFunc
	qoderHandler  http.HandlerFunc
	controlHandler http.HandlerFunc // 非 nil 时挂载模拟 qoder-login-ctl 到 QoderControlURL
}

func testConsoleWithQoder(t *testing.T, pair qoderPair) (http.Handler, Config) {
	t.Helper()
	core := httptest.NewServer(pair.coreHandler)
	t.Cleanup(core.Close)
	qoder := httptest.NewServer(pair.qoderHandler)
	t.Cleanup(qoder.Close)
	cfg := testConfig(core.URL)
	cfg.QoderURL = mustParseURL(t, qoder.URL)
	cfg.QoderKey = strings.Repeat("q", 32)
	if pair.controlHandler != nil {
		ctl := httptest.NewServer(pair.controlHandler)
		t.Cleanup(ctl.Close)
		cfg.QoderControlURL = mustParseURL(t, ctl.URL)
	}
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func TestQoderPrefixRoutesToQoder(t *testing.T) {
	var coreHits, qoderHits int
	var mu sync.Mutex
	var qoderAuth, qoderBody, qoderAPIKey string
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			qoderHits++
			mu.Unlock()
			qoderAuth = r.Header.Get("Authorization")
			qoderAPIKey = r.Header.Get("X-Api-Key")
			body, _ := io.ReadAll(r.Body)
			qoderBody = string(body)
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	// qoder- prefix routes to qoder
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("qoder-qwen3.8-max")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	req.Header.Set("X-Api-Key", strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if qoderHits != 1 || coreHits != 0 {
		t.Fatalf("expected qoder=1 core=0, got qoder=%d core=%d", qoderHits, coreHits)
	}
	mu.Unlock()
	// key injected, API key stripped
	if qoderAuth != "Bearer "+strings.Repeat("q", 32) {
		t.Fatalf("qoder auth = %q", qoderAuth)
	}
	if qoderAPIKey != "" {
		t.Fatalf("X-Api-Key leaked to qoder: %q", qoderAPIKey)
	}
	// prefix stripped in body
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(qoderBody), &envelope) != nil {
		t.Fatalf("qoder body not JSON: %s", qoderBody)
	}
	if envelope.Model != "qwen3.8-max" {
		t.Fatalf("qoder model prefix not stripped: %q", envelope.Model)
	}
}

func TestQoderPrefixDisabledFallsToCore(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("qoder should not be hit")
		},
	})
	// non-qoder model goes to core
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("cn:workbuddy")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestQoderNotConfiguredFallsToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	// QoderURL nil
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("qoder-qwen3.8-max")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMergedModelsIncludesQoder(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:workbuddy","realm":"cn"}]}`)
				return
			}
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-max"}]}`)
				return
			}
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Data []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &result) != nil {
		t.Fatalf("response not JSON: %s", rec.Body.String())
	}
	found := false
	for _, m := range result.Data {
		if m.ID == "qoder-qwen3.8-max" && m.Realm == "qoder" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("qoder model not found in merged list: %+v", result.Data)
	}
}

func TestAdminQoderStatusDisabled(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/admin/qoder", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// without admin session, expect 401
	if rec.Code != 401 {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestAdminQoderStatusEnabled(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-max"},{"id":"qwen3.8-pro"}]}`)
				return
			}
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	// login to get session
	cookie, _ := login(t, h)
	// get qoder status
	req := httptest.NewRequest("GET", "/admin/qoder", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var status struct {
		Enabled   bool `json:"enabled"`
		Reachable bool `json:"reachable"`
		ModelCount int `json:"model_count"`
		Models    []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"models"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", rec.Body.String())
	}
	if !status.Enabled || !status.Reachable || status.ModelCount != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, m := range status.Models {
		if !strings.HasPrefix(m.ID, "qoder-") {
			t.Fatalf("model missing prefix: %q", m.ID)
		}
		if m.Realm != "qoder" {
			t.Fatalf("model missing realm: %q", m.Realm)
		}
	}
}

func TestAdminQoderChatRejectsNonPrefix(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("qoder should not be hit")
		},
	})
	// login
	cookie, csrf := login(t, h)
	// chat with non-prefixed model
	w := adminRequest(h, "POST", "/admin/qoder/chat", chatBody("cn:workbuddy"), cookie, csrf)
	if w.Code != 400 {
		t.Fatalf("expected 400 for non-qoder model, got %d: %s", w.Code, w.Body.String())
	}
}

// qoderControlFixture 记录 console 对控制端的调用，验证注入头与防泄漏。
func qoderControlFixture(t *testing.T, routes map[string]map[string]map[string]any) (http.Handler, *sync.Mutex, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	models := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-flash"}]}`)
			return
		}
		fmt.Fprint(w, `{"qoder":true}`)
	}
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler:  func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"core":true}`) },
		qoderHandler: models,
		controlHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.Method+" "+r.URL.Path)
			// 控制端只应收到自定义头与 Bearer，不应有管理 Cookie。
			if got := r.Header.Get("X-Qoder-Ctl"); got != "1" {
				t.Errorf("missing X-Qoder-Ctl header on %s: %q", r.URL.Path, got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("q", 32) {
				t.Errorf("ctl auth = %q", got)
			}
			if got := r.Header.Get("Cookie"); got != "" {
				t.Errorf("admin cookie leaked to ctl: %q", got)
			}
			mu.Unlock()
			byMethod, ok := routes[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":"not found"}`)
				return
			}
			out, ok := byMethod[r.Method]
			if !ok {
				w.WriteHeader(405)
				return
			}
			if code, ok := out["code"].(int); ok && code != 200 {
				w.WriteHeader(code)
			}
			_ = json.NewEncoder(w).Encode(out["body"])
		},
	})
	return h, &mu, &paths
}

func TestAdminQoderLoginFlow(t *testing.T) {
	h, mu, paths := qoderControlFixture(t, map[string]map[string]map[string]any{
		"/login/start": {"POST": {"body": map[string]any{"state": "waiting", "auth_url": "https://qoder.cn/device/selectAccounts?x=1"}}},
		"/login":       {"GET": {"body": map[string]any{"state": "success", "auth_url": "https://qoder.cn/device/selectAccounts?x=1"}}},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/qoder/login", "{}", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	var start struct {
		State   string `json:"state"`
		AuthURL string `json:"auth_url"`
	}
	if json.Unmarshal(w.Body.Bytes(), &start) != nil || start.AuthURL == "" {
		t.Fatalf("bad start response: %s", w.Body.String())
	}
	w = adminRequest(h, "GET", "/admin/qoder/login", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("poll = %d: %s", w.Code, w.Body.String())
	}
	var poll struct {
		State string `json:"state"`
	}
	if json.Unmarshal(w.Body.Bytes(), &poll) != nil || poll.State != "success" {
		t.Fatalf("bad poll response: %s", w.Body.String())
	}
	mu.Lock()
	got := strings.Join(*paths, ",")
	mu.Unlock()
	if !strings.Contains(got, "POST /login/start") || !strings.Contains(got, "GET /login") {
		t.Fatalf("ctl calls = %q", got)
	}
}

func TestAdminQoderStatusMergeLoggedIn(t *testing.T) {
	h, _, _ := qoderControlFixture(t, map[string]map[string]map[string]any{
		"/status": {"GET": {"body": map[string]any{"ok": true, "status": map[string]any{"logged_in": true, "version": "1.1.64"}}}},
	})
	cookie, _ := login(t, h)
	w := adminRequest(h, "GET", "/admin/qoder", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Control  *bool `json:"control"`
		LoggedIn *bool `json:"logged_in"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Control == nil || out.LoggedIn == nil {
		t.Fatalf("missing control fields: %s", w.Body.String())
	}
	if !*out.Control || !*out.LoggedIn {
		t.Fatalf("control=%v logged_in=%v", *out.Control, *out.LoggedIn)
	}
}

func TestAdminQoderControlUnreachableDegrades(t *testing.T) {
	// 控制端指向一个立即关闭的端口：状态接口不报错，control=false。
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	qoder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[]}`)
			return
		}
		fmt.Fprint(w, `{"qoder":true}`)
	}))
	qoder.Close() // 立即关闭：数据面与控制面都不可达
	cfg := testConfig(core.URL)
	cfg.QoderURL = mustParseURL(t, qoder.URL)
	cfg.QoderControlURL = mustParseURL(t, qoder.URL)
	cfg.QoderKey = strings.Repeat("q", 32)
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := login(t, srv)
	w := adminRequest(srv, "GET", "/admin/qoder", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Enabled bool `json:"enabled"`
		Control bool `json:"control"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("bad json: %s", w.Body.String())
	}
	if !out.Enabled || out.Control {
		t.Fatalf("expected enabled+control=false, got %+v", out)
	}
}

func TestAdminQoderLogout(t *testing.T) {
	h, mu, paths := qoderControlFixture(t, map[string]map[string]map[string]any{
		"/logout": {"POST": {"body": map[string]any{"removed": []string{"/root/.qoderworkcn/auth.json"}}}},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/qoder/logout", "{}", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("logout = %d: %s", w.Code, w.Body.String())
	}
	mu.Lock()
	got := strings.Join(*paths, ",")
	mu.Unlock()
	if !strings.Contains(got, "POST /logout") {
		t.Fatalf("ctl calls = %q", got)
	}
}

func TestAdminQoderLoginWithoutControlRejected(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler:  func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"core":true}`) },
		qoderHandler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"qoder":true}`) },
	})
	cookie, csrf := login(t, h)
	for _, tc := range []struct {
		method, path, body string
	}{
		{"POST", "/admin/qoder/login", "{}"},
		{"GET", "/admin/qoder/login", ""},
		{"POST", "/admin/qoder/logout", "{}"},
	} {
		w := adminRequest(h, tc.method, tc.path, tc.body, cookie, csrf)
		if w.Code != 409 {
			t.Fatalf("%s %s = %d, want 409", tc.method, tc.path, w.Code)
		}
	}
}
