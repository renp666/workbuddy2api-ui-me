package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 常态健康检查：console 本身就在各旁路的网络命名空间内（旁路都是
// network_mode: service:console），因此由 console 自己按固定间隔探活，
// 无需新增独立 sidecar。这里复用各通道已有的探测函数，只做聚合与留存，
// 不改变任何既有分流语义。

// healthProbeInterval 是后台探活间隔。探活只读端口/服务存活，不消耗上游额度，
// 与 probe.go 的手工测速探测（会真实打模型、消耗额度）是两回事。
const healthProbeInterval = 30 * time.Second

// healthProbeTimeout 是单轮探活的总超时。各通道探测自身已有 3 秒超时，
// 这里再兜一层，避免个别通道挂死拖住整轮。
const healthProbeTimeout = 10 * time.Second

// healthHistorySize 是留存的历史轮数。内存态保留最近这些轮；落盘的探测历史
// （health-history-<YYYYMMDD>.jsonl）在启动时只回放最近这些轮进内存快照。
const healthHistorySize = 20

// healthHistoryPrefix 是探测历史 JSONL 的文件名前缀，按天轮转为
// health-history-<YYYYMMDD>.jsonl，与错误账本 errors-<YYYYMMDD>.jsonl 同目录、
// 同保留期（30 天，复用 jsonlStore 的轮转清理）。目录是 bind mount，重启不丢。
const healthHistoryPrefix = "health-history"

// healthProbeSeedInterval 是后台「补齐真调用结论」的节流间隔。总览的绿灯必须有真
// 调用证据支撑：已启用通道若「没有任何结论」或「结论为失败」，每隔这个间隔用最小
// 代价重探一次（每通道取目录前 N 个模型），既不让「没测过」长期挂在未知态，也不让
// 一次瞬时失败把通道永久钉在红灯。上游额度消耗被节流到 ≤ 通道数×N 次/间隔。
const healthProbeSeedInterval = 5 * time.Minute

// healthProbeSeedModels 是补齐探测每通道取的模型数（取目录前 N 个）。
const healthProbeSeedModels = 1

// healthChannelResult 是单条通道一轮探活的结论。
//
// Reachable 只回答「端口/服务是否活着」，不回答「模型能不能真跑」——两者必须
// 分开呈现，否则手工测速探测已经测出模型失败时，总览仍报绿灯，就是「假绿」。
// Probe* 字段把 probe.go 的真调用结论（零额外上游成本，只读内存缓存）带进同一条
// 记录：state=failed 表示该通道当前没有一跳能真跑通。
type healthChannelResult struct {
	Channel   routeChannel `json:"channel"`
	Enabled   bool         `json:"enabled"`
	Reachable bool         `json:"reachable"`
	LatencyMs int64        `json:"latency_ms"`
	// Detail 是附加说明：失败时是原因，autoclaw 成功时附带 healthy/total 计数。
	Detail string `json:"detail,omitempty"`
	// ProbeState 取自真调用结论（手工测速探测；opencode 取 OW Bridge /health）：
	// 空=未探测（未知，不计为健康），ok=有可用模型，failed=有结论但无一可用。
	ProbeState string `json:"probe_state,omitempty"`
	// ProbeOKModels / ProbeTotalModels 是真调用结论里的可用模型数与已测模型数。
	ProbeOKModels    int `json:"probe_ok_models,omitempty"`
	ProbeTotalModels int `json:"probe_total_models,omitempty"`
	// ProbeDetail 是首个失败模型的原因摘要，便于页面直接解释红灯。
	ProbeDetail string `json:"probe_detail,omitempty"`
	// ProbeHint 是通道侧补充的「可执行下一步」，只在红灯且通道确有线索时出现。
	// 例：raccoon 的凭证被上游拒 → 「token_expired: 请重新导入凭证」；core 的严格 pin
	// 账号在冷却 → 指出锁的是谁、卡在哪、多久恢复。让红灯自带处置办法，而不是只丢一句
	// 笼统的 no_healthy_account / pinned_account_unavailable 让人去猜。
	ProbeHint string `json:"probe_hint,omitempty"`
	// public 是本轮代表模型（对外形态），category 是真调用失败时的归类。二者不对外
	// 序列化（无 json tag 的未导出字段），只供探测历史落盘——历史行要能回答「哪条通道
	// 上的哪个模型、因何失败」，让重启后的回放不是只有一串布尔值。
	public   string
	category string
}

