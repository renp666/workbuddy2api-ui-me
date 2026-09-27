// model_filter_test.go 补丁 0009 回归：模型可用性过滤（零可用才隐藏）、
// realm 归属标签、成功响应账号归属头（X-Account / X-Account-Realm）。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// TestModelListHidesFullyCooledCNModels CN 域：某模型在全部账号上被 6004 模型级
// 冷却 → 该模型隐藏；未冷却模型保留；realm 标签恒为 cn。
func TestModelListHidesFullyCooledCNModels(t *testing.T) {
	resetModelsCache()
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `{"code":0,"data":{"models":[
			{"id":"dyn-hot","maxInputTokens":65536,"maxOutputTokens":8192},
			{"id":"dyn-cooled","maxInputTokens":65536,"maxOutputTokens":8192}
		],"agents":[{"name":"cli","models":["dyn-hot","dyn-cooled"]}]}}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	// 唯一账号在 dyn-cooled 上进入模型级冷却（6004 独立冷却）。
	p.CooldownSoftForModel("u1", time.Hour, time.Now().Add(time.Hour), "dyn-cooled", "test")
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: false})

	byID := map[string]map[string]any{}
	for _, m := range h.modelList() {
		if id, ok := m["id"].(string); ok {
			byID[id] = m
		}
	}
	if _, ok := byID["cn:dyn-hot"]; !ok {
		t.Error("cn:dyn-hot should stay listed (account available for it)")
	}
	if _, ok := byID["cn:dyn-cooled"]; ok {
		t.Error("cn:dyn-cooled must be hidden (zero available accounts for it)")
	}
	if r := byID["cn:dyn-hot"]["realm"]; r != "cn" {
		t.Errorf("realm tag=%v want cn", r)
	}
}

// TestModelListEmptyPoolHidesAllStaticCN 池内无任何账号：静态兜底分支整域隐藏
// （零可用才隐藏的极端形态），列表只剩（若开启的）其他域。
func TestModelListEmptyPoolHidesAllStaticCN(t *testing.T) {
	resetModelsCache()
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New(), GlobalEnabled: false})
	if got := h.modelList(); len(got) != 0 {
		t.Errorf("empty pool: modelList=%d entries want 0 (no account → nothing servable)", len(got))
	}
}

// TestModelListDisabledAccountHidesRealm 唯一 CN 账号被禁用 → cn 域全部隐藏。
func TestModelListDisabledAccountHidesRealm(t *testing.T) {
	resetModelsCache()
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	p.Disable("u1", "test")
	h := NewHandler(Config{Pool: p, Upstream: upstream.New(), GlobalEnabled: false})
	for _, m := range h.modelList() {
		if id, _ := m["id"].(string); strings.HasPrefix(id, "cn:") {
			t.Fatalf("disabled-only pool: cn model %q must be hidden", id)
		}
	}
}

// TestChatSuccessCarriesAccountHeaders 成功响应带归属头：昵称 ASCII → X-Account=昵称；
// 流式与非流式两条成功路径都透出；realm 头反映账号平台。
func TestChatSuccessCarriesAccountHeaders(t *testing.T) {
	// 非流式：上游仍回 SSE，handler 走 Aggregate 聚合（isStream=false → content-type json）。
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("sync code=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("X-Account"); got != "alice" {
		t.Errorf("sync X-Account=%q want alice", got)
	}
	if got := rec.Header().Get("X-Account-Realm"); got != "cn" {
		t.Errorf("sync X-Account-Realm=%q want cn", got)
	}

	// 流式：中文昵称回落 UID（头值非 ASCII 会被 net/http 丢弃）。
	upStream := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	p2 := testPoolWith(&auth.Auth{UID: "u2", Nickname: "小明", AccessToken: "at2", ExpiresAt: 9999999999})
	h2 := NewHandler(Config{Pool: p2, Upstream: upStream})
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)))
	if rec2.Code != 200 {
		t.Fatalf("stream code=%d body=%s", rec2.Code, rec2.Body)
	}
	if got := rec2.Header().Get("X-Account"); got != "u2" {
		t.Errorf("stream X-Account=%q want u2 (non-ASCII nickname falls back to UID)", got)
	}
	if got := rec2.Header().Get("X-Account-Realm"); got != "cn" {
		t.Errorf("stream X-Account-Realm=%q want cn", got)
	}
}

// TestChatErrorPathKeepsAccountHidden 错误路径不泄露账号：全部账号不可用 →
// 503 无 X-Account 头（末端错误规范化语义保持）。
func TestChatErrorPathKeepsAccountHidden(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	p.Disable("u1", "test")
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code == 200 {
		t.Fatalf("disabled account must not serve: code=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Account"); got != "" {
		t.Errorf("error path must not carry X-Account, got %q", got)
	}
}

// TestSetAccountHeadersSanitizes 昵称含控制字符（非可打印 ASCII）→ 回落 UID；
// 头值统一经 sanitizeHeaderValue 剥除 CR/LF（响应头注入防护）。
func TestSetAccountHeadersSanitizes(t *testing.T) {
	rec := httptest.NewRecorder()
	setAccountHeaders(rec, &auth.Auth{UID: "u9", Nickname: "a\r\nb X-Injected: 1 ", AccessToken: "at"})
	if got := rec.Header().Get("X-Account"); got != "u9" {
		t.Errorf("non-printable nickname must fall back to UID, got %q", got)
	}
	if got := sanitizeHeaderValue("a\r\nb X-Injected: 1 "); got != "ab X-Injected: 1" {
		t.Errorf("sanitizeHeaderValue=%q want control chars stripped + trimmed", got)
	}
}

// TestModelListCarriesAccountRefs 多账号池：模型条目透出 accounts 清单（UID 升序、
// 昵称展示、无昵称只有 uid），且模型级冷却的账号不出现在该模型清单中。
func TestModelListCarriesAccountRefs(t *testing.T) {
	resetModelsCache()
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, `{"code":0,"data":{"models":[
			{"id":"dyn-a","maxInputTokens":65536,"maxOutputTokens":8192},
			{"id":"dyn-b","maxInputTokens":65536,"maxOutputTokens":8192}
		],"agents":[{"name":"cli","models":["dyn-a","dyn-b"]}]}}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "u2", Nickname: "bob", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}, // 无昵称
	)
	// u2 在 dyn-b 上模型级冷却 → dyn-b 清单只含 u1；dyn-a 清单含两个账号（u1 在前）。
	p.CooldownSoftForModel("u2", time.Hour, time.Now().Add(time.Hour), "dyn-b", "test")
	h := NewHandler(Config{Pool: p, Upstream: up, GlobalEnabled: false})

	byID := map[string]map[string]any{}
	for _, m := range h.modelList() {
		if id, ok := m["id"].(string); ok {
			byID[id] = m
		}
	}
	refsJSON := func(m map[string]any) string {
		raw, err := json.Marshal(m["accounts"])
		if err != nil {
			t.Fatalf("marshal accounts: %v", err)
		}
		return string(raw)
	}
	if got := refsJSON(byID["cn:dyn-a"]); got != `[{"uid":"u1"},{"nickname":"bob","uid":"u2"}]` {
		t.Errorf("dyn-a accounts=%s want [u1, u2(bob)] sorted by uid", got)
	}
	if got := refsJSON(byID["cn:dyn-b"]); got != `[{"uid":"u1"}]` {
		t.Errorf("dyn-b accounts=%s want [u1] (u2 cooled for this model)", got)
	}
}

// TestModelListRealmTagGlobal global 域条目带 realm=global 标签。
func TestModelListRealmTagGlobal(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	resetModelsCache()
	cf := newGlobalModelsHandlerFake(t, 500, `{"code":500,"msg":"boom"}`) // 探测失败 → 静态名单
	p := testPoolWith(&auth.Auth{UID: "g1", AccessToken: "at_gl", Domain: "www.workbuddy.ai", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: cf.up, GlobalEnabled: true})

	var found int
	for _, m := range h.modelList() {
		id, _ := m["id"].(string)
		if strings.HasPrefix(id, "global:") {
			found++
			if m["realm"] != "global" {
				t.Fatalf("%s realm=%v want global", id, m["realm"])
			}
		}
	}
	if found == 0 {
		t.Fatal("no global models listed with healthy global account")
	}
	raw, _ := json.Marshal(h.modelList())
	if !strings.Contains(string(raw), `"realm":"global"`) {
		t.Errorf("json realm tag missing: %s", raw)
	}
}
