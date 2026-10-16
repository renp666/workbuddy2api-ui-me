package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// traeModelPrefix 把 model 名以该前缀开头的请求分流到国内版 trae sidecar。
// core 用 cn:/global: 命名空间，glm-/qoder-/opencode-/autoclaw- 前缀互不相同，
// 因此不会撞名。sidecar 目录里的模型是裸名（如 glm-5.2），公共形态加该前缀。
const traeModelPrefix = "trae-"

// traeAIModelPrefix 国际版（trae.ai 上游）的公共前缀。
//
// 为什么不是 "trae-intl-"：分流用 strings.HasPrefix 判前缀，而 "trae-intl-gpt-5.4"
// 同时以 "trae-" 开头，会被国内版分支先剥成 "intl-gpt-5.4" 再打给国内上游。
// 前缀之间必须是**互不包含**的关系，所以另起一个词。
const traeAIModelPrefix = "traeai-"

// traeRouteBodyLimit 限制分流检查时缓冲的请求体大小。
const traeRouteBodyLimit = 32 << 20

// traeCtlTimeout 覆盖状态/登录/账号类控制调用；签到与 Token 刷新逐账号访问上游，
// 故另用更长的 traeTaskTimeout。
const (
	traeCtlTimeout  = 15 * time.Second
	traeTaskTimeout = 180 * time.Second
)

var errTraeControlUnreachable = errors.New("trae control unreachable")

// traeChannel 一条 Trae 区域通道的全部差异点。两只 sidecar 是同一个镜像、
// 同一套控制协议，差别只有下面这些值与"有没有签到体系"。
type traeChannel struct {
	channel routeChannel // 探测/路由/用量账本里用的通道身份
	prefix  string       // 公共模型前缀
	realm   string       // /v1/models 条目的 realm 标签
	label   string       // 面向用户的通道名（错误文案与页签标题）
	url     *url.URL     // nil = 该通道未启用
	key     string
	// checkin 国内版有每日签到与积分；国际版上游没有这两个端点（实测 404），
	// 页签因此不出现签到按钮，控制调用也不会打出去。
	checkin bool
	// note 供页签显示区域提示（SG/US 入口差异）。
	note string
}

// traeChannels 返回两条 Trae 通道（含未启用的），注册循环按 url 是否为 nil 决定挂哪些。
func (h *server) traeChannels() []traeChannel {
	return []traeChannel{
		{channel: channelTrae, prefix: traeModelPrefix, realm: "trae", label: "Trae",
			url: h.cfg.TraeURL, key: h.cfg.TraeKey, checkin: true},
		{channel: channelTraeAI, prefix: traeAIModelPrefix, realm: "traeai", label: "Trae 国际版",
			url: h.cfg.TraeAIURL, key: h.cfg.TraeAIKey, checkin: false,
			note: "上游按区域分入口（默认 SG）；权益是订阅档位，没有每日签到与积分"},
	}
}

// enabledTraeChannels 只返回已配置的通道。
func (h *server) enabledTraeChannels() []traeChannel {
	var out []traeChannel
	for _, ch := range h.traeChannels() {
		if ch.url != nil {
			out = append(out, ch)
		}
	}
	return out
}

// traeAuthorizeHint 记录"最近一次登录是哪个区域发起的"，用来把浏览器 302 回到
// console 根路径 /authorize 后转给正确的 sidecar。授权页 SSR 硬要求路径必须是
// /authorize（主机字面量 127.0.0.1、端口任意），而这条路径全局只有一个，两个区域
// 必须共用它，所以只能靠发起方来分发。缺提示时回落国内版，保持既有行为。
type traeAuthorizeHint struct {
	mu      sync.Mutex
	channel routeChannel
}

var traePendingAuthorize = &traeAuthorizeHint{channel: channelTrae}

func (t *traeAuthorizeHint) set(ch routeChannel) {
	t.mu.Lock()
	t.channel = ch
	t.mu.Unlock()
}

func (t *traeAuthorizeHint) get() routeChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.channel
}

