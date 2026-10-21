package scheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// probeCatalogBody FetchModels 的成功响应：models 与 cli agent 交出一条可用模型
// （无 efforts 字段 → storeEfforts 空桶跳过，不触碰客户端缓存）。
const probeCatalogBody = `{"code":0,"data":{"models":[{"id":"gpt-5.2","name":"GPT-5.2","maxInputTokens":400000,"maxOutputTokens":128000}],"agents":[{"name":"cli","models":["gpt-5.2"]}]}}`

// probeUpstream 假上游目录服务：status 控制目录口行为（0 = 成功目录，否则按该
// HTTP 状态码失败），calls 计外呼次数，paths 记录请求路径（断言探针确为目录端点）。
type probeUpstream struct {
	status atomic.Int32
	calls  atomic.Int32
	mu     sync.Mutex
	paths  []string
}

func (u *probeUpstream) handler(w http.ResponseWriter, r *http.Request) {
	u.calls.Add(1)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path)
	u.mu.Unlock()
	if code := int(u.status.Load()); code != 0 {
		http.Error(w, `{"code":10000}`, code)
		return
	}
	w.Write([]byte(probeCatalogBody))
}

// lastPath 返回最近一次请求路径。
func (u *probeUpstream) lastPath() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.paths) == 0 {
		return ""
	}
	return u.paths[len(u.paths)-1]
}

// newProbeScheduler 构造指向假目录服务的调度器与空池。
func newProbeScheduler(t *testing.T, u *probeUpstream) (*Scheduler, *pool.Pool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(u.handler))
	t.Cleanup(srv.Close)
	p := pool.New("")
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), p
}

// resetProbeState 清探活器运行态（包级单例，测试间互不污染）。
func resetProbeState(t *testing.T) {
	t.Helper()
	probeFailsMu.Lock()
	probeFails = map[string]int{}
	probeFailsMu.Unlock()
	probeRunning.Store(false)
}

// addProbeAuth 向池里加一个可探活的假账号。
func addProbeAuth(p *pool.Pool, uid string) {
	p.Add(&auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
}

// shortProbeCooldown 注入短软冷却并注册恢复：生产 interval 下限 1h > 30min，下一轮
// 时软冷却必已过期；测试轮次背靠背，用短冷却 + probeGap 等待等价复现该时序。
func shortProbeCooldown(t *testing.T) {
	t.Helper()
	old := probeSoftCooldown
	probeSoftCooldown = 2 * time.Millisecond
	t.Cleanup(func() { probeSoftCooldown = old })
}

// probeGap 等待一段足以让短软冷却过期的轮间间隔。
func probeGap() { time.Sleep(5 * time.Millisecond) }

// probeFailsOf 读取连败计数（探活器包内状态，同包直读）。
func probeFailsOf(uid string) int {
	probeFailsMu.Lock()
	defer probeFailsMu.Unlock()
	return probeFails[uid]
}

// TestProbeInterval 开关与钳制：未设置/<=0/非法=关闭；小数钳到 1h 下限；正常值透传。
func TestProbeInterval(t *testing.T) {
	s := &Scheduler{}
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 0}, // 未设置：默认关闭
		{"0", 0},
		{"-3", 0},
		{"6", 6 * time.Hour}, // 部署建议值
		{"1", time.Hour},     // 恰在下限
		{"0.01", time.Hour},  // 低于下限 → 钳制
		{"0.5", time.Hour},   // 低于下限 → 钳制
		{"abc", 0},           // 非法 → 关闭
	}
	for _, c := range cases {
		t.Setenv("WB2A_PROBE_HOURS", c.env)
		if got := s.ProbeInterval(); got != c.want {
			t.Errorf("WB2A_PROBE_HOURS=%q: got %v want %v", c.env, got, c.want)
		}
	}
}

