package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// sseNoUsage 与 sseOK 同形，但末帧不带 usage 子对象（上游未回报 usage 的口径）。
const sseNoUsage = "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"你好\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: [DONE]\n\n"

// TestChatLogsStreamRowNoUsageShowsDash 流式末帧无 usage 时 tok 列必须显示 "-"。
// chatStat.toks 初值 -1 即「未观测到 usage」哨兵（见字段注释与 logChatRow 的
// toks>=0 分支），不得被 stats.Tokens() 在无 usage 时返回的零值 0 覆盖成
// 「测得 0 token」。非流式同场景走 completionTokens 显式返回 -1 保留了哨兵，
// 两条路径口径必须一致。回移自上游 b1f7bc8。
func TestChatLogsStreamRowNoUsageShowsDash(t *testing.T) {
	withChatLog(t)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseNoUsage, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	out := captureStdout(t, func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("code=%d", rec.Code)
		}
	})
	if !strings.Contains(out, "tok=-") {
		t.Errorf("流式无 usage 应显示 tok=-（未观测哨兵），实际:\n%s", out)
	}
	if strings.Contains(out, "tok=0") {
		t.Errorf("流式无 usage 被伪造成 tok=0（伪装成测得 0 token）:\n%s", out)
	}
}
