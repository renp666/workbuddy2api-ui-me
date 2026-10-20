package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// 灰度渠道校验（workbuddy-manager issue #193/#201 移植）handler 侧回归。

// TestApplyErrorPolicyConversationIDMissingCools 上游实测形态 HTTP 400 + code 11-128
// （code 值没加引号的非法 JSON）。出站已补会话头族（session.ConversationIDForHeaders），
// 仍撞上说明该账号被灰度到了别的口径——这时**不能**像以前那样归到通用 4xx「只换号
// 不罚号」：那会让坏号留在池里"显示健康、每次被选中都失败"。也不硬禁用：灰度随时
// 可能放开，禁用会让用户白重新登录。
func TestApplyErrorPolicyConversationIDMissingCools(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 1000)
	h := NewHandler(Config{Pool: p, SoftCooldown: 600 * time.Second})

	// 上游实测形态：HTTP 400 + code 11-128，且 code 的值**没加引号**（非法 JSON）。
	// 理由里的码必须照实取到——这条用例同时钉住 CodeOf 对非法 JSON 的容忍。
	h.applyErrorPolicy("u1", upstream.ErrAccountFault,
		`{"code":11-128,"msg":"conversation id missing","requestId":"x","displayMsg":{"zh":"请求被拦截，请重新发送"}}`,
		"glm-5.2")
	st, _ := p.Status("u1")
	if st.Disabled {
		t.Fatalf("渠道校验不应硬禁用（灰度可能放开，禁用等于让用户白重登）: %+v", st)
	}
	if !st.Cooling || st.CoolKind != "soft_rate" {
		t.Fatalf("应 soft_rate 软冷却（挪出池子但可自愈）: %+v", st)
	}
	if !strings.Contains(st.Reason, "11-128") {
		t.Errorf("冷却理由应带真实码，否则排障按错码去搜: %q", st.Reason)
	}
	if strings.Contains(st.Reason, "11140") {
		t.Errorf("理由里不该出现写死的 11140（issue #201 缺陷 1）: %q", st.Reason)
	}
}

// TestCodeOfToleratesIllegalJSON CodeOf 直接单测：`"code":11-128`（值没加引号的非法
// JSON，json.Unmarshal 解不了）也要取到码；常规数字码与引号形态同样覆盖。
func TestCodeOfToleratesIllegalJSON(t *testing.T) {
	cases := map[string]string{
		`{"code":11-128,"msg":"conversation id missing"}`: "11-128", // 非法 JSON（灰度实测）
		`{"code":11140,"msg":"request illegal"}`:          "11140",
		`{"error":{"data":{"code":14017,"msg":"trial not activated"}}}`: "14017",
		`{"code": "6004"}`:                     "6004", // 引号形态
		`{"code":  6004 }`:                     "6004", // 空格容差
		`{"msg":"no code here"}`:               "",     // 无码
		`not json at all`:                      "",
	}
	for body, want := range cases {
		if got := upstream.CodeOf(body); got != want {
			t.Errorf("CodeOf(%q) = %q, want %q", body, got, want)
		}
	}
}

// TestChatConversationIDDerivedWhenClientOmitsIt 端到端回归（灰度移植）：OpenAI 兼容
// 客户端的请求体里没有 conversationId 也没有 metadata 键 → 出站仍必须带
// X-Conversation-ID（同会话稳定值），否则被灰度渠道校验命中的账号每次出站都判 11-128。
func TestChatConversationIDDerivedWhenClientOmitsIt(t *testing.T) {
	var sawConvID, sawConvReqID string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	// 在 transport 里截获出站头（newFakeUpstream 的 behavior 只给 authz，这里
	// 复用其 client 再包一层）。
	up.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawConvID = r.Header.Get("X-Conversation-ID")
		sawConvReqID = r.Header.Get("X-Conversation-Request-ID")
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sseOK)),
		}, nil
	})}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at-u1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"cn:glm-5.2","messages":[{"role":"user","content":"你好"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sawConvID == "" {
		t.Fatal("客户端没给 conversationId 时出站也必须带 X-Conversation-ID（灰度渠道校验必查头）")
	}
	if len(sawConvID) != 32 {
		t.Errorf("派生 conversationID 应为 32 hex，got %q", sawConvID)
	}
	if sawConvReqID == "" {
		t.Error("X-Conversation-Request-ID 聚合主键必须必发")
	}
}

// TestChatConversationIDPassthroughClientValue 客户端给了 conversationId 就透传原值
// （派生值会让后台按对话聚合的键与客户端自己记的对不上）。
func TestChatConversationIDPassthroughClientValue(t *testing.T) {
	var sawConvID string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	up.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawConvID = r.Header.Get("X-Conversation-ID")
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sseOK)),
		}, nil
	})}
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at-u1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"cn:glm-5.2","conversationId":"client-conv-9","messages":[{"role":"user","content":"你好"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sawConvID != "client-conv-9" {
		t.Fatalf("客户端 conversationId 应原值透传，got %q", sawConvID)
	}
}

// TestChatTransportFailureDegradesAfterThreshold 连败降权端到端（灰度移植 issue #114）：
// 传输层失败此前只换号——单号池里同一坏号"显示健康、每次被选中都失败"，请求 5 连败
// 后该号临时出池，后续请求直接 503（不再反复撞死号刷日志/刷风控）。
func TestChatTransportFailureDegradesAfterThreshold(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 0, "", false // behavior 不被走到：transport 层直接报错
	})
	up.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF // 传输层失败（不罚号，只换号）
	})}
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at-u1", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000)
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 1})

	body := `{"model":"cn:glm-5.2","messages":[{"role":"user","content":"你好"}]}`
	// 前几次：503 但账号仍在池（连败未达阈）。
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("第 %d 次：code=%d（传输层失败应 503）", i+1, rec.Code)
		}
		if p.Pick("") == nil {
			t.Fatalf("第 %d 次失败不应提前降权", i+1)
		}
	}
	// 第 5 次（默认阈值 5）失败后：账号临时出池。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("第 5 次：code=%d", rec.Code)
	}
	if st, ok := p.Status("u1"); !ok || st.DegradeUntil.IsZero() {
		t.Fatalf("连败达阈后应降权出池: %+v ok=%v", st, ok)
	}
	if p.Pick("") != nil {
		t.Fatal("降权中的号不应再被选中")
	}
}
