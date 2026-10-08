package upstream

import "net/http"

const (
	clientPlatform = "desktop-windows"
	clientVersion  = "v1.0.0"
)

// llmHeaders 是 LLM 数据面的固定三件套（上游不做 UA / device 校验）。
func llmHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+accessToken)
}

// desktopHeaders 是站点 / 积分链路要求的客户端身份头。
// withLang 为真时额外带上中文标记（setting_info 用）。
func desktopHeaders(req *http.Request, accessToken string, withLang bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Client-Platform", clientPlatform)
	req.Header.Set("X-Client-Version", clientVersion)
	if withLang {
		req.Header.Set("X-Raccoon-Language", "zh")
	}
}
