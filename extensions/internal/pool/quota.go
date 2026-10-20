// 每日限额护栏（补丁 0017，对标 hub 的每日护栏——简化两条先行：每日 Token + 保留积分）。
//
// 护栏 1 · 每日 Token：按账号累计当日成功调用的已知 token 观测（prompt+completion），
// 达到限额即把账号冷却到本地次日 0 点（CoolQuota）。计数喂入点是 usagelog.RecordAuth
// 挂的 observer（成功路径），重启后由 cmd 层从当天 usage JSONL 聚合重建——账本本身即
// 持久化，不新增落盘文件。0 点由 healthy 的 until 判定自然解封，签到/余额刷新不误放
// （reviveCoolingLocked 已排除有效 CoolQuota）。
//
// 护栏 2 · 保留积分：Pool.reserveCredits 之下的账号不参与轮换（normal/兜底/直取同口径），
// credits 由签到/余额刷新更新后 healthy 实时复判自然回池，无需解封动作。
//
// 与上游 Cooldown 的关键差异：护栏是本地配额管理而非上游故障信号，达额摘除**不喂熔断器**
// （计入熔断会让「全达额」升级成指数级封禁，0 点解封反而被 breakerUntil 拖长）；同时
// pickEarliestExpiryLocked 与 reviveCoolingLocked 对 CoolQuota 的特殊处理保证护栏
// 不被兜底消费与签到解冻击穿。
package pool

import (
	"fmt"
	"log"
	"sync"
	"time"

	"workbuddy2api/internal/logfmt"
)

// dayKeyLayout 护栏计数窗口的本地日期键；与 usagelog 的日文件名同刻度，
// 重建（按账本日界聚合）与运行时翻转使用同一口径。
const dayKeyLayout = "20060102"

// NextLocalMidnight 返回 now 之后最近的一个本地 0 点（护栏解封时刻）。
// 正处于 0 点整的一瞬返回次日 0 点（此时护栏窗口已翻转，调用方不会拿到 0 秒 Retry-After）。
func NextLocalMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

// SetReserveCredits 设置保留积分线（环境变量 WB2A_RESERVE_CREDITS，cmd 层接线）；
// <=0 关闭。运行期只允许设置一次语义（启动时调用，不提供在线调整）。
func (p *Pool) SetReserveCredits(credits int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reserveCredits = credits
}

// belowReserve 报告账号是否低于保留积分线（reserveCredits<=0 恒 false）。
// 调用方需已持有 p.mu（pick/pickEarliestExpiryLocked/PickByUID* 均在锁内调用）。
func (p *Pool) belowReserve(e *entry) bool {
	return p.reserveCredits > 0 && e.credits < p.reserveCredits
}

// CooldownQuotaUntil 每日护栏达额的专用冷却入口：写 until/coolKind/reason 并清模型级
// 冷却表（与 Cooldown 同口径），但**不喂熔断器**、不动 softStreak——护栏是本地配额
// 管理，不是「上游限流」信号，喂入会把护栏摘除升级成指数级熔断封禁并拖长解封。
// 同值重复触发（并发在途请求先后落账越过同一限额）幂等跳过，不反复置脏触发落盘。
func (p *Pool) CooldownQuotaUntil(uid string, until time.Time, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return
	}
	if e.coolKind == CoolQuota && e.until.Equal(until) {
		return
	}
	e.until = until
	e.coolKind = CoolQuota
	e.reason = reason
	e.modelCooldowns = nil // 账号级护栏生效期间模型豁免无意义，清空与 Cooldown 同口径
	p.dirty.Store(true)
	log.Printf("WARN: [pool] quota_guard uid=%s until=%s %s", logfmt.UID8(uid), until.Format(time.RFC3339), reason)
}

// DailyQuota 每日 Token 护栏。零值不可用，经 NewDailyQuota 构造；limit<=0 时全部
// 方法退化为 no-op/false，调用方无需逐点判开关。
type DailyQuota struct {
	pool  *Pool
	limit int64 // 每账号每日 token 上限；<=0 = 护栏关闭

	mu     sync.Mutex
	day    string           // 当前计数窗口的本地日期键
	tokens map[string]int64 // uid → 当日累计 token（已知观测）
}

