package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// healthChannelStub 描述一条旁路 sidecar 的探活应答形状。
type healthChannelStub struct {
	// path 是该通道探活命中的路径：glm/qoder 走 /v1/models，opencode 走 /health，
	// autoclaw/trae/raccoon 走 /healthz，freellm 走 /livez。
	path string
	// body 是应答体；status 是应答码。
	body   string
	status int
	// extra 覆盖同一 sidecar 的其他路径（freellm 的可达性与池就绪是两个端点）。
	// 未列出的路径仍按 404 应答，避免把「没设计到的调用」混成默认 200。
	extra map[string]healthChannelStub
}

func (stub healthChannelStub) serve(w http.ResponseWriter, path string) {
	if path != stub.path {
		if next, ok := stub.extra[path]; ok {
			next.serve(w, path)
			return
		}
		w.WriteHeader(404)
		return
	}
	w.WriteHeader(stub.status)
	fmt.Fprint(w, stub.body)
}

// healthyStub 返回一条默认健康应答，覆盖各通道被探活时命中的路径。
func healthyStub(c routeChannel) healthChannelStub {
	switch c {
	case channelGLM, channelQoder:
		return healthChannelStub{path: "/v1/models", body: `{"data":[{"id":"probe-model"}]}`, status: 200}
	case channelOpenCode:
		return healthChannelStub{path: "/health", body: `{"phase":"ready"}`, status: 200}
	case channelAutoClaw:
		return healthChannelStub{path: "/healthz", body: `{"healthy":2,"total":3}`, status: 200}
	case channelTrae, channelTraeAI, channelRaccoon:
		return healthChannelStub{path: "/healthz", body: "ok", status: 200}
	case channelFreellm:
		// 可达性看 /livez，池就绪看 /readyz；/v1/models 只用于目录，不再是可达判据。
		return healthChannelStub{
			path: "/livez", body: `{"status":"ok","version":"0.4.1","uptime_s":1}`, status: 200,
			extra: map[string]healthChannelStub{
				"/readyz":    {path: "/readyz", body: `{"status":"ok","ready_upstreams":1}`, status: 200},
				"/v1/models": {path: "/v1/models", body: `{"data":[{"id":"freellm-test-model","execution_status":"ready"}]}`, status: 200},
			},
		}
	}
	return healthChannelStub{path: "/", body: "{}", status: 200}
}

// healthConsole 建一个 console，并按 channels 里给出的通道挂上 sidecar stub。
// core 固定答 /internal/v1/info，让 core 通道恒可达。HealthProbe 由调用方决定。
func healthConsole(t *testing.T, channels map[routeChannel]healthChannelStub, probe bool) http.Handler {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	cfg.HealthProbe = probe
	for channel, stub := range channels {
		stub := stub
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stub.serve(w, r.URL.Path)
		}))
		t.Cleanup(ts.Close)
		target := mustParseURL(t, ts.URL)
		switch channel {
		case channelGLM:
			cfg.ZCodeURL = target
			cfg.ZCodeKey = strings.Repeat("z", 32)
		case channelQoder:
			cfg.QoderURL = target
			cfg.QoderKey = strings.Repeat("q", 32)
		case channelOpenCode:
			cfg.OpenCodeURL = target
			cfg.OpenCodeKey = strings.Repeat("o", 32)
		case channelAutoClaw:
			cfg.AutoClawURL = target
			cfg.AutoClawKey = strings.Repeat("c", 32)
		case channelTrae:
			cfg.TraeURL = target
			cfg.TraeKey = strings.Repeat("t", 32)
		case channelTraeAI:
			cfg.TraeAIURL = target
			cfg.TraeAIKey = strings.Repeat("i", 32)
		case channelRaccoon:
			cfg.RaccoonURL = target
			cfg.RaccoonKey = strings.Repeat("r", 32)
		case channelFreellm:
			cfg.FreellmURL = target
			cfg.FreellmKey = strings.Repeat("f", 32)
		}
	}
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type healthResponse struct {
	OK        bool                  `json:"ok"`
	CheckedAt time.Time             `json:"checked_at"`
	Channels  []healthChannelResult `json:"channels"`
	History   []healthHistoryEntry  `json:"history"`
}

