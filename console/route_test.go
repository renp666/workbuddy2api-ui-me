package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// switchModel 通过管理端点切换单个模型的积分开关。
func switchModel(t *testing.T, h http.Handler, model string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := login(t, h)
	payload, err := json.Marshal(map[string]any{"model": model, "enabled": enabled})
	if err != nil {
		t.Fatal(err)
	}
	return adminRequest(h, "POST", "/admin/model-switch", string(payload), cookie, csrf)
}

// readSwitch 读取当前人工覆盖表。
func readSwitch(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "GET", "/admin/model-switch", "", cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("read switch: %d %s", rec.Code, rec.Body)
	}
	var state struct {
		Overrides map[string]bool `json:"overrides"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &state) != nil {
		t.Fatalf("switch state not JSON: %s", rec.Body)
	}
	return state.Overrides
}

// gateChat 向公共对话出口发一次请求，用于验证积分开关的放行与拒绝。
func gateChat(h http.Handler, model string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody(model))))
	return rec
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
	// 人工启用让 glm-5.3 进公布白名单（调用本就默认放行，这里顺带覆盖开关端点与别名共存）。
	if got := switchModel(t, h, "glm-5.3", true); got.Code != 200 {
		t.Fatalf("model-switch enable: %d %s", got.Code, got.Body)
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

// TestRouteAliasAcceptsPrefixedModelName 复现真实 routes.json 的写法：控制台模型下拉
// 里选出来的 qoder/opencode 名字本身就带公共前缀（fetchChannelModels 已补过一次）。
// 若 publicModel 无条件再补前缀，会拼出双前缀，上游报 model_not_found——这正是需求书
// §2「no2 → model_not_found」的根因。这里断言带前缀的 Model 转发后上游收到的仍是单前缀
// 剥掉后的原生名。
func TestRouteAliasAcceptsPrefixedModelName(t *testing.T) {
	var mu sync.Mutex
	var ocModel string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			ocModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	})
	// Model 已带 opencode- 前缀，模拟从页面下拉直接保存的真实配置。
	if got := saveRoutes(t, h, []routeEntry{{Alias: "bunny", Channel: channelOpenCode, Model: "opencode-OC · Space Bunny Free", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("bunny"))))
	if rec.Code != 200 || rec.Body.String() != `{"opencode":true}` {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	// 剥前缀后交给上游的必须是原生名，不能残留双前缀或前缀没剥干净。
	if ocModel != "OC · Space Bunny Free" {
		t.Fatalf("opencode saw model %q, want native name without prefix", ocModel)
	}
	if got := rec.Header().Get("X-Prism-Routed-Model"); got != "opencode-OC · Space Bunny Free" {
		t.Fatalf("routed header=%q, want single prefix", got)
	}
}

// TestAliasEntriesAppearInPublicModels 验证别名作为虚拟条目出现在公共 /v1/models：
// realm 沿用所属通道，带 alias/routed_model 标注。公布与开关解耦（「关机的手机仍
// 拿在手里」）：别名配置了就公布，停用别名也在列表里，调用时才报「已停用」；
// 真实直名走启用表白名单，未显式启用不上榜但仍可按名调用。
func TestAliasEntriesAppearInPublicModels(t *testing.T) {
	var mu sync.Mutex
	var ocModel string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:free","object":"model","credits":"x0.00"}]}`)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			ocModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	})
	// 一条 opencode 别名（目标未配置直名）+ 一条 core 别名（启用）+ 一条 core 别名（停用）。
	if got := saveRoutes(t, h, []routeEntry{
		{Alias: "bunny", Channel: channelOpenCode, Model: "opencode-OC · Space Bunny Free", Enabled: true},
		{Alias: "main", Channel: channelCore, Model: "cn:free", Enabled: true},
		{Alias: "off", Channel: channelCore, Model: "cn:free", Enabled: false},
	}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	fetch := func() map[string]struct {
		realm, routed string
		alias         bool
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
		if rec.Code != 200 {
			t.Fatalf("models: %d %s", rec.Code, rec.Body)
		}
		var list struct {
			Data []struct {
				ID          string `json:"id"`
				Realm       string `json:"realm"`
				Alias       bool   `json:"alias"`
				RoutedModel string `json:"routed_model"`
			} `json:"data"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatalf("models body not JSON: %s", rec.Body)
		}
		byID := map[string]struct {
			realm, routed string
			alias         bool
		}{}
		for _, item := range list.Data {
			byID[item.ID] = struct {
				realm, routed string
				alias         bool
			}{item.Realm, item.RoutedModel, item.Alias}
		}
		return byID
	}
	byID := fetch()
	// 真实直名未显式启用：不在公布名单，但调用仍放行（双轨语义）。
	if _, ok := byID["cn:free"]; ok {
		t.Fatalf("unenabled direct name must stay unpublished: %+v", byID)
	}
	if rec := gateChat(h, "cn:free"); rec.Code != 200 {
		t.Fatalf("unpublished model must stay callable: %d %s", rec.Code, rec.Body)
	}
	bunny, ok := byID["bunny"]
	if !ok || !bunny.alias || bunny.realm != "opencode" || bunny.routed != "opencode-OC · Space Bunny Free" {
		t.Fatalf("opencode alias entry wrong: %+v present=%v", bunny, ok)
	}
	main, ok := byID["main"]
	if !ok || !main.alias || main.realm != "cn" || main.routed != "cn:free" {
		t.Fatalf("core alias entry wrong: %+v present=%v", main, ok)
	}
	off, ok := byID["off"]
	if !ok || !off.alias || off.routed != "cn:free" {
		t.Fatalf("disabled alias must still be published: %+v present=%v", off, ok)
	}
	// 显式启用后直名上榜（公布走启用表白名单）。
	if got := switchModel(t, h, "cn:free", true); got.Code != 200 {
		t.Fatalf("enable cn:free: %d %s", got.Code, got.Body)
	}
	if _, ok := fetch()["cn:free"]; !ok {
		t.Fatal("enabled direct name missing from list")
	}
	mu.Lock()
	defer mu.Unlock()
	_ = ocModel
}

// TestRouteDisabledAliasReturnsDisabledError 验证配置了但停用的别名不再透明落到
// core（旧行为会带着原名打出莫名其妙的 404），而是明确报「已停用」且不打到上游；
// 名字本身仍在公布名单里（见 TestAliasEntriesAppearInPublicModels）。
func TestRouteDisabledAliasReturnsDisabledError(t *testing.T) {
	var mu sync.Mutex
	var coreHits int
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			coreHits++
			mu.Unlock()
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "off", Channel: channelCore, Model: "cn:auto", Enabled: false}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("off"))))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "已停用") {
		t.Fatalf("disabled alias must report disabled: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if coreHits != 0 {
		t.Fatalf("disabled alias must not reach core, hits=%d", coreHits)
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
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("gateway-auto"))))
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
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("gateway-auto"))))
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
		{"auto 保留名", []routeEntry{{Alias: "gateway-auto", Channel: channelCore, Model: "cn:auto", Enabled: true}}},
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

// TestValidateRouteEntriesAcceptsSlashInUpstreamModelNames 覆盖 autoclaw 上游
// 下发的原生模型名形态：id 里带 /（openclaw/default、global/zai_auto）。
// 别名表与模型开关表都必须放行，否则管理员既配不了别名也调不了这些模型的开关。
func TestValidateRouteEntriesAcceptsSlashInUpstreamModelNames(t *testing.T) {
	ok := []routeEntry{
		{Alias: "no1", Channel: channelAutoClaw, Model: "autoclaw-openclaw/default", Enabled: true},
		{Alias: "no2", Channel: channelAutoClaw, Model: "autoclaw-global/zai_auto", Enabled: true},
	}
	if err := validateRouteEntries(ok); err != nil {
		t.Fatalf("带 / 的上游模型名被别名表拒：%v", err)
	}
	list := []string{"autoclaw-openclaw/default", "autoclaw-global/zai_auto"}
	if err := validateModelSwitchList("禁用模型表", list); err != nil {
		t.Fatalf("带 / 的上游模型名被开关表拒：%v", err)
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
	if state.AutoModel != "gateway-auto" {
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
	if !strings.Contains(rec.Body.String(), `"id":"gateway-auto"`) {
		t.Fatalf("启用路由后 /v1/models 应包含 gateway-auto：%s", rec.Body)
	}

	// 未配置 RouteFile 时不得出现 gateway-auto，否则客户端会选到一个没人解析的模型。
	plain, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, coreList)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if strings.Contains(rec.Body.String(), `"id":"gateway-auto"`) {
		t.Fatalf("未启用路由时不应暴露 gateway-auto：%s", rec.Body)
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

// TestParseCreditRule 覆盖上游 credits 原始串的解析口径：带/不带后缀、
// 零值、非法值与缺失都不能被当成可用倍率，未知不得伪造零。
func TestParseCreditRule(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"x0.79 credits", 0.79, true},
		{"x0.00 credits", 0, true},
		{"x0.00", 0, true},
		{"x3.47", 3.47, true},
		{"X1.5", 1.5, true},
		{"", 0, false},
		{"credits", 0, false},
		{"free", 0, false},
		{"x-1", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseCreditRule(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseCreditRule(%q)=(%v,%v) want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// creditConsole 建一个带可写路由配置的 console，core 的 /v1/models 用给定 JSON 回应。
func creditConsole(t *testing.T, coreModels string) http.Handler {
	t.Helper()
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, coreModels)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	return h
}

// TestModelSwitchDefaultsFromCredits 验证调用与公布双轨（2026-10-10 口径）：
// 调用统一对外开放——未配置的直名默认放行，倍率不再参与判定；公布走启用表白名单，
// 未显式启用的直名一律不上 /v1/models。
func TestModelSwitchDefaultsFromCredits(t *testing.T) {
	coreModels := `{"object":"list","data":[
		{"id":"cn:free","credits":"x0.00"},
		{"id":"cn:paid","credits":"x0.79"},
		{"id":"cn:unknown"}]}`
	h := creditConsole(t, coreModels)
	// 对话出口：未配置一律放行——免费、付费、未知倍率一视同仁。
	for _, model := range []string{"cn:free", "cn:paid", "cn:unknown"} {
		if rec := gateChat(h, model); rec.Code != 200 {
			t.Fatalf("%s should be open by default: %d %s", model, rec.Code, rec.Body)
		}
	}
	// /v1/models：未显式启用的直名不公布。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	body := rec.Body.String()
	for _, model := range []string{"cn:free", "cn:paid", "cn:unknown"} {
		if strings.Contains(body, model) {
			t.Fatalf("unenabled model %s leaked into list: %s", model, body)
		}
	}
}

// TestModelSwitchManualOverridePersists 验证人工开关双向生效并落盘：
// 强制启用付费模型（调用放行 + 上公布名单）、强制停用免费模型（调用 404 + 退出名单）。
func TestModelSwitchManualOverridePersists(t *testing.T) {
	coreModels := `{"object":"list","data":[{"id":"cn:free","credits":"x0.00"},{"id":"cn:paid","credits":"x0.79"}]}`
	h := creditConsole(t, coreModels)
	if got := switchModel(t, h, "cn:paid", true); got.Code != 200 {
		t.Fatalf("enable paid: %d %s", got.Code, got.Body)
	}
	if got := switchModel(t, h, "cn:free", false); got.Code != 200 {
		t.Fatalf("disable free: %d %s", got.Code, got.Body)
	}
	overrides := readSwitch(t, h)
	if overrides["cn:paid"] != true || overrides["cn:free"] != false {
		t.Fatalf("overrides=%v want paid=true free=false", overrides)
	}
	if rec := gateChat(h, "cn:paid"); rec.Code != 200 {
		t.Fatalf("manually enabled model rejected: %d %s", rec.Code, rec.Body)
	}
	if rec := gateChat(h, "cn:free"); rec.Code != 404 {
		t.Fatalf("manually disabled model allowed: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "cn:paid") {
		t.Fatalf("enabled model missing from list: %s", body)
	}
	if strings.Contains(body, "cn:free") {
		t.Fatalf("disabled model leaked into list: %s", body)
	}
}

// TestModelSwitchOpenCodeDisable 验证 opencode 免费通道同样参与开关：未配置默认
// 放行，显式停用后直名与指向它的别名一起被拒，上游不被触达。
func TestModelSwitchOpenCodeDisable(t *testing.T) {
	var ocModel string
	var mu sync.Mutex
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			mu.Lock()
			ocModel = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	})
	if got := saveRoutes(t, h, []routeEntry{{Alias: "oc", Channel: channelOpenCode, Model: "OC · Free", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "opencode-OC · Free"); rec.Code != 200 {
		t.Fatalf("unconfigured opencode model must pass: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	ocModel = "" // 停用前的放行请求合法触达过上游，清掉记录只看停用之后。
	mu.Unlock()
	if got := switchModel(t, h, "opencode-OC · Free", false); got.Code != 200 {
		t.Fatalf("disable: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "opencode-OC · Free"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "已停用") {
		t.Fatalf("disabled opencode model must 404: %d %s", rec.Code, rec.Body)
	}
	if rec := gateChat(h, "oc"); rec.Code != 404 {
		t.Fatalf("alias to disabled target must 404: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if ocModel != "" {
		t.Fatalf("disabled model reached upstream as %q", ocModel)
	}
}

// sideExemptConsole 挂一个旁路通道上游，用于验证该通道的开关语义。免费通道
// （opencode/autoclaw/trae/raccoon）不采集倍率，但同样参与启停开关，这里用统一
// 构造避免逐个用例重复。
func sideExemptConsole(t *testing.T, channel routeChannel, urlField func(*Config, *url.URL), keyField func(*Config, string)) http.Handler {
	t.Helper()
	coreTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(coreTS.Close)
	sideTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"%s":true}`, channel)
	}))
	t.Cleanup(sideTS.Close)
	cfg := testConfig(coreTS.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	urlField(&cfg, mustParseURL(t, sideTS.URL))
	keyField(&cfg, strings.Repeat("k", 32))
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestModelSwitchAutoClawDisable 验证 autoclaw 免费通道参与开关：默认放行、停用生效。
func TestModelSwitchAutoClawDisable(t *testing.T) {
	h := sideExemptConsole(t, channelAutoClaw,
		func(c *Config, u *url.URL) { c.AutoClawURL = u },
		func(c *Config, k string) { c.AutoClawKey = k })
	if got := saveRoutes(t, h, []routeEntry{{Alias: "ac", Channel: channelAutoClaw, Model: "claude-sonnet", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "autoclaw-claude-sonnet"); rec.Code != 200 {
		t.Fatalf("unconfigured autoclaw model must pass: %d %s", rec.Code, rec.Body)
	}
	if got := switchModel(t, h, "autoclaw-claude-sonnet", false); got.Code != 200 {
		t.Fatalf("disable: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "autoclaw-claude-sonnet"); rec.Code != 404 {
		t.Fatalf("disabled autoclaw model must 404: %d %s", rec.Code, rec.Body)
	}
}

// TestModelSwitchTraeDisable 验证 trae 通道参与开关（倍率只是展示，不参与判定）：
// 默认放行、停用生效。
func TestModelSwitchTraeDisable(t *testing.T) {
	h := sideExemptConsole(t, channelTrae,
		func(c *Config, u *url.URL) { c.TraeURL = u },
		func(c *Config, k string) { c.TraeKey = k })
	if got := saveRoutes(t, h, []routeEntry{{Alias: "tr", Channel: channelTrae, Model: "claude-4", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "trae-claude-4"); rec.Code != 200 {
		t.Fatalf("unconfigured trae model must pass: %d %s", rec.Code, rec.Body)
	}
	if got := switchModel(t, h, "trae-claude-4", false); got.Code != 200 {
		t.Fatalf("disable: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "trae-claude-4"); rec.Code != 404 {
		t.Fatalf("disabled trae model must 404: %d %s", rec.Code, rec.Body)
	}
}

// TestModelSwitchRaccoonDisable 验证 raccoon 免费通道参与开关：默认放行、停用生效。
func TestModelSwitchRaccoonDisable(t *testing.T) {
	h := sideExemptConsole(t, channelRaccoon,
		func(c *Config, u *url.URL) { c.RaccoonURL = u },
		func(c *Config, k string) { c.RaccoonKey = k })
	if got := saveRoutes(t, h, []routeEntry{{Alias: "rc", Channel: channelRaccoon, Model: "glm-5.3", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "raccoon-glm-5.3"); rec.Code != 200 {
		t.Fatalf("unconfigured raccoon model must pass: %d %s", rec.Code, rec.Body)
	}
	if got := switchModel(t, h, "raccoon-glm-5.3", false); got.Code != 200 {
		t.Fatalf("disable: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "raccoon-glm-5.3"); rec.Code != 404 {
		t.Fatalf("disabled raccoon model must 404: %d %s", rec.Code, rec.Body)
	}
}

// TestModelSwitchAutoFollowsTarget 验证 auto 虚拟名本身不被开关拦下，但按解析后的
// 目标名受检：目标被显式停用时按目标名拒绝，而不是按 auto 拒绝。
func TestModelSwitchAutoFollowsTarget(t *testing.T) {
	h := creditConsole(t, `{"object":"list","data":[{"id":"cn:auto","credits":"x0.00"}]}`)
	if got := saveRoutes(t, h, nil, "cn:auto"); got.Code != 200 {
		t.Fatalf("save fallback: %d %s", got.Code, got.Body)
	}
	if rec := gateChat(h, "gateway-auto"); rec.Code != 200 {
		t.Fatalf("auto with an open target rejected: %d %s", rec.Code, rec.Body)
	}
	// 显式停用解析目标 cn:auto：auto 请求按目标名拒绝。
	if got := switchModel(t, h, "cn:auto", false); got.Code != 200 {
		t.Fatalf("disable target: %d %s", got.Code, got.Body)
	}
	rec := gateChat(h, "gateway-auto")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "cn:auto") {
		t.Fatalf("auto must be judged by its resolved target: %d %s", rec.Code, rec.Body)
	}
}

// TestModelSwitchRejectsAutoName 直接对 auto 名设开关应被拒（虚拟模型不参与开关）。
func TestModelSwitchRejectsAutoName(t *testing.T) {
	h := creditConsole(t, `{"object":"list","data":[]}`)
	if got := switchModel(t, h, "gateway-auto", true); got.Code != 400 {
		t.Fatalf("switching gateway-auto should be rejected: %d %s", got.Code, got.Body)
	}
}

// TestModelSwitchSurvivesAliasSave 验证保存别名表不会清空模型开关覆盖表。
func TestModelSwitchSurvivesAliasSave(t *testing.T) {
	h := creditConsole(t, `{"object":"list","data":[{"id":"cn:paid","credits":"x0.79"}]}`)
	if got := switchModel(t, h, "cn:paid", true); got.Code != 200 {
		t.Fatalf("enable: %d %s", got.Code, got.Body)
	}
	if got := saveRoutes(t, h, []routeEntry{{Alias: "keep", Channel: channelCore, Model: "cn:auto", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save aliases: %d %s", got.Code, got.Body)
	}
	overrides := readSwitch(t, h)
	if overrides["cn:paid"] != true {
		t.Fatalf("alias save wiped the switch table: overrides=%v", overrides)
	}
}

// TestModelSwitchEndpointsRequireAdmin 验证开关端点受管理会话与 CSRF 保护。
func TestModelSwitchEndpointsRequireAdmin(t *testing.T) {
	h := creditConsole(t, `{"object":"list","data":[]}`)
	if w := adminRequest(h, "GET", "/admin/model-switch", "", nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated read should 401: %d", w.Code)
	}
	if w := adminRequest(h, "POST", "/admin/model-switch", `{"model":"cn:x","enabled":true}`, nil, ""); w.Code != 401 {
		t.Fatalf("unauthenticated write should 401: %d", w.Code)
	}
	cookie, _ := login(t, h)
	if w := adminRequest(h, "POST", "/admin/model-switch", `{"model":"cn:x","enabled":true}`, cookie, ""); w.Code != 403 {
		t.Fatalf("missing CSRF should 403: %d", w.Code)
	}
}

// TestModelSwitchDisabledWithoutRouteFile 未配置可写路由目录时开关端点应 409，
// 且公共出口不因本功能改变行为（routes==nil 恒放行）。
func TestModelSwitchDisabledWithoutRouteFile(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:paid","credits":"x0.79"}]}`)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	})
	cookie, csrf := login(t, h)
	if w := adminRequest(h, "POST", "/admin/model-switch", `{"model":"cn:paid","enabled":false}`, cookie, csrf); w.Code != 409 {
		t.Fatalf("switch without route file should 409: %d %s", w.Code, w.Body)
	}
	// 未启用路由时保持现状：倍率>0 的直名也放行、也照常公布。
	if rec := gateChat(h, "cn:paid"); rec.Code != 200 {
		t.Fatalf("gate must be inert without route file: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), "cn:paid") {
		t.Fatalf("direct names must stay published without route file: %s", rec.Body)
	}
}

// ocHealthHandler 造一个 opencode 上游：/health 按给定 JSON 回探测结论，
// 其余路径回一句固定文本。opencode 是免费通道、不受积分开关约束，因此候选链
// 的选路用例都建在它上面，不必先手工放行模型。
func ocHealthHandler(t *testing.T, health string, seen *string, mu *sync.Mutex) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, health)
			return
		}
		if r.URL.Path == "/v1/chat/completions" && seen != nil {
			mu.Lock()
			*seen = modelOf(t, r)
			mu.Unlock()
		}
		fmt.Fprint(w, `{"opencode":true}`)
	}
}

// chatViaAlias 经公共出口发一次别名请求，返回响应与 opencode 实际收到的模型名。
func chatViaAlias(t *testing.T, h http.Handler, alias string, seen *string, mu *sync.Mutex) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody(alias))))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	return rec
}

// TestAliasPriorityStrategyUsesPrimary 默认策略 priority 必须无视探测结论，恒用
// 候选链第一跳。这是「我就要这条链路」的显式表达，不能被探测抖动改变；也是存量
// 配置升级后的既有行为。
func TestAliasPriorityStrategyUsesPrimary(t *testing.T) {
	var mu sync.Mutex
	var seen string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, ocHealthHandler(t, `{"phase":"ready","modelResults":{
		"OC · Bad":{"ok":false,"error":"boom"},
		"OC · Good":{"ok":true,"category":"available","durationMs":100}
	}}`, &seen, &mu))
	entry := routeEntry{
		Alias:     "daily",
		Channel:   channelOpenCode,
		Model:     "OC · Bad",
		Fallbacks: []routeTarget{{Channel: channelOpenCode, Model: "OC · Good"}},
		Enabled:   true,
	}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := chatViaAlias(t, h, "daily", &seen, &mu)
	mu.Lock()
	defer mu.Unlock()
	if seen != "OC · Bad" {
		t.Fatalf("priority 应恒用主目标，实际转发 %q", seen)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "0" {
		t.Fatalf("target index=%q, want 0", got)
	}
}

// TestAliasHealthStrategyPicksAvailableFallback 是热备的核心用例：主目标探测失败
// 时必须自动切到链上可用的备用，且别名不变、客户端无感。
func TestAliasHealthStrategyPicksAvailableFallback(t *testing.T) {
	var mu sync.Mutex
	var seen string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, ocHealthHandler(t, `{"phase":"ready","modelResults":{
		"OC · Bad":{"ok":false,"error":"boom"},
		"OC · Good":{"ok":true,"category":"available","durationMs":100}
	}}`, &seen, &mu))
	entry := routeEntry{
		Alias:     "daily",
		Channel:   channelOpenCode,
		Model:     "OC · Bad",
		Fallbacks: []routeTarget{{Channel: channelOpenCode, Model: "OC · Good"}},
		Strategy:  strategyHealth,
		Enabled:   true,
	}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := chatViaAlias(t, h, "daily", &seen, &mu)
	mu.Lock()
	defer mu.Unlock()
	if seen != "OC · Good" {
		t.Fatalf("health 应切到可用备用，实际转发 %q", seen)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "1" {
		t.Fatalf("target index=%q, want 1", got)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "opencode" {
		t.Fatalf("channel=%q", got)
	}
	if got := rec.Header().Get("X-Prism-Routed-Model"); got != "opencode-OC · Good" {
		t.Fatalf("routed=%q", got)
	}
}

// TestAliasHealthStrategyNoProbeDataUsesPrimary 通道活着但还没有逐模型结论时，
// health 必须按链序退回主目标，不随机挑、也不因为「没有结论」就报错。
func TestAliasHealthStrategyNoProbeDataUsesPrimary(t *testing.T) {
	var mu sync.Mutex
	var seen string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, ocHealthHandler(t, `{"phase":"ready"}`, &seen, &mu))
	entry := routeEntry{
		Alias:     "daily",
		Channel:   channelOpenCode,
		Model:     "OC · Primary",
		Fallbacks: []routeTarget{{Channel: channelOpenCode, Model: "OC · Backup"}},
		Strategy:  strategyHealth,
		Enabled:   true,
	}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := chatViaAlias(t, h, "daily", &seen, &mu)
	mu.Lock()
	defer mu.Unlock()
	if seen != "OC · Primary" {
		t.Fatalf("无探测结论时应按链序用主目标，实际 %q", seen)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "0" {
		t.Fatalf("target index=%q, want 0", got)
	}
	if reason := rec.Header().Get("X-Prism-Reason"); !strings.Contains(reason, "按链序") {
		t.Fatalf("reason=%q，应说明按链序回退", reason)
	}
}

// TestAliasFallbackChainOrderPreserved 两条备用都可用时取链序靠前的，不按探测
// 耗时排序——链序是管理员的意图，耗时只是通道质量的参考。
func TestAliasFallbackChainOrderPreserved(t *testing.T) {
	var mu sync.Mutex
	var seen string
	// Second 明显更快，但排在 First 之后，必须先选 First。
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, ocHealthHandler(t, `{"phase":"ready","modelResults":{
		"OC · Bad":{"ok":false,"error":"boom"},
		"OC · First":{"ok":true,"category":"available","durationMs":900},
		"OC · Second":{"ok":true,"category":"available","durationMs":10}
	}}`, &seen, &mu))
	entry := routeEntry{
		Alias:   "daily",
		Channel: channelOpenCode,
		Model:   "OC · Bad",
		Fallbacks: []routeTarget{
			{Channel: channelOpenCode, Model: "OC · First"},
			{Channel: channelOpenCode, Model: "OC · Second"},
		},
		Strategy: strategyHealth,
		Enabled:  true,
	}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := chatViaAlias(t, h, "daily", &seen, &mu)
	mu.Lock()
	defer mu.Unlock()
	if seen != "OC · First" {
		t.Fatalf("同档应取链序靠前者，实际 %q", seen)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "1" {
		t.Fatalf("target index=%q, want 1", got)
	}
}

// TestValidateRouteEntriesRejectsBadFallbackChains 候选链的校验必须和主目标同口径，
// 且拒绝重复跳与非法策略。
func TestValidateRouteEntriesRejectsBadFallbackChains(t *testing.T) {
	cases := []struct {
		name string
		in   []routeEntry
	}{
		{"备用与主目标重复", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: channelCore, Model: "cn:auto"}}}}},
		{"备用之间重复", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: channelQoder, Model: "q"}, {Channel: channelQoder, Model: "q"}}}}},
		{"备用 core 缺命名空间", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: channelCore, Model: "auto"}}}}},
		{"备用旁路带冒号", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: channelQoder, Model: "cn:auto"}}}}},
		{"备用通道非法", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: "bogus", Model: "x"}}}}},
		{"备用模型名为空", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true,
			Fallbacks: []routeTarget{{Channel: channelQoder, Model: "  "}}}}},
		{"策略非法", []routeEntry{{Alias: "a", Channel: channelCore, Model: "cn:auto", Enabled: true, Strategy: "random"}}},
	}
	for _, tc := range cases {
		if err := validateRouteEntries(tc.in); err == nil {
			t.Errorf("%s: 期望校验失败", tc.name)
		}
	}
}

