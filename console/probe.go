package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 手工测速探测：core/glm/qoder 三个上游不提供逐模型探测结论（opencode 的
// OW Bridge 自带 /health.modelResults，不在本机制范围内），模型状态列因此
// 一直是「未探测」。这里补一个仅由管理员手工触发的流式探测：每个模型发一条
// 最小对话，记录 TTFT、总耗时与输出速度，结论进内存缓存（重启即清空），
// 既供模型表展示，也接入 gateway-auto 的选路竞争。不点按钮就不产生任何
// 上游调用，不消耗额度。
//
// 参数口径借鉴成熟测速工具（NVIDIA GenAI-Perf / AWS LLMeter / Ray LLMPerf）：
// 流式请求 + 校验 content 非空。非流式 max_tokens=1 会把推理模型误判为可用
// ——预算全耗在隐藏推理上时上游照样回 200 + 空 content + finish_reason=length。
// max_tokens 取 256 而不是更小的值，同样是为了给推理模型留出可见输出；实测
// 记录表明 64 预算下 cn:glm 这类推理模型会稳定得到空 content。
// 速度优先用 stream_options.include_usage 回报的真实 completion_tokens，
// 拿不到就留空由前端显示「—」，不得伪造。

const (
	probeRequestTimeout = 30 * time.Second
	probeConcurrency    = 2
	probeBodyLimit      = 1 << 20
	probeMaxTokens      = 256
)

// probeJob 是一次探测里的单个模型任务，public 是对外形态的模型名。
type probeJob struct {
	channel routeChannel
	public  string
}

// probeRunner 持有探测运行状态与结论缓存。结论按通道分组、以公共模型名为键，
// 值直接采用前端 modelProbeState 认识的形态（ok/category/durationMs/error/
// source/probedAt），外加测速指标（ttftMs/outputTokens/tokensPerSec），
// 这样旁路状态端点把它塞进 health.modelResults 就能原样复用既有渲染。
type probeRunner struct {
	mu        sync.Mutex
	running   bool
	done      int
	total     int
	startedAt time.Time
	results   map[routeChannel]map[string]map[string]any
}

func newProbeRunner() *probeRunner {
	return &probeRunner{results: map[routeChannel]map[string]map[string]any{}}
}

// channelResults 返回某通道结论的拷贝；没有结论时返回 nil。
func (p *probeRunner) channelResults(c routeChannel) map[string]map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	src := p.results[c]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// snapshot 返回当前运行状态与各通道结论，供 /admin/probe 序列化。
func (p *probeRunner) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	channels := map[string]map[string]map[string]any{}
	for c, byModel := range p.results {
		if len(byModel) == 0 {
			continue
		}
		channels[string(c)] = byModel
	}
	return map[string]any{
		"running": p.running,
		"done":    p.done,
		"total":   p.total,
		"channels": channels,
	}
}

// start 同步抓取各通道目录并规划任务，随后异步执行。ctx 只用于抓目录
// （随请求生命周期），探测本身用独立 context，避免页面断开中断在途探测。
func (p *probeRunner) start(h *server, ctx context.Context, wanted []routeChannel) error {
	var jobs []probeJob
	for _, c := range wanted {
		if c == channelOpenCode || !h.channelEnabled(c) {
			continue
		}
		ids, ok := h.fetchChannelModels(ctx, c)
		if !ok {
			continue
		}
		for _, id := range ids {
			jobs = append(jobs, probeJob{channel: c, public: id})
		}
	}
	if len(jobs) == 0 {
		return errors.New("没有可探测的模型：所选通道未启用或当前不可达")
	}
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return errors.New("已有探测正在进行，请等待完成")
	}
	p.running = true
	p.done = 0
	p.total = len(jobs)
	p.startedAt = time.Now()
	for _, c := range wanted {
		delete(p.results, c)
	}
	p.mu.Unlock()
	go p.run(h, jobs)
	return nil
}

func (p *probeRunner) run(h *server, jobs []probeJob) {
	byChannel := map[routeChannel][]probeJob{}
	for _, job := range jobs {
		byChannel[job.channel] = append(byChannel[job.channel], job)
	}
	var wg sync.WaitGroup
	for c, list := range byChannel {
		wg.Add(1)
		go func(c routeChannel, list []probeJob) {
			defer wg.Done()
			sem := make(chan struct{}, probeConcurrency)
			var inner sync.WaitGroup
			for _, job := range list {
				inner.Add(1)
				go func(job probeJob) {
					defer inner.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					result := h.probeOne(job.channel, job.public)
					p.record(job.channel, job.public, result)
				}(job)
			}
			inner.Wait()
		}(c, list)
	}
	wg.Wait()
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
	// 探测完成立刻让 auto 的候选缓存过期，下一次 gateway-auto 解析就用新结论。
	h.probes.invalidate()
}

func (p *probeRunner) record(c routeChannel, public string, result map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.results[c] == nil {
		p.results[c] = map[string]map[string]any{}
	}
	p.results[c][public] = result
	if p.done < p.total {
		p.done++
	}
}

