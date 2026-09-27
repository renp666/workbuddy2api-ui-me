package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type zcodePair struct {
	coreHandler    http.HandlerFunc
	zcodeHandler   http.HandlerFunc
	controlHandler http.HandlerFunc
}

func testConsoleWithZcode(t *testing.T, pair zcodePair) (http.Handler, Config) {
	t.Helper()
	core := httptest.NewServer(pair.coreHandler)
	t.Cleanup(core.Close)
	zcode := httptest.NewServer(pair.zcodeHandler)
	t.Cleanup(zcode.Close)
	cfg := testConfig(core.URL)
	cfg.ZCodeURL = mustParseURL(t, zcode.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	if pair.controlHandler != nil {
		control := httptest.NewServer(pair.controlHandler)
		t.Cleanup(control.Close)
		cfg.ZCodeControlURL = mustParseURL(t, control.URL)
	}
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func chatBody(model string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
}

func TestGlmPrefixRoutesToZCode(t *testing.T) {
	var coreHits, zcodeHits int
	var mu sync.Mutex
	var zcodeAuth, zcodeBody, zcodeAPIKey string
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			zcodeHits++
			zcodeAuth = r.Header.Get("Authorization")
			zcodeAPIKey = r.Header.Get("X-Api-Key")
			body, _ := io.ReadAll(r.Body)
			zcodeBody = string(body)
			mu.Unlock()
			fmt.Fprint(w, `{"zcode":true}`)
		},
	})
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		r := httptest.NewRequest("POST", "http://console.test"+path, strings.NewReader(chatBody("glm-5.2")))
		r.Header.Set("Authorization", "Bearer client-api-key")
		r.Header.Set("x-api-key", "client-api-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"zcode":true}` {
			t.Fatalf("%s routed wrong: %d %s", path, w.Code, w.Body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if zcodeHits != 3 || coreHits != 0 {
		t.Fatalf("hits core=%d zcode=%d", coreHits, zcodeHits)
	}
	if zcodeAuth != "Bearer "+strings.Repeat("z", 32) {
		t.Errorf("zcode auth=%q", zcodeAuth)
	}
	if zcodeAPIKey != "" {
		t.Errorf("deployment api key leaked to zcode as x-api-key: %q", zcodeAPIKey)
	}
	if zcodeBody != chatBody("glm-5.2") {
		t.Errorf("zcode body=%q", zcodeBody)
	}
}

func TestNamespacedCoreModelsStayOnCore(t *testing.T) {
	// "cn:glm-5.2" is a core model id; prefix matching must not capture it.
	var coreHits, zcodeHits int
	var mu sync.Mutex
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			zcodeHits++
			mu.Unlock()
			t.Errorf("unexpected zcode hit")
		},
	})
	for _, model := range []string{"cn:glm-5.2", "global:gpt-5.4", ""} {
		r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", strings.NewReader(chatBody(model)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"core":true}` {
			t.Fatalf("model %q routed wrong: %d %s", model, w.Code, w.Body)
		}
	}
	// Malformed JSON also falls through to core.
	r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", strings.NewReader(`not-json`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("malformed body: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if coreHits != 4 || zcodeHits != 0 {
		t.Fatalf("hits core=%d zcode=%d", coreHits, zcodeHits)
	}
}