// TestValidateRouteEntriesAcceptsFallbackChain 合法候选链必须放行，且链长有上限。
func TestValidateRouteEntriesAcceptsFallbackChain(t *testing.T) {
	ok := []routeEntry{{
		Alias:   "daily",
		Channel: channelCore,
		Model:   "global:deepseek-v4.1-flash",
		Fallbacks: []routeTarget{
			{Channel: channelAutoClaw, Model: "autoclaw-glm-5.3-flash"},
			{Channel: channelQoder, Model: "qwen3.8-flash"},
		},
		Strategy: strategyHealth,
		Enabled:  true,
	}}
	if err := validateRouteEntries(ok); err != nil {
		t.Fatalf("合法候选链被拒：%v", err)
	}
	// 空策略合法，按 priority 生效；未知策略同样退化为 priority。
	if entry := (routeEntry{Alias: "x", Channel: channelCore, Model: "cn:auto"}); entry.strategy() != strategyPriority {
		t.Fatalf("空策略应按 priority，得到 %q", entry.strategy())
	}
	if entry := (routeEntry{Strategy: "bogus"}); entry.strategy() != strategyPriority {
		t.Fatalf("未知策略应退化为 priority，得到 %q", entry.strategy())
	}
	// 超过上限要拒。
	tooMany := []routeEntry{{Alias: "x", Channel: channelCore, Model: "cn:auto", Enabled: true}}
	for i := 0; i < maxRouteTargets; i++ {
		tooMany[0].Fallbacks = append(tooMany[0].Fallbacks, routeTarget{Channel: channelQoder, Model: fmt.Sprintf("m%d", i)})
	}
	if err := validateRouteEntries(tooMany); err == nil {
		t.Fatalf("候选链超过 %d 跳应被拒", maxRouteTargets)
	}
}

