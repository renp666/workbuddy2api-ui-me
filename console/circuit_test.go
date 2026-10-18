package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func circuitFixture(t *testing.T) (*server, func(time.Duration)) {
	t.Helper()
	dir := t.TempDir()
	h := &server{cfg: Config{RouteFile: filepath.Join(dir, "routes.json")}, outcomes: newOutcomeWindow(), alerts: newAlertLedger(dir), probes: &probeCache{}}
	h.routes = newRouteStore(h.cfg.RouteFile)
	var clock atomic.Int64
	clock.Store(time.Now().UnixMilli())
	now := func() time.Time { return time.UnixMilli(clock.Load()) }
	h.outcomes.now = now
	h.initCircuits(now)
	t.Cleanup(func() { close(h.circuits.stop); <-h.circuits.done })
	return h, func(d time.Duration) { clock.Add(d.Milliseconds()) }
}

func circuitWait(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("熔断 worker 未完成")
		}
		time.Sleep(time.Millisecond)
	}
}

func circuitFeed(h *server, ok bool, category string) {
	h.outcomes.add(errorEvent{Ts: h.outcomes.now().UnixMilli(), Alias: "daily", Channel: "core", ModelResolved: "cn:bad", Category: category, ErrorSnippet: "Bearer abcdef123456 " + strings.Repeat("错", 250)}, ok)
}

func circuitSave(t *testing.T, h *server, alias string) {
	t.Helper()
	if err := h.routes.save([]routeEntry{{Alias: alias, Channel: channelCore, Model: "cn:bad", Enabled: true, Fallbacks: []routeTarget{{channelCore, "cn:backup"}}}}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCircuitTriggerFixtures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcomes []bool
		category string
		open     bool
		reason   string
	}{
		{"streak", []bool{false, false, false}, "upstream_error", true, "连续失败"},
		{"success resets", []bool{false, false, true, false, false}, "upstream_error", false, ""},
		{"pin", []bool{false, false, false, false, false}, "pinned_account_unavailable", false, ""},
		{"80 percent", []bool{false, false, true, false, false}, "upstream_error", false, ""},
		{"rate", []bool{false, false, true, false, false, true, false, false, true, false, false, true, false, false}, "upstream_error", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := circuitFixture(t)
			circuitSave(t, h, "daily")
			for _, ok := range tc.outcomes {
				circuitFeed(h, ok, tc.category)
			}
			if got := h.circuits.blocked(routeTarget{channelCore, "cn:bad"}); got != tc.open {
				t.Fatalf("open=%v want %v", got, tc.open)
			}
			if tc.open {
				circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
				if !strings.Contains(h.alerts.recentAlerts(1)[0].Message, tc.reason) {
					t.Fatal("依据缺失")
				}
			}
		})
	}
	// 80% 的历史窗口后再失败：无连续3失败，但 5/6 >80%。
	t.Run("rate over 80", func(t *testing.T) {
		h, _ := circuitFixture(t)
		circuitSave(t, h, "daily")
		h.outcomes.mu.Lock()
		for i := 0; i < 5; i++ {
			h.outcomes.samples = append(h.outcomes.samples, outcomeSample{ts: h.outcomes.now(), channel: channelCore, model: "cn:bad", ok: i == 4, category: "upstream_error"})
		}
		h.outcomes.mu.Unlock()
		circuitFeed(h, false, "upstream_error")
		circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
		a := h.alerts.recentAlerts(1)[0]
		if !strings.Contains(a.Message, "15min 错误率") || a.Samples != 6 || a.Fail != 5 {
			t.Fatalf("未走错误率触发: %+v", a)
		}
	})
}