// TestAdminHealthAggregatesAllChannels 锁定「网关 + 七通道」总览：七条通道
// 无论是否启用都要出现，且顺序与 routeChannelOrder 一致；全部健康时 ok=true。
// 「全部健康」现在要求每条已启用通道都有真调用证据（probe_state=ok）——所以
// 这里给每条通道记一条可用结论，模拟已完成探测。
func TestAdminHealthAggregatesAllChannels(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	h := healthConsole(t, stubs, false)
	srv := h.(*server)
	for _, c := range routeChannelOrder {
		srv.probe.record(c, string(c)+":probe-model", map[string]any{"ok": true})
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK {
		t.Fatalf("all-healthy round should be ok, got %+v", got.Channels)
	}
	if len(got.Channels) != len(routeChannelOrder) {
		t.Fatalf("channel count=%d, want %d", len(got.Channels), len(routeChannelOrder))
	}
	for i, want := range routeChannelOrder {
		item := got.Channels[i]
		if item.Channel != want {
			t.Fatalf("channel[%d]=%q, want %q（顺序须与 routeChannelOrder 一致）", i, item.Channel, want)
		}
		if !item.Enabled || !item.Reachable {
			t.Fatalf("channel %q enabled=%v reachable=%v, want both true", want, item.Enabled, item.Reachable)
		}
	}
	// autoclaw 成功时应附带 healthy/total 计数。
	for _, item := range got.Channels {
		if item.Channel == channelAutoClaw && item.Detail != "healthy=2 total=3" {
			t.Fatalf("autoclaw detail=%q, want healthy=2 total=3", item.Detail)
		}
	}
}

// TestAdminHealthReportsUnreachableChannel 锁定失败判定：任一已启用通道不可达
// 即整轮 ok=false，且该通道 reachable=false 并带原因，其余通道不受牵连。
func TestAdminHealthReportsUnreachableChannel(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	stubs[channelTrae] = healthChannelStub{path: "/healthz", body: "not-ok", status: 200}
	h := healthConsole(t, stubs, false)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Fatal("a single unreachable channel must flip the whole round to ok=false")
	}
	for _, item := range got.Channels {
		switch item.Channel {
		case channelTrae:
			if item.Reachable || item.Detail == "" {
				t.Fatalf("trae reachable=%v detail=%q, want unreachable with reason", item.Reachable, item.Detail)
			}
		case channelCore, channelGLM, channelQoder, channelOpenCode, channelAutoClaw, channelRaccoon:
			if !item.Reachable {
				t.Fatalf("channel %q should stay reachable", item.Channel)
			}
		}
	}
}

// TestAdminHealthOnlyProbesEnabledChannels 锁定「未启用通道不计入失败」：
// 只配 core 时，其余六条 enabled=false 且不参与 ok 判定。
func TestAdminHealthOnlyProbesEnabledChannels(t *testing.T) {
	h := healthConsole(t, nil, false)
	// 只有 core 启用；给它记一条可用结论，总览才有资格报 ok=true。
	h.(*server).probe.record(channelCore, "cn:glm-5.3", map[string]any{"ok": true})
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK {
		t.Fatal("core-only deployment should still be ok")
	}
	if len(got.Channels) != len(routeChannelOrder) {
		t.Fatalf("channel count=%d, want %d", len(got.Channels), len(routeChannelOrder))
	}
	for _, item := range got.Channels {
		if item.Channel == channelCore {
			if !item.Enabled || !item.Reachable {
				t.Fatalf("core must be enabled+reachable, got %+v", item)
			}
			continue
		}
		if item.Enabled {
			t.Fatalf("channel %q should be disabled in a core-only deployment", item.Channel)
		}
	}
}

// TestAdminHealthCoreUnreachable 锁定 core 探活失败口径：core 恒启用，
// 桥接握手失败即整轮 ok=false 且 core 带上原因。
func TestAdminHealthCoreUnreachable(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Fatal("unreachable core must flip the round to ok=false")
	}
	for _, item := range got.Channels {
		if item.Channel == channelCore {
			if item.Reachable || item.Detail == "" {
				t.Fatalf("core reachable=%v detail=%q, want unreachable with reason", item.Reachable, item.Detail)
			}
		}
	}
}

