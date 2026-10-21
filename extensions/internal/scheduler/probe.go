// 通道周期性轻量探活（issue #15，扩展能力）：用「模型目录拉取」作为轻量探针
// （轻 GET，非 billing 口、非对话口），周期性验证 healthy 账号的凭据可用性，
// 提前发现失效凭据，而不是等对话 401/12153 打到用户流量上才暴露。
//
// 与周期余额刷新（balance.go）同款骨架：阻塞式 ticker 循环 + 账号间 800ms 限速 +
// 环境变量开关。口径与有意偏差：
//   - 默认关闭：WB2A_PROBE_HOURS 未设置或 <=0 不启动；>0 为间隔小时数（支持小数），
//     下限钳 1h（防误配 0.01 之类把上游目录口打爆）。部署建议 6（每天 4 轮）。
//   - 只探 normal healthy 账号：禁用、任一冷却/熔断态（Status.Cooling 覆盖
//     until+breakerUntil）、在途满号一律跳过——探活是体检，不给病号加号。
//   - 失败动作：单次失败 CoolSoft 30 分钟 + WARN 日志（含 uid 与错误分类短语，
//     不打错误全文）；同一账号连续 3 次探活失败 CoolHard 到次日 04:00（凭据疑似
//     失效，与 402 同语义，等余额刷新解冻闭环兜底）。成功清连败计数。
//   - 绝不喂熔断器：目录口失败不代表对话通道故障，冷却经 pool.ProbeCooldown /
//     ProbeCooldownHard 写入，不汇入对话侧熔断统计，也不动 softStreak/pin/粘性。
//   - 一轮内失败不重试，等下一轮；上一轮未跑完则跳过本轮（防重入）。
//   - 连败计数是探活器自有内存 map，不落盘：重启清零重新观察（周期体检，丢计数
//     可接受）。挂在包级而非 Scheduler 字段：Scheduler 定义在上游 scheduler.go，
//     零补丁约束下扩展不能给它加字段；进程内调度器单例，包级语义等价。
package scheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workbuddy2api/internal/logfmt"
)

// minProbeInterval 探活间隔下限：防误配小数小时把上游目录口打爆。
const minProbeInterval = time.Hour

// probeAccountDelay 账号间限速：与余额刷新（balance.go）同款 800ms。
const probeAccountDelay = 800 * time.Millisecond

// probeFailThreshold 同一账号连续探活失败次数阈值：达到即 CoolHard（凭据疑似失效）。
const probeFailThreshold = 3

// probeSoftCooldown 单次探活失败的软冷却时长：可疑号暂移出轮换，等下一轮探活验证。
// var 而非 const：interval 下限 1h > 30min，生产上下一轮时软冷却必已过期；测试把
// 轮次背靠背执行，需注入短时长才能等价复现「冷却过期→下轮再探」的连败升级路径。
var probeSoftCooldown = 30 * time.Minute

// 探活器自有运行态（见文件头「连败计数」说明）。
var (
	probeFailsMu sync.Mutex
	probeFails   = map[string]int{} // uid → 连续探活失败次数
	probeRunning atomic.Bool        // 防重入闸：上一轮没跑完就到下一轮时跳过
)

// ProbeInterval 从环境变量 WB2A_PROBE_HOURS 读探活周期：未设置或 <=0 返回 0（关闭）；
// >0 为间隔小时数，下限钳 1h；非法值告警并视为关闭。重启生效，部署建议 6。
func (s *Scheduler) ProbeInterval() time.Duration {
	v := strings.TrimSpace(os.Getenv("WB2A_PROBE_HOURS"))
	if v == "" {
		return 0
	}
	h, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("WARN: probe: WB2A_PROBE_HOURS=%q 非法，探活保持关闭", v)
		return 0
	}
	if h <= 0 {
		return 0
	}
	d := time.Duration(h * float64(time.Hour))
	if d < minProbeInterval {
		// 覆盖小数误配与极端大值溢出成负数两种情形，统一钳回下限（限速护栏兜底，安全）。
		log.Printf("WARN: probe: WB2A_PROBE_HOURS=%q 低于下限，钳制为 %v", v, minProbeInterval)
		d = minProbeInterval
	}
	return d
}

// RunProbe 阻塞式周期循环：每 interval 触发一轮探活，ctx 取消退出。调用方用
// `go sch.RunProbe(ctx, interval)` 启动；interval<=0 直接返回（默认关闭）。
func (s *Scheduler) RunProbe(ctx context.Context, interval time.Duration) {
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
			s.ProbeHealthyAccounts(ctx)
		}
	}
}

