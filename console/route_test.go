package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newRouteStoreAt 在临时目录里建一份路由配置，用于隔离每个用例的落盘状态。
func newRouteStoreAt(t *testing.T) *routeStore {
	t.Helper()
	return newRouteStore(filepath.Join(t.TempDir(), "routes.json"))
}

// routeConsole 建一个带可写路由配置的 console；可选挂上 opencode 上游喂探测数据。
func routeConsole(t *testing.T, core http.HandlerFunc, opencode http.HandlerFunc) (http.Handler, Config) {
	t.Helper()
	coreTS := httptest.NewServer(core)
	t.Cleanup(coreTS.Close)
	cfg := testConfig(coreTS.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	if opencode != nil {
		ocTS := httptest.NewServer(opencode)
		t.Cleanup(ocTS.Close)
		cfg.OpenCodeURL = mustParseURL(t, ocTS.URL)
		cfg.OpenCodeKey = strings.Repeat("o", 32)
	}
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func saveRoutes(t *testing.T, h http.Handler, models []routeEntry, fallback string) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := login(t, h)
	payload, err := json.Marshal(map[string]any{"models": models, "auto_fallback": fallback})
	if err != nil {
		t.Fatal(err)
	}
	return adminRequest(h, "POST", "/admin/route/save", string(payload), cookie, csrf)
}

func TestRouteAliasRewritesModelAndAddsHeaders(t *testing.T) {
	var mu sync.Mutex
	var coreModel string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		// 保存配置会顺带抓一次通道目录（GET /v1/models），只记录真正转发的对话请求。
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			coreModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "fast", Channel: channelGLM, Model: "glm-5.3", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	// 保存成功后配置立即生效，且是落盘过的。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("fast")))
	h.ServeHTTP(rec, req)
	// glm 通道未配置，别名解析后仍会落到 core（前缀不匹配旁路），
	// 关键是请求体里的 model 已被改写成公共形态，而不是客户端原样透传。
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if coreModel != "glm-5.3" {
		t.Fatalf("core saw model %q", coreModel)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "glm" {
		t.Fatalf("channel header=%q", got)
	}
	if got := rec.Header().Get("X-Prism-Routed-Model"); got != "glm-5.3" {
		t.Fatalf("routed header=%q", got)
	}
}

func TestRouteAliasRoutesToOpenCodeSidecar(t *testing.T) {
	var mu sync.Mutex
	var coreChats, ocChats int
	var ocBody string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			coreChats++
			mu.Unlock()
		}
		fmt.Fprint(w, `{"core":true}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			ocChats++
			body, _ := io.ReadAll(r.Body)
			ocBody = string(body)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	})
	if got := saveRoutes(t, h, []routeEntry{{Alias: "cheap", Channel: channelOpenCode, Model: "OC · FreeModel", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("cheap")))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != `{"opencode":true}` {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if ocChats != 1 || coreChats != 0 {
		t.Fatalf("chats core=%d opencode=%d", coreChats, ocChats)
	}
	// 公共前缀在转发前被剥掉，上游收到的是它自己的模型名。
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(ocBody), &envelope) != nil {
		t.Fatalf("opencode body not JSON: %s", ocBody)
	}
	if envelope.Model != "OC · FreeModel" {
		t.Fatalf("opencode model=%q", envelope.Model)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "opencode" {
		t.Fatalf("channel header=%q", got)
	}
}

func TestRouteDisabledAliasDoesNotCaptureModel(t *testing.T) {
	var mu sync.Mutex
	var coreModel string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			coreModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	// enabled=false 的条目必须完全透明：请求原样落到 core，模型名不被改写。
	if got := saveRoutes(t, h, []routeEntry{{Alias: "off", Channel: channelCore, Model: "cn:auto", Enabled: false}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("off"))))
	mu.Lock()
	defer mu.Unlock()
	if coreModel != "off" {
		t.Fatalf("disabled alias rewrote model to %q", coreModel)
	}
	if rec.Header().Get("X-Prism-Channel") != "" {
		t.Fatal("disabled alias must not emit routing headers")
	}
}

func TestAutoFallsBackToCoreWithoutProbeData(t *testing.T) {
	var mu sync.Mutex
	var coreModel string
	// 没有任何通道提供探测结论，auto 必须回退到可配置的核心通道模型。
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			coreModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	if got := saveRoutes(t, h, nil, "global:auto"); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("auto"))))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if coreModel != "global:auto" {
		t.Fatalf("auto fallback model=%q", coreModel)
	}
	if rec.Header().Get("X-Prism-Fallback") != "core" {
		t.Fatalf("missing fallback header: %q", rec.Header().Get("X-Prism-Fallback"))
	}
	if rec.Header().Get("X-Prism-Channel") != "core" {
		t.Fatalf("channel=%q", rec.Header().Get("X-Prism-Channel"))
	}
}

func TestAutoPrefersProbedAvailableModel(t *testing.T) {
	var mu sync.Mutex
	var ocModel string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// 两个候选：一个仅对话且更快，一个确认可用但更慢；auto 必须先取可用档。
			fmt.Fprint(w, `{"phase":"ready","modelResults":{
				"OC · Fast":{"ok":true,"chatOnly":true,"durationMs":10},
				"OC · Good":{"ok":true,"category":"available","durationMs":900}
			}}`)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			ocModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	})
	if got := saveRoutes(t, h, nil, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("auto"))))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if ocModel != "OC · Good" {
		t.Fatalf("auto picked %q, want the available-band model", ocModel)
	}
	if rec.Header().Get("X-Prism-Fallback") != "" {
		t.Fatal("auto with a usable candidate must not be flagged as fallback")
	}
}

func TestValidateRouteEntriesRejectsReservedAndBadChannels(t *testing.T) {
	cases := []struct {
		name string
		in   []routeEntry
	}{
		{"保留前缀", []routeEntry{{Alias: "glm-x", Channel: channelCore, Model: "cn:auto", Enabled: true}}},
		{"命名空间前缀", []routeEntry{{Alias: "cn:x", Channel: channelCore, Model: "cn:auto", Enabled: true}}},
		{"auto 保留名", []routeEntry{{Alias: "auto", Channel: channelCore, Model: "cn:auto", Enabled: true}}},
		{"通道非法", []routeEntry{{Alias: "x", Channel: "bogus", Model: "cn:auto", Enabled: true}}},
		{"core 缺命名空间", []routeEntry{{Alias: "x", Channel: channelCore, Model: "auto", Enabled: true}}},
		{"旁路带冒号", []routeEntry{{Alias: "x", Channel: channelQoder, Model: "cn:auto", Enabled: true}}},
		{"别名重复", []routeEntry{
			{Alias: "dup", Channel: channelCore, Model: "cn:auto", Enabled: true},
			{Alias: "dup", Channel: channelCore, Model: "cn:auto", Enabled: true},
		}},
	}
	for _, tc := range cases {
		if err := validateRouteEntries(tc.in); err == nil {
			t.Errorf("%s: 期望校验失败", tc.name)
		}
	}
}

func TestValidateRouteEntriesAcceptsLegalConfig(t *testing.T) {
	ok := []routeEntry{
		{Alias: "fast", Channel: channelGLM, Model: "glm-5.3", Enabled: true},
		{Alias: "cheap", Channel: channelQoder, Model: "qoder-auto", Enabled: true},
		{Alias: "main", Channel: channelCore, Model: "cn:auto", Enabled: true},
	}
	if err := validateRouteEntries(ok); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
	if err := validateAutoFallback("cn:auto"); err != nil {
		t.Fatalf("合法回退被拒：%v", err)
	}
	if err := validateAutoFallback("auto"); err == nil {
		t.Fatal("回退缺命名空间应被拒")
	}
}

func TestRouteStoreReloadKeepsLastGoodConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store := newRouteStore(path)
	if err := store.save([]routeEntry{{Alias: "keep", Channel: channelCore, Model: "cn:auto", Enabled: true}}, ""); err != nil {
		t.Fatal(err)
	}
	// 手工把文件写坏：reload 必须沿用上一份可用快照，而不是清空。
	if err := os.WriteFile(path, []byte(`{"version":1,"models":[{"alias":"glm-bad"`), 0o600); err != nil {
		t.Fatal(err)
	}
	store.reload()
	if _, ok := store.lookup("keep"); !ok {
		t.Fatal("损坏配置把上一份可用快照掀翻了")
	}
	// 版本不匹配同样按上一份处理。
	if err := os.WriteFile(path, []byte(`{"version":99,"models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store.reload()
	if _, ok := store.lookup("keep"); !ok {
		t.Fatal("版本不匹配把上一份可用快照掀翻了")
	}
}

func TestRouteSaveRejectsInvalidAndKeepsOldConfig(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "keep", Channel: channelCore, Model: "cn:auto", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	// 一次非法保存必须整体拒绝，不能部分生效。
	if got := saveRoutes(t, h, []routeEntry{{Alias: "glm-x", Channel: channelCore, Model: "cn:auto", Enabled: true}}, ""); got.Code != 400 {
		t.Fatalf("非法保存应 400，实际 %d %s", got.Code, got.Body)
	}
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "GET", "/admin/route", "", cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("read state: %d %s", rec.Code, rec.Body)
	}
	var state struct {
		Enabled bool `json:"enabled"`
		Aliases []struct {
			Alias string `json:"alias"`
		} `json:"aliases"`
		AutoModel string `json:"auto_model"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &state) != nil {
		t.Fatalf("state not JSON: %s", rec.Body)
	}
	if !state.Enabled || len(state.Aliases) != 1 || state.Aliases[0].Alias != "keep" {
		t.Fatalf("非法保存污染了现有配置：%+v", state)
	}
	if state.AutoModel != "auto" {
		t.Fatalf("auto_model=%q", state.AutoModel)
	}
}

func TestRouteEndpointsRequireAdminSession(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	if w := adminRequest(h, "GET", "/admin/route", "", nil, ""); w.Code != 401 {
		t.Fatalf("未登录读配置应 401，实际 %d", w.Code)
	}
	if w := adminRequest(h, "POST", "/admin/route/save", `{"models":[]}`, nil, ""); w.Code != 401 {
		t.Fatalf("未登录保存应 401，实际 %d", w.Code)
	}
	// 有会话但缺 CSRF 的写操作必须被拒。
	cookie, _ := login(t, h)
	if w := adminRequest(h, "POST", "/admin/route/save", `{"models":[]}`, cookie, ""); w.Code != 403 {
		t.Fatalf("缺 CSRF 应 403，实际 %d", w.Code)
	}
}

func TestAutoModelListedOnlyWhenRoutingEnabled(t *testing.T) {
	coreList := `{"object":"list","data":[{"id":"cn:auto","object":"model"}]}`
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, coreList)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("models: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"auto"`) {
		t.Fatalf("启用路由后 /v1/models 应包含 auto：%s", rec.Body)
	}

	// 未配置 RouteFile 时不得出现 auto，否则客户端会选到一个没人解析的模型。
	plain, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, coreList)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if strings.Contains(rec.Body.String(), `"id":"auto"`) {
		t.Fatalf("未启用路由时不应暴露 auto：%s", rec.Body)
	}
}

// modelOf 读出转发到上游的请求体里的 model 字段。
func modelOf(t *testing.T, r *http.Request) string {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.Model
}