func (h *server) traeProxy(ch traeChannel) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(ch.url)
			stripPrivateHeaders(pr.Out.Header)
			pr.Out.Header.Del("X-Api-Key")
			// trae sidecar 用 withAuth 保护 /v1/* 与 /status；只有桥接密钥可以跨这一跳，
			// 部署 API Key 绝不外传。
			if ch.key != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+ch.key)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": ch.label + " 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, ch.label+" 服务不可达，请稍后重试")
		},
	}
}

// stripTraeModelPrefix 剥掉指定通道的模型前缀。只剥自己那条的前缀，
// 否则 traeai-gpt-5.4 会被国内版剥成 intl-gpt-5.4。
func stripTraeModelPrefix(body []byte, prefix string) []byte {
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil {
		return body
	}
	if model, ok := envelope["model"].(string); ok && strings.HasPrefix(model, prefix) {
		envelope["model"] = strings.TrimPrefix(model, prefix)
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body
	}
	return out
}

// routesToTrae 判 model 前缀决定是否分流到该通道，并在分流时剥前缀。
func (h *server) routesToTrae(w http.ResponseWriter, r *http.Request, ch traeChannel) (route, handled bool) {
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
		restoreBody(r, body)
		return false, false
	}
	if !strings.HasPrefix(envelope.Model, ch.prefix) {
		restoreBody(r, body)
		return false, false
	}
	modified := stripTraeModelPrefix(body, ch.prefix)
	r.Body = io.NopCloser(bytes.NewReader(modified))
	r.ContentLength = int64(len(modified))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	return true, false
}

func restoreBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
}

// traeHealth 探测 sidecar /healthz。该端点返回纯文本 "ok"（非 JSON），
// 与 autoclaw bridge 的 {healthy,total} 不同，故只做纯文本判定。
func (h *server) traeHealth(ctx context.Context, ch traeChannel) bool {
	if ch.url == nil {
		return false
	}
	target := *ch.url
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

// adminTraeStatus 汇总通道供页签展示。sidecar 不可达仍是成功应答，
// 只是 reachable:false。/v1/models 与 /status 走 withAuth，需带桥接密钥。
func (h *server) adminTraeStatus(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ch.url == nil {
			writeJSON(w, 200, map[string]any{"enabled": false})
			return
		}
		result := map[string]any{
			"enabled":      true,
			"reachable":    false,
			"checkin":      ch.checkin,
			"model_count":  0,
			"models":       []map[string]any{},
			"accounts":     []any{},
			"edition_note": ch.note,
		}
		if h.traeHealth(r.Context(), ch) {
			result["reachable"] = true
		}
		if out, code, err := h.traeCtl(r.Context(), ch, "GET", "/v1/models", nil, traeCtlTimeout); err == nil && code == 200 {
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
					if !strings.HasPrefix(id, ch.prefix) {
						id = ch.prefix + id
					}
					model := map[string]any{"id": id, "realm": ch.realm}
					// sidecar 从上游 display_contact_config 里解出的倍率（"x0.08"）。透传给页面，
					// 「积分消耗」列复用 core 的同一套解析，缺失即未知，不补零。
					if credits, ok := entry["credits"].(string); ok && credits != "" {
						model["credits"] = credits
					}
					models = append(models, model)
				}
			}
			result["models"] = models
			result["model_count"] = len(models)
			result["reachable"] = true
		}
		if out, code, err := h.traeCtl(r.Context(), ch, "GET", "/status", nil, traeCtlTimeout); err == nil && code == 200 {
			if accounts, ok := out["accounts"]; ok && accounts != nil {
				result["accounts"] = accounts
			}
			// sidecar 自报区域与权益口径，页面据此决定显示积分还是订阅档位。
			for _, key := range []string{"edition", "edition_label", "agent_host"} {
				if v, ok := out[key]; ok {
					result[key] = v
				}
			}
		}
		// /status 的账号条目来自 pool.Status（含冷却与软开关），但没有凭据有效期；
		// 这里补一次账号列表，把 expires_at / expired_soon / has_auth 按 uid 合并回去。
		// 任一侧失败都只降级（缺字段），不影响主响应。
		if out, code, err := h.traeCtl(r.Context(), ch, "GET", "/admin/api/accounts", nil, traeCtlTimeout); err == nil && code == 200 {
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
		if health := h.probeHealth(ch.channel); health != nil {
			result["health"] = health
		}
		writeJSON(w, 200, result)
	}
}

