package upstream

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"workbuddy2api-trae/trae/auth"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
	}{
		{200, `{"code":1005,"message":"plan limit","extra":{"plan":2}}`, ErrPlanLimit},
		{200, `{"code":1005,"msg":"权益不足"}`, ErrPlanLimit},
		{429, ``, ErrSoftRate},
		{401, `{"code":1001,"msg":"login required"}`, ErrSessionDead},
		{401, ``, ErrSessionDead},
		{404, ``, ErrNotFound},
		{500, `boom`, ErrServer},
		{503, `unavailable`, ErrServer},
		{400, `{"code":11101,"msg":"bad param"}`, ErrClient},
		{200, `{"checked_in":false}`, ErrNone},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d,%q)=%v want %v", c.status, c.body, got, c.want)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testClient(fn rtFunc) *Client {
	// 底 Edition 用国内版：这些用例覆盖签到/积分链路，零值 Edition 会把它们判成国际版跳过。
	ed := EditionCN()
	ed.AgentHost = "https://agent.example"
	ed.UgHost = "https://ug.example"
	ed.OAuthHost = "https://oauth.example"
	return &Client{
		HTTP: &http.Client{Transport: fn},
		Ed:   ed,
	}
}

func TestRefreshTokenExchange(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, EpExchange) {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			return nil, errors.New("missing content-type")
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"ClientID":"en1oxy7wnw8j9n"`)) || !bytes.Contains(body, []byte(`"RefreshToken":"oldrt"`)) {
			return nil, errors.New("bad body: " + string(body))
		}
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786805537,"TokenExpireDuration":1209600}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "oldrt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Errorf("tokens not updated: %+v", a)
	}
	if a.ExpiresAt != 1786805537 {
		t.Errorf("expiresAt=%d", a.ExpiresAt)
	}
}

// TestRefreshTokenExchangeMilliseconds 覆盖上游 TokenExpireAt 返回毫秒的场景：
// 必须归一化为 Unix 秒后再写 auth.ExpiresAt。
func TestRefreshTokenExchangeMilliseconds(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141,"TokenExpireDuration":1209600}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "oldrt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if a.ExpiresAt != 1786847930 {
		t.Errorf("expiresAt=%d want 1786847930 (毫秒转秒)", a.ExpiresAt)
	}
}

func TestRefreshTokenIfNeededSkipsFresh(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999, ApiHost: "https://oauth.example"}
	refreshed, err := c.RefreshTokenIfNeeded(a, 24*3600*1e9)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Error("fresh token should not refresh")
	}
	if calls != 0 {
		t.Errorf("ExchangeToken should not be called, calls=%d", calls)
	}
	if a.AccessToken != "at" {
		t.Error("token should remain unchanged")
	}
}

func TestRefreshTokenIfNeededRefreshesExpired(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	refreshed, err := c.RefreshTokenIfNeeded(a, 24*3600*1e9)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed || calls != 1 {
		t.Errorf("expired token should refresh once, refreshed=%v calls=%d", refreshed, calls)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Errorf("tokens not updated: %+v", a)
	}
}

func TestRefreshTokenUsesAuthApiHost(t *testing.T) {
	var gotHost string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotHost = r.URL.Scheme + "://" + r.URL.Host
		return jsonResp(200, `{"Result":{"Token":"newat"}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://custom.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatal(err)
	}
	if gotHost != "https://custom.example" {
		t.Errorf("host=%s want auth.apiHost", gotHost)
	}
}

func TestChatStreamSendsHeadersAndRewritesBody(t *testing.T) {
	var gotAuth, gotUID, gotAppID, gotIdeVer string
	var gotBody []byte
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		gotUID = r.Header.Get("X-Uid")
		gotAppID = r.Header.Get("X-App-Id")
		gotIdeVer = r.Header.Get("X-Ide-Version")
		gotBody, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event:done\ndata:{\"finish_reason\":\"stop\"}\n\n")),
		}, nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1", MachineID: "m1", DeviceID: "d1"}
	rc, status, respBody, err := c.ChatStream(a, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	if respBody != nil {
		t.Errorf("200 response should carry nil body, got %q", respBody)
	}
	rc.Close()
	if gotAuth != "Cloud-IDE-JWT at" || gotUID != "u1" {
		t.Errorf("headers: auth=%q uid=%q", gotAuth, gotUID)
	}
	if gotAppID != AppID || gotIdeVer != IdeVersion {
		t.Errorf("app headers: appid=%q idever=%q", gotAppID, gotIdeVer)
	}
	if !bytes.Contains(gotBody, []byte(`"stream":true`)) || !bytes.Contains(gotBody, []byte(`"function":"solo_work_lite"`)) {
		t.Errorf("body not rewritten: %s", gotBody)
	}
}

