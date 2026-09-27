// pin_lock_test.go 补丁 0010 回归：人工严格锁定选号语义——
// 只用锁定号、不可用直接 pinned_account_unavailable、失败不换号、解锁恢复轮换。
package server

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workbuddy2api/internal/auth"
)

// fakePinner 是测试用 Pinner：realm -> 锁定 UID。
type fakePinner map[string]string

func (f fakePinner) PinnedUID(realm string) string { return f[realm] }

func chatRequest(h *Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

const chatBody = `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`

// TestPinnedRequestOnlyUsesPinnedAccount 池内两个健康 CN 号，锁定 u2：
// 上游必须只收到 u2 的凭据，u1 即使健康也零次参与；成功带头。
func TestPinnedRequestOnlyUsesPinnedAccount(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		seen[authz]++
		mu.Unlock()
		return 200, sseOK, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", Nickname: "bob", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, Pinned: fakePinner{"cn": "u2"}})
	rec := chatRequest(h, chatBody)
	if rec.Code != 200 {
		t.Fatalf("pinned healthy account must serve: code=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("X-Account"); got != "bob" {
		t.Errorf("X-Account=%q want bob", got)
	}
	if seen["Bearer at2"] != 1 {
		t.Errorf("pinned account calls=%d want 1", seen["Bearer at2"])
	}
	if seen["Bearer at1"] != 0 {
		t.Errorf("non-pinned account must never be called, got %d", seen["Bearer at1"])
	}
}

// TestPinnedUnavailableRejectsWithoutTouchingOthers 锁定号被禁用：直接 503
// pinned_account_unavailable，上游零调用，其他健康号绝不兜底（省钱保障）。
func TestPinnedUnavailableRejectsWithoutTouchingOthers(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		return 200, sseOK, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", Nickname: "bob", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	p.Disable("u2", "manual")
	h := NewHandler(Config{Pool: p, Upstream: up, Pinned: fakePinner{"cn": "u2"}})
	rec := chatRequest(h, chatBody)
	if rec.Code != 503 {
		t.Fatalf("code=%d want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pinned_account_unavailable") {
		t.Fatalf("body=%s want pinned_account_unavailable", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "锁定") {
		t.Errorf("严格锁定文案应说明语义，实际: %s", rec.Body)
	}
	if calls != 0 {
		t.Errorf("upstream calls=%d want 0 (disabled pinned account never leaves the gateway)", calls)
	}
}

// TestPinnedUpstreamFailureNeverRotates 锁定号上游 500：错误已记录在锁定号上，
// 但绝不轮换到健康的 u2（零调用），终态仍是 pinned_account_unavailable。
func TestPinnedUpstreamFailureNeverRotates(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		seen[authz]++
		mu.Unlock()
		if authz == "Bearer at1" {
			return 500, `{"error":"boom"}`, false
		}
		return 200, sseOK, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", Nickname: "bob", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 3, Pinned: fakePinner{"cn": "u1"}})
	rec := chatRequest(h, chatBody)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "pinned_account_unavailable") {
		t.Fatalf("code=%d body=%s want 503 pinned_account_unavailable", rec.Code, rec.Body)
	}
	if seen["Bearer at2"] != 0 {
		t.Errorf("失败后不得轮换到 u2，实际调用 %d 次", seen["Bearer at2"])
	}
	if seen["Bearer at1"] != 1 {
		t.Errorf("锁定号应只尝试 1 次（失败即终止），实际 %d 次", seen["Bearer at1"])
	}
}

// TestUnpinRestoresAutomaticRotation 解除锁定后，u1 再报 500 时正常轮换到健康的
// u2 并成功——锁定行为无残留（MaxRotate 默认 3 足够一次换号）。
func TestUnpinRestoresAutomaticRotation(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		if authz == "Bearer at1" {
			return 500, `{"error":"boom"}`, false
		}
		return 200, sseOK, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", Nickname: "bob", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	pins := fakePinner{"cn": "u1"}
	h := NewHandler(Config{Pool: p, Upstream: up, Pinned: pins})

	locked := chatRequest(h, chatBody)
	if locked.Code != 503 || !strings.Contains(locked.Body.String(), "pinned_account_unavailable") {
		t.Fatalf("locked stage: code=%d body=%s", locked.Code, locked.Body)
	}
	delete(pins, "cn") // 解锁
	freed := chatRequest(h, chatBody)
	if freed.Code != 200 {
		t.Fatalf("unpin must restore rotation: code=%d body=%s", freed.Code, freed.Body)
	}
	if got := freed.Header().Get("X-Account"); got != "bob" {
		t.Errorf("unpin rotation X-Account=%q want bob", got)
	}
}
