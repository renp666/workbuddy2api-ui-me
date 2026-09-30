package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// usageReportEntry 旁路通道（zcode/qoder）单次调用的用量观测，字段与 core
// 桥接上报端点（POST /internal/v1/usage/reports）的 schema 一一对应。
// Prompt/Completion 为 -1 表示上游未回报；Credit 仅在显式出现时记录（缺失≠0）。
type usageReportEntry struct {
	UID        string   `json:"uid"`
	Account    string   `json:"account"`
	Model      string   `json:"model"`
	Mode       string   `json:"mode"`
	Prompt     int      `json:"prompt_tokens"`
	Completion int      `json:"completion_tokens"`
	Credit     *float64 `json:"credit,omitempty"`
	HasCredit  bool     `json:"has_credit"`
}

// maxReportTokens 防御上游异常 usage 数值（防 int 溢出）。
const maxReportTokens = 1 << 40

// usageScanLimit 单次响应参与 usage 扫描的缓冲上限；超过后 SSE 滑动丢头、
// JSON 直接放弃扫描，token 观测保持缺失语义。
const usageScanLimit = 4 << 20

// usageReporter 把旁路通道观测经桥接密钥 POST 给 core 落账；这是 console
// 旁路通道调用进入「调用统计」账本的唯一通道。
type usageReporter struct {
	client  *http.Client
	coreURL string
	key     string
}