func TestCircuitBackupRecoveryReplay(t *testing.T) {
	h, advance := circuitFixture(t)
	// 非标准键序、未知字段与空白必须一字不动。
	raw := []byte(`{ "enabled_models": ["manual"], "models": [{"note":"keep","model":"cn:bad","alias":"daily","channel":"core","enabled":true,"fallbacks":[{"model":"cn:backup","channel":"core"}]}], "version":1,"extra":{"key":true},"auto_fallback":"cn:auto" }`)
	if err := os.WriteFile(h.cfg.RouteFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h.routes.reload()
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	backup, err := os.ReadFile(filepath.Join(filepath.Dir(h.cfg.RouteFile), "routes.bak"))
	if err != nil || string(backup) != string(raw) {
		t.Fatalf("备份不匹配: %v", err)
	}
	rewritten, _ := os.ReadFile(h.cfg.RouteFile)
	if string(rewritten) != strings.Replace(string(raw), `"model":"cn:bad"`, `"model":"cn:backup"`, 1) {
		t.Fatalf("改了 active 之外内容: %s", rewritten)
	}
	if d := h.resolveEntry(context.Background(), h.routes.snapshotFile().Models[0]); d.RoutedModel != "cn:backup" {
		t.Fatal(d)
	}
	// 独立实例回放，后台 worker 不读取真实服务。
	replay := &server{cfg: h.cfg, routes: newRouteStore(h.cfg.RouteFile), outcomes: newOutcomeWindow(), alerts: newAlertLedger(filepath.Dir(h.cfg.RouteFile))}
	replay.initCircuits(h.outcomes.now)
	defer func() { close(replay.circuits.stop); <-replay.circuits.done }()
	if len(replay.routes.snapshotFile().Models) != 1 {
		t.Fatal("重启回放未加载路由配置")
	}
	if !replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("重启丢失熔断")
	}
	// 停掉原实例 worker，避免两个实例同时改同一 fixture。
	close(h.circuits.stop)
	<-h.circuits.done
	h.circuits.stop = make(chan struct{})
	h.circuits.done = make(chan struct{})
	close(h.circuits.done)
	advance(circuitCooldown - time.Millisecond)
	replay.circuits.sweep()
	replay.circuits.notify()
	if !replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("提前恢复")
	}
	advance(time.Millisecond)
	replay.circuits.sweep()
	replay.circuits.notify()
	circuitWait(t, func() bool { return !replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) })
	restored, _ := os.ReadFile(h.cfg.RouteFile)
	if string(restored) != string(raw) {
		t.Fatalf("恢复不匹配: %s", restored)
	}
	alerts := replay.alerts.recentAlerts(10)
	if len(alerts) != 1 || alerts[0].Rule != "circuit_closed" {
		t.Fatal(alerts)
	}
	replay.outcomes.now = h.outcomes.now
	advance(time.Millisecond)
	for i := 0; i < 2; i++ {
		circuitFeed(replay, false, "upstream_error")
	}
	if replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("观察态继承旧连败")
	}
	circuitFeed(replay, false, "upstream_error")
	if !replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("观察态未重新累计连败")
	}
}

func TestCircuitStrongProtected(t *testing.T) {
	h, _ := circuitFixture(t)
	circuitSave(t, h, "3-strong")
	raw, _ := os.ReadFile(h.cfg.RouteFile)
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	next, _ := os.ReadFile(h.cfg.RouteFile)
	if string(raw) != string(next) {
		t.Fatal("3-strong 被重写")
	}
	e := h.routes.snapshotFile().Models[0]
	if d := h.resolveEntry(context.Background(), e); d.RoutedModel != "cn:bad" {
		t.Fatal(d)
	}
}

func TestCircuitExpiredReplay(t *testing.T) {
	h, advance := circuitFixture(t)
	circuitSave(t, h, "daily")
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	advance(circuitCooldown)
	replay := &server{cfg: h.cfg, routes: newRouteStore(h.cfg.RouteFile), outcomes: newOutcomeWindow(), alerts: newAlertLedger(filepath.Dir(h.cfg.RouteFile))}
	replay.initCircuits(h.outcomes.now)
	defer func() { close(replay.circuits.stop); <-replay.circuits.done }()
	if replay.circuits.blocked(routeTarget{channelCore, "cn:bad"}) || replay.routes.snapshotFile().Models[0].Model != "cn:bad" {
		t.Fatal("过期回放未恢复")
	}
}

func TestCircuitMultipleOpenRecovery(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			h, advance := circuitFixture(t)
			e := routeEntry{Alias: "daily", Channel: channelCore, Model: "cn:bad", Enabled: true, Fallbacks: []routeTarget{{channelCore, "cn:backup"}, {channelCore, "cn:last"}}}
			if err := h.routes.save([]routeEntry{e}, ""); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				circuitFeed(h, false, "upstream_error")
			}
			circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
			advance(time.Minute)
			for i := 0; i < 3; i++ {
				h.outcomes.add(errorEvent{Ts: h.outcomes.now().UnixMilli(), Alias: "daily", Channel: "core", ModelResolved: "cn:backup", Category: "upstream_error"}, false)
			}
			circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 2 })
			if d := h.resolveEntry(context.Background(), h.routes.reload().Models[0]); d.RoutedModel != "cn:last" {
				t.Fatal(d)
			}
			if reverse {
				h.circuits.mu.Lock()
				a := h.circuits.states[routeTarget{channelCore, "cn:bad"}]
				b := h.circuits.states[routeTarget{channelCore, "cn:backup"}]
				a.Ts, b.Ts = b.Ts, a.Ts
				h.circuits.states[routeTarget{channelCore, "cn:bad"}] = a
				h.circuits.states[routeTarget{channelCore, "cn:backup"}] = b
				h.circuits.mu.Unlock()
			}
			advance(29 * time.Minute)
			h.circuits.sweep()
			h.circuits.notify()
			circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 3 })
			want := "cn:bad"
			if reverse {
				want = "cn:backup"
			}
			if d := h.resolveEntry(context.Background(), h.routes.reload().Models[0]); d.RoutedModel != want {
				t.Fatalf("first close: %+v want %s", d, want)
			}
			advance(time.Minute)
			h.circuits.sweep()
			h.circuits.notify()
			circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 4 })
			if got := h.routes.reload().Models[0].Model; got != "cn:bad" {
				t.Fatalf("恢复后 active=%s", got)
			}
		})
	}
}

