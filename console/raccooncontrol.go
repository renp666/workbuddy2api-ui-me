package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var errRaccoonControlUnreachable = errors.New("raccoon control unreachable")

// raccoonCtl 调用 raccoon sidecar 的控制端点。sidecar 与 console 共享网络命名空间
// 且仅监听回环，console 注入 Bearer 桥接密钥；与 trae 控制链路同构，console 不持有
// Docker 权限。pth 可带查询串。
func (h *server) raccoonCtl(ctx context.Context, method, pth string, payload any, timeout time.Duration) (map[string]any, int, error) {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	return h.raccoonCtlRaw(ctx, method, pth, raw, timeout)
}

// raccoonCtlRaw 是 raccoonCtl 的原样字节版本：凭证导入的 body 可能超过 decodeAdmin
// 的 8192 字节上限，需原样转发。
func (h *server) raccoonCtlRaw(ctx context.Context, method, pth string, raw []byte, timeout time.Duration) (map[string]any, int, error) {
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	target := *h.cfg.RaccoonURL
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
	if h.cfg.RaccoonKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.RaccoonKey)
	}
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return nil, 0, errRaccoonControlUnreachable
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, errRaccoonControlUnreachable
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errRaccoonControlUnreachable
	}
	return out, response.StatusCode, nil
}

// raccoonForward 把控制端点的 JSON 结果下发。raccoon sidecar 用标准 HTTP 状态码表达
// 业务结果（400 参数错、404 账号不存在、409 导入遇更新的旧凭证），这些都是正常业务
// 语义，需原样透传；只有传输失败是 503，5xx 收敛成 502 并带出上游消息。
func (h *server) raccoonForward(w http.ResponseWriter, r *http.Request, method, pth string, payload any, timeout time.Duration) {
	if h.cfg.RaccoonURL == nil {
		adminError(w, 409, "Raccoon 通道未启用")
		return
	}
	out, code, err := h.raccoonCtl(r.Context(), method, pth, payload, timeout)
	if err != nil {
		adminError(w, 503, "Raccoon 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, raccoonCtlMessage(out, "Raccoon 控制请求失败"))
		return
	}
	writeJSON(w, code, out)
}

// raccoonCtlMessage 从 raccoon 的 OpenAI 风格错误包 {"error":{"message":...}} 取消息，
// 兼容纯字符串 {"error":"..."} 形态。
func raccoonCtlMessage(out map[string]any, fallback string) string {
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

// adminRaccoonImport 导入凭证（sidecar /admin/api/accounts/import）。小浣熊在 Docker
// 下没有网页登录（登录态依赖宿主 DPAPI），因此登录环节等价于凭证导入。body 原样转发：
// 支持 {cookie,token,...} 一类 JSON，可能超过 decodeAdmin 的 8192 字节上限。同 uid 已有
// 更新的凭证时 sidecar 返回 409 skipped_existing_newer，由 raccoonForward 原样透传。
func (h *server) adminRaccoonImport(w http.ResponseWriter, r *http.Request) {
	if h.cfg.RaccoonURL == nil {
		adminError(w, 409, "Raccoon 通道未启用")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return
	}
	out, code, err := h.raccoonCtlRaw(r.Context(), "POST", "/admin/api/accounts/import", raw, raccoonCtlTimeout)
	if err != nil {
		adminError(w, 503, "Raccoon 容器不可达")
		return
	}
	if code >= 500 {
		adminError(w, 502, raccoonCtlMessage(out, "Raccoon 导入请求失败"))
		return
	}
	writeJSON(w, code, out)
}

// adminRaccoonAccountsDelete 删除账号凭据（sidecar DELETE /admin/api/accounts/{uid}）。
func (h *server) adminRaccoonAccountsDelete(w http.ResponseWriter, r *http.Request) {
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
	h.raccoonForward(w, r, "DELETE", "/admin/api/accounts/"+url.PathEscape(body.UID), nil, raccoonCtlTimeout)
}

// adminRaccoonAccountsPatch 启用/停用或改名账号（sidecar PATCH /admin/api/accounts/{uid}）。
func (h *server) adminRaccoonAccountsPatch(w http.ResponseWriter, r *http.Request) {
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
	h.raccoonForward(w, r, "PATCH", "/admin/api/accounts/"+url.PathEscape(body.UID), payload, raccoonCtlTimeout)
}

// adminRaccoonAccountsRefresh 刷新单账号状态（sidecar POST .../refresh）。逐账号访问
// 上游，故用更长的 raccoonTaskTimeout。
func (h *server) adminRaccoonAccountsRefresh(w http.ResponseWriter, r *http.Request) {
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
	h.raccoonForward(w, r, "POST", "/admin/api/accounts/"+url.PathEscape(body.UID)+"/refresh", map[string]any{}, raccoonTaskTimeout)
}

// adminRaccoonCredits 读取各账号额度与状态（sidecar /admin/api/credits）。
func (h *server) adminRaccoonCredits(w http.ResponseWriter, r *http.Request) {
	h.raccoonForward(w, r, "GET", "/admin/api/credits", nil, raccoonTaskTimeout)
}

// adminRaccoonUsage 读取 raccoon 请求用量（sidecar /admin/api/usage）。
func (h *server) adminRaccoonUsage(w http.ResponseWriter, r *http.Request) {
	h.raccoonForward(w, r, "GET", "/admin/api/usage", nil, raccoonCtlTimeout)
}

// adminRaccoonSchedule 返回排程视图（签到时点、上次执行时间、下次触发）。
func (h *server) adminRaccoonSchedule(w http.ResponseWriter, r *http.Request) {
	h.raccoonForward(w, r, "GET", "/admin/api/schedule", nil, raccoonCtlTimeout)
}

// adminRaccoonScheduleCheckin 手动跑一轮积分链路。小浣熊没有原生签到接口，sidecar 只在
// 每日固定时点跑「桌面登录奖励 → setting_info 触发 → 账单核对」；容器在那个小时没运行
// 就会漏掉一整天，所以需要这个「立即跑一次」的入口。逐账号访问上游，用长超时。
func (h *server) adminRaccoonScheduleCheckin(w http.ResponseWriter, r *http.Request) {
	h.raccoonForward(w, r, "POST", "/admin/api/schedule/checkin", map[string]any{}, raccoonTaskTimeout)
}

// adminRaccoonScheduleRefresh 手动跑一轮 token 预刷新（同样逐账号访问上游）。
func (h *server) adminRaccoonScheduleRefresh(w http.ResponseWriter, r *http.Request) {
	h.raccoonForward(w, r, "POST", "/admin/api/schedule/refresh", map[string]any{}, raccoonTaskTimeout)
}
