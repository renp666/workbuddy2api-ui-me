package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const circuitCooldown = 30 * time.Minute

type circuitChange struct {
	Alias  string      `json:"alias"`
	Before routeTarget `json:"before"`
	After  routeTarget `json:"after"`
}

type circuitState struct {
	Committed bool            `json:"committed"`
	Ts        int64           `json:"ts"`
	Channel   routeChannel    `json:"channel"`
	Model     string          `json:"model"`
	Alias     string          `json:"alias,omitempty"`
	Open      bool            `json:"open"`
	Observing bool            `json:"observing"`
	Reason    string          `json:"reason"`
	Category  string          `json:"category,omitempty"`
	Snippet   string          `json:"error_snippet,omitempty"`
	Samples   int             `json:"samples"`
	Fail      int             `json:"fail"`
	// CooldownMs 是本次熔断的冷却时长：429 带 Retry-After 头时按头定时（封顶
	// circuitCooldown），其余维持固定窗口。旧状态无该字段时 sweep 回落 circuitCooldown。
	CooldownMs int64           `json:"cooldown_ms,omitempty"`
	Changes    []circuitChange `json:"changes,omitempty"`
}

type circuitBreaker struct {
	mu      sync.RWMutex
	h       *server
	now     func() time.Time
	store   *jsonlStore
	states  map[routeTarget]circuitState
	streaks map[routeTarget]int
	pending []circuitState
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

func circuitKey(t routeTarget) routeTarget {
	return routeTarget{Channel: t.Channel, Model: t.nativeModel()}
}

// 3-strong 是显式人工管理组，不因共享同一原生模型而自动摘除。
func circuitProtected(e routeEntry) bool {
	return e.Alias == "3-strong" || strings.HasPrefix(e.Alias, "3-strong-") || strings.HasPrefix(e.Alias, "3-strong/")
}

func (h *server) initCircuits(now func() time.Time) {
	if h.routes == nil || h.outcomes == nil {
		return
	}
	c := &circuitBreaker{h: h, now: now, store: newJSONLStore(consoleDataDir(h.cfg.RouteFile), "circuits", false),
		states: map[routeTarget]circuitState{}, streaks: map[routeTarget]int{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	paths, _ := filepath.Glob(filepath.Join(c.store.dir, "circuits-*.jsonl"))
	for _, path := range paths {
		for _, line := range readJSONLLines(path) {
			var state circuitState
			if json.Unmarshal(line, &state) != nil || state.Channel == "" || state.Model == "" {
				continue
			}
			c.states[circuitKey(routeTarget{state.Channel, state.Model})] = state
		}
	}
	h.circuits = c
	h.routes.circuits = c
	h.routes.reload()
	for _, state := range c.states {
		if !state.Open || state.Committed {
			continue
		}
		cooldown := time.Duration(state.CooldownMs) * time.Millisecond
		if cooldown <= 0 {
			cooldown = circuitCooldown
		}
		if now().Sub(time.UnixMilli(state.Ts)) < cooldown {
			c.pending = append(c.pending, state)
		}
	}
	// 回放只恢复熔断状态，不回放失败账本冒充全量窗口分母。
	c.sweep()
	c.drain()
	c.store.cleanup(now())
	h.outcomes.mu.Lock()
	h.outcomes.onAdd = c.observe
	h.outcomes.mu.Unlock()
	go c.run()
}

// observe 在 outcomeWindow 锁内执行，仅短暂更新内存并通知 worker，无 I/O。
func (c *circuitBreaker) observe(ev errorEvent, ok bool) {
	if ev.ModelResolved == "" {
		return
	}
	// 冷却准入（§九 9.3）：只有 429/5xx/超时类失败计 streak、进窗口分母；auth /
	// model_error 等需要人工处理的失败只走告警账本，不触发熔断（pin 的排除被该
	// 判据覆盖，不用再单列）。
	if !ok && !cooldownCategory(ev.Category) {
		return
	}
	key := circuitKey(routeTarget{routeChannel(ev.Channel), ev.ModelResolved})
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[key]
	if state.Open {
		return
	}
	if ok {
		c.streaks[key] = 0
		return
	}
	c.streaks[key]++
	stats := windowStats{}
	cutoff := c.now().Add(-routeErrorWindow)
	for _, sample := range c.h.outcomes.samples {
		if sample.ts.Before(cutoff) || (state.Observing && sample.ts.UnixMilli() < state.Ts) || (!sample.ok && !cooldownCategory(sample.category)) {
			continue
		}
		if circuitKey(routeTarget{sample.channel, sample.model}) == key {
			stats = stats.plus(sample)
		}
	}
	reason := ""
	if c.streaks[key] >= 3 {
		reason = fmt.Sprintf("连续失败 %d 次", c.streaks[key])
	} else if stats.ok+stats.fail >= 5 && stats.rate() > .8 {
		reason = fmt.Sprintf("15min 错误率 %.0f%%（n=%d）", stats.rate()*100, stats.ok+stats.fail)
	}
	if reason == "" {
		return
	}
	// 观察期只看恢复后的样本，恢复前的失败不得立即再次触发。
	state = circuitState{Ts: c.now().UnixMilli(), Channel: key.Channel, Model: key.Model, Alias: ev.Alias, Open: true,
		Reason: reason, Category: ev.Category, Snippet: sanitizeSnippet(ev.ErrorSnippet), Samples: stats.ok + stats.fail, Fail: stats.fail,
		CooldownMs: retryAfterCooldown(ev.RetryAfterSec)}
	c.states[key] = state
	c.pending = append(c.pending, state)
	c.notify()
}

// retryAfterCooldown 把 Retry-After 换算成冷却时长：有头用头（封顶 circuitCooldown），
// 无头维持固定窗口。
func retryAfterCooldown(retryAfterSec int) int64 {
	cooldown := circuitCooldown
	if retryAfterSec > 0 {
		if v := time.Duration(retryAfterSec) * time.Second; v < cooldown {
			cooldown = v
		}
	}
	return int64(cooldown / time.Millisecond)
}

// cooldownLabel 把冷却时长渲染成人读标签（30min / 120s），供熔断恢复告警；旧状态
// 无 CooldownMs 字段（0）时回落固定窗口的展示值。
func cooldownLabel(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		d = circuitCooldown
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dmin", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int((d+time.Second-1)/time.Second))
}

func (c *circuitBreaker) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *circuitBreaker) blocked(t routeTarget) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.states[circuitKey(t)].Open
}

func (c *circuitBreaker) originalEntry(e routeEntry) routeEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, state := range c.states {
		if !state.Open {
			continue
		}
		for _, change := range state.Changes {
			if e.Alias == change.Alias && (routeTarget{e.Channel, e.Model}) == change.After {
				e.Channel, e.Model = change.Before.Channel, change.Before.Model
				return e
			}
		}
	}
	return e
}