func TestOtherPublicPathsStayOnCore(t *testing.T) {
	var coreHits int
	var mu sync.Mutex
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected zcode hit: %s", r.URL) },
	})
	for _, path := range []string{"/v1/embeddings", "/status", "/healthz"} {
		r := httptest.NewRequest("GET", "http://console.test"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if coreHits != 3 {
		t.Fatalf("core hits=%d", coreHits)
	}
}

func TestMergedModels(t *testing.T) {
	coreList := `{"object":"list","data":[{"id":"cn:workbuddy"},{"id":"glm-duplicate"}]}`
	zcodeList := `{"object":"list","data":[{"id":"glm-5.2"},{"id":"glm-duplicate"}]}`
	var zcodeAuth string
	var mu sync.Mutex
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, coreList)
				return
			}
			t.Errorf("unexpected core path %s", r.URL.Path)
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			zcodeAuth = r.Header.Get("Authorization")
			mu.Unlock()
			fmt.Fprint(w, zcodeList)
		},
	})
	r := httptest.NewRequest("GET", "http://console.test/v1/models", nil)
	r.Header.Set("Authorization", "Bearer client-api-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	var envelope struct {
		Object string `json:"object"`
		Data   []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Object != "list" {
		t.Fatalf("envelope %s", w.Body)
	}
	ids := make([]string, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		ids = append(ids, item.ID)
	}
	want := []string{"cn:workbuddy", "glm-duplicate", "glm-5.2"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("merged ids=%v want %v", ids, want)
	}
	// glm-* entries come from a third-party upstream whose list carries no realm;
	// console must tag them "glm" so callers can tell the platform apart. Core
	// entries keep whatever realm core assigned (here none in the fake list).
	realms := map[string]string{}
	for _, item := range envelope.Data {
		realms[item.ID] = item.Realm
	}
	if realms["glm-duplicate"] != "glm" || realms["glm-5.2"] != "glm" {
		t.Fatalf("glm realm tags = %q/%q want glm/glm", realms["glm-duplicate"], realms["glm-5.2"])
	}
	if _, ok := realms["cn:workbuddy"]; !ok {
		t.Fatalf("core entry missing from merged list")
	}
	mu.Lock()
	defer mu.Unlock()
	if zcodeAuth != "Bearer "+strings.Repeat("z", 32) {
		t.Errorf("zcode models auth=%q", zcodeAuth)
	}
}

func TestMergedModelsDegradesAndPassesThrough(t *testing.T) {
	coreList := `{"object":"list","data":[{"id":"cn:workbuddy"}]}`
	t.Run("zcode down degrades to core", func(t *testing.T) {
		zcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		zcodeURL := zcode.URL
		zcode.Close()
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, coreList) }))
		t.Cleanup(core.Close)
		cfg := testConfig(core.URL)
		cfg.ZCodeURL = mustParseURL(t, zcodeURL)
		cfg.ZCodeKey = strings.Repeat("z", 32)
		h, err := NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		w := adminRequest(h, "GET", "/v1/models", "", nil, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "cn:workbuddy") || strings.Contains(w.Body.String(), "glm-") {
			t.Fatalf("degraded models: %d %s", w.Code, w.Body)
		}
	})
	t.Run("core auth error passes through", func(t *testing.T) {
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
			},
			zcodeHandler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
		})
		w := adminRequest(h, "GET", "/v1/models", "", nil, "")
		if w.Code != 401 || !strings.Contains(w.Body.String(), "invalid api key") {
			t.Fatalf("auth passthrough: %d %s", w.Code, w.Body)
		}
	})
	t.Run("core unreachable is 503", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		deadURL := dead.URL
		dead.Close()
		zcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.2"}]}`)
		}))
		t.Cleanup(zcode.Close)
		cfg := testConfig(deadURL)
		cfg.ZCodeURL = mustParseURL(t, zcode.URL)
		cfg.ZCodeKey = strings.Repeat("z", 32)
		h, err := NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		w := adminRequest(h, "GET", "/v1/models", "", nil, "")
		if w.Code != 503 {
			t.Fatalf("core down: %d %s", w.Code, w.Body)
		}
	})
}

func TestZCodeDisabledKeepsPlainProxy(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"core":true}`) })
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/chat/completions", chatBody("glm-5.2")},
		{"POST", "/v1/messages", chatBody("glm-5.2")},
		{"GET", "/v1/models", ""},
	} {
		r := httptest.NewRequest(tc.method, "http://console.test"+tc.path, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"core":true}` {
			t.Fatalf("%s with zcode disabled: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestZCodeBodyLimit(t *testing.T) {
	var coreHits, zcodeHits int
	var mu sync.Mutex
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			zcodeHits++
			mu.Unlock()
			t.Errorf("oversized body must not reach zcode")
		},
	})
	oversized := `{"model":"glm-5.2","pad":"` + strings.Repeat("x", zcodeRouteBodyLimit) + `"}`
	r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", strings.NewReader(oversized))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("oversized: %d", w.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if coreHits != 0 || zcodeHits != 0 {
		t.Fatalf("hits core=%d zcode=%d", coreHits, zcodeHits)
	}
}

func TestAdminZcodeDisabled(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled status: %d %s", w.Code, w.Body)
	}
	w = adminRequest(h, "POST", "/admin/zcode/chat", chatBody("glm-5.2"), cookie, csrf)
	if w.Code != 409 {
		t.Fatalf("disabled chat: %d %s", w.Code, w.Body)
	}
	if w := adminRequest(h, "GET", "/admin/session", "", cookie, csrf); !strings.Contains(w.Body.String(), `"zcode_enabled":false`) {
		t.Fatalf("session flag: %s", w.Body)
	}
}

