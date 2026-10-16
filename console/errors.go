package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 健康检查模块 M1 · 批 A（PRD：docs/superpowers/specs/2026-10-16-健康检查模块PRD-v1.0.md §2-F1/F2）：
//
//   - F1 错误事件账本：公共出口（/v1/chat/completions、/v1/messages、/v1/responses）
//     的失败分支异步落账到 errors-<YYYYMMDD>.jsonl（append-only、按天轮转、保留 30 天），
//     并提供 GET /admin/errors 聚合查询。
//   - F2 错误归类统一口径：与 probe 共用一份分类函数（probe.go 的 failureCategory）。
//
// 健康检查模块 M1 · 批 D（PRD §2-F4/F5）：
//
//   - F4 P1 告警事件：在错误落账的同一条异步链路上检测 P1 规则（别名窗口错误率 >50%
//     且样本 ≥5、任一 pin 类错误），命中后 append alerts.jsonl（单文件、保留 30 天）
//     并提供 GET /admin/alerts。通知侧由 Hermes 轮询，console 不引入任何外部通知依赖。
//   - F5 健康页错误统计区块消费 F1 的 /admin/errors（见 web/app.js）。
//
// 纪律（红线 1/3/4）：落账与告警检测都在独立协程里跑，绝不阻塞调用主链路，失败保持
// 沉默——统计是尽力而为，不是调用契约；error_snippet 落盘前脱敏（不含 key/token/账号）；
// 只用标准库 + JSONL，不建数据库、不新增第三方依赖（go.mod 不动）。
//
// 采集点在 proxy.go 的 observePublicExits：它包装 /v1/* 出口，记录响应状态与响应体
// 摘要，请求结束后交给这里的 finishExitObserve 归档。

const (
	// errorLedgerRetention 是 JSONL 账本的保留时长（PRD：保留 30 天）。
	errorLedgerRetention = 30 * 24 * time.Hour
	// errorSnippetLimit 是 error_snippet 的最大字数（rune），与 readProbeError 的
	// 200 字口径一致，但按 rune 截断，避免把多字节字符截成半个。
	errorSnippetLimit = 200
	// errorSnippetScanLimit 是失败响应参与摘要提取的缓冲上限。错误体通常远小于它；
	// 超过只说明上游把整页 HTML 塞进了错误响应，截断即可，不必整份缓存。
	errorSnippetScanLimit = 16 << 10
	// errorRecentLimit 是 /admin/errors 里「最近错误事件流水」的条数。
	errorRecentLimit = 20
	// errorRecentKeep 是内存里留存的最近失败事件数（流水取前 20，多留余量）。
	errorRecentKeep = 50
	// errorWindowMaxSamples 是滚动窗口的样本上限：超过丢最旧，保证内存有界。
	errorWindowMaxSamples = 20000
	// alertRetention 是 alerts.jsonl 的保留时长（PRD §2-F4：保留 30 天）。单文件追加，
	// 过期靠「启动/跨天时按 ts 重写」实现——事件量小（P1 命中才有），重写代价可忽略。
	alertRetention = 30 * 24 * time.Hour
	// alertSuppressWindow 是同 (channel, category) 的抑制窗口（PRD §2-F4：30min 只出一条）。
	alertSuppressWindow = 30 * time.Minute
	// alertRecentKeep 是 /admin/alerts 内存留存的最近告警条数（新在前）。
	alertRecentKeep = 200
	// alertLevelP1 是 M1 唯一的告警级别：规则命中的都是「必须人看」的事。
	alertLevelP1 = "P1"
	// alertRuleAliasErrorRate / alertRulePinError 是两条 P1 规则的稳定标识。
	alertRuleAliasErrorRate = "alias_error_rate"
	alertRulePinError       = "pin_error"
	// alertAliasWindow 是别名错误率规则看的滚动窗口（PRD §2-F4：15min）。
	alertAliasWindow = 15 * time.Minute
	// alertAliasMinSamples 是别名错误率规则的最小调用样本（PRD §2-F4：≥5）。
	alertAliasMinSamples = 5
	// alertAliasRateThreshold 是别名错误率阈值，严格大于才告警。
	alertAliasRateThreshold = 0.5
)

// errorWindowFor 把查询参数换成窗口时长。未知值一律拒绝，不让前端拼错参数时静默
// 退化成另一个窗口（数字对不上比报错更难排查）。
func errorWindowFor(key string) (time.Duration, bool) {
	switch key {
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	}
	return 0, false
}

