// Package upstream 对接小浣熊（xiaohuanxiong.com）上游：LLM 数据面、鉴权刷新、
// 模型目录、余额 / 订阅与每日积分发放。
//
// 端点与字段口径来自桌面端与 github.com/aimod-cc/agent2api 的 raccoon provider：
//   - LLM：POST {llmBase}/chat/completions（OpenAI 兼容，SSE），请求头三件套
//   - 刷新：POST {authOrigin}/api/web/auth/v1/refresh
//   - 目录：GET {llmBase}/model_catalog
//   - 余额：GET {mainSite}/api/web/points/v1/balance
//   - 订阅：GET {authOrigin}/api/web/auth/v1/entitlement_info
//   - 积分：POST {mainSite}/api/web/desktop/v1/login/points/grant
//     GET  {mainSite}/api/web/office/v3/setting_info
//     GET  {mainSite}/api/web/points/v1/bills
package upstream

import "time"

const (
	// DefaultLLMBaseURL 是 LLM 数据面根地址。
	DefaultLLMBaseURL = "https://xiaohuanxiong.com/api/web/llm/v2"
	// DefaultAuthOrigin 是鉴权接口根地址。
	DefaultAuthOrigin = "https://xiaohuanxiong.com"
	// DefaultMainSite 是积分 / 站点接口根地址。
	DefaultMainSite = "https://xiaohuanxiong.com"

	// EpRefresh 是刷新端点（挂在鉴权前缀下）。
	EpRefresh = "/refresh"
	// EpChat 是聊天端点（挂在 LLM 前缀下）。
	EpChat = "/chat/completions"
	// EpCatalog 是模型目录端点（挂在 LLM 前缀下）。
	EpCatalog = "/model_catalog"
	// EpBalance 是积分余额端点。
	EpBalance = "/api/web/points/v1/balance"
	// EpEntitlement 是订阅权益端点。
	EpEntitlement = "/api/web/auth/v1/entitlement_info"
	// EpDesktopGrant 是一次性桌面登录积分奖励。
	EpDesktopGrant = "/api/web/desktop/v1/login/points/grant"
	// EpSettingInfo 触发每日积分发放。
	EpSettingInfo = "/api/web/office/v3/setting_info"
	// EpBills 是积分账单（核对入账的权威来源）。
	EpBills = "/api/web/points/v1/bills"

	// DefaultModel 是默认对话模型（上游无「默认模型」语义，此处为本仓约定）。
	DefaultModel = "raccoon-chat-ml-5-5"

	// RefreshMargin 是临期前主动刷新的余量。
	RefreshMargin = 5 * time.Minute
	// RequestTimeout 是短 RPC 的总超时。
	RequestTimeout = 20 * time.Second
	// StreamTimeout 是流式生成的首包等待上限；不设请求级总超时（长回答合法）。
	StreamTimeout = 15 * time.Minute
	// CatalogTimeout 是模型目录超时。
	CatalogTimeout = 10 * time.Second
	// CatalogTTL 是模型目录缓存时长。
	CatalogTTL = 10 * time.Minute
	// GrantCheckTimeout 是积分入账核对（bills）的独立超时。
	GrantCheckTimeout = 25 * time.Second
)

// staticModels 是远程目录不可用时的兜底目录（与上游桌面端一致）。
var staticModels = []ModelInfo{
	{ID: "raccoon-8c4485", Name: "Raccoon-Work", OwnedBy: "xiaohuanxiong"},
	{ID: "raccoon-19b265", Name: "Raccoon-Work-260817-A", OwnedBy: "xiaohuanxiong"},
	{ID: "raccoon-405a1c", Name: "Raccoon-Work-260817-B", OwnedBy: "xiaohuanxiong"},
	{ID: DefaultModel, Name: "Raccoon Chat ML 5.5", OwnedBy: "xiaohuanxiong"},
	{ID: "sn-sensenova-6-8-flash-lite", Name: "SenseNova 6.8 Flash Lite", OwnedBy: "xiaohuanxiong"},
}

// StaticModels 返回兜底目录的副本。
func StaticModels() []ModelInfo {
	out := make([]ModelInfo, len(staticModels))
	copy(out, staticModels)
	return out
}
