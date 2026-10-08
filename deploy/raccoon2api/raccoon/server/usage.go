package server

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
)

const maxUsageDays = 30

type usageEntry struct {
	Requests         int64 `json:"requests"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type usageDay struct {
	Date             string                 `json:"date"`
	Requests         int64                  `json:"requests"`
	Errors           int64                  `json:"errors"`
	PromptTokens     int64                  `json:"prompt_tokens"`
	CompletionTokens int64                  `json:"completion_tokens"`
	TotalTokens      int64                  `json:"total_tokens"`
	ByAccount        map[string]*usageEntry `json:"by_account"`
	ByModel          map[string]*usageEntry `json:"by_model"`
}

func (d *usageDay) add(uid, model string, prompt, completion int64, isErr bool) {
	if d.ByAccount == nil {
		d.ByAccount = map[string]*usageEntry{}
	}
	if d.ByModel == nil {
		d.ByModel = map[string]*usageEntry{}
	}
	d.Requests++
	d.PromptTokens += prompt
	d.CompletionTokens += completion
	d.TotalTokens += prompt + completion
	if isErr {
		d.Errors++
	}
	bump(d.ByAccount, uid, prompt, completion, isErr)
	bump(d.ByModel, model, prompt, completion, isErr)
}

func bump(m map[string]*usageEntry, key string, prompt, completion int64, isErr bool) {
	if key == "" {
		return
	}
	e, ok := m[key]
	if !ok {
		e = &usageEntry{}
		m[key] = e
	}
	e.Requests++
	e.PromptTokens += prompt
	e.CompletionTokens += completion
	e.TotalTokens += prompt + completion
	if isErr {
		e.Errors++
	}
}

type usageStore struct {
	mu   sync.Mutex
	path string
	days map[string]*usageDay
}

func NewUsageStore(path string) *usageStore {
	s := &usageStore{path: path, days: map[string]*usageDay{}}
	if path == "" {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var list []*usageDay
	if json.Unmarshal(raw, &list) != nil {
		return s
	}
	for _, d := range list {
		if d == nil || d.Date == "" {
			continue
		}
		if d.ByAccount == nil {
			d.ByAccount = map[string]*usageEntry{}
		}
		if d.ByModel == nil {
			d.ByModel = map[string]*usageEntry{}
		}
		s.days[d.Date] = d
	}
	return s
}

func (s *usageStore) record(uid, model string, prompt, completion int64, isErr bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.days == nil {
		s.days = map[string]*usageDay{}
	}
	date := time.Now().Format("2006-01-02")
	d, ok := s.days[date]
	if !ok {
		d = &usageDay{Date: date}
		s.days[date] = d
	}
	d.add(uid, model, prompt, completion, isErr)
	s.trimLocked()
	s.saveLocked()
}

// snapshot 返回按日期倒序的视图，避免调用方拿到内部指针。
func (s *usageStore) snapshot() []*usageDay {
	if s == nil {
		return []*usageDay{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *usageStore) snapshotLocked() []*usageDay {
	out := make([]*usageDay, 0, len(s.days))
	for _, d := range s.days {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out
}

// trimLocked 只保留最近 maxUsageDays 天。
func (s *usageStore) trimLocked() {
	if len(s.days) <= maxUsageDays {
		return
	}
	dates := make([]string, 0, len(s.days))
	for date := range s.days {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates[:len(dates)-maxUsageDays] {
		delete(s.days, date)
	}
}

func (s *usageStore) saveLocked() {
	if s.path == "" {
		return
	}
	raw, err := json.MarshalIndent(s.snapshotLocked(), "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, append(raw, '\n'), 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

func (h *Handler) adminUsage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"days": h.usage.snapshot()})
}

// tailCap 是用量提取保留的响应尾部字节数上限。
const tailCap = 256 << 10

// capWriter 透传写入的同时保留尾部字节，供流式结束后提取 usage。
type capWriter struct {
	http.ResponseWriter
	tail []byte
}

func (c *capWriter) Write(p []byte) (int, error) {
	c.tail = append(c.tail, p...)
	if len(c.tail) > tailCap {
		c.tail = append([]byte(nil), c.tail[len(c.tail)-tailCap:]...)
	}
	return c.ResponseWriter.Write(p)
}

var usageRe = regexp.MustCompile(`"usage"\s*:\s*\{`)

// extractUsage 从 SSE 尾部或 JSON 响应里取 token 用量。
// 流式响应的最终 chunk 才带完整 usage，因此取最后一个匹配对象。
func extractUsage(tail []byte) (prompt, completion int64) {
	locs := usageRe.FindAllIndex(tail, -1)
	if len(locs) == 0 {
		return 0, 0
	}
	start := locs[len(locs)-1][0]
	depth := 0
	end := -1
	for i := start; i < len(tail); i++ {
		switch tail[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end > 0 {
			break
		}
	}
	if end <= 0 {
		return 0, 0
	}
	var obj struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	}
	if json.Unmarshal(tail[start:end], &obj) != nil {
		return 0, 0
	}
	prompt = obj.PromptTokens
	completion = obj.CompletionTokens
	// 部分上游只给 total_tokens，反推缺失的 completion。
	if completion == 0 && obj.TotalTokens > prompt {
		completion = obj.TotalTokens - prompt
	}
	return prompt, completion
}
