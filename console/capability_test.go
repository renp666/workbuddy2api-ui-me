package main

import (
	"encoding/json"
	"testing"
)

// TestCapabilityRowsFromListParsesRealShapes 锁定真实响应形态（2026-10-05 实测抓取自
// OpenRouter 公开目录）：AA 三指数与 design_arena 条目都是可空数值，必须原样落到指针上；
// 快照顺序即全球热度名次，下标从 1 起。
func TestCapabilityRowsFromListParsesRealShapes(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"deepseek/deepseek-v4.1-flash","canonical_slug":"deepseek/deepseek-v4.1-flash-20260910",` +
			`"name":"DeepSeek V4.1 Flash","context_length":163840,` +
			`"pricing":{"prompt":"0.000000003","completion":"0.0000024","input_cache_read":"0.000000003"},` +
			`"architecture":{"input_modalities":["text","image"]},` +
			`"supported_parameters":["tools","response_format","structured_outputs","reasoning"],` +
			`"benchmarks":{"artificial_analysis":{"intelligence_index":51.8,"coding_index":72,"agentic_index":null},` +
			`"design_arena":[{"arena":"models","category":"fullstack","elo":1287,"win_rate":61.3,"rank":3}]}}`),
	}
	rows := capabilityRowsFromList(list)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Heat != 1 {
		t.Errorf("heat=%d want 1（下标即名次，从 1 起）", row.Heat)
	}
	if row.ID != "deepseek/deepseek-v4.1-flash" || row.Slug != "deepseek/deepseek-v4.1-flash-20260910" {
		t.Errorf("alignment keys lost: %+v", row)
	}
	if row.Context != 163840 {
		t.Errorf("context=%d want 163840", row.Context)
	}
	if row.Free {
		t.Errorf("free=true want false（pricing 非零）")
	}
	if derefFloat(row.Intelligence) != 51.8 {
		t.Errorf("intelligence=%v want 51.8", row.Intelligence)
	}
	// 上游这里是 int 形态的 72，必须能落到 float 指针上，不因整数/浮点差异丢字段。
	if derefFloat(row.Coding) != 72 {
		t.Errorf("coding=%v want 72", row.Coding)
	}
	if row.Agentic != nil {
		t.Errorf("agentic=%v want nil（上游为 null，不得当成 0）", row.Agentic)
	}
	if len(row.Arena) != 1 {
		t.Fatalf("arena=%v want 1 条", row.Arena)
	}
	if row.Arena[0].Category != "fullstack" || derefFloat(row.Arena[0].Elo) != 1287 || row.Arena[0].Rank != 3 {
		t.Errorf("arena entry mangled: %+v", row.Arena[0])
	}
	if !row.Tools || !row.Structured || !row.Reasoning {
		t.Errorf("capability flags lost: tools=%v structured=%v reasoning=%v", row.Tools, row.Structured, row.Reasoning)
	}
	if row.VideoIn {
		t.Errorf("video_in=true want false（input_modalities 只有 text/image）")
	}
}

// TestCapabilityRowsFromListKeepsUnrankedRowsHonest 锁定「全模型榜单」的两条硬口径：
// 没有 benchmarks 的行必须照样出现在榜上（供人工判断），且所有能力字段留空而不是补零；
// 缺字段不等于零分，与 heat 的「不得伪造零」同一条线。
func TestCapabilityRowsFromListKeepsUnrankedRowsHonest(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"tiny/none","canonical_slug":"tiny/none","name":"Tiny None","context_length":8192,` +
			`"pricing":{"prompt":"0","completion":"0"},"architecture":{"input_modalities":["text"]},` +
			`"supported_parameters":["max_tokens"]}`),
		json.RawMessage(`{"id":"a/first","canonical_slug":"a/first","name":"A","context_length":1000,` +
			`"pricing":{"prompt":"0","completion":"0"},"architecture":{"input_modalities":["text"]},` +
			`"supported_parameters":[],"benchmarks":{"artificial_analysis":{"intelligence_index":3.8,"coding_index":null,"agentic_index":null},"design_arena":[]}}`),
	}
	rows := capabilityRowsFromList(list)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (全量，不因缺榜而丢行), got %d: %+v", len(rows), rows)
	}
	missing := rows[0]
	if missing.Intelligence != nil || missing.Coding != nil || missing.Agentic != nil || len(missing.Arena) != 0 {
		t.Fatalf("缺 benchmarks 的行被补了值: %+v", missing)
	}
	if missing.Heat != 1 || rows[1].Heat != 2 {
		t.Fatalf("heat order broken: %d,%d", missing.Heat, rows[1].Heat)
	}
	if !missing.Free {
		t.Errorf("pricing 全零应判 free: %+v", missing)
	}
	// 空 design_arena 数组与 AA 显式 null 同样是「没有数据」，不是零。
	if rows[1].Coding != nil || len(rows[1].Arena) != 0 {
		t.Fatalf("显式 null / 空数组被当成了数据: %+v", rows[1])
	}
}

