// conversation.go 会话头族的灰度适配（对标 workbuddy-manager issue #193/#201 灰度移植）：
// 上游 2026-10 起对**部分账号**灰度启用了渠道校验，出站缺 X-Conversation-ID 头即判
// `11-128 conversation id missing`（响应还是非法 JSON，code 值没加引号）。被灰度的号
// 在池里「显示健康、每次被选中都失败」。本文件提供派生补头：客户端给了 conversationId
// 就透传，没给就派生一个**同会话稳定**的值补到出站头上（只补头、不改 body——剥离
// 实测证明校验对象是头，body 里的 conversationId 不是必填项）。
//
// 为什么是 extensions 新文件而非补丁：本文件全部是新函数（对 ids.go 零改动），
// ids.go 的接线改动（ResolveConversationID → ConversationIDForHeaders、轮级复合键、
// RequestIDForKey 纯派生）见补丁 0018。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// firstUserText 取 body 里**第一条** role=="user" 消息的可签名文本（复用
// contentSignature，与 TurnKey 同口径：字符串直取、多模态 parts 拼接+摘要）。
// 一次会话内首条 user 消息不变，故可作会话级派生锚点；无可签名内容返回 ""。
func firstUserText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var obj struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	for _, m := range obj.Messages {
		if m.Role != "user" {
			continue
		}
		return contentSignature(m.Content)
	}
	return ""
}

// ConversationIDForHeaders 给出站会话头族要用的 X-Conversation-ID：客户端给了就用
// 它的原值，没给就**派生一个同一会话稳定的值**。
//
// 为什么不再"客户端没给就不发"（ids.go 原注释的语义）：上游灰度渠道校验要求
// X-Conversation-ID 必发，缺头即判 11-128 conversation id missing——被灰度的账号
// 在池里每次被选中都失败。头族本身是官方客户端的形态，补一个稳定值既让校验通过，
// 也保住后台按会话聚合的语义（同会话多轮恒同值）。校验对象是**头**：body 里的
// conversationId 不是必填项，所以只补头、不改 body。
//
// 稳定性锚点取**首条 user 消息**（firstUserText）：一次会话内它不变，所以同会话的
// 多轮请求、换号重试、降级重发都派生出同一个值；不同会话首条消息不同 → 不会被并成
// 一个会话。回落顺序：首条 user 消息 → 会话键（sessKey）→ 请求级随机（此时没有任何
// "会话"可供聚合，随机值只为满足校验，与 RequestIDForKey 的空键语义一致）。
func ConversationIDForHeaders(body []byte, sessKey string) string {
	if v := ResolveConversationID(body); v != "" {
		return v
	}
	if anchor := firstUserText(body); anchor != "" {
		return RequestIDForKey("conv:" + anchor)
	}
	if sessKey != "" {
		return RequestIDForKey("conv:" + sessKey)
	}
	return NewMessageID()
}

// contentSignature 取消息 content 的确定性签名（纯图片轮不再碎片化）：
//   - string 形态：返回文本，与 contentText 结果**完全一致**——纯文本路径键值
//     不变，存量会话的轮键/粘性键零漂移（向后兼容契约）；
//   - 数组形态（多模态 parts）：文本 part（type 为 "" 或 "text"，与 contentText
//     的拼接口径一致）按原文无缝拼接；非文本 part 追加 "[type:摘要]"——摘要取该
//     part 原始字节的 sha256 前 8 hex。data: base64 内联图可能超长，原文入键会
//     放大派生哈希压力，只入短摘要；type + 原文摘要天然区分不同内容/数量的非文本
//     part 集合，无需额外计数占位。
//
// 无可签名内容（空 / null / 空数组 / 纯文本 part 全为空串）返回 ""（不伪造——
// 调用方回落原有的空键语义）。TurnKey 与 firstUserText 共用本函数，两条链路的
// 图片盲区一并修复。
func contentSignature(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// 字符串形态：纯文本路径签名 == contentText 结果（键值零漂移）。
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return ""
		}
		return str
	}
	if s[0] != '[' {
		return ""
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	hasNonText := false
	for _, pr := range parts {
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(pr, &p); err != nil {
			return ""
		}
		if p.Type == "" || p.Type == "text" {
			// 文本 part：无缝拼接（与 contentText 完全同口径——全文本 part 的数组
			// 签名 == contentText 结果，存量键零漂移）。
			b.WriteString(p.Text)
			continue
		}
		// 非文本 part：type + 原文 sha256 前 8 hex（超长 data: URL 只入短摘要）。
		hasNonText = true
		sum := sha256.Sum256(pr)
		fmt.Fprintf(&b, "\n[%s:%s]\n", p.Type, hex.EncodeToString(sum[:4]))
	}
	out := b.String()
	if !hasNonText {
		return out
	}
	return strings.TrimSpace(out)
}