// TestProbeSkipsUnhealthy 只探 normal healthy：禁用/软冷却/硬冷却/熔断/在途满号/
// 空 token 一律跳过；探针确为模型目录端点（非 billing/对话口）。
func TestProbeSkipsUnhealthy(t *testing.T) {
	resetProbeState(t)
	u := &probeUpstream{}
	s, p := newProbeScheduler(t, u)

	addProbeAuth(p, "u-ok")
	addProbeAuth(p, "u-dis")
	addProbeAuth(p, "u-soft")
	addProbeAuth(p, "u-hard")
	addProbeAuth(p, "u-brk")
	addProbeAuth(p, "u-busy")
	p.Add(&auth.Auth{UID: "u-notoken", RefreshToken: "rt"}) // 空 AccessToken：不可探
	p.Disable("u-dis", "手动禁用")
	p.Cooldown("u-soft", pool.CoolSoft, time.Hour, "429")
	p.Cooldown("u-hard", pool.CoolHard, time.Hour, "余额不足")
	for i := 0; i < 3; i++ { // 三次喂入触发熔断：until 已过期、breakerUntil 生效中
		p.Cooldown("u-brk", pool.CoolSoft, time.Millisecond, "x")
	}
	p.SetMaxInFlight(1)
	if !p.Acquire("u-busy") {
		t.Fatal("u-busy 租约应可用")
	}

	s.ProbeHealthyAccounts(context.Background())

	if got := u.calls.Load(); got != 1 {
		t.Errorf("探活外呼=%d want 1（只探 u-ok）", got)
	}
	if path := u.lastPath(); !strings.HasSuffix(path, "/console/enterprises/personal/models") {
		t.Errorf("探针路径=%q want 模型目录端点", path)
	}
	if st, _ := p.Status("u-ok"); st.Cooling || st.Disabled {
		t.Errorf("u-ok 应保持 healthy: %+v", st)
	}
}

// TestProbeSingleFailureSoftCools 单次失败：CoolSoft 30 分钟；不喂熔断器、不动 softStreak。
func TestProbeSingleFailureSoftCools(t *testing.T) {
	resetProbeState(t)
	u := &probeUpstream{}
	s, p := newProbeScheduler(t, u)
	addProbeAuth(p, "u1")
	u.status.Store(http.StatusUnauthorized)

	s.ProbeHealthyAccounts(context.Background())

	if got := u.calls.Load(); got != 1 {
		t.Errorf("外呼=%d want 1", got)
	}
	st, ok := p.Status("u1")
	if !ok || !st.Cooling {
		t.Fatalf("探活失败后应软冷却: ok=%v st=%+v", ok, st)
	}
	if st.CoolKind != "soft_rate" {
		t.Errorf("cool_kind=%q want soft_rate", st.CoolKind)
	}
	if !strings.Contains(st.Reason, "探活失败") {
		t.Errorf("reason=%q want 含「探活失败」", st.Reason)
	}
	if st.BreakerFails != 0 || !st.BreakerUntil.IsZero() {
		t.Errorf("熔断器被喂入: fails=%d until=%v", st.BreakerFails, st.BreakerUntil)
	}
	if st.SoftStreak != 0 {
		t.Errorf("soft_streak=%d want 0（探活失败不累加软退避指数）", st.SoftStreak)
	}
}

// TestProbeThreeConsecutiveFailuresHardCools 连续 3 次失败：CoolHard（凭据疑似失效）；
// 硬冷却后退出探活名单；解冻后连败计数从零重新观察。轮间用短软冷却 + probeGap
// 复现生产时序（interval 下限 1h > 软冷却 30min，下一轮时账号必已出冷却）。
func TestProbeThreeConsecutiveFailuresHardCools(t *testing.T) {
	resetProbeState(t)
	shortProbeCooldown(t)
	u := &probeUpstream{}
	s, p := newProbeScheduler(t, u)
	addProbeAuth(p, "u1")
	ctx := context.Background()
	u.status.Store(http.StatusUnauthorized)

	s.ProbeHealthyAccounts(ctx) // n=1 → 软冷却
	probeGap()
	s.ProbeHealthyAccounts(ctx) // 冷却已过期，再探 → n=2
	probeGap()
	if got := u.calls.Load(); got != 2 {
		t.Fatalf("前两轮各应外呼一次（软冷却过期后再探）, calls=%d", got)
	}
	s.ProbeHealthyAccounts(ctx) // n=3 → 硬冷却

	st, _ := p.Status("u1")
	if !st.Cooling || st.CoolKind != "hard_credit" {
		t.Errorf("第三次失败后应硬冷却: cooling=%v kind=%q", st.Cooling, st.CoolKind)
	}
	if st.BreakerFails != 0 || !st.BreakerUntil.IsZero() {
		t.Errorf("熔断器被喂入: fails=%d until=%v", st.BreakerFails, st.BreakerUntil)
	}
	if !strings.Contains(st.Reason, "凭据疑似失效") {
		t.Errorf("reason=%q want 含「凭据疑似失效」", st.Reason)
	}

	// 硬冷却账号退出探活名单：再一轮零外呼。
	n := u.calls.Load()
	s.ProbeHealthyAccounts(ctx)
	if got := u.calls.Load(); got != n {
		t.Errorf("硬冷却账号不应再被探活: %d -> %d", n, got)
	}

	// 余额刷新解冻闭环：解冻成功后目录口恢复 → 探活成功清零；再失败从 1 重新累计。
	p.ReviveHardCooledIfCredits("u1", 100)
	u.status.Store(0)
	s.ProbeHealthyAccounts(ctx)
	if st, _ := p.Status("u1"); st.Cooling {
		t.Errorf("解冻且探活成功后应 healthy: %+v", st)
	}
	if got := probeFailsOf("u1"); got != 0 {
		t.Errorf("成功后连败计数应清零, got %d", got)
	}
	u.status.Store(http.StatusServiceUnavailable)
	s.ProbeHealthyAccounts(ctx)
	if got := probeFailsOf("u1"); got != 1 {
		t.Errorf("解冻后首次失败应从 1 重新累计, got %d", got)
	}
}

