// wafip.go WAF IP 级拦截 fail-fast 状态机（上游 8825c4c 的最小化回移）。
//
// 背景：WAF 403 拦的是网关出口 IP 而非账号（原作者实测 3 个账号 1 秒内全部 403）。
// 本仓快照只有账号级轮转：一次客户端请求最多被放大 MaxRotate 倍，继续换号等于拿
// 同一出口 IP 再向上游打一遍，只会加重风控。
//
// 判定：在 ErrWafBlock 分类之上做短窗多号计数——wafIPWindow（60s 窗）内出现
// ≥ wafIPThreshold 个**不同** UID 命中 WAF 403 → 判 IP 级拦截，激活至
// now+wafIPWindow。单号反复 403（账号级偶发）永不触发：只数不同号。激活期内新
// 命中不续期（保守：不做主动探测，窗口自然解除）。
//
// 归属层：放 server（Handler 局部）而非 pool——IP 级状态的唯一消费者是
// chatCompletions 轮转循环（是否继续轮转）；pool 是账号级记账层，跨 UID 语义不属于
// 任何账号。server 已有进程级状态先例 degradeGate（mu+until 同风格）。账号级记账
// 保持不变（applyErrorPolicy 一字不改），IP 级状态只改变「是否继续轮转」——协同不
// 叠加。进程内状态、重启清零（窗口 60s，重建成本极低）。
package server

import (
	"log"
	"sync"
	"time"
)

// wafIPWindow IP 级判定窗 + 激活时长：窗内不同账号命中 WAF 403 达阈值即判 IP 级
// 拦截，激活同样长（到期自然解除）。var 仅供测试注入短窗（生产恒 60s）。
var wafIPWindow = 60 * time.Second

// wafIPThreshold 判定阈值：窗内不同 UID 数达到该值激活。取 2——「多号」的最小定义：
// 单号反复 403 永不触发，两个不同号在 60s 内接连被拦（同一出口 IP）已是 IP 级证据，
// 阈值 2 比 3 更早止损，少放大一次轮转。
const wafIPThreshold = 2

// wafIPGate WAF IP 级拦截状态机（Handler 内嵌，零值可用）。
type wafIPGate struct {
	mu    sync.Mutex
	hits  map[string]time.Time // uid → 最近一次 WAF 403 时刻（判定窗内，惰性剪枝）
	until time.Time            // IP 级拦截激活截止；零值 = 未激活
}

// noteWaf 记一次某账号的 WAF 403，返回记账后 IP 级拦截是否激活（调用方据此
// fail-fast 终止轮转）。
//   - 已激活（now < until）：不续期、不记账 → true；
//   - 未激活：记 hits[uid]=now（同号重复命中覆盖不累计，判定口径是「不同号数」），
//     剪掉窗外的过期命中；不同 UID 数达 wafIPThreshold → 激活到 now+wafIPWindow
//     （打一条 WARN 供观测），清空判定窗（解除后需全新命中重新判定，不叠旧账）。
func (g *wafIPGate) noteWaf(uid string) bool {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.until) {
		return true // 激活期内新命中：不续期（自然解除语义）
	}
	if g.hits == nil {
		g.hits = map[string]time.Time{}
	}
	g.hits[uid] = now
	for u, t := range g.hits {
		if now.Sub(t) > wafIPWindow {
			delete(g.hits, u)
		}
	}
	if len(g.hits) >= wafIPThreshold {
		g.until = now.Add(wafIPWindow)
		log.Printf("WARN: [server] waf ip-level block: %d accounts hit waf 403 within %s, rotate fail-fast until %s", len(g.hits), wafIPWindow, g.until.Format(time.RFC3339))
		g.hits = map[string]time.Time{}
		return true
	}
	return false
}

// active 报告 IP 级拦截是否激活（末端错误文案区分 IP 级/账号级措辞用）。
func (g *wafIPGate) active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return time.Now().Before(g.until)
}