func (r *usageReporter) report(ctx context.Context, entries []usageReportEntry) {
	if len(entries) == 0 || r == nil || r.coreURL == "" || r.key == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		log.Print("WARN: [usage-report] encode: ", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.coreURL+"/internal/v1/usage/reports", bytes.NewReader(payload))
	if err != nil {
		log.Print("WARN: [usage-report] request: ", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.key)
	resp, err := r.client.Do(req)
	if err != nil {
		log.Print("WARN: [usage-report] send: ", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		log.Print("WARN: [usage-report] core 拒绝上报：HTTP ", resp.StatusCode)
	}
}

// reportUsageAsync 独立协程上报（5 秒上限），绝不阻塞或影响调用主链路。
func (h *server) reportUsageAsync(entries []usageReportEntry) {
	if len(entries) == 0 || h.cfg.BridgeKey == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		(&usageReporter{client: h.client, coreURL: h.cfg.CoreURL.String(), key: h.cfg.BridgeKey}).report(ctx, entries)
	}()
}

// usageCaptureWriter 包装 ReverseProxy 的响应写入口：内容原样透传给客户端，
// 同时按响应类型做有界扫描（application/json 整体缓存、text/event-stream
// 滑动窗口），在 finish 时提取 usage 供上报。它不改变状态码与响应头，扫描
// 失败保持沉默——统计是尽力而为，不是调用契约。
type usageCaptureWriter struct {
	http.ResponseWriter
	entry usageReportEntry // UID/Account/Model/Mode 预填，tokens 在 finish 时填充
	buf   []byte
	mode  string // "json" | "sse" | ""（未识别则不扫描）
	set   bool   // 已根据 Content-Type 完成一次模式判定
}

// newUsageCapture 以通道占位身份（zcode=GLM 通道 / qoder=Qoder 通道 / opencode=OpenCode 通道）构造捕获器。
func (h *server) newUsageCapture(w http.ResponseWriter, model, channel string, stream bool) *usageCaptureWriter {
	e := usageReportEntry{UID: channel, Model: model, Mode: "sync", Prompt: -1, Completion: -1}
	if stream {
		e.Mode = "stream"
	}
	switch channel {
	case "qoder":
		e.Account = "Qoder 通道"
	case "opencode":
		e.Account = "OpenCode 通道"
	default:
		e.Account = "GLM 通道"
	}
	return &usageCaptureWriter{ResponseWriter: w, entry: e}
}

// Flush 透传给底层 writer；ReverseProxy 的流式冲刷依赖该接口，缺失会破坏 SSE 实时性。
func (c *usageCaptureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (c *usageCaptureWriter) Write(p []byte) (int, error) {
	if !c.set {
		c.set = true
		ct := strings.ToLower(c.Header().Get("Content-Type"))
		switch {
		case strings.Contains(ct, "text/event-stream"):
			c.mode = "sse"
		case strings.Contains(ct, "application/json"):
			c.mode = "json"
		}
	}
	switch c.mode {
	case "sse":
		// 滑动窗口：usage 出现在流尾，丢弃最早内容保证内存有界。
		c.buf = append(c.buf, p...)
		if over := len(c.buf) - usageScanLimit; over > 0 {
			c.buf = c.buf[over:]
		}
	case "json":
		if len(c.buf)+len(p) > usageScanLimit {
			c.mode = ""
			c.buf = nil
		} else {
			c.buf = append(c.buf, p...)
		}
	}
	return c.ResponseWriter.Write(p)
}

// finish 在响应拷贝完成后调用；仅当解析出 usage 时才产出观测（失败调用不计入）。
func (c *usageCaptureWriter) finish() (usageReportEntry, bool) {
	if len(c.buf) == 0 {
		return usageReportEntry{}, false
	}
	var prompt, completion int
	var credit *float64
	var hasCredit, ok bool
	switch c.mode {
	case "json":
		prompt, completion, credit, hasCredit, ok = usageFromJSONBody(c.buf)
	case "sse":
		prompt, completion, credit, hasCredit, ok = usageFromSSEBody(c.buf)
	}
	if !ok {
		return usageReportEntry{}, false
	}
	e := c.entry
	e.Prompt, e.Completion, e.Credit, e.HasCredit = prompt, completion, credit, hasCredit
	return e, true
}

// finishUsageCapture 在代理返回后提交捕获结果并异步上报。
func (h *server) finishUsageCapture(c *usageCaptureWriter) {
	if e, ok := c.finish(); ok {
		h.reportUsageAsync([]usageReportEntry{e})
	}
}

// usageFromJSONBody 从聚合 JSON 响应提取 usage 观测，与 core usagelog 同语义：
// 缺失字段 -1，credit 仅在显式出现时记录。兼容 OpenAI（prompt/completion_tokens）
// 与 Responses API（input/output_tokens）两种字段名。
func usageFromJSONBody(body []byte) (prompt, completion int, credit *float64, hasCredit bool, ok bool) {
	var resp struct {
		Usage *struct {
			Prompt     *float64 `json:"prompt_tokens"`
			Completion *float64 `json:"completion_tokens"`
			Input      *float64 `json:"input_tokens"`
			Output     *float64 `json:"output_tokens"`
			Credit     *float64 `json:"credit"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) != nil || resp.Usage == nil {
		return 0, 0, nil, false, false
	}
	prompt, completion = -1, -1
	if v := resp.Usage.Prompt; v != nil && validTokens(*v) {
		prompt = int(*v)
	}
	if v := resp.Usage.Input; v != nil && validTokens(*v) && prompt < 0 {
		prompt = int(*v)
	}
	if v := resp.Usage.Completion; v != nil && validTokens(*v) {
		completion = int(*v)
	}
	if v := resp.Usage.Output; v != nil && validTokens(*v) && completion < 0 {
		completion = int(*v)
	}
	if v := resp.Usage.Credit; v != nil && *v >= 0 {
		credit, hasCredit = v, true
	}
	return prompt, completion, credit, hasCredit, true
}

// usageFromSSEBody 从缓冲的 SSE 帧提取最终 usage：逐帧深度扫描 usage 形状的对象，
// 后帧覆盖前帧——覆盖 OpenAI 风格的末帧 usage 聚合，以及 Anthropic 风格的
// message_start（input_tokens）+ message_delta（累计 output_tokens）组合。
func usageFromSSEBody(buf []byte) (prompt, completion int, credit *float64, hasCredit bool, ok bool) {
	var scan struct {
		in, out   int
		hasIn     bool
		hasOut    bool
		credit    float64
		hasCredit bool
	}
	for _, raw := range bytes.Split(buf, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame any
		if json.Unmarshal([]byte(payload), &frame) != nil {
			continue
		}
		scanFrameUsage(frame, &scan)
	}
	if !scan.hasIn && !scan.hasOut {
		return 0, 0, nil, false, false
	}
	prompt, completion = -1, -1
	if scan.hasIn {
		prompt = scan.in
	}
	if scan.hasOut {
		completion = scan.out
	}
	if scan.hasCredit {
		credit, hasCredit = &scan.credit, true
	}
	return prompt, completion, credit, hasCredit, true
}

func scanFrameUsage(node any, scan *struct {
	in, out   int
	hasIn     bool
	hasOut    bool
	credit    float64
	hasCredit bool
}) {
	switch t := node.(type) {
	case map[string]any:
		if v, ok := usageNum(t["prompt_tokens"]); ok {
			scan.in, scan.hasIn = int(v), true
		}
		if v, ok := usageNum(t["input_tokens"]); ok && !scan.hasIn {
			scan.in, scan.hasIn = int(v), true
		}
		if v, ok := usageNum(t["completion_tokens"]); ok {
			scan.out, scan.hasOut = int(v), true
		}
		if v, ok := usageNum(t["output_tokens"]); ok && !scan.hasOut {
			scan.out, scan.hasOut = int(v), true
		}
		if v, ok := usageNum(t["credit"]); ok && v >= 0 {
			scan.credit, scan.hasCredit = v, true
		}
		for _, v := range t {
			scanFrameUsage(v, scan)
		}
	case []any:
		for _, v := range t {
			scanFrameUsage(v, scan)
		}
	}
}

// usageNum 取非负、有界的 JSON 数值。
func usageNum(v any) (float64, bool) {
	f, ok := v.(float64)
	if !ok || !validTokens(f) {
		return 0, false
	}
	return f, true
}

func validTokens(v float64) bool { return v >= 0 && v <= maxReportTokens }
