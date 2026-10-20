// degrade.go 连败降权（对标 workbuddy-manager issue #114，移植自 hub pool/degrade.go）：
// NoteFailures 计数 + 达阈临时出池 + NoteSuccess 清零。喂入口是「不罚号的失败」——
// applyErrorPolicy default（未知 4xx 只换号不罚）与 handler 传输层失败分支（网络抖动
// 同样只换号不罚）。两者都是「不知道原因的失败」：带权威分类的错误（429/WAF/402/
// 5xx/12153/11140/11102）各有「知道原因的惩罚」（冷却/熔断/禁用），不喂本计数器——
// 惩罚已存在，再喂连败是重复计罚（两机制并存取更长者，不叠加）。
//
// 与冷却/熔断「并存取更长者不叠加」由 entry.healthy 的并列或门天然保证
// （disabled/until/breakerUntil/degradeUntil 四个截止任一未到期即不可选）。
// entry 字段（consecutiveFails/degradeUntil）、healthy 或门、NoteSuccess 清零点
// 见补丁 0018；本文件只承载新增方法与配置（extensions 新文件，免补丁）。
package pool

import (
	"os"
	"strconv"
	"time"
)

// 连败降权默认参数（对齐上游 issue #114 定案）：连败 5 次临时出池 10 分钟，
// 外部可调上限 2 小时。
const (
	defaultDegradeThreshold   = 5
	defaultDegradeCooldown    = 10 * time.Minute
	defaultDegradeCooldownMax = 2 * time.Hour
)

// degradeCfg 连败降权参数（包级单例配置——Pool 在进程内单实例，参数经 cmd 层
// SetDegrade 注入；放 extensions 包级变量而非 Pool 字段，避免为三个配置值扩大
// 上游 pool.go 的补丁面）。
var degradeCfg struct {
	threshold int
	cooldown  time.Duration
	max       time.Duration
}

func init() {
	degradeCfg.threshold = defaultDegradeThreshold
	degradeCfg.cooldown = defaultDegradeCooldown
	degradeCfg.max = defaultDegradeCooldownMax
}

// SetDegrade 设置连败降权参数（默认由 EnvDegrade 从环境变量注入；测试可直接调用）。
// 非正值保持默认；cooldownMax 是**硬上限**：cooldown 超过上限被钳到上限（degradeDuration
// 运行时钳制），而不是反过来抬上限。
func SetDegrade(threshold int, cooldown, cooldownMax time.Duration) {
	if threshold > 0 {
		degradeCfg.threshold = threshold
	}
	if cooldown > 0 {
		degradeCfg.cooldown = cooldown
	}
	if cooldownMax > 0 {
		degradeCfg.max = cooldownMax
	}
	if degradeCfg.max < degradeCfg.cooldown {
		degradeCfg.cooldown = degradeCfg.max
	}
}

// EnvDegrade 从环境变量注入连败降权参数（wrapCore 启动时调用一次）：
//   - WB2A_DEGRADE_THRESHOLD：连败触发阈值（默认 5）；
//   - WB2A_DEGRADE_COOLDOWN：降权时长（Go duration，默认 10m）；
//   - WB2A_DEGRADE_COOLDOWN_MAX：降权时长上限（默认 2h）。
//
// 未设置/非法值保持默认；无需任何必填配置。
func EnvDegrade() {
	if v := os.Getenv("WB2A_DEGRADE_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			degradeCfg.threshold = n
		}
	}
	if v := os.Getenv("WB2A_DEGRADE_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			degradeCfg.cooldown = d
		}
	}
	if v := os.Getenv("WB2A_DEGRADE_COOLDOWN_MAX"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			degradeCfg.max = d
		}
	}
	if degradeCfg.threshold <= 0 {
		degradeCfg.threshold = defaultDegradeThreshold
	}
	if degradeCfg.cooldown <= 0 {
		degradeCfg.cooldown = defaultDegradeCooldown
	}
	if degradeCfg.max <= 0 {
		degradeCfg.max = defaultDegradeCooldownMax
	}
	if degradeCfg.max < degradeCfg.cooldown {
		degradeCfg.cooldown = degradeCfg.max
	}
}

// NoteFailures 记录一次「不罚号的失败」（连败兜底的喂入口）：consecutiveFails++，
// 达到阈值触发降权（degradeUntil = now + 降权时长，计数清零供下一轮重新累计）。
// **已在降权期内**（degradeUntil 未到期）再次达阈：不延长、不翻倍（防用户重试
// 把降权越堆越厚）——计数已清零，到期放行后需重新连败满阈值才再降。
// 成功（NoteSuccess，补丁 0018 的清零点）清零计数并清降权截止。
func (p *Pool) NoteFailures(uid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	e.consecutiveFails++
	if e.consecutiveFails < degradeCfg.threshold {
		return
	}
	e.consecutiveFails = 0
	now := time.Now()
	if !e.degradeUntil.IsZero() && now.Before(e.degradeUntil) {
		// 降权期内的连败达阈：不延长（取更长者语义由 healthy 或门保证）。
		return
	}
	e.degradeUntil = now.Add(degradeDuration())
}

// degradeDuration 返回降权时长（固定 cooldown，钳到 [default, max]）。不做指数升级：
// 触发计数在达阈时清零、不持久化历史触发次数，档位不可推导；固定时长已满足
// 「临时出池一段时间」的语义（真持续坏号会在下一轮满阈值后再降一次）。
func degradeDuration() time.Duration {
	d := degradeCfg.cooldown
	if d <= 0 {
		d = defaultDegradeCooldown
	}
	max := degradeCfg.max
	if max <= 0 {
		max = defaultDegradeCooldownMax
	}
	if d > max {
		d = max
	}
	return d
}
