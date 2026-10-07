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
	"net/url"
	"strings"
	"time"
)

// traeModelPrefix 把 model 名以该前缀开头的请求分流到可选的 trae sidecar。
// core 用 cn:/global: 命名空间，glm-/qoder-/opencode-/autoclaw- 前缀互不相同，
// 因此不会撞名。sidecar 目录里的模型是裸名（如 glm-5.2），公共形态加该前缀。
const traeModelPrefix = "trae-"

// traeRouteBodyLimit 限制分流检查时缓冲的请求体大小。
const traeRouteBodyLimit = 32 << 20

// traeCtlTimeout 覆盖状态/登录/账号类控制调用；签到与 Token 刷新逐账号访问上游，
// 故另用更长的 traeTaskTimeout。
const (
	traeCtlTimeout  = 15 * time.Second
	traeTaskTimeout = 180 * time.Second
)

var errTraeControlUnreachable = errors.New("trae control unreachable")

func (h *server) traeProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.TraeURL)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			// trae sidecar 用 withAuth 保护 /v1/* 与 /status；只有桥接密钥可以跨这一跳，
			// 部署 API Key 绝不外传。
			if h.cfg.TraeKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.TraeKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "Trae 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "Trae 服务不可达，请稍后重试")
		},
	}
}

func stripTraeModelPrefix(body []byte) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, traeModelPrefix) {
		envelope["model"] = strings.TrimPrefix(model, traeModelPrefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

func (h *server) routesToTrae(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, traeRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > traeRouteBodyLimit {
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
	if !strings.HasPrefix(envelope.Model, traeModelPrefix) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		return false, false
	}
	modified := stripTraeModelPrefix(body)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

// traeHealth 探测 trae sidecar /healthz。该端点返回纯文本 "ok"（非 JSON），
// 与 autoclaw bridge 的 {healthy,total} 不同，故只做纯文本判定。
func (h *server) traeHealth(ctx context.Context) bool {
	target := *h.cfg.TraeURL
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

// adminTraeStatus 汇总可选 Trae 通道供页签展示。sidecar 不可达仍是成功应答，
// 只是 reachable:false。/v1/models 与 /status 走 withAuth，需带桥接密钥。
func (h *server) adminTraeStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.TraeURL == nil {
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
	if h.traeHealth(r.Context()) {
		result["reachable"] = true
	}
	if out, code, err := h.traeCtl(r.Context(), "GET", "/v1/models", nil, traeCtlTimeout); err == nil && code == 200 {
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
				if !strings.HasPrefix(id, traeModelPrefix) {
					id = traeModelPrefix + id
				}
				models = append(models, map[string]string{"id": id, "realm": "trae"})
			}
		}
		result["models"] = models
		result["model_count"] = len(models)
		result["reachable"] = true
	}
	if out, code, err := h.traeCtl(r.Context(), "GET", "/status", nil, traeCtlTimeout); err == nil && code == 200 {
		if accounts, ok := out["accounts"]; ok && accounts != nil {
			result["accounts"] = accounts
		}
	}
	// /status 的账号条目来自 pool.Status（含冷却与软开关），但没有凭据有效期；
	// 这里补一次账号列表，把 expires_at / expired_soon / has_auth 按 uid 合并回去。
	// 任一侧失败都只降级（缺字段），不影响主响应。
	if out, code, err := h.traeCtl(r.Context(), "GET", "/admin/api/accounts", nil, traeCtlTimeout); err == nil && code == 200 {
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
	if health := h.probeHealth(channelTrae); health != nil {
		result["health"] = health
	}
	writeJSON(w, 200, result)
}

func (h *server) adminTraeChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.TraeURL == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	route, handled := h.routesToTrae(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "Trae 测试仅支持 trae- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.traeProxy().ServeHTTP(w, req)
}

// traeCtl 调用 trae sidecar 的控制端点。sidecar 与 console 共享网络命名空间且
// 仅监听回环，console 注入 Bearer 桥接密钥；与 zcode/qoder 控制链路同构，console
// 不持有 Docker 权限。pth 可带查询串。
func (h *server) traeCtl(ctx context.Context, method, pth string, payload any, timeout time.Duration) (map[string]any, int, error) {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	return h.traeCtlRaw(ctx, method, pth, raw, timeout)
}

// traeCtlRaw 是 traeCtl 的原样字节版本：账号导入的 body 可能超过 decodeAdmin 的
// 8192 字节上限，需原样转发。
func (h *server) traeCtlRaw(ctx context.Context, method, pth string, raw []byte, timeout time.Duration) (map[string]any, int, error) {
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	target := *h.cfg.TraeURL
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
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cfg.TraeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.TraeKey)
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, 0, errTraeControlUnreachable
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, errTraeControlUnreachable
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errTraeControlUnreachable
	}
	return out, response.StatusCode, nil
}

// traeForward 把控制端点的 JSON 结果下发。trae sidecar 用标准 HTTP 状态码表达业务
// 结果（400 参数错、404 pending/账号不存在、409 导入遇更新的旧凭证），这些都是正常
// 业务语义，需原样透传；只有传输失败是 503，5xx 收敛成 502 并带出上游消息。
func (h *server) traeForward(w http.ResponseWriter, r *http.Request, method, pth string, payload any, timeout time.Duration) {
	if h.cfg.TraeURL == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	out, code, err := h.traeCtl(r.Context(), method, pth, payload, timeout)
	if err != nil {
		adminError(w, 503, "Trae 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, traeCtlMessage(out, "Trae 控制请求失败"))
		return
	}
	writeJSON(w, code, out)
}

// traeCtlMessage 从 trae 的 OpenAI 风格错误包 {"error":{"message":...}} 取消息，
// 兼容纯字符串 {"error":"..."} 形态。
func traeCtlMessage(out map[string]any, fallback string) string {
	if errObj, ok := out["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := out["error"].(string); ok && msg != "" {
		return msg
	}
	return fallback
}

// traeCallbackBase 决定 TRAE 登录回调落点：优先显式公开 origin，否则按当前请求的
// scheme+Host 推断，保证本地明文部署也能把回调导回 console。
func (h *server) traeCallbackBase(r *http.Request) string {
	if h.cfg.PublicOrigin != "" {
		return h.cfg.PublicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// rewriteTraeCallback 把 sidecar 返回的 login_url 里的 auth_callback_url 查询参数
// 改写为 console 公开地址下的 /admin/trae/authorize。sidecar 的 adminLoginStart 只
// 接受 callback_port 并硬编码拼出 127.0.0.1:<port>/authorize，而该端口在 overlay 下
// 已被 TRAE_CALLBACK_LISTEN=0 关闭，故由 console 侧重写查询参数完成回调导流。
func (h *server) rewriteTraeCallback(loginURL string, r *http.Request) string {
	u, err := url.Parse(loginURL)
	if err != nil {
		return loginURL
	}
	q := u.Query()
	q.Set("auth_callback_url", h.traeCallbackBase(r)+"/admin/trae/authorize")
	u.RawQuery = q.Encode()
	return u.String()
}

// adminTraeAuthorize 是 TRAE 浏览器 302 的公开落点，无管理会话与 CSRF 校验
// （TRAE 域跳转不带 console Cookie），与 sidecar /authorize 语义一致。只把回调
// query 转发给 sidecar，由它完成 ExchangeToken 与落盘。
func (h *server) adminTraeAuthorize(w http.ResponseWriter, r *http.Request) {
	if h.cfg.TraeURL == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	target := *h.cfg.TraeURL
	target.Path = "/authorize"
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, "Trae 回调转发请求构造失败")
		return
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		adminError(w, 503, "Trae 容器不可达")
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		adminError(w, 502, "Trae 回调响应读取失败")
		return
	}
	if ct := response.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(raw)
}

// adminTraeLoginStart 生成登录 URL + pending 态（sidecar /admin/api/login），并把
// 返回的 login_url/callback_url 里的回调落点改写为 console 公开地址。
func (h *server) adminTraeLoginStart(w http.ResponseWriter, r *http.Request) {
	if h.cfg.TraeURL == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	out, code, err := h.traeCtl(r.Context(), "POST", "/admin/api/login", map[string]any{}, traeCtlTimeout)
	if err != nil {
		adminError(w, 503, "Trae 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, traeCtlMessage(out, "Trae 登录请求失败"))
		return
	}
	if code != 200 {
		writeJSON(w, code, out)
		return
	}
	callbackURL := h.traeCallbackBase(r) + "/admin/trae/authorize"
	if loginURL, ok := out["login_url"].(string); ok && loginURL != "" {
		out["login_url"] = h.rewriteTraeCallback(loginURL, r)
	}
	out["callback_url"] = callbackURL
	writeJSON(w, 200, out)
}

// adminTraeLoginPoll 轮询单次登录结果（sidecar /admin/api/login/result）。
func (h *server) adminTraeLoginPoll(w http.ResponseWriter, r *http.Request) {
	pendingID := r.URL.Query().Get("pending_id")
	if pendingID == "" {
		adminError(w, 400, "缺少 pending_id")
		return
	}
	h.traeForward(w, r, "GET", "/admin/api/login/result?pending_id="+url.QueryEscape(pendingID), nil, traeCtlTimeout)
}

// adminTraeLoginCancel 取消单次登录（sidecar /admin/api/login/cancel）。
func (h *server) adminTraeLoginCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PendingID string `json:"pending_id"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	h.traeForward(w, r, "POST", "/admin/api/login/cancel", map[string]any{"pending_id": body.PendingID}, traeCtlTimeout)
}

// adminTraeImport 导入凭证（sidecar /admin/api/accounts/import）。body 原样转发：
// 支持纯回调链接字符串或 {callback_url,json,machine_id,device_id} JSON，后者可能
// 超过 decodeAdmin 的 8192 字节上限。同 uid 已有更新的凭证时 sidecar 返回 409
// skipped_existing_newer，由 traeForward 原样透传。
func (h *server) adminTraeImport(w http.ResponseWriter, r *http.Request) {
	if h.cfg.TraeURL == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return
	}
	out, code, err := h.traeCtlRaw(r.Context(), "POST", "/admin/api/accounts/import", raw, traeCtlTimeout)
	if err != nil {
		adminError(w, 503, "Trae 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, traeCtlMessage(out, "Trae 导入请求失败"))
		return
	}
	writeJSON(w, code, out)
}

// adminTraeAccountsDelete 删除账号凭据（sidecar DELETE /admin/api/accounts/{uid}）。
func (h *server) adminTraeAccountsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID string `json:"uid"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if body.UID == "" {
		adminError(w, 400, "缺少 uid")
		return
	}
	h.traeForward(w, r, "DELETE", "/admin/api/accounts/"+url.PathEscape(body.UID), nil, traeCtlTimeout)
}

// adminTraeAccountsPatch 启用/停用或改名账号（sidecar PATCH /admin/api/accounts/{uid}）。
func (h *server) adminTraeAccountsPatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID      string `json:"uid"`
		Enabled  *bool  `json:"enabled"`
		Nickname string `json:"nickname"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if body.UID == "" {
		adminError(w, 400, "缺少 uid")
		return
	}
	payload := map[string]any{}
	if body.Enabled != nil {
		payload["enabled"] = *body.Enabled
	}
	if body.Nickname != "" {
		payload["nickname"] = body.Nickname
	}
	h.traeForward(w, r, "PATCH", "/admin/api/accounts/"+url.PathEscape(body.UID), payload, traeCtlTimeout)
}

// adminTraeAccountsRefresh 手动 ExchangeToken 刷新单账号（sidecar POST .../refresh）。
func (h *server) adminTraeAccountsRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID string `json:"uid"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if body.UID == "" {
		adminError(w, 400, "缺少 uid")
		return
	}
	h.traeForward(w, r, "POST", "/admin/api/accounts/"+url.PathEscape(body.UID)+"/refresh", map[string]any{}, traeTaskTimeout)
}

// adminTraeAccountJSON 脱敏预览单账号凭证（sidecar GET .../json）。
func (h *server) adminTraeAccountJSON(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		adminError(w, 400, "缺少 uid")
		return
	}
	h.traeForward(w, r, "GET", "/admin/api/accounts/"+url.PathEscape(uid)+"/json", nil, traeCtlTimeout)
}