// TestCapabilityFreeOnlyFromExplicitZeroPricing 锁定「免费档」的授予条件：只有上游明示
// 零价才叫 free。缺 pricing、空串、解析失败一律不是 free —— 否则一个未报价的模型会被
// 顶上「最省」位，而它可能实际很贵，这是这套档位能造成的最大伤害。
func TestCapabilityFreeOnlyFromExplicitZeroPricing(t *testing.T) {
	cases := []struct {
		name    string
		pricing string
		want    bool
	}{
		{"明示零价", `"pricing":{"prompt":"0","completion":"0"}`, true},
		{"明示零价带小数", `"pricing":{"prompt":"0.000","completion":"0.0"}`, true},
		{"非零价", `"pricing":{"prompt":"0.00000015","completion":"0.0000005"}`, false},
		{"缺字段", "", false},
		{"空串", `"pricing":{"prompt":"","completion":""}`, false},
		{"解析失败", `"pricing":{"prompt":"unknown","completion":"unknown"}`, false},
		{"只有一半是零", `"pricing":{"prompt":"0","completion":"0.0000005"}`, false},
	}
	for _, tc := range cases {
		fields := `"id":"x/y","canonical_slug":"x/y","name":"X","context_length":1000,"architecture":{"input_modalities":["text"]},"supported_parameters":[]`
		if tc.pricing != "" {
			fields += "," + tc.pricing
		}
		rows := capabilityRowsFromList([]json.RawMessage{json.RawMessage("{" + fields + "}")})
		if len(rows) != 1 {
			t.Fatalf("%s: want 1 row, got %+v", tc.name, rows)
		}
		if rows[0].Free != tc.want {
			t.Errorf("%s: free=%v want %v", tc.name, rows[0].Free, tc.want)
		}
	}
}

// TestCapabilityRowsFromListSkipsUnusable 与 heat 同一口径：缺 id（无法对齐）或整条
// 解析失败的条目跳过，不产生空行、不打乱后续条目的相对顺序；跳过后 heat 名次仍是
// 「在表中的位置」，不补占位名次。
func TestCapabilityRowsFromListSkipsUnusable(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"a/first","canonical_slug":"a/first","name":"A","context_length":1,"pricing":{"prompt":"0","completion":"0"},"architecture":{"input_modalities":["text"]},"supported_parameters":[]}`),
		json.RawMessage(`{"canonical_slug":"no-id"}`),
		json.RawMessage(`not json`),
		json.RawMessage(`{"id":"b/fourth","canonical_slug":"b/fourth","name":"B","context_length":1,"pricing":{"prompt":"1","completion":"1"},"architecture":{"input_modalities":["text"]},"supported_parameters":[]}`),
	}
	rows := capabilityRowsFromList(list)
	if len(rows) != 2 {
		t.Fatalf("want 2 usable rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].ID != "a/first" || rows[1].ID != "b/fourth" {
		t.Fatalf("usable rows must keep relative order: %+v", rows)
	}
	if rows[0].Heat != 1 || rows[1].Heat != 2 {
		t.Fatalf("heat 名次应为派生后的位置: %d,%d", rows[0].Heat, rows[1].Heat)
	}
}

// TestCapabilityRowsFromListOnLiveSnapshotShape 拿真实抓到的响应形态（含 18 个 ~ 前缀
// id 与 :free 变体）压一遍，确认整份 466 条里不会因为某个字段形态而批量掉行。
// 用内联样本而不是读盘，保证离线可跑。
func TestCapabilityRowsFromListOnLiveSnapshotShape(t *testing.T) {
	list := []json.RawMessage{
		json.RawMessage(`{"id":"~z-ai/glm-flash-latest","canonical_slug":"z-ai/glm-flash","name":"GLM Flash (latest)","context_length":128000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"input_modalities":["text","image","video"]},"supported_parameters":["tools"],"benchmarks":{"artificial_analysis":{"intelligence_index":41.1,"coding_index":null,"agentic_index":null},"design_arena":[]}}`),
		json.RawMessage(`{"id":"stealth/space-bunny-alpha:free","canonical_slug":"stealth/space-bunny-alpha","name":"Space Bunny Alpha (free)","context_length":200000,"pricing":{"prompt":"0","completion":"0"},"architecture":{"input_modalities":["text","image","audio","video"]},"supported_parameters":["max_tokens","temperature"],"benchmarks":{"design_arena":[{"arena":"agents","category":"agenticslides","elo":1200,"win_rate":50,"rank":15}]}}`),
	}
	rows := capabilityRowsFromList(list)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %+v", rows)
	}
	if !rows[0].VideoIn {
		t.Errorf("~ 前缀行的 video_in 丢了: %+v", rows[0])
	}
	if rows[0].Tools != true || rows[1].Tools != false {
		t.Errorf("tools flag wrong: %v %v", rows[0].Tools, rows[1].Tools)
	}
	if len(rows[1].Arena) != 1 || rows[1].Arena[0].Arena != "agents" {
		t.Errorf("agents arena lost: %+v", rows[1].Arena)
	}
	if derefFloat(rows[1].Arena[0].WinRate) != 50 {
		t.Errorf("win_rate int 形态应落到指针: %v", rows[1].Arena[0].WinRate)
	}
	if rows[0].Intelligence == nil || *rows[0].Intelligence != 41.1 {
		t.Errorf("intelligence lost on ~ 前缀行: %v", rows[0].Intelligence)
	}
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}
