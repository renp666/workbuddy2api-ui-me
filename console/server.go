package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type Config struct {
	CoreURL  *url.URL
	ZCodeURL *url.URL // Optional second upstream for glm-* models; nil disables routing.
	ZCodeKey string   // Replaces Authorization when forwarding to ZCodeURL.
	// ZCodeControlURL is the zcode-proxy localhost control API (POST /control).
	// nil keeps the Zcode tab read-only; set it to drive login/enable/disable/logout in-page.
	ZCodeControlURL *url.URL
	// ZCodeDataDir 是 zcode-proxy 持久化目录（runtime/zcode）在 console 侧的挂载点。
	// 空表示未挂载，账号档案功能整体关闭，页签只保留通道状态与登录。
	// 目录里的凭据是 sidecar 用容器内恒定密钥加密的 blob：console 只做整文件拷贝，
	// 不解密、不解析、不比较内容语义，因此换文件即换账号。
	ZCodeDataDir string
	// QoderURL is the optional qoder-proxy upstream for qoder-* models; nil disables routing.
	QoderURL *url.URL
	// QoderKey replaces Authorization when forwarding to QoderURL.
	QoderKey string
	// QoderControlURL 是 qoder-proxy 容器内 qoder-login-ctl 的地址（仅回环）。
	// nil 时 Qoder 页签不提供登录/登出（纯 PAT 或只读模式）。
	QoderControlURL *url.URL
	// OpenCodeURL is the optional opencode sidecar (OW Bridge) upstream for
	// opencode-* models; nil disables routing.
	OpenCodeURL *url.URL
	// OpenCodeKey replaces Authorization when forwarding to OpenCodeURL.
	OpenCodeKey string
	// CCRURL 是可选的 claude-code-router 管理服务地址（宿主回环，容器侧经 host.docker.internal）。
	// nil 时不启用 CCR 页签；它不是模型上游，不参与公共出口分流。
	CCRURL *url.URL
	// CCRTokenFile 是 CCR 自己写的 service.json 路径（只读挂载）。管理 Token 每次启动轮换，
	// 只能现读，不能写成环境变量。
	CCRTokenFile string
	// AutoClawURL 是可选的 autoclaw2api 容器地址（autoclaw-* 模型上游）；nil 时不启用分流。
	AutoClawURL *url.URL
	// AutoClawKey 是转发到 AutoClawURL 时替换 Authorization 的桥接密钥（bridge 的 AC2A_API_KEY）。
	AutoClawKey string
	// FreellmURL 是可选的 FreeLLMAPI sidecar 容器地址（freellm-* 模型上游）；nil 时不启用分流。
	FreellmURL *url.URL
	// FreellmKey 是转发到 FreellmURL 时替换 Authorization 的统一 key（freellmapi-…）。
	FreellmKey string
	// TraeURL 是可选的 trae2api sidecar 容器地址（trae-* 模型上游）；nil 时不启用分流。
	TraeURL *url.URL
	// TraeKey 是转发到 TraeURL 时替换 Authorization 的桥接密钥（sidecar 的 TRAE_API_KEY）。
	TraeKey string
	// TraeAIURL/TraeAIKey 是同一 sidecar 镜像的国际版实例（TRAE_EDITION=intl，
	// traeai-* 模型上游）；nil 时不启用分流，公共出口与现状一致。
	TraeAIURL *url.URL
	TraeAIKey string
	// RaccoonURL 是可选的 raccoon2api sidecar 容器地址（raccoon-* 模型上游）；nil 时不启用分流。
	RaccoonURL *url.URL
	// RaccoonKey 是转发到 RaccoonURL 时替换 Authorization 的桥接密钥（sidecar 的 RACCOON_API_KEY）。
	RaccoonKey string
	// RouteFile 是 console 自己可写的路由别名配置文件路径（容器内路径）。
	// 为空时不启用别名功能，公共出口与现状完全一致。
	RouteFile    string
	AdminKey     string
	APIKey       string // Only exposed by the authenticated, CSRF-protected access endpoint.
	BridgeKey    string
	PublicOrigin string
	// TrustedProxyCIDRs lists proxies allowed to name the client in X-Forwarded-For.
	// It is consumed by the login limiter only; same-origin and CSRF checks keep using the connection peer.
	TrustedProxyCIDRs string
	RequireHTTPS      bool
	// HealthProbe 决定 console 是否启动后台常态探活协程（聚合端点 GET /admin/health
	// 始终可用）。探活只读端口/服务存活，不消耗上游额度。生产由 configFromEnv 置真；
	// 测试构造的 Config 保持零值，避免后台请求干扰以 mock 上游断言调用次数的用例。
	// 健康检查模块 M1 起，它同时是「错误账本 + 出口观测」的总开关：关闭时错误事件
	// 不落盘、出口观测整体停用（见 NewServer），保证既有测试不被异步写文件干扰。
	HealthProbe bool
}
type adminSession struct {
	csrf, owner string
	expires     time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	oauthGate   chan struct{}
}
type loginLimit struct {
	since time.Time
	count int
}
type sessionContextKey struct{}
type server struct {
	cfg      Config
	mux      *http.ServeMux
	mu       sync.Mutex
	sessions map[string]*adminSession
	limits   map[string]loginLimit
	client   *http.Client
	trusted  []*net.IPNet
	// zcodeClient fetches both /v1/models lists for the merged public endpoint. It has no
	// overall timeout so slow core model probes keep the same semantics as the plain proxy path.
	zcodeClient *http.Client
	// httpsEnabled is derived from PublicOrigin and only gates HSTS; it is not an auth check.
	httpsEnabled bool
	// zcodeUserStopped 记录本运行周期内用户是否手动停用过 zcode 代理：
	// 自动拉起协程在该标志为 true 时不再 startProxy，避免和管理员意图打架。
	// 容器重启后内存归零，即恢复自动拉起。手动 enable 会清掉该标志。
	zcodeUserStopped bool
	// routes 是「对外别名 → 真实上游」的路由表，由控制台页面编辑、保存即生效。
	// 为 nil 时公共出口与现状完全一致，不改变任何既有分流语义。
	routes *routeStore
	// probes 缓存各通道的模型探测结论，供 gateway-auto 虚拟模型挑选最优链路。
	probes *probeCache
	// probe 是手工测速探测的运行器与结论缓存（内存态，重启即清空）。
	probe *probeRunner
	// credits 缓存 core 下发的模型积分消耗规则（补丁 0011 的 credits 字段），
	// 供模型开关判定默认态与页面展示。
	credits *creditCache
	// apiKeys 是附加 API Key 清单（多 Key 归因，对标项 #5）：console 权威校验 +
	// 账本归属。为 nil 时（RouteFile 未配置）gate 只认主 Key，行为与没有本功能时一致。
	apiKeys *apiKeyStore
	// catalog 缓存 OpenRouter 公开目录的一份快照，同时供「全球热度」列和
	// 「模型能力 TOP」榜单派生，保证两列出自同一次抓取。
	catalog *catalogCache
	// health 保留后台常态探活的历史轮次（内存态，重启清空）。
	health *healthCache
	// outcomes 是公共出口调用的滚动结果窗口（成功+失败），/admin/errors 聚合的
	// 数据源；内存态、重启从零累计。为 nil 时出口观测整体停用，公共出口行为与
	// 没有本功能时完全一致。
	outcomes *outcomeWindow
	// ledger 是错误事件账本（errors-YYYYMMDD.jsonl，append-only、按天轮转）。
	// 为 nil 时失败调用不落盘，只进内存窗口。
	ledger *errorLedger
	// alerts 是 P1 告警账本（alerts.jsonl 单文件、保留 30 天 + 内存环形缓冲）。
	// 为 nil 时不做告警检测；与 outcomes/ledger 同一道模块总开关（HealthProbe）。
	circuits *circuitBreaker
	alerts   *alertLedger
	// zcodeQuota 缓存 zcode 账号在 Z.AI 侧的真实模型权益（sidecar GET /quota 的
	// balances[].showName），GLM 目录按它过滤，不再靠档位名推断。
	zcodeQuota *zcodeQuotaCache
}