// TestHealthProbeGate 锁定后台探活的门控契约：HealthProbe=false（测试默认零值）
// 时不得启动后台协程，历史保持为空；置真时启动并很快写入首轮。
func TestHealthProbeGate(t *testing.T) {
	h := healthConsole(t, nil, false)
	s := h.(*server)
	if got := s.health.snapshot(); len(got) != 0 {
		t.Fatalf("HealthProbe=false 不应启动后台探活，history=%d 条", len(got))
	}
	// 聚合端点与后台协程解耦：未开探活时端点仍即时跑一轮。
	cookie, csrf := login(t, h)
	if w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf); w.Code != 200 {
		t.Fatalf("aggregate endpoint must stay available without the background probe: %d", w.Code)
	}
	if got := s.health.snapshot(); len(got) != 0 {
		t.Fatalf("聚合端点不应写入 history，history=%d 条", len(got))
	}

	probed := healthConsole(t, nil, true)
	ps := probed.(*server)
	deadline := time.Now().Add(3 * time.Second)
	for len(ps.health.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := ps.health.snapshot(); len(got) == 0 {
		t.Fatal("HealthProbe=true 应启动后台探活并写入首轮历史")
	}
}

// TestHealthHistoryBounded 锁定历史长度上限：写入超过上限时只保留最近 N 条。
func TestHealthHistoryBounded(t *testing.T) {
	cache := &healthCache{}
	for i := 0; i < healthHistorySize+5; i++ {
		cache.record(healthRound{CheckedAt: time.Unix(int64(i), 0), OK: true})
	}
	got := cache.snapshot()
	if len(got) != healthHistorySize {
		t.Fatalf("history len=%d, want %d", len(got), healthHistorySize)
	}
	if got[len(got)-1].CheckedAt.Unix() != int64(healthHistorySize+4) {
		t.Fatalf("history tail=%d, want the newest round", got[len(got)-1].CheckedAt.Unix())
	}
}

// TestHealthUnknownChannel 兜底分支：未知通道名一律判不可达。
func TestHealthUnknownChannel(t *testing.T) {
	h := &server{cfg: Config{CoreURL: mustParseURL(t, "http://127.0.0.1:1")}}
	reachable, detail := h.channelReachable(context.Background(), routeChannel("nope"))
	if reachable || detail == "" {
		t.Fatalf("unknown channel reachable=%v detail=%q", reachable, detail)
	}
}

// TestAdminHealthNotGreenWhenProbeFails 锁定「假绿」回归：端口/服务全部存活，
// 但手工测速探测已测出某通道模型全部失败时，总览必须 ok=false，并把真调用
// 结论（probe_state/probe_detail）带进该通道记录。修复前这里会假绿（ok=true）。
func TestAdminHealthNotGreenWhenProbeFails(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	h := healthConsole(t, stubs, false)
	srv := h.(*server)
	// 直接注入探测结论：raccoon 端口活、模型全失败（no_healthy_account）。
	srv.probe.record(channelRaccoon, "raccoon-sn-glm-5-3-flash", map[string]any{
		"ok":       false,
		"category": "error",
		"error":    `{"error":{"code":"no_healthy_account"}}`,
	})
	// core 有可用模型：不得因探测结论被误判为失败。
	srv.probe.record(channelCore, "global:deepseek-v4.1-flash", map[string]any{"ok": true})

	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Fatalf("端口全活但 raccoon 真调用全失败，总览不得报 ok=true（假绿）")
	}
	var raccoon *healthChannelResult
	for i := range got.Channels {
		if got.Channels[i].Channel == channelRaccoon {
			raccoon = &got.Channels[i]
		}
	}
	if raccoon == nil {
		t.Fatal("missing raccoon channel")
	}
	if !raccoon.Reachable {
		t.Fatalf("raccoon 端口应仍判可达（存活与可用是两回事），got reachable=false detail=%q", raccoon.Detail)
	}
	if raccoon.ProbeState != "failed" {
		t.Fatalf("raccoon probe_state=%q, want failed", raccoon.ProbeState)
	}
	if raccoon.ProbeTotalModels != 1 || raccoon.ProbeOKModels != 0 {
		t.Fatalf("raccoon probe models ok=%d total=%d, want 0/1", raccoon.ProbeOKModels, raccoon.ProbeTotalModels)
	}
	if raccoon.ProbeDetail == "" {
		t.Fatal("raccoon probe_detail 不得为空，页面要能直接解释红灯")
	}
	// 未探测的通道 probe_state 必须留空——「没测过」不得被当成健康或失败。
	for _, item := range got.Channels {
		if item.Channel == channelRaccoon || item.Channel == channelCore {
			continue
		}
		if item.ProbeState != "" {
			t.Fatalf("channel %q probe_state=%q, want empty（未探测）", item.Channel, item.ProbeState)
		}
	}
}

