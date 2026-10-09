package pool

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
)

func mustAuth(t *testing.T, uid, token string, expiresAt int64) *auth.Auth {
	t.Helper()
	raw := `{"uid":"` + uid + `","access_token":"` + token + `"`
	if expiresAt > 0 {
		raw += `,"expires_at":` + strconv.FormatInt(expiresAt, 10)
	}
	raw += `}`
	a, err := auth.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("auth.Parse(%s): %v", uid, err)
	}
	return a
}

func TestPickPrefersHigherPoints(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "low", "t1", 0))
	p.Add(mustAuth(t, "high", "t2", 0))
	p.SetPoints("high", 500)
	a, ok := p.Pick()
	if !ok || a.UID() != "high" {
		t.Fatalf("Pick = %v/%v, want the account with more points", a, ok)
	}
}

func TestPickExcludingSkipsTried(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Add(mustAuth(t, "u2", "t2", 0))
	a, ok := p.PickExcluding(map[string]bool{"u1": true})
	if !ok || a.UID() != "u2" {
		t.Fatalf("PickExcluding = %v/%v, want u2", a, ok)
	}
	if _, ok := p.PickExcluding(map[string]bool{"u1": true, "u2": true}); ok {
		t.Fatal("PickExcluding should fail once every account was tried")
	}
}

func TestDisabledAccountIsNotPicked(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Disable("u1", "token_expired: 登录态失效")
	if _, ok := p.Pick(); ok {
		t.Fatal("disabled account must not be pickable")
	}
	if p.HealthyCount() != 0 {
		t.Fatalf("HealthyCount = %d, want 0", p.HealthyCount())
	}
	st, ok := p.Status("u1")
	if !ok || !st.Disabled || st.Reason == "" {
		t.Fatalf("status = %+v, want disabled with a reason", st)
	}
}

func TestSetEnabledBlocksAndRestoresPick(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	if !p.SetEnabled("u1", false, "disabled_by_user") {
		t.Fatal("SetEnabled(false) should hit the account")
	}
	if _, ok := p.Pick(); ok {
		t.Fatal("soft-disabled account must not be pickable")
	}
	if !p.SetEnabled("u1", true, "") {
		t.Fatal("SetEnabled(true) should hit the account")
	}
	if _, ok := p.Pick(); !ok {
		t.Fatal("re-enabled account must be pickable again")
	}
	if p.SetEnabled("nope", true, "") {
		t.Fatal("SetEnabled on unknown uid should report false")
	}
}

// 「启用」必须名副其实：硬禁用（登录态失效）的账号也要能靠显式启用恢复。
// 若凭证确实已失效，下一次请求会再次 Disable 并给出新原因，属自愈。
func TestSetEnabledRevivesDisabledAccount(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Disable("u1", "token_expired: 刷新后仍被拒，请重新导入凭证")
	if _, ok := p.Pick(); ok {
		t.Fatal("disabled account must not be pickable")
	}
	if !p.SetEnabled("u1", true, "") {
		t.Fatal("SetEnabled(true) should hit the account")
	}
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("account disappeared")
	}
	if st.Disabled || !st.Enabled || st.Reason != "" {
		t.Fatalf("explicit enable must clear the hard disable, got %+v", st)
	}
	if _, ok := p.Pick(); !ok {
		t.Fatal("revived account must be pickable")
	}
}

// Q3：重新导入新凭证必须让被硬禁用的账号复活，否则唯一的恢复手段失效。
func TestReimportRevivesDisabledAccount(t *testing.T) {
	p := New("")
	expires := time.Now().Add(24 * time.Hour).Unix()
	p.Add(mustAuth(t, "u1", "old-token", expires))
	p.Disable("u1", "token_expired: 刷新后仍被拒")
	p.Add(mustAuth(t, "u1", "new-token", expires))
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("account disappeared")
	}
	if st.Disabled || st.Reason != "" {
		t.Fatalf("reimport must revive the account, got %+v", st)
	}
	if _, ok := p.Pick(); !ok {
		t.Fatal("revived account must be pickable")
	}
}

// 同一个 token、但过期时间更早的导入不算重新登录：不得复活，也不得覆盖。
func TestOlderImportDoesNotRevive(t *testing.T) {
	p := New("")
	newer := time.Now().Add(48 * time.Hour).Unix()
	p.Add(mustAuth(t, "u1", "same-token", newer))
	p.Disable("u1", "token_expired: 刷新后仍被拒")
	p.Add(mustAuth(t, "u1", "same-token", time.Now().Add(time.Hour).Unix()))
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("account disappeared")
	}
	if !st.Disabled {
		t.Fatalf("older import must not revive the account, got %+v", st)
	}
}