// consoleDataDir 返回 console 可写数据目录（容器内 /app/console-data，与 routes.json
// 同一 bind mount，重启不丢）。路由配置文件未配置时返回空串，表示没有可写目录——
// 此时错误账本停用，出口行为与没有本功能时完全一致（不新建任何文件）。
func consoleDataDir(routeFile string) string {
	if strings.TrimSpace(routeFile) == "" {
		return ""
	}
	return filepath.Dir(routeFile)
}

// errorEvent 是错误账本的单行记录，字段与 PRD §2-F1 的 JSONL 单行一一对应。
// 只记失败调用；成功调用的观测（错误率的分母）进内存滚动窗口（outcomeWindow），
// 不写这份账本——账本是排障与审计用的错误真源，不是全量调用流水。
type errorEvent struct {
	Ts             int64  `json:"ts"`
	Alias          string `json:"alias,omitempty"`
	ModelResolved  string `json:"model_resolved,omitempty"`
	Channel        string `json:"channel"`
	API            string `json:"api,omitempty"`
	Mode           string `json:"mode,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	Category       string `json:"category"`
	ErrorSnippet   string `json:"error_snippet,omitempty"`
	DurationMs     int64  `json:"duration_ms"`
	Client         string `json:"client,omitempty"`
	RoutedVia      string `json:"routed_via,omitempty"`
	RetryCount     int    `json:"retry_count,omitempty"`
}

// usageChannelToRoute 把用量账本的通道身份翻译成路由通道名。两套名字只在 GLM 通道上
// 不同（账本叫 zcode、路由叫 glm），core 在账本侧叫 workbuddy。与 channel_consistency_test
// 里的 alias 表保持同一份口径。
var usageChannelToRoute = map[string]routeChannel{
	"workbuddy": channelCore,
	"zcode":     channelGLM,
}

// routeChannelOfUID 把用量账本的通道身份换算成路由通道名；未知身份原样保留，
// 让「谁在报错」永远如实呈现，不被映射吞掉。
func routeChannelOfUID(uid string) routeChannel {
	if c, ok := usageChannelToRoute[uid]; ok {
		return c
	}
	return routeChannel(uid)
}

// publicChannelForModel 按公共模型名前缀判通道，口径与 publicRouter 的分流前缀一致
// （前缀常量在各通道文件里单点定义，这里只引用）。别名命中时通道取自
// X-Prism-Channel 响应头，不经过这里。
func publicChannelForModel(model string) routeChannel {
	switch {
	case strings.HasPrefix(model, zcodeModelPrefix):
		return channelGLM
	case strings.HasPrefix(model, qoderModelPrefix):
		return channelQoder
	case strings.HasPrefix(model, opencodeModelPrefix):
		return channelOpenCode
	case strings.HasPrefix(model, autoclawModelPrefix):
		return channelAutoClaw
	case strings.HasPrefix(model, traeAIModelPrefix):
		return channelTraeAI
	case strings.HasPrefix(model, traeModelPrefix):
		return channelTrae
	case strings.HasPrefix(model, raccoonModelPrefix):
		return channelRaccoon
	case strings.HasPrefix(model, freellmModelPrefix):
		return channelFreellm
	}
	return channelCore
}

// routedViaFor 给出「这次调用经由谁转发」的短标识，与部署里的服务名对齐
// （qoder → qoder-proxy），core 就是 core。
func routedViaFor(channel routeChannel) string {
	if channel == channelCore {
		return "core"
	}
	return string(channel) + "-proxy"
}

// nativeModelName 剥掉公共前缀得到通道原生模型名。前缀规则只有 routeChannel.prefix()
// 一处实现（glm 的 glm- 是上游自下发的前缀，prefix() 为空，保持原样），与
// routeTarget.nativeModel 同源。
func nativeModelName(channel routeChannel, public string) string {
	if prefix := channel.prefix(); prefix != "" {
		return strings.TrimPrefix(public, prefix)
	}
	return public
}

// —— 脱敏（红线 3）：error_snippet 不含 key/token/账号 ————————————————
//
// 上游错误体可能原样回显请求里的凭据（鉴权失败时尤其常见）。这里在落盘前把常见凭据
// 形态替换成 ***：鉴权头、Bearer 串、sk- 开头的 key、JSON/查询串里的凭据字段值、
// 邮箱形态的账号。命中不了的不硬猜——脱敏是下限保障，不是语义解析。
var snippetRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization|proxy-authorization|x-api-key|cookie)\s*[:=]\s*(?:bearer\s+)?\S+`), `$1: ***`},
	{regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{6,}`), `Bearer ***`},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{6,}`), `sk-***`},
	{regexp.MustCompile(`(?i)("?(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|pwd|session[_-]?id|cookie)"?\s*[:=]\s*)"[^"]{4,}"`), `$1"***"`},
	{regexp.MustCompile(`(?i)((?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|pwd)\s*[:=]\s*)[^\s,;}&"]{4,}`), `${1}***`},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), `***@***`},
}

