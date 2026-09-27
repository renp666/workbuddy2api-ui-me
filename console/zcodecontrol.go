package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"
)

// The Zcode tab can drive the optional zcode-proxy container through its localhost-only
// control API (POST /control, bound to 127.0.0.1 inside the shared network namespace).
// Console holds no Docker privileges: login, enable, disable and logout are plain
// in-process HTTP commands, mirroring how the WorkBuddy tab bridges OAuth to core.

// controlResponse is the union of the zcode control protocol envelopes. Every command
// answers HTTP 200 with {ok:true,...} or {ok:false,error:...}; unused fields stay zero.
type controlResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	State        string `json:"state"`
	Provider     string `json:"provider"`
	Plan         string `json:"plan"`
	ProxyPort    int    `json:"proxyPort"`
	LoggedIn     bool   `json:"loggedIn"`
	Event        string `json:"event"`
	AuthorizeURL string `json:"authorizeUrl"`
	Port         int    `json:"port"`
}

// errControlUnreachable distinguishes transport failures (503) from control-level errors (502/409).
var errControlUnreachable = errors.New("zcode control unreachable")

func (h *server) zcodeControl(ctx context.Context, payload map[string]any) (*controlResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	target := *h.cfg.ZCodeControlURL
	target.Path = "/control"
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, errControlUnreachable
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, errControlUnreachable
	}
	var out controlResponse
	if json.Unmarshal(raw, &out) != nil {
		return nil, errControlUnreachable
	}
	return &out, nil
}

