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
	"time"
)

// usageReportSink 在 core 替身里挂上桥接上报接收器，记录收到的每次上报。
type usageReportSink struct {
	mu     sync.Mutex
	ips    []map[string]any // 每次上报（解析后的 entries 列表）
	auths  []string
	reject int // 非零时对上报回该状态码，用于验证失败不影响主链路
}

func (s *usageReportSink) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/internal/v1/usage/reports" {
		fmt.Fprint(w, `{"core":true}`)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	if s.reject != 0 {
		w.WriteHeader(s.reject)
		fmt.Fprint(w, `{"error":"rejected"}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var payload struct {
		Entries []map[string]any `json:"entries"`
	}
	if json.Unmarshal(body, &payload) != nil {
		w.WriteHeader(400)
		return
	}
	w.WriteHeader(202)
	fmt.Fprint(w, `{"accepted":`+fmt.Sprint(len(payload.Entries))+`}`)
	s.ips = append(s.ips, payload.Entries...)
}

func (s *usageReportSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ips)
}

// waitFor 等待异步上报到达（goroutine 上报，主响应先返回）。
func (s *usageReportSink) waitFor(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := append([]map[string]any(nil), s.ips...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("usage report did not arrive: got %d want %d", s.count(), n)
	return nil
}

// usagePair 起一个带 core（含上报接收器）与旁路上游的 console；
// channel 为 "zcode"、"qoder" 或 "opencode"，决定上游挂到哪个分流配置。
func usagePair(t *testing.T, sink *usageReportSink, channel string, upstream http.HandlerFunc) http.Handler {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(sink.handle))
	t.Cleanup(core.Close)
	proxy := httptest.NewServer(upstream)
	t.Cleanup(proxy.Close)
	cfg := testConfig(core.URL)
	switch channel {
	case "qoder":
		cfg.QoderURL = mustParseURL(t, proxy.URL)
		cfg.QoderKey = strings.Repeat("q", 32)
	case "opencode":
		cfg.OpenCodeURL = mustParseURL(t, proxy.URL)
		cfg.OpenCodeKey = strings.Repeat("o", 32)
	default:
		cfg.ZCodeURL = mustParseURL(t, proxy.URL)
		cfg.ZCodeKey = strings.Repeat("z", 32)
	}
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func postChat(h http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://console.test"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer client-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUsageReportZcodeSyncAndStream(t *testing.T) {
	sink := &usageReportSink{}
	// 同步 JSON 与流式 SSE 各自携带 usage；上游收到同一公共模型名（glm- 前缀保留）。
	var gotStream bool
	var mu sync.Mutex
	h := usagePair(t, sink, "zcode", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &env)
		mu.Lock()
		gotStream = env.Stream
		mu.Unlock()
		if r.URL.Path == "/v1/messages" {
			// Anthropic 风格：message_start + message_delta 组合。
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n")
			return
		}
		if env.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":9}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"credit":0.25}}`)
	})

	// 同步 JSON：tokens + credit。
	w := postChat(h, "/v1/chat/completions", `{"model":"glm-5.3-flash","messages":[]}`)
	if w.Code != 200 {
		t.Fatalf("sync call: %d %s", w.Code, w.Body)
	}
	entries := sink.waitFor(t, 1)
	e := entries[len(entries)-1]
	if e["uid"] != "zcode" || e["account"] != "GLM 通道" || e["model"] != "glm-5.3-flash" || e["mode"] != "sync" {
		t.Fatalf("sync entry = %v", e)
	}
	if e["prompt_tokens"] != float64(3) || e["completion_tokens"] != float64(4) {
		t.Fatalf("sync tokens = %v", e)
	}
	if v, ok := e["credit"].(float64); !ok || v != 0.25 {
		t.Fatalf("sync credit = %v", e)
	}

	// 流式 SSE（OpenAI 末帧聚合）。
	sink.mu.Lock()
	before := len(sink.ips)
	sink.mu.Unlock()
	w = postChat(h, "/v1/chat/completions", `{"model":"glm-5.3-flash","stream":true,"messages":[]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("stream call: %d %s", w.Code, w.Body)
	}
	entries = sink.waitFor(t, before+1)
	e = entries[len(entries)-1]
	if e["mode"] != "stream" {
		t.Fatalf("stream entry mode = %v", e)
	}
	if e["prompt_tokens"] != float64(5) || e["completion_tokens"] != float64(9) {
		t.Fatalf("stream tokens = %v", e)
	}
	if _, hasCredit := e["credit"]; hasCredit {
		t.Fatalf("stream entry must omit credit: %v", e)
	}

	// Anthropic SSE 组合：/v1/messages。
	sink.mu.Lock()
	before = len(sink.ips)
	sink.mu.Unlock()
	w = postChat(h, "/v1/messages", `{"model":"glm-5.3-flash","stream":true,"max_tokens":8,"messages":[]}`)
	if w.Code != 200 {
		t.Fatalf("messages call: %d %s", w.Code, w.Body)
	}
	entries = sink.waitFor(t, before+1)
	e = entries[len(entries)-1]
	if e["prompt_tokens"] != float64(11) || e["completion_tokens"] != float64(7) {
		t.Fatalf("anthropic tokens = %v", e)
	}

	mu.Lock()
	defer mu.Unlock()
	if !gotStream {
		t.Fatalf("upstream never saw stream=true")
	}
}

func TestUsageReportQoderKeepsPublicModelName(t *testing.T) {
	sink := &usageReportSink{}
	h := usagePair(t, sink, "qoder", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// qoder 上游收到的是剥前缀后的模型名。
		var env struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &env)
		if env.Model != "qwen3.8-flash" {
			t.Errorf("upstream model = %q want stripped name", env.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":6,"completion_tokens":2}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"qoder-qwen3.8-flash","messages":[]}`)
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	entries := sink.waitFor(t, 1)
	e := entries[0]
	// 上报里的模型名必须保留公共前缀，便于与 zcode/core 区分。
	if e["uid"] != "qoder" || e["account"] != "Qoder 通道" || e["model"] != "qoder-qwen3.8-flash" {
		t.Fatalf("qoder entry = %v", e)
	}
	if e["prompt_tokens"] != float64(6) || e["completion_tokens"] != float64(2) {
		t.Fatalf("qoder tokens = %v", e)
	}
}

