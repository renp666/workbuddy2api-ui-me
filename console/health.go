package main

import (
	"context"
	"fmt"
	"net/http"
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

// healthHistorySize 是留存的历史轮数（内存态，重启清空）。
const healthHistorySize = 20

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
			states[c.Channel] = c.Reachable
		}
	}
	return healthHistoryEntry{CheckedAt: r.CheckedAt, OK: r.OK, Channels: states}
}

// healthCache 保留最近 healthHistorySize 轮后台探活结论（内存态，重启清空）。
type healthCache struct {
	mu       sync.Mutex
	history  []healthHistoryEntry
	lastSeed time.Time
}

func (c *healthCache) record(round healthRound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, round.historyEntry())
	if len(c.history) > healthHistorySize {
		c.history = c.history[len(c.history)-healthHistorySize:]
	}
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
	for _, result := range results {
		if !result.Enabled {
			continue
		}
		// 端口不可达、真调用结论为失败、或根本没有真调用结论，都不算健康。
		// 只认 Reachable 就是「假绿」；把「没测过」也算绿同样是把未知当健康。
		if !result.Reachable || result.ProbeState != "ok" {
			round.OK = false
		}
	}
	return round
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
// 当前状态而不是上一轮缓存；history 是后台定时探活留存的趋势。只读，不改配置。
func (h *server) adminHealth(w http.ResponseWriter, r *http.Request) {
	round := h.runHealthRound(r.Context())
	writeJSON(w, 200, map[string]any{
		"ok":         round.OK,
		"checked_at": round.CheckedAt,
		"channels":   round.Channels,
		"history":    h.health.snapshot(),
	})
}
