package server

import (
	"io"
	"net/http"
	"os"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
	"workbuddy2api-raccoon/raccoon/upstream"
)

// accountSummary 是对外的脱敏账号视图。has_auth / expires_at / expired_soon
// 由控制台用来渲染凭据有效性，控制台只做透传，不解释其语义。
type accountSummary struct {
	UID         string `json:"uid"`
	Nickname    string `json:"nickname"`
	Enabled     bool   `json:"enabled"`
	Disabled    bool   `json:"disabled,omitempty"`
	Cooling     bool   `json:"cooling,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Points      int64  `json:"points,omitempty"`
	ErrCount    int    `json:"err_count,omitempty"`
	ExpiresAt   int64  `json:"expires_at"`
	ExpiredSoon bool   `json:"expired_soon"`
	HasAuth     bool   `json:"has_auth"`
}

func (h *Handler) summaryFor(uid string) (accountSummary, bool) {
	st, ok := h.cfg.Pool.Status(uid)
	if !ok {
		return accountSummary{}, false
	}
	out := accountSummary{
		UID:      st.UID,
		Nickname: st.Nickname,
		Enabled:  st.Enabled,
		Disabled: st.Disabled,
		Cooling:  st.Cooling,
		Reason:   st.Reason,
		Points:   st.Points,
		ErrCount: st.ErrCount,
	}
	if a := h.cfg.Pool.AuthByUID(uid); a != nil {
		out.HasAuth = a.JWT() != ""
		out.ExpiresAt = a.ExpiresAtValue()
		// 展示口径独立于预刷新余量（小浣熊 token 有效期远长于 5min，用协议余量会让页签
		// 几乎永不显示临期），对齐 trae2api 的 24h。没有 refresh token 的凭证永远无法
		// 自助恢复，即使上游没声明 expires_at 也要提示临期。
		out.ExpiredSoon = a.NeedsRefresh(h.cfg.DisplaySkew) || !a.CanRefresh()
	}
	return out, true
}

// adminAccounts 返回账号列表。此端点不鉴权（同 trae）：只回脱敏字段，
// 控制台页签在只读视图里也需要它。
func (h *Handler) adminAccounts(w http.ResponseWriter, _ *http.Request) {
	list := make([]accountSummary, 0)
	for _, st := range h.cfg.Pool.List() {
		if s, ok := h.summaryFor(st.UID); ok {
			list = append(list, s)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
}

type importResult struct {
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	Action     string `json:"action"`
	NeedsCheck bool   `json:"needs_check"`
}

// adminImportAccount 导入一份凭证。小浣熊在 Docker 下没有网页登录（桌面登录态依赖宿主
// DPAPI），因此登录环节等价于凭证导入：请求体就是桌面端 auth.json 的原样内容。
// 同 uid 已存在更新的凭证时不覆盖，返回 409 skipped_existing_newer。
func (h *Handler) adminImportAccount(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDecodeBody+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "read_error", err.Error())
		return
	}
	if len(raw) > maxDecodeBody {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "凭证超过 1 MiB 上限")
		return
	}
	a, err := auth.Parse(raw)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_credentials", err.Error())
		return
	}
	uid := a.UID()
	path := auth.FilePathFor(h.cfg.AuthDir, uid)
	a.SetFilePath(path)

	action := "updated"
	if _, statErr := os.Stat(path); statErr != nil {
		action = "created"
	}
	if oldRaw, readErr := os.ReadFile(path); readErr == nil {
		if old, parseErr := auth.Parse(oldRaw); parseErr == nil {
			// 保护：已存凭证过期时间更晚且尚未过期时，不接受更旧的导入覆盖。
			if old.ExpiresAtValue() > a.ExpiresAtValue() && old.ExpiresAtValue() > time.Now().Unix() {
				writeJSON(w, http.StatusConflict, importResult{
					UID:        uid,
					Nickname:   a.Nickname(),
					Action:     "skipped_existing_newer",
					NeedsCheck: a.NeedsRefresh(h.cfg.RefreshSkew),
				})
				return
			}
		}
	}
	if err := a.SaveAtomic(); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	h.cfg.Pool.Add(a)
	writeJSON(w, http.StatusOK, importResult{
		UID:        uid,
		Nickname:   a.Nickname(),
		Action:     action,
		NeedsCheck: a.NeedsRefresh(h.cfg.RefreshSkew),
	})
}

// adminDeleteAccount 删除账号（凭证文件与池状态同时清）。
func (h *Handler) adminDeleteAccount(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	if h.cfg.Pool.Remove(uid) {
		// 池条目与凭证文件同时清：只删池的话，SyncToDir 会在下次启动把残留文件收编回来。
		_ = os.Remove(auth.FilePathFor(h.cfg.AuthDir, uid))
		writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "deleted": true})
		return
	}
	// 池里没有也清一次残留文件，避免手工删文件后状态与磁盘不一致。
	_ = os.Remove(auth.FilePathFor(h.cfg.AuthDir, uid))
	writeOpenAIError(w, http.StatusNotFound, "not_found", "账号不存在："+uid)
}

type patchRequest struct {
	Enabled  *bool   `json:"enabled"`
	Nickname *string `json:"nickname"`
}

// adminPatchAccount 改用户软开关或展示名。
func (h *Handler) adminPatchAccount(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var body patchRequest
	if err := decodeBody(r, &body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if body.Enabled != nil {
		reason := ""
		if !*body.Enabled {
			reason = "disabled_by_user"
		}
		if !h.cfg.Pool.SetEnabled(uid, *body.Enabled, reason) {
			writeOpenAIError(w, http.StatusNotFound, "not_found", "账号不存在："+uid)
			return
		}
	}
	if body.Nickname != nil && *body.Nickname != "" {
		a := h.cfg.Pool.AuthByUID(uid)
		if a == nil {
			writeOpenAIError(w, http.StatusNotFound, "not_found", "账号不存在："+uid)
			return
		}
		a.SetNickname(*body.Nickname)
		if err := a.SaveAtomic(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
	}
	s, ok := h.summaryFor(uid)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "账号不存在："+uid)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// adminRefreshAccount 强制刷新单账号凭证。逐账号访问上游，故失败给 502。
func (h *Handler) adminRefreshAccount(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := h.cfg.Pool.AuthByUID(uid)
	if a == nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "账号不存在："+uid)
		return
	}
	if !a.CanRefresh() {
		h.cfg.Pool.Disable(uid, "no_refresh_token: 凭证无 refresh token，请重新导入")
		writeOpenAIError(w, http.StatusBadRequest, "no_refresh_token", "该凭证没有 refresh token，请重新导入")
		return
	}
	if err := h.cfg.Upstream.RefreshToken(r.Context(), a); err != nil {
		if upstream.IsTokenExpiredErr(err) {
			h.cfg.Pool.Disable(uid, "token_expired: 刷新被拒，请重新导入凭证")
		}
		writeOpenAIError(w, http.StatusBadGateway, "refresh_failed", err.Error())
		return
	}
	h.cfg.Pool.NoteSuccess(uid)
	st, _ := h.summaryFor(uid)
	writeJSON(w, http.StatusOK, map[string]any{
		"uid":        uid,
		"expires_at": a.ExpiresAtValue(),
		"status":     st,
	})
}
