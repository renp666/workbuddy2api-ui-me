// 每日限额护栏回归（补丁 0017）：达额摘除不喂熔断、签到不误放、兜底/保留积分
// 双护栏排除、RealmExhausted 四态与 0 点窗口。
package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestDailyQuotaObserveTriggersQuotaCooldown 达额即 CoolQuota 冷却到次日 0 点。
func TestDailyQuotaObserveTriggersQuotaCooldown(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	q := NewDailyQuota(p, 1000)
	q.ObserveTokens("u1", 600, 399) // 999：未达
	if _, ok := p.byUID["u1"]; !ok {
		t.Fatal("u1 not in pool")
	}
	if e := p.byUID["u1"]; e.coolKind == CoolQuota {
		t.Fatalf("999/1000 不应触发护栏")
	}
	q.ObserveTokens("u1", 2, 0) // 1001：达额
	e := p.byUID["u1"]
	if e.coolKind != CoolQuota || e.until.IsZero() || !time.Now().Before(e.until) {
		t.Fatalf("达额后应处于有效 CoolQuota 冷却，got kind=%v until=%v", e.coolKind, e.until)
	}
	if want := NextLocalMidnight(time.Now()); !e.until.Equal(want) {
		t.Fatalf("until = %v, want %v（本地次日 0 点）", e.until, want)
	}
	// 达额号退出 normal 轮换
	if a := p.PickExcludingForRealm(nil, "", ""); a != nil && a.UID == "u1" {
		t.Fatal("达额号不应再被 normal 选中（唯一账号场景应走兜底 nil 或不返回该号）")
	}
}

// TestDailyQuotaUnknownTokensNotCounted 负值哨兵（未知 usage）不计、全负不触发。
func TestDailyQuotaUnknownTokensNotCounted(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	q := NewDailyQuota(p, 10)
	q.ObserveTokens("u1", -1, -1) // 全未知
	q.ObserveTokens("u1", 0, 0)   // 全零
	if e := p.byUID["u1"]; e.coolKind == CoolQuota {
		t.Fatal("未知观测不应触发护栏")
	}
}

// TestDailyQuotaDayRollover 本地 0 点翻转清零计数窗口。
func TestDailyQuotaDayRollover(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	q := NewDailyQuota(p, 1000)
	q.ObserveTokens("u1", 900, 90)
	q.mu.Lock()
	q.day = "20000101" // 强制窗口过期
	q.mu.Unlock()
	q.ObserveTokens("u1", 5, 5)
	if e := p.byUID["u1"]; e.coolKind == CoolQuota {
		t.Fatal("跨天后昨日用量不应累计触发护栏")
	}
}

// TestCooldownQuotaUntilNoBreakerFeed 护栏摘除不喂熔断：成功/失败计数与
// softStreak 保持原值（区别于 Cooldown 入口）。
func TestCooldownQuotaUntilNoBreakerFeed(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.NoteSuccess("u1")
	before := p.byUID["u1"]
	succ, errTotal, streak := before.successCount, before.errTotal, before.softStreak
	p.CooldownQuotaUntil("u1", NextLocalMidnight(time.Now()), "test")
	e := p.byUID["u1"]
	if e.successCount != succ || e.errTotal != errTotal || e.softStreak != streak {
		t.Fatalf("护栏摘除喂入了熔断计数：success %d→%d err %d→%d streak %d→%d",
			succ, e.successCount, errTotal, e.errTotal, streak, e.softStreak)
	}
}

// TestCooldownQuotaUntilIdempotent 同 until 重复触发幂等（不重复置脏）。
func TestCooldownQuotaUntilIdempotent(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	until := NextLocalMidnight(time.Now())
	p.CooldownQuotaUntil("u1", until, "first")
	p.dirty.Store(false)
	p.CooldownQuotaUntil("u1", until, "first")
	if p.dirty.Load() {
		t.Fatal("同 until 重复触发不应置脏")
	}
}

// TestReviveKeepsQuotaCooldown 签到解冻不误放有效 CoolQuota；过期后可正常解封。
func TestReviveKeepsQuotaCooldown(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.CooldownQuotaUntil("u1", NextLocalMidnight(time.Now()), "daily limit")
	p.ReenableIfCredits("u1", 500)
	if e := p.byUID["u1"]; e.coolKind != CoolQuota || e.until.IsZero() {
		t.Fatal("签到到账不应解封有效 CoolQuota")
	}
	// 过期的 CoolQuota（0 点已过）由 revive 正常清掉（保护 revive 对旧脏状态的兜底能力）
	p.mu.Lock()
	p.byUID["u1"].until = time.Now().Add(-time.Minute)
	p.mu.Unlock()
	p.ReenableIfCredits("u1", 500)
	if e := p.byUID["u1"]; e.coolKind == CoolQuota {
		t.Fatal("过期 CoolQuota 应被 revive 清除")
	}
}

