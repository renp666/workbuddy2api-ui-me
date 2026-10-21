package main

import (
	"encoding/json"
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
		{"白名单缺省", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: nil}, false},
		{"白名单空数组", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{}}, false},
		{"白名单合法", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{"glm-*", "cn:good-model"}}, false},
		{"白名单非法通配符", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{"[a-"}}, true},
		{"白名单空条目", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{""}}, true},
		{"白名单条目超128字节", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{strings.Repeat("m", 129)}}, true},
		{"白名单条数超上限", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: make([]string, maxKeyModels+1)}, true},
		{"白名单重复条目", apiKeyEntry{Label: "ci-bot", Key: gateExtraKey(), Models: []string{"glm-*", "glm-*"}}, true},
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
	if entry, ok := broken.matchEntry("Bearer "+gateMainKey, gateMainKey); !ok || entry.Label != defaultKeyLabel {
		t.Fatalf("主 Key 匹配应不受影响：label=%q ok=%v", entry.Label, ok)
	}
	if _, ok := broken.matchEntry("Bearer "+gateExtraKey(), gateMainKey); ok {
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

// TestKeyModelWhitelistMatching 锁定白名单匹配矩阵：空=不限、原名/剥前缀后的裸名
// 都试 pattern、通配符、循环剥离。前缀清单与 keyModelPrefixes 同源，新增旁路前缀
// 时本测试会提醒同步矩阵。
func TestKeyModelWhitelistMatching(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		model    string
		want     bool
	}{
		{"无白名单=不限", nil, "anything", true},
		{"无白名单=不限(空数组)", []string{}, "anything", true},
		{"原名精确命中", []string{"cn:good-model"}, "cn:good-model", true},
		{"原名不命中", []string{"cn:good-model"}, "cn:other-model", false},
		{"裸名命中（单前缀）", []string{"glm-5.3-flash"}, "cn:glm-5.3-flash", true},
		{"裸名命中（双前缀循环剥离）", []string{"glm-5.3-flash"}, "autoclaw-cn:glm-5.3-flash", true},
		{"通配符命中原名", []string{"autoclaw-*"}, "autoclaw-cn:glm-5.3-flash", true},
		{"通配符命中裸名", []string{"glm-*"}, "zcode-glm-4.6", true},
		{"通配符命中深层裸名", []string{"5.3-flash"}, "autoclaw-cn:glm-5.3-flash", true},
		{"区域前缀改写后命中", []string{"global:cn:glm-4.5"}, "global:cn:glm-4.5", true},
		{"剥到只剩裸名仍不命中", []string{"glm-4.5"}, "cn:glm-4.6", false},
		{"无前缀模型直接比", []string{"other-model"}, "other-model", true},
		{"大小写敏感", []string{"GLM-*"}, "glm-4.5", false},
		{"通配符问号", []string{"cn:glm-4.?"}, "cn:glm-4.5", true},
	}
	for _, tc := range cases {
		if got := modelAllowed(tc.patterns, tc.model); got != tc.want {
			t.Fatalf("%s: modelAllowed(%q, %q)=%v want %v", tc.name, tc.patterns, tc.model, got, tc.want)
		}
	}
}

