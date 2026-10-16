// 管理面板（/admin）：只读查询，无鉴权（本地面板）；CLI 操作留待开发。
package server

import (
	_ "embed"
	"net/http"
	"sync"
	"time"

	"workbuddy2api-trae/trae/pool"
)

//go:embed admin.html
var adminPageHTML []byte

// adminPage 返回内嵌 HTML 面板（深色简洁风，无外部依赖）。
func (h *Handler) adminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(adminPageHTML)
}

// adminCredits 查询全部账号的实时额度 + 签到状态（并发拉取上游）。
//
// 权益表达随区域分叉：国内版是积分余额（ent_usage），国际版是订阅档位（pay_status）。
// 国际版刻意不把订阅折算成一个积分数字——上游从没给过那个量，编一个数会污染
// core 账本与管理页口径（见仓库 AGENTS.md「未知积分或奖励不能变成零」同源的道理）。
func (h *Handler) adminCredits(w http.ResponseWriter, r *http.Request) {
	ed := h.ed()
	// 两种权益口径是两套条目形状，不共用一个结构：共用就会在订阅模式下把
	// remain/limit/used/packs 打成 0，而 0 在页面上长得像"已知且为零"——
	// 上游从没给过这些量，补零等于编造（见仓库 AGENTS.md「未知积分或奖励不能变成零」）。
	type creditAcct struct {
		UID            string `json:"uid"`
		Nickname       string `json:"nickname"`
		Remain         int64  `json:"remain"`
		Limit          int64  `json:"limit"`
		Used           int64  `json:"used"`
		Packs          int    `json:"packs"`
		CheckedIn      bool   `json:"checked_in"`
		CheckinCredits int64  `json:"checkin_credits"`
		CheckinEnable  bool   `json:"checkin_enable"`
		Cooling        bool   `json:"cooling"`
		Disabled       bool   `json:"disabled"`
		Error          string `json:"error,omitempty"`
	}
	type subscriptionAcct struct {
		UID                string `json:"uid"`
		Nickname           string `json:"nickname"`
		Subscription       string `json:"subscription,omitempty"` // 空 = 未知，不下发该键
		IdentityID         int    `json:"identity_id"`
		HasPackage         bool   `json:"has_package"`
		FastRequestPer     int64  `json:"fast_request_per"`
		DollarUsageBilling bool   `json:"dollar_usage_billing"`
		Cooling            bool   `json:"cooling"`
		Disabled           bool   `json:"disabled"`
		Error              string `json:"error,omitempty"`
	}

	st := h.cfg.Pool.List()
	out := make([]any, len(st))
	var wg sync.WaitGroup
	for i, s := range st {
		wg.Add(1)
		go func(i int, s pool.Status) {
			defer wg.Done()
			a := h.cfg.Pool.AuthByUID(s.UID)
			if a == nil {
				out[i] = creditAcct{UID: s.UID, Nickname: s.Nickname, Error: "no auth found"}
				return
			}

			if ed.EntitlementMode == "subscription" {
				ac := subscriptionAcct{UID: s.UID, Nickname: s.Nickname, Cooling: s.Cooling, Disabled: s.Disabled}
				sub, err := h.cfg.Upstream.FetchSubscription(a)
				if err != nil {
					ac.Error = "subscription: " + err.Error()
				} else {
					ac.Subscription = sub.Identity
					ac.IdentityID = sub.IdentityID
					ac.HasPackage = sub.HasPackage
					ac.FastRequestPer = sub.FastRequest
					ac.DollarUsageBilling = sub.DollarUsageBilling
				}
				out[i] = ac
				return
			}

			ac := creditAcct{UID: s.UID, Nickname: s.Nickname, Cooling: s.Cooling, Disabled: s.Disabled}
			remain, limit, used, packs, err := h.cfg.Upstream.EntUsage(a)
			if err != nil {
				ac.Error = "ent_usage: " + err.Error()
			} else {
				ac.Remain, ac.Limit, ac.Used, ac.Packs = remain, limit, used, packs
			}
			checkedIn, credits, enable, cerr := h.cfg.Upstream.CheckinStatus(a)
			if cerr != nil {
				if ac.Error != "" {
					ac.Error += "; "
				}
				ac.Error += "checkin: " + cerr.Error()
			} else {
				ac.CheckedIn, ac.CheckinCredits, ac.CheckinEnable = checkedIn, credits, enable
			}
			out[i] = ac
		}(i, s)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{
		"fetched_at":        time.Now().Format("2006-01-02 15:04:05"),
		"edition":           ed.Key,
		"entitlement_mode":  ed.EntitlementMode,
		"checkin_supported": ed.CheckinSupported,
		"accounts":          out,
	})
}