func NewServer(cfg Config) (http.Handler, error) {
	if cfg.CoreURL == nil || !validOriginURL(cfg.CoreURL) {
		return nil, errors.New("WB2A_CORE_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.ZCodeURL != nil && !validOriginURL(cfg.ZCodeURL) {
		return nil, errors.New("WB2A_ZCODE_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.ZCodeControlURL != nil && !validOriginURL(cfg.ZCodeControlURL) {
		return nil, errors.New("WB2A_ZCODE_CONTROL_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.QoderURL != nil && !validOriginURL(cfg.QoderURL) {
		return nil, errors.New("WB2A_QODER_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.QoderControlURL != nil && !validOriginURL(cfg.QoderControlURL) {
		return nil, errors.New("WB2A_QODER_CONTROL_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.OpenCodeURL != nil && !validOriginURL(cfg.OpenCodeURL) {
		return nil, errors.New("WB2A_OPENCODE_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.CCRURL != nil && !validOriginURL(cfg.CCRURL) {
		return nil, errors.New("WB2A_CCR_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址，管理 Token 只从挂载的 service.json 读取")
	}
	if cfg.AutoClawURL != nil && !validOriginURL(cfg.AutoClawURL) {
		return nil, errors.New("WB2A_AUTOCLAW_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.TraeURL != nil && !validOriginURL(cfg.TraeURL) {
		return nil, errors.New("WB2A_TRAE_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.TraeAIURL != nil && !validOriginURL(cfg.TraeAIURL) {
		return nil, errors.New("WB2A_TRAEAI_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.RaccoonURL != nil && !validOriginURL(cfg.RaccoonURL) {
		return nil, errors.New("WB2A_RACCOON_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if cfg.FreellmURL != nil && !validOriginURL(cfg.FreellmURL) {
		return nil, errors.New("WB2A_FREELLMAPI_URL 必须是无凭据、路径、查询和片段的 HTTP(S) 地址")
	}
	if !ValidateAdminOrigin(cfg.PublicOrigin) {
		return nil, errors.New("WB2A_PUBLIC_ORIGIN 必须是有效的 HTTP(S) origin")
	}
	if len(cfg.AdminKey) < 32 || len(cfg.BridgeKey) < 32 || cfg.APIKey == "" || cfg.AdminKey == cfg.BridgeKey || cfg.AdminKey == cfg.APIKey || cfg.BridgeKey == cfg.APIKey {
		return nil, errors.New("管理和桥接密钥须至少 32 字节，三种密钥须非空且互不相同")
	}
	target := *cfg.CoreURL
	target.Path = ""
	cfg.CoreURL = &target
	if cfg.ZCodeURL != nil {
		zcodeTarget := *cfg.ZCodeURL
		zcodeTarget.Path = ""
		cfg.ZCodeURL = &zcodeTarget
	}
	if cfg.QoderURL != nil {
		qoderTarget := *cfg.QoderURL
		qoderTarget.Path = ""
		cfg.QoderURL = &qoderTarget
	}
	if cfg.QoderControlURL != nil {
		qoderControlTarget := *cfg.QoderControlURL
		qoderControlTarget.Path = ""
		cfg.QoderControlURL = &qoderControlTarget
	}
	if cfg.OpenCodeURL != nil {
		opencodeTarget := *cfg.OpenCodeURL
		opencodeTarget.Path = ""
		cfg.OpenCodeURL = &opencodeTarget
	}
	if cfg.CCRURL != nil {
		ccrTarget := *cfg.CCRURL
		ccrTarget.Path = ""
		cfg.CCRURL = &ccrTarget
	}
	if cfg.AutoClawURL != nil {
		autoclawTarget := *cfg.AutoClawURL
		autoclawTarget.Path = ""
		cfg.AutoClawURL = &autoclawTarget
	}
	if cfg.TraeURL != nil {
		traeTarget := *cfg.TraeURL
		traeTarget.Path = ""
		cfg.TraeURL = &traeTarget
	}
	if cfg.TraeAIURL != nil {
		traeAITarget := *cfg.TraeAIURL
		traeAITarget.Path = ""
		cfg.TraeAIURL = &traeAITarget
	}
	if cfg.RaccoonURL != nil {
		raccoonTarget := *cfg.RaccoonURL
		raccoonTarget.Path = ""
		cfg.RaccoonURL = &raccoonTarget
	}
	if cfg.FreellmURL != nil {
		freellmTarget := *cfg.FreellmURL
		freellmTarget.Path = ""
		cfg.FreellmURL = &freellmTarget
	}
	cfg.PublicOrigin = strings.TrimRight(cfg.PublicOrigin, "/")
	var trusted []*net.IPNet
	for _, raw := range strings.Split(cfg.TrustedProxyCIDRs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, errors.New("WB2A_TRUSTED_PROXY_CIDRS 须是逗号分隔的 CIDR 列表")
		}
		if ones, bits := network.Mask.Size(); ones == 0 && bits > 0 {
			log.Printf("[console] 警告：WB2A_TRUSTED_PROXY_CIDRS 含 %s，等于信任所有直连对端，X-Forwarded-For 可被任意伪造；仅当 console 只经可信代理可达时才是安全的", network.String())
		}
		trusted = append(trusted, network)
	}
	h := &server{cfg: cfg, mux: http.NewServeMux(), sessions: map[string]*adminSession{}, limits: map[string]loginLimit{}, client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, zcodeClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, trusted: trusted, httpsEnabled: strings.HasPrefix(cfg.PublicOrigin, "https://")}
	if cfg.RouteFile != "" {
		h.routes = newRouteStore(cfg.RouteFile)
		// 附加 API Key 清单与 routes.json 同目录（consoleDataDir）；文件缺失视为
		// 空清单，损坏时告警降级（见 newAPIKeyStore），不阻塞启动。
		h.apiKeys = newAPIKeyStore(filepath.Join(consoleDataDir(cfg.RouteFile), "apikeys.json"))
	}
	h.probes = &probeCache{}
	h.probe = newProbeRunner()
	h.credits = &creditCache{}
	h.catalog = &catalogCache{}
	h.health = &healthCache{}
	// 错误账本与滚动窗口（健康检查 M1 · F1）：与后台探活共用 HealthProbe 这道模块
	// 总开关（生产由 configFromEnv 置真）。之所以不能只看 RouteFile 是否存在：账本
	// 由独立协程异步落盘，若测试里只配了 RouteFile（t.TempDir）就写文件，协程会活到
	// 测试结束之后、和 t.TempDir 的清理赛跑（Windows 上表现为「目录非空」偶发红）。
	// 关掉时 outcomes 与 ledger 均为 nil，出口行为与没有本功能时完全一致（不建文件）。
	if dir := consoleDataDir(cfg.RouteFile); cfg.HealthProbe && dir != "" {
		h.outcomes = newOutcomeWindow()
		h.ledger = newErrorLedger(dir)
		h.ledger.cleanup(time.Now())
		h.alerts = newAlertLedger(dir)
		h.alerts.cleanup(time.Now())
		h.initCircuits(time.Now)
	}
	h.zcodeQuota = &zcodeQuotaCache{}
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(assets))
	h.mux.Handle("GET /{$}", fileServer)
	for _, name := range []string{"app.js", "style.css", "favicon.svg"} {
		h.mux.Handle("GET /"+name, fileServer)
	}
	h.mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"service": "workbuddy2api-console", "status": "running"})
	})
	public := h.proxy(false)
	// 只要存在任一需要「先解析请求模型名再分流」的能力，就必须走 publicRouter：
	// 旁路通道靠它按前缀分流，路由别名与 auto 虚拟模型也只在它内部生效。
	// 只配了 core 的部署（RouteFile 非空但旁路 URL 全空）同样要挂上，
	// 否则别名改写与 /v1/models 的 auto 条目会静默失效。
	// 公共出口鉴权权威（多 Key 归因，对标项 #5）：/v1/* 与 GET /status 先过
	// apiKeyGate（主 Key + 附加清单，未命中自产 401），命中后 core 通道统一换发
	// cfg.APIKey。/healthz 保持免鉴权（探活语义不变）。
	if cfg.ZCodeURL != nil || cfg.QoderURL != nil || cfg.OpenCodeURL != nil || cfg.AutoClawURL != nil || cfg.TraeURL != nil || cfg.RaccoonURL != nil || cfg.FreellmURL != nil || cfg.RouteFile != "" {
		var routed http.Handler = h.publicRouter(public)
		if h.circuits != nil {
			routed = h.circuitExitGuard(routed)
		}
		h.mux.Handle("/v1/", h.observePublicExits(h.apiKeyGate(routed)))
	} else {
		h.mux.Handle("/v1/", h.observePublicExits(h.apiKeyGate(public)))
	}
	h.mux.Handle("GET /status", h.apiKeyGate(public))
	h.mux.Handle("GET /healthz", public)
	h.mux.HandleFunc("POST /admin/login", h.adminLogin)
	h.mux.HandleFunc("GET /admin/session", h.withAdmin(func(w http.ResponseWriter, r *http.Request) {
		info, err := h.coreInfo(r.Context())
		if err != nil {
			adminError(w, 503, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"csrf": sessionFrom(r).csrf, "global_enabled": info.GlobalEnabled, "zcode_enabled": cfg.ZCodeURL != nil, "zcode_control": cfg.ZCodeControlURL != nil, "qoder_enabled": cfg.QoderURL != nil, "qoder_control": cfg.QoderControlURL != nil, "opencode_enabled": cfg.OpenCodeURL != nil, "ccr_enabled": cfg.CCRURL != nil, "autoclaw_enabled": cfg.AutoClawURL != nil, "trae_enabled": cfg.TraeURL != nil, "traeai_enabled": cfg.TraeAIURL != nil, "raccoon_enabled": cfg.RaccoonURL != nil, "freellm_enabled": cfg.FreellmURL != nil, "apikeys_enabled": h.apiKeys != nil})
	}))
	h.mux.HandleFunc("POST /admin/logout", h.withAdmin(h.adminLogout))
	h.mux.HandleFunc("GET /admin/zcode", h.withAdmin(h.adminZcodeStatus))
	h.mux.HandleFunc("POST /admin/zcode/chat", h.withAdmin(h.adminZcodeChat))
	h.mux.HandleFunc("POST /admin/zcode/login", h.withAdmin(h.adminZcodeLogin))
	h.mux.HandleFunc("POST /admin/zcode/config", h.withAdmin(h.adminZcodeConfig))
	h.mux.HandleFunc("POST /admin/zcode/enable", h.withAdmin(h.adminZcodeEnable))
	h.mux.HandleFunc("POST /admin/zcode/disable", h.withAdmin(h.adminZcodeDisable))
	h.mux.HandleFunc("POST /admin/zcode/logout", h.withAdmin(h.adminZcodeLogout))
	// 账号档案（手工轮换）：zcode-proxy 本身单凭据，多账号由 console 在文件层留档实现。
	h.mux.HandleFunc("GET /admin/zcode/accounts", h.withAdmin(h.adminZcodeAccounts))
	h.mux.HandleFunc("POST /admin/zcode/accounts/save", h.withAdmin(h.adminZcodeAccountSave))
	h.mux.HandleFunc("POST /admin/zcode/accounts/switch", h.withAdmin(h.adminZcodeAccountSwitch))
	h.mux.HandleFunc("POST /admin/zcode/accounts/delete", h.withAdmin(h.adminZcodeAccountDelete))
	h.mux.HandleFunc("POST /admin/zcode/accounts/rename", h.withAdmin(h.adminZcodeAccountRename))
	h.mux.HandleFunc("GET /admin/qoder", h.withAdmin(h.adminQoderStatus))
	h.mux.HandleFunc("POST /admin/qoder/chat", h.withAdmin(h.adminQoderChat))
	h.mux.HandleFunc("POST /admin/qoder/login", h.withAdmin(h.adminQoderLoginStart))
	h.mux.HandleFunc("GET /admin/qoder/login", h.withAdmin(h.adminQoderLoginPoll))
	h.mux.HandleFunc("POST /admin/qoder/logout", h.withAdmin(h.adminQoderLogout))
	h.mux.HandleFunc("GET /admin/opencode", h.withAdmin(h.adminOpenCodeStatus))
	h.mux.HandleFunc("POST /admin/opencode/chat", h.withAdmin(h.adminOpenCodeChat))
	h.mux.HandleFunc("GET /admin/freellm", h.withAdmin(h.adminFreellmStatus))
	h.mux.HandleFunc("POST /admin/freellm/chat", h.withAdmin(h.adminFreellmChat))
	// AutoClaw 页签：状态/测试对话 + 完整控制链路（登录、账号启停、维护、签到、积分）。
	h.mux.HandleFunc("GET /admin/autoclaw", h.withAdmin(h.adminAutoClawStatus))
	h.mux.HandleFunc("POST /admin/autoclaw/chat", h.withAdmin(h.adminAutoClawChat))
	h.mux.HandleFunc("POST /admin/autoclaw/login/cn/sendcode", h.withAdmin(h.adminAutoClawSendCode))
	h.mux.HandleFunc("POST /admin/autoclaw/login/cn", h.withAdmin(h.adminAutoClawLoginCN))
	h.mux.HandleFunc("POST /admin/autoclaw/login/global/import-desktop", h.withAdmin(h.adminAutoClawImportDesktop))
	h.mux.HandleFunc("POST /admin/autoclaw/login/global/oauth", h.withAdmin(h.adminAutoClawOAuth))
	h.mux.HandleFunc("POST /admin/autoclaw/accounts/toggle", h.withAdmin(h.adminAutoClawAccountsToggle))
	h.mux.HandleFunc("POST /admin/autoclaw/accounts/delete", h.withAdmin(h.adminAutoClawAccountsDelete))
	h.mux.HandleFunc("POST /admin/autoclaw/maintain", h.withAdmin(h.adminAutoClawMaintain))
	h.mux.HandleFunc("POST /admin/autoclaw/credits/refresh", h.withAdmin(h.adminAutoClawCreditsRefresh))
	h.mux.HandleFunc("GET /admin/autoclaw/credits", h.withAdmin(h.adminAutoClawCredits))
	h.mux.HandleFunc("POST /admin/autoclaw/checkin", h.withAdmin(h.adminAutoClawCheckin))
	// Trae 页签（国内版 trae-* 与国际版 traeai-*）：状态/测试对话 + 控制链路。
	// 两只 sidecar 是同一个镜像、同一套控制协议，所以这里按通道描述符循环注册，
	// 路径段就是通道名（/admin/trae、/admin/traeai），不再复制 16 行注册 ×2。
	for _, ch := range h.traeChannels() {
		base := "/admin/" + string(ch.channel)
		h.mux.HandleFunc("GET "+base, h.withAdmin(h.adminTraeStatus(ch)))
		h.mux.HandleFunc("POST "+base+"/chat", h.withAdmin(h.adminTraeChat(ch)))
		h.mux.HandleFunc("POST "+base+"/login", h.withAdmin(h.adminTraeLoginStart(ch)))
		h.mux.HandleFunc("GET "+base+"/login", h.withAdmin(h.adminTraeLoginPoll(ch)))
		h.mux.HandleFunc("POST "+base+"/login/cancel", h.withAdmin(h.adminTraeLoginCancel(ch)))
		h.mux.HandleFunc("POST "+base+"/accounts/import", h.withAdmin(h.adminTraeImport(ch)))
		h.mux.HandleFunc("POST "+base+"/accounts/delete", h.withAdmin(h.adminTraeAccountsDelete(ch)))
		h.mux.HandleFunc("POST "+base+"/accounts/patch", h.withAdmin(h.adminTraeAccountsPatch(ch)))
		h.mux.HandleFunc("POST "+base+"/accounts/refresh", h.withAdmin(h.adminTraeAccountsRefresh(ch)))
		h.mux.HandleFunc("GET "+base+"/accounts/json", h.withAdmin(h.adminTraeAccountJSON(ch)))
		h.mux.HandleFunc("GET "+base+"/schedule", h.withAdmin(h.adminTraeSchedule(ch)))
		h.mux.HandleFunc("POST "+base+"/schedule/checkin", h.withAdmin(h.adminTraeScheduleCheckin(ch)))
		h.mux.HandleFunc("POST "+base+"/schedule/refresh", h.withAdmin(h.adminTraeScheduleRefresh(ch)))
		h.mux.HandleFunc("GET "+base+"/credits", h.withAdmin(h.adminTraeCredits(ch)))
		h.mux.HandleFunc("GET "+base+"/usage", h.withAdmin(h.adminTraeUsage(ch)))
		// TRAE OAuth 浏览器 302 的公开落点：TRAE 域跳转不带 console Cookie，
		// 故无 withAdmin（与 sidecar /authorize 语义一致），只转发回调 query。
		h.mux.HandleFunc("GET "+base+"/authorize", h.adminTraeAuthorize)
	}
	// TRAE 授权页对 auth_callback_url 的形状有硬约束：主机必须是字面量 127.0.0.1、
	// 路径必须是 /authorize（端口任意），否则 SSR 返回空数据并显示登录失败。所以
	// 实际回调落在 console 根路径上，与各区域的 /admin/<region>/authorize 共用同一处理器；
	// 两条区域共用这一个根路径，由处理器按"谁发起了登录"分发。
	h.mux.HandleFunc("GET /authorize", h.adminTraeAuthorize)
	// Raccoon 页签：状态/测试对话 + 凭证导入与账号控制链路（Docker 下无网页登录，只能粘贴凭证）。
	h.mux.HandleFunc("GET /admin/raccoon", h.withAdmin(h.adminRaccoonStatus))
	h.mux.HandleFunc("POST /admin/raccoon/chat", h.withAdmin(h.adminRaccoonChat))
	h.mux.HandleFunc("POST /admin/raccoon/accounts/import", h.withAdmin(h.adminRaccoonImport))
	h.mux.HandleFunc("POST /admin/raccoon/accounts/delete", h.withAdmin(h.adminRaccoonAccountsDelete))
	h.mux.HandleFunc("POST /admin/raccoon/accounts/patch", h.withAdmin(h.adminRaccoonAccountsPatch))
	h.mux.HandleFunc("POST /admin/raccoon/accounts/refresh", h.withAdmin(h.adminRaccoonAccountsRefresh))
	h.mux.HandleFunc("GET /admin/raccoon/credits", h.withAdmin(h.adminRaccoonCredits))
	h.mux.HandleFunc("GET /admin/raccoon/usage", h.withAdmin(h.adminRaccoonUsage))
	h.mux.HandleFunc("GET /admin/ccr", h.withAdmin(h.adminCCRStatus))
	h.mux.HandleFunc("POST /admin/ccr/gateway", h.withAdmin(h.adminCCRGateway))
	h.mux.HandleFunc("GET /admin/ccr/link", h.withAdmin(h.adminCCRLink))
	// 路由别名：GET 读当前配置与各通道模型目录，POST 落盘并即时生效。
	h.mux.HandleFunc("GET /admin/route", h.withAdmin(h.writeRouteState))
	h.mux.HandleFunc("POST /admin/route/save", h.withAdmin(h.adminRouteSave))
	// 附加 API Key（多 Key 归因）：GET 读清单，POST 全量保存（新增/启停/删除整表提交）。
	h.mux.HandleFunc("GET /admin/apikeys", h.withAdmin(h.adminAPIKeysState))
	h.mux.HandleFunc("POST /admin/apikeys", h.withAdmin(h.adminAPIKeysSave))
	// 整组启停：模型列表与别名表共用同一个写入口，保证两边状态一致。
	h.mux.HandleFunc("POST /admin/route/switch", h.withAdmin(h.adminRouteSwitch))
	// 单别名测试：发一条最小对话，把可用性与速度/失败原因直接带回页面。
	h.mux.HandleFunc("POST /admin/route/test", h.withAdmin(h.adminRouteTest))
	// 模型积分开关：GET 读人工覆盖表，POST 切换单个模型并即时生效。
	h.mux.HandleFunc("GET /admin/model-switch", h.withAdmin(h.adminModelSwitchState))
	h.mux.HandleFunc("POST /admin/model-switch", h.withAdmin(h.adminModelSwitch))
	// 全球热度：GET 读 OpenRouter 公开目录的名次表（服务端 24h 缓存）。
	h.mux.HandleFunc("GET /admin/heat", h.withAdmin(h.adminHeatRanks))
	// 模型能力 TOP：GET 读同一份目录快照派生的能力榜单（只读，不写任何配置）。
	h.mux.HandleFunc("GET /admin/capabilities", h.withAdmin(h.adminCapabilities))
	// 手工测速探测：POST 触发一轮探测，GET 轮询进度与结论（仅内存，重启清空）。
	h.mux.HandleFunc("POST /admin/probe", h.withAdmin(h.adminProbeStart))
	h.mux.HandleFunc("GET /admin/probe", h.withAdmin(h.adminProbeState))
	// 常态健康检查：GET 即时跑一轮「网关 + 七通道」总览并附后台探活历史（只读）。
	h.mux.HandleFunc("GET /admin/health", h.withAdmin(h.adminHealth))
	h.mux.HandleFunc("GET /admin/models", h.withAdmin(h.adminModels))
	// 错误统计：GET 聚合窗口内的成功/失败样本（健康检查 M1 · F1），供健康页
	// 「错误统计」区块消费；数据源是内存滚动窗口，不读失败账本（避免错误率虚高）。
	h.mux.HandleFunc("GET /admin/errors", h.withAdmin(h.adminErrors))
	// P1 告警：GET 最近告警事件（新在前），供 Hermes 定时轮询与页面留痕（健康检查
	// M1 · F4）；检测与落盘在错误落账的异步链路上，通知侧不在 console 代码范围。
	h.mux.HandleFunc("GET /admin/alerts", h.withAdmin(h.adminAlerts))
	h.mux.HandleFunc("POST /admin/access", h.withAdmin(func(w http.ResponseWriter, r *http.Request) {
		if _, err := h.coreInfo(r.Context()); err != nil {
			adminError(w, 503, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"api_key": cfg.APIKey})
	}))
	for _, route := range []struct{ pattern, method, path string }{
		{"GET /admin/status", "GET", "/internal/v1/status"},
		{"POST /admin/chat", "POST", "/internal/v1/chat"},
		{"POST /admin/messages", "POST", "/internal/v1/messages"},
		{"POST /admin/oauth", "POST", "/internal/v1/oauth"},
		{"POST /admin/oauth/{id}/poll", "GET", "/internal/v1/oauth/{id}"},
		{"POST /admin/oauth/{id}/region", "POST", "/internal/v1/oauth/{id}/region"},
		{"GET /admin/tasks", "GET", "/internal/v1/tasks"},
		{"POST /admin/tasks/{id}/runs", "POST", "/internal/v1/tasks/{id}/runs"},
		{"GET /admin/task-runs", "GET", "/internal/v1/task-runs"},
		{"GET /admin/task-runs/{id}", "GET", "/internal/v1/task-runs/{id}"},
		{"GET /admin/usage", "GET", "/internal/v1/usage"},
		{"GET /admin/usage-daily", "GET", "/internal/v1/usage/daily"},
		{"GET /admin/credit-packages", "GET", "/internal/v1/credit-packages"},
		{"GET /admin/pin", "GET", "/internal/v1/pin"},
		{"POST /admin/pin", "POST", "/internal/v1/pin"},
		{"POST /admin/unpin", "POST", "/internal/v1/unpin"},
	} {
		h.mux.HandleFunc(route.pattern, h.withAdmin(h.management(route.method, route.path)))
	}
	if cfg.ZCodeControlURL != nil {
		go h.zcodeAutoStart()
	}
	if cfg.HealthProbe {
		go h.healthAutoStart()
	}
	return h, nil
}

func validOriginURL(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Hostname() != "" && u.User == nil && u.Opaque == "" && (u.Path == "" || u.Path == "/") && u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawFragment == ""
}
func ValidateAdminOrigin(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && validOriginURL(u)
}

func (h *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	// HSTS only when this deployment is actually reachable over HTTPS; declaring it on a
	// plaintext origin would pin browsers against a scheme the deployment does not serve.
	if h.httpsEnabled {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	// Reject ambiguous encodings before ServeMux can clean or redirect them.
	if r.URL.EscapedPath() != r.URL.Path || strings.ContainsAny(r.URL.Path, "%\\") || strings.Contains(r.URL.Path, "//") {
		adminError(w, 400, "请求路径无效")
		return
	}
	for _, part := range strings.Split(r.URL.Path, "/") {
		if part == "." || part == ".." {
			adminError(w, 400, "请求路径无效")
			return
		}
	}
	h.mux.ServeHTTP(w, r)
}
func randomSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("system random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func equalSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func sessionID(r *http.Request) string {
	c, err := r.Cookie("wb2a_admin")
	if err != nil {
		return ""
	}
	return c.Value
}
func sessionFrom(r *http.Request) adminSession {
	return r.Context().Value(sessionContextKey{}).(adminSession)
}
func writeJSON(w http.ResponseWriter, code int, out any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(out)
}
func adminError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
func decodeAdmin(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		adminError(w, 400, "请求内容无效")
		return false
	}
	return true
}
func (h *server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if h.cfg.PublicOrigin != "" {
		return origin == h.cfg.PublicOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}
func (h *server) cleanupLocked() {
	now := time.Now()
	for id, s := range h.sessions {
		if now.After(s.expires) {
			s.cancel()
			delete(h.sessions, id)
		}
	}
	for ip, l := range h.limits {
		if now.Sub(l.since) > time.Minute {
			delete(h.limits, ip)
		}
	}
}
func (h *server) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.cleanupLocked()
		s, ok := h.sessions[sessionID(r)]
		var session adminSession
		if ok {
			session = *s
		}
		h.mu.Unlock()
		if !ok {
			adminError(w, 401, "管理会话已过期，请重新登录")
			return
		}
		if r.Method != "GET" && (!h.sameOrigin(r) || !equalSecret(r.Header.Get("X-CSRF-Token"), session.csrf)) {
			adminError(w, 403, "请求来源或验证信息无效")
			return
		}
		if r.URL.Path != "/admin/logout" {
			ctx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(session.ctx, cancel)
			defer stop()
			defer cancel()
			r = r.WithContext(ctx)
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	}
}

// clientAddress resolves the true peer of a request: the direct connection, or
// the rightmost X-Forwarded-For entry when that connection came from a trusted
// proxy. It backs the login rate-limit bucket and the source attribution that
// console attaches to forwarded public calls.
func (h *server) clientAddress(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	peer := net.ParseIP(ip)
	if peer == nil || len(h.trusted) == 0 {
		return ip
	}
	trusted := false
	for _, network := range h.trusted {
		if network.Contains(peer) {
			trusted = true
			break
		}
	}
	// A single header is the only shape a reversing proxy appends to; repeated headers leave the last hop ambiguous.
	forwarded := r.Header.Values("X-Forwarded-For")
	if !trusted || len(forwarded) != 1 {
		return ip
	}
	entry := forwarded[0]
	if comma := strings.LastIndexByte(entry, ','); comma >= 0 {
		entry = entry[comma+1:]
	}
	client := net.ParseIP(strings.TrimSpace(entry))
	if client == nil {
		return ip
	}
	return client.String()
}
func (h *server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if !h.sameOrigin(r) {
		adminError(w, 403, "请求来源无效")
		return
	}
	ip := h.clientAddress(r)
	h.mu.Lock()
	h.cleanupLocked()
	lim := h.limits[ip]
	if lim.count >= 10 || len(h.limits) >= 1024 || len(h.sessions) >= 64 {
		h.mu.Unlock()
		adminError(w, 429, "尝试过于频繁，请一分钟后再试")
		return
	}
	if lim.since.IsZero() {
		lim.since = time.Now()
	}
	lim.count++
	h.limits[ip] = lim
	h.mu.Unlock()
	var body struct {
		Key string `json:"key"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if !equalSecret(body.Key, h.cfg.AdminKey) {
		adminError(w, 401, "管理密钥不正确")
		return
	}
	id, csrf, owner := randomSecret(), randomSecret(), randomSecret()
	h.mu.Lock()
	if len(h.sessions) >= 64 {
		h.mu.Unlock()
		adminError(w, 429, "管理会话过多，请稍后重试")
		return
	}
	expires := time.Now().Add(8 * time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	h.sessions[id] = &adminSession{csrf: csrf, owner: owner, expires: expires, ctx: ctx, cancel: cancel, oauthGate: make(chan struct{}, 1)}
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wb2a_admin", Value: id, Path: "/admin", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(h.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	// Authentication remains available during a core outage; actions still fail closed.
	info, _ := h.coreInfo(r.Context())
	writeJSON(w, 200, map[string]any{"csrf": csrf, "global_enabled": info.GlobalEnabled, "zcode_enabled": h.cfg.ZCodeURL != nil, "zcode_control": h.cfg.ZCodeControlURL != nil, "qoder_enabled": h.cfg.QoderURL != nil, "qoder_control": h.cfg.QoderControlURL != nil, "opencode_enabled": h.cfg.OpenCodeURL != nil, "ccr_enabled": h.cfg.CCRURL != nil, "autoclaw_enabled": h.cfg.AutoClawURL != nil, "trae_enabled": h.cfg.TraeURL != nil, "traeai_enabled": h.cfg.TraeAIURL != nil, "raccoon_enabled": h.cfg.RaccoonURL != nil, "freellm_enabled": h.cfg.FreellmURL != nil})
}
func (h *server) adminLogout(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	delete(h.sessions, sessionID(r))
	sessionFrom(r).cancel()
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wb2a_admin", Value: "", Path: "/admin", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(h.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	// Invalidate immediately, then order cleanup after any admitted OAuth control.
	// Cleanup must survive a browser disconnect, but cannot wait indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	gate := sessionFrom(r).oauthGate
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		adminError(w, 503, "授权流程取消超时，管理会话已退出")
		return
	}
	if _, err := h.coreInfo(ctx); err != nil {
		adminError(w, 503, err.Error())
		return
	}
	response, err := h.bridgeRequest(ctx, "DELETE", "/internal/v1/owners/"+sessionFrom(r).owner+"/flows", sessionFrom(r).owner)
	if err != nil {
		adminError(w, 503, "核心服务不可达，管理会话已退出")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		adminError(w, 503, "核心授权流程取消失败，管理会话已退出")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// modelHealthRows 只读 24h 调用窗口与 /admin/health 共用的逐模型探测缓存。
// 探测结论不混入调用分母；无 bool 结论不等于失败，通道历史也不能冒充模型结论。
func (h *server) modelHealthRows() []map[string]any {
	stats := map[routeChannel]map[string]windowStats{}
	if h.outcomes != nil {
		stats = h.outcomes.collect(24 * time.Hour).models
	}
	probes := map[routeChannel]map[string]map[string]any{}
	for _, channel := range routeChannelOrder {
		if h.probe != nil {
			probes[channel] = map[string]map[string]any{}
			for public, result := range h.probe.channelResults(channel) {
				probes[channel][nativeModelName(channel, public)] = result
			}
		}
		for model := range probes[channel] {
			if stats[channel] == nil {
				stats[channel] = map[string]windowStats{}
			}
			if _, exists := stats[channel][model]; !exists {
				stats[channel][model] = windowStats{}
			}
		}
	}
	rows := []map[string]any{}
	for channel, models := range stats {
		for model, stat := range models {
			state := "unknown"
			var rate any
			samples := stat.ok + stat.fail
			if samples > 0 {
				rate = roundRate(stat.rate())
				switch {
				case stat.rate() < 0.2:
					state = "ok"
				case stat.rate() <= 0.8:
					state = "degraded"
				default:
					state = "fail"
				}
			}
			if ok, known := probes[channel][model]["ok"].(bool); known {
				if !ok {
					state = "fail"
				} else if samples == 0 {
					state = "ok"
				}
			}
			rows = append(rows, map[string]any{"channel": string(channel), "model": model, "health": state, "health_samples": samples, "health_error_rate": rate})
		}
	}
	return rows
}

// adminModels 保留既有管理代理的鉴权/owner 与目录字段，只补内存健康快照。
func (h *server) adminModels(w http.ResponseWriter, r *http.Request) {
	if _, err := h.coreInfo(r.Context()); err != nil {
		adminError(w, 503, err.Error())
		return
	}
	if !h.sessionActive(r) {
		adminError(w, 401, "管理会话已过期，请重新登录")
		return
	}
	proxy := h.proxy(true)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		var payload map[string]any
		if json.Unmarshal(raw, &payload) == nil {
			if models, ok := payload["data"].([]any); ok {
				healthRows := h.modelHealthRows()
				byModel := map[string]map[string]any{}
				for _, row := range healthRows {
					if row["channel"] == string(channelCore) {
						byModel[row["model"].(string)] = row
					}
				}
				for _, item := range models {
					row, ok := item.(map[string]any)
					if !ok {
						continue
					}
					id, _ := row["id"].(string)
					row["health"], row["health_samples"], row["health_error_rate"] = "unknown", 0, nil
					if health := byModel[id]; health != nil {
						for _, key := range []string{"health", "health_samples", "health_error_rate"} {
							row[key] = health[key]
						}
					}
				}
				// 旁路与别名表复用同一快照，不新增接口或持久化文件。
				payload["health_models"] = healthRows
				raw, err = json.Marshal(payload)
				if err != nil {
					return err
				}
				resp.Header.Del("ETag")
				resp.Header.Set("Content-Type", "application/json")
			}
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		resp.ContentLength = int64(len(raw))
		resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
		return nil
	}
	req := r.Clone(r.Context())
	req.URL.Path, req.URL.RawPath, req.RequestURI = "/internal/v1/models", "", ""
	req.Body, req.ContentLength, req.TransferEncoding = nil, 0, nil
	req.Header.Del("Content-Length")
	req.Header.Del("Content-Type")
	proxy.ServeHTTP(w, req)
}
