// upcode.go 「禁用/冷却理由」用的上游业务码短标签（对标 workbuddy-manager issue #201
// 缺陷 1，灰度移植）。handler 同包 extensions 新文件，免补丁。
package server

import (
	"fmt"

	"workbuddy2api/internal/upstream"
)

// upstreamCodeLabel 给「禁用/冷却理由」用的短标签：优先取响应体里的**真实业务码**
// （数字，或 11-128 这种带横线的字符串），取不到再回落到 HTTP 状态。
//
// 不写死具体码是刻意的：applyErrorPolicy 的 request illegal 分支历史上只见过 11140，
// 于是理由里写死了它；灰度渠道校验现场实际是 `http 400 + code 11-128`，排障的人
// 拿着 11140 去搜方向全错（11140 指向"UA 平台段送错"，而真实原因是渠道校验）。
// 理由里带上响应自己的码，才不会把人带偏。
func upstreamCodeLabel(body string, uerr *upstream.Error) string {
	if code := upstream.CodeOf(body); code != "" {
		if uerr != nil && uerr.Status > 0 {
			return fmt.Sprintf("code %s, http %d", code, uerr.Status)
		}
		return "code " + code
	}
	if uerr != nil && uerr.Status > 0 {
		return fmt.Sprintf("http %d", uerr.Status)
	}
	return "unknown code"
}
