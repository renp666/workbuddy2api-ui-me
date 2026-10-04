package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// zcodeModelPrefix routes model names starting with this prefix to the optional
// zcode-proxy upstream. Core model ids are all "cn:"/"global:" namespaced, so there is no collision.
const zcodeModelPrefix = "glm-"

// zcodeRouteBodyLimit bounds the buffered request body we inspect for the model field.
const zcodeRouteBodyLimit = 32 << 20

// zcodeModelsBodyLimit bounds each upstream /v1/models response we merge.
const zcodeModelsBodyLimit = 4 << 20

func (h *server) zcodeProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.ZCodeURL)
			stripPrivateHeaders(pr.Out.Header)
			// Never leak the deployment API key (Anthropic clients send it as x-api-key)
			// to the third-party container; only the dedicated zcode key may cross that hop.
			pr.Out.Header.Del("X-Api-Key")
			if h.cfg.ZCodeKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "GLM 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "GLM 服务不可达，请稍后重试")
		},
	}
}

// publicRouter dispatches the public /v1/* surface: glm-* model chat/messages/responses requests
// go to zcode-proxy, qoder-* model requests go to qoder-proxy, opencode-* model requests go to
// the opencode sidecar, everything else stays on core.
func (h *server) publicRouter(core *httputil.ReverseProxy) http.HandlerFunc {
	var zcode, qoder, opencode *httputil.ReverseProxy
	if h.cfg.ZCodeURL != nil {
		zcode = h.zcodeProxy()
	}
	if h.cfg.QoderURL != nil {
		qoder = h.qoderProxy()
	}
	if h.cfg.OpenCodeURL != nil {
		opencode = h.opencodeProxy()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			h.mergedModels(w, r, core)
			return
		case "POST /v1/chat/completions", "POST /v1/messages", "POST /v1/responses":
			// Buffer body once, inspect model field, restore for downstream.
			body, err := io.ReadAll(io.LimitReader(r.Body, zcodeRouteBodyLimit+1))
			if err != nil {
				adminError(w, 400, "请求体读取失败")
				return
			}
			if len(body) > zcodeRouteBodyLimit {
				adminError(w, 413, "请求体超过 32 MiB 分流检查上限")
				return
			}
			var envelope struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if json.Unmarshal(body, &envelope) != nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				r.TransferEncoding = nil
				core.ServeHTTP(w, r)
				return
			}
			// 别名与 auto 虚拟模型在这一步解析：命中就把 model 改写成该链路的
			// 公共形态（带通道前缀），下面的既有前缀分流就能原样复用。未命中
			// 时 envelope.Model 不变，出口行为与没有该功能时完全一致。
			if decision, ok := h.resolveAlias(r.Context(), envelope.Model); ok {
				if rewritten, changed := rewriteModelField(body, decision.RoutedModel); changed {
					body = rewritten
				}
				envelope.Model = decision.RoutedModel
				w = withPrismHeaders(w, decision)
			}
			// 模型积分开关：停用（含默认停用）的模型在这里拒绝，与 /v1/models
			// 的隐藏共用同一口径。auto 解析后的目标同样受检。
			if !h.modelGate(r.Context(), envelope.Model) {
				writeModelDisabledError(w, r, envelope.Model)
				return
			}
			if zcode != nil && strings.HasPrefix(envelope.Model, zcodeModelPrefix) {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// 旁路通道不经过 core，响应在这里捕获 usage 并异步回传 core 落账。
				capture := h.newUsageCapture(w, envelope.Model, "zcode", envelope.Stream)
				zcode.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if qoder != nil && strings.HasPrefix(envelope.Model, qoderModelPrefix) {
				modified := stripQoderModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// qoder 上报保留公共前缀模型名（剥前缀前的 envelope.Model）便于区分通道。
				capture := h.newUsageCapture(w, envelope.Model, "qoder", envelope.Stream)
				qoder.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if opencode != nil && strings.HasPrefix(envelope.Model, opencodeModelPrefix) {
				modified := stripOpenCodeModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// opencode 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, envelope.Model, "opencode", envelope.Stream)
				opencode.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.TransferEncoding = nil
			core.ServeHTTP(w, r)
			return
		}
		core.ServeHTTP(w, r)
	}
}

// routesToZCode buffers the JSON body to inspect its top-level model field and restores it
// for the downstream proxy. Malformed or model-less bodies fall through to core unchanged.
// The bool pair is (route to zcode, response already written).
func (h *server) routesToZCode(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, zcodeRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > zcodeRouteBodyLimit {
		adminError(w, 413, "请求体超过 32 MiB 分流检查上限")
		return false, true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false, false
	}
	return strings.HasPrefix(envelope.Model, zcodeModelPrefix), false
}

type modelsEnvelope struct {
	Object string            `json:"object"`
	Data   []json.RawMessage `json:"data"`
}

// filterZcodeModels drops GLM models the current plan tier cannot serve. The zcode-proxy
// gateway only answers the -flash variants under start-plan (claimed trial/weekend quotas);
// advertising the full catalog there hands clients models that fail with 1113. Any other
// plan (including an unknown/unreachable plan) keeps the list intact.
func filterZcodeModels(list []json.RawMessage, plan string) []json.RawMessage {
	if plan != "start-plan" {
		return list
	}
	out := make([]json.RawMessage, 0, len(list))
	for _, item := range list {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &entry) == nil && strings.Contains(entry.ID, "-flash") {
			out = append(out, item)
		}
	}
	return out
}

