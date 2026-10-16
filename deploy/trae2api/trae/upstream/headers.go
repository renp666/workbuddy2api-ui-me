// headers.go SOLO 三类请求头：对话（SOLOHeaders）/ ug（UgHeaders）/ oauth（OAuthHeaders）。
// 三类的区域可变项（UA 版本、AppID、X-User-Region）一律从 Edition 取，不写死域名侧常量。
package upstream

import (
	"net/http"

	"workbuddy2api-trae/trae/auth"
)

// SOLOHeaders 设置 llm_utils_chat / get_detail_param 所需的 SOLO 专属头。
// 规则来自 SPEC §1 SOLO headers（实测必须）。
func SOLOHeaders(req *http.Request, a *auth.Auth, stream bool, ed Edition) {
	ed = ed.normalized()
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "Trae/"+ed.IdeVersion)
	at := a.JWT() // 读锁快照，防与 RefreshToken 写并发竞态
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+at)
	req.Header.Set("X-Cloudide-Token", at)
	req.Header.Set("X-Ide-Token", at)
	if a.UID != "" {
		req.Header.Set("X-Uid", a.UID)
	}
	req.Header.Set("X-App-Id", ed.AppID)
	req.Header.Set("X-App-Version", "default")
	req.Header.Set("X-Ide-Version", ed.IdeVersion)
	req.Header.Set("X-Ide-Version-Code", ed.IdeVersionCo)
	req.Header.Set("X-App-Version-Code", ed.IdeVersionCo)
	req.Header.Set("X-Ide-Version-Type", "stable")
	req.Header.Set("X-Device-Type", "windows")
	req.Header.Set("X-OS-Version", OSVersion)
	req.Header.Set("X-Device-Brand", DeviceBrand)
	req.Header.Set("Request-Traffic-Type", "prod")
	if a.MachineID != "" {
		req.Header.Set("X-Machine-Id", a.MachineID)
	}
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// UgHeaders 设置权益类请求（积分/签到/订阅态）所需头。
// X-User-Region 只在 Edition 给了值时才发：国际版没有归属可填，硬写 "CN" 会让
// 上游按国内区域路由到不存在的端点，表现是一条像故障的 404。
func UgHeaders(req *http.Request, a *auth.Auth, ed Edition) {
	ed = ed.normalized()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Trae/"+ed.IdeVersion)
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+a.JWT()) // 读锁快照
	if ed.UserRegion != "" {
		req.Header.Set("X-User-Region", ed.UserRegion)
	}
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// OAuthHeaders 设置 ExchangeToken / GetUserInfo 所需头（无签名，仅 UA）。
func OAuthHeaders(req *http.Request, ed Edition) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Trae/"+ed.normalized().IdeVersion)
}