// healthy 归纳一条通道记录是否「健康」。判定口径与 runHealthRound 完全一致：端口/服务
// 可达，且真调用结论为 ok。端口活着但没测过或测出失败都不算健康——落盘历史与内存快照
// 共用这一处判定，避免两套口径各说各话。
func (r healthChannelResult) healthy() bool {
	return r.Reachable && r.ProbeState == "ok"
}

// probeVerdict 归纳某通道的真调用结论。
// 无结论时 state 为空串——「没测过」与「测了不行」必须区分，不得把未知当健康。
// opencode 没有手工探测：它的逐模型结论来自 OW Bridge 自带 /health.modelResults，
// 单独取；其余通道读手工测速探测缓存。
func (h *server) probeVerdict(ctx context.Context, c routeChannel) (state string, okModels, totalModels int, detail string) {
	results := h.probe.channelResults(c)
	if len(results) == 0 {
		if c == channelOpenCode {
			return h.opencodeProbeVerdict(ctx)
		}
		return "", 0, 0, ""
	}
	totalModels = len(results)
	for _, value := range results {
		if ok, _ := value["ok"].(bool); ok {
			okModels++
			continue
		}
		if detail == "" {
			if note, _ := value["error"].(string); note != "" {
				detail = note
			} else if category, _ := value["category"].(string); category != "" {
				detail = category
			}
		}
	}
	if okModels > 0 {
		return "ok", okModels, totalModels, ""
	}
	return "failed", 0, totalModels, detail
}

// opencodeProbeVerdict 从 OW Bridge /health.modelResults 归纳 opencode 的真调用结论，
// 与手工探测缓存同构（ok/failed/空）。OW Bridge 的 /health 本身就是逐模型探测结果，
// 所以这里不额外产生上游调用，只是把已有结论接进总览——否则 opencode 会因「没有
// 手工结论」被误判成未探测，甚至被当成不可用。
func (h *server) opencodeProbeVerdict(ctx context.Context) (string, int, int, string) {
	health := h.opencodeHealth(ctx)
	if health == nil {
		return "", 0, 0, ""
	}
	raw, _ := health["modelResults"].(map[string]any)
	if len(raw) == 0 {
		return "", 0, 0, ""
	}
	total, okModels := 0, 0
	detail := ""
	for name, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		total++
		if success, _ := entry["ok"].(bool); success {
			okModels++
			continue
		}
		if detail == "" {
			if note, _ := entry["error"].(string); note != "" {
				detail = note
			} else if category, _ := entry["category"].(string); category != "" {
				detail = category
			} else {
				detail = name
			}
		}
	}
	if total == 0 {
		return "", 0, 0, ""
	}
	if okModels > 0 {
		return "ok", okModels, total, ""
	}
	return "failed", 0, total, detail
}

// healthRound 是一轮「网关 + 七通道」总览。
type healthRound struct {
	CheckedAt time.Time             `json:"checked_at"`
	OK        bool                  `json:"ok"`
	Channels  []healthChannelResult `json:"channels"`
}

// healthHistoryEntry 是历史轮次的紧凑形态，只留每通道可达与否，供趋势与告警。
type healthHistoryEntry struct {
	CheckedAt time.Time             `json:"checked_at"`
	OK        bool                  `json:"ok"`
	Channels  map[routeChannel]bool `json:"channels"`
}

func (r healthRound) historyEntry() healthHistoryEntry {
	states := make(map[routeChannel]bool, len(r.Channels))
	for _, c := range r.Channels {
		if c.Enabled {
			states[c.Channel] = c.healthy()
		}
	}
	return healthHistoryEntry{CheckedAt: r.CheckedAt, OK: r.OK, Channels: states}
}

// healthHistoryLine 是落盘的一轮探测结论（PRD §2-F3）：一行一轮，rounds 数组逐通道
// 记录代表模型、健康与否、失败归类与耗时。ts 是轮次时间（Unix 毫秒）。
type healthHistoryLine struct {
	Ts     int64                  `json:"ts"`
	Rounds []healthHistoryChannel `json:"rounds"`
}