// TestAdminHealthNotGreenWhenUnproven 锁定「未知不是健康」：端口/服务全部存活、
// 但没有任何真调用结论（未探测）时，总览不得报 ok=true——否则就是「假绿」的另一种
// 形态（修复前只把 probe_state=failed 当不健康，未探测仍算绿）。
func TestAdminHealthNotGreenWhenUnproven(t *testing.T) {
	stubs := map[routeChannel]healthChannelStub{}
	for _, c := range routeChannelOrder {
		stubs[c] = healthyStub(c)
	}
	h := healthConsole(t, stubs, false)
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Fatal("端口全活但没有任何真调用结论时，总览不得报 ok=true（未探测 ≠ 健康）")
	}
	for _, item := range got.Channels {
		if !item.Enabled {
			continue
		}
		if !item.Reachable {
			t.Fatalf("channel %q 应判可达（存活与可用是两回事）", item.Channel)
		}
		if item.ProbeState != "" {
			t.Fatalf("channel %q probe_state=%q, want empty（未探测）", item.Channel, item.ProbeState)
		}
	}
}

// TestAdminHealthRaccoonCredentialHint 锁定「账号侧原因要带进健康结论」这条经验：
// raccoon 端口活着但真调用全失败时，除了 probe_detail，还要把账号池记录的失效原因
// 带进 probe_hint，让红灯自带可执行下一步（重登桌面端再导入凭证）——只报一句笼统的
// no_healthy_account，人无法区分「临时冷却」与「凭证已死必须重导」，而后者才是常态。
func TestAdminHealthRaccoonCredentialHint(t *testing.T) {
	const reason = "token_expired: 刷新后仍被拒，请重新导入凭证"
	raccoon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprint(w, "ok")
		case "/admin/api/accounts":
			fmt.Fprintf(w, `{"accounts":[{"uid":"raccoon-desktop","reason":%q}]}`, reason)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(raccoon.Close)

	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)

	cfg := testConfig(core.URL)
	cfg.RaccoonURL = mustParseURL(t, raccoon.URL)
	cfg.RaccoonKey = strings.Repeat("r", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := h.(*server)
	srv.probe.record(channelRaccoon, "raccoon-sn-glm-5-3-flash", map[string]any{
		"ok":       false,
		"category": "error",
		"error":    `{"error":{"code":"no_healthy_account"}}`,
	})
	srv.probe.record(channelCore, "global:deepseek-v4.1-flash", map[string]any{"ok": true})

	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Fatal("raccoon 真调用全失败时总览不得报 ok=true")
	}
	var item *healthChannelResult
	for i := range got.Channels {
		if got.Channels[i].Channel == channelRaccoon {
			item = &got.Channels[i]
		}
	}
	if item == nil {
		t.Fatal("missing raccoon channel")
	}
	if !item.Reachable {
		t.Fatalf("raccoon 端口应仍判可达，got detail=%q", item.Detail)
	}
	if item.ProbeHint != reason {
		t.Fatalf("raccoon probe_hint=%q, want %q", item.ProbeHint, reason)
	}
	// 健康通道不得带提示（只在红灯时才有意义）。
	for _, c := range got.Channels {
		if c.Channel == channelCore && c.ProbeHint != "" {
			t.Fatalf("core probe_hint=%q, want empty", c.ProbeHint)
		}
	}
}

