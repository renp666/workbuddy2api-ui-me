package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// retryUpstream 是一台可编程 httptest 上游：记录调用次数与本跳收到的模型名，
// 按预设状态码/响应体/Retry-After 应答，供请求内重试用例驱动各分支。
type retryUpstream struct {
	mu         sync.Mutex
	calls      int
	seenModel  string
	code       int
	body       string
	retryAfter string
}

func (u *retryUpstream) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &envelope)
		u.mu.Lock()
		u.calls++
		u.seenModel = envelope.Model
		code, body, retryAfter := u.code, u.body, u.retryAfter
		u.mu.Unlock()
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func (u *retryUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *retryUpstream) model() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seenModel
}

// retryFixture 建一个 core + opencode 双通道 console，别名 daily = core/cn:bad
// （主）+ opencode/OC · Backup（备用），两跳构成最小可重试候选链。
func retryFixture(t *testing.T, core, oc *retryUpstream) (http.Handler, *server) {
	t.Helper()
	coreTS := httptest.NewServer(core.handler())
	t.Cleanup(coreTS.Close)
	cfg := testConfig(coreTS.URL)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	ocTS := httptest.NewServer(oc.handler())
	t.Cleanup(ocTS.Close)
	cfg.OpenCodeURL = mustParseURL(t, ocTS.URL)
	cfg.OpenCodeKey = strings.Repeat("o", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := h.(*server)
	srv.outcomes = newOutcomeWindow()
	entry := routeEntry{Alias: "daily", Channel: channelCore, Model: "cn:bad", Enabled: true,
		Fallbacks: []routeTarget{{Channel: channelOpenCode, Model: "OC · Backup"}}}
	if err := srv.routes.save([]routeEntry{entry}, ""); err != nil {
		t.Fatal(err)
	}
	return h, srv
}

// TestRetrySwitchesOn429 锁定核心拍板口径：主目标 429（冷却类失败）在首字节前
// 暂扣切换，客户端拿到备用的 200；失败跳落错误账本（category=rate_limit、
// Retry-After 解析、RetryCount=1），响应头反映实际服务的跳与尝试数。
func TestRetrySwitchesOn429(t *testing.T) {
	core := &retryUpstream{code: 429, body: `{"error":{"message":"rate limited"}}`, retryAfter: "7"}
	oc := &retryUpstream{body: `{"oc":true}`}
	h, srv := retryFixture(t, core, oc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("daily"))))
	if rec.Code != 200 || rec.Body.String() != `{"oc":true}` {
		t.Fatalf("应切到备用拿 200，实际 %d %s", rec.Code, rec.Body)
	}
	if got := core.count(); got != 1 {
		t.Fatalf("core 调用次数=%d, want 1", got)
	}
	if got := oc.count(); got != 1 {
		t.Fatalf("opencode 调用次数=%d, want 1", got)
	}
	if got := core.model(); got != "cn:bad" {
		t.Fatalf("core 应收到改写后的主目标模型名 %q", got)
	}
	if got := oc.model(); got != "OC · Backup" {
		t.Fatalf("opencode 应收到剥前缀后的原生模型名 %q", got)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "opencode" {
		t.Fatalf("X-Prism-Channel=%q, want opencode", got)
	}
	if got := rec.Header().Get("X-Prism-Routed-Model"); got != "opencode-OC · Backup" {
		t.Fatalf("X-Prism-Routed-Model=%q", got)
	}
	if got := rec.Header().Get("X-Prism-Target-Index"); got != "1" {
		t.Fatalf("X-Prism-Target-Index=%q, want 1", got)
	}
	if got := rec.Header().Get("X-Prism-Attempts"); got != "2" {
		t.Fatalf("X-Prism-Attempts=%q, want 2", got)
	}
	if reason := rec.Header().Get("X-Prism-Reason"); !strings.Contains(reason, "重试切第 2 跳") {
		t.Fatalf("Reason 应注明切换：%q", reason)
	}
	srv.outcomes.mu.Lock()
	defer srv.outcomes.mu.Unlock()
	if len(srv.outcomes.recent) != 1 {
		t.Fatalf("失败账本应恰好一条，实际 %d", len(srv.outcomes.recent))
	}
	ev := srv.outcomes.recent[0]
	if ev.Category != "rate_limit" || ev.RetryCount != 1 || ev.RetryAfterSec != 7 {
		t.Fatalf("失败事件口径不符: %+v", ev)
	}
	if ev.Alias != "daily" || ev.Channel != "core" || ev.ModelResolved != "cn:bad" || ev.UpstreamStatus != 429 {
		t.Fatalf("失败事件归属不符: %+v", ev)
	}
}

// TestRetryDoesNotSwitchOn401 锁定 4xx 口径：auth 类失败只告警不切换，客户端
// 拿到上游原文，备用跳完全不被调用（省一次注定无意义的上游请求）。
func TestRetryDoesNotSwitchOn401(t *testing.T) {
	core := &retryUpstream{code: 401, body: `{"error":"auth failed"}`}
	oc := &retryUpstream{body: `{"oc":true}`}
	h, _ := retryFixture(t, core, oc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("daily"))))
	if rec.Code != 401 || rec.Body.String() != `{"error":"auth failed"}` {
		t.Fatalf("401 应原样透传，实际 %d %s", rec.Code, rec.Body)
	}
	if got := oc.count(); got != 0 {
		t.Fatalf("auth 失败不应调用备用跳，opencode 被调了 %d 次", got)
	}
	if got := core.count(); got != 1 {
		t.Fatalf("core 调用次数=%d, want 1", got)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "core" {
		t.Fatalf("X-Prism-Channel=%q, want core", got)
	}
}