// healthHistoryChannel 是落盘历史里的单通道结论，字段名与 PRD §2-F3 的单行 schema
// 一一对应。字段一律不带 omitempty：历史行是排障用的持久真源，schema 必须稳定，
// 空值也要显式落出来（下游 jq/python 聚合才不必区分「键不存在」与「值为空」）。
type healthHistoryChannel struct {
	Channel    routeChannel `json:"channel"`
	Public     string       `json:"public"`
	OK         bool         `json:"ok"`
	Category   string       `json:"category"`
	DurationMs int64        `json:"durationMs"`
}

// historyLine 把一轮总览压成落盘行。只落已启用通道（与 historyEntry 同一取舍）：
// 未启用通道的「不可达」是配置事实而非探测结论，落盘只会污染趋势。
func (r healthRound) historyLine() healthHistoryLine {
	rounds := make([]healthHistoryChannel, 0, len(r.Channels))
	for _, c := range r.Channels {
		if !c.Enabled {
			continue
		}
		rounds = append(rounds, healthHistoryChannel{
			Channel:    c.Channel,
			Public:     c.public,
			OK:         c.healthy(),
			Category:   c.category,
			DurationMs: c.LatencyMs,
		})
	}
	return healthHistoryLine{Ts: r.CheckedAt.UnixMilli(), Rounds: rounds}
}

// entry 把落盘行还原成内存快照形态（启动回放时用）。整轮 OK 由各通道结论归并：
// 任一通道不健康即 false，与 runHealthRound 的判定同源。
func (l healthHistoryLine) entry() healthHistoryEntry {
	states := make(map[routeChannel]bool, len(l.Rounds))
	ok := true
	for _, c := range l.Rounds {
		states[c.Channel] = c.OK
		if !c.OK {
			ok = false
		}
	}
	return healthHistoryEntry{CheckedAt: time.UnixMilli(l.Ts), OK: ok, Channels: states}
}

// healthCache 保留最近 healthHistorySize 轮后台探活结论。内存快照用于接口返回；
// 同一轮还会 append 落盘到 health-history-<YYYYMMDD>.jsonl（按天轮转、保留 30 天），
// 启动时回放最近 ≤healthHistorySize 轮——这样重启 console 不会丢掉探测结论。
type healthCache struct {
	mu       sync.Mutex
	history  []healthHistoryEntry
	lastSeed time.Time
	// store 是探测历史的 JSONL 落盘器。为 nil 时只留内存（测试或未配置可写目录），
	// 行为与改造前一致。
	store *jsonlStore
	// initOnce 保证「清理过期文件 + 回放历史」只跑一次（后台协程与 /admin/health
	// 谁先到都行）。
	initOnce sync.Once
}

func (c *healthCache) record(round healthRound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, round.historyEntry())
	if len(c.history) > healthHistorySize {
		c.history = c.history[len(c.history)-healthHistorySize:]
	}
	if c.store == nil {
		return
	}
	// 落盘失败保持沉默（探测结论是观测，不是调用契约）：内存快照已更新，接口照常。
	// 在锁内 append 保证内存顺序与落盘顺序一致（写入频率低，一行小写入的代价可忽略）。
	if err := c.store.append(round.historyLine()); err != nil {
		log.Print("WARN: [health-history] 探测历史落盘失败（保持沉默）：", err)
	}
}

// healthInit 在第一次探活/查询前完成一次性初始化：清理过期文件（保留 30 天），并把
// 落盘历史回放最近 ≤healthHistorySize 轮进内存快照。这样重启 console 后健康接口立刻
// 有结论（health present: True），而不是从空历史重来。未开启模块（HealthProbe=false）
// 或没有可写目录时不碰磁盘，行为与改造前完全一致。
func (c *healthCache) healthInit(cfg Config) {
	c.initOnce.Do(func() {
		if !cfg.HealthProbe {
			return
		}
		dir := consoleDataDir(cfg.RouteFile)
		if dir == "" {
			return
		}
		store := newJSONLStore(dir, healthHistoryPrefix, false)
		store.cleanup(time.Now())
		history := replayHealthHistory(dir, healthHistorySize)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.store = store
		if len(history) == 0 {
			return
		}
		// 回放结果在前、内存里可能已有的轮次在后；超出上限时丢最旧的。
		combined := append(history, c.history...)
		if len(combined) > healthHistorySize {
			combined = combined[len(combined)-healthHistorySize:]
		}
		c.history = combined
	})
}

