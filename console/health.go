package main

import (
	"context"
	"fmt"
	"net/http"
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
	// ProbeState 取自手工测速探测结论：空=未探测，ok=有可用模型，
	// failed=有结论但无一可用。仅作参考，不触发任何上游调用。
	ProbeState string `json:"probe_state,omitempty"`
	// ProbeOKModels / ProbeTotalModels 是真调用结论里的可用模型数与已测模型数。
	ProbeOKModels    int `json:"probe_ok_models,omitempty"`
	ProbeTotalModels int `json:"probe_total_models,omitempty"`
	// ProbeDetail 是首个失败模型的原因摘要，便于页面直接解释红灯。
	ProbeDetail string `json:"probe_detail,omitempty"`
}

// probeVerdict 从手工测速探测缓存里归纳某通道的真调用结论。
// 无结论时 state 为空串——「没测过」与「测了不行」必须区分，不得把未知当健康。
func (h *server) probeVerdict(c routeChannel) (state string, okModels, totalModels int, detail string) {
	results := h.probe.channelResults(c)
	if len(results) == 0 {
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
	mu      sync.Mutex
	history []healthHistoryEntry
}

func (c *healthCache) record(round healthRound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, round.historyEntry())
	if len(c.history) > healthHistorySize {
		c.history = c.history[len(c.history)-healthHistorySize:]
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	h.health.record(h.runHealthRound(ctx))
}

// runHealthRound 并行探活 routeChannelOrder 里的全部已启用通道，
// 任一已启用通道不可达即整轮 OK=false（core 恒启用，故 core 不可达必然失败）。
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
		// 端口不可达、或端口活着但真调用结论表明该通道无一跳可用，都算不健康。
		// 只认 Reachable 就是「假绿」：手工探测已测出模型全挂时总览仍报 ok=true。
		if !result.Reachable || result.ProbeState == "failed" {
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
	result.ProbeState, result.ProbeOKModels, result.ProbeTotalModels, result.ProbeDetail = h.probeVerdict(c)
	return result
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
	case channelAutoClaw:
		state := h.autoclawHealth(ctx)
		if state == nil {
			return false, "bridge /healthz 不可达"
		}
		// /healthz 在无健康账号时返回 503，那仍是「可达」信号，只附带计数。
		healthy, _ := state["healthy"].(float64)
		total, _ := state["total"].(float64)
		return true, fmt.Sprintf("healthy=%d total=%d", int(healthy), int(total))
	case channelTrae:
		if !h.traeHealth(ctx) {
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
