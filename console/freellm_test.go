package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type freellmPair struct {
	coreHandler    http.HandlerFunc
	freellmHandler http.HandlerFunc
}

func testConsoleWithFreellm(t *testing.T, pair freellmPair) (http.Handler, *httptest.Server) {
	t.Helper()
	core := httptest.NewServer(pair.coreHandler)
	t.Cleanup(core.Close)
	fl := httptest.NewServer(pair.freellmHandler)
	t.Cleanup(fl.Close)
	cfg := testConfig(core.URL)
	cfg.FreellmURL = mustParseURL(t, fl.URL)
	cfg.FreellmKey = strings.Repeat("f", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, fl
}

func TestFreellmPrefixRoutesToFreellm(t *testing.T) {
	var coreHits, flHits int
	var mu sync.Mutex
	var flAuth, flBody, flAPIKey, flOrigin, flCookie string
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		freellmHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			flHits++
			mu.Unlock()
			flAuth = r.Header.Get("Authorization")
			flAPIKey = r.Header.Get("X-Api-Key")
			flOrigin = r.Header.Get("Origin")
			flCookie = r.Header.Get("Cookie")
			body, _ := io.ReadAll(r.Body)
			flBody = string(body)
			fmt.Fprint(w, `{"freellm":true}`)
		},
	})
	// freellm- 前缀分流到 FreeLLMAPI；带 Origin（浏览器客户端）与部署 API Key 也不得泄漏。
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("freellm-gemini-3-flash")))
	req.Header.Set("Origin", "http://console.test")
	req.Header.Set("Authorization", "Bearer deployment-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if flHits != 1 || coreHits != 0 {
		t.Fatalf("freellm- 前缀应分流到 FreeLLMAPI：flHits=%d coreHits=%d", flHits, coreHits)
	}
	if flAuth != "Bearer "+strings.Repeat("f", 32) {
		t.Fatalf("Authorization=%q, want Bearer <FreellmKey>", flAuth)
	}
	if flAPIKey != "" || flOrigin != "" || flCookie != "" {
		t.Fatalf("私有头泄漏：X-Api-Key=%q Origin=%q Cookie=%q", flAPIKey, flOrigin, flCookie)
	}
	if !strings.Contains(flBody, `"model":"gemini-3-flash"`) {
		t.Fatalf("模型前缀未剥除：%s", flBody)
	}
}

func TestFreellmNonPrefixedStaysOnCore(t *testing.T) {
	var coreHits, flHits int
	var mu sync.Mutex
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		freellmHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			flHits++
			mu.Unlock()
			fmt.Fprint(w, `{"freellm":true}`)
		},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("cn:auto")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if coreHits != 1 || flHits != 0 {
		t.Fatalf("无前缀请求应留在 core：coreHits=%d flHits=%d", coreHits, flHits)
	}
}

func TestFreellmAdminChatRequiresPrefix(t *testing.T) {
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler:    func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: func(w http.ResponseWriter, r *http.Request) {},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "POST", "/admin/freellm/chat", chatBody("cn:auto"), cookie, csrf)
	if w.Code != 400 {
		t.Fatalf("非 freellm- 前缀的对话测试应 400，got %d %s", w.Code, w.Body)
	}
}

func TestFreellmStatusMergesCatalog(t *testing.T) {
	// 可达：目录条目带 freellm- 前缀与 realm。
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":[{"id":"gemini-3-flash"},{"id":"groq-kimi-k2.5"}]}`)
		},
	})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/freellm", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	var got struct {
		Enabled    bool             `json:"enabled"`
		Reachable  bool             `json:"reachable"`
		ModelCount int              `json:"model_count"`
		Models     []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || !got.Reachable || got.ModelCount != 2 {
		t.Fatalf("enabled=%v reachable=%v count=%d", got.Enabled, got.Reachable, got.ModelCount)
	}
	for _, m := range got.Models {
		id, _ := m["id"].(string)
		if !strings.HasPrefix(id, "freellm-") {
			t.Fatalf("目录条目应补公共前缀，got %q", id)
		}
		if m["realm"] != "freellm" {
			t.Fatalf("realm=%v, want freellm", m["realm"])
		}
	}
}

func TestFreellmStatusDisabledWhenUnconfigured(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/freellm", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != false {
		t.Fatalf("未配置时 enabled 应为 false，got %v", got["enabled"])
	}
}