func TestAdminZcodeStatus(t *testing.T) {
	var zcodeAuth string
	var mu sync.Mutex
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			zcodeAuth = r.Header.Get("Authorization")
			mu.Unlock()
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.2"},{"id":"glm-4.7"}]}`)
		},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var status struct {
		Enabled    bool                `json:"enabled"`
		Reachable  bool                `json:"reachable"`
		ModelCount int                 `json:"model_count"`
		Models     []map[string]string `json:"models"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.Enabled || !status.Reachable || status.ModelCount != 2 {
		t.Fatalf("status body %s", w.Body)
	}
	if len(status.Models) != 2 || status.Models[0]["id"] != "glm-5.2" || status.Models[1]["id"] != "glm-4.7" {
		t.Fatalf("models %v", status.Models)
	}
	mu.Lock()
	defer mu.Unlock()
	if zcodeAuth != "Bearer "+strings.Repeat("z", 32) {
		t.Errorf("status probe auth=%q", zcodeAuth)
	}
	if w := adminRequest(h, "GET", "/admin/session", "", cookie, csrf); !strings.Contains(w.Body.String(), `"zcode_enabled":true`) {
		t.Fatalf("session flag: %s", w.Body)
	}
}

func TestAdminZcodeStatusUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) }))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	cfg.ZCodeURL = mustParseURL(t, deadURL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"reachable":false`) {
		t.Fatalf("unreachable status: %d %s", w.Code, w.Body)
	}
}

func TestAdminZcodeChatProxies(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth, gotCookie, gotCSRF, gotAPIKey, gotBody string
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if !mockInfo(w, r) {
				t.Errorf("admin zcode chat must not touch core: %s", r.URL.Path)
			}
		},
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			gotCookie = r.Header.Get("Cookie")
			gotCSRF = r.Header.Get("X-CSRF-Token")
			gotAPIKey = r.Header.Get("X-Api-Key")
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			mu.Unlock()
			fmt.Fprint(w, `{"zcode":true}`)
		},
	})
	cookie, csrf := login(t, h)
	r := httptest.NewRequest("POST", "http://console.test/admin/zcode/chat", strings.NewReader(chatBody("glm-5.2")))
	r.Header.Set("Origin", "http://console.test")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(cookie)
	r.Header.Set("x-api-key", "deployment-api-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != `{"zcode":true}` {
		t.Fatalf("chat proxy: %d %s", w.Code, w.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path=%q", gotPath)
	}
	if gotAuth != "Bearer "+strings.Repeat("z", 32) {
		t.Errorf("upstream auth=%q", gotAuth)
	}
	if gotBody != chatBody("glm-5.2") {
		t.Errorf("upstream body=%q", gotBody)
	}
	if gotCookie != "" || gotCSRF != "" {
		t.Errorf("management credentials leaked to zcode: cookie=%q csrf=%q", gotCookie, gotCSRF)
	}
	if gotAPIKey != "" {
		t.Errorf("deployment api key leaked to zcode: %q", gotAPIKey)
	}
}

func TestAdminZcodeChatRejectsNonGlm(t *testing.T) {
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:  func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) { t.Errorf("rejected body must not reach zcode") },
	})
	cookie, csrf := login(t, h)
	for _, body := range []string{chatBody("cn:glm-5.2"), chatBody("other-model"), `not-json`, ""} {
		w := adminRequest(h, "POST", "/admin/zcode/chat", body, cookie, csrf)
		if w.Code != 400 {
			t.Fatalf("body %q: %d %s", body, w.Code, w.Body)
		}
	}
}

func TestAdminZcodeChatRequiresAdminSession(t *testing.T) {
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:  func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) { t.Errorf("unauthenticated request reached zcode") },
	})
	if w := adminRequest(h, "GET", "/admin/zcode", "", nil, ""); w.Code != 401 {
		t.Fatalf("status without session: %d", w.Code)
	}
	cookie, _ := login(t, h)
	if w := adminRequest(h, "POST", "/admin/zcode/chat", chatBody("glm-5.2"), cookie, ""); w.Code != 403 {
		t.Fatalf("chat without csrf: %d", w.Code)
	}
}

