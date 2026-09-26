package usagelog

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func creditPtr(v float64) *float64 { return &v }

// writeDayFile 直接落一份受控 JSONL，绕开 Record 的「写入时刻」文件选择。
func writeDayFile(t *testing.T, dir string, day time.Time, lines ...string) {
	t.Helper()
	name := filepath.Join(dir, "usage-"+day.Format(dayLayout)+".jsonl")
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFiltersByRangeAndSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)
	withCredit, _ := json.Marshal(Entry{TS: today.Unix(), UID: "u1", Account: "甲", Model: "m", Mode: "stream", Prompt: 10, Completion: 20, Credit: creditPtr(1.5)})
	noCredit, _ := json.Marshal(Entry{TS: today.Unix() + 1, UID: "u2", Model: "m", Mode: "sync", Prompt: -1, Completion: -1})
	atEnd, _ := json.Marshal(Entry{TS: today.Unix() + 60, UID: "u9", Model: "m", Mode: "sync", Prompt: 0, Completion: 0})
	old, _ := json.Marshal(Entry{TS: yesterday.Unix(), UID: "u1", Model: "m", Mode: "sync", Prompt: 5, Completion: 5})
	writeDayFile(t, dir, today, string(withCredit), string(noCredit), "{corrupt", string(atEnd))
	writeDayFile(t, dir, yesterday, string(old))

	items, err := l.Query(today.Unix()-60, today.Unix()+60)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("期望当天 2 条（损坏行与 end 边界行排除），得到 %d：%v", len(items), items)
	}
	if items[0].UID != "u1" || items[0].Credit == nil || *items[0].Credit != 1.5 {
		t.Errorf("首条应为带 credit 的 u1：%v", items[0])
	}
	if items[1].Credit != nil || items[1].Prompt != -1 {
		t.Errorf("无 credit/usage 的观测不得伪造：%v", items[1])
	}
	// 覆盖昨天的区间能读到旧文件。
	items, _ = l.Query(yesterday.Unix()-60, today.Unix()+60)
	if len(items) != 3 {
		t.Fatalf("跨天查询期望 3 条，得到 %d", len(items))
	}
	if _, err := l.Query(100, 100); err == nil {
		t.Fatal("start>=end 应报错")
	}
}

func TestRecordAuthWiresNicknameFallbackAndExplicitZeroCredit(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	Set(nil)
	RecordAuth(&auth.Auth{UID: "u1", Nickname: "甲"}, "m", "stream", 1, 2, 3, true) // 未接线 no-op
	Set(l)
	RecordAuth(&auth.Auth{UID: "u1", Nickname: "甲"}, "cn:m", "stream", 100, 200, 1.5, true)
	RecordAuth(&auth.Auth{UID: "u2"}, "cn:m", "sync", 10, 20, 0, true)  // 显式 0 是合法免费观测
	RecordAuth(&auth.Auth{UID: "u3"}, "cn:m", "sync", -1, -1, 9, false) // 无 credit 观测照常落盘，但 credit 保持缺失
	RecordAuth(nil, "cn:m", "sync", 1, 1, 1, true)

	items, err := l.Query(time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("期望 3 条（nil 账号与未观测 credit 不落盘），得到 %d：%v", len(items), items)
	}
	if items[0].Account != "甲" || items[0].UID != "u1" {
		t.Errorf("昵称应优先：%v", items[0])
	}
	if items[1].Account != "u2" || items[1].Credit == nil || *items[1].Credit != 0 {
		t.Errorf("昵称缺省回落 UID，显式 0 credit 必须保留：%v", items[1])
	}
	if items[2].Credit != nil {
		t.Errorf("缺失 credit 不得写成 0：%v", items[2])
	}
}

func TestFromResponseDistinguishesMissingFromZero(t *testing.T) {
	full := map[string]any{"usage": map[string]any{"prompt_tokens": 100.0, "completion_tokens": 50.0, "credit": 2.5}}
	prompt, completion, credit, hasCredit := FromResponse(full)
	if prompt != 100 || completion != 50 || !hasCredit || credit != 2.5 {
		t.Fatalf("完整 usage 解析错误：%d %d %v %v", prompt, completion, credit, hasCredit)
	}
	free := map[string]any{"usage": map[string]any{"prompt_tokens": 1.0, "completion_tokens": 1.0, "credit": 0.0}}
	if _, _, credit, hasCredit = FromResponse(free); !hasCredit || credit != 0 {
		t.Fatalf("显式 credit:0 是合法免费观测：%v %v", credit, hasCredit)
	}
	noCredit := map[string]any{"usage": map[string]any{"prompt_tokens": 1.0, "completion_tokens": 1.0}}
	if prompt, completion, _, hasCredit = FromResponse(noCredit); hasCredit || prompt != 1 || completion != 1 {
		t.Fatalf("缺 credit 时 hasCredit=false，token 保留：%d %d %v", prompt, completion, hasCredit)
	}
	noUsage := map[string]any{"choices": []any{}}
	if prompt, completion, _, hasCredit = FromResponse(noUsage); hasCredit || prompt != -1 || completion != -1 {
		t.Fatalf("无 usage 返回 -1 哨兵：%d %d %v", prompt, completion, hasCredit)
	}
}

func TestSummarizeKeepsMissingApartFromZero(t *testing.T) {
	entries := []Entry{
		{Prompt: 10, Completion: 5, Credit: creditPtr(1.25)},
		{Prompt: -1, Completion: -1, Credit: creditPtr(0)}, // 免费 + usage 缺失
		{Prompt: 0, Completion: 0},                         // 无 credit 观测
	}
	s := Summarize(entries)
	if s.Calls != 3 || s.Prompt != 10 || s.Completion != 5 {
		t.Fatalf("已知值聚合错误：%+v", s)
	}
	if s.Credit != 1.25 || s.CreditMissing != 1 || s.UsageMissing != 1 {
		t.Fatalf("缺失计数不得并入零值：%+v", s)
	}
	if math.IsNaN(s.Credit) || math.IsInf(s.Credit, 0) {
		t.Fatalf("credit 聚合出现非法值：%v", s.Credit)
	}
}