func TestFreellmUsageCaptureLabelsChannel(t *testing.T) {
	sink := &usageReportSink{}
	var mu sync.Mutex
	upstreamSeen := ""
	h := usagePair(t, sink, "freellm", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamSeen = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"freellm-test-model","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(upstreamSeen, strings.Repeat("f", 32)) {
		t.Fatalf("上游未收到 FreellmKey，got %q", upstreamSeen)
	}
	entries := sink.waitFor(t, 1)
	e := entries[0]
	// 上报里的模型名必须保留公共前缀，账号名为固定占位译名。
	if e["uid"] != "freellm" || e["account"] != "FreeLLMAPI 通道" || e["model"] != "freellm-test-model" {
		t.Fatalf("freellm entry = %v", e)
	}
	if e["prompt_tokens"] != float64(8) || e["completion_tokens"] != float64(3) {
		t.Fatalf("freellm tokens = %v", e)
	}
}

func TestFreellmHealthReachableViaLivez(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	h := healthConsole(t, stubs, false)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, item := range got.Channels {
		if item.Channel != channelFreellm {
			continue
		}
		found = true
		if !item.Enabled || !item.Reachable {
			t.Fatalf("freellm enabled=%v reachable=%v, want both true（/livez 应答即算可达）", item.Enabled, item.Reachable)
		}
	}
	if !found {
		t.Fatal("总览缺少 freellm 条目")
	}
}

// 池子没配齐时端口依然是活的：reachable 保持 true，原因走 Detail 说清楚，
// 让总览的红灯自带处置口径（「没配供应商 key」而不是「服务挂了」）。
func TestFreellmHealthExplainsPoolNotReady(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	stubs[channelFreellm] = healthChannelStub{
		path: "/livez", body: `{"status":"ok"}`, status: 200,
		extra: map[string]healthChannelStub{
			"/readyz": {path: "/readyz", body: `{"status":"unavailable","reason":"no_upstreams_configured"}`, status: 503},
		},
	}
	h := healthConsole(t, stubs, false)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range got.Channels {
		if item.Channel != channelFreellm {
			continue
		}
		if !item.Reachable {
			t.Fatalf("sidecar 活着应答就该 reachable=true，got %v", item)
		}
		if !strings.Contains(item.Detail, "no_upstreams_configured") {
			t.Fatalf("Detail=%q，要带出上游给的原因档", item.Detail)
		}
		return
	}
	t.Fatal("总览缺少 freellm 条目")
}

func TestFreellmChannelInRouteValidation(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := saveRoutes(t, h, []routeEntry{{Alias: "fl", Channel: channelFreellm, Model: "gemini-3-flash", Enabled: true}}, "")
	if w.Code != 200 {
		t.Fatalf("freellm 通道别名应可保存，got %d %s", w.Code, w.Body)
	}
}

func TestFreellmReservedPrefixBlocksAlias(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := saveRoutes(t, h, []routeEntry{{Alias: "freellm-evil", Channel: channelFreellm, Model: "x", Enabled: true}}, "")
	if w.Code == 200 {
		t.Fatalf("freellm- 是保留前缀，别名不得占用")
	}
}

// freellmStub 按路径分派 FreeLLMAPI sidecar 的应答。上游真实形状实测于
// v0.14.2 活体：/livez 与 /readyz 免鉴权、/v1/* 要统一 key。
func freellmStub(routes map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if strings.HasPrefix(body, "503 ") {
			w.WriteHeader(503)
			fmt.Fprint(w, strings.TrimPrefix(body, "503 "))
			return
		}
		fmt.Fprint(w, body)
	}
}