// replayHealthHistory 从落盘历史里回放最近 limit 轮（时间升序返回）。
//
// 读取顺序：文件名按日期升序，逐文件从最后一行往前读（同一文件内后写的在后），凑够
// limit 轮即停；最后反转为时间升序，与内存快照的追加顺序一致。文件不存在（首次启动）
// 返回 nil；损坏行跳过，不让单条脏数据毁掉整段回放。
func replayHealthHistory(dir string, limit int) []healthHistoryEntry {
	if limit <= 0 {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, healthHistoryPrefix+"-*.jsonl"))
	sort.Strings(matches)
	var out []healthHistoryEntry // 新在前，最后反转
	for i := len(matches) - 1; i >= 0 && len(out) < limit; i-- {
		if _, ok := rotatedDay(healthHistoryPrefix, filepath.Base(matches[i])); !ok {
			continue // 非本前缀的按天文件（如残留的半成品名）一律不读
		}
		lines := readJSONLLines(matches[i])
		for j := len(lines) - 1; j >= 0 && len(out) < limit; j-- {
			var line healthHistoryLine
			if json.Unmarshal(lines[j], &line) != nil {
				continue
			}
			out = append(out, line.entry())
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// readJSONLLines 读出一个 JSONL 文件的全部非空行（保持文件内原始顺序）。读失败返回 nil。
func readJSONLLines(path string) [][]byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, []byte(line))
	}
	return out
}

// seedDue 报告距上次补齐探测是否已过一个节流间隔。零值 lastSeed 让启动后立即补齐。
func (c *healthCache) seedDue(interval time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastSeed) >= interval
}

// markSeeded 记录一次补齐探测的发起时刻（成功发起才调用）。
func (c *healthCache) markSeeded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeed = time.Now()
}

func (c *healthCache) snapshot() []healthHistoryEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]healthHistoryEntry, len(c.history))
	copy(out, c.history)
	return out
}

// healthAutoStart 按固定间隔跑一轮探活并留存，启动即先跑一次，
// 让刚起好的容器不用等一个完整间隔就有结论。
func (h *server) healthAutoStart() {
	h.health.healthInit(h.cfg)
	ticker := time.NewTicker(healthProbeInterval)
	defer ticker.Stop()
	h.healthOnce()
	for range ticker.C {
		h.healthOnce()
	}
}

func (h *server) healthOnce() {
	h.maybeSeedProbes()
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	h.health.record(h.runHealthRound(ctx))
}

// maybeSeedProbes 在后台按节流间隔，给「没有结论」或「结论为失败」的已启用通道补一次
// 轻量真调用探测（每通道 1 个模型），让总览的绿/红都有真调用证据：绿灯不再可能建立在
// 「没测过」之上；一次瞬时失败也会在下一个间隔被重探，不会永久钉在红灯。已有 ok 结论
// 的通道不打扰（真实额度消耗因此有界）。
func (h *server) maybeSeedProbes() {
	if !h.health.seedDue(healthProbeSeedInterval) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var need []routeChannel
	for _, c := range routeChannelOrder {
		if c == channelOpenCode || !h.channelEnabled(c) {
			continue
		}
		if state, _, _, _ := h.probeVerdict(ctx, c); state != "ok" {
			need = append(need, c)
		}
	}
	if len(need) == 0 {
		h.health.markSeeded()
		return
	}
	if err := h.probe.lightStart(h, ctx, need, healthProbeSeedModels); err != nil {
		return // 已有探测在跑 / 当前没有可探测的模型：下一个间隔再试
	}
	h.health.markSeeded()
}

