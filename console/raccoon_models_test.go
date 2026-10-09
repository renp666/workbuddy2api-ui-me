package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testConsoleWithRaccoon 建一个带 raccoon2api sidecar 桩的 console；core 只答
// /internal/v1/info。cfg.HealthProbe 保持零值，否则后台探活协程会向桩发请求、
// 把 models 断言搅乱。
func testConsoleWithRaccoon(t *testing.T, sidecar http.HandlerFunc) http.Handler {
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
	cfg.RaccoonURL = mustParseURL(t, bridge.URL)
	cfg.RaccoonKey = strings.Repeat("r", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// raccoonBridge 桩住状态处理器真实调用的路径：/healthz 免鉴权且回纯文本 "ok"、
// /v1/models 回给定载荷、/status 与 /admin/api/accounts 回空账号。
func raccoonBridge(modelsJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			fmt.Fprint(w, "ok")
		case "/v1/models":
			fmt.Fprint(w, modelsJSON)
		case "/status":
			fmt.Fprint(w, `{"accounts":[]}`)
		case "/admin/api/accounts":
			fmt.Fprint(w, `{"accounts":[]}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"unexpected sidecar path"}`)
		}
	}
}

// adminRaccoonModels 走管理会话调 GET /admin/raccoon，解出模型列表。
func adminRaccoonModels(t *testing.T, h http.Handler) []map[string]any {
	t.Helper()
	cookie, csrf := login(t, h)
	w := adminRequest(h, "GET", "/admin/raccoon", "", cookie, csrf)
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

// sidecar 目录条目带 name（上游真实展示名），console 必须原样透传：前端靠它把
// raccoon-8c4485 这类代码名显示成 Raccoon-Work。
func TestAdminRaccoonModelsPassThroughName(t *testing.T) {
	const payload = `{"object":"list","data":[` +
		`{"id":"raccoon-8c4485","name":"Raccoon-Work"},` +
		`{"id":"raccoon-chat-ml-5-5","name":"Raccoon Chat ML 5.5"}]}`
	h := testConsoleWithRaccoon(t, raccoonBridge(payload))
	models := adminRaccoonModels(t, h)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	if models[0]["realm"] != "raccoon" {
		t.Errorf("realm = %#v, want raccoon", models[0]["realm"])
	}
	if models[0]["id"] != "raccoon-8c4485" {
		t.Errorf("id = %#v, want raccoon-8c4485", models[0]["id"])
	}
	if models[0]["name"] != "Raccoon-Work" {
		t.Errorf("name = %#v, want Raccoon-Work", models[0]["name"])
	}
	if models[1]["name"] != "Raccoon Chat ML 5.5" {
		t.Errorf("models[1].name = %#v, want Raccoon Chat ML 5.5", models[1]["name"])
	}
}

// sidecar 没给 name（或给了空值）时不得写该键，也不能凭空造展示名；id 仍按需加前缀。
func TestAdminRaccoonModelsOmitAbsentName(t *testing.T) {
	const payload = `{"object":"list","data":[` +
		`{"id":"raccoon-8c4485","name":""},` +
		`{"id":"sn-sensenova-6-8-flash-lite"}]}`
	h := testConsoleWithRaccoon(t, raccoonBridge(payload))
	models := adminRaccoonModels(t, h)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2: %#v", len(models), models)
	}
	for i, m := range models {
		if _, ok := m["name"]; ok {
			t.Errorf("models[%d] must not carry name: %#v", i, m)
		}
		if m["realm"] != "raccoon" {
			t.Errorf("models[%d].realm = %#v, want raccoon", i, m["realm"])
		}
	}
	if models[1]["id"] != "raccoon-sn-sensenova-6-8-flash-lite" {
		t.Errorf("models[1].id = %#v, want raccoon-sn-sensenova-6-8-flash-lite", models[1]["id"])
	}
}