// TestAliasFallbacksSurviveSaveAndReload 候选链与策略必须完整落盘并能重读，
// 否则重启一次热备配置就丢了。
func TestAliasFallbacksSurviveSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store := newRouteStore(path)
	want := routeEntry{
		Alias:   "daily",
		Channel: channelCore,
		Model:   "global:deepseek-v4.1-flash",
		Fallbacks: []routeTarget{
			{Channel: channelAutoClaw, Model: "autoclaw-glm-5.3-flash"},
			{Channel: channelQoder, Model: "qwen3.8-flash"},
		},
		Strategy: strategyHealth,
		Enabled:  true,
	}
	if err := store.save([]routeEntry{want}, ""); err != nil {
		t.Fatal(err)
	}
	reloaded := newRouteStore(path)
	got, ok := reloaded.lookup("daily")
	if !ok {
		t.Fatal("别名未落盘")
	}
	if got.strategy() != strategyHealth {
		t.Fatalf("策略丢失：%q", got.strategy())
	}
	if len(got.Fallbacks) != 2 {
		t.Fatalf("备用链路丢失：%+v", got.Fallbacks)
	}
	if got.Fallbacks[0] != (routeTarget{Channel: channelAutoClaw, Model: "autoclaw-glm-5.3-flash"}) {
		t.Fatalf("第 1 条备用错位：%+v", got.Fallbacks[0])
	}
	if got.Fallbacks[1] != (routeTarget{Channel: channelQoder, Model: "qwen3.8-flash"}) {
		t.Fatalf("第 2 条备用错位：%+v", got.Fallbacks[1])
	}
	// targets() 的链首必须仍是主目标。
	if got.targets()[0].publicModel() != "global:deepseek-v4.1-flash" {
		t.Fatalf("链首不是主目标：%+v", got.targets()[0])
	}
}

