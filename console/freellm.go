package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// freellmModelPrefix routes model names starting with this prefix to the
// optional FreeLLMAPI sidecar (freellm-proxy). FreeLLMAPI is a self-hosted
// multi-provider free-tier aggregator: one OpenAI-compatible upstream fronting
// dozens of providers with per-key rotation, quota learning and fallback chains
// baked into the sidecar itself — the console deliberately keeps none of that
// (same split as the other sidecar channels). Its ids are bare names (e.g.
// "gemini-3-flash"), so the public form is "freellm-<name>"; core model ids
// are "cn:"/"global:" namespaced and glm-/qoder-/opencode-/autoclaw-/trae-/
// raccoon- prefixes are distinct, so there is no collision.
const freellmModelPrefix = "freellm-"

// freellmRouteBodyLimit 限制分流检查时缓冲的请求体大小。上游
// REQUEST_BODY_LIMIT_MB 默认 25 MiB，且超限在路由前就返回 413
// request_too_large（不进 fallback 链）——网关缓冲得更宽只会把「注定失败的
// 大请求」伪装成棱镜自己的问题，所以这里取比上游默认更小的值。
const freellmRouteBodyLimit = 24 << 20

// freellmCallable 判断一条上游目录条目此刻是否值得对外播种。
// 上游用 execution_status 区分「现在能服务」与「注定失败」：needsKey 缺供应商
// key、exhausted 在冷却，两者调用必失败；ready 才是可用。字段缺失的是虚拟条目
// （auto/fusion，可用性在调用时才定），保留。未知取值按「不播种」处理——
// 宁可少给一个候选，也不给别名链和探测塞一个必然 503 的目标。
func freellmCallable(executionStatus string) bool {
	return executionStatus == "" || executionStatus == "ready"
}

// freellmCall 调用 sidecar 的统一 key 面（/v1/*）与免鉴权诊断端点。诊断端点与
// 推理面同一把 key。未配置通道时直接判为「无应答」，不让探活路径碰空指针。
func (h *server) freellmCall(ctx context.Context, pth string, timeout time.Duration) upstreamResponse {
	if h.cfg.FreellmURL == nil {
		return upstreamResponse{}
	}
	target := *h.cfg.FreellmURL
	target.Path = pth
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return upstreamResponse{}
	}
	if h.cfg.FreellmKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.FreellmKey)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return h.fetchUpstream(req.WithContext(ctx))
}

// freellmLiveness 读 /livez：进程能否服务（DB 可达 + 加密钥匙就绪）。免鉴权、
// 不进 sidecar 的请求限流器，所以每 30 秒一轮探活不会挤占推理面的分钟额度。
func (h *server) freellmLiveness(ctx context.Context) bool {
	return h.freellmCall(ctx, "/livez", 3*time.Second).status == 200
}

// freellmPool 读 /readyz：池内此刻有没有可服务的上游，以及上游给的机器可读
// 原因档（no_upstreams_configured / all_upstreams_rate_limited /
// all_upstreams_unhealthy / db_unreachable）。上游注释明确说这两个端点是给
// 元网关跳实例用的，棱镜正是元网关。
func (h *server) freellmPool(ctx context.Context) (ready bool, reason string, upstreams int) {
	response := h.freellmCall(ctx, "/readyz", 3*time.Second)
	if response.status == 0 {
		return false, "", 0
	}
	var envelope struct {
		Status         string `json:"status"`
		Reason         string `json:"reason"`
		ReadyUpstreams *int   `json:"ready_upstreams"`
	}
	if json.Unmarshal(response.body, &envelope) != nil {
		// 应答不是预期形状时只报「就绪与否」这一个事实，不编原因、不编计数。
		return response.status == 200, "", 0
	}
	if response.status == 200 {
		if envelope.ReadyUpstreams != nil {
			upstreams = *envelope.ReadyUpstreams
		}
		return true, "", upstreams
	}
	return false, envelope.Reason, 0
}

// freellmProviders 读 /v1/providers：逐平台的 key 健康与顶层 counts 汇总。
// 同样挂在限流器之前。形状是 {"providers":[{platform,name,status,keys,
// resume_at?,last_error?,requests_remaining_pct?}],"counts":{...}}，
// nil 表示拿不到（缺失不拿空表冒充）。
func (h *server) freellmProviders(ctx context.Context) ([]any, map[string]any) {
	response := h.freellmCall(ctx, "/v1/providers", 3*time.Second)
	if response.status != 200 {
		return nil, nil
	}
	var state map[string]any
	if json.Unmarshal(response.body, &state) != nil || state == nil {
		return nil, nil
	}
	counts, _ := state["counts"].(map[string]any)
	providers, _ := state["providers"].([]any)
	if providers == nil {
		return nil, nil
	}
	return providers, counts
}

// freellmCatalog 拉 /v1/models 的原始条目。上游的目录是「全网能力清单」而非
// 「本实例可服务清单」，因此返回全集与可调用子集两份，由调用方决定用哪份、
// 并把两个数都给页面——只报全集会造出 259 条假候选，只报子集又解释不了差额。
func (h *server) freellmCatalog(ctx context.Context) ([]map[string]any, []map[string]any, bool) {
	response := h.freellmCall(ctx, "/v1/models", 3*time.Second)
	list, ok := parseModelList(response)
	if !ok {
		return nil, nil, false
	}
	all := make([]map[string]any, 0, len(list))
	callable := make([]map[string]any, 0, len(list))
	for _, item := range list {
		var entry map[string]any
		if json.Unmarshal(item, &entry) != nil || entry == nil {
			continue
		}
		id, _ := entry["id"].(string)
		if id == "" {
			continue
		}
		if !strings.HasPrefix(id, freellmModelPrefix) {
			entry["id"] = freellmModelPrefix + id
		}
		entry["realm"] = "freellm"
		status, _ := entry["execution_status"].(string)
		if freellmCallable(status) {
			callable = append(callable, entry)
		}
		all = append(all, entry)
	}
	return all, callable, true
}

