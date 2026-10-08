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
	"time"
)

// autoclawModelPrefix routes model names starting with this prefix to the optional
// autoclaw2api sidecar (Python bridge). Core ids are "cn:"/"global:" namespaced, and
// the glm-/qoder-/opencode- prefixes are distinct, so there is no collision. The bridge
// already namespaces global models as "global/<name>", so the public form there is
// "autoclaw-global/<name>".
const autoclawModelPrefix = "autoclaw-"

// autoclawRouteBodyLimit bounds the buffered request body we inspect for the model field.
const autoclawRouteBodyLimit = 32 << 20

// autoclawCtlTimeout 覆盖状态/登录/账号类控制调用；签到与维护逐账号调用上游，
// 单个账号超时即 20 秒，故另用更长的 autoclawTaskTimeout。
const (
	autoclawCtlTimeout  = 15 * time.Second
	autoclawTaskTimeout = 180 * time.Second
)

var errAutoClawControlUnreachable = errors.New("autoclaw control unreachable")

func (h *server) autoclawProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.AutoClawURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			// autoclaw2api guards /v1/* and /api/* with the bridge key; only that key
			// may cross this hop, never the deployment API key.
			if h.cfg.AutoClawKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.AutoClawKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "AutoClaw 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "AutoClaw 服务不可达，请稍后重试")
		},
	}
}

func stripAutoClawModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, autoclawModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, autoclawModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToAutoClaw(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, autoclawRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > autoclawRouteBodyLimit {
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
	if !strings.HasPrefix(envelope.Model, autoclawModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripAutoClawModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

// adminAutoClawStatus reports the optional AutoClaw channel for the console tab.
// An unreachable bridge is still a successful status answer with reachable:false.
// bridge /healthz 免鉴权（在鉴权之前），/v1/models 与 /status 要求 Bearer 桥接密钥。
func (h *server) adminAutoClawStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AutoClawURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	result := map[string]any{
		"enabled":     true,
		"reachable":   false,
		"model_count": 0,
		"models":      []map[string]string{},
		"accounts":    []any{},
	}
	if health := h.autoclawHealth(r.Context()); health != nil {
		result["health"] = health
		result["reachable"] = true
	}
	if out, code, err := h.autoclawCtl(r.Context(), "GET", "/v1/models", nil, autoclawCtlTimeout); err == nil && code == 200 {
		models := []map[string]string{}
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
				if !strings.HasPrefix(id, autoclawModelPrefix) {
					id = autoclawModelPrefix + id
				}
				models = append(models, map[string]string{"id": id, "realm": "autoclaw"})
			}
		}
		result["models"] = models
		result["model_count"] = len(models)
		result["reachable"] = true
	}
	if out, code, err := h.autoclawCtl(r.Context(), "GET", "/status", nil, autoclawCtlTimeout); err == nil && code == 200 {
		if accounts, ok := out["accounts"]; ok && accounts != nil {
			result["accounts"] = accounts
		}
		if mode, ok := out["mode"]; ok && mode != nil {
			result["mode"] = mode
		}
	}
	// 手工测速探测的结论以 health.modelResults 形态下发，前端模型表原样复用既有渲染。
	// 与 trae 不同，autoclaw 的 health 已被 /healthz 探活快照占用（前端卡片读 healthy/total），
	// 因此这里按 key 合并而不是覆盖，两类信息共存。
	if probe := h.probeHealth(channelAutoClaw); probe != nil {
		if health, ok := result["health"].(map[string]any); ok {
			for k, v := range probe {
				health[k] = v
			}
		} else {
			result["health"] = probe
		}
	}
	writeJSON(w, 200, result)
}

// autoclawHealth fetches bridge /healthz ({healthy,total}); nil on any failure.
// The endpoint answers 503 when no account is healthy, so the status code is not the signal.
func (h *server) autoclawHealth(ctx context.Context) map[string]any {
	target := *h.cfg.AutoClawURL
	target.Path = "/healthz"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	if response.status == 0 {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(response.body, &out) != nil || out == nil {
		return nil
	}
	return out
}

func (h *server) adminAutoClawChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AutoClawURL == nil {
		adminError(w, 409, "AutoClaw 通道未启用")
		return
	}
	route, handled := h.routesToAutoClaw(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "AutoClaw 测试仅支持 autoclaw- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.autoclawProxy().ServeHTTP(w, req)
}

// autoclawCtl 调用 autoclaw2api 容器的控制端点。容器与 console 共享网络命名空间且
// 仅监听回环，console 注入 Bearer 桥接密钥；与 zcode/qoder 控制链路同构，console
// 不持有 Docker 权限。pth 可带查询串（如 /api/credits/detail?region=cn）。
func (h *server) autoclawCtl(ctx context.Context, method, pth string, payload any, timeout time.Duration) (map[string]any, int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(raw)
	}
	target := *h.cfg.AutoClawURL
	if i := strings.IndexByte(pth, '?'); i >= 0 {
		target.Path = pth[:i]
		target.RawQuery = pth[i+1:]
	} else {
		target.Path = pth
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cfg.AutoClawKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.AutoClawKey)
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, 0, errAutoClawControlUnreachable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, errAutoClawControlUnreachable
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errAutoClawControlUnreachable
	}
	return out, response.StatusCode, nil
}

