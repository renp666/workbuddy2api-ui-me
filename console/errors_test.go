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
	"testing"
	"time"
)

// 健康检查 M1 · 批 A 的验收对象是「错误事件落盘 + 聚合接口」（PRD §2-F1/F2、
// §4 红线、§5 判据 1）。这两条路径只在生产配置（HealthProbe=true + 可写 RouteFile）
// 下才会打开，测试里显式打开以覆盖真实落盘与聚合口径。

// errorConsole 建一个开启了健康检查模块（错误账本 + 出口观测）的 console。
//
// HealthProbe=true 是错误账本的总开关（见 NewServer）：生产由 configFromEnv 置真。
// RouteFile 指向临时目录，账本与 routes.json 同目录（容器内是 /app/console-data）。
func errorConsole(t *testing.T, core http.HandlerFunc) (http.Handler, *server, string) {
	t.Helper()
	coreTS := httptest.NewServer(core)
	t.Cleanup(coreTS.Close)
	cfg := testConfig(coreTS.URL)
	dir := t.TempDir()
	cfg.RouteFile = filepath.Join(dir, "routes.json")
	cfg.HealthProbe = true
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, h.(*server), dir
}

// readErrorRawLines 读出账本目录下所有 errors-*.jsonl 的原始行（按行解码成 map，
// 用于核对字段完整性，避免结构体 omitempty 把缺字段伪装成零值）。
func readErrorRawLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, "errors-*.jsonl"))
	var out []map[string]any
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			out = append(out, row)
		}
	}
	return out
}

// readErrorEvents 把账本原始行解码成 errorEvent（断言用）。
func readErrorEvents(t *testing.T, dir string) []errorEvent {
	t.Helper()
	var events []errorEvent
	for _, row := range readErrorRawLines(t, dir) {
		raw, _ := json.Marshal(row)
		var ev errorEvent
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		events = append(events, ev)
	}
	return events
}

// waitErrorEvents 轮询等待账本至少落盘 want 行。落账在独立协程里跑，必须轮询到
// 目标行数——顺带保证所有写协程都已结束，不会和 t.TempDir 的清理赛跑。
func waitErrorEvents(t *testing.T, dir string, want int) []errorEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := readErrorEvents(t, dir)
		if len(events) >= want {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("错误账本落盘不足：想要 %d 条，实际 %d 条", want, len(events))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// chatCore 是多数用例共用的 core 桩：/internal/v1/info 正常应答，/v1/models 空目录，
// 其余路径交给 fallback。让后台探活协程有确定的落脚点，不产生噪声。
func chatCore(t *testing.T, chat http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case mockInfo(w, r):
		case r.URL.Path == "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[]}`)
		default:
			chat(w, r)
		}
	}
}

// —— F2 归类映射逐项 ————————————————————————————————

// TestFailureCategoryMapping 逐项锁定统一归类口径（PRD §2-F2）：
// 401/403→auth、404→model_error、429→rate_limit、408/504→timeout、5xx→upstream_error、
// 错误体含 pinned_account_unavailable→pin、其余→error。前端已认识的四个键
// （auth/model_error/rate_limit/error）语义必须原样不变。
func TestFailureCategoryMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, "", "auth"},
		{http.StatusForbidden, "", "auth"},
		{http.StatusNotFound, "", "model_error"},
		{http.StatusTooManyRequests, "", "rate_limit"},
		{http.StatusRequestTimeout, "", "timeout"},
		{http.StatusGatewayTimeout, "", "timeout"},
		{http.StatusInternalServerError, "", "upstream_error"},
		{http.StatusBadGateway, "", "upstream_error"},
		{http.StatusServiceUnavailable, "", "upstream_error"},
		{http.StatusServiceUnavailable, `{"error":{"code":"pinned_account_unavailable"}}`, "pin"},
		// pin 优先于状态码：core 严格 pin 被锁时回 503，必须单独成类而不是 upstream_error。
		{http.StatusBadGateway, `{"error":{"code":"pinned_account_unavailable"}}`, "pin"},
		{http.StatusBadRequest, "", "error"},
		{0, "", "error"},
	}
	for _, c := range cases {
		if got := failureCategory(c.status, c.body); got != c.want {
			t.Errorf("failureCategory(%d, %q) = %q, want %q", c.status, c.body, got, c.want)
		}
	}
	// 前端 modelFailureLabels 已认识的键必须保持这四个字面量不变。
	for _, key := range []string{"auth", "model_error", "rate_limit", "error"} {
		if failureCategory(200, "") == key && key != "error" {
			t.Fatalf("保留键 %q 被改判", key)
		}
	}
}