func decodeFreellmStatus(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/freellm", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// 可达性与池就绪是两件事：/livez 答「进程能服务」，/readyz 答「至少一个上游可用」。
// 目录 200 两者都不证明（实测 259 条里 257 条 needsKey，调用必 503）。
func TestFreellmStatusReportsPoolNotReady(t *testing.T) {
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: freellmStub(map[string]string{
			"/livez":        `{"status":"ok","version":"0.4.1","uptime_s":1}`,
			"/readyz":       `503 {"status":"unavailable","reason":"no_upstreams_configured"}`,
			"/v1/models":    `{"data":[{"id":"grok-free","execution_status":"ready"}]}`,
			"/v1/providers": `{"providers":[],"counts":{"healthy":0,"rate_limited":0,"invalid":0,"unknown":0}}`,
		}),
	})
	got := decodeFreellmStatus(t, h)
	if got["reachable"] != true {
		t.Fatalf("reachable=%v，sidecar 活着应答就该算可达", got["reachable"])
	}
	if got["pool_ready"] != false {
		t.Fatalf("pool_ready=%v，/readyz 503 时必须为 false", got["pool_ready"])
	}
	if got["pool_reason"] != "no_upstreams_configured" {
		t.Fatalf("pool_reason=%v，要把上游给的原因档透出来", got["pool_reason"])
	}
}

func TestFreellmStatusReportsPoolReady(t *testing.T) {
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: freellmStub(map[string]string{
			"/livez":        `{"status":"ok"}`,
			"/readyz":       `{"status":"ok","ready_upstreams":3}`,
			"/v1/models":    `{"data":[{"id":"grok-free","execution_status":"ready"}]}`,
			"/v1/providers": `{"providers":[],"counts":{"healthy":3,"rate_limited":0,"invalid":0,"unknown":0}}`,
		}),
	})
	got := decodeFreellmStatus(t, h)
	if got["pool_ready"] != true {
		t.Fatalf("pool_ready=%v, want true", got["pool_ready"])
	}
	if _, present := got["pool_reason"]; present {
		t.Fatalf("就绪时不该编出一个原因档：%v", got["pool_reason"])
	}
	if n, _ := got["ready_upstreams"].(float64); n != 3 {
		t.Fatalf("ready_upstreams=%v, want 3", got["ready_upstreams"])
	}
}

// 上游 /v1/providers 的真实形状是「逐平台 status + 顶层 counts」。
// 之前按 healthy_keys/rate_limited_keys 求和读的是不存在的字段，页签恒缺读数。
func TestFreellmStatusSurfacesProviderPool(t *testing.T) {
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: freellmStub(map[string]string{
			"/livez":     `{"status":"ok"}`,
			"/readyz":    `{"status":"ok","ready_upstreams":2}`,
			"/v1/models": `{"data":[{"id":"grok-free","execution_status":"ready"}]}`,
			"/v1/providers": `{"providers":[{"platform":"groq","name":"Groq","status":"healthy","keys":2},` +
				`{"platform":"openrouter","name":"OpenRouter","status":"rate_limited","keys":1,"resume_at":"2026-10-16T09:00:00.000Z","requests_remaining_pct":0}],` +
				`"counts":{"healthy":1,"rate_limited":1,"invalid":0,"unknown":0}}`,
		}),
	})
	got := decodeFreellmStatus(t, h)
	providers, _ := got["providers"].([]any)
	if len(providers) != 2 {
		t.Fatalf("providers=%v，要按平台逐条透出", got["providers"])
	}
	first, _ := providers[0].(map[string]any)
	if first["platform"] != "groq" || first["status"] != "healthy" {
		t.Fatalf("providers[0]=%v", first)
	}
	second, _ := providers[1].(map[string]any)
	if second["resume_at"] == nil || second["requests_remaining_pct"] == nil {
		t.Fatalf("限流平台要带恢复时间与余量：%v", second)
	}
	counts, _ := got["provider_counts"].(map[string]any)
	if counts == nil || counts["healthy"] != float64(1) || counts["rate_limited"] != float64(1) {
		t.Fatalf("provider_counts=%v，直接采用上游顶层 counts", got["provider_counts"])
	}
}