func TestChatStreamUsesDedicatedStreamClient(t *testing.T) {
	// StreamHTTP 优先于 HTTP 被 ChatStream 使用（无总超时的长 SSE 流客户端）。
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("event:done\ndata:{\"finish_reason\":\"stop\"}\n\n")),
		}, nil
	})
	c.StreamHTTP = &http.Client{Transport: c.HTTP.Transport} // 无 Timeout
	rc, status, _, err := c.ChatStream(&auth.Auth{AccessToken: "at", UID: "u1"}, []byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil || status != 200 {
		t.Fatalf("chat: status=%d err=%v", status, err)
	}
	rc.Close()
	if c.StreamHTTP.Timeout != 0 {
		t.Errorf("stream client should have no total timeout, got %v", c.StreamHTTP.Timeout)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return jsonResp(429, `rate limited`), nil
	})
	a := &auth.Auth{AccessToken: "at", UID: "u1"}
	_, status, respBody, err := c.ChatStream(a, []byte(`{}`))
	if status != 429 {
		t.Errorf("status=%d", status)
	}
	if err != nil {
		t.Fatalf("429 should come via status, err=%v", err)
	}
	if Classify(status, string(respBody)) != ErrSoftRate {
		t.Errorf("not classified soft rate: %q", respBody)
	}
}

func TestUserEntUsageAggregation(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, EpEntUsage) {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Cloud-IDE-JWT at" {
			return nil, errors.New("missing auth header")
		}
		return jsonResp(200, `{"is_credits_billing":true,"user_entitlement_pack_list":[
			{"entitlement_base_info":{"quota":{"credits_limit":2000}}},
			{"entitlement_base_info":{"quota":{"credits_limit":500}}}
		]}`), nil
	})
	remain, err := c.UserEntUsage(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatalf("ent usage: %v", err)
	}
	if remain != 2500 {
		t.Errorf("remain=%d want 2500", remain)
	}
}

func TestCheckinStatusAndClaim(t *testing.T) {
	var path string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		if r.Header.Get("X-User-Region") != "CN" {
			return nil, errors.New("missing X-User-Region")
		}
		return jsonResp(200, `{"checked_in":false,"credits":200,"enable":true}`), nil
	})
	checkedIn, credits, enable, err := c.CheckinStatus(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatal(err)
	}
	if checkedIn || !enable || credits != 200 {
		t.Errorf("status: checked=%v enable=%v credits=%d", checkedIn, enable, credits)
	}
	if path != EpCheckinStatus {
		t.Errorf("path=%s", path)
	}
}

// modelsFixture 是一份 get_detail_param 的真实形态裁剪版：两条内置模型（config_source=1，
// 带 display_contact_config 倍率串）、两条本地配置模型（config_source=3 + is_custom_model，
// 就是被人工加回上游目录的网关别名）、一条上游没给倍率的内置模板。
const modelsFixture = `{"config_info_list":[
 {"config_name":"Doubao-Seed-Evolving","config_source":1,
  "display_config":{"display_name":"Seed-Evolving","is_custom_model":false},
  "display_contact_config":"{\"consumption_rate\":{\"enable\":true,\"data\":{\"rate\":0.08}}}"},
 {"config_name":"glm-5.3","config_source":1,
  "display_config":{"display_name":"GLM-5.3","is_custom_model":false},
  "display_contact_config":"{\"consumption_rate\":{\"enable\":true,\"data\":{\"rate\":0.78}}}"},
 {"config_name":"no22","config_source":3,
  "display_config":{"display_name":"no22","is_custom_model":true}},
 {"config_name":"gateway-auto","config_source":3,
  "display_config":{"display_name":"gateway-auto","is_custom_model":true}},
 {"config_name":"custom_model_gemini","config_source":1,
  "display_config":{"display_name":"","is_custom_model":false}}
]}`

func TestCreditsFromContact(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"", ""},
		{"{}", ""},
		{`{"consumption_rate":{"enable":true,"data":{}}}`, ""},
		{`{"consumption_rate":{"enable":true,"data":{"rate":0.08}}}`, "x0.08"},
		{`{"consumption_rate":{"data":{"rate":0.2}}}`, "x0.2"},
		{`{"consumption_rate":{"data":{"rate":1}}}`, "x1"},
		// 上游写了 0 是「免费」，必须与「没这个字段」区分开：前者出 "x0"，后者出空串。
		{`{"consumption_rate":{"data":{"rate":0}}}`, "x0"},
		{"not json", ""},
	}
	for _, c := range cases {
		if got := creditsFromContact(c.raw); got != c.want {
			t.Errorf("creditsFromContact(%q)=%q want %q", c.raw, got, c.want)
		}
	}
}