// adminTraeSchedule 读取签到/刷新排程状态（sidecar /admin/api/schedule）。
func (h *server) adminTraeSchedule(w http.ResponseWriter, r *http.Request) {
	h.traeForward(w, r, "GET", "/admin/api/schedule", nil, traeCtlTimeout)
}

// adminTraeScheduleCheckin 手动触发一轮签到（sidecar /admin/api/schedule/checkin）。
func (h *server) adminTraeScheduleCheckin(w http.ResponseWriter, r *http.Request) {
	h.traeForward(w, r, "POST", "/admin/api/schedule/checkin", map[string]any{}, traeTaskTimeout)
}

// adminTraeScheduleRefresh 手动触发一轮 Token 刷新（sidecar /admin/api/schedule/refresh）。
func (h *server) adminTraeScheduleRefresh(w http.ResponseWriter, r *http.Request) {
	h.traeForward(w, r, "POST", "/admin/api/schedule/refresh", map[string]any{}, traeTaskTimeout)
}

// adminTraeCredits 读取各账号积分与签到状态（sidecar /admin/api/credits，无鉴权）。
func (h *server) adminTraeCredits(w http.ResponseWriter, r *http.Request) {
	h.traeForward(w, r, "GET", "/admin/api/credits", nil, traeTaskTimeout)
}

// adminTraeUsage 读取 trae 请求用量（sidecar /admin/api/usage，无鉴权）。
func (h *server) adminTraeUsage(w http.ResponseWriter, r *http.Request) {
	h.traeForward(w, r, "GET", "/admin/api/usage", nil, traeCtlTimeout)
}
