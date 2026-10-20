package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// 连败降权（灰度移植 issue #114）：喂入口是「不罚号的失败」（无权威分类的未知 4xx
// 与传输层失败）。此前这些失败只换号——坏号显示健康、每次被选中都失败。

// resetDegradeCfg 测试后恢复默认参数（SetDegrade 改的是包级配置，测试隔离）。
func resetDegradeCfg(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetDegrade(defaultDegradeThreshold, defaultDegradeCooldown, defaultDegradeCooldownMax)
	})
}

func newDegradePool(t *testing.T, uid string) *Pool {
	t.Helper()
	p := New("")
	p.Add(&auth.Auth{UID: uid})
	p.SetCredits(uid, 1000)
	return p
}

func TestNoteFailuresDegradesAtThreshold(t *testing.T) {
	resetDegradeCfg(t)
	SetDegrade(3, time.Minute, time.Hour)
	p := newDegradePool(t, "u1")

	// 前两次：仍在池
	p.NoteFailures("u1")
	p.NoteFailures("u1")
	if _, ok := p.Pick(); !ok {
		t.Fatal("未达阈值不应降权")
	}
	// 第三次达阈：临时出池
	p.NoteFailures("u1")
	if _, ok := p.Pick(); ok {
		t.Fatal("达阈后应临时出池（坏号显示健康、每次被选中都失败的修复）")
	}
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("账号应存在")
	}
	if !st.Cooling || st.DegradeUntil.IsZero() {
		t.Fatalf("降权状态应可见：cooling=%v degradeUntil=%v", st.Cooling, st.DegradeUntil)
	}
	if st.ConsecutiveFails != 0 {
		t.Errorf("达阈后计数应清零供下一轮重新累计：got %d", st.ConsecutiveFails)
	}
	if st.CoolKind != "" {
		t.Errorf("降权不是 CoolSoft/Hard 域，不应写 CoolKind：got %q", st.CoolKind)
	}
}

func TestNoteSuccessClearsDegrade(t *testing.T) {
	resetDegradeCfg(t)
	SetDegrade(2, time.Minute, time.Hour)
	p := newDegradePool(t, "u1")

	p.NoteFailures("u1")
	p.NoteFailures("u1")
	if _, ok := p.Pick(); ok {
		t.Fatal("前置：已达阈降权")
	}
	// 账号被证明恢复（成功调用）→ 立即回池，不等降权到期。
	p.NoteSuccess("u1")
	if _, ok := p.Pick(); !ok {
		t.Fatal("NoteSuccess 后应立即回池")
	}
	st, _ := p.Status("u1")
	if st.Cooling || !st.DegradeUntil.IsZero() {
		t.Fatalf("NoteSuccess 应清降权状态：cooling=%v degradeUntil=%v", st.Cooling, st.DegradeUntil)
	}
}

func TestDegradeWithinCooldownDoesNotExtend(t *testing.T) {
	resetDegradeCfg(t)
	SetDegrade(1, time.Hour, time.Hour) // 阈值 1、降权 1h：好观察「不延长」
	p := newDegradePool(t, "u1")

	p.NoteFailures("u1")
	first, _ := p.Status("u1")
	if first.DegradeUntil.IsZero() {
		t.Fatal("前置：首次达阈应降权")
	}
	// 降权期内继续失败达阈：不延长、不翻倍（防用户重试把降权越堆越厚）。
	p.NoteFailures("u1")
	p.NoteFailures("u1")
	second, _ := p.Status("u1")
	if !second.DegradeUntil.Equal(first.DegradeUntil) {
		t.Fatalf("降权期内再达阈不应延长截止：first=%v second=%v", first.DegradeUntil, second.DegradeUntil)
	}
}

func TestDegradeUnknownUIDIsNoop(t *testing.T) {
	resetDegradeCfg(t)
	SetDegrade(1, time.Minute, time.Hour)
	p := newDegradePool(t, "u1")
	p.NoteFailures("ghost") // 不存在：不得 panic、不得影响他号
	if _, ok := p.Pick(); !ok {
		t.Fatal("未知 UID 的失败不应影响真实账号")
	}
}

func TestDegradeDurationClamp(t *testing.T) {
	resetDegradeCfg(t)
	// 上限 < 基数：钳到基数；基数 > 上限：钳到上限。
	SetDegrade(1, time.Minute, time.Minute)
	if got := degradeDuration(); got != time.Minute {
		t.Fatalf("基数被钳到上限：got %v", got)
	}
	SetDegrade(1, 3*time.Hour, time.Hour)
	if got := degradeDuration(); got != time.Hour {
		t.Fatalf("超上限的基数应钳到上限：got %v", got)
	}
}

func TestEnvDegradeAppliesAndRecovers(t *testing.T) {
	resetDegradeCfg(t)
	t.Setenv("WB2A_DEGRADE_THRESHOLD", "7")
	t.Setenv("WB2A_DEGRADE_COOLDOWN", "90s")
	t.Setenv("WB2A_DEGRADE_COOLDOWN_MAX", "bogus") // 非法值保持默认，不 panic
	EnvDegrade()
	if degradeCfg.threshold != 7 || degradeCfg.cooldown != 90*time.Second {
		t.Fatalf("合法 env 未生效：threshold=%d cooldown=%v", degradeCfg.threshold, degradeCfg.cooldown)
	}
	if degradeCfg.max != defaultDegradeCooldownMax {
		t.Errorf("非法 env 应保持默认 max：got %v", degradeCfg.max)
	}
}
