package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现「模型能力 TOP」的数据面：把 OpenRouter 公开目录的一份快照同时当作
// 两个视图的来源——全球热度名次（heat.go）和能力榜单（本文件），两者不可能打架。
//
// 数据口径（用户已裁定，不得偏离）：
//   - 只采外部现成榜单，console 不自己跑评测、不猜分；
//   - 源站不标注各指数的评测发布日期，因此「更新日期」只能是本快照的采集时间，
//     并在页面上如实说明这一点；
//   - 缺字段 = 没有数据，留空；不得补零、不得补占位名次。

// openRouterCatalogURL 是 OpenRouter 官方公开接口，固定常量、不开放配置：
// 可配置目标会把管理面板变成 SSRF 跳板。sort=most-popular 让数组顺序本身携带热度名次。
const openRouterCatalogURL = "https://openrouter.ai/api/v1/models?sort=most-popular"

// catalogCacheTTL 以「天」为尺度：目录与榜外名次是天级变化，分钟级刷新只白耗外呼。
const catalogCacheTTL = 24 * time.Hour

// catalogFetchTimeout 比内网通道宽得多：实测该公网源链路吞吐在 3 KB/s～155 KB/s 之间
// 摆动，快照约 1 MB，8 秒会在最差情况下只收到局部正文导致解析失败、整列静默变「—」。
const catalogFetchTimeout = 30 * time.Second

// capabilityProvenanceNote 随响应下发，页面原样展示：把来源、日期语义和缺数据口径
// 一次讲清，避免读者把快照采集时间误读成评测发布日期。
const capabilityProvenanceNote = "数据源：OpenRouter 公开模型目录（按 most-popular 返回，数组顺序即全球热度名次）。能力指数来自 Artificial Analysis 与 Design Arena，由该目录一并转发；源站不标注各项评测的发布日期，下方时间是本快照的采集时间（24 小时内复用）。缺字段一律留空，不补零、不猜名次。"