// sanitizeSnippet 脱敏并截断错误摘要。先替换凭据再按 rune 截断，保证落盘的每条摘要
// 既不含凭据也不超过 200 字。
func sanitizeSnippet(raw string) string {
	text := strings.TrimSpace(raw)
	for _, rule := range snippetRedactions {
		text = rule.re.ReplaceAllString(text, rule.repl)
	}
	runes := []rune(text)
	if len(runes) > errorSnippetLimit {
		runes = runes[:errorSnippetLimit]
	}
	return string(runes)
}

// —— JSONL 存储：append-only、按天轮转、保留 30 天 ————————————————
//
// 只用标准库 + JSONL（红线 4）：单实例部署、数据量小（日千级事件），查询用流式聚合，
// 不引入数据库。目录由 server 注入，与 routes.json 同一 bind mount，重启不丢。

type jsonlStore struct {
	mu     sync.Mutex
	dir    string
	prefix string
	single bool   // true=单文件 prefix.jsonl；false=按天 prefix-YYYYMMDD.jsonl
	day    string // 当前追加的日期（本地时区），变更时触发一次过期清理
}

func newJSONLStore(dir, prefix string, single bool) *jsonlStore {
	return &jsonlStore{dir: dir, prefix: prefix, single: single}
}

func (s *jsonlStore) pathFor(day string) string {
	if s.single {
		return filepath.Join(s.dir, s.prefix+".jsonl")
	}
	return filepath.Join(s.dir, s.prefix+"-"+day+".jsonl")
}