// TestProbeUsesSharedFailureCategory 证明 probe 与调用侧共用同一份归类函数
// （PRD §2-F2「禁止两张皮」）：探测一个回 429 的上游，结论类别必须是 rate_limit，
// 而不是 probe 自己那套老口径。
func TestProbeUsesSharedFailureCategory(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
	}))
	t.Cleanup(up.Close)
	cfg := testConfig(up.URL)
	cfg.ZCodeURL = mustParseURL(t, up.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := h.(*server).probeOne(channelGLM, "glm-5.3")
	if result["category"] != "rate_limit" {
		t.Fatalf("probe 归类未共用统一口径：category=%v", result["category"])
	}
}

// —— F1 错误事件落盘 ————————————————————————————————

// TestErrorLedgerRecordsFailedCall 覆盖判据 1 前半：制造一次失败调用（core 回 429）
// → errors-*.jsonl 出现一行，字段与 PRD §2-F1 的单行 schema 一一对应；观测不得改变
// 调用主链路的状态码。
func TestErrorLedgerRecordsFailedCall(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// 留一点耗时，保证 duration_ms 非零（omitempty 下 0 会被省略）。
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("X-Fallback-Attempts", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limit exceeded, retry after 12s"}}`)
	}))

	rec := postChat(h, "/v1/chat/completions", `{"model":"cn:test","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("观测不得改变状态码：got %d", rec.Code)
	}

	events := waitErrorEvents(t, dir, 1)
	ev := events[0]
	if ev.Ts <= 0 {
		t.Fatalf("ts 缺失：%+v", ev)
	}
	if ev.Channel != string(channelCore) {
		t.Fatalf("channel=%q，期望 core", ev.Channel)
	}
	if ev.API != "chat" {
		t.Fatalf("api=%q，期望 chat", ev.API)
	}
	if ev.Mode != "sync" {
		t.Fatalf("mode=%q，期望 sync", ev.Mode)
	}
	if ev.UpstreamStatus != http.StatusTooManyRequests {
		t.Fatalf("upstream_status=%d，期望 429", ev.UpstreamStatus)
	}
	if ev.Category != "rate_limit" {
		t.Fatalf("category=%q，期望 rate_limit", ev.Category)
	}
	if !strings.Contains(ev.ErrorSnippet, "rate limit") {
		t.Fatalf("error_snippet=%q，期望含上游摘要", ev.ErrorSnippet)
	}
	if ev.DurationMs <= 0 {
		t.Fatalf("duration_ms=%d，期望 > 0", ev.DurationMs)
	}
	if !strings.HasPrefix(ev.Client, "192.0.2.1") {
		t.Fatalf("client=%q，期望含对端地址", ev.Client)
	}
	if ev.RoutedVia != "core" {
		t.Fatalf("routed_via=%q，期望 core", ev.RoutedVia)
	}
	if ev.ModelResolved != "cn:test" {
		t.Fatalf("model_resolved=%q，期望 cn:test", ev.ModelResolved)
	}
	if ev.RetryCount != 2 {
		t.Fatalf("retry_count=%d，期望 2（X-Fallback-Attempts）", ev.RetryCount)
	}

	// 字段完整性：按 PRD §2-F1 的单行字段逐项核对原始 JSON 键（不靠结构体零值）。
	raw := readErrorRawLines(t, dir)
	if len(raw) == 0 {
		t.Fatal("账本无原始行")
	}
	for _, key := range []string{"ts", "model_resolved", "channel", "api", "mode", "upstream_status", "category", "error_snippet", "duration_ms", "client", "routed_via", "retry_count"} {
		if _, ok := raw[0][key]; !ok {
			t.Fatalf("账本行缺少字段 %q：%v", key, raw[0])
		}
	}
}

// TestErrorLedgerRecordsStreamFailure 覆盖 mode 判定：请求体显式 stream=true 时
// 失败事件记 stream，与 sync 区分开（前端按模式拆错误分布要用）。
func TestErrorLedgerRecordsStreamFailure(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"quota\"}}\n\n")
	}))
	postChat(h, "/v1/chat/completions", `{"model":"cn:test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	events := waitErrorEvents(t, dir, 1)
	if events[0].Mode != "stream" {
		t.Fatalf("mode=%q，期望 stream", events[0].Mode)
	}
}

// TestErrorLedgerRecordsAliasAttribution 覆盖别名归因：命中 resolveAlias 时
// alias 记客户端原名、channel/model_resolved 记解析后的真实目标（取自 X-Prism-*）。
func TestErrorLedgerRecordsAliasAttribution(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	if got := saveRoutes(t, h, []routeEntry{{Alias: "fast", Channel: channelCore, Model: "cn:test", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	rec := postChat(h, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d %s", rec.Code, rec.Body)
	}
	events := waitErrorEvents(t, dir, 1)
	ev := events[0]
	if ev.Alias != "fast" {
		t.Fatalf("alias=%q，期望 fast", ev.Alias)
	}
	if ev.Channel != string(channelCore) {
		t.Fatalf("channel=%q，期望 core", ev.Channel)
	}
	if ev.ModelResolved != "cn:test" {
		t.Fatalf("model_resolved=%q，期望 cn:test", ev.ModelResolved)
	}
	if ev.Category != "model_error" {
		t.Fatalf("category=%q，期望 model_error", ev.Category)
	}
}

// TestErrorLedgerRedactsAndTruncatesSnippet 覆盖红线 3 与 200 字截断：
// 上游错误体里的凭据（api_key / Bearer / sk-）落盘前必须脱敏，且摘要 ≤200 字。
func TestErrorLedgerRedactsAndTruncatesSnippet(t *testing.T) {
	const secret = "supersecretvalue123"
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"bad key","api_key":"%s","hint":"Authorization: Bearer sk-abcdef123456"},"pad":"%s"}`,
			secret, strings.Repeat("x", 1000))
	}))
	postChat(h, "/v1/chat/completions", `{"model":"cn:test","messages":[{"role":"user","content":"hi"}]}`)
	events := waitErrorEvents(t, dir, 1)
	snippet := events[0].ErrorSnippet
	if strings.Contains(snippet, secret) {
		t.Fatalf("error_snippet 泄露凭据：%q", snippet)
	}
	if strings.Contains(snippet, "sk-abcdef123456") {
		t.Fatalf("error_snippet 泄露 sk- key：%q", snippet)
	}
	if !strings.Contains(snippet, "***") {
		t.Fatalf("error_snippet 未见脱敏标记：%q", snippet)
	}
	if runes := []rune(snippet); len(runes) > errorSnippetLimit {
		t.Fatalf("error_snippet 超过 %d 字：%d", errorSnippetLimit, len(runes))
	}
}

// TestSanitizeSnippetTruncatesAndRedacts 单测脱敏 + 截断的边界：
// 按 rune 截断（不把多字节字符截成半个），凭据形态逐项命中。
func TestSanitizeSnippetTruncatesAndRedacts(t *testing.T) {
	if got := []rune(sanitizeSnippet(strings.Repeat("字", 300))); len(got) != errorSnippetLimit {
		t.Fatalf("多字节截断长度=%d，期望 %d", len(got), errorSnippetLimit)
	}
	if got := []rune(sanitizeSnippet(strings.Repeat("a", 500))); len(got) != errorSnippetLimit {
		t.Fatalf("ASCII 截断长度=%d，期望 %d", len(got), errorSnippetLimit)
	}
	cases := []struct {
		in         string
		notContain string
	}{
		{`Authorization: Bearer abcdef123456`, "abcdef123456"},
		{`x-api-key: zzzzzzzzzzzzz`, "zzzzzzzzzzzzz"},
		{`{"api_key":"supersecretvalue"}`, "supersecretvalue"},
		{`{"token":"abcdefghijklmno"}`, "abcdefghijklmno"},
		{`upstream rejected key sk-abcdef123456 here`, "sk-abcdef123456"},
		{`contact user@example.com for help`, "user@example.com"},
	}
	for _, c := range cases {
		got := sanitizeSnippet(c.in)
		if strings.Contains(got, c.notContain) {
			t.Errorf("sanitizeSnippet(%q) = %q，仍含 %q", c.in, got, c.notContain)
		}
	}
}

// —— /admin/errors 聚合口径 ————————————————————————————————

// TestAdminErrorsAggregatesWindow 覆盖判据 1 后半：窗口内成功/失败计数、错误率、
// 按通道/类别/模型的分布与最近失败流水；聚合失败数与账本行数对得上。
func TestAdminErrorsAggregatesWindow(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &env)
		if env.Model == "cn:bad" {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"too many"}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	// 2 成功 + 1 失败，全部经公共出口，进同一个滚动窗口（成功是错误率的分母）。
	for _, model := range []string{"cn:ok", "cn:ok", "cn:bad"} {
		postChat(h, "/v1/chat/completions", chatBody(model))
	}
	waitErrorEvents(t, dir, 1)

	cookie, csrf := login(t, h)
	var got struct {
		Window     string              `json:"window"`
		OK         int                 `json:"ok"`
		Fail       int                 `json:"fail"`
		ErrorRate  float64             `json:"error_rate"`
		Channels   []errorChannelStat  `json:"channels"`
		Categories []errorCategoryStat `json:"categories"`
		Recent     []errorEvent        `json:"recent"`
	}
	w := adminRequest(h, "GET", "/admin/errors?window=15m", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("errors status=%d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Window != "15m" || got.OK != 2 || got.Fail != 1 {
		t.Fatalf("聚合口径错误：window=%q ok=%d fail=%d", got.Window, got.OK, got.Fail)
	}
	if got.ErrorRate != 0.3333 {
		t.Fatalf("error_rate=%v，期望 0.3333", got.ErrorRate)
	}
	// 聚合失败数与账本行数对得上（判据 1 的核心一致性要求）。
	if events := readErrorEvents(t, dir); len(events) != got.Fail {
		t.Fatalf("账本行数 %d 与聚合失败数 %d 对不上", len(events), got.Fail)
	}
	if len(got.Channels) != 1 || got.Channels[0].Channel != "core" {
		t.Fatalf("channels=%+v，期望仅 core 一行", got.Channels)
	}
	core := got.Channels[0]
	if core.OK != 2 || core.Fail != 1 || core.ErrorRate != 0.3333 {
		t.Fatalf("core 行错误：%+v", core)
	}
	if core.Categories["rate_limit"] != 1 {
		t.Fatalf("core.categories=%v，期望 rate_limit=1", core.Categories)
	}
	if len(core.TopModels) == 0 || core.TopModels[0].Model != "cn:bad" {
		t.Fatalf("core.top_models=%+v，期望失败最多的 cn:bad 居首", core.TopModels)
	}
	if len(got.Categories) != 1 || got.Categories[0].Category != "rate_limit" || got.Categories[0].Fail != 1 || got.Categories[0].Share != 1 {
		t.Fatalf("categories=%+v", got.Categories)
	}
	if len(got.Recent) != 1 || got.Recent[0].Category != "rate_limit" {
		t.Fatalf("recent=%+v，期望 1 条 rate_limit", got.Recent)
	}

	// 三档窗口都接受，默认 15m，未知窗口拒绝。
	for _, window := range []string{"1h", "24h"} {
		if w := adminRequest(h, "GET", "/admin/errors?window="+window, "", cookie, csrf); w.Code != 200 {
			t.Fatalf("window=%s status=%d", window, w.Code)
		}
	}
	if w := adminRequest(h, "GET", "/admin/errors", "", cookie, csrf); w.Code != 200 || !strings.Contains(w.Body.String(), `"window":"15m"`) {
		t.Fatalf("默认窗口应为 15m：%d %s", w.Code, w.Body)
	}
	if w := adminRequest(h, "GET", "/admin/errors?window=7d", "", cookie, csrf); w.Code != 400 {
		t.Fatalf("未知窗口应 400，got %d %s", w.Code, w.Body)
	}
	// 管理端点必须鉴权。
	if w := adminRequest(h, "GET", "/admin/errors?window=15m", "", nil, ""); w.Code != 401 {
		t.Fatalf("未鉴权应 401，got %d", w.Code)
	}
}

// TestErrorWindowForParsesKnownKeys 锁定窗口解析：只认 15m/1h/24h。
func TestErrorWindowForParsesKnownKeys(t *testing.T) {
	for key, want := range map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour} {
		if got, ok := errorWindowFor(key); !ok || got != want {
			t.Fatalf("errorWindowFor(%q)=%v,%v 期望 %v", key, got, ok, want)
		}
	}
	if _, ok := errorWindowFor("7d"); ok {
		t.Fatal("未知窗口不应被接受")
	}
}

// —— 门控与透传 ————————————————————————————————

// TestErrorLedgerDisabledWithoutHealthProbe 锁定门控：模块总开关关闭时不建任何文件，
// 且失败调用仍原样透传（出口行为与没有本功能时完全一致）。
func TestErrorLedgerDisabledWithoutHealthProbe(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mockInfo(w, r) {
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"nope"}}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	dir := t.TempDir()
	cfg.RouteFile = filepath.Join(dir, "routes.json")
	// 不设 HealthProbe：模块关闭。
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postChat(h, "/v1/chat/completions", chatBody("cn:test")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码应原样透传：got %d", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if matches, _ := filepath.Glob(filepath.Join(dir, "errors-*.jsonl")); len(matches) != 0 {
		t.Fatalf("模块关闭时不得落盘：%v", matches)
	}
}

// TestExitObservationPassesResponseThrough 锁定红线 1：观测绝不改变状态码、响应头与
// 响应体（成功响应零缓冲透传）。
func TestExitObservationPassesResponseThrough(t *testing.T) {
	const payload = `{"choices":[{"message":{"content":"hello"}}]}`
	h, _, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "kept")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, payload)
	}))
	rec := postChat(h, "/v1/chat/completions", chatBody("cn:test"))
	if rec.Code != http.StatusOK || rec.Body.String() != payload {
		t.Fatalf("响应被观测改动：%d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Custom") != "kept" {
		t.Fatalf("响应头被观测改动：%q", rec.Header().Get("X-Custom"))
	}
}
