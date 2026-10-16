package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readAlertEvents 读出 alerts.jsonl 的全部行。
func readAlertEvents(t *testing.T, dir string) []alertEvent {
	t.Helper()
	path := filepath.Join(dir, "alerts.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []alertEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev alertEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func waitAlerts(t *testing.T, dir string, want int) []alertEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := readAlertEvents(t, dir)
		if len(events) >= want {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("告警落盘不足：想要 %d 条，实际 %d 条", want, len(events))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAlertAliasErrorRateRule 覆盖 PRD 判据 3：注入 ≥5 次同别名失败调用使错误率 >50%
// → alerts.jsonl 出 P1 事件。
func TestAlertAliasErrorRateRule(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"too many"}}`)
	}))
	if got := saveRoutes(t, h, []routeEntry{{Alias: "fast", Channel: channelCore, Model: "cn:test", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	for i := 0; i < 5; i++ {
		postChat(h, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	}
	waitErrorEvents(t, dir, 5)
	alerts := waitAlerts(t, dir, 1)
	a := alerts[0]
	if a.Level != "P1" || a.Rule != alertRuleAliasErrorRate {
		t.Fatalf("告警级别/规则错：%+v", a)
	}
	if a.Alias != "fast" || a.Channel != "core" {
		t.Fatalf("告警归因错：%+v", a)
	}
	if a.Samples != 5 || a.Fail != 5 {
		t.Fatalf("告警样本口径错：%+v", a)
	}
	if a.ErrorRate != 1 {
		t.Fatalf("错误率错：%v", a.ErrorRate)
	}
	if !strings.Contains(a.Message, "fast") {
		t.Fatalf("告警文案缺别名：%q", a.Message)
	}
	// 抑制：同 (channel, category) 30min 内只出一条。
	time.Sleep(80 * time.Millisecond)
	if got := readAlertEvents(t, dir); len(got) != 1 {
		t.Fatalf("30min 抑制失效，落了 %d 条：%+v", len(got), got)
	}
}

// TestAlertAliasSampleGuard 样本 <5 不告警（防呆铁律）。
func TestAlertAliasSampleGuard(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"too many"}}`)
	}))
	if got := saveRoutes(t, h, []routeEntry{{Alias: "fast", Channel: channelCore, Model: "cn:test", Enabled: true}}, ""); got.Code != 200 {
		t.Fatalf("save: %d %s", got.Code, got.Body)
	}
	for i := 0; i < 4; i++ {
		postChat(h, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	}
	waitErrorEvents(t, dir, 4)
	time.Sleep(100 * time.Millisecond)
	if got := readAlertEvents(t, dir); len(got) != 0 {
		t.Fatalf("样本 <5 不该告警：%+v", got)
	}
}

// TestAlertPinRule 覆盖 pin 规则：任一 pin 类错误事件即告警。
func TestAlertPinRule(t *testing.T) {
	h, _, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"pinned_account_unavailable"}}`)
	}))
	postChat(h, "/v1/chat/completions", `{"model":"cn:test","messages":[{"role":"user","content":"hi"}]}`)
	alerts := waitAlerts(t, dir, 1)
	a := alerts[0]
	if a.Rule != alertRulePinError || a.Category != "pin" || a.Level != "P1" {
		t.Fatalf("pin 告警口径错：%+v", a)
	}
	if !strings.Contains(a.Message, "pinned") && !strings.Contains(a.Message, "pin") {
		t.Fatalf("pin 告警文案缺提示：%q", a.Message)
	}
}

// TestAdminAlertsEndpoint 覆盖 GET /admin/alerts（含鉴权）。
func TestAdminAlertsEndpoint(t *testing.T) {
	h, srv, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"pinned_account_unavailable"}}`)
	}))
	postChat(h, "/v1/chat/completions", `{"model":"cn:test","messages":[{"role":"user","content":"hi"}]}`)
	waitAlerts(t, dir, 1)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/alerts", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("alerts status=%d %s", w.Code, w.Body)
	}
	var got struct {
		Level  string       `json:"level"`
		Alerts []alertEvent `json:"alerts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Level != "P1" || len(got.Alerts) != 1 || got.Alerts[0].Rule != alertRulePinError {
		t.Fatalf("alerts 载荷错：%+v", got)
	}
	if w := adminRequest(h, "GET", "/admin/alerts", "", nil, ""); w.Code != 401 {
		t.Fatalf("未鉴权应 401，got %d", w.Code)
	}
	// 门控：模块关闭时不建 alerts.jsonl。
	_ = srv
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mockInfo(w, r) {
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"pinned_account_unavailable"}}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	dir2 := t.TempDir()
	cfg.RouteFile = filepath.Join(dir2, "routes.json")
	h2, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	postChat(h2, "/v1/chat/completions", chatBody("cn:test"))
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir2, "alerts.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("模块关闭时不得落 alerts.jsonl：%v", err)
	}
}

// TestAlertCleanupPrunesExpired 覆盖 30 天保留：过期行被清掉，抑制时刻回填。
func TestAlertCleanupPrunesExpired(t *testing.T) {
	dir := t.TempDir()
	old := alertEvent{Ts: time.Now().Add(-40 * 24 * time.Hour).UnixMilli(), Level: "P1", Rule: alertRulePinError, Channel: "core", Category: "pin", Message: "old"}
	fresh := alertEvent{Ts: time.Now().Add(-1 * time.Hour).UnixMilli(), Level: "P1", Rule: alertRulePinError, Channel: "core", Category: "pin", Message: "fresh"}
	l := newAlertLedger(dir)
	if !l.record(old) {
		t.Fatal("old 首次应记录")
	}
	// 手工把旧行塞进文件（record 的抑制会挡掉第二条同类）。
	line, _ := json.Marshal(fresh)
	if err := os.WriteFile(filepath.Join(dir, "alerts.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	l2 := newAlertLedger(dir)
	l2.cleanup(time.Now())
	kept := readAlertEvents(t, dir)
	if len(kept) != 1 || kept[0].Message != "fresh" {
		t.Fatalf("过期清理口径错：%+v", kept)
	}
	// 抑制时刻回填：1 小时内的 fresh 同类告警应被抑制。
	if l2.record(fresh) {
		t.Fatal("重启后抑制窗口未回填，重启即重报")
	}
}