// probeOne 对单个模型发一条流式最小对话并归类结论。
func (h *server) probeOne(c routeChannel, public string) map[string]any {
	native := public
	if prefix := c.prefix(); prefix != "" {
		native = strings.TrimPrefix(public, prefix)
	}
	body, err := json.Marshal(map[string]any{
		"model":            native,
		"messages":         []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens":       probeMaxTokens,
		"stream":           true,
		"stream_options":   map[string]any{"include_usage": true},
	})
	if err != nil {
		return probeFailResult("error", "探测请求构造失败", 0)
	}
	var target string
	if c == channelCore {
		// core 复用桥接口：forward 只校验桥接密钥并注入部署 API Key，
		// 不要求管理会话 owner，探测与网页对话测试走同一鉴权通道。
		u := *h.cfg.CoreURL
		u.Path = "/internal/v1/chat"
		target = u.String()
	} else {
		base, _ := h.channelUpstream(c)
		if base == nil {
			return probeFailResult("error", "通道未配置上游", 0)
		}
		u := *base
		u.Path = "/v1/chat/completions"
		target = u.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target, strings.NewReader(string(body)))
	if err != nil {
		return probeFailResult("error", "探测请求构造失败", 0)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c == channelCore {
		req.Header.Set("Authorization", "Bearer "+h.cfg.BridgeKey)
	} else if _, key := h.channelUpstream(c); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	start := time.Now()
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return probeFailResult("timeout", "探测超时", int(time.Since(start).Milliseconds()))
		}
		return probeFailResult("error", "上游不可达", int(time.Since(start).Milliseconds()))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail := readProbeError(response.Body)
		return probeFailResult(probeFailureCategory(response.StatusCode), detail, int(time.Since(start).Milliseconds()))
	}
	ttftMs := -1
	finish := ""
	content := false
	completionTokens := -1
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), probeBodyLimit)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens *int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			if !content && choice.Delta.Content != "" {
				content = true
				ttftMs = int(time.Since(start).Milliseconds())
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finish = *choice.FinishReason
			}
		}
		if chunk.Usage != nil && chunk.Usage.CompletionTokens != nil && *chunk.Usage.CompletionTokens >= 0 {
			completionTokens = *chunk.Usage.CompletionTokens
		}
	}
	durationMs := int(time.Since(start).Milliseconds())
	if ctx.Err() != nil {
		return probeFailResult("timeout", "探测超时", durationMs)
	}
	if err := scanner.Err(); err != nil {
		return probeFailResult("error", "响应流读取失败", durationMs)
	}
	if !content {
		note := "上游返回空内容"
		if finish == "length" {
			note = "空内容（输出预算耗尽，疑似推理模型预算不足）"
		}
		return probeFailResult("empty", note, durationMs)
	}
	result := map[string]any{
		"ok":         true,
		"source":     "probe",
		"durationMs": durationMs,
		"probedAt":   time.Now().UnixMilli(),
	}
	if ttftMs >= 0 {
		result["ttftMs"] = ttftMs
	}
	if completionTokens > 0 {
		result["outputTokens"] = completionTokens
		if gen := durationMs - ttftMs; gen > 0 {
			result["tokensPerSec"] = round1(float64(completionTokens) / (float64(gen) / 1000))
		}
	}
	return result
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// probeFailureCategory 把 HTTP 状态码归类到前端 modelFailureLabels 认识的键。
func probeFailureCategory(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusNotFound:
		return "model_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	default:
		return "error"
	}
}

func readProbeError(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 512))
	if err != nil {
		return "上游返回错误"
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "上游返回错误"
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func probeFailResult(category, note string, durationMs int) map[string]any {
	return map[string]any{
		"ok":         false,
		"category":   category,
		"error":      note,
		"source":     "probe",
		"durationMs": durationMs,
		"probedAt":   time.Now().UnixMilli(),
	}
}

// probeableChannels 是手工探测覆盖的通道；opencode 由 OW Bridge 自带探测，排除。
var probeableChannels = []routeChannel{channelCore, channelGLM, channelQoder}

// adminProbeStart 触发一轮手工探测。body.channels 缺省为全部可探测通道。
func (h *server) adminProbeStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channels []string `json:"channels"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	wanted := probeableChannels
	if len(body.Channels) > 0 {
		wanted = nil
		for _, raw := range body.Channels {
			c := routeChannel(strings.TrimSpace(raw))
			if c != channelCore && c != channelGLM && c != channelQoder {
				adminError(w, 400, "探测通道只能是 core、glm 或 qoder")
				return
			}
			wanted = append(wanted, c)
		}
	}
	if err := h.probe.start(h, r.Context(), wanted); err != nil {
		adminError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, h.probe.snapshot())
}

// adminProbeState 返回探测进度与结论，供页面轮询。
func (h *server) adminProbeState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.probe.snapshot())
}

// probeHealth 把某通道的手工探测结论包装成前端 channelModelRows
// 认识的 health 形态（modelResults 以公共模型名为键）。
func (h *server) probeHealth(c routeChannel) map[string]any {
	results := h.probe.channelResults(c)
	if len(results) == 0 {
		return nil
	}
	return map[string]any{"modelResults": results}
}