// autoclawForward 把控制端点的 JSON 结果原样下发。bridge 的业务错误以 200 +
// {"error":...} 形式返回，只有传输失败或桥接密钥不符才是非 200。
func (h *server) autoclawForward(w http.ResponseWriter, r *http.Request, method, pth string, payload any, timeout time.Duration) {
	if h.cfg.AutoClawURL == nil {
		adminError(w, 409, "AutoClaw 通道未启用")
		return
	}
	out, code, err := h.autoclawCtl(r.Context(), method, pth, payload, timeout)
	if err != nil {
		adminError(w, 503, "AutoClaw 容器不可达")
		return
	}
	if code != 200 {
		adminError(w, 502, autoclawCtlMessage(out, "AutoClaw 控制请求失败"))
		return
	}
	writeJSON(w, 200, out)
}

func autoclawCtlMessage(out map[string]any, fallback string) string {
	if msg, ok := out["error"].(string); ok && msg != "" {
		return msg
	}
	return fallback
}

// adminAutoClawSendCode 发送国内手机号验证码（bridge /api/login/cn/sendcode）。
func (h *server) adminAutoClawSendCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/login/cn/sendcode", map[string]any{"phone": body.Phone}, autoclawCtlTimeout)
}

// adminAutoClawLoginCN 以手机号 + 验证码完成国内登录（bridge /api/login/cn）。
func (h *server) adminAutoClawLoginCN(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/login/cn", map[string]any{"phone": body.Phone, "code": body.Code}, autoclawCtlTimeout)
}

// adminAutoClawImportDesktop 从桌面客户端凭据导入国际版账号（bridge /api/login/global/import-desktop）。
func (h *server) adminAutoClawImportDesktop(w http.ResponseWriter, r *http.Request) {
	h.autoclawForward(w, r, "POST", "/api/login/global/import-desktop", map[string]any{}, autoclawCtlTimeout)
}

// adminAutoClawOAuth 以 OAuth 回调地址完成国际版登录（bridge /api/login/global/oauth）。
func (h *server) adminAutoClawOAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CallbackURL string `json:"callback_url"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/login/global/oauth", map[string]any{"callback_url": body.CallbackURL}, autoclawCtlTimeout)
}

// adminAutoClawAccountsToggle 启用/停用单个账号（bridge /api/accounts/toggle）。
func (h *server) adminAutoClawAccountsToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID    string `json:"uid"`
		Region string `json:"region"`
		Enable bool   `json:"enable"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/accounts/toggle", map[string]any{"uid": body.UID, "region": body.Region, "enable": body.Enable}, autoclawCtlTimeout)
}

// adminAutoClawAccountsDelete 删除账号凭据（bridge /api/accounts/delete）。
func (h *server) adminAutoClawAccountsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID    string `json:"uid"`
		Region string `json:"region"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/accounts/delete", map[string]any{"uid": body.UID, "region": body.Region}, autoclawCtlTimeout)
}

// adminAutoClawMaintain 手动触发一次积分刷新 + 会话维护（bridge /api/maintain）。
func (h *server) adminAutoClawMaintain(w http.ResponseWriter, r *http.Request) {
	h.autoclawForward(w, r, "POST", "/api/maintain", map[string]any{}, autoclawTaskTimeout)
}

// adminAutoClawCreditsRefresh 立即刷新全部账号积分（bridge /api/credits/refresh）。
func (h *server) adminAutoClawCreditsRefresh(w http.ResponseWriter, r *http.Request) {
	h.autoclawForward(w, r, "POST", "/api/credits/refresh", map[string]any{}, autoclawTaskTimeout)
}

// adminAutoClawCredits 读取某区积分明细（bridge /api/credits/detail?region=）。
func (h *server) adminAutoClawCredits(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	if region != "global" {
		region = "cn"
	}
	h.autoclawForward(w, r, "GET", "/api/credits/detail?region="+region, nil, autoclawCtlTimeout)
}

// adminAutoClawCheckin 运行签到/任务（bridge /api/checkin），逐账号调用上游故超时放宽。
func (h *server) adminAutoClawCheckin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Region string `json:"region"`
		UID    string `json:"uid"`
		DryRun bool   `json:"dry_run"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.autoclawForward(w, r, "POST", "/api/checkin", map[string]any{"region": body.Region, "uid": body.UID, "dry_run": body.DryRun}, autoclawTaskTimeout)
}
