package main

import (
	"encoding/json"
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
// 纪律（红线 1/3/4）：落账在独立协程里跑，绝不阻塞调用主链路，失败保持沉默——统计是
// 尽力而为，不是调用契约；error_snippet 落盘前脱敏（不含 key/token/账号）；只用标准库
// + JSONL，不建数据库、不新增第三方依赖（go.mod 不动）。
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
		ts: time.UnixMilli(ev.Ts), channel: routeChannel(ev.Channel), model: ev.ModelResolved,
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
func (h *server) recordErrorAsync(ev errorEvent) {
	if h.ledger == nil {
		return
	}
	ledger := h.ledger
	go ledger.record(ev)
}