// TestRouteTargetPublicModelIsIdempotent 备用链的模型名允许写成裸名或已带公共
// 前缀的形态，两者必须换算成同一个公共名与同一个原生名——否则会拼出
// qoder-qoder-x 这种双前缀，或让探测结论永远匹配不上。
func TestRouteTargetPublicModelIsIdempotent(t *testing.T) {
	bare := routeTarget{Channel: channelQoder, Model: "qwen3.8-flash"}
	prefixed := routeTarget{Channel: channelQoder, Model: "qoder-qwen3.8-flash"}
	if bare.publicModel() != prefixed.publicModel() {
		t.Fatalf("公共名不一致：%q vs %q", bare.publicModel(), prefixed.publicModel())
	}
	if bare.publicModel() != "qoder-qwen3.8-flash" {
		t.Fatalf("公共名=%q", bare.publicModel())
	}
	if bare.nativeModel() != "qwen3.8-flash" || prefixed.nativeModel() != "qwen3.8-flash" {
		t.Fatalf("原生名不一致：%q vs %q", bare.nativeModel(), prefixed.nativeModel())
	}
	// core 的命名空间由上游自带，prefix 为空，两个名字都原样保留。
	core := routeTarget{Channel: channelCore, Model: "global:deepseek-v4.1-flash"}
	if core.publicModel() != "global:deepseek-v4.1-flash" || core.nativeModel() != "global:deepseek-v4.1-flash" {
		t.Fatalf("core 跳被改写：%q / %q", core.publicModel(), core.nativeModel())
	}
}

