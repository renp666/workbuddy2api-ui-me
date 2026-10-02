package main

import (
	"encoding/json"
	"testing"
)

// TestHeatRanksFromListPreservesOrder 锁定「顺序即名次」口径：most-popular 响应
// 没有数值排名字段，名次完全由条目在数组里的位置决定，解析不得重排或补占位。
func TestHeatRanksFromListPreservesOrder(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"tencent/hy4-preview","canonical_slug":"tencent/hy4-preview"}`),
		json.RawMessage(`{"id":"deepseek/deepseek-v4.1-flash","canonical_slug":"deepseek/deepseek-v4.1-flash-20260910"}`),
		json.RawMessage(`{"id":"z-ai/glm-5.3-flash"}`),
	}
	ranks := heatRanksFromList(list)
	if len(ranks) != 3 {
		t.Fatalf("want 3 ranks, got %d", len(ranks))
	}
	// 下标即名次：第一条最热，顺序必须原样保留。
	if ranks[0].ID != "tencent/hy4-preview" || ranks[1].ID != "deepseek/deepseek-v4.1-flash" || ranks[2].ID != "z-ai/glm-5.3-flash" {
		t.Fatalf("order not preserved: %+v", ranks)
	}
	if ranks[1].CanonicalSlug != "deepseek/deepseek-v4.1-flash-20260910" {
		t.Fatalf("canonical_slug not parsed: %+v", ranks[1])
	}
	if ranks[2].CanonicalSlug != "" {
		t.Fatalf("missing slug must stay empty, not guessed: %+v", ranks[2])
	}
}

// TestHeatRanksFromListSkipsUnusable 锁定「不伪造」：缺 id 或解析失败的条目跳过，
// 不产生空名次、不打乱后续条目的相对顺序。
func TestHeatRanksFromListSkipsUnusable(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"a/first"}`),
		json.RawMessage(`{"canonical_slug":"no-id-here"}`), // 无 id，无法对齐，丢弃
		json.RawMessage(`not json`),                        // 解析失败，丢弃
		json.RawMessage(`{"id":"b/fourth"}`),
	}
	ranks := heatRanksFromList(list)
	if len(ranks) != 2 {
		t.Fatalf("want 2 usable ranks, got %d: %+v", len(ranks), ranks)
	}
	if ranks[0].ID != "a/first" || ranks[1].ID != "b/fourth" {
		t.Fatalf("usable entries must keep relative order: %+v", ranks)
	}
}
