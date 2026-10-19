// 周期余额刷新（扩展能力，对标 panel 的 schedule.balance_refresh_minutes，默认 5）：
// 硬冷却（402 余额耗尽）账号的余额恢复后立即解冻回池，不再等下一次签到
// （09:00/21:00）或次日 04:00 冷却到期。
//
// 与 panel 的有意偏差：panel 每轮刷新池内全部账号的余额；本实现只对硬冷却账号
// 外呼（通常 0~N 个号，各一次 billing GET）。理由：① 解冻是本能力的核心目标，
// 硬冷却号是唯一「等外部事件恢复」的群体；② 软冷却/熔断由时间机制自恢复，正常
// 账号的 credits 由签到（每日两次）与对话路径保持新鲜，全池刷新只是权重微增益，
// 却让每号每天多出数百次 billing 调用，个人账号的风控面不值得。
package scheduler

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"workbuddy2api/internal/logfmt"
)

// defaultBalanceRefreshInterval 默认刷新周期；wrapCore 读 WB2A_BALANCE_REFRESH_MINUTES
// 可覆盖（分钟），<=0 关闭。
const defaultBalanceRefreshInterval = 5 * time.Minute

// BalanceRefreshInterval 从环境变量 WB2A_BALANCE_REFRESH_MINUTES 读刷新周期（分钟）：
// 未设置用默认 5 分钟；设为 0 或负数关闭；非法值告警并回落默认。
func (s *Scheduler) BalanceRefreshInterval() time.Duration {
	v := os.Getenv("WB2A_BALANCE_REFRESH_MINUTES")
	if v == "" {
		return defaultBalanceRefreshInterval
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("balance refresh: WB2A_BALANCE_REFRESH_MINUTES=%q 非法，回落默认 %v", v, defaultBalanceRefreshInterval)
		return defaultBalanceRefreshInterval
	}
	return time.Duration(n) * time.Minute
}

// balanceAccountDelay 账号间限速：与旅行/活跃上报的 800ms 同口径，硬冷却号通常
// 只有零星几个，串行慢查不构成实际延迟压力。
var balanceAccountDelay = 800 * time.Millisecond

// RunBalanceRefresh 阻塞式周期循环：每 interval 触发一轮硬冷却账号余额刷新，
// ctx 取消退出。调用方用 `go sch.RunBalanceRefresh(ctx, interval)` 启动；
// interval<=0 时直接返回（关闭）。
func (s *Scheduler) RunBalanceRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RefreshHardCooledBalance(ctx)
		}
	}
}

// RefreshHardCooledBalance 执行一轮刷新：硬冷却账号逐个查余额，恢复（>0）即解冻
// 并更新余额/快过架子集。单号失败只记日志跳过，不影响其他号；不主动刷新 token
// （补票与落盘是签到任务的职责），token 过期导致的查询失败下一轮自然重试。
func (s *Scheduler) RefreshHardCooledBalance(ctx context.Context) {
	uids := s.cfg.Pool.HardCooledUIDs()
	if len(uids) == 0 {
		return
	}
	soon := s.cfg.ExpiringSoonWindow
	for i, uid := range uids {
		if ctx.Err() != nil {
			return
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(balanceAccountDelay):
			}
		}
		a := s.cfg.Pool.AuthByUID(uid)
		if a == nil || a.AccessToken == "" {
			continue
		}
		remain, buckets, err := s.cfg.Upstream.UserResourceDetailed(a, soon)
		if err != nil {
			log.Printf("balance %s: %v", logfmt.UID8(uid), err)
			continue
		}
		s.cfg.Pool.ReviveHardCooledIfCredits(uid, remain)
		s.cfg.Pool.SetCreditsDetailed(uid, remain, buckets.Expiring)
		if remain > 0 {
			log.Printf("balance %s: 余额恢复，解冻回池 credits=%d", logfmt.UID8(uid), remain)
		}
	}
}