func TestUsageReportOpenCodeKeepsPublicModelName(t *testing.T) {
	sink := &usageReportSink{}
	h := usagePair(t, sink, "opencode", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// opencode 上游收到的是剥前缀后的模型名。
		var env struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &env)
		if env.Model != "OC · Free" {
			t.Errorf("upstream model = %q want stripped name", env.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":8,"completion_tokens":3}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"opencode-OC · Free","messages":[]}`)
	if w.Code != 200 {
		t.Fatalf("call: %d %s", w.Code, w.Body)
	}
	entries := sink.waitFor(t, 1)
	e := entries[0]
	// 上报里的模型名必须保留公共前缀，便于与 zcode/core 区分。
	if e["uid"] != "opencode" || e["account"] != "OpenCode 通道" || e["model"] != "opencode-OC · Free" {
		t.Fatalf("opencode entry = %v", e)
	}
	if e["prompt_tokens"] != float64(8) || e["completion_tokens"] != float64(3) {
		t.Fatalf("opencode tokens = %v", e)
	}
}

func TestUsageReportMissingUsageStaysSilent(t *testing.T) {
	sink := &usageReportSink{}
	h := usagePair(t, sink, "zcode", func(w http.ResponseWriter, r *http.Request) {
		// 上游错误响应：没有 usage，失败调用不计入统计。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"glm-5.3-flash","messages":[]}`)
	if w.Code != 502 {
		t.Fatalf("call: %d", w.Code)
	}
	time.Sleep(200 * time.Millisecond)
	if got := sink.count(); got != 0 {
		t.Fatalf("failed call must not report usage, got %d entries", got)
	}
}

func TestUsageReportRejectedDoesNotBreakCall(t *testing.T) {
	sink := &usageReportSink{reject: 500}
	h := usagePair(t, sink, "zcode", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"glm-5.3-flash","messages":[]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"prompt_tokens":1`) {
		t.Fatalf("call must succeed despite rejected report: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		got := len(sink.auths)
		sink.mu.Unlock()
		if got >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.auths) == 0 {
		t.Fatalf("report attempt never reached core")
	}
}

func TestUsageReportCorePathDoesNotDoubleCount(t *testing.T) {
	sink := &usageReportSink{}
	// cn: 命名空间模型走 core；core 替身对普通路径回带 usage 的响应，
	// 若 console 误把 core 路径也捕获上报，就会出现双计。
	h := usagePair(t, sink, "zcode", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected zcode hit: %s", r.URL)
	})
	w := postChat(h, "/v1/chat/completions", `{"model":"cn:glm-5.2","messages":[]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `{"core":true}`) {
		t.Fatalf("core call: %d %s", w.Code, w.Body)
	}
	time.Sleep(200 * time.Millisecond)
	if got := sink.count(); got != 0 {
		t.Fatalf("core path must not produce usage reports, got %d", got)
	}
}