// runHealthRound 并行探活 routeChannelOrder 里的全部已启用通道，任一已启用通道
// 「不可达 / 无真调用证据 / 真调用失败」即整轮 OK=false（core 恒启用，故 core
// 不可达必然失败）。
func (h *server) runHealthRound(ctx context.Context) healthRound {
	results := make([]healthChannelResult, len(routeChannelOrder))
	var wg sync.WaitGroup
	for i, channel := range routeChannelOrder {
		results[i] = healthChannelResult{Channel: channel, Enabled: h.channelEnabled(channel)}
		if !results[i].Enabled {
			continue
		}
		wg.Add(1)
		go func(index int, c routeChannel) {
			defer wg.Done()
			results[index] = h.probeChannelHealth(ctx, c)
		}(i, channel)
	}
	wg.Wait()
	round := healthRound{CheckedAt: time.Now(), OK: true, Channels: results}
	markSidecarIsolation(&round)
	for _, result := range results {
		if !result.Enabled {
			continue
		}
		// 端口不可达、真调用结论为失败、或根本没有真调用结论，都不算健康。
		// 只认 Reachable 就是「假绿」；把「没测过」也算绿同样是把未知当健康。
		if !result.healthy() {
			round.OK = false
		}
	}
	return round
}

// markSidecarIsolation 识别「console 被单独重启/重建」这一特定故障，并给每个旁路
// 通道挂上可执行的处置提示。
//
// 全部旁路都是 network_mode: service:console，共享 console 的网络命名空间。console
// 被单独 restart/recreate 后拿到新的网络命名空间，旁路仍留在旧的那一个里：从 console
// 内部看，每个旁路的 127.0.0.1:<端口> 全部不可达，而 core（走 compose 网络）照常健康。
// 这个组合特征足够独特——「core 可达 + 所有已启用旁路同时不可达」基本只可能是它，
// 而不是 N 条通道各自独立地同时坏掉。
//
// 专门识别它的理由：此时 docker ps 里旁路全是 Up（旧命名空间还活着），容器 ID 锚点
// 也仍指向当前 console（容器没被重建，ID 不变），常规排查手段全部显示「正常」，
// 只有从 console 内部实际连一下才能发现——为此绕过两轮。
func markSidecarIsolation(round *healthRound) {
	sidecars, down := 0, 0
	coreOK := false
	for _, r := range round.Channels {
		if !r.Enabled {
			continue
		}
		if r.Channel == channelCore {
			coreOK = r.Reachable
			continue
		}
		sidecars++
		if !r.Reachable {
			down++
		}
	}
	// 只配了 1 条旁路时不套用：单通道掉线有自己的原因，硬套网络命名空间说法会误导。
	if !coreOK || sidecars < 2 || down != sidecars {
		return
	}
	const hint = "core 正常但全部旁路同时不可达：console 被单独重启/重建，旁路仍留在旧网络命名空间。" +
		"需把 console 与全部旁路放进同一条 compose 命令 --force-recreate 重建。"
	for i := range round.Channels {
		r := &round.Channels[i]
		if r.Enabled && r.Channel != channelCore && !r.Reachable && r.ProbeHint == "" {
			r.ProbeHint = hint
		}
	}
}

func (h *server) probeChannelHealth(ctx context.Context, c routeChannel) healthChannelResult {
	start := time.Now()
	reachable, detail := h.channelReachable(ctx, c)
	result := healthChannelResult{
		Channel:   c,
		Enabled:   true,
		Reachable: reachable,
		LatencyMs: time.Since(start).Milliseconds(),
		Detail:    detail,
	}
	// 并入手工测速探测的真调用结论（只读缓存，不触发上游调用）。端口活着但
	// 真调用失败时必须显式暴露，否则总览的绿灯会把「服务在、模型跑不动」瞒过去。
	result.ProbeState, result.ProbeOKModels, result.ProbeTotalModels, result.ProbeDetail = h.probeVerdict(ctx, c)
	// 代表模型与失败归类只为探测历史落盘准备（不对外序列化）：历史行要能回答
	// 「哪条通道上的哪个模型、因何失败」。取目录里字典序最小的模型作稳定代表。
	result.public, result.category = h.probeModelSummary(c)
	// 账号池通道在红灯时补一条可执行提示：raccoon 的失败绝大多数来自凭证侧
	// （refresh_token 被上游拒），把账号池记录的 reason 带出来，人才能直接知道要重登
	// 桌面端再导入，而不是对着 no_healthy_account 干瞪眼。只在失败时取，开销有界。
	if c == channelRaccoon && result.ProbeState != "ok" {
		result.ProbeHint = h.raccoonAccountHint(ctx)
	}
	if c == channelCore && result.ProbeState != "ok" {
		result.ProbeHint = h.corePinHint(ctx)
	}
	return result
}

