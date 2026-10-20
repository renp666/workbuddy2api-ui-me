package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// —— 多 API Key 归因（对标项 #5）——
// 锁定三层语义：清单空=现状透传（零回归）、清单非空=console 权威+换发+标签贯穿、
// 清单持久化与降级。

var gateMainKey = strings.Repeat("p", 32) // 与 testConfig 的 APIKey 一致

func gateExtraKey() string { return "extra-key-0123456789" }

// TestAPIKeyEntryValidation 锁定清单条目校验：标签/key 的形态与「默认」保留。
func TestAPIKeyEntryValidation(t *testing.T) {
	cases := []struct {
		name    string
		entry   apiKeyEntry
		wantErr bool
	}{
		{"合法条目", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Enabled: true}, false},
		{"标签为空", apiKeyEntry{Label: "", Key: gateExtraKey()}, true},
		{"标签超64字节", apiKeyEntry{Label: strings.Repeat("l", 65), Key: gateExtraKey()}, true},
		{"标签含空格", apiKeyEntry{Label: "has space", Key: gateExtraKey()}, true},
		{"标签非ASCII", apiKeyEntry{Label: "标签", Key: gateExtraKey()}, true},
		{"默认保留", apiKeyEntry{Label: defaultKeyLabel, Key: gateExtraKey()}, true},
		{"key过短", apiKeyEntry{Label: "ci-bot", Key: "short-key-15x"}, true},
		{"key超128", apiKeyEntry{Label: "ci-bot", Key: strings.Repeat("k", 129)}, true},
		{"key含空格", apiKeyEntry{Label: "ci-bot", Key: "key with space 0123"}, true},
	}
	for _, tc := range cases {
		err := validateAPIKeyEntry(tc.entry)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

// TestAPIKeyStoreReplaceReloadAndLimits：replace 校验（重复标签/超上限）→ 落盘
// → 重开加载一致；损坏文件按空清单降级，主 Key 不受影响。
func TestAPIKeyStoreReplaceReloadAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apikeys.json")
	s := newAPIKeyStore(path)
	if len(s.list()) != 0 {
		t.Fatal("缺文件应为空清单")
	}
	keys := []apiKeyEntry{
		{Label: "ci-bot", Key: gateExtraKey(), Enabled: true, CreatedAt: 111},
		{Label: "parked", Key: "parked-key-012345678", Enabled: false, CreatedAt: 222},
	}
	if err := s.replace(keys); err != nil {
		t.Fatal(err)
	}
	if err := s.replace([]apiKeyEntry{
		{Label: "dup", Key: gateExtraKey()},
		{Label: "dup", Key: "another-key-01234567"},
	}); err == nil {
		t.Fatal("重复标签应被拒绝")
	}
	tooMany := make([]apiKeyEntry, 0, maxAPIKeys+1)
	for i := 0; i <= maxAPIKeys; i++ {
		tooMany = append(tooMany, apiKeyEntry{Label: "k" + string(rune('a'+i)), Key: gateExtraKey()})
	}
	if err := s.replace(tooMany); err == nil {
		t.Fatal("超上限应被拒绝")
	}
	// 两次失败 replace 不得破坏已落盘清单。
	reloaded := newAPIKeyStore(path)
	got := reloaded.list()
	if len(got) != 2 || got[0].Label != "ci-bot" || got[0].Key != gateExtraKey() || !got[0].Enabled || got[0].CreatedAt != 111 || got[1].Label != "parked" || got[1].Enabled {
		t.Fatalf("重载清单不符：%+v", got)
	}
	// 损坏降级：清单清空（fail-closed），但主 Key 匹配照常。
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := newAPIKeyStore(path)
	if len(broken.list()) != 0 {
		t.Fatal("损坏文件应按空清单降级")
	}
	if label, ok := broken.match("Bearer "+gateMainKey, gateMainKey); !ok || label != defaultKeyLabel {
		t.Fatalf("主 Key 匹配应不受影响：label=%q ok=%v", label, ok)
	}
	if _, ok := broken.match("Bearer "+gateExtraKey(), gateMainKey); ok {
		t.Fatal("降级后附加 Key 不应命中")
	}
}

