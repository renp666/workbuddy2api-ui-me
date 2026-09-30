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

type opencodePair struct {
	coreHandler     http.HandlerFunc
	opencodeHandler http.HandlerFunc
}

func testConsoleWithOpenCode(t *testing.T, pair opencodePair) (http.Handler, Config) {
	t.Helper()
	core := httptest.NewServer(pair.coreHandler)
	t.Cleanup(core.Close)
	oc := httptest.NewServer(pair.opencodeHandler)
	t.Cleanup(oc.Close)
	cfg := testConfig(core.URL)
	cfg.OpenCodeURL = mustParseURL(t, oc.URL)
	cfg.OpenCodeKey = strings.Repeat("o", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func TestOpenCodePrefixRoutesToOpenCode(t *testing.T) {
	var coreHits, ocHits int
	var mu sync.Mutex
	var ocAuth, ocBody, ocAPIKey, ocOrigin, ocCookie string
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			ocHits++
			mu.Unlock()
			ocAuth = r.Header.Get("Authorization")
			ocAPIKey = r.Header.Get("X-Api-Key")
			ocOrigin = r.Header.Get("Origin")
			ocCookie = r.Header.Get("Cookie")
			body, _ := io.ReadAll(r.Body)
			ocBody = string(body)
			fmt.Fprint(w, `{"opencode":true}`)
		},
	})
	// opencode- 前缀分流到 opencode 上游；带 Origin（浏览器客户端）与部署 API Key 也不得泄漏。
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("opencode-OC · FreeModel")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	req.Header.Set("X-Api-Key", strings.Repeat("a", 32))
	req.Header.Set("Origin", "http://client.test")
	req.Header.Set("Cookie", "wb2a_admin=leaked")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if ocHits != 1 || coreHits != 0 {
		t.Fatalf("expected opencode=1 core=0, got opencode=%d core=%d", ocHits, coreHits)
	}
	// 专用密钥注入；OW Bridge 校验 Bearer 且拒绝任何带 Origin 的请求。
	if ocAuth != "Bearer "+strings.Repeat("o", 32) {
		t.Fatalf("opencode auth = %q", ocAuth)
	}
	if ocAPIKey != "" {
		t.Fatalf("X-Api-Key leaked to opencode: %q", ocAPIKey)
	}
	if ocOrigin != "" {
		t.Fatalf("Origin leaked to opencode: %q", ocOrigin)
	}
	if ocCookie != "" {
		t.Fatalf("admin cookie leaked to opencode: %q", ocCookie)
	}
	// 前缀在转发前剥离，剩余模型名与上游 /v1/models 列表条目一致。
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(ocBody), &envelope) != nil {
		t.Fatalf("opencode body not JSON: %s", ocBody)
	}
	if envelope.Model != "OC · FreeModel" {
		t.Fatalf("opencode model prefix not stripped: %q", envelope.Model)
	}
}

func TestOpenCodeNotConfiguredFallsToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	// OpenCodeURL nil：opencode- 模型原样落回 core，与未启用 overlay 时公共出口一致。
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("opencode-OC · FreeModel")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMergedModelsIncludesOpenCode(t *testing.T) {
	var mu sync.Mutex
	var ocAuth string
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:workbuddy","realm":"cn"}]}`)
				return
			}
			fmt.Fprint(w, `{"core":true}`)
		},
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				mu.Lock()
				ocAuth = r.Header.Get("Authorization")
				mu.Unlock()
				// OW Bridge 返回的条目 id 形如「OC · <名称>」。
				fmt.Fprint(w, `{"object":"list","data":[{"id":"OC · Free","object":"model","owned_by":"opencode"}]}`)
				return
			}
			fmt.Fprint(w, `{"opencode":true}`)
		},
	})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	auth := ocAuth
	mu.Unlock()
	// OW Bridge 所有路由（含 /v1/models）都需要 Bearer 鉴权。
	if auth != "Bearer "+strings.Repeat("o", 32) {
		t.Fatalf("merged models opencode auth = %q", auth)
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
		if m.ID == "opencode-OC · Free" && m.Realm == "opencode" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("opencode model not found in merged list: %+v", result.Data)
	}
}