// TestAPIKeyGateModelWhitelist 锁定白名单拦截语义（对标项 #16）：
// 受限 Key 请求白名单外模型 → 403 model_not_allowed（OpenAI 形态 + X-Request-Id）、
// 不触达 core、进错误账本（key_label 归属、auth 类）；命中白名单（含裸名/通配符）
// 与主 Key 不受影响；/v1/models 恒返回全量不裁剪；缺 model 字段与解析失败放行。
func TestAPIKeyGateModelWhitelist(t *testing.T) {
	var mu sync.Mutex
	coreHits := 0
	h, srv, dir := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		coreHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	if err := srv.apiKeys.replace([]apiKeyEntry{{
		Label: "wl", Key: gateExtraKey(), Enabled: true, CreatedAt: 1,
		Models: []string{"glm-*", "cn:good-model"},
	}}); err != nil {
		t.Fatal(err)
	}
	do := func(path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://console.test"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	coreHitsLocked := func() int {
		mu.Lock()
		defer mu.Unlock()
		return coreHits
	}

	// 白名单内：原名、裸名、通配符都放行，正常触达 core。
	for _, model := range []string{"cn:good-model", "zcode-glm-4.6", "autoclaw-cn:glm-x"} {
		before := coreHitsLocked()
		rec := do("/v1/chat/completions", gateExtraKey(), chatBody(model))
		if rec.Code != http.StatusOK {
			t.Fatalf("白名单内模型 %s 应放行：status=%d body=%s", model, rec.Code, rec.Body)
		}
		if hits := coreHitsLocked(); hits != before+1 {
			t.Fatalf("白名单内模型 %s 应触达 core：hits=%d before=%d", model, hits, before)
		}
	}

	// 白名单外：403 model_not_allowed，不触达 core，带 X-Request-Id。
	before := coreHitsLocked()
	rec := do("/v1/chat/completions", gateExtraKey(), chatBody("cn:other-model"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("白名单外模型应 403：status=%d body=%s", rec.Code, rec.Body)
	}
	var errBody struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("403 应为 JSON：%v", err)
	}
	if errBody.Error.Code != "model_not_allowed" || errBody.Error.Type != "invalid_request_error" ||
		!strings.Contains(errBody.Error.Message, "cn:other-model") {
		t.Fatalf("403 形态不符：%s", rec.Body.String())
	}
	if !hex32.MatchString(rec.Header().Get("X-Request-Id")) {
		t.Fatalf("403 应带 X-Request-Id：%q", rec.Header().Get("X-Request-Id"))
	}
	if hits := coreHitsLocked(); hits != before {
		t.Fatalf("403 不应触达 core：hits=%d want=%d", hits, before)
	}

	// 三个对话出口都受约束。
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		if rec := do(path, gateExtraKey(), chatBody("cn:other-model")); rec.Code != http.StatusForbidden {
			t.Fatalf("%s 白名单外模型应 403：status=%d body=%s", path, rec.Code, rec.Body)
		}
	}

	// 缺 model 字段与解析失败：放行（模型名校验归 core）。
	for _, body := range []string{`{"messages":[{"role":"user","content":"hi"}]}`, `{broken json`} {
		if rec := do("/v1/chat/completions", gateExtraKey(), body); rec.Code != http.StatusOK {
			t.Fatalf("无 model 字段/坏 body 应放行（%q）：status=%d body=%s", body, rec.Code, rec.Body)
		}
	}

	// 主 Key 无白名单：同一受限模型照常放行。
	if rec := do("/v1/chat/completions", gateMainKey, chatBody("cn:other-model")); rec.Code != http.StatusOK {
		t.Fatalf("主 Key 不受白名单约束：status=%d body=%s", rec.Code, rec.Body)
	}

	// /v1/models 恒全量（用户拍板不裁剪）：受限 Key 也 200。
	getModels := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://console.test/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if rec := getModels(gateExtraKey()); rec.Code != http.StatusOK {
		t.Fatalf("/v1/models 不应受白名单裁剪：status=%d body=%s", rec.Code, rec.Body)
	}

	// 错误账本留痕：403 落 auth 类、带 key_label 与 request_id、produced=false。
	waitLedgerIdle(t)
	found := false
	for _, row := range readErrorRawLines(t, dir) {
		if row["category"] == "auth" && row["key_label"] == "wl" {
			if id, _ := row["request_id"].(string); !hex32.MatchString(id) {
				t.Fatalf("403 账本行应带 request_id：%v", row)
			}
			if snippet, _ := row["error_snippet"].(string); !strings.Contains(snippet, "model_not_allowed") {
				t.Fatalf("403 账本行应带响应摘要：%v", row)
			}
			if produced, ok := row["produced"]; ok && produced == true {
				t.Fatalf("403 账本行不应是产出样本：%v", row)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("403 应进错误账本（category=auth key_label=wl）")
	}
}