// probeModelSummary 给出某通道本轮的代表模型（对外形态）与失败归类，供探测历史落盘。
//
// 结论按公共模型名为键，map 遍历无序，所以这里按名字排序取第一个作稳定代表——同一
// 状态下反复落盘的行完全一致，趋势比对不会被随机顺序搅乱。失败归类取「首个失败模型」
// 的 category（与 probeVerdict 的 detail 取舍同源），无失败时为空。
func (h *server) probeModelSummary(c routeChannel) (string, string) {
	results := h.probe.channelResults(c)
	if len(results) == 0 {
		return "", ""
	}
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	public := names[0]
	category := ""
	for _, name := range names {
		if ok, _ := results[name]["ok"].(bool); !ok {
			if value, _ := results[name]["category"].(string); value != "" {
				category = value
			}
			break
		}
	}
	return public, category
}

// corePinHintTimeout 是取 core 账号池/pin 快照的超时。只在红灯时调用，开销有界。
const corePinHintTimeout = 3 * time.Second

// corePinHint 在 core 红灯时补一条账号侧的可执行解释。
//
// core 的账号是「严格 pin」的（patch 0010：同一 realm 锁定一个账号，被锁账号不可用即
// 503 pinned_account_unavailable，且不做故障转移），realm 内其余账号再健康也用不上。
// 于是 pinned_account_unavailable 单独摆在那儿，人完全不知道下一步该干什么。这里把 pin
// 与账号池快照拼成一句人话：域里锁的是谁、卡在哪、预计多久恢复、域内有没有能顶上却因
// 锁定而不会顶上的账号。
func (h *server) corePinHint(ctx context.Context) string {
	if h.cfg.CoreURL == nil {
		return ""
	}
	status, code, err := h.coreCtl(ctx, "GET", "/internal/v1/status", corePinHintTimeout)
	if err != nil || code != 200 {
		return ""
	}
	pins, _, _ := h.coreCtl(ctx, "GET", "/internal/v1/pin", corePinHintTimeout)

	type account struct {
		uid, nickname, realm, reason string
		cooling                      bool
		remaining                    float64
	}
	var accounts []account
	for _, item := range sliceOf(status["accounts"]) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		accounts = append(accounts, account{
			uid:       strOf(m, "uid"),
			nickname:  orDefault(strOf(m, "nickname"), strOf(m, "uid")),
			realm:     strOf(m, "realm"),
			reason:    strOf(m, "reason"),
			cooling:   m["cooling"] == true,
			remaining: numOf(m, "cool_remaining_sec"),
		})
	}

	// pin 按 realm 索引，用来判断「正在出问题的账号是不是恰好被锁住的那个」。
	pinned := map[string]string{}
	for realm, item := range mapOf(pins["pins"]) {
		if m, ok := item.(map[string]any); ok {
			pinned[realm] = strOf(m, "uid")
		}
	}

	var parts []string
	for _, realm := range []string{"global", "cn"} {
		locked := -1
		for i := range accounts {
			if accounts[i].realm == realm && accounts[i].uid == pinned[realm] {
				locked = i
				break
			}
		}
		if locked < 0 {
			continue // 这个域没有 pin 或 pin 的账号不在池内：不是本机制能解释的红灯
		}
		if !accounts[locked].cooling {
			continue // 被锁账号本身健康：红灯另有原因，别硬找说法
		}
		pin := accounts[locked]
		part := fmt.Sprintf("%s 域锁定的 %s %s", realm, pin.nickname, orDefault(pin.reason, "暂不可用"))
		if pin.remaining > 0 {
			part += fmt.Sprintf("，约 %.0f 秒后恢复", pin.remaining)
		}
		// 严格锁定下，域内其他健康账号不会顶上——这句话必须说出来，
		// 否则「域里其实有健康账号」和「红灯」看起来互相矛盾。
		var spare string
		for i := range accounts {
			if i != locked && accounts[i].realm == realm && !accounts[i].cooling {
				spare = accounts[i].nickname
				break
			}
		}
		if spare != "" {
			part += fmt.Sprintf("；严格锁定不做故障转移，域内 %s 可用但不会顶上（需先解除锁定）", spare)
		} else {
			part += "；严格锁定不做故障转移，且域内无其他可用账号"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "；")
}

