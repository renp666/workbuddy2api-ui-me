// edition.go 区域上游参数集：一只 sidecar 进程 = 一个区域。
//
// 国内版与国际版的**协议形状相同**（同路径、同请求头名、同 SSE 事件集、同 function 作用域），
// 差异只在主机名、权益体系与模型目录，因此这里只承载"会变的量"，不复制业务逻辑。
// 国际版取值全部来自 2026-10-16 本机 SG Pro 账号实测：
//   - coresg-normal.trae.ai 对 /api/agent/v3/llm_utils_chat 与 /api/ide/v1/get_detail_param
//     的响应形状与国内版 trae-api-cn.mchost.guru 逐字一致（含真实 token_usage）；
//   - ug-normal.trae.ai 对 /cloudide/api/v3/trae/{oauth/ExchangeToken,GetUserInfo} 返回
//     JSON 错误信封，而对 /api/agent/* 返回 404，故它只做鉴权/权益 host；
//   - api.trae.ai 看似最像 API 主机，实际对 /trae/* 与 /api/agent/* 全 404，不作为默认值；
//   - /trae/api/v2/ug/checkin_credits/* 与 /trae/api/v2/pay/ide_user_ent_usage 在四个国际
//     host 上一律 404（对照：同路径打 api.trae.cn 返回 JSON 信封），即国际版无签到/积分体系。
package upstream

import (
	"log"
	"os"
	"strings"
)

// Edition 一套区域上游参数。零值按国内版补齐，便于测试里只覆盖用到的字段。
type Edition struct {
	// Key 区域标识：cn / intl。写入 /status 与日志，避免运维靠域名猜区域。
	Key   string
	Label string

	AgentHost   string // 对话 + 模型目录
	OAuthHost   string // ExchangeToken / GetUserInfo 回落 host（账号自带 ApiHost 优先）
	UgHost      string // 权益/签到 host
	ConsoleHost string // 浏览器授权页 host
	ClientID    string // 主 client_id（授权页 URL 与首次刷新用它）
	// ClientIDAlt 备用类别 client_id：刷新遇 10101「not matched to the client」时再试一次。
	// 存在的理由：凭据由哪类客户端签发不由区域决定（国际版桌面客户端是 TRAE 类，
	// 国内 SOLO 是 SOLO 类），而凭据文件本身不记录类别，只能试出来并记住。
	ClientIDAlt   string
	AppID         string
	IdeVersion    string
	IdeVersionCo  string // X-Ide-Version-Code：填错会被上游把模型目录裁空（实测 2026-10-16）
	Function      string
	FunctionAgent string
	UserRegion    string // X-User-Region，空则不发该头

	EpCheckinStatus string
	EpCheckinClaim  string
	EpEntUsage      string // 国内版积分
	EpSubscription  string // 国际版订阅态（权益体系的替代物）

	// CheckinSupported 国际版无签到体系：调度器与状态端点据此整体跳过，
	// 不发注定 404 的请求去刷告警。
	CheckinSupported bool
	// KeepCustomModels 是否保留上游目录里的 config_source==3 条目。
	// 国内版必须剔：那一类在本通道里就是"本网关别名被人工加回 IDE"的回流（列出即回环）。
	// 国际版保留：它没有这个生态，那批条目是 BYOK 槽位，与别名无关。
	// 字段取反命名是为了让零值 Edition 落在国内版行为上（false=剔除）。
	KeepCustomModels bool
	// EntitlementMode 决定管理页账号条目怎么表达"还能用多少"：
	// "credits" 显示积分余额，"subscription" 显示订阅档位（不折算成积分数字，
	// 因为国际版本就没有积分余额这个量，合成一个数会污染 core 账本口径）。
	EntitlementMode  string
	DefaultModelName string
}

// EditionCN 国内版预设，取值来自 constants.go。
func EditionCN() Edition {
	return Edition{
		Key:              "cn",
		Label:            "Trae 国内版",
		AgentHost:        AgentHost,
		OAuthHost:        OAuthHost,
		UgHost:           UgHost,
		ConsoleHost:      ConsoleHost,
		ClientID:         ClientIDSOLO,
		ClientIDAlt:      ClientIDTrae,
		AppID:            AppID,
		IdeVersion:       IdeVersion,
		IdeVersionCo:     IdeVersionCode,
		Function:         Function,
		FunctionAgent:    FunctionAgent,
		UserRegion:       "CN",
		EpCheckinStatus:  EpCheckinStatus,
		EpCheckinClaim:   EpCheckinClaim,
		EpEntUsage:       EpEntUsage,
		CheckinSupported: true,
		EntitlementMode:  "credits",
		DefaultModelName: DefaultConfigName,
	}
}

// EditionIntl 国际版预设（trae.ai 一族，SG 入口）。
func EditionIntl() Edition {
	return Edition{
		Key:           "intl",
		Label:         "Trae 国际版",
		AgentHost:     "https://coresg-normal.trae.ai",
		OAuthHost:     "https://ug-normal.trae.ai",
		UgHost:        "https://ug-normal.trae.ai",
		ConsoleHost:   "https://www.trae.ai",
		ClientID:      ClientIDSOLO,
		ClientIDAlt:   ClientIDTrae,
		AppID:         AppID,
		IdeVersion:    IdeVersion,
		IdeVersionCo:  IdeVersionCode,
		Function:      Function,
		FunctionAgent: FunctionAgent,
		// 国际版没有签到/积分端点，X-User-Region 也无从归属 → 不发该头。
		UserRegion:       "",
		EpSubscription:   "/trae/api/v1/pay/ide_user_pay_status",
		CheckinSupported: false,
		KeepCustomModels: true,
		EntitlementMode:  "subscription",
		DefaultModelName: "gpt-5.4",
	}
}

