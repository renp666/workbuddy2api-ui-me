package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// 补丁 0012b 回归：流前瞬态失败同号短退避重试。
// 覆盖单账号池与粘性号场景——换号重试（MaxRotate）在这些场景无意义。

// TestChatSameAccountRetryOnSoftRate 单账号首次 429、重发成功 → 200，同号两次调用，
// 且首次 429 未被罚冷却（重发在 applyErrorPolicy 之前吸收）。
// 注：本网关非流式请求也期望上游返回 SSE（handler 用 Aggregate 聚合成 JSON），
// 故成功分支返回 sseOK + isStream=true（与既有 TestChatRotatesOnHardCredit 一致）。
func TestChatSameAccountRetryOnSoftRate(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return 429, `{"code":6004,"msg":"rate limited"}`, false
		}
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, RetrySameAccount: 2, RetryBackoff: time.Millisecond, SoftCooldown: time.Minute})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("same-account attempts=%d want 2", got)
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Errorf("transient 429 absorbed by retry must not cool account: %+v", st)
	}
}

// TestChatSameAccountRetryExhaustedAppliesPolicy 两次都 429 → 耗尽后走既有策略：
// 软冷却 + 末端 429（单账号池软限流终态是 rate_limit_exceeded，非 503），
// 同号调用次数等于 RetrySameAccount。
func TestChatSameAccountRetryExhaustedAppliesPolicy(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return 429, `{"code":6004,"msg":"rate limited"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, RetrySameAccount: 2, RetryBackoff: time.Millisecond, SoftCooldown: time.Minute})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d want 429 body=%s", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("same-account attempts=%d want 2", got)
	}
	st, _ := p.Status("u1")
	if !st.Cooling {
		t.Errorf("exhausted 429 must cool account via existing policy: %+v", st)
	}
}

// TestChatNoRetryByDefault 未注入 RetrySameAccount（零值=1）时行为与现状一致：
// 单账号 429 只调用一次，直接冷却 → 末端 429。保证既有测试与源码模式零回归。
func TestChatNoRetryByDefault(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return 429, `{"code":6004,"msg":"rate limited"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("default must not retry same account: calls=%d want 1", got)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("code=%d want 429", rec.Code)
	}
}

// TestChatHardCreditNotRetriedSameAccount 非瞬态分类（ErrHardCredit 402）不触发同号重发，
// 直接走既有硬冷却换号语义（单账号即 503），只调用一次。
func TestChatHardCreditNotRetriedSameAccount(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return 402, `{"code":1,"msg":"余额不足"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, RetrySameAccount: 3, RetryBackoff: time.Millisecond})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("hard credit must not retry same account: calls=%d want 1", got)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code=%d want 503", rec.Code)
	}
}

// pinnedStore 固定返回某 UID 的锁定查询（测试用，不联网）。
type pinnedStore struct{ uid string }

func (s pinnedStore) PinnedUID(realm string) string { return s.uid }

// TestChatPinnedNoSameAccountRetry 严格锁定号即使瞬态失败也不重发（保持 0010「不换号、
// 不额外消耗」语义），只调用一次即按锁定不可用结束。
func TestChatPinnedNoSameAccountRetry(t *testing.T) {
	var calls int32
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt32(&calls, 1)
		return 500, `{"msg":"upstream boom"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "pin1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, Pinned: pinnedStore{uid: "pin1"}, RetrySameAccount: 3, RetryBackoff: time.Millisecond})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"cn:glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("pinned account must not retry same account: calls=%d want 1", got)
	}
}

// TestChatTransportErrorRetriedSameAccount 传输层错误（RoundTrip 返回 error）在同号内
// 重发一次后成功 → 200，两次调用。
func TestChatTransportErrorRetriedSameAccount(t *testing.T) {
	var calls int32
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return nil, errors.New("dial tcp: connection reset")
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, RetrySameAccount: 2, RetryBackoff: time.Millisecond})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("transport-error same-account attempts=%d want 2", got)
	}
}
