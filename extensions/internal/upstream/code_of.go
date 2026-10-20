// code_of.go 从上游响应体抽取业务码（对标 workbuddy-manager issue #201 缺陷 1 灰度移植）。
package upstream

import "regexp"

// codeRe 从上游响应体里抽业务码：数字（11140）与带横线的字符串（11-128）两种形态。
// 上游偶尔给出**非法 JSON**（`"code":11-128` 没加引号，灰度渠道校验的实测响应），
// 所以这里用正则而不是 json.Unmarshal —— 解析失败的响应也要能报出码来。
var codeRe = regexp.MustCompile(`"code"\s*:\s*"?([0-9]{2,8}(?:-[0-9]{1,4})?)"?`)

// CodeOf 取上游响应体里的业务码字面值；取不到返回 ""。
//
// 存在的理由：禁用/冷却理由此前**写死** 11140，而灰度渠道校验的真实响应是
// HTTP 400 + code 11-128 —— 排障时按 11140 去搜会得到完全错误的方向（"UA 平台段
// 送错"）。理由里带上响应自己的码，才不会把人带偏。
// 供 handler 的 upstreamCodeLabel 组装「code X, http Y」短标签。
func CodeOf(body string) string {
	m := codeRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
