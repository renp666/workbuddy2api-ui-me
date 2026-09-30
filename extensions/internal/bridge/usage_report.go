package bridge

import (
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/usagelog"
)

// usageReportLimit 单次上报允许的最大条数，防误配/失控循环。
const usageReportLimit = 64

// usageReportBody 是 console 侧旁路通道（zcode/qoder）经桥接密钥回传的用量观测。
// 上报者并非账号池账号，而是通道占位身份；token 缺失用 -1、credit 用 has_credit
// 区分「未观测」与「0」，与账本缺失语义一致。
type usageReportBody struct {
	Entries []usageReportEntry `json:"entries"`
}

type usageReportEntry struct {
	UID        string   `json:"uid"`
	Account    string   `json:"account"`
	Model      string   `json:"model"`
	Mode       string   `json:"mode"`
	Prompt     int      `json:"prompt_tokens"`
	Completion int      `json:"completion_tokens"`
	Credit     *float64 `json:"credit,omitempty"`
	HasCredit  bool     `json:"has_credit"`
}

// reportUsage 落账 console 旁路通道的调用观测。账本由 core 进程持有，
// console 无其他写入路径，这是两条旁路通道进入「调用统计」的唯一入口。
func (h *handler) reportUsage(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		bridgeError(w, 400, "查询参数无效")
		return
	}
	var body usageReportBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Entries) == 0 || len(body.Entries) > usageReportLimit {
		bridgeError(w, 400, "上报条目数量无效")
		return
	}
	for _, item := range body.Entries {
		model := strings.TrimSpace(item.Model)
		if model == "" || len(model) > 200 {
			bridgeError(w, 400, "上报模型名无效")
			return
		}
	}
	// 内容校验先于接线判定：入参错误一律 400，未接线才 503。
	if h.cfg.Usage == nil {
		bridgeError(w, 503, "调用统计暂不可用，请稍后重试")
		return
	}
	for _, item := range body.Entries {
		e := usagelog.Entry{
			TS:         time.Now().Unix(),
			UID:        item.UID,
			Account:    item.Account,
			Model:      item.Model,
			Mode:       item.Mode,
			Prompt:     item.Prompt,
			Completion: item.Completion,
		}
		if item.HasCredit {
			e.Credit = item.Credit
		}
		h.cfg.Usage.Record(e)
	}
	writeJSON(w, 202, map[string]int{"accepted": len(body.Entries)})
}