func TestCircuitRecoveryPreservesManualEdit(t *testing.T) {
	h, advance := circuitFixture(t)
	circuitSave(t, h, "daily")
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	e := h.routes.snapshotFile().Models[0]
	e.Model = "cn:manual"
	e.Enabled = false
	if err := h.routes.save([]routeEntry{e}, ""); err != nil {
		t.Fatal(err)
	}
	advance(circuitCooldown)
	h.circuits.sweep()
	h.circuits.notify()
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 2 })
	e = h.routes.reload().Models[0]
	if e.Model != "cn:manual" || e.Enabled {
		t.Fatal("恢复覆盖了人工编辑")
	}
}

func TestCircuitPinExitClassification(t *testing.T) {
	h, _ := circuitFixture(t)
	h.alerts = nil
	circuitSave(t, h, "daily")
	for i := 0; i < 5; i++ {
		h.finishExitObserve(exitObservation{model: "daily", prismChannel: "core", routedModel: "cn:bad", status: 503, body: `{"error":{"code":"pinned_account_unavailable"}}`})
	}
	if h.circuits.blocked(routeTarget{channelCore, "cn:bad"}) {
		t.Fatal("M1 pin 归类触发熔断")
	}
}

func TestCircuitAllOpenExit(t *testing.T) {
	var upstream atomic.Int64
	core := httptest.NewServer(chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			upstream.Add(1)
		}
		w.WriteHeader(503)
	}))
	defer core.Close()
	cfg := testConfig(core.URL)
	cfg.HealthProbe = true
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	handler, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.(*server)
	defer func() { close(h.circuits.stop); <-h.circuits.done }()
	if err := h.routes.save([]routeEntry{{Alias: "daily", Channel: channelCore, Model: "cn:bad", Enabled: true}}, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	response := postChat(handler, "/v1/chat/completions", `{"model":"daily","messages":[]}`)
	if response.Code != 503 || upstream.Load() != 0 {
		t.Fatalf("全熔断仍转发: status=%d calls=%d", response.Code, upstream.Load())
	}
	waitErrorEvents(t, filepath.Dir(cfg.RouteFile), 1)
}

func TestCircuitSwitchWritesSerialized(t *testing.T) {
	h, advance := circuitFixture(t)
	circuitSave(t, h, "daily")
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := h.routes.setModelSwitch("cn:manual", true); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 3; i++ {
		circuitFeed(h, false, "upstream_error")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	advance(circuitCooldown)
	h.circuits.sweep()
	h.circuits.notify()
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 2 })
	if enabled, has := h.routes.modelOverride("cn:manual"); !has || !enabled {
		t.Fatal("熔断事务丢失模型启停覆盖")
	}
}

func TestCircuitNativeKey(t *testing.T) {
	h, _ := circuitFixture(t)
	e := routeEntry{Alias: "daily", Channel: channelOpenCode, Model: "opencode-bad", Enabled: true, Fallbacks: []routeTarget{{channelCore, "cn:backup"}}}
	if err := h.routes.save([]routeEntry{e}, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		h.outcomes.add(errorEvent{Ts: h.outcomes.now().UnixMilli(), Alias: "daily", Channel: "opencode", ModelResolved: "bad", Category: "upstream_error"}, false)
	}
	circuitWait(t, func() bool { return len(h.alerts.recentAlerts(10)) == 1 })
	if !h.circuits.blocked(routeTarget{channelOpenCode, "opencode-bad"}) || h.circuits.blocked(routeTarget{channelQoder, "bad"}) {
		t.Fatal("通道/原生模型键错误")
	}
	if d := h.resolveEntry(context.Background(), e); d.RoutedModel != "cn:backup" {
		t.Fatal(d)
	}
	if len([]rune(h.alerts.recentAlerts(1)[0].ErrorSnippet)) > 200 {
		t.Fatal("摘要超过200字")
	}
}

func TestCircuitReplaysUncommittedIntent(t *testing.T) {
	h, _ := circuitFixture(t)
	circuitSave(t, h, "daily")
	intent := circuitState{Ts: h.outcomes.now().UnixMilli(), Channel: channelCore, Model: "cn:bad", Open: true, Reason: "连续失败 3 次", Changes: []circuitChange{{Alias: "daily", Before: routeTarget{channelCore, "cn:bad"}, After: routeTarget{channelCore, "cn:backup"}}}}
	if err := h.circuits.store.append(intent); err != nil {
		t.Fatal(err)
	}
	replay := &server{cfg: h.cfg, routes: newRouteStore(h.cfg.RouteFile), outcomes: newOutcomeWindow(), alerts: newAlertLedger(filepath.Dir(h.cfg.RouteFile))}
	replay.initCircuits(h.outcomes.now)
	defer func() { close(replay.circuits.stop); <-replay.circuits.done }()
	if replay.routes.snapshotFile().Models[0].Model != "cn:backup" {
		t.Fatal("未完成意图没有回放")
	}
}