// TestAliasWithFallbacksAdvertisesPrimaryOnly /v1/models 只广告别名本身与主目标，
// 不把整条候选链摊开——客户端选的是别名，备用是服务端内部的事。
func TestAliasWithFallbacksAdvertisesPrimaryOnly(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:free","object":"model","credits":"x0.00"}]}`)
			return
		}
		fmt.Fprint(w, `{"core":true}`)
	}, nil)
	entry := routeEntry{
		Alias:     "daily",
		Channel:   channelCore,
		Model:     "cn:free",
		Fallbacks: []routeTarget{{Channel: channelCore, Model: "cn:free2"}},
		Strategy:  strategyHealth,
		Enabled:   true,
	}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("models: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			Alias       bool   `json:"alias"`
			RoutedModel string `json:"routed_model"`
		} `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("models body not JSON: %s", rec.Body)
	}
	var found bool
	for _, item := range list.Data {
		if item.ID == "daily" {
			found = true
			if !item.Alias || item.RoutedModel != "cn:free" {
				t.Fatalf("别名条目应只广告主目标：%+v", item)
			}
		}
		// 备用目标不该作为独立模型名出现在列表里。
		if item.ID == "cn:free2" {
			t.Fatalf("备用目标不应被广告：%s", rec.Body)
		}
	}
	if !found {
		t.Fatalf("别名未出现在 /v1/models：%s", rec.Body)
	}
}

// TestAliasWithoutFallbacksKeepsLegacyBehavior 不写 fallbacks/strategy 的存量条目
// 必须与引入候选链之前完全一致：恒用主目标、target index 为 0。
func TestAliasWithoutFallbacksKeepsLegacyBehavior(t *testing.T) {
	var mu sync.Mutex
	var seen string
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}, ocHealthHandler(t, `{"phase":"ready","modelResults":{
		"OC · Only":{"ok":true,"category":"available","durationMs":50}
	}}`, &seen, &mu))
	entry := routeEntry{Alias: "legacy", Channel: channelOpenCode, Model: "OC · Only", Enabled: true}
	if got := saveRoutes(t, h, []routeEntry{entry}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := chatViaAlias(t, h, "legacy", &seen, &mu)
	mu.Lock()
	defer mu.Unlock()
	if seen != "OC · Only" {
		t.Fatalf("存量条目应恒用主目标，实际 %q", seen)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "0" {
		t.Fatalf("target index=%q, want 0", got)
	}
}

// switchGroup 通过整组端点切换「一个真实模型 + 所有指向它的别名」。
func switchGroup(t *testing.T, h http.Handler, model string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := login(t, h)
	payload, err := json.Marshal(map[string]any{"model": model, "enabled": enabled})
	if err != nil {
		t.Fatal(err)
	}
	return adminRequest(h, "POST", "/admin/route/switch", string(payload), cookie, csrf)
}

// testAlias 通过别名测试端点对单个别名发一次最小对话。
func testAlias(t *testing.T, h http.Handler, alias string) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := login(t, h)
	payload, err := json.Marshal(map[string]any{"alias": alias})
	if err != nil {
		t.Fatal(err)
	}
	return adminRequest(h, "POST", "/admin/route/test", string(payload), cookie, csrf)
}

// readRouteAliases 读路由状态里的别名条目，保留原始 JSON 形态以便断言 enabled/probe。
func readRouteAliases(t *testing.T, h http.Handler) []map[string]any {
	t.Helper()
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "GET", "/admin/route", "", cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("read route: %d %s", rec.Code, rec.Body)
	}
	var state struct {
		Aliases []map[string]any `json:"aliases"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &state) != nil {
		t.Fatalf("route state not JSON: %s", rec.Body)
	}
	return state.Aliases
}

