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

const qoderModelPrefix = "qoder-"
const qoderRouteBodyLimit = 32 << 20

func (h *server) qoderProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.QoderURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			if h.cfg.QoderKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.QoderKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "Qoder 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "Qoder 服务不可达，请稍后重试")
		},
	}
}

func stripQoderModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, qoderModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, qoderModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToQoder(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, qoderRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > qoderRouteBodyLimit {
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
	if !strings.HasPrefix(envelope.Model, qoderModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripQoderModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

func (h *server) adminQoderStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.QoderURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	target := *h.cfg.QoderURL
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, "Qoder 状态探测请求构造失败")
		return
	}
	if h.cfg.QoderKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.QoderKey)
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
				if !strings.HasPrefix(id, qoderModelPrefix) {
					id = qoderModelPrefix + id
				}
				models = append(models, map[string]string{"id": id, "realm": "qoder"})
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"enabled": true, "reachable": reachable,
		"model_count": len(models), "models": models,
	})
}

func (h *server) adminQoderChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.QoderURL == nil {
		adminError(w, 409, "Qoder 通道未启用")
		return
	}
	route, handled := h.routesToQoder(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "Qoder 测试仅支持 qoder- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.qoderProxy().ServeHTTP(w, req)
}
