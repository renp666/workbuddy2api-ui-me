// 余额后台刷新与硬冷却解冻（扩展能力，对标 panel 的 balance_refresh）。
//
// 402 余额耗尽的账号进入 CoolHard 硬冷却（CooldownUntilTomorrow4AM），等待签到任务
// （09:00/21:00）查到余额恢复后由 ReenableIfCredits 解冻。本文件提供周期余额刷新
// 所需的两个池入口：枚举当前硬冷却账号、按余额解冻——让「余额恢复→回池」不再绑定
// 签到时点。
package pool

import "time"

// HardCooledUIDs 返回当前处于有效硬冷却的账号 UID：coolKind==CoolHard 且 until
// 未到期且未被禁用。软冷却（429 限流）与熔断有各自的时间恢复机制，不属于余额
// 刷新的解冻对象；until 已过期的硬冷却同样不进名单（pick 兜底已可见，无需刷新）。
func (p *Pool) HardCooledUIDs() []string {
	now := time.Now()
	p.mu.RLock()
	defer p.mu.RUnlock()
	uids := make([]string, 0, 4)
	for uid, e := range p.byUID {
		if e.disabled || e.coolKind != CoolHard || e.until.IsZero() || !now.Before(e.until) {
			continue
		}
		uids = append(uids, uid)
	}
	return uids
}

// ReviveHardCooledIfCredits 余额刷新场景的解冻：仅当 remain>0 且账号非禁用且
// 处于硬冷却时清冷却域（经 reviveCoolingLocked：只清冷却域、更新 credits，不动
// 熔断器——余额恢复不证明 chat 通道健康，与签到解冻同一口径）。
//
// 与签到用的 ReenableIfCredits 相比判据更严：余额刷新是 5 分钟粒度的周期动作，
// 软冷却账号 remain>0 只说明余额充足，不代表 429 限流窗口已过——误清软冷却会把
// 限流号立即打回轮换放大上游压力；熔断同理，只能等 breakerUntil 过期或 chat 成功。
// 非硬冷却（软冷却/熔断/正常）账号仅更新 credits。
func (p *Pool) ReviveHardCooledIfCredits(uid string, remain int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byUID[uid]; ok {
		if remain > 0 && !e.disabled && e.coolKind == CoolHard {
			p.reviveCoolingLocked(e, remain)
		} else {
			e.credits = remain
		}
		p.dirty.Store(true)
	}
}