// catalogEntry 是目录条目里本功能用到的字段。可空数值一律用指针，null 与 0 必须能区分。
type catalogEntry struct {
	ID            string `json:"id"`
	CanonicalSlug string `json:"canonical_slug"`
	Name          string `json:"name"`
	Context       int64  `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	Architecture struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	SupportedParameters []string `json:"supported_parameters"`
	Benchmarks          *struct {
		ArtificialAnalysis *struct {
			Intelligence *float64 `json:"intelligence_index"`
			Coding       *float64 `json:"coding_index"`
			Agentic      *float64 `json:"agentic_index"`
		} `json:"artificial_analysis"`
		DesignArena []capabilityArena `json:"design_arena"`
	} `json:"benchmarks"`
}

// capabilityArena 是一条 Design Arena 对战胜率记录：arena 区分 models/agents，
// category 就是「设计」维度下的具体类目（website、gamedev、dataviz……）。
type capabilityArena struct {
	Arena    string   `json:"arena"`
	Category string   `json:"category"`
	Elo      *float64 `json:"elo"`
	WinRate  *float64 `json:"win_rate"`
	Rank     int      `json:"rank"`
}

// capabilityRow 是下发给页面的一行榜单。heat 是在派生表中的位置（从 1 起），
// 不是上游字段；对齐键 id/slug 交给前端的 modelKey 归一化，服务端不二次加工。
type capabilityRow struct {
	Heat         int               `json:"heat"`
	ID           string            `json:"id"`
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Context      int64             `json:"context"`
	Free         bool              `json:"free"`
	Intelligence *float64          `json:"intelligence"`
	Coding       *float64          `json:"coding"`
	Agentic      *float64          `json:"agentic"`
	Arena        []capabilityArena `json:"arena"`
	Tools        bool              `json:"tools"`
	Structured   bool              `json:"structured"`
	Reasoning    bool              `json:"reasoning"`
	VideoIn      bool              `json:"video_in"`
	ImageIn      bool              `json:"image_in"`
}

// capabilityRowsFromList 按快照顺序整理榜单：缺 id（无法与本地通道对齐）或整条
// 解析失败的条目跳过，不补占位名次。与 heatRanksFromList 同构，便于离线回归契约。
func capabilityRowsFromList(list []json.RawMessage) []capabilityRow {
	rows := make([]capabilityRow, 0, len(list))
	for _, item := range list {
		var entry catalogEntry
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		row := capabilityRow{
			Heat:       len(rows) + 1,
			ID:         entry.ID,
			Slug:       entry.CanonicalSlug,
			Name:       entry.Name,
			Context:    entry.Context,
			Free:       freePricing(entry.Pricing.Prompt, entry.Pricing.Completion),
			Tools:      hasToken(entry.SupportedParameters, "tools"),
			Structured: hasToken(entry.SupportedParameters, "structured_outputs"),
			Reasoning:  hasToken(entry.SupportedParameters, "reasoning"),
			VideoIn:    hasToken(entry.Architecture.InputModalities, "video"),
			ImageIn:    hasToken(entry.Architecture.InputModalities, "image"),
		}
		if entry.Benchmarks != nil {
			if aa := entry.Benchmarks.ArtificialAnalysis; aa != nil {
				row.Intelligence = aa.Intelligence
				row.Coding = aa.Coding
				row.Agentic = aa.Agentic
			}
			row.Arena = entry.Benchmarks.DesignArena
		}
		rows = append(rows, row)
	}
	return rows
}

// freePricing 只在 prompt 与 completion 都明示为零价时判 free。缺字段、空串、
// 非数值的报价都不算免费——否则一个未报价的模型会被顶上「最省」位，而它可能很贵。
func freePricing(prompt, completion string) bool {
	return zeroPrice(prompt) && zeroPrice(completion)
}

func zeroPrice(raw string) bool {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && value == 0
}

func hasToken(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// catalogCache 缓存整份目录快照（原始 JSON 条目），两个视图按请求各自派生。
// 缓存单位从「只存标识串」放大成「存整份正文」，上限仍是 fetchUpstream 的 4 MiB
// 正文闸，实测约 1 MB；换来的性质是热度列与能力榜不可能取到不同批次的数据。
type catalogCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	list      []json.RawMessage
	haveData  bool
}

// raw 返回快照条目与采集时间；过期时重新抓取，失败沿用上一份。
func (c *catalogCache) raw(h *server, ctx context.Context) ([]json.RawMessage, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < catalogCacheTTL {
		return c.list, c.fetchedAt, c.haveData
	}
	list, ok := h.fetchOpenRouterCatalog(ctx)
	c.fetchedAt = time.Now()
	if ok {
		c.list = list
		c.haveData = true
	}
	return c.list, c.fetchedAt, c.haveData
}

// fetchOpenRouterCatalog 抓取公开目录。任何非 200、超正文上限或解析失败都按
// 「拿不到」处理，不猜、不截断成半份榜。
func (h *server) fetchOpenRouterCatalog(ctx context.Context) ([]json.RawMessage, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", openRouterCatalogURL, nil)
	if err != nil {
		return nil, false
	}
	short, cancel := context.WithTimeout(req.Context(), catalogFetchTimeout)
	defer cancel()
	list, ok := parseModelList(h.fetchUpstream(req.WithContext(short)))
	if !ok || len(list) == 0 {
		return nil, false
	}
	return list, true
}

// adminCapabilities 下发整份能力榜单与快照时间。从未取到过数据时如实返回
// available:false，页面据此显示「拿不到」而不是一张空表假装成数据。
func (h *server) adminCapabilities(w http.ResponseWriter, r *http.Request) {
	list, fetchedAt, haveData := h.catalog.raw(h, r.Context())
	if !haveData {
		writeJSON(w, 200, map[string]any{"available": false, "rows": []capabilityRow{}})
		return
	}
	writeJSON(w, 200, map[string]any{
		"available":   true,
		"snapshot_at": fetchedAt.UTC().Format(time.RFC3339),
		"note":        capabilityProvenanceNote,
		"rows":        capabilityRowsFromList(list),
	})
}
