package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestHardCooledUIDs 名单只收有效硬冷却：软冷却、禁用、已过期的硬冷却均不进。
func TestHardCooledUIDs(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u-hard", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u-hard-expired", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u-soft", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u-disabled", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u-normal", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u-hard", CoolHard, time.Hour, "余额不足")
	p.Cooldown("u-hard-expired", CoolHard, -time.Hour, "余额不足") // until 已过：兜底可见，无需刷新
	p.Cooldown("u-soft", CoolSoft, time.Hour, "429")
	p.Cooldown("u-disabled", CoolHard, time.Hour, "余额不足")
	p.Disable("u-disabled", "手动禁用")

	got := p.HardCooledUIDs()
	if len(got) != 1 || got[0] != "u-hard" {
		t.Errorf("HardCooledUIDs=%v want [u-hard]", got)
	}
}

// TestHardCooledUIDsEmpty 无硬冷却账号时返回空切片（供调用方短路跳过外呼）。
func TestHardCooledUIDsEmpty(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	if got := p.HardCooledUIDs(); len(got) != 0 {
		t.Errorf("HardCooledUIDs=%v want empty", got)
	}
}

// TestReviveHardCooledIfCredits 解冻判据：仅「硬冷却 + 非禁用 + remain>0」清冷却域；
// 软冷却/禁用账号只更新 credits 不动状态。
func TestReviveHardCooledIfCredits(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u2", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u3", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Add(&auth.Auth{UID: "u4", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Cooldown("u1", CoolHard, time.Hour, "余额不足")
	p.Cooldown("u2", CoolHard, time.Hour, "余额不足")
	p.Cooldown("u3", CoolSoft, time.Hour, "429")
	p.Cooldown("u4", CoolHard, time.Hour, "余额不足")
	p.Disable("u4", "手动禁用")

	// 硬冷却 + 余额恢复：解冻。
	p.ReviveHardCooledIfCredits("u1", 500)
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("u1 missing")
	}
	if st.Cooling || st.CoolKind != "" || st.Credits != 500 {
		t.Errorf("u1 after revive: cooling=%v kind=%q credits=%d, want 解冻且 credits=500", st.Cooling, st.CoolKind, st.Credits)
	}

	// 硬冷却 + 余额仍为 0：保持冷却。
	p.ReviveHardCooledIfCredits("u2", 0)
	st, _ = p.Status("u2")
	if !st.Cooling || st.CoolKind != "hard_credit" {
		t.Errorf("u2 remain=0: cooling=%v kind=%q, want 仍硬冷却", st.Cooling, st.CoolKind)
	}
	if st.Credits != 0 {
		t.Errorf("u2 credits=%d want 0", st.Credits)
	}

	// 软冷却 + 余额恢复：限流窗口未过，不得解冻（与签到解冻 ReenableIfCredits 的关键差异）。
	p.ReviveHardCooledIfCredits("u3", 500)
	st, _ = p.Status("u3")
	if !st.Cooling || st.CoolKind != "soft_rate" {
		t.Errorf("u3 soft cooling: cooling=%v kind=%q, want 仍软冷却", st.Cooling, st.CoolKind)
	}
	if st.Credits != 500 {
		t.Errorf("u3 credits=%d want 500（仅更新余额）", st.Credits)
	}

	// 禁用账号：不解冻（Disable 本身已清冷却域，这里断言禁用态保持、
	// 仅走 credits 更新分支，不会被复活回可用状态）。
	p.ReviveHardCooledIfCredits("u4", 500)
	st, _ = p.Status("u4")
	if !st.Disabled || st.Cooling {
		t.Errorf("u4 disabled: disabled=%v cooling=%v, want 禁用保持且不复活", st.Disabled, st.Cooling)
	}
}

// TestReviveHardCooledIfCreditsUnknownUID 不存在的 uid 为空操作（与池内其他方法同口径）。
func TestReviveHardCooledIfCreditsUnknownUID(t *testing.T) {
	p := New("")
	p.ReviveHardCooledIfCredits("ghost", 500) // 不 panic 即通过
}
