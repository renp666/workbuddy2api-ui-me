package server

import (
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// withWafIPWindow 注入 IP 级判定窗（生产恒 60s）。改的是包级变量，本文件内的测试
// 一律不得 t.Parallel()，且必须等窗口断言结束再由 Cleanup 复原。
func withWafIPWindow(t *testing.T, d time.Duration) {
	t.Helper()
	prev := wafIPWindow
	wafIPWindow = d
	t.Cleanup(func() { wafIPWindow = prev })
}

// TestWafIPGateMultiAccountTriggers 状态机单测：窗内两个不同号命中即激活；
// 单号反复命中（任意多次）不触发；激活期内 noteWaf 恒 true。
func TestWafIPGateMultiAccountTriggers(t *testing.T) {
	withWafIPWindow(t, time.Minute)
	var g wafIPGate
	for n := 0; n < 10; n++ {
		if g.noteWaf("u1") {
			t.Fatalf("single account repeated hits must never trigger, iter=%d", n)
		}
	}
	if g.active() {
		t.Fatal("single account must not activate")
	}
	// 第二个不同号进入窗内 → 本次调用即达阈值激活并返回 true（fail-fast 生效点
	// 就是阈值命中的那次请求）。
	if !g.noteWaf("u2") {
		t.Fatal("threshold crossing call must activate and return true")
	}
	if !g.active() {
		t.Fatal("two distinct accounts within window must activate")
	}
	if !g.noteWaf("u3") {
		t.Fatal("hits during active window must report active")
	}
}

// TestWafIPGateWindowExpiry 窗口过期自然解除 + 激活期内不续期 + 解除后旧账已清。
func TestWafIPGateWindowExpiry(t *testing.T) {
	withWafIPWindow(t, 150*time.Millisecond)
	var g wafIPGate
	g.noteWaf("u1")
	g.noteWaf("u2")
	if !g.active() {
		t.Fatal("must activate")
	}
	if !g.noteWaf("u3") {
		t.Fatal("mid-window hit must report active")
	}
	time.Sleep(200 * time.Millisecond)
	if g.active() {
		t.Fatal("gate must deactivate after window expiry")
	}
	// 激活时判定窗被清空：解除后单号命中不残留旧账，不立即再激活。
	if g.noteWaf("u4") {
		t.Fatal("post-expiry single hit must not re-activate (hits cleared on activation)")
	}
	if g.active() {
		t.Fatal("post-expiry single hit must leave gate inactive")
	}
}

// TestWafIPGateConcurrent 并发安全（T1-e，须配 -race 跑）：noteWaf/active 混发，
// 只要求不变量「不 panic、不死锁、激活后 active 恒真直到窗口过期」，不断言具体
// 号数（并发下命中顺序不确定）。
func TestWafIPGateConcurrent(t *testing.T) {
	withWafIPWindow(t, 20*time.Millisecond)
	var g wafIPGate
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for k := 0; k < 200; k++ {
				g.noteWaf(string(rune('a' + n)))
				_ = g.active()
			}
		}(i)
	}
	wg.Wait()
}

// TestChatWafIPFailFastStopsRotation 端到端止损验收（T1-d）：3 个账号全部 WAF
// 403 → 第二个号命中即激活 IP 级状态 → 第三个号零调用。MaxRotate 默认 3，
// 非 fail-fast 路径会打满 3 次（放大倍数 3），这里必须恰好 2 次。
//
// 与上游版本的口径差异（最小化回移，交接书 §4.4）：本仓不回移 34405ca 的账号级
// WAF 软冷却，ErrWafBlock 走 applyErrorPolicy 的 default 分支「只换号不罚」，
// 因此三个号都不该出现冷却/禁用——IP 级状态只改「是否继续轮转」。
func TestChatWafIPFailFastStopsRotation(t *testing.T) {
	withWafIPWindow(t, time.Minute)
	var calls atomic.Int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls.Add(1)
		return 403, "", false // WAF 拦截：裸 403 空 body
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u3", AccessToken: "at3", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if n := calls.Load(); n != 2 {
		t.Fatalf("upstream calls=%d want 2 (fail-fast stops rotation at threshold; u3 must not be hit)", n)
	}
	if rec.Code != 503 {
		t.Fatalf("code=%d want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "waf ip-level block") {
		t.Errorf("IP 级激活期的末端文案应可读，实际: %s", rec.Body)
	}
	if !h.wafIP.active() {
		t.Error("gate must stay active after the request (window has not elapsed)")
	}
	for _, uid := range []string{"u1", "u2", "u3"} {
		st, _ := p.Status(uid)
		if st.Cooling || st.Disabled {
			t.Errorf("uid=%s 不应被冷却/禁用（本仓 WAF 无账号级惩罚）: %+v", uid, st)
		}
	}
}

// TestChatWafSingleAccountStillRotates 单号 WAF 403 不触发 IP 级：轮转继续换到
// 健康号并成功——既有行为零回归。
func TestChatWafSingleAccountStillRotates(t *testing.T) {
	withWafIPWindow(t, time.Minute)
	var calls atomic.Int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls.Add(1)
		if authz == "Bearer at-bad" {
			return 403, "", false // 仅此号被拦
		}
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s (单号 WAF 必须继续轮转到健康号)", rec.Code, rec.Body)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("calls=%d want 2 (bad 一次 + good 一次)", n)
	}
	if h.wafIP.active() {
		t.Error("单号命中不得激活 IP 级状态")
	}
}

// TestChatWafIPGateClearsAfterExpiryThenRotates 窗口过期解除的端到端证据：
// 激活 → 窗口过期 → 单号 WAF 403 不再 fail-fast，恢复到既有的换号行为。
func TestChatWafIPGateClearsAfterExpiryThenRotates(t *testing.T) {
	withWafIPWindow(t, 150*time.Millisecond)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 403, "", false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec.Code != 503 {
		t.Fatalf("stage1 code=%d want 503", rec.Code)
	}
	if !h.wafIP.active() {
		t.Fatal("stage1 must activate the IP gate")
	}
	time.Sleep(200 * time.Millisecond)
	if h.wafIP.active() {
		t.Fatal("IP gate must deactivate after the window expires")
	}
	// 阶段二：窗口解除后只有 u1 被拦（u2 健康）→ 必须换号成功。
	up2 := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at1" {
			return 403, "", false
		}
		return 200, sseOK, true
	})
	h.cfg.Upstream = up2 // 复用同一 Handler / 同一 IP 状态机（验的是过期解除，不是新实例）
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))
	if rec2.Code != 200 {
		t.Fatalf("stage2 code=%d body=%s (窗口解除后不得再 fail-fast)", rec2.Code, rec2.Body)
	}
}
