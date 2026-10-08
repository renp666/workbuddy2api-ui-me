package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
)

// ErrKind 是上游错误的分类。小浣熊没有业务错误码，只能按 HTTP 状态码判定。
type ErrKind int

const (
	ErrNone         ErrKind = iota
	ErrQuotaLimited         // 429：积分 / 配额限流
	ErrTokenExpired         // 401：登录态过期，可刷新后重试
	ErrNotFound             // 404：模型或端点不存在
	ErrServer               // 5xx
	ErrClient               // 其余 4xx
)

func (k ErrKind) String() string {
	switch k {
	case ErrQuotaLimited:
		return "quota_limited"
	case ErrTokenExpired:
		return "token_expired"
	case ErrNotFound:
		return "not_found"
	case ErrServer:
		return "server"
	case ErrClient:
		return "client"
	default:
		return "none"
	}
}

// Error 是带分类的上游错误。
type Error struct {
	Kind   ErrKind
	Status int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream %s (http %d): %s", e.Kind, e.Status, e.Msg)
}

// Classify 按状态码分类上游错误；body 只用于将来的业务码兼容，当前不参与判定。
func Classify(status int, _ []byte) ErrKind {
	switch {
	case status == http.StatusUnauthorized:
		return ErrTokenExpired
	case status == http.StatusTooManyRequests:
		return ErrQuotaLimited
	case status == http.StatusNotFound:
		return ErrNotFound
	case status >= 500:
		return ErrServer
	case status >= 400:
		return ErrClient
	default:
		return ErrNone
	}
}

// IsTokenExpiredErr 报告错误是否为登录态过期。
func IsTokenExpiredErr(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == ErrTokenExpired
}

// IsQuotaErr 报告错误是否为配额限流。
func IsQuotaErr(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == ErrQuotaLimited
}

// ModelInfo 是模型目录条目（已完成上游键名映射）。
type ModelInfo struct {
	ID               string `json:"id"`
	Object           string `json:"object"`
	Name             string `json:"name"`
	ContextWindow    int    `json:"context_window,omitempty"`
	MaxTokens        int    `json:"max_tokens,omitempty"`
	SupportsImages   bool   `json:"supports_images,omitempty"`
	SupportsThinking bool   `json:"supports_reasoning,omitempty"`
	OwnedBy          string `json:"owned_by"`
}

const maxJSONBody = 8 << 20

// Client 是上游客户端。HTTP 走短 RPC，StreamHTTP 无请求级总超时。
type Client struct {
	HTTP        *http.Client
	StreamHTTP  *http.Client
	CatalogHTTP *http.Client

	LLMBase    string
	AuthOrigin string
	MainSite   string

	// refreshMu 串行化刷新：同一 refresh token 被并发使用会被上游作废。
	refreshMu sync.Mutex

	catalogMu   sync.Mutex
	catalog     []ModelInfo
	catalogAt   time.Time
	catalogAuth string
}

// New 按环境变量覆盖上游地址，默认走公网小浣熊站点。
func New() *Client {
	streamTr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: StreamTimeout,
	}
	plainTr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: RequestTimeout,
	}
	catalogTr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: CatalogTimeout,
	}
	return &Client{
		HTTP:        &http.Client{Transport: plainTr, Timeout: RequestTimeout},
		StreamHTTP:  &http.Client{Transport: streamTr},
		CatalogHTTP: &http.Client{Transport: catalogTr, Timeout: CatalogTimeout},
		LLMBase:     strings.TrimRight(envOr("RACCOON_LLM_BASE_URL", DefaultLLMBaseURL), "/"),
		AuthOrigin:  strings.TrimRight(envOr("RACCOON_AUTH_ORIGIN", DefaultAuthOrigin), "/"),
		MainSite:    strings.TrimRight(envOr("RACCOON_MAIN_SITE_URL", DefaultMainSite), "/"),
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func (c *Client) doJSON(cl *http.Client, req *http.Request) ([]byte, error) {
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream unreachable: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, fmt.Errorf("upstream read: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Kind: Classify(resp.StatusCode, raw), Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	return raw, nil
}

// ---------- 鉴权刷新 ----------

// RefreshTokenIfNeeded 在 within 内临期时刷新；返回是否真的刷了。
func (c *Client) RefreshTokenIfNeeded(ctx context.Context, a *auth.Auth, within time.Duration) (bool, error) {
	if !a.NeedsRefresh(within) {
		return false, nil
	}
	if err := c.RefreshToken(ctx, a); err != nil {
		return false, err
	}
	return true, nil
}

// RefreshToken 用 refresh token 换新凭证并原子落盘。
func (c *Client) RefreshToken(ctx context.Context, a *auth.Auth) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	rt := a.RefreshTokenValue()
	if rt == "" {
		return fmt.Errorf("refresh_failed: no refreshToken — re-import credentials")
	}
	payload, err := json.Marshal(map[string]string{"refresh_token": rt})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.AuthOrigin+"/api/web/auth/v1"+EpRefresh, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	desktopHeaders(req, a.JWT(), false)
	raw, err := c.doJSON(c.HTTP, req)
	if err != nil {
		return err
	}
	var out refreshResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("refresh_failed: parse_error: %v", err)
	}
	access := firstNonEmpty(out.AccessToken, out.Token, out.AuthToken)
	if access == "" {
		return fmt.Errorf("refresh_failed: no token in response — re-login required")
	}
	// 只有确认拿到新 access token 才改字段：失败路径保留旧 refresh token 可重试。
	a.SetTokens(access, out.RefreshToken, auth.NormalizeExpiresAt(out.ExpiresAt))
	return a.SaveAtomic()
}

