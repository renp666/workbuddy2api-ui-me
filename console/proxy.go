package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

type coreInfo struct {
	Protocol      int  `json:"protocol"`
	GlobalEnabled bool `json:"global_enabled"`
}

func (h *server) bridgeRequest(ctx context.Context, method, path, owner string) (*http.Response, error) {
	target := *h.cfg.CoreURL
	target.Path = path
	r, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+h.cfg.BridgeKey)
	if owner != "" {
		r.Header.Set("X-Console-Owner", owner)
	}
	return h.client.Do(r)
}
func (h *server) coreInfo(ctx context.Context) (coreInfo, error) {
	var info coreInfo
	response, err := h.bridgeRequest(ctx, "GET", "/internal/v1/info", "")
	if err != nil {
		return info, errors.New("核心服务不可达，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return info, errors.New("核心管理协议探测失败，请检查服务版本与桥接配置")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(body) > 8192 || json.Unmarshal(body, &info) != nil || info.Protocol != 1 {
		return coreInfo{}, errors.New("核心管理协议版本不兼容，请同步升级 console 与 core")
	}
	return info, nil
}

// coreCtl 调 core 的只读管理端点（经桥接密钥鉴权，不带 owner，因此只能取全局快照，
// 不能动账号池）。body 上限 1 MiB：账号池状态只含汇总字段。
//
// owner 传空与 raccoonCtl/trae 控制链路的差别在于：这里不注入 X-Console-Owner，
// core 就不会按 owner 过滤，返回的是整池状态。只在失败时调用（见 corePinHint），
// 不在正常路径上打 core。
func (h *server) coreCtl(ctx context.Context, method, path string, timeout time.Duration) (map[string]any, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := h.bridgeRequest(ctx, method, path, "")
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if len(raw) == 0 {
		return nil, response.StatusCode, nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errors.New("core 返回了非 JSON")
	}
	return out, response.StatusCode, nil
}
func stripPrivateHeaders(headers http.Header) {
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-console-") || strings.HasPrefix(lower, "x-bridge-") || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-prism-") {
			headers.Del(name)
		}
	}
	for _, name := range []string{"Forwarded", "X-Real-IP", "Cookie", "X-CSRF-Token"} {
		headers.Del(name)
	}
}
func (h *server) proxy(management bool) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.CoreURL)
			stripPrivateHeaders(pr.Out.Header)
			// stripPrivateHeaders already dropped any inbound value, so core may treat
			// this one as console-attested; core masks it to a netblock on write.
			if addr := h.clientAddress(pr.In); addr != "" {
				pr.Out.Header.Set("X-Console-Client", addr)
			}
			if management {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.BridgeKey)
				pr.Out.Header.Set("X-Console-Owner", sessionFrom(pr.In).owner)
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !management && r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "核心服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "核心服务不可达，请稍后重试")
		},
	}
}
func (h *server) management(method, path string) http.HandlerFunc {
	proxy := h.proxy(true)
	return func(w http.ResponseWriter, r *http.Request) {
		oauthControl := strings.HasPrefix(path, "/internal/v1/oauth")
		if oauthControl {
			session := sessionFrom(r)
			select {
			case session.oauthGate <- struct{}{}:
				defer func() { <-session.oauthGate }()
			case <-r.Context().Done():
				adminError(w, 401, "管理会话已过期，请重新登录")
				return
			}
			if !h.sessionActive(r) {
				adminError(w, 401, "管理会话已过期，请重新登录")
				return
			}
			// Finish admitted control before owner cleanup; chat still uses the client context.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		if _, err := h.coreInfo(r.Context()); err != nil {
			adminError(w, 503, err.Error())
			return
		}
		if !h.sessionActive(r) {
			adminError(w, 401, "管理会话已过期，请重新登录")
			return
		}
		req := r.Clone(r.Context())
		req.Method = method
		req.URL.Path = strings.ReplaceAll(path, "{id}", r.PathValue("id"))
		req.URL.RawPath = ""
		req.RequestURI = ""
		if method == "GET" {
			req.Body = nil
			req.ContentLength = 0
			req.TransferEncoding = nil
			req.Header.Del("Content-Length")
			req.Header.Del("Content-Type")
		}
		proxy.ServeHTTP(w, req)
	}
}

func (h *server) sessionActive(r *http.Request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sessionID(r)]
	return s != nil && time.Now().Before(s.expires) && s.owner == sessionFrom(r).owner
}