// TestAPIKeyGateEmptyListKeepsPassthrough 锁定现状语义：未配置附加 Key 时公共
// 出口不做 console 侧校验，客户端凭据原样透传（core withAuth 照旧权威）。
func TestAPIKeyGateEmptyListKeepsPassthrough(t *testing.T) {
	var coreAuth string
	h, _, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		coreAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	rec := postChat(h, "/v1/chat/completions", chatBody("cn:test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("清单空时假 Key 也应直通（现状语义）：status=%d body=%s", rec.Code, rec.Body)
	}
	if coreAuth != "Bearer client-key" {
		t.Fatalf("凭据应原样透传，core 收到 %q", coreAuth)
	}
}

// TestAPIKeyGateEnforcesWhenListed 锁定权威语义：清单非空后——主 Key 命中记
// 「默认」、附加 Key 命中换发主 Key 并注入标签、未命中自产 401 不触达 core、
// 401 与失败样本进错误账本且带标签。
func TestAPIKeyGateEnforcesWhenListed(t *testing.T) {
	var mu sync.Mutex
	coreHits := 0
	var coreAuth, coreLabel string
	h, srv, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		coreHits++
		coreAuth = r.Header.Get("Authorization")
		coreLabel = r.Header.Get("X-Console-Key-Label")
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer fail-please" {
			// 不会发生：core 只会收到换发后的主 Key；此分支仅防意外。
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	if err := srv.apiKeys.replace([]apiKeyEntry{{Label: "ci-bot", Key: gateExtraKey(), Enabled: true, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}

	doChat := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", strings.NewReader(chatBody("cn:test")))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 主 Key：命中「默认」，core 收到换发后的主 Key 与标签。
	rec := doChat(gateMainKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("主 Key 应命中：status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	authForMain, labelForMain := coreAuth, coreLabel
	mu.Unlock()
	if authForMain != "Bearer "+gateMainKey || labelForMain != defaultKeyLabel {
		t.Fatalf("主 Key 换发不符：core auth=%q label=%q", authForMain, labelForMain)
	}

	// 附加 Key：命中标签 ci-bot，core 收到主 Key（附加 Key 明文不出 console）。
	rec = doChat(gateExtraKey())
	if rec.Code != http.StatusOK {
		t.Fatalf("附加 Key 应命中：status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	authForExtra, labelForExtra := coreAuth, coreLabel
	mu.Unlock()
	if authForExtra != "Bearer "+gateMainKey || labelForExtra != "ci-bot" {
		t.Fatalf("附加 Key 换发不符：core auth=%q label=%q", authForExtra, labelForExtra)
	}

	// 未命中：console 自产 401（OpenAI 错误形态），core 零触达。
	mu.Lock()
	before := coreHits
	mu.Unlock()
	rec = doChat("totally-wrong-key")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_api_key") {
		t.Fatalf("未命中应 401：status=%d body=%s", rec.Code, rec.Body)
	}
	rec = doChat("")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401：status=%d", rec.Code)
	}
	mu.Lock()
	hitsAfterDeny := coreHits
	mu.Unlock()
	if hitsAfterDeny != before {
		t.Fatalf("401 不应触达 core：hits=%d want=%d", hitsAfterDeny, before)
	}
	// 401 也带 X-Request-Id（observe 在 gate 外层）。
	if !hex32.MatchString(rec.Header().Get("X-Request-Id")) {
		t.Fatalf("401 应带 X-Request-Id：%q", rec.Header().Get("X-Request-Id"))
	}

	// 失败样本进错误账本且带 key_label（用附加 Key 打一个 core 500）。
	mu.Lock()
	coreHits = 0
	mu.Unlock()
	hfail, srvf, dirf := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	if err := srvf.apiKeys.replace([]apiKeyEntry{{Label: "ci-bot", Key: gateExtraKey(), Enabled: true, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", strings.NewReader(chatBody("cn:test")))
	r.Header.Set("Authorization", "Bearer "+gateExtraKey())
	w := httptest.NewRecorder()
	hfail.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("应透传 500：status=%d", w.Code)
	}
	waitLedgerIdle(t)
	events := readErrorRawLines(t, dirf)
	found := false
	for _, row := range events {
		if row["key_label"] == "ci-bot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误账本应带 key_label=ci-bot：%v", events)
	}
}

// TestAPIKeyGateOnStatusAndModels 锁定：/status 与 /v1/models 同受 gate 约束
//（清单非空时）；/healthz 保持免鉴权。/v1/models 走 mergedModels 直发 core
//（不经公共代理 Rewrite），附加 Key 必须在此换发主 Key——mock 校验 Authorization，
// 换发缺失时真实 core 的 401 会被 mock 的无条件 200 掩盖。
func TestAPIKeyGateOnStatusAndModels(t *testing.T) {
	var mu sync.Mutex
	var coreAuth string
	h, srv, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		coreAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	if err := srv.apiKeys.replace([]apiKeyEntry{{Label: "ci-bot", Key: gateExtraKey(), Enabled: true, CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
	get := func(path, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://console.test"+path, nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if rec := get("/status", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/status 无 Key 应 401：status=%d", rec.Code)
	}
	if rec := get("/status", gateExtraKey()); rec.Code != http.StatusOK {
		t.Fatalf("/status 附加 Key 应命中：status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := get("/v1/models", "totally-wrong-key"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/models 错 Key 应 401：status=%d", rec.Code)
	}
	if rec := get("/v1/models", gateExtraKey()); rec.Code != http.StatusOK {
		t.Fatalf("/v1/models 附加 Key 应命中：status=%d body=%s", rec.Code, rec.Body)
	}
	mu.Lock()
	gotAuth := coreAuth
	mu.Unlock()
	if gotAuth != "Bearer "+gateMainKey {
		t.Fatalf("/v1/models 附加 Key 应换发主 Key（mergedModels 直发不经 Rewrite）：core auth=%q", gotAuth)
	}
	if rec := get("/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("/healthz 应保持免鉴权：status=%d", rec.Code)
	}
}

// TestUsageCaptureCarriesKeyLabel 锁定旁路上报的标签来源：newUsageCapture 从
// 请求 ctx 取 gate 注入的标签，随 usage_report 落 core 账本。
func TestUsageCaptureCarriesKeyLabel(t *testing.T) {
	_, srv, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("POST", "http://console.test/v1/chat/completions", nil)
	if got := srv.newUsageCapture(httptest.NewRecorder(), r, "m", "zcode", false).entry.KeyLabel; got != "" {
		t.Fatalf("无标签 ctx 应留空：%q", got)
	}
	r = r.WithContext(withKeyLabel(r.Context(), "ci-bot"))
	if got := srv.newUsageCapture(httptest.NewRecorder(), r, "m", "zcode", false).entry.KeyLabel; got != "ci-bot" {
		t.Fatalf("ctx 标签应进上报条目：%q", got)
	}
}
