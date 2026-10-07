package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// claude-code-router（CCR）的管理服务只监听宿主回环，既拒绝非回环 Host，又要求每次启动轮换的
// x-ccr-web-auth。console 容器没有宿主进程权限，所以这里只能驱动 CCR 自己的模型网关
// （startGateway / stopGateway）；ccr 后台服务本身要由管理员在宿主机用 ccr ui 拉起。

const (
	ccrTokenFileLimit = 8 << 10
	ccrRPCBodyLimit   = 1 << 20
	ccrStatusTimeout  = 3 * time.Second
	// 网关启动要在宿主侧拉起子进程，留足余量，否则页面会报一条看起来像故障的超时。
	ccrActionTimeout = 20 * time.Second
)

type ccrOutcome int

const (
	ccrOK ccrOutcome = iota
	ccrNoAuth
	ccrBadAuth
	ccrUnreachable
	ccrRefused
)

// ccrLoopbackHost 是 CCR 唯一接受的 Host 形式：容器经 host.docker.internal 连过去时，
// 若沿用该 Host 会拿到一条 403，看着像鉴权失败。
func ccrLoopbackHost(u *url.URL) string {
	if port := u.Port(); port != "" {
		return "127.0.0.1:" + port
	}
	return "127.0.0.1"
}

// parseCCRWebAuth 取 CCR 管理 Token。真源是 service.json 的 url 字段的 ccr_web_token 查询参数；
// 同一文件里的 serviceToken 是另一种凭据，不能拿来过 x-ccr-web-auth。
func parseCCRWebAuth(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > ccrTokenFileLimit {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var doc struct {
		URL string `json:"url"`
	}
	if decoder.Decode(&doc) != nil {
		return "", false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", false
	}
	parsed, err := url.Parse(doc.URL)
	if err != nil {
		return "", false
	}
	token := parsed.Query().Get("ccr_web_token")
	if token == "" {
		return "", false
	}
	return token, true
}

func (h *server) ccrAuthReady() bool {
	_, ok := parseCCRWebAuth(h.cfg.CCRTokenFile)
	return h.cfg.CCRURL != nil && ok
}

// ccrRPC 调一次 CCR 的 JSON-RPC。只有 HTTP 200 且信封 ok:true 才算成功，value 原样返回。
func (h *server) ccrRPC(ctx context.Context, method string, timeout time.Duration) (json.RawMessage, ccrOutcome) {
	if h.cfg.CCRURL == nil {
		return nil, ccrNoAuth
	}
	token, ok := parseCCRWebAuth(h.cfg.CCRTokenFile)
	if !ok {
		return nil, ccrNoAuth
	}
	payload, err := json.Marshal(map[string]any{"method": method, "params": map[string]any{}})
	if err != nil {
		return nil, ccrRefused
	}
	target := *h.cfg.CCRURL
	target.Path = "/api/ccr/rpc"
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, ccrRefused
	}
	req.Host = ccrLoopbackHost(h.cfg.CCRURL)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ccr-web-auth", token)
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, ccrUnreachable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, ccrRPCBodyLimit+1))
	if err != nil || len(body) > ccrRPCBodyLimit {
		return nil, ccrRefused
	}
	// 401/403 是服务在线、Token 不认：与不可达是两回事，报错了要把人指向重建挂载。
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ccrBadAuth
	}
	if response.StatusCode != http.StatusOK {
		return nil, ccrRefused
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(body, &envelope) != nil || !envelope.OK {
		return nil, ccrRefused
	}
	return envelope.Value, ccrOK
}

// ccrStatus 把 CCR 的网关状态映射成页面读得到的字段。未配置 Token 时不发起任何网络请求。
func (h *server) ccrStatus(ctx context.Context) map[string]any {
	ready := h.ccrAuthReady()
	result := map[string]any{"enabled": true, "auth_ready": ready, "reachable": false}
	if !ready {
		return result
	}
	value, outcome := h.ccrRPC(ctx, "getGatewayStatus", ccrStatusTimeout)
	if outcome == ccrBadAuth {
		result["auth_mismatch"] = true
		return result
	}
	if outcome != ccrOK {
		return result
	}
	result["reachable"] = true
	var gateway struct {
		Endpoint      string `json:"endpoint"`
		State         string `json:"state"`
		PID           int    `json:"pid"`
		LastStartedAt string `json:"lastStartedAt"`
	}
	if json.Unmarshal(value, &gateway) != nil {
		return result
	}
	result["state"] = gateway.State
	result["endpoint"] = gateway.Endpoint
	if gateway.PID > 0 {
		result["pid"] = gateway.PID
	}
	if gateway.LastStartedAt != "" {
		result["last_started_at"] = gateway.LastStartedAt
	}
	return result
}

func (h *server) adminCCRStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.CCRURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, 200, h.ccrStatus(r.Context()))
}

// adminCCRGateway 只驱动 CCR 的模型网关。ccr 后台服务不在时这里是 503，
// 因为容器没有宿主进程权限，替管理员执行 ccr ui 不是本控制台的能力。
func (h *server) adminCCRGateway(w http.ResponseWriter, r *http.Request) {
	if h.cfg.CCRURL == nil {
		adminError(w, 409, "CCR 通道未启用")
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	method, label := "startGateway", "启动"
	switch in.Action {
	case "start":
	case "stop":
		method, label = "stopGateway", "关停"
	default:
		adminError(w, 400, "action 仅支持 start 或 stop")
		return
	}
	switch _, outcome := h.ccrRPC(r.Context(), method, ccrActionTimeout); outcome {
	case ccrBadAuth:
		adminError(w, 409, "CCR 管理服务在线但拒绝了管理 Token：CCR 每次重启都会轮换 Token，请重建 console 容器（docker compose up -d --force-recreate）重新读取挂载")
	case ccrNoAuth:
		adminError(w, 409, "未读取到 CCR 管理 Token，请确认 service.json 已只读挂载并配置 WB2A_CCR_TOKEN_FILE")
	case ccrUnreachable:
		adminError(w, 503, "CCR 管理服务不可达，请先在宿主机执行 ccr ui 再刷新")
	case ccrRefused:
		adminError(w, 502, "CCR 模型网关"+label+"失败")
	default:
		writeJSON(w, 200, h.ccrStatus(r.Context()))
	}
}

// adminCCRLink 只在管理员点击时把当次 Token 交给浏览器：CCR 网页界面自己从 location.href
// 读 ccr_web_token，所以这一步无法在服务端代劳。地址写成回环形式，只能在这台机器上打开。
func (h *server) adminCCRLink(w http.ResponseWriter, r *http.Request) {
	if h.cfg.CCRURL == nil {
		adminError(w, 409, "CCR 通道未启用")
		return
	}
	token, ok := parseCCRWebAuth(h.cfg.CCRTokenFile)
	if !ok {
		adminError(w, 409, "未读取到 CCR 管理 Token，请确认 service.json 已只读挂载并配置 WB2A_CCR_TOKEN_FILE")
		return
	}
	query := url.Values{}
	query.Set("ccr_web_token", token)
	writeJSON(w, 200, map[string]string{"ui_url": h.cfg.CCRURL.Scheme + "://" + ccrLoopbackHost(h.cfg.CCRURL) + "/?" + query.Encode()})
}
