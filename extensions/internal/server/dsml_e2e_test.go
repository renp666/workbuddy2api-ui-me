// dsml_e2e_test.go 补丁 0016 回归：正文工具调用标记（DSML）还原的 handler 端到端语义——
// 非流式转换 + finish_reason 改写、流式合成 delta.tool_calls、tools 名单严格判定、
// 修复器禁用零改动（MarkupAllowUnknown=false 且请求无 tools）。
package server

import (
	"encoding/json"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// buildDSMLSync 构建非流式泄漏上游响应（上游恒为 SSE，Aggregate 聚合）：
// content 内嵌标记文本的 chunk 帧 + finish_reason=stop + [DONE]。
func buildDSMLSync(t *testing.T) string {
	t.Helper()
	markup := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"exec_command\"><｜｜DSML｜｜ parameter name=\"cmd\" string=\"true\">ls -la</｜｜DSML｜｜ parameter></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
	b, _ := json.Marshal("好的，我来执行命令。\n" + markup)
	return "data: {\"id\":\"chatcmpl-dsml\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(b) + "}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-dsml\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// buildDSMLStream 按需构建流式 fixture（content 里嵌入标记文本）。
func buildDSMLStream(t *testing.T) string {
	t.Helper()
	markup := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"exec_command\"><｜｜DSML｜｜ parameter name=\"cmd\" string=\"true\">ls -la</｜｜DSML｜｜ parameter></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
	mk := func(content string) string {
		b, _ := json.Marshal(content)
		return string(b)
	}
	return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"开始\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":" + mk(markup) + "}}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1753600000,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// TestDSMLNonStreamRepaired 非流式：泄漏标记还原为 message.tool_calls，
// finish_reason stop→tool_calls，正文只剩前缀。
func TestDSMLNonStreamRepaired(t *testing.T) {
	up := newFakeUpstream(t, func(auth string) (int, string, bool) {
		return 200, buildDSMLSync(t), false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	// MarkupAllowUnknown=false 但请求带 tools 名单（exec_command）→ 严格判定命中。
	h := NewHandler(Config{Pool: p, Upstream: up})
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"exec_command","parameters":{}}}]}`
	rec := chatRequest(h, body)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body)
	}
	c := resp.Choices[0]
	if c.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", c.FinishReason)
	}
	if strings.Contains(c.Message.Content, "DSML") {
		t.Fatalf("标记应从正文移除: %q", c.Message.Content)
	}
	if !strings.HasPrefix(c.Message.Content, "好的") {
		t.Fatalf("前缀正文应保留: %q", c.Message.Content)
	}
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].Function.Name != "exec_command" {
		t.Fatalf("tool_calls=%+v", c.Message.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(c.Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments 非法: %v", err)
	}
	if args["cmd"] != "ls -la" {
		t.Fatalf("args=%v", args)
	}
}

// TestDSMLNonStreamStrictReject 名单不含该工具 → 整块原文透出（不转换、不改 finish_reason）。
func TestDSMLNonStreamStrictReject(t *testing.T) {
	raw := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\\\"unknown_tool\\\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>\"}}]}\n\n" +
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(auth string) (int, string, bool) { return 200, raw, false })
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"other"}}]}`
	rec := chatRequest(h, body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "unknown_tool") {
		t.Fatalf("不认识的工具应原文透出: code=%d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"tool_calls"`) {
		t.Fatal("不应产生 tool_calls")
	}
}

// TestDSMLStreamSynthesizesToolCalls 流式：标记还原为追加的 delta.tool_calls 合成帧，
// 正文帧被改写（标记移除），stop→tool_calls。
func TestDSMLStreamSynthesizesToolCalls(t *testing.T) {
	raw := buildDSMLStream(t)
	up := newFakeUpstream(t, func(auth string) (int, string, bool) { return 200, raw, true })
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"exec_command"}}]}`
	rec := chatRequest(h, body)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	text := rec.Body.String()
	if strings.Contains(text, "DSML") {
		t.Fatalf("标记不应出现在流正文: %s", text)
	}
	// 收集全部 delta.tool_calls。
	var got []map[string]any
	sawFinish := ""
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "data: ") || strings.HasPrefix(ln, "data: [DONE]") {
			continue
		}
		var fr struct {
			Choices []struct {
				Delta struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "data: ")), &fr) != nil {
			continue
		}
		for _, ch := range fr.Choices {
			got = append(got, ch.Delta.ToolCalls...)
			if ch.FinishReason != nil && *ch.FinishReason != "" && *ch.FinishReason != "null" {
				sawFinish = *ch.FinishReason
			}
		}
	}
	if len(got) != 1 {
		t.Fatalf("合成 tool_calls=%d want 1: %s", len(got), text)
	}
	fn, _ := got[0]["function"].(map[string]any)
	if fn == nil || fn["name"] != "exec_command" {
		t.Fatalf("合成调用=%v", got[0])
	}
	if got[0]["index"] != float64(0) {
		t.Fatalf("index=%v want 0（流内无上游自带 tool_calls）", got[0]["index"])
	}
	if sawFinish != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", sawFinish)
	}
}

// TestDSMLDisabledZeroChange 修复器禁用（无 tools 名单 + MarkupAllowUnknown=false）：
// 泄漏标记原样透传，行为与补丁前完全一致。
func TestDSMLDisabledZeroChange(t *testing.T) {
	markup := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"exec_command\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
	mkb, _ := json.Marshal(markup)
	raw := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(mkb) + "}}]}\n\n" +
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(auth string) (int, string, bool) { return 200, raw, false })
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up}) // 默认 MarkupAllowUnknown=false
	rec := chatRequest(h, `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DSML") {
		t.Fatalf("禁用态应原样透传: code=%d body=%s", rec.Code, rec.Body)
	}
}

// TestDSMLHistoryNamesAllowlist 会话历史里出现过的工具名参与严格名单（tools 声明
// 缺失时经历史 tool_calls 恢复判定能力）。
func TestDSMLHistoryNamesAllowlist(t *testing.T) {
	markup := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"exec_command\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
	mkb, _ := json.Marshal(markup)
	raw := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(mkb) + "}}]}\n\n" +
		"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := newFakeUpstream(t, func(auth string) (int, string, bool) { return 200, raw, false })
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "alice", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	// 无 tools 字段，但 assistant 历史带 tool_calls → 名单命中 exec_command。
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"exec_command","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"ok"},{"role":"user","content":"go"}]}`
	rec := chatRequest(h, body)
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"tool_calls"`) || strings.Contains(rec.Body.String(), "DSML") {
		t.Fatalf("历史名单应命中并转换: %s", rec.Body)
	}
}