// TestQuotaAndReserveExcludedFromFallback 护栏号与低于保留线的号都不参与全冷却兜底。
func TestQuotaAndReserveExcludedFromFallback(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "quota", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.Add(&auth.Auth{UID: "reserve", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.SetReserveCredits(100)
	p.CooldownQuotaUntil("quota", NextLocalMidnight(time.Now()), "daily limit")
	p.Cooldown("reserve", CoolSoft, time.Hour, "429") // 逼进兜底域（软冷却可参与兜底）
	p.SetCredits("reserve", 50)                       // 低于保留线
	if a := p.PickExcludingForRealm(nil, "", ""); a != nil {
		t.Fatalf("护栏号与保留线以下号都不应参与兜底，got %s", a.UID)
	}
}

// TestReserveCreditsExcludesPickAndDirect 保留积分线覆盖 normal 与直取/pin 路径；
// credits 恢复后实时回池。
func TestReserveCreditsExcludesPickAndDirect(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.Add(&auth.Auth{UID: "u1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.SetReserveCredits(100)
	p.SetCredits("u1", 99)
	if a := p.Pick(""); a != nil {
		t.Fatalf("低于保留线的号不应参与 normal 轮换，got %s", a.UID)
	}
	if a := p.PickByUID("u1"); a != nil {
		t.Fatal("低于保留线的号不应被直取（粘性/pin 同口径）")
	}
	if a := p.PickByUIDForModel("u1", "hy4-preview"); a != nil {
		t.Fatal("低于保留线的号不应被带模型直取")
	}
	p.SetCredits("u1", 200)
	if a := p.Pick(""); a == nil || a.UID != "u1" {
		t.Fatalf("credits 恢复后应回池，got %v", a)
	}
}

// TestRealmExhausted 全达额 true；存在软冷却/可用/全禁用号 false；关闭恒 false。
func TestRealmExhausted(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	p := New("")
	p.Add(&auth.Auth{UID: "c1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.Add(&auth.Auth{UID: "c2", Domain: "www.codebuddy.cn", AccessToken: "at"})
	q := NewDailyQuota(p, 100)
	if q.RealmExhausted("cn") {
		t.Fatal("无达额号时不应报告枯竭")
	}
	p.CooldownQuotaUntil("c1", NextLocalMidnight(time.Now()), "daily limit")
	if q.RealmExhausted("cn") {
		t.Fatal("仍有健康号时不应报告枯竭")
	}
	p.CooldownQuotaUntil("c2", NextLocalMidnight(time.Now()), "daily limit")
	if !q.RealmExhausted("cn") {
		t.Fatal("全部账号达额应报告枯竭")
	}
	// 软冷却号不属于护栏对象：加入后不再「全部达额」，应回 503 语义
	p.Add(&auth.Auth{UID: "c3", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p.Cooldown("c3", CoolSoft, time.Hour, "429")
	if q.RealmExhausted("cn") {
		t.Fatal("存在软冷却号时应走 503 语义，不应报告护栏枯竭")
	}
	// 禁用号不是护栏对象
	p2 := New("")
	p2.Add(&auth.Auth{UID: "d1", Domain: "www.codebuddy.cn", AccessToken: "at"})
	p2.Disable("d1", "session dead")
	q2 := NewDailyQuota(p2, 100)
	if q2.RealmExhausted("cn") {
		t.Fatal("全禁用域不应报告护栏枯竭")
	}
	// 域隔离：cn 枯竭不代表 global 枯竭
	if q.RealmExhausted("global") {
		t.Fatal("global 域无账号时不应报告枯竭")
	}
	// 护栏关闭恒 false
	q3 := NewDailyQuota(p, 0)
	if q3.RealmExhausted("cn") {
		t.Fatal("limit=0 护栏关闭，不应报告枯竭")
	}
}

// TestNextLocalMidnight 0 点窗口边界。
func TestNextLocalMidnight(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 10, 20, 15, 30, 0, 0, loc)
	if got := NextLocalMidnight(now); !got.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, loc)) {
		t.Fatalf("NextLocalMidnight = %v, want 2026-10-21 00:00 local", got)
	}
	midnight := time.Date(2026, 10, 21, 0, 0, 0, 0, loc)
	if got := NextLocalMidnight(midnight); !got.Equal(time.Date(2026, 10, 22, 0, 0, 0, 0, loc)) {
		t.Fatalf("0 点整应返回次日 0 点，got %v", got)
	}
}
