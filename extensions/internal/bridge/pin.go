package bridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"workbuddy2api/internal/pool"
)

// 人工锁定账号管理端点。锁定是部署级全局操作（不属于某个 OAuth owner），因此不走
// withOwner；可达性本身由桥接密钥保证，console 侧另以管理会话 + CSRF 把关。
// 锁定目标的 realm 一律以后端账号池为准，前端只传 uid，无法伪造跨域锁定。

// pinView 是单个平台的锁定展示；未锁定时该域在响应中为 null。
type pinView struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname,omitempty"`
	Realm    string `json:"realm"`
	// Exists=false 表示锁定记录仍在，但该账号已不在池（删除凭据/重新授权换号），
	// 选号路径会持续 503，前端应提示用户解锁或重新锁定。
	Exists bool `json:"exists"`
}

func (h *handler) pinsUnavailable(w http.ResponseWriter) bool {
	if h.cfg.Pins == nil {
		bridgeError(w, 503, "账号锁定功能暂不可用")
		return true
	}
	return false
}

func (h *handler) listPins(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		bridgeError(w, 400, "查询参数无效")
		return
	}
	if h.pinsUnavailable(w) {
		return
	}
	poolByUID := make(map[string]pool.Status, len(h.cfg.Pool.List()))
	for _, st := range h.cfg.Pool.List() {
		poolByUID[st.UID] = st
	}
	views := make(map[string]*pinView, 2)
	for _, realm := range []string{"cn", "global"} {
		uid := h.cfg.Pins.PinnedUID(realm)
		if uid == "" {
			views[realm] = nil
			continue
		}
		view := &pinView{UID: uid, Realm: realm, Exists: false}
		if st, ok := poolByUID[uid]; ok {
			view.Exists = true
			view.Nickname = st.Nickname
			view.Realm = st.Realm // 以池中账号的实际域为准
		}
		views[realm] = view
	}
	writeJSON(w, 200, map[string]any{"pins": views})
}

func decodePinBody(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(raw) > 8192 {
		bridgeError(w, 400, "请求内容无效")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		bridgeError(w, 400, "请求内容无效")
		return false
	}
	return true
}

func (h *handler) pinAccount(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		bridgeError(w, 400, "查询参数无效")
		return
	}
	if h.pinsUnavailable(w) {
		return
	}
	var body struct {
		UID string `json:"uid"`
	}
	if !decodePinBody(w, r, &body) {
		return
	}
	if body.UID == "" || len(body.UID) > 128 {
		bridgeError(w, 400, "账号标识无效")
		return
	}
	var realm, nickname string
	found := false
	for _, st := range h.cfg.Pool.List() {
		if st.UID == body.UID {
			realm, nickname, found = st.Realm, st.Nickname, true
			break
		}
	}
	if !found {
		bridgeError(w, 404, "账号不在当前账号池，无法锁定")
		return
	}
	if err := h.cfg.Pins.Set(realm, body.UID); err != nil {
		bridgeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "realm": realm, "uid": body.UID, "nickname": nickname})
}

func (h *handler) unpinAccount(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		bridgeError(w, 400, "查询参数无效")
		return
	}
	if h.pinsUnavailable(w) {
		return
	}
	var body struct {
		Realm string `json:"realm"`
	}
	if !decodePinBody(w, r, &body) {
		return
	}
	if body.Realm != "cn" && body.Realm != "global" {
		bridgeError(w, 400, "realm 仅支持 cn 或 global")
		return
	}
	if err := h.cfg.Pins.Clear(body.Realm); err != nil {
		bridgeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "realm": body.Realm})
}
