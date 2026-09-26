// usage_hook.go 调用统计取数钩子：从流式读取器/聚合响应提取单次用量观测，
// 交由 usagelog 落账（接线见 extensions/cmd/server/extension.go；补丁 0008 在
// handler 的两条成功路径上调用）。
package server

// usageSnapshot 返回流式末帧的 usage 观测。usage 缺失时 prompt/completion 为 -1
// 哨兵；credit 只有在 usage 存在且字段显式出现时才算合法观测（显式 0 保留）。
func (s *chatStatsReader) usageSnapshot() (prompt, completion int, credit float64, hasCredit bool) {
	prompt, completion = -1, -1
	if s.hasUsage {
		prompt, completion = s.prompt, s.tokens
	}
	return prompt, completion, s.credit, s.hasUsage && s.hasCredit
}