// TestProbeSuccessClearsFailStreak 成功清连败计数：2 败 → 成功 → 再 1 败只从 1 重新累计。
func TestProbeSuccessClearsFailStreak(t *testing.T) {
	resetProbeState(t)
	shortProbeCooldown(t)
	u := &probeUpstream{}
	s, p := newProbeScheduler(t, u)
	addProbeAuth(p, "u1")
	ctx := context.Background()

	u.status.Store(http.StatusUnauthorized)
	s.ProbeHealthyAccounts(ctx)
	probeGap()
	s.ProbeHealthyAccounts(ctx)
	if got := probeFailsOf("u1"); got != 2 {
		t.Fatalf("两轮失败后连败计数应=2, got %d", got)
	}
	u.status.Store(0)
	probeGap()
	s.ProbeHealthyAccounts(ctx) // 成功 → 清零
	u.status.Store(http.StatusUnauthorized)
	s.ProbeHealthyAccounts(ctx)

	if got := probeFailsOf("u1"); got != 1 {
		t.Errorf("成功清零后再失败应从 1 重新累计（而非续到 3 触发硬冷却）, got %d", got)
	}
	if st, _ := p.Status("u1"); st.CoolKind == "hard_credit" {
		t.Errorf("清零生效时不应硬冷却: %+v", st)
	}
}

// TestProbeSkipsWhenPreviousRoundRunning 防重入：上一轮未结束，本轮直接跳过。
func TestProbeSkipsWhenPreviousRoundRunning(t *testing.T) {
	resetProbeState(t)
	u := &probeUpstream{}
	s, p := newProbeScheduler(t, u)
	addProbeAuth(p, "u1")

	probeRunning.Store(true) // 模拟上一轮未结束
	s.ProbeHealthyAccounts(context.Background())
	probeRunning.Store(false)

	if got := u.calls.Load(); got != 0 {
		t.Errorf("上一轮未结束时应跳过本轮, calls=%d", got)
	}
}

// TestRunProbeDisabled interval<=0 直接返回（默认关闭路径）。
func TestRunProbeDisabled(t *testing.T) {
	s := &Scheduler{}
	done := make(chan struct{})
	go func() {
		s.RunProbe(context.Background(), 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunProbe(0) 未立即返回")
	}
}

// TestProbeErrClass 错误分类短语：覆盖 FetchModels 各错误前缀与兜底。
func TestProbeErrClass(t *testing.T) {
	cases := []struct{ msg, want string }{
		{"models api status 401: Unauthorized", "http_401"},
		{"models api status 502: Bad Gateway", "http_502"},
		{"models parse: unexpected token", "parse"},
		{"no cli agent models found", "empty_catalog"},
		{"models api returned empty list", "empty_catalog"},
		{"models api code=11102", "api_code"},
		{`Get "https://x": dial tcp: timeout`, "network"},
	}
	for _, c := range cases {
		if got := probeErrClass(errors.New(c.msg)); got != c.want {
			t.Errorf("probeErrClass(%q)=%q want %q", c.msg, got, c.want)
		}
	}
}
