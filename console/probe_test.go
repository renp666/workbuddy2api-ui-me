package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// probe_test.go 覆盖手工测速探测链路：管理鉴权与参数校验、无可达模型与在途
// 拒绝、core 探测的成功/失败结论形态、结论参与 gateway-auto 选路，以及旁路
// 状态端点附带 health.modelResults。

// sseOK 回一条带可见内容、延迟后附 usage 的流式响应；20ms 生成间隔让
// tokensPerSec 可计算（ttft 与总耗时不同秒级塌陷）。
func sseOK(w http.ResponseWriter, completionTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	time.Sleep(20 * time.Millisecond)
	if completionTokens > 0 {
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":%d}}\n\n", completionTokens)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// sseEmpty 模拟推理预算耗尽：无 content、finish_reason=length。
func sseEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// readProbeBody 取出探测请求里的模型名与原始 JSON 文本。
func readProbeBody(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &env) != nil {
		t.Fatalf("probe body not JSON: %s", raw)
	}
	return env.Model, string(raw)
}

// waitForProbe 轮询 /admin/probe 直到本轮结束，返回最终 snapshot。
func waitForProbe(t *testing.T, h http.Handler, cookie *http.Cookie, csrf string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec := adminRequest(h, "GET", "/admin/probe", "", cookie, csrf)
		if rec.Code != 200 {
			t.Fatalf("probe state: %d %s", rec.Code, rec.Body)
		}
		var snap map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &snap) != nil {
			t.Fatalf("probe state not JSON: %s", rec.Body)
		}
		if snap["running"] == false {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// snapshotChannel 从 snapshot 里取某通道的按模型结论表。
func snapshotChannel(t *testing.T, snap map[string]any, channel string) map[string]any {
	t.Helper()
	channels, _ := snap["channels"].(map[string]any)
	byModel, _ := channels[channel].(map[string]any)
	return byModel
}

func TestProbeAdminAuthAndValidation(t *testing.T) {
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}, nil)
	if rec := adminRequest(h, "GET", "/admin/probe", "", nil, ""); rec.Code != 401 {
		t.Fatalf("unauthenticated GET: %d", rec.Code)
	}
	cookie, csrf := login(t, h)
	if rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["core"]}`, cookie, ""); rec.Code != 403 {
		t.Fatalf("missing csrf: %d", rec.Code)
	}
	rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["opencode"]}`, cookie, csrf)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "只能是 core、glm 或 qoder") {
		t.Fatalf("bad channel: %d %s", rec.Code, rec.Body)
	}
}

