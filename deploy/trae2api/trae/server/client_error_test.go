// client_error_test.go 上游 4xx（请求体自身不合法）的处理不变量：不惩罚账号。
package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workbuddy2api-trae/trae/auth"
	"workbuddy2api-trae/trae/upstream"
)

// countingUpstream 只统计**对话**调用被打了几次，用来证明 400 没有触发换号重试。
// 必须按路径过滤：mapModel 会先经 fetchDynamicModels 打一次 get_detail_param，
// 把它一起数就会得到 2 次，判据变成"看起来红了但其实没错"。
func countingUpstream(t *testing.T, status int, body string, calls *int32) *upstream.Client {
	t.Helper()
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "/llm_utils_chat") {
				atomic.AddInt32(calls, 1)
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
		Ed: upstream.EditionCN(),
	}
}

func TestUpstreamClientErrorDoesNotCoolAccount(t *testing.T) {
	// 真实触发路径：调用方发来畸形 JSON，上游按 payload 校验回 400。
	// 旧行为把它计入"账号连续错误"并重试到 MaxRotate，最后回一条误导性的
	// 503「all accounts unavailable」——惩罚落在了账号头上，而不是请求方。
	var calls int32
	up := countingUpstream(t, http.StatusBadRequest,
		`{"code":4001,"message":"bad request: invalid character ']' after object key:value pair"}`, &calls)

	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 3})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("http = %d, want 400（客户端请求体问题不该被升格成 5xx）: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "4001") {
		t.Errorf("上游原话没带出来，调用方无从自查: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("上游被打 %d 次, want 1（对 400 换账号重试没有意义）", got)
	}
	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("账号不在池里")
	}
	if st.Cooling || st.Disabled || st.ErrCount != 0 {
		t.Errorf("账号状态被动了：cooling=%v disabled=%v err_count=%d", st.Cooling, st.Disabled, st.ErrCount)
	}
}

// TestUpstreamServerErrorStillCounts 是对照组：上面那条短路不能顺手把真正的
// 服务端故障也变成"不计错误"，否则账号持续 5xx 时就没有冷却保护了。
func TestUpstreamServerErrorStillCounts(t *testing.T) {
	var calls int32
	up := countingUpstream(t, http.StatusInternalServerError, `{"code":5000,"message":"upstream boom"}`, &calls)
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 2, ErrThreshold: 1, ErrCooldown: 6e10})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusBadRequest {
		t.Errorf("5xx 被当成 400 回了：%s", rec.Body.String())
	}
	st, _ := p.Status("u1")
	if !st.Cooling {
		t.Errorf("持续 5xx 应进入冷却保护，实际 cooling=%v err_count=%d", st.Cooling, st.ErrCount)
	}
	if got := atomic.LoadInt32(&calls); got < 1 {
		t.Errorf("上游调用次数 = %d, want ≥1", got)
	}
}
