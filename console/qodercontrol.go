package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Qoder 页签通过容器内 qoder-login-ctl（仅 127.0.0.1:3001）驱动 qoderclicn 的
// 设备码 OAuth。console 不持有 Docker 权限：发起登录、轮询、登出都是普通 HTTP
// 调用，与 zcode 控制链路同构；qoder-proxy 自身没有控制面，该进程由本仓镜像附带。

var errQoderControlUnreachable = errors.New("qoder control unreachable")

// qoderCtl 调用控制进程。timeout 取 6 秒：ctl 的 /status 会现场执行
// qoderclicn status（冷启动 Node 约 1 秒），ctl 侧另有 3 秒结果缓存。
func (h *server) qoderCtl(ctx context.Context, method, pth string, payload any) (map[string]any, int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(raw)
	}
	target := *h.cfg.QoderControlURL
	target.Path = pth
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, 0, err
	}
	// 自定义头：ctl 用它区分服务端调用与浏览器跨站请求（预检拦截）。
	req.Header.Set("X-Qoder-Ctl", "1")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cfg.QoderKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.QoderKey)
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, 0, errQoderControlUnreachable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, errQoderControlUnreachable
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errQoderControlUnreachable
	}
	return out, response.StatusCode, nil
}

// adminQoderLoginStart 发起设备码登录；授权链接可能要等 CLI 进程输出，
// 因此 auth_url 允许为空，前端拿 state=waiting 后轮询 GET /admin/qoder/login。
func (h *server) adminQoderLoginStart(w http.ResponseWriter, r *http.Request) {
	if h.cfg.QoderControlURL == nil {
		adminError(w, 409, "Qoder 控制端未配置")
		return
	}
	out, code, err := h.qoderCtl(r.Context(), "POST", "/login/start", map[string]any{})
	if err != nil {
		adminError(w, 503, "Qoder 容器不可达")
		return
	}
	if code != 200 {
		adminError(w, 502, qoderCtlMessage(out, "Qoder 登录流程启动失败"))
		return
	}
	writeJSON(w, 200, map[string]any{
		"state":    out["state"],
		"auth_url": out["auth_url"],
	})
}

func (h *server) adminQoderLoginPoll(w http.ResponseWriter, r *http.Request) {
	if h.cfg.QoderControlURL == nil {
		adminError(w, 409, "Qoder 控制端未配置")
		return
	}
	out, code, err := h.qoderCtl(r.Context(), "GET", "/login", nil)
	if err != nil {
		adminError(w, 503, "Qoder 容器不可达")
		return
	}
	if code != 200 {
		adminError(w, 502, qoderCtlMessage(out, "Qoder 登录状态获取失败"))
		return
	}
	writeJSON(w, 200, out)
}

func (h *server) adminQoderLogout(w http.ResponseWriter, r *http.Request) {
	if h.cfg.QoderControlURL == nil {
		adminError(w, 409, "Qoder 控制端未配置")
		return
	}
	out, code, err := h.qoderCtl(r.Context(), "POST", "/logout", map[string]any{})
	if err != nil {
		adminError(w, 503, "Qoder 容器不可达")
		return
	}
	if code != 200 {
		adminError(w, 502, qoderCtlMessage(out, "Qoder 退出登录失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// qoderLoggedIn 探测设备码登录态；控制端未配置或不可达时返回 false。
// 第二返回值表示控制端是否可用（前端据此区分「纯 PAT 模式」与「登录控制在线」）。
func (h *server) qoderLoggedIn(ctx context.Context) (bool, bool) {
	if h.cfg.QoderControlURL == nil {
		return false, false
	}
	out, code, err := h.qoderCtl(ctx, "GET", "/status", nil)
	if err != nil || code != 200 {
		return false, false
	}
	status, _ := out["status"].(map[string]any)
	loggedIn, _ := status["logged_in"].(bool)
	return loggedIn, true
}

func qoderCtlMessage(out map[string]any, fallback string) string {
	if msg, ok := out["error"].(string); ok && msg != "" {
		return msg
	}
	return fallback
}
