// Package scheduler 负责小浣熊侧的定时维护：每日积分发放触发与 token 预刷新。
//
// 小浣熊没有原生签到接口：每日积分靠「桌面登录奖励 → setting_info 触发 → 账单核对」
// 三步链路获取。本包把这条链路放在每日固定时点跑一次，并在同一趟里刷新积分余额，
// 让被限流冷却的账号在确认仍有余额时解冻。
package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"workbuddy2api-raccoon/raccoon/pool"
	"workbuddy2api-raccoon/raccoon/upstream"
)

type Config struct {
	Pool         *pool.Pool
	Upstream     *upstream.Client
	CheckinHour  int
	RefreshHours []int
	RefreshSkew  time.Duration
}

type Scheduler struct {
	cfg         Config
	mu          sync.Mutex
	lastCheckin time.Time
	lastRefresh time.Time
}

func New(cfg Config) *Scheduler {
	if cfg.CheckinHour < 0 || cfg.CheckinHour > 23 {
		cfg.CheckinHour = 9
	}
	if len(cfg.RefreshHours) == 0 {
		cfg.RefreshHours = []int{3}
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = upstream.RefreshMargin
	}
	return &Scheduler{cfg: cfg}
}

// Run 以分钟粒度轮询整点，到点执行对应任务；ctx 取消即退出。
func (s *Scheduler) Run(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			now := time.Now()
			if now.Hour() == s.cfg.CheckinHour {
				if _, err := s.RunCheckinNow(ctx); err != nil {
					log.Printf("raccoon 每日积分链路完成，但有账号失败：%v", err)
				}
			}
			if containsHour(s.cfg.RefreshHours, now.Hour()) {
				if _, err := s.RunRefreshNow(ctx); err != nil {
					log.Printf("raccoon token 预刷新完成，但有账号失败：%v", err)
				}
			}
			timer.Reset(time.Minute)
		}
	}
}

// RunCheckinNow 逐账号跑积分三步链路并刷新余额。返回参与处理的账号数与首个错误。
func (s *Scheduler) RunCheckinNow(ctx context.Context) (int, error) {
	var processed int
	var firstErr error
	note := func(uid string, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
		log.Printf("raccoon 积分链路 %s: %v", uid, err)
	}
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled || !st.Enabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil {
			continue
		}
		processed++
		// 先核对账单：今日已入账就不再触发（上游按天幂等，重发无意义），只同步余额。
		if bills, err := s.cfg.Upstream.Bills(ctx, a, 20, 0); err == nil && upstream.GrantedToday(bills, time.Now()) > 0 {
			if bal, err := s.cfg.Upstream.Balance(ctx, a); err == nil {
				s.cfg.Pool.SetPoints(st.UID, bal.Available)
				s.cfg.Pool.ReenableIfPoints(st.UID, bal.Available)
			}
			continue
		}
		// ① 一次性桌面登录奖励（上游幂等，已发放时 granted=false）。
		if _, err := s.cfg.Upstream.DesktopGrant(ctx, a); err != nil {
			note(st.UID, err)
			continue
		}
		// ② 每日积分发放触发器（按天幂等）。
		if _, _, err := s.cfg.Upstream.SettingInfo(ctx, a); err != nil {
			note(st.UID, err)
			continue
		}
		// ③ 权威结果：账单核对今日是否确有入账。bills 是慢接口，失败按计划书降级不阻塞。
		if bills, err := s.cfg.Upstream.Bills(ctx, a, 20, 0); err != nil {
			log.Printf("raccoon %s 账单核对降级（不影响本次触发）：%v", st.UID, err)
		} else if granted := upstream.GrantedToday(bills, time.Now()); granted <= 0 {
			log.Printf("raccoon %s 已触发每日积分，但账单核对未见今日正向入账", st.UID)
		}
		// ④ 刷新余额：有余额才解冻被限流的账号。
		if bal, err := s.cfg.Upstream.Balance(ctx, a); err != nil {
			note(st.UID, err)
		} else {
			s.cfg.Pool.SetPoints(st.UID, bal.Available)
			s.cfg.Pool.ReenableIfPoints(st.UID, bal.Available)
		}
	}
	s.mu.Lock()
	s.lastCheckin = time.Now()
	s.mu.Unlock()
	return processed, firstErr
}

// RunRefreshNow 刷新临期凭证；刷新失败且确认登录态已死时硬禁用。
func (s *Scheduler) RunRefreshNow(ctx context.Context) (int, error) {
	var processed int
	var firstErr error
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || !a.NeedsRefresh(s.cfg.RefreshSkew) {
			continue
		}
		processed++
		if err := s.cfg.Upstream.RefreshToken(ctx, a); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("raccoon 预刷新 %s 失败：%v", st.UID, err)
			if !a.CanRefresh() || upstream.IsTokenExpiredErr(err) {
				s.cfg.Pool.Disable(st.UID, "refresh_failed: 登录态失效，请重新导入凭证")
			}
		}
	}
	s.mu.Lock()
	s.lastRefresh = time.Now()
	s.mu.Unlock()
	return processed, firstErr
}

// Status 返回排程视图（时点、下次触发与上次执行时间）。
func (s *Scheduler) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := map[string]any{
		"checkin_hour":  s.cfg.CheckinHour,
		"refresh_hours": s.cfg.RefreshHours,
	}
	if d := nextFire(now, []int{s.cfg.CheckinHour}); d > 0 {
		out["next_checkin"] = now.Add(d).Format(time.RFC3339)
	}
	if d := nextFire(now, s.cfg.RefreshHours); d > 0 {
		out["next_refresh"] = now.Add(d).Format(time.RFC3339)
	}
	if !s.lastCheckin.IsZero() {
		out["last_checkin"] = s.lastCheckin.UTC().Format(time.RFC3339)
	}
	if !s.lastRefresh.IsZero() {
		out["last_refresh"] = s.lastRefresh.UTC().Format(time.RFC3339)
	}
	return out
}

// nextFire 返回到下一个目标整点的时长；时点全部非法时返回一小时（避免忙等）。
func nextFire(now time.Time, hours []int) time.Duration {
	best := time.Duration(0)
	for _, h := range hours {
		if h < 0 || h > 23 {
			continue
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if d := t.Sub(now); best == 0 || d < best {
			best = d
		}
	}
	if best == 0 {
		return time.Hour
	}
	return best
}

func containsHour(hours []int, h int) bool {
	for _, v := range hours {
		if v == h {
			return true
		}
	}
	return false
}
