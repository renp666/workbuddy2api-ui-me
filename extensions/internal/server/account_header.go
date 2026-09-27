// account_header.go 归属标签响应头（补丁 0009 配套扩展，接线见补丁说明）：
// chatCompletions 两条成功路径调用 setAccountHeaders，把本次实际使用的账号与
// 平台域透出给客户端（公共 API 与控制台对话测试共用同一链路）。
//
// 头值安全：HTTP 头值不允许非 ASCII 字节。昵称常见中文，直接写入会让
// net/http 静默丢弃该头，因此仅当昵称为可打印 ASCII 时用作 X-Account，
// 否则回落到 UID；并统一剥除 CR/LF 等控制字符，杜绝响应头注入。
package server

import (
	"net/http"
	"strings"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
)

func setAccountHeaders(w http.ResponseWriter, acct *auth.Auth) {
	display := acct.Nickname
	if !isPrintableASCII(display) {
		display = acct.UID
	}
	w.Header().Set("X-Account", sanitizeHeaderValue(display))
	w.Header().Set("X-Account-Realm", acct.Realm())
}

// accountRefsToJSON 把模型可用账号清单序列化为 /v1/models 条目的 accounts 字段：
// uid 恒有（稳定可寻址），nickname 仅用于展示、为空时省略该键。
func accountRefsToJSON(refs []pool.AccountRef) []map[string]string {
	out := make([]map[string]string, 0, len(refs))
	for _, r := range refs {
		m := map[string]string{"uid": r.UID}
		if r.Nickname != "" {
			m["nickname"] = r.Nickname
		}
		out = append(out, m)
	}
	return out
}

func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// sanitizeHeaderValue 剥除控制字符（含 CR/LF，防响应头拆分）与首尾空白。
func sanitizeHeaderValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}