func aliasEnabled(t *testing.T, h http.Handler, alias string) bool {
	t.Helper()
	for _, entry := range readRouteAliases(t, h) {
		if entry["alias"] == alias {
			enabled, _ := entry["enabled"].(bool)
			return enabled
		}
	}
	t.Fatalf("alias %q not found", alias)
	return false
}

// TestRouteSwitchSyncsModelAndAliases 验证「整组启停」：模型列表与别名表共用同一个
// 写入口，点任一处，模型开关表与所有指向它的别名一起变；指向别的模型的别名不受牵连。
func TestRouteSwitchSyncsModelAndAliases(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:glm-5.3","credits":"x0.79"},{"id":"cn:free","credits":"x0.00"}]}`)
	}, nil)
	entries := []routeEntry{
		{Alias: "strong-a", Channel: channelCore, Model: "cn:glm-5.3"},
		{Alias: "strong-b", Channel: channelCore, Model: "cn:glm-5.3"},
		{Alias: "daily", Channel: channelCore, Model: "cn:free", Enabled: true},
	}
	if got := saveRoutes(t, h, entries, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if got := switchGroup(t, h, "cn:glm-5.3", true); got.Code != 200 {
		t.Fatalf("route/switch on: %d %s", got.Code, got.Body)
	}
	if overrides := readSwitch(t, h); overrides["cn:glm-5.3"] != true {
		t.Fatalf("模型开关表没有落盘: %v", overrides)
	}
	if !aliasEnabled(t, h, "strong-a") || !aliasEnabled(t, h, "strong-b") {
		t.Fatalf("指向该模型的别名必须跟着开")
	}
	if !aliasEnabled(t, h, "daily") {
		t.Fatalf("指向别的模型的别名不该被牵连")
	}
	// 反向：关掉模型，两个别名一起关，无关别名照旧。
	if got := switchGroup(t, h, "cn:glm-5.3", false); got.Code != 200 {
		t.Fatalf("route/switch off: %d %s", got.Code, got.Body)
	}
	if aliasEnabled(t, h, "strong-a") || aliasEnabled(t, h, "strong-b") {
		t.Fatalf("别名没有跟着模型一起关")
	}
	if !aliasEnabled(t, h, "daily") {
		t.Fatalf("切另一个模型时误改了无关别名")
	}
}