func TestFetchModelsScopeHidesLocalModelsAndReadsCredits(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) { return jsonResp(200, modelsFixture), nil })
	infos, err := c.fetchModelsScope(&auth.Auth{AccessToken: "at", UID: "u1"}, FunctionAgent)
	if err != nil {
		t.Fatalf("fetchModelsScope: %v", err)
	}
	got := map[string]string{}
	for _, mi := range infos {
		got[mi.ID] = mi.Credits
	}
	// 本地配置模型是「网关别名被人工加进 IDE」后回流的条目，列出去会让 trae-<别名> 指回网关自身。
	for _, hidden := range []string{"no22", "gateway-auto"} {
		if _, ok := got[hidden]; ok {
			t.Errorf("本地配置模型 %s 没被过滤掉：它会指回本网关自身形成回环", hidden)
		}
	}
	if len(infos) != 3 {
		t.Fatalf("保留 %d 条，want 3（两条内置 + 一条无倍率的内置模板）: %v", len(infos), got)
	}
	if got["Doubao-Seed-Evolving"] != "x0.08" {
		t.Errorf("credits=%q want x0.08", got["Doubao-Seed-Evolving"])
	}
	if got["glm-5.3"] != "x0.78" {
		t.Errorf("credits=%q want x0.78", got["glm-5.3"])
	}
	if got["custom_model_gemini"] != "" {
		t.Errorf("上游没给倍率时应留空（未知不是 0），得到 %q", got["custom_model_gemini"])
	}
}

// TestFetchModelsScopeKeepsLocalModelsOutOfBothScopes 守住两个作用域都过滤：
// 只改一个作用域时，另一个仍会把网关别名漏出去。
func TestFetchModelsScopeKeepsLocalModelsOutOfBothScopes(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) { return jsonResp(200, modelsFixture), nil })
	for _, fn := range []string{Function, FunctionAgent} {
		infos, err := c.fetchModelsScope(&auth.Auth{AccessToken: "at"}, fn)
		if err != nil {
			t.Fatalf("scope %s: %v", fn, err)
		}
		for _, mi := range infos {
			if strings.HasPrefix(mi.ID, "no") || mi.ID == "gateway-auto" {
				t.Errorf("scope %s 漏出本地配置模型 %s", fn, mi.ID)
			}
		}
	}
}

// TestRefreshTokenFallsBackToAltClientID 守住「凭据类别与区域无关」这条实测结论：
// 国际版桌面客户端签发的 refreshToken 属于 TRAE 类 client_id，
// 用 SOLO 类刷新会得到 10101 "refresh token is not matched to the client"。
// 刷新链必须自动切到备用类别并记住，否则整条通道的 token 一旦过期就永久不可用。
func TestRefreshTokenFallsBackToAltClientID(t *testing.T) {
	var seen []string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case bytes.Contains(body, []byte(`"ClientID":"`+ClientIDSOLO+`"`)):
			seen = append(seen, ClientIDSOLO)
			return jsonResp(400, `{"ResponseMetadata":{"Error":{"Code":"10101","Data":{"__Message.error":"refresh token is not matched to the client"},"StandardCode":"040004"}}}`), nil
		case bytes.Contains(body, []byte(`"ClientID":"`+ClientIDTrae+`"`)):
			seen = append(seen, ClientIDTrae)
			return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
		}
		return nil, errors.New("unexpected client id: " + string(body))
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh 应回退到备用 client_id 成功: %v", err)
	}
	if len(seen) != 2 || seen[0] != ClientIDSOLO || seen[1] != ClientIDTrae {
		t.Errorf("尝试顺序 = %v，want [%s %s]", seen, ClientIDSOLO, ClientIDTrae)
	}
	if a.ClientID != ClientIDTrae {
		t.Errorf("a.ClientID=%q，want 记住 %q（否则每次刷新都要重试一轮）", a.ClientID, ClientIDTrae)
	}
	if a.AccessToken != "newat" || a.RefreshToken != "newrt" {
		t.Errorf("token 未更新: %+v", a)
	}
}

// TestRefreshTokenUsesRememberedClientID 守住「记住后不再试错」：
// 凭据已存 clientId 时必须直接命中，一次请求搞定。
func TestRefreshTokenUsesRememberedClientID(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"ClientID":"`+ClientIDTrae+`"`)) {
			return jsonResp(400, `{"ResponseMetadata":{"Error":{"Code":"10101","Data":{"__Message.error":"refresh token is not matched to the client"}}}}`), nil
		}
		return jsonResp(200, `{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786847930141}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://oauth.example", ClientID: ClientIDTrae}
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if calls != 1 {
		t.Errorf("ExchangeToken 调了 %d 次，want 1（记住的类别应直接命中）", calls)
	}
}

// TestRefreshTokenStopsOnNonMismatchError 守住「不把别的 4xx 当成类别不匹配」：
// token 真失效时换 client_id 也没有意义，只会多打一次上游。
func TestRefreshTokenStopsOnNonMismatchError(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(401, `{"ResponseMetadata":{"Error":{"Code":"20101","Data":{"__Message.error":"refresh token is invalid"}}}}`), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1, ApiHost: "https://oauth.example"}
	if err := c.RefreshToken(a); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Errorf("ExchangeToken 调了 %d 次，want 1（非类别不匹配不该重试）", calls)
	}
	if a.ClientID != "" {
		t.Errorf("失败时不应写入 ClientID，得到 %q", a.ClientID)
	}
}