func (c *circuitBreaker) run() {
	defer close(c.done)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-c.stop:
			c.drain()
			return
		case <-tick.C:
			c.sweep()
			c.drain()
		case <-c.wake:
			c.drain()
		}
	}
}

func (c *circuitBreaker) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, state := range c.states {
		if !state.Open {
			continue
		}
		cooldown := time.Duration(state.CooldownMs) * time.Millisecond
		if cooldown <= 0 {
			cooldown = circuitCooldown
		}
		if c.now().Sub(time.UnixMilli(state.Ts)) < cooldown {
			continue
		}
		// 恢复完成前继续硬摘除。worker 成功合并配置后才放行。
		already := false
		for _, job := range c.pending {
			if job.Channel == key.Channel && job.Model == key.Model {
				already = true
			}
		}
		if already {
			continue
		}
		state.Open, state.Observing = false, true
		c.pending = append(c.pending, state)
	}
}

func (c *circuitBreaker) drain() {
	c.mu.Lock()
	jobs := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, state := range jobs {
		if err := c.apply(&state); err != nil {
			log.Print("WARN: [circuits] 配置事务失败，稍后重试：", err)
			c.mu.Lock()
			c.pending = append(c.pending, state)
			c.mu.Unlock()
			continue
		}
		key := circuitKey(routeTarget{state.Channel, state.Model})
		if !state.Open {
			state.Ts = c.now().UnixMilli()
		}
		state.Committed = true
		if err := c.store.append(state); err != nil {
			log.Print("WARN: [circuits] 状态落盘失败：", err)
			c.mu.Lock()
			c.pending = append(c.pending, state)
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		c.states[key] = state
		if !state.Open {
			c.streaks[key] = 0
		}
		c.mu.Unlock()
		if c.h.alerts != nil {
			rule, message := "circuit_open", "熔断："
			if !state.Open {
				rule, message = "circuit_closed", "冷却 "+cooldownLabel(state.CooldownMs)+" 后恢复："
			}
			rate := float64(0)
			if state.Samples > 0 {
				rate = float64(state.Fail) / float64(state.Samples)
			}
			c.h.alerts.record(alertEvent{Ts: c.now().UnixMilli(), Level: alertLevelP1, Rule: rule, Alias: state.Alias, Channel: string(state.Channel), Model: state.Model, Category: state.Category, Message: message + state.Reason, ErrorRate: rate, Samples: state.Samples, Fail: state.Fail, ErrorSnippet: state.Snippet})
		}
	}
}

// apply 与管理写配置共用 saveMu。只变 active 字符串值，备用数组与未知字段原样保留。
func (c *circuitBreaker) apply(state *circuitState) error {
	s := c.h.routes
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var file routeFile
	if err = json.Unmarshal(raw, &file); err != nil {
		return err
	}
	changes := []circuitChange{}
	if state.Open {
		if len(state.Changes) > 0 {
			for _, change := range state.Changes {
				for _, e := range file.Models {
					if e.Alias == change.Alias && (routeTarget{e.Channel, e.Model}) == change.Before {
						changes = append(changes, change)
					}
				}
			}
		} else {
			for _, e := range file.Models {
				if circuitProtected(e) {
					continue
				}
				original := c.originalEntry(e)
				if circuitKey(routeTarget{original.Channel, original.Model}) != (routeTarget{state.Channel, state.Model}) && circuitKey(routeTarget{e.Channel, e.Model}) != (routeTarget{state.Channel, state.Model}) {
					continue
				}
				for _, target := range original.targets() {
					if !c.blocked(target) {
						changes = append(changes, circuitChange{e.Alias, routeTarget{original.Channel, original.Model}, target})
						break
					}
				}
			}
		}
	} else {
		// routes.bak 为最近一次改写的真源；多次熔断覆盖备份时用该事件保存的对应字段。
		backup, _ := os.ReadFile(filepath.Join(filepath.Dir(s.path), "routes.bak"))
		var saved routeFile
		_ = json.Unmarshal(backup, &saved)
		for _, change := range state.Changes {
			before := change.Before
			for _, e := range saved.Models {
				if e.Alias == change.Alias && (routeTarget{e.Channel, e.Model}) == before {
					before = routeTarget{e.Channel, e.Model}
				}
			}
			for _, e := range file.Models {
				original := c.originalEntry(e)
				automated := (routeTarget{e.Channel, e.Model}) == change.After || (routeTarget{original.Channel, original.Model}) == change.Before && (routeTarget{e.Channel, e.Model}) != change.Before
				if !circuitProtected(e) && e.Alias == change.Alias && automated {
					target := before
					if c.blockedExcept(target, routeTarget{state.Channel, state.Model}) {
						for _, t := range append([]routeTarget{before}, e.Fallbacks...) {
							if !c.blockedExcept(t, routeTarget{state.Channel, state.Model}) {
								target = t
								break
							}
						}
					}
					changes = append(changes, circuitChange{e.Alias, routeTarget{e.Channel, e.Model}, target})
				}
			}
		}
	}
	next, err := patchCircuitJSON(raw, changes)
	if err != nil {
		return err
	}
	if state.Open && len(state.Changes) == 0 {
		if err = writeCircuitFile(filepath.Join(filepath.Dir(s.path), "routes.bak"), raw); err != nil {
			return err
		}
		// 先持久化带原目标的意图，崩溃在 routes 替换前后均可回放。
		state.Changes = changes
		if err = c.store.append(*state); err != nil {
			state.Changes = nil
			return err
		}
	}
	if !bytes.Equal(raw, next) {
		if err = writeCircuitFile(s.path, next); err != nil {
			return err
		}
	}

	if err = json.Unmarshal(next, &file); err != nil {
		return err
	}
	s.mu.Lock()
	s.file = file
	s.mu.Unlock()
	return nil
}

func (c *circuitBreaker) blockedExcept(t, except routeTarget) bool {
	if circuitKey(t) == except {
		return false
	}
	return c.blocked(t)
}

func writeCircuitFile(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".circuit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(f.Name(), 0600); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

type circuitSpan struct {
	start, end int
	raw        []byte
}

// Decoder 的 offset 给出值的字节区间；只定位 models 数组内的 active 字段。
func patchCircuitJSON(raw []byte, changes []circuitChange) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	var spans []circuitSpan
	var walk func(string) error
	walk = func(path string) error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			values := map[string]circuitSpan{}
			alias := ""
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key := k.(string)
				start := int(d.InputOffset())
				for start < len(raw) && (raw[start] == ':' || raw[start] == ' ' || raw[start] == '\n' || raw[start] == '\r' || raw[start] == '\t') {
					start++
				}
				if path == "/models/entry" && (key == "alias" || key == "channel" || key == "model") {
					var value string
					if err := d.Decode(&value); err != nil {
						return err
					}
					values[key] = circuitSpan{start, int(d.InputOffset()), nil}
					if key == "alias" {
						alias = value
					}
				} else {
					if err := walk(path + "/" + key); err != nil {
						return err
					}
				}
			}
			if _, err := d.Token(); err != nil {
				return err
			}
			if path == "/models/entry" {
				for _, change := range changes {
					if change.Alias == alias {
						for key, value := range map[string]string{"channel": string(change.After.Channel), "model": change.After.Model} {
							span, ok := values[key]
							if !ok {
								return fmt.Errorf("active 缺少 %s", key)
							}
							span.raw, _ = json.Marshal(value)
							spans = append(spans, span)
						}
					}
				}
			}
		} else if delim == '[' {
			for d.More() {
				child := path + "/entry"
				if err := walk(child); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	// 从后向前替换，避免前面的替换改变后续 offset。

	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := append([]byte(nil), raw...)
	for _, span := range spans {
		out = append(append(append([]byte(nil), out[:span.start]...), span.raw...), out[span.end:]...)
	}
	return out, nil
}

// headerPrismCircuitOpen 标记本次 503 是网关自产的熔断拦截（非上游失败），
// 出口观察器据此把它排除在熔断输入之外，防止「自产 503 喂回熔断器」的自反馈。
const headerPrismCircuitOpen = "X-Prism-Circuit-Open"

// 全链熔断的内部占位符只在本地返回 503，禁止当成真实模型发给上游。
func (h *server) circuitExitGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method == http.MethodPost && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/responses") {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, zcodeRouteBodyLimit))
			if err != nil {
				http.Error(w, "请求内容不可读取", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var envelope struct {
				Model string `json:"model"`
			}
			var decision routeDecision
			matched := false
			if json.Unmarshal(raw, &envelope) == nil {
				decision, matched = h.resolveAlias(r.Context(), envelope.Model)
			}
			if matched && decision.RoutedModel == "__circuit_open__" {
				w.Header().Set(headerPrismCircuitOpen, "1")
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"type": "circuit_open", "message": "候选链全部熔断，请稍后重试"}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func circuitRouteReason(reason string, index int) string {
	if index > 0 {
		return fmt.Sprintf("%s · 熔断切第 %d 跳备用", reason, index+1)
	}
	return reason
}