func (h *server) adminTraeChat(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ch.url == nil {
			adminError(w, 409, ch.label+" 通道未启用")
			return
		}
		route, handled := h.routesToTrae(w, r, ch)
		if handled {
			return
		}
		if !route {
			adminError(w, 400, ch.label+" 测试仅支持 "+ch.prefix+" 前缀模型")
			return
		}
		req := r.Clone(r.Context())
		req.URL.Path = "/v1/chat/completions"
		req.URL.RawPath = ""
		req.RequestURI = ""
		h.traeProxy(ch).ServeHTTP(w, req)
	}
}

// traeCtl 调用 sidecar 的控制端点。sidecar 与 console 共享网络命名空间且
// 仅监听回环，console 注入 Bearer 桥接密钥；与 zcode/qoder 控制链路同构，console
// 不持有 Docker 权限。pth 可带查询串。
func (h *server) traeCtl(ctx context.Context, ch traeChannel, method, pth string, payload any, timeout time.Duration) (map[string]any, int, error) {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	return h.traeCtlRaw(ctx, ch, method, pth, raw, timeout)
}

// traeCtlRaw 是 traeCtl 的原样字节版本：账号导入的 body 可能超过 decodeAdmin 的
// 8192 字节上限，需原样转发（storage.json 全文可达 20KB 以上）。
func (h *server) traeCtlRaw(ctx context.Context, ch traeChannel, method, pth string, raw []byte, timeout time.Duration) (map[string]any, int, error) {
	if ch.url == nil {
		return nil, 0, errTraeControlUnreachable
	}
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	target := *ch.url
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
	if ch.key != "" {
		req.Header.Set("Authorization", "Bearer "+ch.key)
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
func (h *server) traeForward(w http.ResponseWriter, r *http.Request, ch traeChannel, method, pth string, payload any, timeout time.Duration) {
	if ch.url == nil {
		adminError(w, 409, ch.label+" 通道未启用")
		return
	}
	out, code, err := h.traeCtl(r.Context(), ch, method, pth, payload, timeout)
	if err != nil {
		adminError(w, 503, ch.label+" 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, traeCtlMessage(out, ch.label+" 控制请求失败"))
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

// traeDefaultCallbackPort 在请求 Host 不带端口时的兜底回调端口，与 console 默认
// 监听端口一致。
const traeDefaultCallbackPort = "7863"

// traeCallbackBase 决定 TRAE 登录回调落点。TRAE 授权页在服务端（SSR）校验
// auth_callback_url 的形状：主机必须是字面量 127.0.0.1、路径必须是 /authorize，
// 端口任意。实测（2026-10-07，www.trae.cn）：host=localhost、path=/admin/trae/authorize、
// 或整个参数缺失，都会让该页返回空 SSR 数据并渲染「登录失败 网络错误，请刷新页面重试。」，
// 因此这里固定 127.0.0.1，端口沿用调用方访问 console 时实际使用的端口。
// 国际版授权页（www.trae.ai/authorization）是否同形尚未实测，但两条区域共用同一个
// console 根路径落点，形状要求一致是这套导流能复用的前提。
func (h *server) traeCallbackBase(r *http.Request) string {
	port := traeDefaultCallbackPort
	if _, hostPort, err := net.SplitHostPort(r.Host); err == nil && hostPort != "" {
		port = hostPort
	}
	return "http://127.0.0.1:" + port
}

// rewriteTraeCallback 把 sidecar 返回的 login_url 里的 auth_callback_url 查询参数
// 改写为 console 根路径 /authorize。sidecar 的 adminLoginStart 只接受 callback_port
// 并硬编码拼出 127.0.0.1:<port>/authorize，而该端口在 overlay 下已被
// TRAE_CALLBACK_LISTEN=0 关闭，故由 console 侧重写查询参数完成回调导流。
func (h *server) rewriteTraeCallback(loginURL string, r *http.Request) string {
	u, err := url.Parse(loginURL)
	if err != nil {
		return loginURL
	}
	q := u.Query()
	q.Set("auth_callback_url", h.traeCallbackBase(r)+"/authorize")
	u.RawQuery = q.Encode()
	return u.String()
}

// adminTraeAuthorize 是 TRAE 浏览器 302 的公开落点，无管理会话与 CSRF 校验
// （TRAE 域跳转不带 console Cookie），与 sidecar /authorize 语义一致。只把回调
// query 转发给 sidecar，由它完成 ExchangeToken 与落盘。
// 两个区域共用这一条路径，所以按"最近发起登录的区域"分发。
func (h *server) adminTraeAuthorize(w http.ResponseWriter, r *http.Request) {
	ch := h.traeChannelByChannel(traePendingAuthorize.get())
	if ch.url == nil {
		// 提示指向的通道没启用（例如只在国际版发起过、之后改了部署），
		// 再试另一条，避免出现"启用了却收不到回调"的死路。
		for _, alt := range h.enabledTraeChannels() {
			ch = alt
			break
		}
	}
	if ch.url == nil {
		adminError(w, 409, "Trae 通道未启用")
		return
	}
	target := *ch.url
	target.Path = "/authorize"
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, ch.label+" 回调转发请求构造失败")
		return
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		adminError(w, 503, ch.label+" 容器不可达")
		return
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		adminError(w, 502, ch.label+" 回调响应读取失败")
		return
	}
	if ct := response.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// 回调已被消费，提示复位到国内版。
	traePendingAuthorize.set(channelTrae)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(raw)
}

// traeChannelByChannel 按通道身份取描述符；找不到返回未启用（url nil）。
func (h *server) traeChannelByChannel(c routeChannel) traeChannel {
	for _, ch := range h.traeChannels() {
		if ch.channel == c {
			return ch
		}
	}
	return traeChannel{channel: c, label: "Trae"}
}

// adminTraeLoginStart 生成登录 URL + pending 态（sidecar /admin/api/login），并把
// 返回的 login_url/callback_url 里的回调落点改写为 console 公开地址。
func (h *server) adminTraeLoginStart(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ch.url == nil {
			adminError(w, 409, ch.label+" 通道未启用")
			return
		}
		out, code, err := h.traeCtl(r.Context(), ch, "POST", "/admin/api/login", map[string]any{}, traeCtlTimeout)
		if err != nil {
			adminError(w, 503, ch.label+" 容器不可达")
			return
		}
		if code >= 500 {
			adminError(w, 502, traeCtlMessage(out, ch.label+" 登录请求失败"))
			return
		}
		if code != 200 {
			writeJSON(w, code, out)
			return
		}
		// 先登记发起方，再回 URL：用户点开链接时回调才可能被判给正确区域。
		traePendingAuthorize.set(ch.channel)
		callbackURL := h.traeCallbackBase(r) + "/authorize"
		if loginURL, ok := out["login_url"].(string); ok && loginURL != "" {
			out["login_url"] = h.rewriteTraeCallback(loginURL, r)
		}
		out["callback_url"] = callbackURL
		writeJSON(w, 200, out)
	}
}

// adminTraeLoginPoll 轮询单次登录结果（sidecar /admin/api/login/result）。
func (h *server) adminTraeLoginPoll(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pendingID := r.URL.Query().Get("pending_id")
		if pendingID == "" {
			adminError(w, 400, "缺少 pending_id")
			return
		}
		h.traeForward(w, r, ch, "GET", "/admin/api/login/result?pending_id="+url.QueryEscape(pendingID), nil, traeCtlTimeout)
	}
}

// adminTraeLoginCancel 取消单次登录（sidecar /admin/api/login/cancel）。
func (h *server) adminTraeLoginCancel(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PendingID string `json:"pending_id"`
		}
		if !decodeAdmin(w, r, &body) {
			return
		}
		h.traeForward(w, r, ch, "POST", "/admin/api/login/cancel", map[string]any{"pending_id": body.PendingID}, traeCtlTimeout)
	}
}