// tagModelRealm injects a "realm" attribution tag into a raw model-list entry
// (patch-0009 parity: core already tags cn/global server-side; glm entries come
// from a third-party upstream whose list has no realm, so console adds it here).
// Non-object or id-less entries pass through untouched.
func tagModelRealm(item json.RawMessage, realm string) json.RawMessage {
	var entry map[string]any
	if json.Unmarshal(item, &entry) != nil || entry == nil {
		return item
	}
	if id, ok := entry["id"].(string); !ok || id == "" {
		return item
	}
	entry["realm"] = realm
	out, err := json.Marshal(entry)
	if err != nil {
		return item
	}
	return out
}

type upstreamResponse struct {
	status      int
	contentType string
	body        []byte
}

// mergedModels fetches /v1/models from core and zcode-proxy in parallel and returns the union,
// core entries first, deduplicated by model id. Anything other than a parseable core list is
// replayed to the client verbatim (preserving auth and error semantics); a zcode failure simply
// degrades to the core list alone.
func (h *server) mergedModels(w http.ResponseWriter, r *http.Request, core *httputil.ReverseProxy) {
	coreURL := *h.cfg.CoreURL
	coreURL.Path = "/v1/models"
	coreURL.RawQuery = r.URL.RawQuery
	coreReq, err := http.NewRequestWithContext(r.Context(), "GET", coreURL.String(), nil)
	if err != nil {
		core.ServeHTTP(w, r)
		return
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		coreReq.Header.Set("Authorization", auth)
	}
	var (
		wg          sync.WaitGroup
		coreResp    upstreamResponse
		zcodeRaw    []json.RawMessage
		qoderRaw    []json.RawMessage
		opencodeRaw []json.RawMessage
		zcodePlan   string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		coreResp = h.fetchUpstream(coreReq)
	}()
	if h.cfg.ZCodeURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zcodeURL := *h.cfg.ZCodeURL
			zcodeURL.Path = "/v1/models"
			zcodeReq, err := http.NewRequestWithContext(r.Context(), "GET", zcodeURL.String(), nil)
			if err != nil {
				return
			}
			if h.cfg.ZCodeKey != "" {
				zcodeReq.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
			}
			zcodeRaw, _ = h.fetchModels(zcodeReq)
		}()
		if h.cfg.ZCodeControlURL != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				zcodePlan = h.zcodePlan(r.Context())
			}()
		}
	}
	if h.cfg.QoderURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qoderURL := *h.cfg.QoderURL
			qoderURL.Path = "/v1/models"
			qoderReq, err := http.NewRequestWithContext(r.Context(), "GET", qoderURL.String(), nil)
			if err != nil {
				return
			}
			if h.cfg.QoderKey != "" {
				qoderReq.Header.Set("Authorization", "Bearer "+h.cfg.QoderKey)
			}
			list, _ := h.fetchModels(qoderReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the qoder-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, qoderModelPrefix) {
					entry["id"] = qoderModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				qoderRaw = append(qoderRaw, tagModelRealm(tagged, "qoder"))
			}
		}()
	}
	if h.cfg.OpenCodeURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opencodeURL := *h.cfg.OpenCodeURL
			opencodeURL.Path = "/v1/models"
			opencodeReq, err := http.NewRequestWithContext(r.Context(), "GET", opencodeURL.String(), nil)
			if err != nil {
				return
			}
			// OW Bridge guards every route (incl. /v1/models) with Bearer auth.
			if h.cfg.OpenCodeKey != "" {
				opencodeReq.Header.Set("Authorization", "Bearer "+h.cfg.OpenCodeKey)
			}
			list, _ := h.fetchModels(opencodeReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the opencode-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, opencodeModelPrefix) {
					entry["id"] = opencodeModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				opencodeRaw = append(opencodeRaw, tagModelRealm(tagged, "opencode"))
			}
		}()
	}
	wg.Wait()
	zcodeRaw = filterZcodeModels(zcodeRaw, zcodePlan)
	coreList, ok := parseModelList(coreResp)
	if !ok {
		if coreResp.status == 0 {
			adminError(w, 503, "核心服务不可达，请稍后重试")
			return
		}
		if ct := coreResp.contentType; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(coreResp.status)
		w.Write(coreResp.body)
		return
	}
	totalLen := len(coreList) + len(zcodeRaw) + len(qoderRaw) + len(opencodeRaw)
	seen := make(map[string]bool, totalLen)
	merged := make([]json.RawMessage, 0, totalLen)
	all := append(append([]json.RawMessage{}, coreList...), zcodeRaw...)
	all = append(all, qoderRaw...)
	all = append(all, opencodeRaw...)
	// 用刚拿到的 core 目录刷新倍率缓存，省掉 modelGate 判定的一次重复抓取。
	if h.credits != nil {
		h.credits.store(creditRulesFromList(coreList))
	}
	for _, item := range all {
		var entry struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		if entry.Realm == "" && strings.HasPrefix(entry.ID, zcodeModelPrefix) {
			item = tagModelRealm(item, "glm")
		}
		// 隐藏语义：停用（含默认停用）的模型不出现在公共 /v1/models 里，
		// 与对话出口的拒绝共用 modelGate 同一口径。
		if !h.modelGate(r.Context(), entry.ID) {
			continue
		}
		merged = append(merged, item)
	}
	// auto 是 console 自建的虚拟模型：它不在任何上游目录里，但客户端需要能在
	// /v1/models 里看到并选中它，否则各 agent 无法把它写进配置。路由未启用
	// （没有可写配置）时不能暴露它——那时没有解析器，请求会原样落到 core。
	if h.routes != nil && !seen[autoModelName] {
		if virtual, err := json.Marshal(map[string]any{
			"id":       autoModelName,
			"object":   "model",
			"owned_by": "prism",
			"realm":    "auto",
			"virtual":  true,
		}); err == nil {
			seen[autoModelName] = true
			merged = append(merged, virtual)
		}
	}
	writeJSON(w, 200, modelsEnvelope{Object: "list", Data: merged})
}

