package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseFlatFields(t *testing.T) {
	a, err := Parse([]byte(`{"access_token":"a1","refresh_token":"r1","expires_at":1700000000}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != DesktopUID {
		t.Fatalf("uid = %q, want %q", a.UID(), DesktopUID)
	}
	if a.JWT() != "a1" || a.RefreshTokenValue() != "r1" {
		t.Fatalf("tokens = %q/%q", a.JWT(), a.RefreshTokenValue())
	}
	if a.ExpiresAtValue() != 1700000000 {
		t.Fatalf("expires_at = %d, want 1700000000", a.ExpiresAtValue())
	}
	if !a.CanRefresh() {
		t.Fatal("CanRefresh = false, want true")
	}
}

// 三种 access token 写法都要认：access_token ?? token ?? auth_token（含嵌套形）。
func TestParseTokenAliases(t *testing.T) {
	for _, raw := range []string{
		`{"token":"t"}`,
		`{"auth_token":"t"}`,
		`{"auth":{"access_token":"t"}}`,
	} {
		a, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(%s): %v", raw, err)
		}
		if a.JWT() != "t" {
			t.Fatalf("Parse(%s).JWT = %q, want t", raw, a.JWT())
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "   ", "{}", `{"access_token":""}`, "not json"} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("Parse(%q) should fail", raw)
		}
	}
}

func TestParseUIDAndNickname(t *testing.T) {
	a, err := Parse([]byte(`{"uid":"u-1","nickname":"阿浣熊","access_token":"t"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != "u-1" || a.Nickname() != "阿浣熊" {
		t.Fatalf("uid/nickname = %q/%q", a.UID(), a.Nickname())
	}
}

func TestNeedsRefresh(t *testing.T) {
	// 上游没声明 expires_at 时不预刷新（对齐上游 is_expiring 口径），交给 401 分支。
	noExp, err := Parse([]byte(`{"access_token":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if noExp.NeedsRefresh(24 * time.Hour) {
		t.Fatal("missing expires_at must not need proactive refresh")
	}

	soon, err := Parse([]byte(`{"access_token":"a","expires_at":` + strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if !soon.NeedsRefresh(5 * time.Minute) {
		t.Fatal("expiring within skew must need refresh")
	}

	far, err := Parse([]byte(`{"access_token":"a","expires_at":` + strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if far.NeedsRefresh(24 * time.Hour) {
		t.Fatal("expiring beyond skew must not need refresh")
	}
}

func TestSaveAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a, err := Parse([]byte(`{"access_token":"a1","refresh_token":"r1"}`))
	if err != nil {
		t.Fatal(err)
	}
	path := FilePathFor(dir, a.UID())
	a.SetFilePath(path)
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credential file missing: %v", err)
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d account(s), want 1", len(loaded))
	}
	if loaded[0].JWT() != "a1" || loaded[0].RefreshTokenValue() != "r1" {
		t.Fatalf("round-trip tokens = %q/%q", loaded[0].JWT(), loaded[0].RefreshTokenValue())
	}
	if loaded[0].UID() != DesktopUID {
		t.Fatalf("uid after reload = %q, want %q", loaded[0].UID(), DesktopUID)
	}
}

// SetTokens 传空 refresh token / 过期时间时保留旧值：刷新响应可能只带 access token。
func TestSetTokensKeepsMissingFields(t *testing.T) {
	a, err := Parse([]byte(`{"access_token":"a1","refresh_token":"r1","expires_at":4102444800}`))
	if err != nil {
		t.Fatal(err)
	}
	a.SetTokens("a2", "", 0)
	if a.JWT() != "a2" {
		t.Fatalf("access token = %q", a.JWT())
	}
	if a.RefreshTokenValue() != "r1" {
		t.Fatalf("refresh token must be kept, got %q", a.RefreshTokenValue())
	}
	if a.ExpiresAtValue() != 4102444800 {
		t.Fatalf("expires_at must be kept, got %d", a.ExpiresAtValue())
	}
}

func TestSaveAtomicWithoutFilePathFails(t *testing.T) {
	a, err := Parse([]byte(`{"access_token":"a1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveAtomic(); err == nil {
		t.Fatal("SaveAtomic without FilePath should fail")
	}
}

// 解析失败的坏文件不能阻塞启动，也不能把好账号挤掉。
func TestLoadDirSkipsUnparsableFiles(t *testing.T) {
	dir := t.TempDir()
	good := FilePathFor(dir, "good")
	if err := os.WriteFile(good, []byte(`{"access_token":"a1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raccoon-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 1 || loaded[0].UID() != "good" {
		t.Fatalf("loaded = %+v, want only the good account", loaded)
	}
}

func TestNormalizeExpiresAt(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
	}{
		{"秒", float64(1700000000), 1700000000},
		{"毫秒", float64(1700000000000), 1700000000},
		{"数字字符串", "1700000000", 1700000000},
		{"缺省", nil, 0},
		{"空串", "  ", 0},
	}
	for _, tc := range cases {
		if got := NormalizeExpiresAt(tc.in); got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestMaskToken(t *testing.T) {
	if got := MaskToken("abcdefgh", 4); got != "abcd…(8 chars)" {
		t.Fatalf("MaskToken = %q", got)
	}
	if got := MaskToken("ab", 4); got != "ab…" {
		t.Fatalf("MaskToken(short) = %q", got)
	}
}

// 桌面端 auth.json 不含 uid（只有 access_token/refresh_token/office_identity）。
// 空 uid 一律落到 DesktopUID 会让所有账号写进同一个 raccoon-desktop.json 互相覆盖，
// 多账号池因此永远只有一行。JWT 的 sub 是上游用户标识，同一账号恒定、不同账号必不同。
func TestParseDerivesUIDFromJWTSubject(t *testing.T) {
	tok := jwtWithSub(t, "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417")
	a, err := Parse([]byte(`{"access_token":"` + tok + `","refresh_token":"r1","office_identity":"personal"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417" {
		t.Fatalf("uid = %q, want the JWT sub", a.UID())
	}
	// 42 字符的 uid 直接当展示名会把账号表那一列撑开。截短取**尾部**：sub 是 UUIDv7，
	// 前缀是创建时间戳，同期账号前缀相同，按前缀截会让两个账号同名。
	if a.Nickname() != "3a5f1bd91417" {
		t.Fatalf("nickname = %q, want the tail segment 3a5f1bd91417", a.Nickname())
	}
}

// 显式 uid 仍然优先：导入方自己指定身份时不能被 JWT 覆盖。
func TestParseKeepsExplicitUID(t *testing.T) {
	tok := jwtWithSub(t, "pu-abc")
	a, err := Parse([]byte(`{"uid":"my-own","access_token":"` + tok + `"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != "my-own" {
		t.Fatalf("uid = %q, want explicit my-own", a.UID())
	}
}

// 非 JWT 的 token（旧数据、测试桩）必须退回 DesktopUID，不能把 uid 弄成空串。
func TestParseFallsBackToDesktopUIDForOpaqueToken(t *testing.T) {
	a, err := Parse([]byte(`{"access_token":"opaque-token"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != DesktopUID {
		t.Fatalf("uid = %q, want %q", a.UID(), DesktopUID)
	}
}

// 早期导入会把空 uid 规范化成 raccoon-desktop 再落盘，所以文件里显式写着这个哨兵值
// 同样等于「没声明身份」。不重新推导的话老文件永远停在旧身份上，迁移也就不会发生。
func TestParseRedrivesUIDFromSentinelDesktopUID(t *testing.T) {
	tok := jwtWithSub(t, "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417")
	a, err := Parse([]byte(`{"uid":"` + DesktopUID + `","access_token":"` + tok + `"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.UID() != "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417" {
		t.Fatalf("uid = %q, want the JWT sub (sentinel must not win)", a.UID())
	}
}

// 历史遗留的 raccoon-desktop.json 在 Parse 能取到 sub 之后，加载时必须迁到新文件名：
// 否则下次导入同一账号会另存一份，两条记录共用 refreshToken，轮换时互相踢下线。
func TestLoadDirMigratesLegacyDesktopFile(t *testing.T) {
	dir := t.TempDir()
	tok := jwtWithSub(t, "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417")
	legacy := FilePathFor(dir, DesktopUID)
	if err := os.WriteFile(legacy, []byte(`{"access_token":"`+tok+`","refresh_token":"r1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d accounts, want 1", len(loaded))
	}
	if loaded[0].UID() != "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417" {
		t.Fatalf("uid = %q, want the JWT sub", loaded[0].UID())
	}
	if _, err := os.Stat(FilePathFor(dir, "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417")); err != nil {
		t.Fatalf("legacy file was not renamed: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy raccoon-desktop.json still present, want it renamed away")
	}
}

// 两个不同账号的 auth.json 必须落到两个文件——这是「导入变新增」的最小复现。
func TestTwoAccountsWithDistinctSubjectsCoexist(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"pu-aaa", "pu-bbb"} {
		tok := jwtWithSub(t, sub)
		a, err := Parse([]byte(`{"access_token":"` + tok + `","refresh_token":"r-` + sub + `"}`))
		if err != nil {
			t.Fatalf("Parse(%s): %v", sub, err)
		}
		if a.UID() != sub {
			t.Fatalf("uid = %q, want %q", a.UID(), sub)
		}
		a.SetFilePath(FilePathFor(dir, a.UID()))
		if err := a.SaveAtomic(); err != nil {
			t.Fatalf("SaveAtomic(%s): %v", sub, err)
		}
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d accounts, want 2 (same-file overwrite is the bug)", len(loaded))
	}
}

// jwtWithSub 造一个只有 payload 有意义的 JWT：签名段随便填，uidFromToken 不验签。
func jwtWithSub(t *testing.T, sub string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": sub, "exp": 4102444800})
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// 桌面端 auth.json 也没有 expires_at。不补的话它停在 0，NeedsRefresh 判定为
// 「无需预刷新」，token 到期后账号直接失效、只能重新导入。JWT 的 exp 就是答案。
func TestParseFillsExpiresAtFromJWT(t *testing.T) {
	tok := jwtWithSub(t, "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417")
	a, err := Parse([]byte(`{"access_token":"` + tok + `","refresh_token":"r1"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.ExpiresAtValue() != 4102444800 {
		t.Fatalf("expires_at = %d, want the JWT exp 4102444800", a.ExpiresAtValue())
	}
	if !a.CanRefresh() {
		t.Fatal("CanRefresh = false, want true")
	}
}

// 显式 expires_at 仍然压过 JWT 的 exp：导入方自己算过有效期时以它为准。
func TestParsePrefersExplicitExpiresAt(t *testing.T) {
	tok := jwtWithSub(t, "pu-x")
	a, err := Parse([]byte(`{"access_token":"` + tok + `","expires_at":1700000000}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.ExpiresAtValue() != 1700000000 {
		t.Fatalf("expires_at = %d, want explicit 1700000000", a.ExpiresAtValue())
	}
}

// 同期创建的两个账号 sub 前缀完全相同（UUIDv7 的时间戳位），昵称必须取尾部才分得开。
func TestShortUIDDistinguishesSamePrefixAccounts(t *testing.T) {
	a1 := "pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417"
	a2 := "pu-01a1365c-dad8-7b40-976b-cd6b3f95bf7c"
	n1, n2 := shortUID(a1), shortUID(a2)
	if n1 == n2 {
		t.Fatalf("nicknames collide: %q == %q (must not truncate the shared UUIDv7 prefix)", n1, n2)
	}
	if n1 != "3a5f1bd91417" || n2 != "cd6b3f95bf7c" {
		t.Fatalf("shortUID = %q / %q, want the tail segments", n1, n2)
	}
}

// 老数据里存着旧口径昵称（uid 前 12 字符），加载时要自动纠正，否则界面上两行同名。
func TestLoadDirFixesLegacyNicknameCollision(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"pu-01a1365c-b8ff-7d05-98f5-3a5f1bd91417", "pu-01a1365c-dad8-7b40-976b-cd6b3f95bf7c"} {
		tok := jwtWithSub(t, sub)
		body := `{"uid":"` + sub + `","nickname":"pu-01a1365c","access_token":"` + tok + `"}`
		if err := os.WriteFile(FilePathFor(dir, sub), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d accounts, want 2", len(loaded))
	}
	if loaded[0].Nickname() == loaded[1].Nickname() {
		t.Fatalf("nicknames still collide: %q", loaded[0].Nickname())
	}
	for _, a := range loaded {
		if a.Nickname() == "pu-01a1365c" {
			t.Fatal("legacy nickname was not corrected on load")
		}
	}
}