// adminTraeImport 导入凭证（sidecar /admin/api/accounts/import）。body 原样转发：
// 支持纯回调链接字符串、凭据 JSON，或桌面客户端 storage.json 全文（sidecar 自己按
// 内容判别并解 tc/AES 信封，host 以凭据自带的为准）。后两者可能超过 decodeAdmin 的
// 8192 字节上限，故不解析直接转。同 uid 已有更新的凭证时 sidecar 返回 409
// skipped_existing_newer，由 traeForward 原样透传。
func (h *server) adminTraeImport(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ch.url == nil {
			adminError(w, 409, ch.label+" 通道未启用")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			adminError(w, 400, "请求体读取失败")
			return
		}
		out, code, err := h.traeCtlRaw(r.Context(), ch, "POST", "/admin/api/accounts/import", raw, traeCtlTimeout)
		if err != nil {
			adminError(w, 503, ch.label+" 容器不可达")
			return
		}
		if code >= 500 {
			adminError(w, 502, traeCtlMessage(out, ch.label+" 导入请求失败"))
			return
		}
		writeJSON(w, code, out)
	}
}

// adminTraeAccountsDelete 删除账号凭据（sidecar DELETE /admin/api/accounts/{uid}）。
func (h *server) adminTraeAccountsDelete(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		h.traeForward(w, r, ch, "DELETE", "/admin/api/accounts/"+url.PathEscape(body.UID), nil, traeCtlTimeout)
	}
}

