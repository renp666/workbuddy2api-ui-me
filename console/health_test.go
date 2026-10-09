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
	// autoclaw/trae/raccoon 走 /healthz。
	path string
	// body 是应答体；status 是应答码。
	body   string
	status int
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
	case channelTrae, channelRaccoon:
		return healthChannelStub{path: "/healthz", body: "ok", status: 200}
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
			if r.URL.Path != stub.path {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(stub.status)
			fmt.Fprint(w, stub.body)
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
		case channelRaccoon:
			cfg.RaccoonURL = target
			cfg.RaccoonKey = strings.Repeat("r", 32)
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
func TestAdminHealthAggregatesAllChannels(t *testing.T) {
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
