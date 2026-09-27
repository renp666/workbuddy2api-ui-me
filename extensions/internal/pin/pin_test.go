package pin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMissingAndSetGetClear(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "pin.json"))
	if err != nil {
		t.Fatalf("open missing: %v", err)
	}
	if got := s.PinnedUID("cn"); got != "" {
		t.Fatalf("empty store returned %q", got)
	}
	if err := s.Set("cn", "19258020078"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := s.PinnedUID("cn"); got != "19258020078" {
		t.Fatalf("pinned uid = %q", got)
	}
	if got := s.PinnedUID("global"); got != "" {
		t.Fatalf("global leaked: %q", got)
	}
	// 同域替换
	if err := s.Set("cn", "other-uid"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := s.PinnedUID("cn"); got != "other-uid" {
		t.Fatalf("replace uid = %q", got)
	}
	// 双域独立
	if err := s.Set("global", "g-uid"); err != nil {
		t.Fatalf("set global: %v", err)
	}
	snap := s.Snapshot()
	if len(snap) != 2 || snap["cn"] != "other-uid" || snap["global"] != "g-uid" {
		t.Fatalf("snapshot = %v", snap)
	}
	if err := s.Clear("cn"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := s.PinnedUID("cn"); got != "" {
		t.Fatalf("clear failed: %q", got)
	}
	if got := s.PinnedUID("global"); got != "g-uid" {
		t.Fatalf("clear cn affected global: %q", got)
	}
}

func TestPersistenceReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pin.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Set("cn", "persisted-uid"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Clear("cn"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.Set("global", "g2"); err != nil {
		t.Fatalf("set global: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.PinnedUID("cn"); got != "" {
		t.Fatalf("cn should be empty, got %q", got)
	}
	if got := reloaded.PinnedUID("global"); got != "g2" {
		t.Fatalf("global = %q after reload", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pin.json perm = %o, want 0600", perm)
	}
}

func TestCorruptFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pin.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "损坏") {
		t.Fatalf("corrupt file should be rejected with explicit error, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "pin.json"))
	for _, tc := range []struct{ realm, uid string }{
		{"", "u"},
		{"eu", "u"},
		{"cn", ""},
		{"cn", " "},
		{"cn", strings.Repeat("x", maxUIDLen+1)},
	} {
		if err := s.Set(tc.realm, tc.uid); err == nil {
			t.Fatalf("Set(%q,%q) accepted", tc.realm, tc.uid)
		}
	}
	if err := s.Clear("eu"); err == nil {
		t.Fatal("Clear(eu) accepted")
	}
}