// adminTraeAccountsPatch 启用/停用或改名账号（sidecar PATCH /admin/api/accounts/{uid}）。
func (h *server) adminTraeAccountsPatch(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		h.traeForward(w, r, ch, "PATCH", "/admin/api/accounts/"+url.PathEscape(body.UID), payload, traeCtlTimeout)
	}
}

// adminTraeAccountsRefresh 手动 ExchangeToken 刷新单账号（sidecar POST .../refresh）。
func (h *server) adminTraeAccountsRefresh(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		h.traeForward(w, r, ch, "POST", "/admin/api/accounts/"+url.PathEscape(body.UID)+"/refresh", map[string]any{}, traeTaskTimeout)
	}
}

// adminTraeAccountJSON 脱敏预览单账号凭证（sidecar GET .../json）。
func (h *server) adminTraeAccountJSON(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := r.URL.Query().Get("uid")
		if uid == "" {
			adminError(w, 400, "缺少 uid")
			return
		}
		h.traeForward(w, r, ch, "GET", "/admin/api/accounts/"+url.PathEscape(uid)+"/json", nil, traeCtlTimeout)
	}
}

// adminTraeSchedule 读取签到/刷新排程状态（sidecar /admin/api/schedule）。
func (h *server) adminTraeSchedule(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.traeForward(w, r, ch, "GET", "/admin/api/schedule", nil, traeCtlTimeout)
	}
}

// adminTraeScheduleCheckin 手动触发一轮签到（sidecar /admin/api/schedule/checkin）。
// 国际版没有签到体系：这一条直接 409，不去打上游拿一个像故障的 404。
func (h *server) adminTraeScheduleCheckin(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ch.checkin {
			adminError(w, 409, ch.label+" 无签到体系（权益是订阅档位）")
			return
		}
		h.traeForward(w, r, ch, "POST", "/admin/api/schedule/checkin", map[string]any{}, traeTaskTimeout)
	}
}

// adminTraeScheduleRefresh 手动触发一轮 Token 刷新（sidecar /admin/api/schedule/refresh）。
func (h *server) adminTraeScheduleRefresh(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.traeForward(w, r, ch, "POST", "/admin/api/schedule/refresh", map[string]any{}, traeTaskTimeout)
	}
}

// adminTraeCredits 读取各账号权益（sidecar /admin/api/credits，无鉴权）。
// 国内版返回积分与签到状态，国际版返回订阅档位；两个形态都由 sidecar 标注
// entitlement_mode，console 不翻译也不补零。
func (h *server) adminTraeCredits(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.traeForward(w, r, ch, "GET", "/admin/api/credits", nil, traeTaskTimeout)
	}
}

// adminTraeUsage 读取 trae 请求用量（sidecar /admin/api/usage，无鉴权）。
func (h *server) adminTraeUsage(ch traeChannel) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.traeForward(w, r, ch, "GET", "/admin/api/usage", nil, traeCtlTimeout)
	}
}