// corePinHint 把「严格 pin 被锁账号在冷却」翻译成可直接执行的下一步。
// core 的 realm 是 patch 0010 的严格锁定（被锁账号不可用即 503，不做故障转移），
// 域内其余账号再健康也用不上，所以 pinned_account_unavailable 必须带上
// 「锁的是谁 / 卡在哪 / 多久恢复 / 域内有没有替补」才有人能照着处置。
func TestAdminHealthCorePinHint(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/info":
			mockInfo(w, r)
		case "/internal/v1/status":
			fmt.Fprint(w, `{
			  "realm_totals":{
			    "cn":{"healthy":2,"cooling":0,"total":2},
			    "global":{"healthy":1,"cooling":1,"total":2}},
			  "accounts":[
			    {"uid":"u-gp","realm":"global","nickname":"renp666","reason":"429 rate limit",
			     "cooling":true,"cool_remaining_sec":343},
			    {"uid":"u-gs","realm":"global","nickname":"renp886"},
			    {"uid":"u-cp","realm":"cn","nickname":"19258020078"},
			    {"uid":"u-cs","realm":"cn","nickname":"R.P"}]}`)
		case "/internal/v1/pin":
			fmt.Fprint(w, `{"pins":{
			  "global":{"uid":"u-gp","realm":"global","nickname":"renp666","exists":true},
			  "cn":{"uid":"u-cp","realm":"cn","nickname":"19258020078","exists":true}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(core.Close)

	h, err := NewServer(testConfig(core.URL))
	if err != nil {
		t.Fatal(err)
	}
	srv := h.(*server)
	srv.probe.record(channelCore, "global:deepseek-v4.1-flash", map[string]any{
		"ok":       false,
		"category": "error",
		"error":    `{"error":{"code":"pinned_account_unavailable"}}`,
	})

	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/health", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("health status=%d %s", w.Code, w.Body)
	}
	var got healthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var item *healthChannelResult
	for i := range got.Channels {
		if got.Channels[i].Channel == channelCore {
			item = &got.Channels[i]
		}
	}
	if item == nil {
		t.Fatal("missing core channel")
	}
	hint := item.ProbeHint
	for _, want := range []string{"global", "renp666", "429 rate limit", "343 秒", "不做故障转移", "renp886", "解除锁定"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("core probe_hint=%q，缺少片段 %q", hint, want)
		}
	}
	// cn 域被锁账号健康：不该被写进解释里（否则红灯原因被摊成两份，人更难定位）。
	if strings.Contains(hint, "cn 域") {
		t.Fatalf("cn 域 pin 健康却仍被写进 hint：%q", hint)
	}
}

// 被锁账号本身健康时不出提示：红灯另有原因，硬套 pin 说法反而误导。
func TestCorePinHintSkipsHealthyPin(t *testing.T) {
	h, err := NewServer(testConfig("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	srv := h.(*server)
	if hint := srv.corePinHint(context.Background()); hint != "" {
		// core 不可达：必须安静返回空，而不是把探测链路本身弄崩。
		t.Fatalf("core 不可达时 probe_hint=%q，want empty", hint)
	}
}

// 被锁账号健康（域内有冷却账号但不是锁定的那个）时不出提示。
func TestCorePinHintSilentWhenPinHealthy(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/status":
			fmt.Fprint(w, `{
			  "realm_totals":{"cn":{"healthy":2,"cooling":0,"total":2},
			                 "global":{"healthy":1,"cooling":1,"total":2}},
			  "accounts":[
			    {"uid":"u-gp","realm":"global","nickname":"renp666"},
			    {"uid":"u-gs","realm":"global","nickname":"renp886",
			     "reason":"429 rate limit","cooling":true,"cool_remaining_sec":120}]}`)
		case "/internal/v1/pin":
			fmt.Fprint(w, `{"pins":{"global":{"uid":"u-gp","realm":"global","nickname":"renp666","exists":true}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(core.Close)

	h, err := NewServer(testConfig(core.URL))
	if err != nil {
		t.Fatal(err)
	}
	hint := h.(*server).corePinHint(context.Background())
	if hint != "" {
		t.Fatalf("pin 账号健康时不应出提示，got %q", hint)
	}
}
