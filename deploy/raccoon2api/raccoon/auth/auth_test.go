package auth

import (
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
