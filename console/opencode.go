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

// opencodeModelPrefix routes model names starting with this prefix to the optional
// opencode sidecar (OW Bridge). Upstream ids look like "OC · <name>", so the public
// form is "opencode-OC · <name>"; core model ids are "cn:"/"global:" namespaced,
// and glm-/qoder- prefixes are distinct, so there is no collision.
const opencodeModelPrefix = "opencode-"

// opencodeRouteBodyLimit bounds the buffered request body we inspect for the model field.
const opencodeRouteBodyLimit = 32 << 20

func (h *server) opencodeProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.OpenCodeURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			// OW Bridge rejects any request carrying an Origin header (server-side 403),
			// so browser-originated calls must not forward it.
			pr.Out.Header.Del("Origin")
			if h.cfg.OpenCodeKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.OpenCodeKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "OpenCode 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "OpenCode 服务不可达，请稍后重试")
		},
	}
}

func stripOpenCodeModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, opencodeModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, opencodeModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToOpenCode(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, opencodeRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > opencodeRouteBodyLimit {
		adminError(w, 413, "请求体超过 32 MiB 分流检查上限")
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
	if !strings.HasPrefix(envelope.Model, opencodeModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripOpenCodeModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

// adminOpenCodeStatus reports the optional OpenCode channel for the console tab.
// An unreachable upstream is still a successful status answer with reachable:false.
// health 透传 OW Bridge /health 的运行状态（phase/version 等），失败则整体缺省。
func (h *server) adminOpenCodeStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.OpenCodeURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	target := *h.cfg.OpenCodeURL
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, "OpenCode 状态探测请求构造失败")
		return
	}
	if h.cfg.OpenCodeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.OpenCodeKey)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	models := []map[string]string{}
	reachable := false
	if list, ok := parseModelList(response); ok {
		reachable = true
		for _, item := range list {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item, &entry) == nil && entry.ID != "" {
				id := entry.ID
				if !strings.HasPrefix(id, opencodeModelPrefix) {
					id = opencodeModelPrefix + id
				}
				models = append(models, map[string]string{"id": id, "realm": "opencode"})
			}
		}
	}
	result := map[string]any{
		"enabled": true, "reachable": reachable,
		"model_count": len(models), "models": models,
	}
	if health := h.opencodeHealth(r.Context()); health != nil {
		result["health"] = health
	}
	writeJSON(w, 200, result)
}

// opencodeHealth fetches OW Bridge /health (full state object); nil on any failure.
func (h *server) opencodeHealth(ctx context.Context) map[string]any {
	target := *h.cfg.OpenCodeURL
	target.Path = "/health"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil
	}
	if h.cfg.OpenCodeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.OpenCodeKey)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	if response.status != 200 {
		return nil
	}
	var state map[string]any
	if json.Unmarshal(response.body, &state) != nil || state == nil {
		return nil
	}
	return state
}

func (h *server) adminOpenCodeChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.OpenCodeURL == nil {
		adminError(w, 409, "OpenCode 通道未启用")
		return
	}
	route, handled := h.routesToOpenCode(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "OpenCode 测试仅支持 opencode- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.opencodeProxy().ServeHTTP(w, req)
}