type refreshResp struct {
	AccessToken  string `json:"access_token"`
	Token        string `json:"token"`
	AuthToken    string `json:"auth_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    any    `json:"expires_at"`
	Code         any    `json:"code"`
	Message      string `json:"message"`
}

// ---------- LLM 数据面 ----------

// ChatStream 发起一次聊天请求。成功时返回可直接透传的响应体；
// 上游非 2xx 时 rc 为 nil、status 与 respBody 携带上游信息、err 为 nil，由调用方分类。
func (c *Client) ChatStream(ctx context.Context, a *auth.Auth, body []byte) (rc io.ReadCloser, status int, respBody []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.LLMBase+EpChat, bytes.NewReader(body))
	if err != nil {
		return nil, 0, nil, err
	}
	llmHeaders(req, a.JWT())
	resp, err := c.StreamHTTP.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
		return nil, resp.StatusCode, raw, nil
	}
	return resp.Body, resp.StatusCode, nil, nil
}

// ---------- 模型目录 ----------

// FetchModels 取远端模型目录（10 分钟 TTL，按账号缓存），失败时回落静态兜底。
func (c *Client) FetchModels(ctx context.Context, a *auth.Auth) []ModelInfo {
	token := a.JWT()

	c.catalogMu.Lock()
	if c.catalog != nil && c.catalogAuth == token && time.Since(c.catalogAt) < CatalogTTL {
		out := c.catalog
		c.catalogMu.Unlock()
		return out
	}
	c.catalogMu.Unlock()

	remote, err := c.fetchCatalog(ctx, a)
	if err != nil || len(remote) == 0 {
		return mergeModels(nil, staticModels)
	}
	merged := mergeModels(remote, staticModels)
	c.catalogMu.Lock()
	c.catalog, c.catalogAt, c.catalogAuth = merged, time.Now(), token
	c.catalogMu.Unlock()
	return merged
}

func (c *Client) fetchCatalog(ctx context.Context, a *auth.Auth) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.LLMBase+EpCatalog, nil)
	if err != nil {
		return nil, err
	}
	llmHeaders(req, a.JWT())
	raw, err := c.doJSON(c.CatalogHTTP, req)
	if err != nil {
		return nil, err
	}
	return parseCatalog(raw), nil
}

type catalogModel struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	ContextWindow   int    `json:"contextWindow"`
	MaxTokens       int    `json:"maxTokens"`
	SupportImage    bool   `json:"supportImage"`
	SupportThinking bool   `json:"supportThinking"`
}

type catalogCategory struct {
	Type         string         `json:"type"`
	DefaultModel string         `json:"default_model"`
	Models       []catalogModel `json:"models"`
}

type catalogData struct {
	Categories []catalogCategory `json:"categories"`
}

type catalogEnvelope struct {
	Data *catalogData `json:"data"`
}

// parseCatalog 展平所有 category；同名模型首次出现者胜；空 name 丢弃。
// 根上没有 data 时把根当作 data（兼容直接返回 categories 的形态）。
func parseCatalog(raw []byte) []ModelInfo {
	var env catalogEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	var data catalogData
	if env.Data != nil {
		data = *env.Data
	} else if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	out := make([]ModelInfo, 0)
	seen := make(map[string]bool)
	for _, cat := range data.Categories {
		for _, m := range cat.Models {
			id := strings.TrimSpace(m.Name)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, ModelInfo{
				ID:               id,
				Object:           "model",
				Name:             firstNonEmpty(strings.TrimSpace(m.DisplayName), id),
				ContextWindow:    m.ContextWindow,
				MaxTokens:        m.MaxTokens,
				SupportsImages:   m.SupportImage,
				SupportsThinking: m.SupportThinking,
				OwnedBy:          "xiaohuanxiong",
			})
		}
	}
	return out
}

func mergeModels(remote, fallback []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(remote)+len(fallback))
	seen := make(map[string]bool)
	for _, m := range append(append([]ModelInfo{}, remote...), fallback...) {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Object == "" {
			m.Object = "model"
		}
		out = append(out, m)
	}
	return out
}

// ---------- 余额与订阅 ----------

// Balance 是积分余额快照。
type Balance struct {
	Available int64 `json:"available"`
	Daily     int64 `json:"daily"`
	Monthly   int64 `json:"monthly"`
	Reward    int64 `json:"reward"`
	TopUp     int64 `json:"topup"`
}

// Balance 查询可用积分与四个钱包分项。
func (c *Client) Balance(ctx context.Context, a *auth.Auth) (Balance, error) {
	raw, err := c.getEnvelope(ctx, c.MainSite+EpBalance, a, false)
	if err != nil {
		return Balance{}, err
	}
	var data struct {
		AvailablePoints int64 `json:"available_points"`
		DailyPoints     int64 `json:"daily_points"`
		MonthlyPoints   int64 `json:"monthly_points"`
		RewardPoints    int64 `json:"reward_points"`
		TopupPoints     int64 `json:"topup_points"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return Balance{}, fmt.Errorf("balance parse_error: %v", err)
	}
	return Balance{
		Available: data.AvailablePoints,
		Daily:     data.DailyPoints,
		Monthly:   data.MonthlyPoints,
		Reward:    data.RewardPoints,
		TopUp:     data.TopupPoints,
	}, nil
}