// FromEnv 按 TRAE_EDITION 选预设，再允许单项覆盖。
//
// 覆盖口的存在是因为国际版按区域分入口（SG=coresg、US=coreva、中性=core-normal），
// 上游换域名时改 compose 即可，不必改代码重 build 镜像。
func FromEnv() Edition {
	ed := EditionCN()
	switch key := strings.ToLower(strings.TrimSpace(os.Getenv("TRAE_EDITION"))); key {
	case "", "cn":
	case "intl", "global", "sg":
		ed = EditionIntl()
	default:
		log.Printf("TRAE_EDITION=%q 未知，回落国内版预设", key)
	}
	ed.AgentHost = envStr("TRAE_AGENT_HOST", ed.AgentHost)
	ed.OAuthHost = envStr("TRAE_OAUTH_HOST", ed.OAuthHost)
	ed.UgHost = envStr("TRAE_UG_HOST", ed.UgHost)
	ed.ConsoleHost = envStr("TRAE_CONSOLE_HOST", ed.ConsoleHost)
	ed.ClientID = envStr("TRAE_CLIENT_ID", ed.ClientID)
	ed.ClientIDAlt = envStr("TRAE_CLIENT_ID_ALT", ed.ClientIDAlt)
	ed.IdeVersion = envStr("TRAE_IDE_VERSION", ed.IdeVersion)
	ed.IdeVersionCo = envStr("TRAE_IDE_VERSION_CODE", ed.IdeVersionCo)
	return ed
}

// normalized 把 Edition 补齐成可用的值。
//
// Key 为空 = 调用方只想覆盖几个 host 就跑国内版链路（测试里普遍如此），此时**整体继承
// 国内版预设**再叠加显式给出的字符串字段。这里必须整体继承而不是逐字段兜底：
// CheckinSupported/KeepCustomModels 这类布尔在国内版的期望值是 true/false，
// 逐字段兜底拿不到"未设置"和"显式设为 false"的区别，会把国内版用例静默判成国际版。
func (e Edition) normalized() Edition {
	def := EditionCN()
	if e.Key == "" {
		out := def
		if e.AgentHost != "" {
			out.AgentHost = e.AgentHost
		}
		if e.OAuthHost != "" {
			out.OAuthHost = e.OAuthHost
		}
		if e.UgHost != "" {
			out.UgHost = e.UgHost
		}
		if e.ConsoleHost != "" {
			out.ConsoleHost = e.ConsoleHost
		}
		if e.ClientID != "" {
			out.ClientID = e.ClientID
		}
		if e.ClientIDAlt != "" {
			out.ClientIDAlt = e.ClientIDAlt
		}
		if e.AppID != "" {
			out.AppID = e.AppID
		}
		if e.IdeVersion != "" {
			out.IdeVersion = e.IdeVersion
		}
		if e.IdeVersionCo != "" {
			out.IdeVersionCo = e.IdeVersionCo
		}
		if e.Function != "" {
			out.Function = e.Function
		}
		if e.FunctionAgent != "" {
			out.FunctionAgent = e.FunctionAgent
		}
		if e.DefaultModelName != "" {
			out.DefaultModelName = e.DefaultModelName
		}
		return out
	}
	if e.AgentHost == "" {
		e.AgentHost = def.AgentHost
	}
	if e.OAuthHost == "" {
		e.OAuthHost = def.OAuthHost
	}
	if e.UgHost == "" {
		e.UgHost = def.UgHost
	}
	if e.ConsoleHost == "" {
		e.ConsoleHost = def.ConsoleHost
	}
	if e.ClientID == "" {
		e.ClientID = def.ClientID
	}
	if e.ClientIDAlt == "" {
		e.ClientIDAlt = def.ClientIDAlt
	}
	if e.AppID == "" {
		e.AppID = def.AppID
	}
	if e.IdeVersion == "" {
		e.IdeVersion = def.IdeVersion
	}
	if e.IdeVersionCo == "" {
		e.IdeVersionCo = def.IdeVersionCo
	}
	if e.Function == "" {
		e.Function = def.Function
	}
	if e.FunctionAgent == "" {
		e.FunctionAgent = def.FunctionAgent
	}
	if e.EpCheckinStatus == "" {
		e.EpCheckinStatus = def.EpCheckinStatus
	}
	if e.EpCheckinClaim == "" {
		e.EpCheckinClaim = def.EpCheckinClaim
	}
	if e.EpEntUsage == "" {
		e.EpEntUsage = def.EpEntUsage
	}
	if e.EpSubscription == "" {
		e.EpSubscription = def.EpSubscription
	}
	if e.EntitlementMode == "" {
		e.EntitlementMode = def.EntitlementMode
	}
	if e.DefaultModelName == "" {
		e.DefaultModelName = def.DefaultModelName
	}
	return e
}

// HostDomain 从 OAuthHost 取注册域（api.trae.com.cn → trae.com.cn），落进 Auth.Domain
// 供面板显示与排障归因；不写死 "trae.cn"，否则国际版账号会被标成国内域名。
func (e Edition) HostDomain() string {
	h := strings.ToLower(e.normalized().OAuthHost)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/:"); i >= 0 {
		h = h[:i]
	}
	if j := strings.Index(h, "."); j >= 0 {
		return h[j+1:]
	}
	return h
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