// TestRetryNoSwitchOnStreamingSuccess 锁定「首字节后不换链」：主目标 200 的流式
// 响应原样透传，备用跳不被调用——开始输出后再切只会造成重复计费与重复回答。
func TestRetryNoSwitchOnStreamingSuccess(t *testing.T) {
	core := &retryUpstream{body: "data: {\"delta\":\"hello\"}\n\ndata: [DONE]\n\n"}
	oc := &retryUpstream{body: `{"oc":true}`}
	h, _ := retryFixture(t, core, oc)
	rec := httptest.NewRecorder()
	body := `{"model":"daily","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("流式响应应原样透传，实际 %d %s", rec.Code, rec.Body)
	}
	if got := oc.count(); got != 0 {
		t.Fatalf("成功流不应触发切换，opencode 被调了 %d 次", got)
	}
}

// TestRetryExhaustsChainPassesThroughLastFailure 锁定全链失败的兜底：最后一跳的
// 真实上游错误（状态码与错误体）透传给客户端，不编造网关错误包；尝试数如实上报。
func TestRetryExhaustsChainPassesThroughLastFailure(t *testing.T) {
	core := &retryUpstream{code: 429, body: `{"error":"limited"}`}
	oc := &retryUpstream{code: 503, body: `{"error":"oc down"}`}
	h, _ := retryFixture(t, core, oc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("daily"))))
	if rec.Code != 503 || rec.Body.String() != `{"error":"oc down"}` {
		t.Fatalf("应透传最后一跳的 503，实际 %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("X-Prism-Attempts"); got != "2" {
		t.Fatalf("X-Prism-Attempts=%q, want 2", got)
	}
	if got := core.count(); got != 1 {
		t.Fatalf("core 调用次数=%d, want 1", got)
	}
	if got := oc.count(); got != 1 {
		t.Fatalf("opencode 调用次数=%d, want 1", got)
	}
}

// TestDirectModelNoRetry 锁定非别名请求零回归：直名模型不经候选链，上游失败
// 原样返回（现状行为），也不该出现任何 X-Prism 决策头。
func TestDirectModelNoRetry(t *testing.T) {
	core := &retryUpstream{code: 429, body: `{"error":"limited"}`}
	oc := &retryUpstream{body: `{"oc":true}`}
	h, _ := retryFixture(t, core, oc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("cn:glm-5.2"))))
	if rec.Code != 429 {
		t.Fatalf("直名模型应原样拿到 429，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("X-Prism-Channel"); got != "" {
		t.Fatalf("非别名请求不应有决策头，实际 %q", got)
	}
	if got := core.count(); got != 1 {
		t.Fatalf("core 调用次数=%d, want 1", got)
	}
	if got := oc.count(); got != 0 {
		t.Fatalf("非别名请求不应触达备用跳，opencode 被调了 %d 次", got)
	}
}

// TestCircuitSkipsAuthFailures 锁定冷却准入（§九 9.3）：auth 类失败不计 streak、
// 不触发熔断，只走告警账本。
func TestCircuitSkipsAuthFailures(t *testing.T) {
	h, _ := circuitFixture(t)
	circuitSave(t, h, "daily")
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "auth")
	}
	if h.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("auth 失败不应触发熔断")
	}
	// 成功照旧复位 streak 的能力不受影响。
	circuitFeed(h, true, "upstream_error")
}

// TestCircuitRetryAfterCooldown 锁定 429 感知冷却：Retry-After 有头时该目标按头
// 定时（封顶 30min），到期即恢复，而不是一律等满 30min。
func TestCircuitRetryAfterCooldown(t *testing.T) {
	h, advance := circuitFixture(t)
	circuitSave(t, h, "daily")
	for i := 0; i < 3; i++ {
		h.outcomes.add(errorEvent{Ts: h.outcomes.now().UnixMilli(), Alias: "daily", Channel: "core",
			ModelResolved: "cn:bad", Category: "rate_limit", RetryAfterSec: 120}, false)
	}
	circuitWait(t, func() bool { return h.circuits.blocked(routeTarget{channelCore, "cn:bad"}) })
	h.circuits.mu.RLock()
	state := h.circuits.states[circuitKey(routeTarget{channelCore, "cn:bad"})]
	h.circuits.mu.RUnlock()
	if state.CooldownMs != 120000 {
		t.Fatalf("冷却时长应取 Retry-After 120s，实际 %dms", state.CooldownMs)
	}
	advance(3 * time.Minute)
	circuitWait(t, func() bool { return !h.circuits.blocked(routeTarget{channelCore, "cn:bad"}) })
}

// TestParseRetryAfter 覆盖 Retry-After 三种形态：整数秒、HTTP-date、非法值。
func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("120"); got != 120 {
		t.Fatalf("秒数解析=%d, want 120", got)
	}
	if got := parseRetryAfter(" 30 "); got != 30 {
		t.Fatalf("带空白的秒数=%d, want 30", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Fatalf("负数应拒绝=%d", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Fatalf("垃圾值应拒绝=%d", got)
	}
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 100 || got > 130 {
		t.Fatalf("HTTP-date 应换算为秒=%d", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Fatalf("过去时间应得 0=%d", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Fatalf("空值应得 0=%d", got)
	}
}