// adminZcodeStatus reports the optional GLM channel for the console Zcode tab.
// An unreachable upstream is still a successful status answer with reachable:false.
func (h *server) adminZcodeStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	var control map[string]any
	if h.cfg.ZCodeControlURL != nil {
		control = h.zcodeControlStatus(r.Context())
	}
	plan, _ := control["plan"].(string)
	target := *h.cfg.ZCodeURL
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, "Zcode 状态探测请求构造失败")
		return
	}
	if h.cfg.ZCodeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	models := []map[string]string{}
	reachable := false
	if list, ok := parseModelList(response); ok {
		reachable = true
		for _, item := range filterZcodeModels(list, plan) {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item, &entry) == nil && entry.ID != "" {
				models = append(models, map[string]string{"id": entry.ID, "realm": "glm"})
			}
		}
	}
	out := map[string]any{"enabled": true, "reachable": reachable, "model_count": len(models), "models": models}
	// 手工测速探测的结论以 health.modelResults 形态下发，前端模型表原样复用既有渲染。
	if health := h.probeHealth(channelGLM); health != nil {
		out["health"] = health
	}
	for k, v := range control {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// adminZcodeChat proxies the console test dialog straight to zcode-proxy. The glm-* prefix
// check keeps this endpoint from becoming a general-purpose forwarding surface; zcodeProxy
// strips management cookies, CSRF and the deployment API key before the request leaves this process.
func (h *server) adminZcodeChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeURL == nil {
		adminError(w, 409, "GLM 通道未启用")
		return
	}
	route, handled := h.routesToZCode(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "Zcode 测试仅支持 glm- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.zcodeProxy().ServeHTTP(w, req)
}

func parseModelList(response upstreamResponse) ([]json.RawMessage, bool) {
	if response.status != 200 {
		return nil, false
	}
	var envelope modelsEnvelope
	if json.Unmarshal(response.body, &envelope) != nil || envelope.Data == nil {
		return nil, false
	}
	return envelope.Data, true
}

// fetchUpstream captures a complete small response; status 0 means the upstream was unreachable.
func (h *server) fetchUpstream(req *http.Request) upstreamResponse {
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return upstreamResponse{}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, zcodeModelsBodyLimit+1))
	if err != nil || len(body) > zcodeModelsBodyLimit {
		return upstreamResponse{status: response.StatusCode}
	}
	return upstreamResponse{status: response.StatusCode, contentType: response.Header.Get("Content-Type"), body: body}
}

func (h *server) fetchModels(req *http.Request) ([]json.RawMessage, error) {
	response := h.fetchUpstream(req)
	list, ok := parseModelList(response)
	if !ok {
		return nil, errors.New("models upstream unavailable")
	}
	return list, nil
}
