package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// testConsoleWithAutoClaw 建一个带 autoclaw2api sidecar 桩的 console；core 只答
// /internal/v1/info。cfg.HealthProbe 保持零值（false），否则后台探活协程会向桩发请求、
// 把 models 断言搅乱。
func testConsoleWithAutoClaw(t *testing.T, sidecar http.HandlerFunc) http.Handler {
	t.Helper()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			fmt.Fprint(w, `{"core":true}`)
		}
	}))
	t.Cleanup(core.Close)
	bridge := httptest.NewServer(sidecar)
	t.Cleanup(bridge.Close)
	cfg := testConfig(core.URL)
	cfg.AutoClawURL = mustParseURL(t, bridge.URL)
	cfg.AutoClawKey = strings.Repeat("c", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// autoClawBridge 桩住状态处理器真实调用的三个路径：/healthz 免鉴权、/v1/models 回给定载荷、
// /status 回空账号。modelsJSON 由调用方给，桩不解释它。
func autoClawBridge(modelsJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprint(w, `{"healthy":2,"total":2}`)
		case "/v1/models":
			fmt.Fprint(w, modelsJSON)
		case "/status":
			fmt.Fprint(w, `{"accounts":[],"mode":"idle"}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"unexpected sidecar path"}`)
		}
	}
}

// adminAutoClawModels 走管理会话调 GET /admin/autoclaw，解出模型列表。
func adminAutoClawModels(t *testing.T, h http.Handler) []map[string]any {
	t.Helper()
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/autoclaw", "", cookie, csrf)
	if w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	return out.Models
}

// bridge 按账号下发归属、tier 标出目标档，console 必须原样透传，不改结构也不补默认值。
func TestAdminAutoClawModelsPassThroughAccountsAndTier(t *testing.T) {
	const payload = `{"object":"list","data":[` +
		`{"id":"global/zai_auto","accounts":[{"uid":"ac-1","nickname":"一号"}],"tier":"agent"},` +
		`{"id":"cn/openclaw/default","accounts":[]}]}`
	h := testConsoleWithAutoClaw(t, autoClawBridge(payload))
	models := adminAutoClawModels(t, h)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	m := models[0]
	if m["realm"] != "autoclaw" {
		t.Errorf("realm = %#v, want autoclaw", m["realm"])
	}
	if m["id"] != "autoclaw-global/zai_auto" {
		t.Errorf("id = %#v, want autoclaw-global/zai_auto", m["id"])
	}
	if m["tier"] != "agent" {
		t.Errorf("tier = %#v, want agent", m["tier"])
	}
	want := []any{map[string]any{"uid": "ac-1", "nickname": "一号"}}
	if got, ok := m["accounts"].([]any); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("accounts = %#v, want %#v", m["accounts"], want)
	}
	if models[1]["id"] != "autoclaw-cn/openclaw/default" {
		t.Errorf("models[1].id = %#v, want autoclaw-cn/openclaw/default", models[1]["id"])
	}
	// accounts 为空数组时不得写入该键。
	if _, ok := models[1]["accounts"]; ok {
		t.Errorf("empty accounts array must not be forwarded: %#v", models[1])
	}
}

// bridge 没给 accounts / tier（或给了空值）时，模型条目里不得出现这两个键，
// 也不能凭空造归属。已带 autoclaw- 前缀的 id 不重复加前缀。
func TestAdminAutoClawModelsOmitAbsentAccountsAndTier(t *testing.T) {
	const payload = `{"object":"list","data":[` +
		`{"id":"global/zai_auto","accounts":[],"tier":""},` +
		`{"id":"autoclaw-cn/openclaw/default"}]}`
	h := testConsoleWithAutoClaw(t, autoClawBridge(payload))
	models := adminAutoClawModels(t, h)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	for i, m := range models {
		if _, ok := m["accounts"]; ok {
			t.Errorf("models[%d] must not carry accounts: %#v", i, m)
		}
		if _, ok := m["tier"]; ok {
			t.Errorf("models[%d] must not carry tier: %#v", i, m)
		}
		if m["realm"] != "autoclaw" {
			t.Errorf("models[%d].realm = %#v, want autoclaw", i, m["realm"])
		}
	}
	if models[1]["id"] != "autoclaw-cn/openclaw/default" {
		t.Errorf("models[1].id = %#v, want autoclaw-cn/openclaw/default", models[1]["id"])
	}
}