// ProbeHealthyAccounts 执行一轮探活：healthy 账号逐个拉模型目录。单号失败只冷却
// 并记日志，不影响其他号；一轮内失败不重试，等下一轮。上一轮未结束时跳过本轮。
func (s *Scheduler) ProbeHealthyAccounts(ctx context.Context) {
	if !probeRunning.CompareAndSwap(false, true) {
		log.Print("probe: 上一轮探活未结束，跳过本轮")
		return
	}
	defer probeRunning.Store(false)

	uids := make([]string, 0, 8)
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled || st.Cooling {
			continue // 禁用 / 任一冷却或熔断态（Cooling = until 或 breakerUntil 生效中）
		}
		if s.cfg.Pool.InFlightFull(st.UID) {
			continue // 在途满号：探活不与对话流量抢租约
		}
		uids = append(uids, st.UID)
	}
	probed, failed := 0, 0
	for i, uid := range uids {
		if ctx.Err() != nil {
			return
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(probeAccountDelay):
			}
		}
		a := s.cfg.Pool.AuthByUID(uid)
		if a == nil || a.AccessToken == "" {
			continue
		}
		probed++
		// FetchModels 内部按账号 realm（globalOn 双闸）选 CN/global 目录端点，沿用
		// 既有方法即可。它不带 ctx 形参，单号挂死由 Client.HTTP 的 Timeout 兜底，
		// 轮内只做账号间取消检查。
		if _, err := s.cfg.Upstream.FetchModels(a); err != nil {
			failed++
			s.probeFail(uid, err)
			continue
		}
		s.probeOK(uid)
	}
	if probed > 0 {
		log.Printf("probe: 一轮探活完成，探活 %d 个账号，失败 %d 个", probed, failed)
	}
}

// probeFail 记一次探活失败：单次失败软冷却 30 分钟；连续达到阈值硬冷却到次日
// 04:00 并清零计数（账号进入硬冷却后退出探活名单，余额刷新解冻后从零重新观察）。
func (s *Scheduler) probeFail(uid string, err error) {
	class := probeErrClass(err)
	probeFailsMu.Lock()
	probeFails[uid]++
	n := probeFails[uid]
	if n >= probeFailThreshold {
		delete(probeFails, uid)
	}
	probeFailsMu.Unlock()

	if n >= probeFailThreshold {
		s.cfg.Pool.ProbeCooldownHard(uid, fmt.Sprintf("探活连续 %d 次失败(%s)，凭据疑似失效", n, class))
		log.Printf("WARN: probe %s: 连续 %d 次探活失败，硬冷却到次日 04:00（凭据疑似失效）", logfmt.UID8(uid), n)
		return
	}
	s.cfg.Pool.ProbeCooldown(uid, probeSoftCooldown, fmt.Sprintf("探活失败(%d/%d)", n, probeFailThreshold))
	log.Printf("WARN: probe %s: 探活失败(%d/%d) class=%s", logfmt.UID8(uid), n, probeFailThreshold, class)
}

// probeOK 探活成功：清连败计数。不写池——探活成功不代表对话成功，不喂 NoteSuccess。
func (s *Scheduler) probeOK(uid string) {
	probeFailsMu.Lock()
	delete(probeFails, uid)
	probeFailsMu.Unlock()
}

// probeErrClass 把 FetchModels 的错误压缩成分类短语（进 WARN 日志，不打错误全文，
// 全文可能含上游响应片段）。FetchModels 返回普通 error（非 upstream.Error），按
// 其消息前缀归类：models api status NNN → http_NNN；解析失败 → parse；目录空 →
// empty_catalog；业务码 → api_code；其余（网络/超时）→ network。
func probeErrClass(err error) string {
	msg := err.Error()
	const statusPrefix = "models api status "
	if i := strings.Index(msg, statusPrefix); i >= 0 {
		rest := msg[i+len(statusPrefix):]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j > 0 {
			return "http_" + rest[:j]
		}
	}
	switch {
	case strings.Contains(msg, "models parse"):
		return "parse"
	case strings.Contains(msg, "no cli agent models found"), strings.Contains(msg, "returned empty list"):
		return "empty_catalog"
	case strings.Contains(msg, "models api code="):
		return "api_code"
	default:
		return "network"
	}
}