// append 追加一行 JSON。每次打开-写入-关闭：账本写入频率低（日千级），常驻句柄反而
// 要处理轮转与句柄失效，不值得。写失败原样返回错误，由调用方决定沉默。
func (s *jsonlStore) append(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	day := time.Now().Format("20060102")
	if day != s.day {
		// 轮转日顺带清理过期文件；清理失败不影响本次落账。
		s.cleanupLocked(time.Now())
		s.day = day
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.pathFor(day), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// cleanup 清理过期数据：按天轮转的删过期文件。启动时调用一次，之后每次轮转再调。
func (s *jsonlStore) cleanup(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
}

func (s *jsonlStore) cleanupLocked(now time.Time) {
	cutoff := now.Add(-errorLedgerRetention)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return // 目录还没建：没有过期文件可删
	}
	for _, entry := range entries {
		day, ok := rotatedDay(s.prefix, entry.Name())
		if !ok || entry.IsDir() {
			continue
		}
		if start := dayStart(day); !start.IsZero() && start.Before(cutoff) {
			os.Remove(filepath.Join(s.dir, entry.Name()))
		}
	}
}

// rotatedDay 从「prefix-YYYYMMDD.jsonl」里解析出日期串；不匹配返回 false。
func rotatedDay(prefix, name string) (string, bool) {
	if !strings.HasPrefix(name, prefix+"-") || !strings.HasSuffix(name, ".jsonl") {
		return "", false
	}
	day := strings.TrimSuffix(strings.TrimPrefix(name, prefix+"-"), ".jsonl")
	if len(day) != 8 {
		return "", false
	}
	for _, r := range day {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return day, true
}

// dayStart 把 YYYYMMDD 解析成本地时区当天零点；解析失败返回零值（零值不早于 cutoff，
// 文件因此被保守保留，不会被误删）。
func dayStart(day string) time.Time {
	parsed, err := time.ParseInLocation("20060102", day, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// —— F1 错误事件账本 ————————————————

type errorLedger struct {
	store *jsonlStore
}

func newErrorLedger(dir string) *errorLedger {
	return &errorLedger{store: newJSONLStore(dir, "errors", false)}
}

// record 落账一行失败事件。失败保持沉默（红线 1）：账本是尽力而为的观测，写不进去
// 也不能反过来影响调用。
func (l *errorLedger) record(ev errorEvent) {
	if err := l.store.append(ev); err != nil {
		log.Print("WARN: [error-ledger] 落账失败（保持沉默）：", err)
	}
}

// cleanup 清理过期账本（启动时调用一次）。
func (l *errorLedger) cleanup(now time.Time) {
	l.store.cleanup(now)
}

// —— 调用结果滚动窗口（/admin/errors 聚合的数据源）————————————
//
// 成功与失败都进窗口：错误率 = fail/(ok+fail)，没有分母就没有错误率。窗口是内存态、
// 重启从零累计——这正是「无数据 = 无统计」的克制口径；绝不从失败账本回放样本，
// 否则重启后只剩失败样本，错误率会被虚高成 100%。

type outcomeSample struct {
	ts       time.Time
	alias    string // 命中别名时的客户端原名（未命中留空）
	channel  routeChannel
	model    string // 通道原生模型名
	ok       bool
	category string // 失败时的归类
}

type outcomeWindow struct {
	mu      sync.Mutex
	now     func() time.Time
	samples []outcomeSample
	recent  []errorEvent // 最近失败事件，新在前
}

func newOutcomeWindow() *outcomeWindow {
	return &outcomeWindow{now: time.Now}
}

// add 记一次调用结果。纯内存、有界、无 I/O，同步调用不构成阻塞（红线 1 针对的是
// 落账这类 I/O，它在 recordErrorAsync 的独立协程里）。
func (w *outcomeWindow) add(ev errorEvent, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples = append(w.samples, outcomeSample{
		ts: time.UnixMilli(ev.Ts), alias: ev.Alias, channel: routeChannel(ev.Channel), model: ev.ModelResolved,
		ok: ok, category: ev.Category,
	})
	if !ok {
		w.recent = append([]errorEvent{ev}, w.recent...)
		if len(w.recent) > errorRecentKeep {
			w.recent = w.recent[:errorRecentKeep]
		}
	}
	// 裁剪：先按 24h（最长查询窗口）切，再按条数上限切，内存始终有界。
	cutoff := w.now().Add(-24 * time.Hour)
	drop := 0
	for drop < len(w.samples) && w.samples[drop].ts.Before(cutoff) {
		drop++
	}
	if drop > 0 {
		w.samples = append(w.samples[:0], w.samples[drop:]...)
	}
	if len(w.samples) > errorWindowMaxSamples {
		w.samples = w.samples[len(w.samples)-errorWindowMaxSamples:]
	}
}

// windowStats 是一个切片上的成功/失败计数视图。
type windowStats struct {
	ok, fail int
}

func (s windowStats) rate() float64 {
	if s.ok+s.fail == 0 {
		return 0
	}
	return float64(s.fail) / float64(s.ok+s.fail)
}

func (s windowStats) plus(sample outcomeSample) windowStats {
	if sample.ok {
		s.ok++
	} else {
		s.fail++
	}
	return s
}

// outcomeAggregate 是一次窗口聚合的全部维度，一次遍历出齐，聚合端点共用一份口径。
type outcomeAggregate struct {
	total             windowStats
	channels          map[routeChannel]windowStats
	models            map[routeChannel]map[string]windowStats
	categories        map[string]int
	channelCategories map[routeChannel]map[string]int
}

// collect 汇总窗口内的样本。倒序遍历，遇到窗口外的样本即可停（样本按时间追加）。
func (w *outcomeWindow) collect(window time.Duration) outcomeAggregate {
	cutoff := w.now().Add(-window)
	agg := outcomeAggregate{
		channels:          map[routeChannel]windowStats{},
		models:            map[routeChannel]map[string]windowStats{},
		categories:        map[string]int{},
		channelCategories: map[routeChannel]map[string]int{},
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := len(w.samples) - 1; i >= 0; i-- {
		sample := w.samples[i]
		if sample.ts.Before(cutoff) {
			break
		}
		agg.channels[sample.channel] = agg.channels[sample.channel].plus(sample)
		if sample.ok {
			agg.total.ok++
		} else {
			agg.total.fail++
		}
		if agg.models[sample.channel] == nil {
			agg.models[sample.channel] = map[string]windowStats{}
		}
		agg.models[sample.channel][sample.model] = agg.models[sample.channel][sample.model].plus(sample)
		if !sample.ok && sample.category != "" {
			agg.categories[sample.category]++
			if agg.channelCategories[sample.channel] == nil {
				agg.channelCategories[sample.channel] = map[string]int{}
			}
			agg.channelCategories[sample.channel][sample.category]++
		}
	}
	return agg
}

// aliasWindowStats 汇总某个别名在窗口内的调用样本。样本不足时仍如实返回计数，
// 是否告警由调用方按「样本 ≥5 且错误率 >50%」判定——这里不替调用方做决定。
// alias 为空时返回零值（未命中别名的调用不参与别名级告警）。
func (w *outcomeWindow) aliasWindowStats(alias string, window time.Duration) (stats windowStats, dominant string) {
	if alias == "" {
		return windowStats{}, ""
	}
	cutoff := w.now().Add(-window)
	byCategory := map[string]int{}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := len(w.samples) - 1; i >= 0; i-- {
		sample := w.samples[i]
		if sample.ts.Before(cutoff) {
			break
		}
		if sample.alias != alias {
			continue
		}
		stats = stats.plus(sample)
		if !sample.ok && sample.category != "" {
			byCategory[sample.category]++
		}
	}
	// 主因取窗口内失败最多的类别（同数按类别名稳定排序），用于告警归类与抑制键。
	best := 0
	for category, count := range byCategory {
		if count > best || (count == best && category < dominant) {
			best, dominant = count, category
		}
	}
	return stats, dominant
}

// recentFailures 返回最近 n 条失败事件（新在前）。
func (w *outcomeWindow) recentFailures(n int) []errorEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.recent) < n {
		n = len(w.recent)
	}
	out := make([]errorEvent, n)
	copy(out, w.recent[:n])
	return out
}

// —— 聚合响应模型（/admin/errors）————————————————

type errorModelStat struct {
	Model     string  `json:"model"`
	OK        int     `json:"ok"`
	Fail      int     `json:"fail"`
	ErrorRate float64 `json:"error_rate"`
}

type errorChannelStat struct {
	Channel    string           `json:"channel"`
	OK         int              `json:"ok"`
	Fail       int              `json:"fail"`
	ErrorRate  float64          `json:"error_rate"`
	Categories map[string]int   `json:"categories"`
	TopModels  []errorModelStat `json:"top_models"`
}

type errorCategoryStat struct {
	Category string  `json:"category"`
	Fail     int     `json:"fail"`
	Share    float64 `json:"share"`
}

// roundRate 把错误率四舍五入到 4 位小数，避免 0.30000000000000004 这类浮点尾巴直接
// 进 JSON 与页面。
func roundRate(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// adminErrors 聚合查询：GET /admin/errors?window=15m|1h|24h。按 (channel, category)
// 返回 {ok, fail, error_rate, top_models}，供前端健康页「错误统计」区块消费。
// 数据源是内存滚动窗口（成功+失败）；错误账本 JSONL 是排障用的持久留痕。
func (h *server) adminErrors(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("window")
	if key == "" {
		key = "15m"
	}
	window, ok := errorWindowFor(key)
	if !ok {
		adminError(w, 400, "window 只能是 15m、1h 或 24h")
		return
	}
	out := map[string]any{
		"window":       key,
		"generated_at": time.Now().UnixMilli(),
		"ok":           0,
		"fail":         0,
		"error_rate":   0,
		"channels":     []errorChannelStat{},
		"categories":   []errorCategoryStat{},
		"recent":       []errorEvent{},
	}
	if h.outcomes != nil {
		agg := h.outcomes.collect(window)
		out["ok"] = agg.total.ok
		out["fail"] = agg.total.fail
		out["error_rate"] = roundRate(agg.total.rate())
		out["channels"] = buildErrorChannelStats(agg)
		out["categories"] = buildErrorCategoryStats(agg.categories, agg.total.fail)
		out["recent"] = h.outcomes.recentFailures(errorRecentLimit)
	}
	writeJSON(w, 200, out)
}

// buildErrorChannelStats 把按通道的计数组装成页面消费的行：类别分布与错误最多的模型
// 都挂在通道行上，一次请求拿全区块需要的全部数据。
func buildErrorChannelStats(agg outcomeAggregate) []errorChannelStat {
	out := make([]errorChannelStat, 0, len(agg.channels))
	for channel, stat := range agg.channels {
		row := errorChannelStat{
			Channel:    string(channel),
			OK:         stat.ok,
			Fail:       stat.fail,
			ErrorRate:  roundRate(stat.rate()),
			Categories: map[string]int{},
		}
		for category, count := range agg.channelCategories[channel] {
			row.Categories[category] = count
		}
		row.TopModels = topErrorModels(agg.models[channel], 3)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out
}

// buildErrorCategoryStats 全局类别分布，按次数降序。
func buildErrorCategoryStats(byCategory map[string]int, totalFail int) []errorCategoryStat {
	out := make([]errorCategoryStat, 0, len(byCategory))
	for category, count := range byCategory {
		share := 0.0
		if totalFail > 0 {
			share = roundRate(float64(count) / float64(totalFail))
		}
		out = append(out, errorCategoryStat{Category: category, Fail: count, Share: share})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fail != out[j].Fail {
			return out[i].Fail > out[j].Fail
		}
		return out[i].Category < out[j].Category
	})
	return out
}

// topErrorModels 取失败最多的前 n 个模型（同失败数按模型名稳定排序）。
func topErrorModels(stats map[string]windowStats, n int) []errorModelStat {
	out := make([]errorModelStat, 0, len(stats))
	for model, stat := range stats {
		out = append(out, errorModelStat{Model: model, OK: stat.ok, Fail: stat.fail, ErrorRate: roundRate(stat.rate())})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fail != out[j].Fail {
			return out[i].Fail > out[j].Fail
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// —— 采集出口：把一次出口调用的观测归档 ————————————————
//
// 由 proxy.go 的 observePublicExits 在响应结束后调用：成功只进内存窗口（聚合的
// 分母），失败再异步落账。全部读已捕获的状态，不产生任何上游调用，也不阻塞调用
// 主链路（落账在 recordErrorAsync 的独立协程里）。
func (h *server) finishExitObserve(obs exitObservation) {
	if h.outcomes == nil {
		return
	}
	status := obs.status
	ok := status < 400 // 0 = 未显式写头（net/http 隐式 200），按成功计
	channel := publicChannelForModel(obs.model)
	routedPublic := obs.model
	ev := errorEvent{
		Ts:         time.Now().UnixMilli(),
		Channel:    string(channel),
		API:        obs.api,
		Mode:       obs.mode,
		DurationMs: obs.durationMs,
		Client:     obs.client,
	}
	// 别名命中时，分流侧把解析结论写进了 X-Prism-* 响应头：通道、改写后的公共模型名
	// 都从那里取，请求里的原名就是别名。未命中时按模型前缀判通道。
	if matched := routeChannel(obs.prismChannel); matched != "" {
		channel = matched
		routedPublic = obs.model
		if obs.routedModel != "" {
			routedPublic = obs.routedModel
		}
		ev.Channel = string(channel)
		ev.Alias = obs.model
	}
	ev.ModelResolved = nativeModelName(channel, routedPublic)
	ev.RoutedVia = routedViaFor(channel)
	ev.RetryCount = obs.retryCount
	if !ok {
		ev.UpstreamStatus = status
		ev.Category = failureCategory(status, obs.body)
		ev.ErrorSnippet = sanitizeSnippet(obs.body)
	}
	h.outcomes.add(ev, ok)
	if !ok {
		h.recordErrorAsync(ev)
	}
}

// recordErrorAsync 独立协程落账，纪律与 reportUsageAsync 一致：绝不阻塞或影响调用
// 主链路；落账失败保持沉默。文件追加本身是有界小操作（每次只写一行），不需要超时
// 上下文参与。
//
// F4 告警检测挂在同一条协程里：检测要读内存窗口（无 I/O、有锁但极短），落盘也是
// 一次小追加。两者都不在请求路径上，合用一个协程即足够，不额外开第二个。
func (h *server) recordErrorAsync(ev errorEvent) {
	if h.ledger == nil && h.alerts == nil {
		return
	}
	go func() {
		if h.ledger != nil {
			h.ledger.record(ev)
		}
		h.detectAlerts(ev)
	}()
}

// —— F4 P1 告警事件（PRD §2-F4）————————————————
//
// console 侧只负责「检测与落盘」：命中 P1 规则的事件 append 到 alerts.jsonl（单文件、
// 保留 30 天），并留一份内存环形缓冲供 GET /admin/alerts 快速读取。通知侧（看板卡 /
// 桌面通知）由 Hermes 轮询这个端点，console 不引入任何外部通知依赖（PRD 原文）。
//
// 两条 P1 规则（M1 全量）：
//  1. 在用别名 15min 滚动窗口错误率 >50% 且窗口调用样本 ≥5；
//  2. 任一 pin 类错误事件（pinned_account_unavailable 极具误导性，必须人看）。
//
// 抑制：同 (channel, category) 30min 内只出一条告警事件。抑制键刻意不含 alias——
// 同一通道的同一类错误在抑制窗口内应归为一条，避免一个别名抖动刷屏。

// alertEvent 是告警账本的单行记录。字段面向 Hermes 看板卡：一眼能看出「哪条规则、
// 哪条通道、什么错误、错多少」，rule 是稳定的规则标识（前端与轮询侧据此分支）。
type alertEvent struct {
	Ts           int64   `json:"ts"`
	Level        string  `json:"level"`
	Rule         string  `json:"rule"`
	Alias        string  `json:"alias,omitempty"`
	Channel      string  `json:"channel"`
	Category     string  `json:"category"`
	Model        string  `json:"model,omitempty"`
	Message      string  `json:"message"`
	ErrorRate    float64 `json:"error_rate"`
	Samples      int     `json:"samples"`
	Fail         int     `json:"fail"`
	ErrorSnippet string  `json:"error_snippet,omitempty"`
}

// alertLedger 持有告警落盘器与抑制状态。
type alertLedger struct {
	mu         sync.Mutex
	store      *jsonlStore
	recent     []alertEvent         // 内存环形缓冲，新在前
	suppressed map[string]time.Time // 抑制键 → 上次出告警时刻
}

func newAlertLedger(dir string) *alertLedger {
	// alerts.jsonl 是单文件（PRD 原文的路径没有按天后缀），保留 30 天靠按 ts 重写。
	return &alertLedger{store: newJSONLStore(dir, "alerts", true), suppressed: map[string]time.Time{}}
}

// alertSuppressKey 生成抑制键：同 (channel, category) 30min 内只出一条。
func alertSuppressKey(channel, category string) string {
	return channel + "\x00" + category
}

// record 落一条告警。抑制窗口内同类告警直接丢弃（返回 false）；落盘失败保持沉默
// （红线 1：告警是观测，不是调用契约），但内存缓冲照常更新，接口仍能看到。
func (l *alertLedger) record(ev alertEvent) bool {
	key := alertSuppressKey(ev.Channel, ev.Category)
	now := time.UnixMilli(ev.Ts)
	l.mu.Lock()
	if last, ok := l.suppressed[key]; ok && now.Sub(last) < alertSuppressWindow {
		l.mu.Unlock()
		return false
	}
	l.suppressed[key] = now
	l.recent = append([]alertEvent{ev}, l.recent...)
	if len(l.recent) > alertRecentKeep {
		l.recent = l.recent[:alertRecentKeep]
	}
	store := l.store
	l.mu.Unlock()
	if store != nil {
		if err := store.append(ev); err != nil {
			log.Print("WARN: [alerts] 告警落盘失败（保持沉默）：", err)
		}
	}
	return true
}

// recentAlerts 返回内存里最近 n 条告警（新在前）。
func (l *alertLedger) recentAlerts(n int) []alertEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.recent) < n {
		n = len(l.recent)
	}
	out := make([]alertEvent, n)
	copy(out, l.recent[:n])
	return out
}

// cleanup 在启动时清理过期告警：单文件按 ts 重写，只留 alertRetention 内的行，并把
// 留存行的抑制时刻回填进内存（重启后抑制窗口不因进程重启而失效，避免重启即重报）。
func (l *alertLedger) cleanup(now time.Time) {
	if l.store == nil {
		return
	}
	cutoff := now.Add(-alertRetention)
	path := l.store.pathFor("")
	lines := readJSONLLines(path)
	var kept [][]byte
	recent := make([]alertEvent, 0, alertRecentKeep)
	suppressed := map[string]time.Time{}
	for _, line := range lines {
		var ev alertEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // 损坏行直接丢弃，不让一条脏数据卡住清理
		}
		ts := time.UnixMilli(ev.Ts)
		if ts.Before(cutoff) {
			continue
		}
		kept = append(kept, line)
		recent = append(recent, ev)
		if ts.After(suppressed[alertSuppressKey(ev.Channel, ev.Category)]) {
			suppressed[alertSuppressKey(ev.Channel, ev.Category)] = ts
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, ts := range suppressed {
		l.suppressed[key] = ts
	}
	if len(recent) > 0 {
		// 文件内旧在前：反转成新在前，并截到内存上限。
		for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
			recent[i], recent[j] = recent[j], recent[i]
		}
		if len(recent) > alertRecentKeep {
			recent = recent[:alertRecentKeep]
		}
		l.recent = recent
	}
	if len(kept) != len(lines) {
		l.rewriteLocked(kept)
	}
}

// rewriteLocked 用留存行整体重写单文件（清理过期行）。失败保持沉默：下次启动再试。
func (l *alertLedger) rewriteLocked(kept [][]byte) {
	path := l.store.pathFor("")
	var buf []byte
	for _, line := range kept {
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		log.Print("WARN: [alerts] 过期告警清理失败（保持沉默）：", err)
	}
}

// detectAlerts 在错误事件落账后检测 P1 规则。命中即 record（内部做抑制）。
// 检测只读内存窗口与当前事件，不产生任何上游调用，也不阻塞调用主链路。
func (h *server) detectAlerts(ev errorEvent) {
	if h.alerts == nil {
		return
	}
	// 规则 2：任一 pin 类错误事件。该类错误极具误导性（core 严格 pin 被锁时回 503，
	// 看起来像通道挂了），必须人看，因此不看样本数、不看错误率，命中即告警。
	if ev.Category == "pin" {
		h.alerts.record(alertEvent{
			Ts: ev.Ts, Level: alertLevelP1, Rule: alertRulePinError,
			Alias: ev.Alias, Channel: ev.Channel, Category: ev.Category, Model: ev.ModelResolved,
			Message:      fmt.Sprintf("通道 %s 出现 pinned 账号拦截：上游回 %d，%s", ev.Channel, ev.UpstreamStatus, pinAlertHint),
			ErrorSnippet: ev.ErrorSnippet,
		})
		return
	}
	// 规则 1：在用别名 15min 滚动窗口错误率 >50% 且样本 ≥5。只对命中别名的调用生效
	// （未命中别名就没有「在用别名」这个维度）。
	if ev.Alias == "" || h.outcomes == nil {
		return
	}
	stats, dominant := h.outcomes.aliasWindowStats(ev.Alias, alertAliasWindow)
	if stats.ok+stats.fail < alertAliasMinSamples {
		return // 样本不足不告警（防呆：单次失败不触发）
	}
	rate := stats.rate()
	if rate <= alertAliasRateThreshold {
		return
	}
	category := dominant
	if category == "" {
		category = ev.Category
	}
	h.alerts.record(alertEvent{
		Ts: ev.Ts, Level: alertLevelP1, Rule: alertRuleAliasErrorRate,
		Alias: ev.Alias, Channel: ev.Channel, Category: category, Model: ev.ModelResolved,
		Message:      fmt.Sprintf("别名 %s 15min 错误率 %.0f%%（%d/%d），主因 %s", ev.Alias, rate*100, stats.fail, stats.ok+stats.fail, category),
		ErrorRate:    roundRate(rate),
		Samples:      stats.ok + stats.fail,
		Fail:         stats.fail,
		ErrorSnippet: ev.ErrorSnippet,
	})
}

// pinAlertHint 是 pin 告警的固定处置提示，与健康模块的排查习惯对齐（对照测试后
// 决定扩白名单 / repin / 换通道），让人看到告警就知道下一步。
const pinAlertHint = "该错误极易误判为通道故障，请先对照 corePinHint 与别名映射确认是否账号被锁"

// adminAlerts 返回最近告警（新在前），供 Hermes 定时轮询与页面留痕。
// 只读内存缓冲，不读文件——文件是排障用的持久留痕，重启由 cleanup 回填。
func (h *server) adminAlerts(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"generated_at": time.Now().UnixMilli(),
		"level":        alertLevelP1,
		"alerts":       []alertEvent{},
	}
	if h.alerts != nil {
		out["alerts"] = h.alerts.recentAlerts(alertRecentKeep)
	}
	writeJSON(w, 200, out)
}