// strOf 取 map 里的字符串字段；不存在或类型不对时返回空串。
func strOf(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

// numOf 取 map 里的数值字段（JSON 数字一律解成 float64）。
func numOf(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

// mapOf / sliceOf 把 JSON 里解出来的 any 收窄成常用容器，nil 时返回空值而非 panic。
func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
}

// orDefault 在 s 为空时给一个兜底说法，避免拼出「域锁定的 xx 」这种断尾句子。
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// channelReachable 按通道复用既有探测口径判定可达性。core 与 glm/qoder 没有
// 独立 health 端点：core 以桥接 /internal/v1/info 握手为准，glm/qoder 以
// /v1/models 目录抓取成功为准，与页面展示的口径保持一致。
func (h *server) channelReachable(ctx context.Context, c routeChannel) (bool, string) {
	switch c {
	case channelCore:
		if _, err := h.coreInfo(ctx); err != nil {
			return false, err.Error()
		}
		return true, ""
	case channelGLM, channelQoder:
		if _, ok := h.fetchChannelModels(ctx, c); !ok {
			return false, "模型目录不可达"
		}
		return true, ""
	case channelOpenCode:
		if h.opencodeHealth(ctx) == nil {
			return false, "OW Bridge /health 不可达"
		}
		return true, ""
	case channelFreellm:
		// 可达性看 /livez（进程活着），池就绪看 /readyz；两者都不进 sidecar 的
		// 分钟限流器。目录抓取不再作为可达判据：它既不能证明能调用（实测 259 条
		// 里 257 条 needsKey），每 30 秒一轮还会挤占推理面的额度预算。
		if !h.freellmLiveness(ctx) {
			return false, "FreeLLMAPI 进程不可达"
		}
		ready, reason, upstreams := h.freellmPool(ctx)
		if ready {
			return true, fmt.Sprintf("ready_upstreams=%d", upstreams)
		}
		if reason != "" {
			return true, fmt.Sprintf("池内暂无可用上游（%s）", reason)
		}
		return true, "池内暂无可用上游"
	case channelAutoClaw:
		state := h.autoclawHealth(ctx)
		if state == nil {
			return false, "bridge /healthz 不可达"
		}
		// /healthz 在无健康账号时返回 503，那仍是「可达」信号，只附带计数。
		healthy, _ := state["healthy"].(float64)
		total, _ := state["total"].(float64)
		return true, fmt.Sprintf("healthy=%d total=%d", int(healthy), int(total))
	case channelTrae, channelTraeAI:
		if !h.traeHealth(ctx, h.traeChannelByChannel(c)) {
			return false, "sidecar /healthz 未返回 ok"
		}
		return true, ""
	case channelRaccoon:
		if !h.raccoonHealth(ctx) {
			return false, "sidecar /healthz 未返回 ok"
		}
		return true, ""
	}
	return false, "未知通道"
}

// adminHealth 返回「网关 + 七通道」总览：请求时即时跑一轮，保证页面看到的是
// 当前状态而不是上一轮缓存；history 是后台定时探活留存的趋势（含重启后从落盘
// 历史回放的部分）。只读，不改配置。
func (h *server) adminHealth(w http.ResponseWriter, r *http.Request) {
	// 后台探活关闭时（测试或未配探活）也允许这里触发一次性回放：只要模块总开关
	// 打开且有可写目录，重启后第一次查询就能看到落盘历史。healthInit 内部按
	// HealthProbe 与目录自行门控，未开启时是空操作。
	h.health.healthInit(h.cfg)
	round := h.runHealthRound(r.Context())
	writeJSON(w, 200, map[string]any{
		"ok":         round.OK,
		"checked_at": round.CheckedAt,
		"channels":   round.Channels,
		"history":    h.health.snapshot(),
	})
}
