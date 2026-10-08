package server

import (
	"net/http"
	"sync"
	"time"
)

// creditAccount 是额度视图：积分余额的四个钱包分项 + 订阅状态。
type creditAccount struct {
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	Points     int64  `json:"points"`
	Daily      int64  `json:"daily"`
	Monthly    int64  `json:"monthly"`
	Reward     int64  `json:"reward"`
	TopUp      int64  `json:"topup"`
	Plan       string `json:"plan,omitempty"`
	PlanStatus string `json:"plan_status,omitempty"`
	Expired    string `json:"plan_expired,omitempty"`
	Cooling    bool   `json:"cooling"`
	Disabled   bool   `json:"disabled"`
	Error      string `json:"error,omitempty"`
}

// adminCredits 并发拉取每个账号的积分余额与订阅权益，并回写池里的积分快照
// （回写后被限流冷却的账号在余额确认时自动解冻）。
func (h *Handler) adminCredits(w http.ResponseWriter, r *http.Request) {
	list := h.cfg.Pool.List()
	out := make([]creditAccount, len(list))
	var wg sync.WaitGroup
	for i, st := range list {
		wg.Add(1)
		go func(i int, uid, nickname string, cooling, disabled bool) {
			defer wg.Done()
			row := creditAccount{UID: uid, Nickname: nickname, Cooling: cooling, Disabled: disabled}
			a := h.cfg.Pool.AuthByUID(uid)
			if a == nil {
				row.Error = "missing_credentials"
				out[i] = row
				return
			}
			bal, err := h.cfg.Upstream.Balance(r.Context(), a)
			if err != nil {
				row.Error = err.Error()
				out[i] = row
				return
			}
			row.Points, row.Daily, row.Monthly, row.Reward, row.TopUp = bal.Available, bal.Daily, bal.Monthly, bal.Reward, bal.TopUp
			h.cfg.Pool.SetPoints(uid, bal.Available)
			h.cfg.Pool.ReenableIfPoints(uid, bal.Available)
			if ent, entErr := h.cfg.Upstream.Entitlement(r.Context(), a); entErr == nil {
				row.Plan, row.PlanStatus, row.Expired = ent.ActivePlan, ent.Status, ent.ExpiredTime
			}
			out[i] = row
		}(i, st.UID, st.Nickname, st.Cooling, st.Disabled)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
		"accounts":   out,
	})
}

func (h *Handler) adminScheduleStatus(w http.ResponseWriter, _ *http.Request) {
	if h.cfg.Sched == nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "排程未启用")
		return
	}
	writeJSON(w, http.StatusOK, h.cfg.Sched.Status())
}

func (h *Handler) adminRunCheckin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Sched == nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "排程未启用")
		return
	}
	processed, err := h.cfg.Sched.RunCheckinNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "error": errString(err)})
}

func (h *Handler) adminRunRefresh(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Sched == nil {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "排程未启用")
		return
	}
	processed, err := h.cfg.Sched.RunRefreshNow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "error": errString(err)})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