// NewDailyQuota 构造护栏；p 用于达额摘除与 RealmExhausted 判定，limit 为每账号
// 每日 token 上限（<=0 护栏关闭，Observe 不计数、RealmExhausted 恒 false）。
func NewDailyQuota(p *Pool, limit int64) *DailyQuota {
	return &DailyQuota{pool: p, limit: limit, tokens: map[string]int64{}}
}

// Limit 返回护栏限额（<=0 = 关闭）；供 cmd 层接线时决定是否注入 handler.Config.Quota。
func (q *DailyQuota) Limit() int64 { return q.limit }

// ObserveTokens 喂入一次成功调用的 token 观测（usagelog.TokenObserver 实现；uid 为空
// 或护栏关闭时不计数；负值视为该侧未知的哨兵按 0 处理——已知部分照计，缺失不虚构
// 也不放大，与账本 Summarize 同口径）。
// 本地 0 点翻转时清零计数窗口。累计达到限额即 CooldownQuotaUntil 摘除到次日 0 点
// （计数型护栏天然滞后一次：达额那次调用本身已消费，摘除从下一个请求生效）。
func (q *DailyQuota) ObserveTokens(uid string, prompt, completion int) {
	if q.limit <= 0 || uid == "" {
		return
	}
	var add int64
	if prompt > 0 {
		add += int64(prompt)
	}
	if completion > 0 {
		add += int64(completion)
	}
	if add <= 0 {
		return // 无已知观测不计：凭 0 触发护栏没有意义，缺失窗口由账本侧兜底审计
	}
	q.mu.Lock()
	day := time.Now().Format(dayKeyLayout)
	if day != q.day {
		q.day = day
		q.tokens = map[string]int64{}
	}
	q.tokens[uid] += add
	total := q.tokens[uid]
	limit := q.limit
	q.mu.Unlock()
	if total >= limit {
		q.pool.CooldownQuotaUntil(uid, NextLocalMidnight(time.Now()),
			fmt.Sprintf("每日 Token 护栏达额（%d/%d）", total, limit))
	}
}

// Rebuild 重启重建：以当天账本按账号聚合的结果整体替换内存计数（day 为聚合所属的
// 本地日期键）。调用方（cmd 层）在 Open 后、服务前执行；若聚合时已跨天（重启恰逢
// 0 点附近），传入的计数属于昨日，调用方应传当天键或跳过重建——day 与当天不符时
// 本方法直接丢弃（昨日用量不应记入今日窗口）。
func (q *DailyQuota) Rebuild(day string, perUID map[string]int64) {
	if q.limit <= 0 || day == "" || day != time.Now().Format(dayKeyLayout) {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.day = day
	q.tokens = make(map[string]int64, len(perUID))
	for uid, n := range perUID {
		if n > 0 {
			q.tokens[uid] = n
		}
	}
}

// RealmExhausted 报告 realm 的非禁用账号是否全部处于有效护栏达额冷却（补丁 0017
// 的 QuotaGuard 实现）。存在任何「非护栏原因不可用」（软冷却/熔断/在途满）或「可用」
// 的账号都返回 false——429 只表达「护栏把该域全部活号摘除」，其他不可用维持 503
// 语义；域内没有非禁用账号（全禁用/空池）同样 false。
func (q *DailyQuota) RealmExhausted(realm string) bool {
	if q.limit <= 0 {
		return false
	}
	now := time.Now()
	p := q.pool
	p.mu.RLock()
	defer p.mu.RUnlock()
	saw := false
	for _, e := range p.byUID {
		if realm != "" && e.a.Realm() != realm {
			continue
		}
		if e.disabled {
			continue // 禁用号不是护栏对象（护栏不做禁用的代理判断）
		}
		saw = true
		if e.coolKind != CoolQuota || e.until.IsZero() || !now.Before(e.until) {
			return false
		}
	}
	return saw
}