func TestProbeRejectsWhenNoReachableModels(t *testing.T) {
	// core 目录不可达且未配置旁路通道：start 必须报「没有可探测的模型」，
	// 而不是起一个空任务数的运行器。
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}, nil)
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["core"]}`, cookie, csrf)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "没有可探测的模型") {
		t.Fatalf("unreachable start: %d %s", rec.Code, rec.Body)
	}
}

func TestProbeCoreResultsAndAutoRouting(t *testing.T) {
	var mu sync.Mutex
	var probeAuth string
	var probeRaw string
	var chatModel string
	h, cfg := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:fast","credits":"x0.00"},{"id":"cn:slow","credits":"x0.00"}]}`)
		case "/internal/v1/chat":
			model, raw := readProbeBody(t, r)
			mu.Lock()
			probeAuth = r.Header.Get("Authorization")
			probeRaw = raw
			mu.Unlock()
			if model == "cn:slow" {
				sseEmpty(w)
				return
			}
			sseOK(w, 10)
		case "/v1/chat/completions":
			mu.Lock()
			chatModel = modelOf(t, r)
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}, nil)
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["core"]}`, cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	snap := waitForProbe(t, h, cookie, csrf)
	if snap["running"] != false {
		t.Fatal("probe still running")
	}
	if snap["done"] != snap["total"] || snap["total"] != float64(2) {
		t.Fatalf("progress=%v/%v", snap["done"], snap["total"])
	}
	byModel := snapshotChannel(t, snap, "core")
	fast, _ := byModel["cn:fast"].(map[string]any)
	if fast == nil || fast["ok"] != true || fast["source"] != "probe" {
		t.Fatalf("fast result=%v", byModel["cn:fast"])
	}
	if tps, _ := fast["tokensPerSec"].(float64); tps <= 0 {
		t.Fatalf("fast tokensPerSec=%v", fast["tokensPerSec"])
	}
	if ttft, _ := fast["ttftMs"].(float64); ttft < 0 {
		t.Fatalf("fast ttftMs=%v", fast["ttftMs"])
	}
	if tokens, _ := fast["outputTokens"].(float64); tokens != 10 {
		t.Fatalf("fast outputTokens=%v", fast["outputTokens"])
	}
	slow, _ := byModel["cn:slow"].(map[string]any)
	if slow == nil || slow["ok"] != false || slow["category"] != "empty" || slow["source"] != "probe" {
		t.Fatalf("slow result=%v", byModel["cn:slow"])
	}
	mu.Lock()
	auth, raw, seen := probeAuth, probeRaw, chatModel
	mu.Unlock()
	if auth != "Bearer "+cfg.BridgeKey {
		t.Fatalf("probe auth=%q", auth)
	}
	if !strings.Contains(raw, `"stream":true`) || !strings.Contains(raw, `"max_tokens":256`) {
		t.Fatalf("probe request not streaming budget: %s", raw)
	}
	if seen != "" {
		t.Fatalf("probe must not touch public chat path, saw %q", seen)
	}

	// 探测结论参与 gateway-auto 选路：cn:fast 仅对话档可用，cn:slow 失败档
	// 被排除；auto 必须落到 fast 而不是回退。
	rec = gateChat(h, "gateway-auto")
	if rec.Code != 200 {
		t.Fatalf("auto chat: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	got := chatModel
	mu.Unlock()
	if got != "cn:fast" {
		t.Fatalf("auto routed to %q", got)
	}
	if rec.Header().Get("X-Prism-Fallback") != "" {
		t.Fatal("usable probe candidate must not be flagged fallback")
	}
	if rec.Header().Get("X-Prism-Routed-Model") != "cn:fast" {
		t.Fatalf("routed header=%q", rec.Header().Get("X-Prism-Routed-Model"))
	}
}

func TestProbeRejectsConcurrentStart(t *testing.T) {
	release := make(chan struct{})
	h, _ := routeConsole(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:one","credits":"x0.00"}]}`)
		case "/internal/v1/chat":
			readProbeBody(t, r)
			<-release
			sseOK(w, 5)
		default:
			fmt.Fprint(w, `{}`)
		}
	}, nil)
	cookie, csrf := login(t, h)
	rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["core"]}`, cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("first start: %d %s", rec.Code, rec.Body)
	}
	rec = adminRequest(h, "POST", "/admin/probe", `{"channels":["core"]}`, cookie, csrf)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "已有探测正在进行") {
		t.Fatalf("second start: %d %s", rec.Code, rec.Body)
	}
	close(release)
	snap := waitForProbe(t, h, cookie, csrf)
	byModel := snapshotChannel(t, snap, "core")
	if result, _ := byModel["cn:one"].(map[string]any); result == nil || result["ok"] != true {
		t.Fatalf("one result=%v", byModel["cn:one"])
	}
}

func TestProbeSideChannelStatusHealth(t *testing.T) {
	var mu sync.Mutex
	var zcodeModel, zcodeAuth, qoderModel, qoderAuth string
	zcodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-x"}]}`)
		case "/v1/chat/completions":
			model, _ := readProbeBody(t, r)
			mu.Lock()
			zcodeModel, zcodeAuth = model, r.Header.Get("Authorization")
			mu.Unlock()
			sseOK(w, 8)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(zcodeTS.Close)
	qoderTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"y"}]}`)
		case "/v1/chat/completions":
			model, _ := readProbeBody(t, r)
			mu.Lock()
			qoderModel, qoderAuth = model, r.Header.Get("Authorization")
			mu.Unlock()
			sseOK(w, 8)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(qoderTS.Close)
	coreTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	t.Cleanup(coreTS.Close)

	cfg := testConfig(coreTS.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	cfg.ZCodeURL = mustParseURL(t, zcodeTS.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	cfg.QoderURL = mustParseURL(t, qoderTS.URL)
	cfg.QoderKey = strings.Repeat("q", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}

	cookie, csrf := login(t, h)
	rec := adminRequest(h, "POST", "/admin/probe", `{"channels":["glm","qoder"]}`, cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	snap := waitForProbe(t, h, cookie, csrf)
	glm := snapshotChannel(t, snap, "glm")
	if result, _ := glm["glm-x"].(map[string]any); result == nil || result["ok"] != true {
		t.Fatalf("glm result=%v", glm["glm-x"])
	}
	qoder := snapshotChannel(t, snap, "qoder")
	if result, _ := qoder["qoder-y"].(map[string]any); result == nil || result["ok"] != true {
		t.Fatalf("qoder result=%v", qoder["qoder-y"])
	}
	mu.Lock()
	zm, za, qm, qa := zcodeModel, zcodeAuth, qoderModel, qoderAuth
	mu.Unlock()
	// glm 原生名自带前缀，原样发送；qoder 公共前缀必须剥掉再发上游。
	if zm != "glm-x" {
		t.Fatalf("zcode probe model=%q", zm)
	}
	if za != "Bearer "+cfg.ZCodeKey {
		t.Fatalf("zcode probe auth=%q", za)
	}
	if qm != "y" {
		t.Fatalf("qoder probe model=%q", qm)
	}
	if qa != "Bearer "+cfg.QoderKey {
		t.Fatalf("qoder probe auth=%q", qa)
	}

	// 旁路状态端点把结论以 health.modelResults（公共模型名为键）附带下发。
	rec = adminRequest(h, "GET", "/admin/zcode", "", cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("zcode status: %d %s", rec.Code, rec.Body)
	}
	var zstatus struct {
		Health struct {
			ModelResults map[string]map[string]any `json:"modelResults"`
		} `json:"health"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &zstatus) != nil {
		t.Fatalf("zcode status not JSON: %s", rec.Body)
	}
	if result := zstatus.Health.ModelResults["glm-x"]; result == nil || result["ok"] != true || result["source"] != "probe" {
		t.Fatalf("zcode health=%v", zstatus.Health.ModelResults)
	}

	rec = adminRequest(h, "GET", "/admin/qoder", "", cookie, csrf)
	if rec.Code != 200 {
		t.Fatalf("qoder status: %d %s", rec.Code, rec.Body)
	}
	var qstatus struct {
		Health struct {
			ModelResults map[string]map[string]any `json:"modelResults"`
		} `json:"health"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &qstatus) != nil {
		t.Fatalf("qoder status not JSON: %s", rec.Body)
	}
	if result := qstatus.Health.ModelResults["qoder-y"]; result == nil || result["ok"] != true || result["source"] != "probe" {
		t.Fatalf("qoder health=%v", qstatus.Health.ModelResults)
	}
}
