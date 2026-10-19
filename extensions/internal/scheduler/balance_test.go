package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// newBalanceTestScheduler 构造指向 fake billing 服务的调度器与单号池。
func newBalanceTestScheduler(t *testing.T, remain int64, calls *atomic.Int32) (*Scheduler, *pool.Pool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":100,"CycleCapacityRemain":` +
			jsonI64(remain) + `,"CycleCapacityUsed":0}]}}}}`))
	}))
	t.Cleanup(srv.Close)
	p := pool.New("")
	up := &upstream.Client{
		HTTP:          srv.Client(),
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
	}
	return New(Config{Pool: p, Upstream: up}), p
}

// TestRefreshHardCooledBalanceRevives 硬冷却账号余额恢复 → 解冻回池并更新余额。
func TestRefreshHardCooledBalanceRevives(t *testing.T) {
	var calls atomic.Int32
	s, p := newBalanceTestScheduler(t, 500, &calls)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u1", pool.CoolHard, time.Hour, "余额不足")

	s.RefreshHardCooledBalance(context.Background())

	if calls.Load() != 1 {
		t.Errorf("billing calls=%d want 1", calls.Load())
	}
	st, _ := p.Status("u1")
	if st.Cooling || st.Credits != 500 {
		t.Errorf("after refresh: cooling=%v credits=%d, want 解冻且 credits=500", st.Cooling, st.Credits)
	}
}

// TestRefreshHardCooledBalanceStaysCooledAtZero 余额仍为 0 → 保持硬冷却。
func TestRefreshHardCooledBalanceStaysCooledAtZero(t *testing.T) {
	s, p := newBalanceTestScheduler(t, 0, nil)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u1", pool.CoolHard, time.Hour, "余额不足")

	s.RefreshHardCooledBalance(context.Background())

	st, _ := p.Status("u1")
	if !st.Cooling || st.CoolKind != "hard_credit" {
		t.Errorf("after refresh: cooling=%v kind=%q, want 仍硬冷却", st.Cooling, st.CoolKind)
	}
}

// TestRefreshHardCooledBalanceSkipsNonHard 软冷却/正常账号不进名单：零外呼、状态不动。
func TestRefreshHardCooledBalanceSkipsNonHard(t *testing.T) {
	var calls atomic.Int32
	s, p := newBalanceTestScheduler(t, 500, &calls)
	p.Add(&auth.Auth{UID: "u-soft", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u-ok", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u-soft", pool.CoolSoft, time.Hour, "429")

	s.RefreshHardCooledBalance(context.Background())

	if calls.Load() != 0 {
		t.Errorf("billing calls=%d want 0（无硬冷却账号不应外呼）", calls.Load())
	}
	if st, _ := p.Status("u-soft"); !st.Cooling {
		t.Errorf("u-soft should stay soft-cooled")
	}
}

// TestRefreshHardCooledBalanceUpstreamError 上游失败 → 账号保持冷却（错误只记日志）。
func TestRefreshHardCooledBalanceUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":10000}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u1", pool.CoolHard, time.Hour, "余额不足")

	s := New(Config{
		Pool: p,
		Upstream: &upstream.Client{
			HTTP:          srv.Client(),
			ChatBaseCN:    srv.URL,
			BillingBaseCN: srv.URL,
		},
	})
	s.RefreshHardCooledBalance(context.Background())

	st, _ := p.Status("u1")
	if !st.Cooling || st.CoolKind != "hard_credit" {
		t.Errorf("upstream error: cooling=%v kind=%q, want 仍硬冷却", st.Cooling, st.CoolKind)
	}
}

// TestRunBalanceRefreshDisabled interval<=0 直接返回（环境变量关闭路径）。
func TestRunBalanceRefreshDisabled(t *testing.T) {
	s := &Scheduler{cfg: Config{}}
	done := make(chan struct{})
	go func() {
		s.RunBalanceRefresh(context.Background(), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunBalanceRefresh(0) 未立即返回")
	}
}