func TestZCodeConfigValidation(t *testing.T) {
	t.Setenv("WB2A_KEY_FILE", "")
	t.Setenv("WB2A_CORE_URL", "http://core:7863")
	t.Setenv("WB2A_ZCODE_URL", "")
	t.Setenv("WB2A_ZCODE_CONTROL_URL", "")
	cfg, _, err := configFromEnv()
	if err != nil || cfg.ZCodeURL != nil || cfg.ZCodeControlURL != nil {
		t.Fatalf("empty zcode url: %+v %+v %v", cfg.ZCodeURL, cfg.ZCodeControlURL, err)
	}
	t.Setenv("WB2A_ZCODE_URL", "http://zcode-proxy:8080")
	t.Setenv("WB2A_ZCODE_KEY", "zk")
	t.Setenv("WB2A_ZCODE_CONTROL_URL", "http://127.0.0.1:9090")
	cfg, _, err = configFromEnv()
	if err != nil || cfg.ZCodeURL == nil || cfg.ZCodeURL.Host != "zcode-proxy:8080" || cfg.ZCodeKey != "zk" || cfg.ZCodeControlURL == nil || cfg.ZCodeControlURL.Host != "127.0.0.1:9090" {
		t.Fatalf("zcode env: %+v %v", cfg, err)
	}
	t.Setenv("WB2A_ZCODE_URL", "http://%zz")
	if _, _, err := configFromEnv(); err == nil {
		t.Fatal("invalid WB2A_ZCODE_URL accepted")
	}
	t.Setenv("WB2A_ZCODE_URL", "http://zcode-proxy:8080")
	t.Setenv("WB2A_ZCODE_CONTROL_URL", "http://%zz")
	if _, _, err := configFromEnv(); err == nil {
		t.Fatal("invalid WB2A_ZCODE_CONTROL_URL accepted")
	}
	cfg = testConfig("http://core:7863")
	bad, _ := url.Parse("http://zcode:8080/path")
	cfg.ZCodeURL = bad
	if _, err := NewServer(cfg); err == nil {
		t.Fatal("zcode url with path accepted")
	}
	cfg = testConfig("http://core:7863")
	cfg.ZCodeURL = mustParseURL(t, "http://zcode:8080")
	cfg.ZCodeControlURL = bad
	if _, err := NewServer(cfg); err == nil {
		t.Fatal("zcode control url with path accepted")
	}
}

// fakeControl records the control command sequence and answers each cmd from a canned map.
type fakeControl struct {
	mu     sync.Mutex
	bodies []string
	resp   map[string]string
}

func (f *fakeControl) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var cmd struct {
		Cmd string `json:"cmd"`
	}
	_ = json.Unmarshal(body, &cmd)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	out, ok := f.resp[cmd.Cmd]
	if !ok {
		out = `{"ok":false,"error":"unexpected_cmd"}`
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, out)
}

func (f *fakeControl) saw(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bodies {
		if strings.Contains(b, prefix) {
			return true
		}
	}
	return false
}

const zcodeStatusLoggedIn = `{"ok":true,"state":"running","provider":"bigmodel","plan":"coding-plan","proxyPort":8080,"loggedIn":true}`

func deadControlURL(t *testing.T) *url.URL {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(dead.Close)
	u := dead.URL
	dead.Close()
	return mustParseURL(t, u)
}

func TestAdminZcodeStatusWithControl(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{"status": zcodeStatusLoggedIn}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.2"}]}`)
		},
		controlHandler: ctrl.handler,
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var status struct {
		Enabled      bool   `json:"enabled"`
		Control      bool   `json:"control"`
		LoggedIn     bool   `json:"logged_in"`
		ProxyRunning bool   `json:"proxy_running"`
		Provider     string `json:"provider"`
		Plan         string `json:"plan"`
		Reachable    bool   `json:"reachable"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status body %s", w.Body)
	}
	if !status.Enabled || !status.Control || !status.LoggedIn || !status.ProxyRunning || !status.Reachable {
		t.Fatalf("status flags %s", w.Body)
	}
	if status.Provider != "bigmodel" || status.Plan != "coding-plan" {
		t.Fatalf("status config %s", w.Body)
	}
	if !ctrl.saw(`"cmd":"status"`) {
		t.Fatalf("control never asked for status: %v", ctrl.bodies)
	}
}

func TestAdminZcodeStatusControlDown(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) }))
	t.Cleanup(core.Close)
	zcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) }))
	t.Cleanup(zcode.Close)
	cfg := testConfig(core.URL)
	cfg.ZCodeURL = mustParseURL(t, zcode.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	cfg.ZCodeControlURL = deadControlURL(t)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"control":true`) || !strings.Contains(w.Body.String(), `"logged_in":false`) {
		t.Fatalf("control down status: %d %s", w.Code, w.Body)
	}
}

