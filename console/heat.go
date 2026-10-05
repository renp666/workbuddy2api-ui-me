package main

import (
	"encoding/json"
	"net/http"
)

// 本文件实现「全球热度」列的数据面：名次来自 console 代理的 OpenRouter 公开模型目录
// （GET https://openrouter.ai/api/v1/models?sort=most-popular，无需密钥），把返回顺序
// 当作全球热度名次，只向页面下发按名次排列的模型标识。
// 抓取与缓存不在这里，与「模型能力 TOP」共用 capability.go 的同一份快照，
// 两列不可能取到不同批次的数据。
// 口径（用户已裁定）：单元格显示原始名次 #N；匹配不到的模型如实留「—」，
// 不得伪造零或猜测名次。对齐键的归一化规则放在前端，与本地热度共用同一个
// modelKey，服务端不做二次加工，避免 Go/JS 两份口径漂移。

// heatEntry 是一条 OpenRouter 目录项里对齐所需的最小字段：id 用于直接匹配，
// canonical_slug 去掉日期后缀与 :variant 后覆盖同一基座模型的所有变体。
type heatEntry struct {
	ID            string `json:"id"`
	CanonicalSlug string `json:"canonical_slug"`
}

// heatRanksFromList 把 OpenRouter 目录条目按返回顺序整理成名次表。sort=most-popular
// 的排序在响应体里、条目本身没有数值字段，因此「顺序即名次」是本功能唯一口径；
// 缺 id 的条目跳过（无法对齐），不补占位、不改序。与 capabilityRowsFromList 同构，
// 便于离线回归解析契约而不发真实网络请求。
func heatRanksFromList(list []json.RawMessage) []heatEntry {
	ranks := make([]heatEntry, 0, len(list))
	for _, item := range list {
		var entry heatEntry
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		ranks = append(ranks, entry)
	}
	return ranks
}

// adminHeatRanks 把名次表转成页面可直接消费的数组：保持名次顺序，
// 每项带 id 与 canonical_slug 两个对齐键。从未取到过数据时如实返回空列表，
// 页面据此把所有「全球热度」单元格留「—」。
func (h *server) adminHeatRanks(w http.ResponseWriter, r *http.Request) {
	list, _, haveData := h.catalog.raw(h, r.Context())
	if !haveData {
		writeJSON(w, 200, map[string]any{"available": false, "ranks": []heatEntry{}})
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "ranks": heatRanksFromList(list)})
}
