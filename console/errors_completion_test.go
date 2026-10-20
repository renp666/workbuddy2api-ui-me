package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// —— 完成成功率双口径（§三 #4）——

// TestCompletionRateAggregate 锁定聚合口径：produced 只在「HTTP 成功且非空流」样本上
// 累计；完成成功率分母是全部调用（ok+fail）；平均耗时含失败样本。
func TestCompletionRateAggregate(t *testing.T) {
	w := newOutcomeWindow()
	base := time.Now().UnixMilli()
	// 三样本：正常成功（produced）、空流成功（非 produced）、失败。
	w.add(errorEvent{Ts: base, Channel: "core", ModelResolved: "m", DurationMs: 100, Produced: true}, true)
	w.add(errorEvent{Ts: base + 1, Channel: "core", ModelResolved: "m", DurationMs: 200, Produced: false}, true)
	w.add(errorEvent{Ts: base + 2, Channel: "core", ModelResolved: "m", DurationMs: 300}, false)
	agg := w.collect(24 * time.Hour)
	if agg.total.ok != 2 || agg.total.fail != 1 {
		t.Fatalf("计数不符：ok=%d fail=%d", agg.total.ok, agg.total.fail)
	}
	if agg.total.produced != 1 {
		t.Fatalf("produced=%d，want 1", agg.total.produced)
	}
	if got := agg.total.completionRate(); got < 0.3332 || got > 0.3334 {
		t.Fatalf("completionRate=%v，want 1/3", got)
	}
	if agg.durationCount != 3 || agg.totalDurationMs != 600 {
		t.Fatalf("耗时累计不符：count=%d sum=%d", agg.durationCount, agg.totalDurationMs)
	}
	// 通道维度的 produced 同步累计。
	if s := agg.channels["core"]; s.produced != 1 {
		t.Fatalf("通道维度 produced=%d，want 1", s.produced)
	}
}

// TestAdminErrorsCompletionFields 锁定 /admin/errors 透出的三个新字段：
// produced_ok / completion_rate / avg_duration_ms。
func TestAdminErrorsCompletionFields(t *testing.T) {
	h, srv, _ := errorConsole(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	base := time.Now().UnixMilli()
	srv.outcomes.add(errorEvent{Ts: base, Channel: "core", ModelResolved: "m", DurationMs: 100, Produced: true}, true)
	srv.outcomes.add(errorEvent{Ts: base + 1, Channel: "core", ModelResolved: "m", DurationMs: 300}, false)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/errors?window=15m", "", cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"produced_ok":1`, `"completion_rate":0.5`, `"avg_duration_ms":200`} {
		if !strings.Contains(body, want) {
			t.Fatalf("聚合响应缺 %s：%s", want, body)
		}
	}
}

// TestAdminErrorsEmptyWindowNullDuration 锁定空窗口：avg_duration_ms=null、produced_ok=0。
func TestAdminErrorsEmptyWindowNullDuration(t *testing.T) {
	h, _, _ := errorConsole(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/errors?window=15m", "", cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"avg_duration_ms":null`) {
		t.Fatalf("空窗口 avg_duration_ms 应为 null：%s", body)
	}
	if !strings.Contains(body, `"produced_ok":0`) {
		t.Fatalf("空窗口 produced_ok 应为 0：%s", body)
	}
}

// TestFinishExitObserveWiresRequestIDAndProduced 锁定观测归档贯穿：
// 成功样本 Produced/RequestID 进窗口；空流成功不计 produced；失败样本 RequestID
// 随账本落盘。
func TestFinishExitObserveWiresRequestIDAndProduced(t *testing.T) {
	_, srv, dir := errorConsole(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv.finishExitObserve(exitObservation{status: 200, wroteBytes: 42, durationMs: 10, requestID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	agg := srv.outcomes.collect(15 * time.Minute)
	if agg.total.ok != 1 || agg.total.produced != 1 {
		t.Fatalf("成功样本应 produced：ok=%d produced=%d", agg.total.ok, agg.total.produced)
	}
	// 空流成功：HTTP 200 但 produced=false。
	srv.finishExitObserve(exitObservation{status: 200, wroteBytes: 80, emptyStream: true, durationMs: 10, requestID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	agg = srv.outcomes.collect(15 * time.Minute)
	if agg.total.ok != 2 || agg.total.produced != 1 {
		t.Fatalf("空流样本不应 produced：ok=%d produced=%d", agg.total.ok, agg.total.produced)
	}
	// 失败样本：RequestID 落盘（异步），Produced 不进窗口统计。
	srv.finishExitObserve(exitObservation{status: 503, body: `{"error":{"code":"x"}}`, durationMs: 5, requestID: "cccccccccccccccccccccccccccccccc"})
	waitLedgerIdle(t)
	lines := readErrorRawLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("失败账本应恰好 1 行，得 %d", len(lines))
	}
	if lines[0]["request_id"] != "cccccccccccccccccccccccccccccccc" {
		t.Fatalf("账本行缺 request_id：%v", lines[0])
	}
}

// waitLedgerIdle 等待 recordErrorAsync 协程落盘完成（与既有测试同样的节拍）。
func waitLedgerIdle(t *testing.T) { t.Helper(); time.Sleep(50 * time.Millisecond) }