// adminZcodeLogin starts a server-mediated OAuth flow and returns the authorize URL the
// admin opens in any browser; the flow completes inside zcode-proxy (no localhost callback).
// The proxy's upstream base URL follows config.provider, which startOAuth does NOT update,
// so a provider switch must go through setConfig first (only possible while the proxy is stopped).
func (h *server) adminZcodeLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeControlURL == nil {
		adminError(w, 409, "GLM 控制端未配置")
		return
	}
	var in struct {
		Provider string `json:"provider"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	if in.Provider != "zai" && in.Provider != "bigmodel" {
		adminError(w, 400, "provider 仅支持 zai 或 bigmodel")
		return
	}
	if status, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "status"}); err == nil && status.OK && status.Provider != "" && status.Provider != in.Provider {
		resp, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "setConfig", "provider": in.Provider})
		if err != nil {
			adminError(w, 503, "GLM 容器不可达")
			return
		}
		if !resp.OK {
			if resp.Error == "stop_proxy_first" {
				adminError(w, 409, "切换服务商需先停用通道")
			} else {
				adminError(w, 502, "GLM 服务商切换失败")
			}
			return
		}
	}
	resp, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "startOAuth", "provider": in.Provider})
	if err != nil {
		adminError(w, 503, "GLM 容器不可达")
		return
	}
	if !resp.OK {
		adminError(w, 502, "GLM 登录流程启动失败")
		return
	}
	writeJSON(w, 200, map[string]any{"authorize_url": resp.AuthorizeURL})
}

// adminZcodeConfig switches the provider and/or plan tier through the control setConfig
// command. The upstream refuses changes while the proxy is running (stop_proxy_first).
func (h *server) adminZcodeConfig(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeControlURL == nil {
		adminError(w, 409, "GLM 控制端未配置")
		return
	}
	var in struct {
		Provider string `json:"provider"`
		Plan     string `json:"plan"`
	}
	if !decodeAdmin(w, r, &in) {
		return
	}
	payload := map[string]any{"cmd": "setConfig"}
	if in.Provider != "" {
		if in.Provider != "zai" && in.Provider != "bigmodel" {
			adminError(w, 400, "provider 仅支持 zai 或 bigmodel")
			return
		}
		payload["provider"] = in.Provider
	}
	if in.Plan != "" {
		if in.Plan != "coding-plan" && in.Plan != "start-plan" {
			adminError(w, 400, "plan 仅支持 coding-plan 或 start-plan")
			return
		}
		payload["plan"] = in.Plan
	}
	if len(payload) == 1 {
		adminError(w, 400, "provider 与 plan 至少提供一个")
		return
	}
	resp, err := h.zcodeControl(r.Context(), payload)
	if err != nil {
		adminError(w, 503, "GLM 容器不可达")
		return
	}
	if !resp.OK {
		if resp.Error == "stop_proxy_first" {
			adminError(w, 409, "请先停用通道，再切换服务商或套餐")
		} else {
			adminError(w, 502, "GLM 配置切换失败")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "provider": resp.Provider, "plan": resp.Plan})
}

func (h *server) adminZcodeEnable(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeControlURL == nil {
		adminError(w, 409, "GLM 控制端未配置")
		return
	}
	h.mu.Lock()
	h.zcodeUserStopped = false
	h.mu.Unlock()
	resp, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "startProxy"})
	if err != nil {
		adminError(w, 503, "GLM 容器不可达")
		return
	}
	if !resp.OK {
		switch resp.Error {
		case "not_logged_in":
			adminError(w, 409, "请先在页签内完成 GLM 登录")
		case "already_running":
			writeJSON(w, 200, map[string]any{"ok": true})
		default:
			adminError(w, 502, "GLM 通道启用失败")
		}
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (h *server) adminZcodeDisable(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeControlURL == nil {
		adminError(w, 409, "GLM 控制端未配置")
		return
	}
	resp, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "stopProxy"})
	if err != nil {
		adminError(w, 503, "GLM 容器不可达")
		return
	}
	if !resp.OK && resp.Error != "not_running" {
		adminError(w, 502, "GLM 通道停用失败")
		return
	}
	// 用户主动停用：本运行周期不再自动拉起，避免与管理员意图冲突。
	// 容器重启后内存清零即恢复自动拉起；重新点「启用」也会清除该标志。
	h.mu.Lock()
	h.zcodeUserStopped = true
	h.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (h *server) adminZcodeLogout(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeControlURL == nil {
		adminError(w, 409, "GLM 控制端未配置")
		return
	}
	resp, err := h.zcodeControl(r.Context(), map[string]any{"cmd": "logout"})
	if err != nil {
		adminError(w, 503, "GLM 容器不可达")
		return
	}
	if !resp.OK {
		adminError(w, 502, "GLM 退出登录失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// zcodeControlStatus fetches the container state; unreachable or command failure degrades to
// a not-logged-in snapshot so the tab keeps rendering (with a retry on the next poll).
func (h *server) zcodeControlStatus(ctx context.Context) map[string]any {
	out := map[string]any{"control": true, "logged_in": false, "proxy_running": false}
	resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "status"})
	if err != nil || !resp.OK {
		return out
	}
	out["logged_in"] = resp.LoggedIn
	out["proxy_running"] = resp.ProxyPort > 0
	out["provider"] = resp.Provider
	out["plan"] = resp.Plan
	return out
}

// zcodePlan reports the container's current plan tier, or "" when the control API is not
// configured or unreachable (callers treat that as "do not filter").
func (h *server) zcodePlan(ctx context.Context) string {
	if h.cfg.ZCodeControlURL == nil {
		return ""
	}
	resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "status"})
	if err != nil || !resp.OK {
		return ""
	}
	return resp.Plan
}

// zcodeAutoStart is a low-frequency background loop (only when WB2A_ZCODE_CONTROL_URL is set)
// that brings the zcode-proxy proxy up after container restarts. The third-party image's
// android entry deliberately starts only the control listener, leaving the proxy stopped
// ("proxy stopped — use startProxy command to start"), so a freshly recreated container
// would otherwise stay grey until the admin opens the Zcode tab and clicks 启用.
//
// Conditions for an automatic startProxy:
//   - control endpoint is reachable and reports logged_in == true (never auto-start before login,
//     since startProxy returns not_logged_in and there is nothing to start without credentials);
//   - proxy is not already running;
//   - the admin has not manually clicked 停用 in this process lifetime (zcodeUserStopped).
//
// A manual 停用 sets zcodeUserStopped so the loop does not fight the admin; a manual 启用
// clears it, and a container restart zeroes the memory flag so auto-start resumes. The loop
// does not log on every idle tick to avoid noise — only successful starts are recorded.
func (h *server) zcodeAutoStart() {
	const interval = 15 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Run immediately on startup so a ready container doesn't wait a full interval.
	h.tryZcodeAutoStart(context.Background())
	for range ticker.C {
		h.tryZcodeAutoStart(context.Background())
	}
}

func (h *server) tryZcodeAutoStart(ctx context.Context) {
	h.mu.Lock()
	stopped := h.zcodeUserStopped
	h.mu.Unlock()
	if stopped {
		return
	}
	status, err := h.zcodeControl(ctx, map[string]any{"cmd": "status"})
	if err != nil || !status.OK {
		return // control 端未就绪，下一轮再试
	}
	if !status.LoggedIn {
		return // 未登录绝不拉起
	}
	if status.ProxyPort > 0 {
		return // 已在运行
	}
	resp, err := h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"})
	if err != nil {
		return
	}
	if resp.OK {
		log.Printf("[console] zcode 代理已自动拉起（provider=%s plan=%s）", resp.Provider, resp.Plan)
	}
	// already_running / not_logged_in 等非 ok 分支静默，下一轮由 status 重新判定。
}
