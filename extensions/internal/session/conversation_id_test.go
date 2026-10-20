package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// 会话头族的 conversationId（workbuddy-manager issue #193 灰度移植）：客户端给了就
// 透传；**没给要派生一个同会话稳定的值** —— 上游对部分账号灰度启用了渠道校验，
// 出站缺 X-Conversation-ID 即判 11-128「conversation id missing」，被灰度到的账号
// 每次被选中都失败。

func bodyWith(messages ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"messages": messages})
	return b
}

func userMsg(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

func TestConversationIDPrefersClientValue(t *testing.T) {
	body := bodyWith(
		map[string]any{"role": "system", "content": "sys"},
		userMsg("你好"),
	)
	// 顶层 conversationId 原值透传（不重新派生 —— 派生值会让后台按对话聚合的键
	// 与客户端自己记的对不上）。
	withField := func(key string, v any) []byte {
		var obj map[string]any
		_ = json.Unmarshal(body, &obj)
		obj[key] = v
		out, _ := json.Marshal(obj)
		return out
	}
	for _, key := range []string{"conversationId", "conversation_id"} {
		if got := ConversationIDForHeaders(withField(key, "client-conv-1"), "sess"); got != "client-conv-1" {
			t.Errorf("%s 没透传：got %q", key, got)
		}
	}
	// metadata 形态同样透传
	meta, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{userMsg("你好")},
		"metadata": map[string]any{"conversationId": "meta-conv"},
	})
	if got := ConversationIDForHeaders(meta, ""); got != "meta-conv" {
		t.Errorf("metadata.conversationId 没透传：got %q", got)
	}
}

func TestConversationIDStableAcrossTurns(t *testing.T) {
	// 同一会话的多轮：首条 user 消息不变、末尾消息在变 → 必须派生同一个值，
	// 否则后台会把一个会话拆成几十条（这正是「稳定」要保住的语义）。
	first := bodyWith(
		map[string]any{"role": "system", "content": "sys"},
		userMsg("帮我改这个函数"),
	)
	second := bodyWith(
		map[string]any{"role": "system", "content": "sys"},
		userMsg("帮我改这个函数"),
		map[string]any{"role": "assistant", "content": "改好了"},
		userMsg("再优化一下"),
	)
	a := ConversationIDForHeaders(first, "")
	b := ConversationIDForHeaders(second, "")
	if a == "" || a != b {
		t.Fatalf("同会话两轮派生不一致：%q vs %q", a, b)
	}
	for _, r := range a {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("派生值形态不是 32 hex（B3 TraceId 也用它）：%q", a)
			break
		}
	}
	if len(a) != 32 {
		t.Errorf("派生值长度应为 32，got %d：%q", len(a), a)
	}
}

func TestConversationIDDiffersAcrossConversations(t *testing.T) {
	a := ConversationIDForHeaders(bodyWith(userMsg("会话甲的第一句")), "")
	b := ConversationIDForHeaders(bodyWith(userMsg("会话乙的第一句")), "")
	if a == b {
		t.Errorf("不同会话派生出同一个值：%q —— 后台会把两条对话并成一条", a)
	}
}

func TestConversationIDFallbacks(t *testing.T) {
	// 无 user 文本（探测请求：只有 system）→ 回落会话键
	probe := bodyWith(
		map[string]any{"role": "system", "content": "You are a helpful assistant."},
		userMsg(""),
	)
	withSess := ConversationIDForHeaders(probe, "sess-key")
	again := ConversationIDForHeaders(probe, "sess-key")
	if withSess == "" || withSess != again {
		t.Errorf("无 user 文本时没有按会话键稳定派生：%q vs %q", withSess, again)
	}
	if withSess == ConversationIDForHeaders(probe, "another-sess") {
		t.Error("不同会话键派生出了同一个值")
	}
	// 什么都没有 → 仍要给出一个非空的 32 hex（头族必发，校验才不会 11-128）
	bare := ConversationIDForHeaders(nil, "")
	if len(bare) != 32 || strings.ContainsFunc(bare, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) {
		t.Errorf("空请求也要给出合法的 32 hex，got %q", bare)
	}
}

// TestContentSignatureBackwardCompatible 钉住轮键零漂移契约（灰度移植 issue #170）：
// 纯文本路径 contentSignature 必须与 contentText 结果**完全一致**——否则升级后同一
// 会话的轮级聚合键整体换值，存量对话轮在上游用量明细里断档一轮。
func TestContentSignatureBackwardCompatible(t *testing.T) {
	cases := []string{
		"你好，帮我改个函数",
		"multi\nline\ttext",
	}
	for _, text := range cases {
		raw, _ := json.Marshal(text)
		if got := contentSignature(raw); got != text {
			t.Errorf("纯文本签名必须等于原文（contentText 口径）：got %q want %q", got, text)
		}
	}
	// 数组形态全文本 part：与 contentText 拼接结果一致
	mixed, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "看这张图 "},
		{"type": "text", "text": "然后回答"},
	})
	if got := contentSignature(mixed); got != "看这张图 然后回答" {
		t.Errorf("全文本 part 数组签名应与 contentText 一致：got %q", got)
	}
}

// TestContentSignatureMultimodalStable 纯图片轮不再碎片化（灰度移植 issue #170）：
// 同一张图（同 parts 内容）→ 同签名；不同图 → 不同签名；图片内容入键的是
// sha256 短摘要而非 base64 原文（超长 data: URL 不放大派生压力）。
func TestContentSignatureMultimodalStable(t *testing.T) {
	img := "data:image/png;base64," + strings.Repeat("QUJD", 1000)
	withImg, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "这是什么"},
		{"type": "image_url", "image_url": map[string]any{"url": img}},
	})
	again, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "这是什么"},
		{"type": "image_url", "image_url": map[string]any{"url": img}},
	})
	a := contentSignature(withImg)
	if a == "" || a != contentSignature(again) {
		t.Fatalf("同一张图的签名应稳定且非空：%q", a)
	}
	if strings.Contains(a, "QUJD") {
		t.Error("签名不应包含 base64 原文（只入 sha256 短摘要）")
	}
	otherImg := "data:image/png;base64," + strings.Repeat("WFla", 1000)
	other, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "这是什么"},
		{"type": "image_url", "image_url": map[string]any{"url": otherImg}},
	})
	if a == contentSignature(other) {
		t.Error("不同图的签名不应相同")
	}
}

// TestRequestIDForKeyPureDerivation RequestIDForKey 纯派生（灰度移植 issue #170 缺陷 2）：
// 同 key 恒同值；内存不再随键数无上限增长——无法直接断言内存，钉住行为契约即可：
// 大量键调用后同 key 仍恒定（纯派生 vs 惰性缓存在行为上等价，但派生版不持有 entry）。
func TestRequestIDForKeyPureDerivation(t *testing.T) {
	first := RequestIDForKey("sticky-conv-1")
	for i := 0; i < 100; i++ {
		_ = RequestIDForKey("noise-key-" + string(rune(i)))
	}
	if again := RequestIDForKey("sticky-conv-1"); again != first {
		t.Fatalf("同键派生不一致：%q vs %q", first, again)
	}
	if len(first) != 32 {
		t.Errorf("应恒 32 hex：%q", first)
	}
	if RequestIDForKey("") == RequestIDForKey("") {
		t.Error("空键应各自独立（请求级随机语义）")
	}
}
