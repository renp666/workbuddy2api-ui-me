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
// and the merged model list go to zcode-proxy, everything else stays on core.
func (h *server) publicRouter(core, zcode *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			h.mergedModels(w, r, core)
			return
		case "POST /v1/chat/completions", "POST /v1/messages", "POST /v1/responses":
			route, handled := h.routesToZCode(w, r)
			if handled {
				return
			}
			if route {
				zcode.ServeHTTP(w, r)
				return
			}
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
	zcodeURL := *h.cfg.ZCodeURL
	zcodeURL.Path = "/v1/models"
	zcodeReq, err := http.NewRequestWithContext(r.Context(), "GET", zcodeURL.String(), nil)
	if err != nil {
		core.ServeHTTP(w, r)
		return
	}
	if h.cfg.ZCodeKey != "" {
		zcodeReq.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
	}
	var (
		wg        sync.WaitGroup
		coreResp  upstreamResponse
		zcodeRaw  []json.RawMessage
		zcodePlan string
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		coreResp = h.fetchUpstream(coreReq)
	}()
	go func() {
		defer wg.Done()
		zcodeRaw, _ = h.fetchModels(zcodeReq)
	}()
	if h.cfg.ZCodeControlURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zcodePlan = h.zcodePlan(r.Context())
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
	seen := make(map[string]bool, len(coreList)+len(zcodeRaw))
	merged := make([]json.RawMessage, 0, len(coreList)+len(zcodeRaw))
	for _, item := range append(append([]json.RawMessage{}, coreList...), zcodeRaw...) {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		if strings.HasPrefix(entry.ID, zcodeModelPrefix) {
			item = tagModelRealm(item, "glm") // glm-* 条目来自第三方上游，console 补归属标签
		}
		merged = append(merged, item)
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