// Entitlement 是订阅权益。office 与 code 两处形状统一，office 优先。
type Entitlement struct {
	ProEnable   bool   `json:"pro_enable"`
	ActivePlan  string `json:"active_plan"`
	ExpiredTime string `json:"pro_expired_time"`
	Status      string `json:"status"`
}

type entPlan struct {
	ProEnable   bool   `json:"pro_enable"`
	ActivePlan  string `json:"active_plan"`
	ExpiredTime string `json:"pro_expired_time"`
}

// Entitlement 查询订阅状态。
func (c *Client) Entitlement(ctx context.Context, a *auth.Auth) (Entitlement, error) {
	raw, err := c.getEnvelope(ctx, c.AuthOrigin+EpEntitlement, a, false)
	if err != nil {
		return Entitlement{}, err
	}
	var data struct {
		Office *entPlan `json:"office"`
		Code   *entPlan `json:"code"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return Entitlement{}, fmt.Errorf("entitlement parse_error: %v", err)
	}
	pick := data.Office
	if pick == nil {
		pick = data.Code
	}
	if pick == nil {
		return Entitlement{Status: "none"}, nil
	}
	out := Entitlement{ProEnable: pick.ProEnable, ActivePlan: pick.ActivePlan, ExpiredTime: pick.ExpiredTime}
	out.Status = "none"
	if out.ProEnable {
		out.Status = "active"
	}
	return out, nil
}

// ---------- 每日积分 ----------

// DesktopGrant 领取一次性桌面登录奖励；已发放过时 granted=false（幂等）。
func (c *Client) DesktopGrant(ctx context.Context, a *auth.Auth) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.MainSite+EpDesktopGrant, strings.NewReader("{}"))
	if err != nil {
		return false, err
	}
	desktopHeaders(req, a.JWT(), false)
	raw, err := c.doJSON(c.HTTP, req)
	if err != nil {
		return false, err
	}
	var out struct {
		Granted *bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, nil
	}
	return out.Granted != nil && *out.Granted, nil
}

// SettingInfo 触发每日积分发放（按天幂等），返回上游的发放提示。
func (c *Client) SettingInfo(ctx context.Context, a *auth.Auth) (toast string, popups int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.MainSite+EpSettingInfo, nil)
	if err != nil {
		return "", 0, err
	}
	desktopHeaders(req, a.JWT(), true)
	raw, err := c.doJSON(c.HTTP, req)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		Popups []json.RawMessage `json:"point_grant_popups"`
		Toast  string            `json:"point_grant_toast"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", 0, fmt.Errorf("setting_info parse_error: %v", err)
	}
	return out.Toast, len(out.Popups), nil
}

