// 每日限额护栏的账本侧回归（补丁 0017）：observer 喂入口径与按日聚合重建。
package usagelog

import (
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestDailyTokenTotals 当日聚合按 uid 求和：负值哨兵不计、空 uid 跳过、跨日不算。
func TestDailyTokenTotals(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	l.Record(Entry{TS: now.Unix(), UID: "u1", Prompt: 100, Completion: 30})
	l.Record(Entry{TS: now.Unix(), UID: "u1", Prompt: -1, Completion: 20}) // 未知 prompt 不计
	l.Record(Entry{TS: now.Unix(), UID: "u2", Prompt: 7, Completion: -1})
	l.Record(Entry{TS: now.Unix(), UID: "", Prompt: 999}) // 空 uid 跳过
	l.Record(Entry{TS: now.AddDate(0, 0, -1).Unix(), UID: "u1", Prompt: 50000})

	day, totals, err := DailyTokenTotals(l, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Format(dayLayout); day != want {
		t.Fatalf("day = %q, want %q", day, want)
	}
	if totals["u1"] != 150 {
		t.Fatalf("u1 = %d, want 150（未知观测缺失不计）", totals["u1"])
	}
	if totals["u2"] != 7 {
		t.Fatalf("u2 = %d, want 7", totals["u2"])
	}
}

type countingObserver struct{ prompt, completion atomic.Int64 }

func (o *countingObserver) ObserveTokens(uid string, prompt, completion int) {
	o.prompt.Add(int64(prompt))
	o.completion.Add(int64(completion))
}

// TestRecordAuthFeedsTokenObserver RecordAuth 在落账后喂 observer；未接线 no-op。
func TestRecordAuthFeedsTokenObserver(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	Set(l)
	t.Cleanup(func() { Set(nil); SetTokenObserver(nil) })

	acct := &auth.Auth{UID: "u1", Nickname: "一号"}
	// 未接线 observer：no-op
	RecordAuth(acct, "m", "chat", 10, 5, 0, false, Origin{})
	// 接线后喂入
	ob := &countingObserver{}
	SetTokenObserver(ob)
	RecordAuth(acct, "m", "chat", 10, 5, 0, false, Origin{})
	RecordAuth(acct, "m", "chat", -1, 8, 0, false, Origin{}) // 未知哨兵原样传（观察者自行判负）
	if ob.prompt.Load() != 9 || ob.completion.Load() != 13 {
		t.Fatalf("observer 收到 prompt=%d completion=%d, want 9/13", ob.prompt.Load(), ob.completion.Load())
	}
}