func TestAdminZcodeStatusWithoutControlKeepsShape(t *testing.T) {
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.2"}]}`)
		},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"control":`) {
		t.Fatalf("shape without control config: %d %s", w.Code, w.Body)
	}
}

func TestAdminZcodeControlActions(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{
		"startOAuth": `{"ok":true,"event":"oauthUrl","authorizeUrl":"https://bigmodel.cn/login?state=abc","callbackPort":0}`,
		"startProxy": `{"ok":true,"event":"proxyStarted","port":8080}`,
		"stopProxy":  `{"ok":true,"event":"proxyStopped"}`,
		"logout":     `{"ok":true,"event":"loggedOut"}`,
	}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
		controlHandler: ctrl.handler,
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/zcode/login", `{"provider":"bigmodel"}`, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"authorize_url":"https://bigmodel.cn/login?state=abc"`) {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if !ctrl.saw(`"cmd":"startOAuth"`) || !ctrl.saw(`"provider":"bigmodel"`) {
		t.Fatalf("login not forwarded: %v", ctrl.bodies)
	}
	for _, path := range []string{"/admin/zcode/enable", "/admin/zcode/disable", "/admin/zcode/logout"} {
		w = adminRequest(h, "POST", path, "", cookie, csrf)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if !ctrl.saw(`"cmd":"startProxy"`) || !ctrl.saw(`"cmd":"stopProxy"`) || !ctrl.saw(`"cmd":"logout"`) {
		t.Fatalf("actions not forwarded: %v", ctrl.bodies)
	}
}

func TestAdminZcodeLoginSyncsProviderViaSetConfig(t *testing.T) {
	t.Run("provider mismatch triggers setConfig before startOAuth", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{
			"status":     `{"ok":true,"state":"stopped","provider":"bigmodel","plan":"coding-plan","proxyPort":0,"loggedIn":false}`,
			"setConfig":  `{"ok":true,"event":"configUpdated","provider":"zai","plan":"coding-plan"}`,
			"startOAuth": `{"ok":true,"event":"oauthUrl","authorizeUrl":"https://x","callbackPort":0}`,
		}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/login", `{"provider":"zai"}`, cookie, csrf); w.Code != 200 {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		if !ctrl.saw(`"cmd":"setConfig"`) || !ctrl.saw(`"provider":"zai"`) {
			t.Fatalf("provider switch not forwarded: %v", ctrl.bodies)
		}
	})
	t.Run("running proxy refuses the switch with 409", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{
			"status":    `{"ok":true,"state":"running","provider":"bigmodel","plan":"coding-plan","proxyPort":8080,"loggedIn":true}`,
			"setConfig": `{"ok":false,"error":"stop_proxy_first"}`,
		}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/login", `{"provider":"zai"}`, cookie, csrf); w.Code != 409 {
			t.Fatalf("stop_proxy_first: %d %s", w.Code, w.Body)
		}
		if ctrl.saw(`"cmd":"startOAuth"`) {
			t.Fatalf("startOAuth ran after a failed switch: %v", ctrl.bodies)
		}
	})
	t.Run("same provider skips setConfig", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{
			"status":     `{"ok":true,"state":"stopped","provider":"bigmodel","plan":"coding-plan","proxyPort":0,"loggedIn":false}`,
			"startOAuth": `{"ok":true,"event":"oauthUrl","authorizeUrl":"https://x","callbackPort":0}`,
		}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/login", `{"provider":"bigmodel"}`, cookie, csrf); w.Code != 200 {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		if ctrl.saw(`"cmd":"setConfig"`) {
			t.Fatalf("setConfig fired for the same provider: %v", ctrl.bodies)
		}
	})
}

