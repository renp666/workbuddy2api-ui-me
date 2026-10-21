// 探活专用池入口（扩展能力，issue #15）：供调度器周期探活读写冷却域与在途判定。
//
// 与 Cooldown/CooldownUntilTomorrow4AM 的关键差异：**不喂熔断器、不动 softStreak**。
// Cooldown 入口同时是熔断器的失败信号（recordBreakerFailureLocked），而探活走的是
// 模型目录口——目录口失败不代表对话通道故障；若汇入对话侧熔断统计，默认阈值 3 时
// 两轮探活误报就能熔断一个好号。探活连败计数在 scheduler 侧自有 map，达到阈值后
// 直接以硬冷却表达「凭据疑似失效」语义升级。
package pool

import "time"

// ProbeCooldown 探活单次失败：账号级软冷却至 now+d（CoolSoft 语义，账号暂移出
// 轮换）。不喂熔断器、不累加 softStreak——探活失败与 429 限流是不同信号域，混计
// 会污染软退避指数。模型级 6004 冷却表一并清空，与 Cooldown 的账号级覆盖口径一致。
// 禁用账号照常只写冷却域（disabled 恒压过冷却，无害）。
func (p *Pool) ProbeCooldown(uid string, d time.Duration, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	e.until = time.Now().Add(d)
	e.coolKind = CoolSoft
	e.reason = reason
	e.modelCooldowns = nil
	p.dirty.Store(true)
}

// ProbeCooldownHard 探活连续失败：硬冷却到下一个 04:00（本地时区），与 402 余额
// 耗尽同语义——进入 HardCooledUIDs 名单，等签到 / 周期余额刷新
// （ReviveHardCooledIfCredits 余额恢复即解冻）闭环兜底。同样不喂熔断器（见文件头）。
func (p *Pool) ProbeCooldownHard(uid string, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	e.until = nextDay4AM(time.Now())
	e.coolKind = CoolHard
	e.reason = reason
	e.modelCooldowns = nil
	p.dirty.Store(true)
}

// InFlightFull 报告账号当前在途请求数是否已达上限（探活跳过在途满号，不与对话
// 流量抢租约）。未配置在途上限（<=0，租约关闭）时恒 false。与 Acquire 的读法一致：
// 锁内取 limit 快照，计数走 atomic。
func (p *Pool) InFlightFull(uid string) bool {
	p.mu.RLock()
	limit := p.maxInFlight
	e, ok := p.byUID[uid]
	p.mu.RUnlock()
	if !ok || limit <= 0 {
		return false
	}
	return e.inFlight.Load() >= int64(limit)
}