// BillEntry 是积分账单条目。字段用 RawMessage 以容忍上游的时间与金额形态差异。
type BillEntry struct {
	CreatedAt   json.RawMessage `json:"created_at"`
	CreateTime  json.RawMessage `json:"create_time"`
	Time        json.RawMessage `json:"time"`
	ChangePoint json.RawMessage `json:"change_points"`
	Points      json.RawMessage `json:"points"`
	Amount      json.RawMessage `json:"amount"`
	Source      string          `json:"source"`
	Description string          `json:"description"`
}

// Bills 拉取积分账单，用于核对今日是否真的有积分入账。
func (c *Client) Bills(ctx context.Context, a *auth.Auth, limit, offset int) ([]BillEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	url := fmt.Sprintf("%s%s?paging.limit=%d&paging.offset=%d", c.MainSite, EpBills, limit, offset)
	raw, err := c.getEnvelope(ctx, url, a, false)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, key := range []string{"list", "records", "items", "bill_list"} {
			v, ok := obj[key]
			if !ok {
				continue
			}
			var out []BillEntry
			if err := json.Unmarshal(v, &out); err != nil {
				return nil, fmt.Errorf("bills %s parse_error: %v", key, err)
			}
			return out, nil
		}
	}
	var direct []BillEntry
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}
	return nil, fmt.Errorf("bills: unrecognized response shape")
}

func (b BillEntry) at() (time.Time, bool) {
	for _, raw := range []json.RawMessage{b.CreatedAt, b.CreateTime, b.Time} {
		if t, ok := asTime(raw); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func (b BillEntry) delta() int64 {
	for _, raw := range []json.RawMessage{b.ChangePoint, b.Points, b.Amount} {
		if n, ok := asInt(raw); ok {
			return n
		}
	}
	return 0
}

// GrantedToday 汇总当日（东八区日历日）的正向积分入账。
func GrantedToday(bills []BillEntry, now time.Time) int64 {
	today := now.In(cstZone).Format("2006-01-02")
	var total int64
	for _, b := range bills {
		t, ok := b.at()
		if !ok || t.In(cstZone).Format("2006-01-02") != today {
			continue
		}
		if n := b.delta(); n > 0 {
			total += n
		}
	}
	return total
}

// ---------- 信封与解析工具 ----------

type envelope struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

// unwrapEnvelope 校验业务码并取出 data（缺失时回落到根对象）。
func unwrapEnvelope(raw []byte) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse_error: %v", err)
	}
	if code := envelopeCode(env.Code); code != 0 {
		msg := firstNonEmpty(env.Message, env.Msg)
		if msg == "" {
			msg = fmt.Sprintf("code=%d", code)
		}
		return nil, fmt.Errorf("upstream rejected: %s", msg)
	}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		return env.Data, nil
	}
	return raw, nil
}

func envelopeCode(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	if n, ok := asInt(raw); ok {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "0" || s == "" {
			return 0
		}
		return -1
	}
	return 0
}

func (c *Client) getEnvelope(ctx context.Context, url string, a *auth.Auth, desktop bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if desktop {
		desktopHeaders(req, a.JWT(), false)
	} else {
		llmHeaders(req, a.JWT())
	}
	raw, err := c.doJSON(c.HTTP, req)
	if err != nil {
		return nil, err
	}
	return unwrapEnvelope(raw)
}

// cstZone 供账单日期判定与 naive 时间解析使用：上游是中文服务，无时区的时间按东八区理解。
var cstZone = time.FixedZone("Asia/Shanghai", 8*3600)

func asTime(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	// 只有裸数字（JSON number）才按时间戳处理；字符串里的前缀数字不能信，
	// 否则 "2026-10-08 …" 会被 Sscanf 截成 2026 当成 1970 年。
	if c := raw[0]; (c >= '0' && c <= '9') || c == '-' {
		if n, ok := asInt(raw); ok && n > 0 {
			if n > 1e12 {
				n /= 1000
			}
			return time.Unix(n, 0), true
		}
		return time.Time{}, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, cstZone); err == nil {
				return t, true
			}
		}
		// 纯数字字符串按时间戳：整串必须都是数字，避免日期串被截断。
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			if n > 1e12 {
				n /= 1000
			}
			return time.Unix(n, 0), true
		}
	}
	return time.Time{}, false
}

func asInt(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return int64(f), true
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var i int64
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