func TestAdminOpenCodeStatusWithoutSessionRejected(t *testing.T) {
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler:     func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"core":true}`) },
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"opencode":true}`) },
	})
	req := httptest.NewRequest("GET", "/admin/opencode", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestAdminOpenCodeStatusDisabled(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	})
	cookie, _ := login(t, h)
	w := adminRequest(h, "GET", "/admin/opencode", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if status.Enabled {
		t.Fatalf("channel must be disabled: %s", w.Body.String())
	}
}

func TestAdminOpenCodeStatusEnabled(t *testing.T) {
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/models":
				fmt.Fprint(w, `{"object":"list","data":[{"id":"OC · Free"},{"id":"OC · Fast"}]}`)
			case "/health":
				fmt.Fprint(w, `{"phase":"ready","version":"0.2.0"}`)
			default:
				fmt.Fprint(w, `{"opencode":true}`)
			}
		},
	})
	cookie, _ := login(t, h)
	w := adminRequest(h, "GET", "/admin/opencode", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var status struct {
		Enabled    bool `json:"enabled"`
		Reachable  bool `json:"reachable"`
		ModelCount int  `json:"model_count"`
		Models     []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"models"`
		Health struct {
			Phase   string `json:"phase"`
			Version string `json:"version"`
		} `json:"health"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", w.Body.String())
	}
	if !status.Enabled || !status.Reachable || status.ModelCount != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, m := range status.Models {
		if !strings.HasPrefix(m.ID, "opencode-") {
			t.Fatalf("model missing prefix: %q", m.ID)
		}
		if m.Realm != "opencode" {
			t.Fatalf("model missing realm: %q", m.Realm)
		}
	}
	// 只读页签借用 OW Bridge /health 的运行状态（phase/version）。
	if status.Health.Phase != "ready" || status.Health.Version != "0.2.0" {
		t.Fatalf("health passthrough missing: %+v", status.Health)
	}
}

func TestAdminOpenCodeChatRejectsNonPrefix(t *testing.T) {
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("opencode should not be hit")
		},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/opencode/chat", chatBody("cn:workbuddy"), cookie, csrf)
	if w.Code != 400 {
		t.Fatalf("expected 400 for non-opencode model, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminOpenCodeSessionFlag(t *testing.T) {
	h, _ := testConsoleWithOpenCode(t, opencodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if !mockInfo(w, r) {
				fmt.Fprint(w, `{"core":true}`)
			}
		},
		opencodeHandler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"opencode":true}`) },
	})
	cookie, _ := login(t, h)
	w := adminRequest(h, "GET", "/admin/session", "", cookie, "")
	if w.Code != 200 {
		t.Fatalf("session: %d %s", w.Code, w.Body.String())
	}
	var session struct {
		OpenCodeEnabled bool `json:"opencode_enabled"`
	}
	if json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatalf("session not JSON: %s", w.Body.String())
	}
	if !session.OpenCodeEnabled {
		t.Fatalf("opencode_enabled must be true: %s", w.Body.String())
	}
}

func TestOpenCodeEnvironmentConfig(t *testing.T) {
	t.Setenv("WB2A_ADMIN_KEY", strings.Repeat("a", 32))
	t.Setenv("WB2A_API_KEY", "source-api")
	t.Setenv("WB2A_BRIDGE_KEY", strings.Repeat("b", 32))
	t.Setenv("WB2A_OPENCODE_URL", "http://127.0.0.1:41980")
	t.Setenv("WB2A_OPENCODE_KEY", strings.Repeat("o", 32))
	cfg, _, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OpenCodeURL == nil || cfg.OpenCodeURL.String() != "http://127.0.0.1:41980" {
		t.Fatalf("WB2A_OPENCODE_URL not parsed: %+v", cfg.OpenCodeURL)
	}
	if cfg.OpenCodeKey != strings.Repeat("o", 32) {
		t.Fatalf("WB2A_OPENCODE_KEY not parsed: %q", cfg.OpenCodeKey)
	}
}