// TestRouteSwitchRejectsAutoAndBadNames 验证整组端点沿用模型开关的入参校验。
func TestRouteSwitchRejectsAutoAndBadNames(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[]}`)
	}, nil)
	if got := switchGroup(t, h, autoModelName, true); got.Code != 400 {
		t.Fatalf("auto 虚拟模型必须被拒: %d %s", got.Code, got.Body)
	}
	if got := switchGroup(t, h, "bad name!", true); got.Code != 400 {
		t.Fatalf("非法模型名必须被拒: %d %s", got.Code, got.Body)
	}
}

// TestRouteStateCarriesProbe 验证别名状态里带 probe 字段（没跑过探测时为 null）：
// 别名页的状态与速度列全靠它，缺字段会让页面把「未探测」渲染成 undefined。
func TestRouteStateCarriesProbe(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:free","credits":"x0.00"}]}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "daily", Channel: channelCore, Model: "cn:free", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	aliases := readRouteAliases(t, h)
	if len(aliases) != 1 {
		t.Fatalf("aliases=%v", aliases)
	}
	probe, ok := aliases[0]["probe"]
	if !ok {
		t.Fatalf("probe 字段缺失: %v", aliases[0])
	}
	if probe != nil {
		t.Fatalf("还没跑过探测，probe 应为 null: %v", probe)
	}
}

// TestRouteTestRejectsUnknownAndDisabled 验证别名测试在发出上游请求之前就判掉
// 「别名不存在」「别名已停用」，并把原因原样回给页面——这两种情况测试必然失败，
// 报清楚比报超时有用，也不该白白打一次上游。
func TestRouteTestRejectsUnknownAndDisabled(t *testing.T) {
	var mu sync.Mutex
	upstreamChats := 0
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/chat" {
			mu.Lock()
			upstreamChats++
			mu.Unlock()
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:free","credits":"x0.00"}]}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "off", Channel: channelCore, Model: "cn:free"}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	if got := testAlias(t, h, "ghost"); got.Code != 404 {
		t.Fatalf("不存在的别名应返回 404: %d %s", got.Code, got.Body)
	}
	got := testAlias(t, h, "off")
	if got.Code != 200 {
		t.Fatalf("停用别名的测试: %d %s", got.Code, got.Body)
	}
	var result map[string]any
	if json.Unmarshal(got.Body.Bytes(), &result) != nil {
		t.Fatalf("result not JSON: %s", got.Body)
	}
	if result["ok"] != false || result["category"] != "alias_disabled" {
		t.Fatalf("停用别名应报 alias_disabled: %v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if upstreamChats != 0 {
		t.Fatalf("被判掉的别名不该发出上游调用，实际 %d 次", upstreamChats)
	}
}

// TestRouteTestRunsMinimalChat 验证可用别名的测试真发一条流式最小对话，并按探测
// 口径带回 ok / 耗时，同时把结论写回探测缓存（别名表状态列随即能看到）。
func TestRouteTestRunsMinimalChat(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/chat" {
			if got := r.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("b", 32) {
				t.Errorf("bridge auth=%q", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":4}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:free","credits":"x0.00"}]}`)
	}, nil)
	if got := saveRoutes(t, h, []routeEntry{{Alias: "daily", Channel: channelCore, Model: "cn:free", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	got := testAlias(t, h, "daily")
	if got.Code != 200 {
		t.Fatalf("alias test: %d %s", got.Code, got.Body)
	}
	var result map[string]any
	if json.Unmarshal(got.Body.Bytes(), &result) != nil {
		t.Fatalf("result not JSON: %s", got.Body)
	}
	if result["ok"] != true {
		t.Fatalf("可用别名应报 ok: %v", result)
	}
	if _, ok := result["durationMs"]; !ok {
		t.Fatalf("结论必须带耗时: %v", result)
	}
	// 结论回写探测缓存：紧接着读路由状态就能看到同一份结论。
	aliases := readRouteAliases(t, h)
	if len(aliases) != 1 || aliases[0]["probe"] == nil {
		t.Fatalf("测试结论没有回写探测缓存: %v", aliases)
	}
}
