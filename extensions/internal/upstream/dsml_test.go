// dsml_test.go 补丁 0016 配套回归测试：正文工具调用标记还原。
//
// 样本取自上游 DeepSeek 系模型的真实泄漏形态（panel 实测记录），分隔符为两个
// U+FF5C 全角竖线。核心不变量：修复层绝不吞字节——拿不准的分支一律原文回吐。
package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// sampleBlock 一条完整标记块：exec_command，string 参数带换行。
// 注意 parameter 闭合带斜杠（panel 代码常量口径；其 README 注释样本漏写斜杠系笔误）。
const sampleBlock = "<｜｜DSML｜｜ calls>\n" +
	"<｜｜DSML｜｜ invoke name=\"exec_command\">\n" +
	"<｜｜DSML｜｜ parameter name=\"cmd\" string=\"true\">ls -la\necho done</｜｜DSML｜｜ parameter>\n" +
	"</｜｜DSML｜｜ invoke>\n" +
	"</｜｜DSML｜｜ calls>"

// sampleBlockJSON 同一工具的缺省 string 属性形态：参数按 JSON 解析。
const sampleBlockJSON = "<｜｜DSML｜｜ calls>" +
	"<｜｜DSML｜｜ invoke name=\"get_weather\">" +
	"<｜｜DSML｜｜ parameter name=\"city\">\"北京\"</｜｜DSML｜｜ parameter>" +
	"<｜｜DSML｜｜ parameter name=\"days\">3</｜｜DSML｜｜ parameter>" +
	"</｜｜DSML｜｜ invoke>" +
	"</｜｜DSML｜｜ calls>"

func allowCmd() map[string]bool { return map[string]bool{"exec_command": true} }

// TestMarkupPipeConstants 钉死分隔符与标记常量形态：两个 U+FF5C 全角竖线。
// 常量被误改（半角竖线/单个竖线）会让整套还原逻辑静默失效。
func TestMarkupPipeConstants(t *testing.T) {
	if markupPipe != "\uFF5C\uFF5C" {
		t.Fatalf("markupPipe=%q want 两个 U+FF5C", markupPipe)
	}
	if markupOpen != "<\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>" {
		t.Fatalf("markupOpen=%q", markupOpen)
	}
	if markupClose != "</\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>" {
		t.Fatalf("markupClose=%q", markupClose)
	}
}

// TestMarkupRepairFeedBasic 完整块转换：正文 emit 为空，调用名/参数正确，
// string="true" 参数按原文字符串保留换行。
func TestMarkupRepairFeedBasic(t *testing.T) {
	r := NewMarkupRepair(allowCmd(), false)
	emit, calls := r.Feed(sampleBlock)
	if emit != "" {
		t.Fatalf("emit=%q want 空", emit)
	}
	if len(calls) != 1 || calls[0].Name != "exec_command" {
		t.Fatalf("calls=%+v", calls)
	}
	if calls[0].Arguments != `{"cmd":"ls -la\necho done"}` {
		t.Fatalf("arguments=%q", calls[0].Arguments)
	}
	if r.Converted() != 1 || r.Seen() != 1 || r.Rejected() != 0 {
		t.Fatalf("converted=%d seen=%d rejected=%d", r.Converted(), r.Seen(), r.Rejected())
	}
	if r.Flush() != "" {
		t.Fatal("Flush 应为空")
	}
}

