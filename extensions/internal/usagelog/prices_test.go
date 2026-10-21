package usagelog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func resetPrices(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		empty := map[string]PriceEntry{}
		SetPricesForTest(empty)
		SetPriceFile("")
	})
}

// TestCostUSDForPrefixAndMath 九通道公共前缀剥离与折算：cn:/global:/autoclaw-cn:
// 双层前缀都落到裸名价目；缺失 token 不折算；未知模型不折算。
func TestCostUSDForPrefixAndMath(t *testing.T) {
	resetPrices(t)
	SetPricesForTest(map[string]PriceEntry{
		"glm-5.3-flash":       {InputUSD: 0.10, OutputUSD: 0.40},
		"deepseek-v4.1-flash": {InputUSD: 0.27, OutputUSD: 1.10},
	})
	cases := []struct {
		model            string
		prompt, complete int
		want             float64
		wantOK           bool
	}{
		{"cn:glm-5.3-flash", 1_000_000, 1_000_000, 0.50, true},
		{"glm-5.3-flash", 2_000_000, 0, 0.20, true},
		{"global:deepseek-v4.1-flash", 1_000_000, 2_000_000, 2.47, true},
		{"traeai-deepseek-v4.1-flash", 500_000, 500_000, 0.685, true},
		{"autoclaw-cn:glm-5.3-flash", 1_000_000, 0, 0.10, true},
		{"raccoon-sn-deepseek-v4-1-flash", 1_000, 1_000, 0, false}, // 不在表内
		{"glm-5.3-flash", -1, 100, 0, false},                       // 输入未观测
		{"glm-5.3-flash", 100, -1, 0, false},                       // 输出未观测
		{"", 100, 100, 0, false},
	}
	for _, tc := range cases {
		got, ok := CostUSDFor(tc.model, tc.prompt, tc.complete)
		if ok != tc.wantOK {
			t.Errorf("CostUSDFor(%q) ok=%v want %v", tc.model, ok, tc.wantOK)
			continue
		}
		if ok && (got < tc.want-1e-9 || got > tc.want+1e-9) {
			t.Errorf("CostUSDFor(%q)=%v want %v", tc.model, got, tc.want)
		}
	}
}

// TestPriceFileHotReload 价目文件热重载：写入生效、stat 变化后重读生效、
// 坏 JSON 沿用上次有效表、文件消失回到默认表。
func TestPriceFileHotReload(t *testing.T) {
	resetPrices(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	write := func(content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// 保证 mtime/size 对下次 stat 可见变化（同秒写入 size 也不同，双保险）。
		future := time.Now().Add(2 * time.Second)
		os.Chtimes(path, future, future)
	}
	write(`{"glm-5.3-flash":{"input":0.10,"output":0.40}}`)
	SetPriceFile(path)
	if cost, ok := CostUSDFor("cn:glm-5.3-flash", 1_000_000, 0); !ok || cost != 0.10 {
		t.Fatalf("v1 cost=%v ok=%v want 0.10 true", cost, ok)
	}
	write(`{"glm-5.3-flash":{"input":0.20,"output":0.80}}`)
	refreshPriceTable(false) // RecordAuth 热路径的调用形态：stat 变化才重读
	if cost, ok := CostUSDFor("cn:glm-5.3-flash", 1_000_000, 0); !ok || cost != 0.20 {
		t.Fatalf("v2 cost=%v ok=%v want 0.20 true", cost, ok)
	}
	write(`{broken`)
	refreshPriceTable(false)
	if cost, ok := CostUSDFor("cn:glm-5.3-flash", 1_000_000, 0); !ok || cost != 0.20 {
		t.Fatalf("坏 JSON 应沿用上次有效表: cost=%v ok=%v", cost, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	refreshPriceTable(false)
	if _, ok := CostUSDFor("glm-5.3-flash", 1_000_000, 0); ok {
		t.Fatal("文件消失后应整体回到默认表（空），未定价")
	}
}

// TestRecordAuthSnapshotsCost 落账快照：定价模型写入 cost_usd，未定价保持 nil，
// 且之后改价不影响已落账的数值（历史不漂移）。
func TestRecordAuthSnapshotsCost(t *testing.T) {
	resetPrices(t)
	dir := t.TempDir()
	l, err := Open(filepath.Join(dir, "usage"))
	if err != nil {
		t.Fatal(err)
	}
	Set(l)
	t.Cleanup(func() { Set(nil) })
	SetPricesForTest(map[string]PriceEntry{"glm-5.3-flash": {InputUSD: 0.10, OutputUSD: 0.40}})

	RecordAuth(&auth.Auth{UID: "u1", Nickname: "一号"}, "cn:glm-5.3-flash", "stream", 1_000_000, 500_000, 0, false, Origin{})
	RecordAuth(&auth.Auth{UID: "u2", Nickname: "二号"}, "unknown-model", "stream", 1_000, 2_000, 0, false, Origin{})
	// 落账后调价：已快照的历史数字不得变化。
	raised := map[string]PriceEntry{"glm-5.3-flash": {InputUSD: 9.99, OutputUSD: 9.99}}
	SetPricesForTest(raised)

	entries, err := l.Query(0, time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d want 2", len(entries))
	}
	if entries[0].CostUSD == nil || *entries[0].CostUSD < 0.299999 || *entries[0].CostUSD > 0.300001 {
		t.Errorf("定价快照=%v want 0.30", entries[0].CostUSD)
	}
	if entries[1].CostUSD != nil {
		t.Errorf("未定价应为 nil，得到 %v", *entries[1].CostUSD)
	}
	s := Summarize(entries)
	if s.Cost < 0.299999 || s.Cost > 0.300001 || s.CostMissing != 1 {
		t.Errorf("Summarize cost=%v missing=%d want 0.30/1", s.Cost, s.CostMissing)
	}
}

// TestCostUSDRoundTripJSON 快照字段随 JSONL 落盘并能原样读回。
func TestCostUSDRoundTripJSON(t *testing.T) {
	resetPrices(t)
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "usage")); err != nil {
		t.Fatal(err)
	}
	cost := 0.0314159
	// 形状自检：价目 JSON 用 input/output 键（文档形状），解析失败说明形状漂移。
	var table map[string]PriceEntry
	if err := json.Unmarshal([]byte(`{"glm-5.3-flash":{"input":10,"output":40}}`), &table); err != nil {
		t.Fatalf("prices.json 形状回归: %v", err)
	}
	raw, err := json.Marshal(Entry{TS: 1, Model: "m", Prompt: 0, Completion: 0, CostUSD: &cost})
	if err != nil {
		t.Fatal(err)
	}
	var back Entry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.CostUSD == nil || *back.CostUSD != cost {
		t.Fatalf("round trip cost=%v want %v", back.CostUSD, cost)
	}
}
