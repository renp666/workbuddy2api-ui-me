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

// raccoonModelPrefix 把 model 名以该前缀开头的请求分流到可选的 raccoon sidecar
// （商汤小浣熊）。core 用 cn:/global: 命名空间，glm-/qoder-/opencode-/autoclaw-/trae-
// 前缀互不相同，因此不会撞名。sidecar 目录里的模型是裸名，公共形态加该前缀。
const raccoonModelPrefix = "raccoon-"

// raccoonRouteBodyLimit 限制分流检查时缓冲的请求体大小。
const raccoonRouteBodyLimit = 32 << 20

// raccoonCtlTimeout 覆盖状态/账号类控制调用；凭据刷新逐账号访问上游，故另用更长的
// raccoonTaskTimeout。
const (
	raccoonCtlTimeout  = 15 * time.Second
	raccoonTaskTimeout = 180 * time.Second
)

func (h *server) raccoonProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.RaccoonURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			// raccoon2api 用桥接密钥保护 /v1/* 与 /status；只有桥接密钥可以跨这一跳，
			// 部署 API Key 绝不外传。
			if h.cfg.RaccoonKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.RaccoonKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "Raccoon 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "Raccoon 服务不可达，请稍后重试")
		},
	}
}

func stripRaccoonModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, raccoonModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, raccoonModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToRaccoon(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, raccoonRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > raccoonRouteBodyLimit {
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
	if !strings.HasPrefix(envelope.Model, raccoonModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripRaccoonModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

// raccoonHealth 探测 raccoon sidecar /healthz。与 trae 一致，该端点返回纯文本 "ok"。
func (h *server) raccoonHealth(ctx context.Context) bool {
	target := *h.cfg.RaccoonURL
	target.Path = "/healthz"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	return response.status == 200 && strings.TrimSpace(string(response.body)) == "ok"
}

// adminRaccoonStatus 汇总可选 Raccoon 通道供页签展示。sidecar 不可达仍是成功应答，
// 只是 reachable:false。/v1/models 与 /status 走桥接密钥鉴权。
func (h *server) adminRaccoonStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.RaccoonURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	result := map[string]any{
		"enabled":     true,
		"reachable":   false,
		"model_count": 0,
		"models":      []map[string]any{},
		"accounts":    []any{},
	}
	if h.raccoonHealth(r.Context()) {
		result["reachable"] = true
	}
	if out, code, err := h.raccoonCtl(r.Context(), "GET", "/v1/models", nil, raccoonCtlTimeout); err == nil && code == 200 {
		models := []map[string]any{}
		if data, ok := out["data"].([]any); ok {
			for _, item := range data {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				id, _ := entry["id"].(string)
				if id == "" {
					continue
				}
				if !strings.HasPrefix(id, raccoonModelPrefix) {
					id = raccoonModelPrefix + id
				}
				// sidecar 的目录条目带 name（上游真实展示名）；透传给页面，
				// 前端优先用它做行标签，id 仍作选路键。
				model := map[string]any{"id": id, "realm": "raccoon"}
				if name, ok := entry["name"].(string); ok && name != "" {
					model["name"] = name
				}
				models = append(models, model)
			}
		}
		result["models"] = models
		result["model_count"] = len(models)
		result["reachable"] = true
	}
	if out, code, err := h.raccoonCtl(r.Context(), "GET", "/status", nil, raccoonCtlTimeout); err == nil && code == 200 {
		if accounts, ok := out["accounts"]; ok && accounts != nil {
			result["accounts"] = accounts
		}
	}
	// /status 的账号条目来自池状态（含冷却与软开关），但没有凭据有效期；
	// 这里补一次账号列表，把 expires_at / expired_soon / has_auth 按 uid 合并回去。
	// 任一侧失败都只降级（缺字段），不影响主响应。
	if out, code, err := h.raccoonCtl(r.Context(), "GET", "/admin/api/accounts", nil, raccoonCtlTimeout); err == nil && code == 200 {
		if extra, ok := out["accounts"].([]any); ok {
			byUID := make(map[string]map[string]any, len(extra))
			for _, item := range extra {
				account, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if uid, ok := account["uid"].(string); ok && uid != "" {
					byUID[uid] = account
				}
			}
			if accounts, ok := result["accounts"].([]any); ok {
				for _, item := range accounts {
					account, ok := item.(map[string]any)
					if !ok {
						continue
					}
					uid, ok := account["uid"].(string)
					if !ok {
						continue
					}
					source, ok := byUID[uid]
					if !ok {
						continue
					}
					for _, key := range []string{"expires_at", "expired_soon", "has_auth"} {
						if value, present := source[key]; present {
							account[key] = value
						}
					}
				}
			}
		}
	}
	// 手工测速探测的结论以 health.modelResults 形态下发，前端模型表原样复用既有渲染。
	if health := h.probeHealth(channelRaccoon); health != nil {
		result["health"] = health
	}
	writeJSON(w, 200, result)
}

func (h *server) adminRaccoonChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.RaccoonURL == nil {
		adminError(w, 409, "Raccoon 通道未启用")
		return
	}
	route, handled := h.routesToRaccoon(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "Raccoon 测试仅支持 raccoon- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.raccoonProxy().ServeHTTP(w, req)
}