// 目录裁剪：needsKey/exhausted 不进对外清单（它们此刻必然失败），
// 虚拟条目（auto/fusion，上游不发 execution_status）保留由调用时判定。
// 总条数与可调用条数分开给，页面才能解释「259 条目录、0 条可用」。
func TestFreellmStatusTrimsCatalogToCallableModels(t *testing.T) {
	h, _ := testConsoleWithFreellm(t, freellmPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {},
		freellmHandler: freellmStub(map[string]string{
			"/livez":        `{"status":"ok"}`,
			"/readyz":       `{"status":"ok","ready_upstreams":1}`,
			"/v1/providers": `{"providers":[],"counts":{"healthy":1,"rate_limited":0,"invalid":0,"unknown":0}}`,
			"/v1/models": `{"data":[{"id":"auto","available":true},` +
				`{"id":"grok-free","execution_status":"ready","context_window":128000,"supported_parameters":["tools","temperature"]},` +
				`{"id":"gemini-3-flash","execution_status":"needsKey","unavailable_reason":"no_key"},` +
				`{"id":"glm-5.3","execution_status":"exhausted"}]}`,
		}),
	})
	got := decodeFreellmStatus(t, h)
	models, _ := got["models"].([]any)
	var ids []string
	for _, m := range models {
		entry, _ := m.(map[string]any)
		ids = append(ids, fmt.Sprint(entry["id"]))
	}
	if strings.Join(ids, ",") != "freellm-auto,freellm-grok-free" {
		t.Fatalf("裁剪后 ids=%v，want [freellm-auto freellm-grok-free]", ids)
	}
	if got["model_count"] != float64(4) {
		t.Fatalf("model_count=%v, want 4（上游目录总条数，不隐瞒）", got["model_count"])
	}
	if got["callable_count"] != float64(2) {
		t.Fatalf("callable_count=%v, want 2", got["callable_count"])
	}
	for _, m := range models {
		entry, _ := m.(map[string]any)
		if entry["realm"] != "freellm" {
			t.Fatalf("realm=%v", entry["realm"])
		}
		if entry["id"] == "freellm-grok-free" {
			if entry["context_window"] != float64(128000) {
				t.Fatalf("context_window=%v，目录的能力面信息要留给页面", entry["context_window"])
			}
			params, _ := entry["supported_parameters"].([]any)
			if len(params) != 2 {
				t.Fatalf("supported_parameters=%v", entry["supported_parameters"])
			}
		}
	}
}

// 别名选择器与探测种子也必须用裁剪后的同一份清单，否则又会把通道钉成假红灯。
func TestFreellmChannelCatalogExcludesUncallableModels(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(core.Close)
	fl := httptest.NewServer(freellmStub(map[string]string{
		"/v1/models": `{"data":[{"id":"auto","available":true},` +
			`{"id":"grok-free","execution_status":"ready"},` +
			`{"id":"gemini-3-flash","execution_status":"needsKey"}]}`,
	}))
	t.Cleanup(fl.Close)
	cfg := testConfig(core.URL)
	cfg.FreellmURL = mustParseURL(t, fl.URL)
	cfg.FreellmKey = strings.Repeat("f", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids, ok := h.(*server).fetchChannelModels(context.Background(), channelFreellm)
	if !ok {
		t.Fatal("目录抓取应判为成功")
	}
	if strings.Join(ids, ",") != "freellm-auto,freellm-grok-free" {
		t.Fatalf("ids=%v，needsKey 不得进入别名候选与探测种子", ids)
	}
}

// 上游 REQUEST_BODY_LIMIT_MB 默认 25 且在路由前就 413（不进 fallback）。
// 网关缓冲得比上游更宽只会把「注定失败的大请求」伪装成网关问题。
func TestFreellmRouteBodyLimitStaysUnderUpstreamDefault(t *testing.T) {
	const upstreamDefault = 25 << 20
	if freellmRouteBodyLimit >= upstreamDefault {
		t.Fatalf("分流上限 %d 不低于上游默认 %d，超体请求会在上游路由前直接 413", freellmRouteBodyLimit, upstreamDefault)
	}
}

// 交接文档把「X-Routed-Via 透传日志」划给 console：账本只有 token 数，
// 谁服务了这次调用、是否走了备选链、命中缓存没有，只能从响应头取证。
func TestFreellmCallLogsRoutingAttribution(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	sink := &usageReportSink{}
	h := usagePair(t, sink, "freellm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Routed-Via", "groq/llama-3.3-70b")
		w.Header().Set("X-Fallback-Attempts", "2")
		w.Header().Set("X-Fallback-Trail", "openrouter:429,groq:200")
		w.Header().Set("X-FreeLLM-Cache", "MISS")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"freellm-llama","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	logged := buf.String()
	for _, want := range []string{"freellm-llama", "groq/llama-3.3-70b", "openrouter:429,groq:200", "MISS"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("归因日志缺少 %q，实际日志：%s", want, logged)
		}
	}
}