func TestReimportAlsoClearsCooldown(t *testing.T) {
	p := New("")
	expires := time.Now().Add(24 * time.Hour).Unix()
	p.Add(mustAuth(t, "u1", "old-token", expires))
	p.Cooldown("u1", CoolQuota, time.Hour, "quota_limited")
	p.NoteError("u1", 3, time.Minute)
	p.Add(mustAuth(t, "u1", "new-token", expires))
	st, _ := p.Status("u1")
	if st.Cooling || st.ErrCount != 0 || st.Reason != "" {
		t.Fatalf("reimport must clear cooldown, got %+v", st)
	}
}

func TestNoteErrorCoolsAtThreshold(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.NoteError("u1", 3, time.Minute)
	p.NoteError("u1", 3, time.Minute)
	if _, ok := p.Pick(); !ok {
		t.Fatal("below threshold the account must stay pickable")
	}
	p.NoteError("u1", 3, time.Minute)
	if _, ok := p.Pick(); ok {
		t.Fatal("at threshold the account must be cooling")
	}
	st, _ := p.Status("u1")
	if !st.Cooling || st.Until == "" {
		t.Fatalf("status = %+v, want cooling with an until timestamp", st)
	}
	p.NoteSuccess("u1")
	if st, _ := p.Status("u1"); st.ErrCount != 0 {
		t.Fatalf("NoteSuccess must clear the error counter, got %+v", st)
	}
}

func TestCooldownThenReenableIfPoints(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Cooldown("u1", CoolQuota, time.Hour, "quota_limited")
	p.ReenableIfPoints("u1", 100)
	if st, _ := p.Status("u1"); st.Cooling {
		t.Fatalf("positive balance must clear the cooldown, got %+v", st)
	}

	p.Cooldown("u1", CoolQuota, time.Hour, "quota_limited")
	p.ReenableIfPoints("u1", 0)
	if st, _ := p.Status("u1"); !st.Cooling {
		t.Fatal("zero balance must not clear the cooldown")
	}
}

func TestSyncToDirDropsMissingAccounts(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Add(mustAuth(t, "u2", "t2", 0))
	p.SyncToDir([]*auth.Auth{mustAuth(t, "u1", "t1x", 0)})
	list := p.List()
	if len(list) != 1 || list[0].UID != "u1" {
		t.Fatalf("list = %+v, want only u1", list)
	}
	if a, ok := p.Pick(); !ok || a.JWT() != "t1x" {
		t.Fatal("SyncToDir must replace credentials of surviving accounts")
	}
}

func TestRemove(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u1", "t1", 0))
	if !p.Remove("u1") {
		t.Fatal("Remove should report a hit")
	}
	if p.Remove("u1") {
		t.Fatal("Remove on missing uid should report false")
	}
	if len(p.List()) != 0 {
		t.Fatal("account should be gone")
	}
}

func TestListIsSortedByUID(t *testing.T) {
	p := New("")
	p.Add(mustAuth(t, "u3", "t3", 0))
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.Add(mustAuth(t, "u2", "t2", 0))
	got := []string{}
	for _, st := range p.List() {
		got = append(got, st.UID)
	}
	if len(got) != 3 || got[0] != "u1" || got[1] != "u2" || got[2] != "u3" {
		t.Fatalf("List order = %v, want u1,u2,u3", got)
	}
}

// 启停、积分、冷却原因都要跨进程存活（容器重启后仍是同一状态）。
func TestStatePersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	p := New(statePath)
	p.Add(mustAuth(t, "u1", "t1", 0))
	p.SetPoints("u1", 42)
	p.SetEnabled("u1", false, "disabled_by_user")
	p.Add(mustAuth(t, "u2", "t2", 0))
	p.Disable("u2", "token_expired")

	q := New(statePath)
	q.SyncToDir([]*auth.Auth{mustAuth(t, "u1", "t1", 0), mustAuth(t, "u2", "t2", 0)})
	u1, ok := q.Status("u1")
	if !ok || u1.Enabled || u1.Points != 42 || u1.Reason == "" {
		t.Fatalf("u1 after reload = %+v, want disabled with points preserved", u1)
	}
	u2, ok := q.Status("u2")
	if !ok || !u2.Disabled {
		t.Fatalf("u2 after reload = %+v, want hard-disabled", u2)
	}
}

func TestCoolKindString(t *testing.T) {
	cases := map[CoolKind]string{
		CoolQuota:   "quota_limited",
		CoolSoft:    "soft_failure",
		CoolErr:     "error_threshold",
		CoolKind(9): "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}