// TestMarkupRepairFeedJSONParam 缺省 string 属性：参数按 JSON 解析出真实类型；
// JSON 失败退化为原文字符串（参数值永不丢）。
func TestMarkupRepairFeedJSONParam(t *testing.T) {
	r := NewMarkupRepair(map[string]bool{"get_weather": true}, false)
	emit, calls := r.Feed(sampleBlockJSON)
	if emit != "" {
		t.Fatalf("emit=%q", emit)
	}
	if len(calls) != 1 {
		t.Fatalf("calls=%+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil {
		t.Fatalf("arguments 不是合法 JSON: %v (%q)", err, calls[0].Arguments)
	}
	if args["city"] != "北京" || args["days"] != float64(3) {
		t.Fatalf("args=%v", args)
	}
}

// TestMarkupRepairFeedNoParams 无参数 invoke 产出 "{}"。
func TestMarkupRepairFeedNoParams(t *testing.T) {
	block := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"ping\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
	r := NewMarkupRepair(map[string]bool{"ping": true}, false)
	emit, calls := r.Feed(block)
	if emit != "" || len(calls) != 1 || calls[0].Arguments != "{}" {
		t.Fatalf("emit=%q calls=%+v", emit, calls)
	}
}

// TestMarkupRepairStrictReject 严格判定：invoke 名不在客户端 tools 名单 → 整块原文回吐。
func TestMarkupRepairStrictReject(t *testing.T) {
	r := NewMarkupRepair(map[string]bool{"other_tool": true}, false)
	emit, calls := r.Feed(sampleBlock)
	if len(calls) != 0 {
		t.Fatalf("calls=%+v", calls)
	}
	if emit != sampleBlock {
		t.Fatalf("emit 与原文不等:\n%q\n%q", emit, sampleBlock)
	}
	if r.Rejected() != 1 {
		t.Fatalf("rejected=%d", r.Rejected())
	}
}

// TestMarkupRepairWeakAccept 名单为空 + 弱判定：结构完美 + 工具名像标识符 → 转换。
func TestMarkupRepairWeakAccept(t *testing.T) {
	r := NewMarkupRepair(nil, true)
	if !r.Enabled() {
		t.Fatal("弱判定修复器应启用")
	}
	emit, calls := r.Feed(sampleBlock)
	if emit != "" || len(calls) != 1 || calls[0].Name != "exec_command" {
		t.Fatalf("emit=%q calls=%+v", emit, calls)
	}
}

// TestMarkupRepairWeakRejectPlaceholder 弱判定挡形态明显不是工具名的输入：
// 空名/含空格/非 ASCII/非法首字符。纯文本占位符（NAME）形态与真实工具名无异，
// panel 原版同样放行（见 dsml.go 弱判定注释的取舍说明），此处钉住该行为。
func TestMarkupRepairWeakRejectPlaceholder(t *testing.T) {
	for _, name := range []string{"", "exec command", "工具", "-foo", "a b"} {
		block := "<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"" + name + "\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>"
		r := NewMarkupRepair(nil, true)
		emit, calls := r.Feed(block)
		if len(calls) != 0 {
			t.Fatalf("name=%q 不应转换", name)
		}
		if emit != block {
			t.Fatalf("name=%q 原文回吐不等", name)
		}
	}
	// NAME 形态放行（与 panel 一致）。
	r := NewMarkupRepair(nil, true)
	emit, calls := r.Feed("<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name=\"NAME\"></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>")
	if len(calls) != 1 || calls[0].Name != "NAME" || emit != "" {
		t.Fatalf("NAME 应放行: emit=%q calls=%+v", emit, calls)
	}
}

// TestMarkupRepairBothEmptyDisabled 名单空 + 弱判定关 → 修复器禁用，Feed 恒等透传。
func TestMarkupRepairBothEmptyDisabled(t *testing.T) {
	r := NewMarkupRepair(nil, false)
	if r.Enabled() {
		t.Fatal("应禁用")
	}
	emit, calls := r.Feed(sampleBlock)
	if emit != sampleBlock || calls != nil {
		t.Fatal("禁用态应恒等透传")
	}
}

// TestMarkupRepairTruncatedStart 起始标记被增量边界切断：前缀正文先透出，
// 缓冲部分在下一片补齐后正确转换；字节不丢。
func TestMarkupRepairTruncatedStart(t *testing.T) {
	full := "前文正文" + sampleBlock + "尾部"
	r := NewMarkupRepair(allowCmd(), true)
	cut := len("前文正文") + 4 // markupOpen 的前 4 字节（< + 首个 U+FF5C 的前 2 字节）
	emit1, calls1 := r.Feed(full[:cut])
	if len(calls1) != 0 {
		t.Fatalf("第一片不应有调用: %+v", calls1)
	}
	if emit1 != "前文正文" {
		t.Fatalf("emit1=%q want 前文正文", emit1)
	}
	emit2, calls2 := r.Feed(full[cut:])
	if len(calls2) != 1 || calls2[0].Name != "exec_command" {
		t.Fatalf("calls2=%+v", calls2)
	}
	if emit2 != "尾部" {
		t.Fatalf("emit2=%q want 尾部", emit2)
	}
	if r.Flush() != "" {
		t.Fatalf("Flush=%q", r.Flush())
	}
}

// TestMarkupRepairOversized 超过 256KB 未闭合 → 原文回吐（含起始标记）并退出块态。
func TestMarkupRepairOversized(t *testing.T) {
	r := NewMarkupRepair(allowCmd(), true)
	emit1, calls1 := r.Feed(sampleBlock[:len(sampleBlock)-len("</｜｜DSML｜｜ calls>")])
	if len(calls1) != 0 || emit1 != "" {
		t.Fatalf("块内缓冲期 emit1=%q calls1=%+v", emit1, calls1)
	}
	big := strings.Repeat("x", markupMaxBlockBytes+1)
	emit2, calls2 := r.Feed(big)
	if len(calls2) != 0 {
		t.Fatalf("calls2=%+v", calls2)
	}
	if !strings.HasPrefix(emit2, markupOpen) || !strings.HasSuffix(emit2, big) {
		t.Fatal("超长未闭合应含起始标记原文回吐")
	}
}

// TestMarkupRepairFlushUnclosed 流结束仍未闭合 → Flush 连同起始标记一起回吐。
func TestMarkupRepairFlushUnclosed(t *testing.T) {
	r := NewMarkupRepair(allowCmd(), true)
	frag := sampleBlock[:20]
	emit, calls := r.Feed(frag)
	if len(calls) != 0 {
		t.Fatalf("calls=%+v", calls)
	}
	tail := r.Flush()
	if tail+emit != frag {
		t.Fatalf("字节丢失: emit=%q tail=%q frag=%q", emit, tail, frag)
	}
	// 处于块态时 Flush 需补回起始标记。
	r2 := NewMarkupRepair(allowCmd(), true)
	_, _ = r2.Feed(markupOpen)
	if !strings.HasPrefix(r2.Flush(), markupOpen) {
		t.Fatal("块态 Flush 应补回起始标记")
	}
}

// TestMarkupRepairBytePreservation 不变量抽测：入参拼接 = 正文 + Flush + 被转换块原文。
func TestMarkupRepairBytePreservation(t *testing.T) {
	text := "说明：\n" + sampleBlock + "\n中间夹一段普通话\n" + sampleBlockJSON + "结尾"
	r := NewMarkupRepair(map[string]bool{"exec_command": true, "get_weather": true}, true)
	var emitAll strings.Builder
	for _, chunk := range []string{text[:7], text[7:40], text[40:]} {
		emit, _ := r.Feed(chunk)
		emitAll.WriteString(emit)
	}
	tail := r.Flush()
	// 断言：emit+tail 恰好等于 text 去掉两块被转换原文后的剩余（字节不丢）。
	want := "说明：\n" + "\n中间夹一段普通话\n" + "结尾"
	if emitAll.String()+tail != want {
		t.Fatalf("emit+tail=%q want %q", emitAll.String()+tail, want)
	}
	if r.Converted() != 2 {
		t.Fatalf("converted=%d", r.Converted())
	}
}

// TestToolNameAllowlist 四源收集：tools[].function.name、裸 tools[].name、
// functions[].name、messages 历史里的 tool_calls/function_call。
func TestToolNameAllowlist(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"a"}},{"name":"b"}],"functions":[{"name":"c"}],"messages":[{"role":"assistant","tool_calls":[{"id":"x","function":{"name":"d"}}]},{"role":"assistant","function_call":{"name":"e"}}]}`)
	got := ToolNameAllowlist(body)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		if !got[n] {
			t.Fatalf("缺 %s: %v", n, got)
		}
	}
	if ToolNameAllowlist([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)) != nil {
		t.Fatal("无工具应返回 nil")
	}
	if ToolNameAllowlist([]byte(`not json`)) != nil {
		t.Fatal("解析失败应返回 nil")
	}
}

// TestRepairAggregatedResponse 非流式：转换 + finish_reason 改写；已有结构化
// tool_calls / finish_reason=length / 名单不匹配三种情形一律不动。
func TestRepairAggregatedResponse(t *testing.T) {
	mk := func(content, finish string, tcs []any) map[string]any {
		msg := map[string]any{"role": "assistant", "content": content}
		if tcs != nil {
			msg["tool_calls"] = tcs
		}
		return map[string]any{"choices": []any{map[string]any{
			"message": msg, "finish_reason": finish,
		}}}
	}
	// 转换路径。
	resp := mk("正文前缀\n"+sampleBlock, "stop", nil)
	r := RepairAggregatedResponse(resp, allowCmd(), false)
	if r.Converted() != 1 {
		t.Fatalf("converted=%d", r.Converted())
	}
	c := resp["choices"].([]any)[0].(map[string]any)
	if c["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason=%v", c["finish_reason"])
	}
	msg := c["message"].(map[string]any)
	if msg["content"] != "正文前缀\n" {
		t.Fatalf("content=%q", msg["content"])
	}
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls=%v", tcs)
	}
	fn := tcs[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "exec_command" {
		t.Fatalf("name=%v", fn["name"])
	}
	if _, has := tcs[0].(map[string]any)["index"]; has {
		t.Fatal("非流式 tool_calls 不应带 index")
	}
	// 已有结构化 tool_calls：不覆盖。
	resp2 := mk(sampleBlock, "stop", []any{map[string]any{"id": "real"}})
	if r2 := RepairAggregatedResponse(resp2, allowCmd(), false); r2.Converted() != 0 {
		t.Fatal("已有 tool_calls 不应覆盖")
	}
	// finish_reason=length：不动。
	resp3 := mk(sampleBlock, "length", nil)
	if r3 := RepairAggregatedResponse(resp3, allowCmd(), false); r3.Converted() != 0 {
		t.Fatal("length 不应转换")
	}
	// 无标记正文：零观测。
	resp4 := mk("普通回复", "stop", nil)
	if r4 := RepairAggregatedResponse(resp4, nil, true); r4.Converted() != 0 {
		t.Fatal("无标记不应转换")
	}
}

// TestRepairStreamFrame 流式帧级：content 改写、合成 tool_calls 帧（index 避开
// 上游已用 index、float64 形态）、stop→tool_calls 改写、禁用态恒 nil。
func TestRepairStreamFrame(t *testing.T) {
	// 帧内既有结构化 tool_calls（index 0）又有泄漏标记正文。
	frame := map[string]any{"choices": []any{map[string]any{
		"index": 0,
		"delta": map[string]any{
			"content":    "中间" + sampleBlock + "后缀",
			"tool_calls": []any{map[string]any{"index": float64(0), "id": "u1", "function": map[string]any{"name": "f", "arguments": "{}"}}},
		},
	}}}
	var maxIdx int
	r := NewMarkupRepair(allowCmd(), true)
	extras := RepairStreamFrame(frame, r, &maxIdx)
	if len(extras) != 1 {
		t.Fatalf("extras=%v", extras)
	}
	delta := frame["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "中间后缀" {
		t.Fatalf("content=%v", delta["content"])
	}
	ec := extras[0]["choices"].([]any)[0].(map[string]any)
	ed := ec["delta"].(map[string]any)
	tcs := ed["tool_calls"].([]any)
	tc := tcs[0].(map[string]any)
	if tc["index"] != float64(1) {
		t.Fatalf("合成 index=%v want 1（避开上游已用的 0）", tc["index"])
	}
	if _, ok := tc["index"].(float64); !ok {
		t.Fatal("合成 index 必须是 float64（下游按 float64 读）")
	}
	if fn := tc["function"].(map[string]any); fn["name"] != "exec_command" {
		t.Fatalf("name=%v", fn["name"])
	}
	// stop 帧改写：converted>0 时 stop→tool_calls。
	stopFrame := map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{}, "finish_reason": "stop",
	}}}
	RepairStreamFrame(stopFrame, r, &maxIdx)
	if stopFrame["choices"].([]any)[0].(map[string]any)["finish_reason"] != "tool_calls" {
		t.Fatal("stop 应被改写为 tool_calls")
	}
	// 禁用态：nil 与 disabled 均恒 nil、不改帧。
	plain := map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"content": sampleBlock},
	}}}
	if RepairStreamFrame(plain, nil, &maxIdx) != nil {
		t.Fatal("nil 修复器应恒 nil")
	}
	disabled := NewMarkupRepair(nil, false)
	if RepairStreamFrame(plain, disabled, &maxIdx) != nil {
		t.Fatal("禁用修复器应恒 nil")
	}
	if plain["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"] != sampleBlock {
		t.Fatal("禁用态帧不应被改写")
	}
}

// TestRepairFlushFrame 尾部回吐帧形态：无缓冲 nil，有缓冲包成 content 帧。
func TestRepairFlushFrame(t *testing.T) {
	if RepairFlushFrame(nil) != nil {
		t.Fatal("nil 应返回 nil")
	}
	r := NewMarkupRepair(allowCmd(), true)
	if RepairFlushFrame(r) != nil {
		t.Fatal("无缓冲应返回 nil")
	}
	_, _ = r.Feed(markupOpen[:20]) // markupOpen 的真前缀（被增量边界截断）
	f := RepairFlushFrame(r)
	if f == nil {
		t.Fatal("有缓冲应返回帧")
	}
	d := f["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if d["content"] == "" {
		t.Fatal("content 不应为空")
	}
}