func TestAdminZcodeConfig(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{
		"setConfig": `{"ok":true,"event":"configUpdated","provider":"bigmodel","plan":"start-plan"}`,
	}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
		controlHandler: ctrl.handler,
	})
	cookie, csrf := login(t, h)
	if w := adminRequest(h, "POST", "/admin/zcode/config", `{"plan":"start-plan"}`, cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"plan":"start-plan"`) {
		t.Fatalf("plan switch: %d %s", w.Code, w.Body)
	}
	if !ctrl.saw(`"cmd":"setConfig"`) || !ctrl.saw(`"plan":"start-plan"`) {
		t.Fatalf("config not forwarded: %v", ctrl.bodies)
	}
	for _, body := range []string{`{"plan":"evil"}`, `{}`, `{"provider":"evil"}`} {
		if w := adminRequest(h, "POST", "/admin/zcode/config", body, cookie, csrf); w.Code != 400 {
			t.Fatalf("body %q: %d %s", body, w.Code, w.Body)
		}
	}
	if w := adminRequest(h, "POST", "/admin/zcode/config", `{"plan":"start-plan"}`, nil, ""); w.Code != 401 {
		t.Fatalf("config without admin: %d %s", w.Code, w.Body)
	}
}

func TestAdminZcodeLoginRejectsBadProvider(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{"startOAuth": `{"ok":true,"event":"oauthUrl","authorizeUrl":"https://x","callbackPort":0}`}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
		controlHandler: ctrl.handler,
	})
	cookie, csrf := login(t, h)
	for _, body := range []string{`{"provider":"evil"}`, `{"provider":"zai"}`, `{}`, `{"provider":"bigmodel","extra":1}`, `not-json`} {
		w := adminRequest(h, "POST", "/admin/zcode/login", body, cookie, csrf)
		if body == `{"provider":"zai"}` {
			if w.Code != 200 {
				t.Fatalf("zai login: %d %s", w.Code, w.Body)
			}
			continue
		}
		if w.Code != 400 {
			t.Fatalf("body %q: %d %s", body, w.Code, w.Body)
		}
	}
	if ctrl.saw(`"provider":"evil"`) {
		t.Fatalf("invalid provider reached control: %v", ctrl.bodies)
	}
}

func TestAdminZcodeEnableDisableSpecialStates(t *testing.T) {
	t.Run("enable not_logged_in is 409", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{"startProxy": `{"ok":false,"error":"not_logged_in"}`}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/enable", "", cookie, csrf); w.Code != 409 {
			t.Fatalf("not_logged_in: %d %s", w.Code, w.Body)
		}
	})
	t.Run("enable already_running is 200", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{"startProxy": `{"ok":false,"error":"already_running"}`}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/enable", "", cookie, csrf); w.Code != 200 {
			t.Fatalf("already_running: %d %s", w.Code, w.Body)
		}
	})
	t.Run("disable not_running is 200", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{"stopProxy": `{"ok":false,"error":"not_running"}`}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/disable", "", cookie, csrf); w.Code != 200 {
			t.Fatalf("not_running: %d %s", w.Code, w.Body)
		}
	})
	t.Run("other control error is 502", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{"logout": `{"ok":false,"error":"boom"}`}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
			controlHandler: ctrl.handler,
		})
		cookie, csrf := login(t, h)
		if w := adminRequest(h, "POST", "/admin/zcode/logout", "", cookie, csrf); w.Code != 502 {
			t.Fatalf("boom: %d %s", w.Code, w.Body)
		}
	})
}

func TestAdminZcodeControlUnreachableIs503(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) }))
	t.Cleanup(core.Close)
	zcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) }))
	t.Cleanup(zcode.Close)
	cfg := testConfig(core.URL)
	cfg.ZCodeURL = mustParseURL(t, zcode.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	cfg.ZCodeControlURL = deadControlURL(t)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	for _, tc := range []struct{ path, body string }{
		{"/admin/zcode/login", `{"provider":"bigmodel"}`},
		{"/admin/zcode/enable", ""},
		{"/admin/zcode/disable", ""},
		{"/admin/zcode/logout", ""},
	} {
		w := adminRequest(h, "POST", tc.path, tc.body, cookie, csrf)
		if w.Code != 503 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

func TestAdminZcodeActionsNeedControlConfig(t *testing.T) {
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:  func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
	})
	cookie, csrf := login(t, h)
	for _, path := range []string{"/admin/zcode/login", "/admin/zcode/enable", "/admin/zcode/disable", "/admin/zcode/logout"} {
		body := ""
		if path == "/admin/zcode/login" {
			body = `{"provider":"bigmodel"}`
		}
		if w := adminRequest(h, "POST", path, body, cookie, csrf); w.Code != 409 {
			t.Fatalf("%s without control config: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestAdminZcodeActionsRequireAdmin(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{"startProxy": `{"ok":true,"event":"proxyStarted","port":8080}`}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler:    func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) },
		controlHandler: ctrl.handler,
	})
	for _, path := range []string{"/admin/zcode/enable", "/admin/zcode/disable", "/admin/zcode/logout", "/admin/zcode/config"} {
		if w := adminRequest(h, "POST", path, "", nil, ""); w.Code != 401 {
			t.Fatalf("%s without session: %d", path, w.Code)
		}
	}
	cookie, _ := login(t, h)
	if w := adminRequest(h, "POST", "/admin/zcode/enable", "", cookie, ""); w.Code != 403 {
		t.Fatalf("enable without csrf: %d", w.Code)
	}
	if ctrl.saw("startProxy") {
		t.Fatalf("rejected request reached control: %v", ctrl.bodies)
	}
}

func TestFilterZcodeModels(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"glm-5.2"}`),
		json.RawMessage(`{"id":"glm-5.3-flash"}`),
		json.RawMessage(`{"id":"not-json`),
	}
	if got := filterZcodeModels(list, "start-plan"); len(got) != 1 || string(got[0]) != `{"id":"glm-5.3-flash"}` {
		t.Fatalf("start-plan filter: %v", got)
	}
	for _, plan := range []string{"coding-plan", "", "unknown"} {
		if got := filterZcodeModels(list, plan); len(got) != len(list) {
			t.Fatalf("plan %q must keep the list intact: %v", plan, got)
		}
	}
}