// filterFreellmModels 是 freellmCallable 在原始目录条目上的批量形态，
// 供 fetchChannelModels（别名选择器与探测种子的入口）复用同一判据。
func filterFreellmModels(list []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(list))
	for _, item := range list {
		var entry struct {
			ID              string `json:"id"`
			ExecutionStatus string `json:"execution_status"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		if freellmCallable(entry.ExecutionStatus) {
			out = append(out, item)
		}
	}
	return out
}

func (h *server) freellmProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.FreellmURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("X-Goog-Api-Key")
			// FreeLLMAPI 禁匿名 Origin 的口径与 OW Bridge 一致（服务端 CORS 过滤，
			// 浏览器来源一律拒），仪表盘调用也不带凭据，一律不转发。
			pr.Out.Header.Del("Origin")
			if h.cfg.FreellmKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.FreellmKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "FreeLLMAPI 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "FreeLLMAPI 服务不可达，请稍后重试")
		},
	}
}

func stripFreellmModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, freellmModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, freellmModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToFreellm(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, freellmRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > freellmRouteBodyLimit {
		adminError(w, 413, "请求体超过 24 MiB 分流检查上限（上游默认 25 MiB，超限会在其路由前直接 413）")
		return false, true
	}
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	if !strings.HasPrefix(envelope.Model, freellmModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripFreellmModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

// adminFreellmStatus 汇总可选 FreeLLMAPI 通道供页签展示。sidecar 不可达仍是
// 成功应答，只是 reachable:false。
//
// 三个口径分开，因为它们会各自独立为真：
//   - reachable ← /livez：进程活着（DB 可达、加密钥匙就绪）。
//   - pool_ready ← /readyz：池内有没有此刻能服务的上游，没有则带上上游给的
//     原因档。目录 200 不证明这一点——实测 259 条目录里 257 条 needsKey，
//     调用一律 503 no_providers_configured。
//   - models ← /v1/models 的可调用子集；model_count 仍是全集条数，
//     callable_count 是对外播种的条数，两个数都给页面才解释得了差额。
//
// 诊断端点都不进 sidecar 的分钟限流器（/v1/models 进），所以每轮探活不会
// 挤占推理面的额度预算。任一端点失败只降级缺字段，不影响主响应。
func (h *server) adminFreellmStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.FreellmURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	result := map[string]any{
		"enabled":        true,
		"reachable":      h.freellmLiveness(r.Context()),
		"model_count":    0,
		"callable_count": 0,
		"models":         []map[string]any{},
	}
	ready, reason, upstreams := h.freellmPool(r.Context())
	result["pool_ready"] = ready
	if ready {
		result["ready_upstreams"] = upstreams
	} else if reason != "" {
		result["pool_reason"] = reason
	}
	if all, callable, ok := h.freellmCatalog(r.Context()); ok {
		result["models"] = callable
		result["model_count"] = len(all)
		result["callable_count"] = len(callable)
	}
	if providers, counts := h.freellmProviders(r.Context()); providers != nil {
		result["providers"] = providers
		if counts != nil {
			result["provider_counts"] = counts
		}
	}
	// /v1/quota-forecast 是上游专门给元网关的日帽余量读数：可以在花掉一次注定
	// 429 的请求之前先看 low_balance，并知道距下次重置还有多久。缺失不补零。
	if forecast := h.freellmCall(r.Context(), "/v1/quota-forecast", 3*time.Second); forecast.status == 200 {
		var payload map[string]any
		if json.Unmarshal(forecast.body, &payload) == nil && payload != nil {
			result["quota_forecast"] = payload
		}
	}
	// 手工测速探测的结论以 health.modelResults 形态下发，前端模型表复用既有渲染。
	if health := h.probeHealth(channelFreellm); health != nil {
		result["health"] = health
	}
	writeJSON(w, 200, result)
}

// logFreellmAttribution 记录一次旁路调用的路由归因。账本只有 token 数，
// 「哪个供应商的哪个模型真正服务了这次调用、重试了几跳、命中缓存没有」
// 只存在于上游响应头里，不落日志就无法复盘免费池的产能转移与积分降幅。
// 尽力而为：头缺失就只写已有字段，全部缺失则不打这条。
func logFreellmAttribution(header http.Header, model string) {
	routedVia := header.Get("X-Routed-Via")
	attempts := header.Get("X-Fallback-Attempts")
	trail := header.Get("X-Fallback-Trail")
	cache := header.Get("X-FreeLLM-Cache")
	if routedVia == "" && attempts == "" && trail == "" && cache == "" {
		return
	}
	fields := []string{"model=" + model}
	if routedVia != "" {
		fields = append(fields, "routed_via="+routedVia)
	}
	if attempts != "" {
		fields = append(fields, "fallback_attempts="+attempts)
	}
	if trail != "" {
		fields = append(fields, "fallback_trail="+trail)
	}
	if cache != "" {
		fields = append(fields, "cache="+cache)
	}
	log.Print("[freellm] 路由归因 ", strings.Join(fields, " "))
}
func (h *server) adminFreellmChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.FreellmURL == nil {
		adminError(w, 409, "FreeLLMAPI 通道未启用")
		return
	}
	route, handled := h.routesToFreellm(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "FreeLLMAPI 测试仅支持 freellm- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.freellmProxy().ServeHTTP(w, req)
}
