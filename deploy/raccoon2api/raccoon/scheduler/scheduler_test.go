package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 界面上的「今日已签到」按本地时区判，不是 UTC：用户在东八区看的是本地的今天。
func TestSameDayUsesLocalDate(t *testing.T) {
	// 用 time.Local 构造，和 sameDay 的判据保持同一时区；塞 FixedZone 会被 .Local()
	// 换算到进程时区，测试本身就先跨日了。
	a := time.Date(2026, 10, 21, 0, 5, 0, 0, time.Local)
	b := time.Date(2026, 10, 21, 23, 55, 0, 0, time.Local)
	if !sameDay(a, b) {
		t.Fatal("同一天的两个时刻应判为同日")
	}
	if sameDay(a, time.Date(2026, 10, 22, 0, 5, 0, 0, time.Local)) {
		t.Fatal("跨日应判为不同日")
	}
}

// 签到时间必须落盘：只放内存的话容器一重启就丢，界面会从「已签到」退回「未签到」，
// 用户分不清是真没签还是重启忘了。
func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "schedule.json")
	stamp := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	before := &Scheduler{cfg: Config{StatePath: path}}
	before.lastCheckin = stamp
	before.saveState()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("状态文件没写出来：%v", err)
	}

	after := &Scheduler{cfg: Config{StatePath: path}}
	after.loadState()
	if !after.lastCheckin.Equal(stamp) {
		t.Fatalf("lastCheckin = %v, want %v", after.lastCheckin, stamp)
	}
}

// 没配 StatePath 时读写都要静默跳过，不能 panic、也不能拦住签到。
func TestStateWithoutPathIsNoop(t *testing.T) {
	s := &Scheduler{cfg: Config{}}
	s.saveState()
	s.loadState()
	if !s.lastCheckin.IsZero() {
		t.Fatal("没有状态文件时 lastCheckin 应保持零值")
	}
}

// 状态文件损坏时只当作没签过，不能让 sidecar 起不来。
func TestLoadStateIgnoresBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{cfg: Config{StatePath: path}}
	s.loadState()
	if !s.lastCheckin.IsZero() {
		t.Fatal("损坏的状态文件应被忽略")
	}
}

func TestStatusReportsCheckedToday(t *testing.T) {
	s := &Scheduler{cfg: Config{CheckinHour: 9, RefreshHours: []int{3}}}
	if got := s.Status()["checked_today"]; got != false {
		t.Fatalf("从没签到过时 checked_today = %v, want false", got)
	}
	s.lastCheckin = time.Now()
	if got := s.Status()["checked_today"]; got != true {
		t.Fatalf("刚签到后 checked_today = %v, want true", got)
	}
	s.lastCheckin = time.Now().Add(-48 * time.Hour)
	if got := s.Status()["checked_today"]; got != false {
		t.Fatalf("两天前签到时 checked_today = %v, want false", got)
	}
}