func TestAdminZcodeStatusStartPlanFilter(t *testing.T) {
	ctrl := &fakeControl{resp: map[string]string{
		"status": `{"ok":true,"state":"stopped","provider":"bigmodel","plan":"start-plan","proxyPort":0,"loggedIn":true}`,
	}}
	h, _ := testConsoleWithZcode(t, zcodePair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) },
		zcodeHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.2"},{"id":"glm-5.3-flash"}]}`)
		},
		controlHandler: ctrl.handler,
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var status struct {
		Plan       string `json:"plan"`
		ModelCount int    `json:"model_count"`
		Models     []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Plan != "start-plan" {
		t.Fatalf("status body %s", w.Body)
	}
	if status.ModelCount != 1 || len(status.Models) != 1 || status.Models[0].ID != "glm-5.3-flash" {
		t.Fatalf("start-plan models not filtered: %s", w.Body)
	}
}

func TestMergedModelsStartPlanFilter(t *testing.T) {
	zcodeList := `{"object":"list","data":[{"id":"glm-5.2"},{"id":"glm-5.3-flash"}]}`
	t.Run("start-plan advertises only flash", func(t *testing.T) {
		ctrl := &fakeControl{resp: map[string]string{
			"status": `{"ok":true,"state":"running","provider":"bigmodel","plan":"start-plan","proxyPort":8080,"loggedIn":true}`,
		}}
		h, _ := testConsoleWithZcode(t, zcodePair{
			coreHandler: func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:workbuddy"}]}`)
			},
			zcodeHandler:   func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, zcodeList) },
			controlHandler: ctrl.handler,
		})
		w := adminRequest(h, "GET", "/v1/models", "", nil, "")
		if w.Code != 200 {
			t.Fatalf("models: %d %s", w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "glm-5.3-flash") || strings.Contains(w.Body.String(), "glm-5.2") {
			t.Fatalf("start-plan merge kept non-flash: %s", w.Body)
		}
		if !strings.Contains(w.Body.String(), "cn:workbuddy") {
			t.Fatalf("core models must survive: %s", w.Body)
		}
	})
	t.Run("unreachable control does not filter", func(t *testing.T) {
		core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:workbuddy"}]}`)
		}))
		t.Cleanup(core.Close)
		zcode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, zcodeList) }))
		t.Cleanup(zcode.Close)
		cfg := testConfig(core.URL)
		cfg.ZCodeURL = mustParseURL(t, zcode.URL)
		cfg.ZCodeKey = strings.Repeat("z", 32)
		cfg.ZCodeControlURL = deadControlURL(t)
		h, err := NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		w := adminRequest(h, "GET", "/v1/models", "", nil, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "glm-5.2") || !strings.Contains(w.Body.String(), "glm-5.3-flash") {
			t.Fatalf("control down must not drop models: %d %s", w.Code, w.Body)
		}
	})
}
