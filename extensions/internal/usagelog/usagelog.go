// Package usagelog 按次记录成功模型调用的用量观测（账号、模型、token、积分扣费），
// 供管理台「调用统计」按时间范围查询。数据按天落 JSONL 到 core 数据目录的
// usage/ 子目录；credit 缺失时保持 nil——缺失≠0，不把未观测写成免费。
package usagelog

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workbuddy2api/internal/auth"
)

// Entry 单次成功调用的用量观测。Prompt/Completion 为 -1 表示上游未回报 usage；
// Credit 为 nil 表示上游未回报 credit（显式 0 是合法免费观测，用指针区分）。
type Entry struct {
	TS         int64    `json:"ts"` // Unix 秒
	UID        string   `json:"uid"`
	Account    string   `json:"account"` // 昵称；空时回落 UID
	Model      string   `json:"model"`
	Mode       string   `json:"mode"` // "stream" | "sync"
	Prompt     int      `json:"prompt_tokens"`
	Completion int      `json:"completion_tokens"`
	Credit     *float64 `json:"credit,omitempty"`
}

// Summary 时间范围内的聚合。Prompt/Completion/Credit 只累计已知观测；
// Missing 计数让展示层区分「0 消耗」与「未观测」，不把缺失当零。
type Summary struct {
	Calls         int     `json:"calls"`
	Prompt        int64   `json:"prompt_tokens"`
	Completion    int64   `json:"completion_tokens"`
	Credit        float64 `json:"credit"`
	CreditMissing int     `json:"credit_missing"`
	UsageMissing  int     `json:"usage_missing"`
}

// Log 按天文件的追加式用量账本。零值不可用，经 Open 构造。
type Log struct {
	mu  sync.Mutex
	dir string
}

// dayLayout 按本地日期切分文件，与时间范围查询的日界一致。
const dayLayout = "20060102"

// Open 建立以 dir 为根的用量账本；目录权限 0700，dir 通常是数据目录下的 usage/。
func Open(dir string) (*Log, error) {
	if dir == "" {
		return nil, errors.New("usagelog 目录为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{dir: dir}, nil
}

// global 是 handler 成功路径引用的账本；Open 接线前为 nil（no-op）。
var global atomic.Pointer[Log]

// Set 接线全局账本；传 nil 关闭记录（测试与未接线场景）。
func Set(l *Log) { global.Store(l) }

// RecordAuth 记录一次成功调用的观测；未接线时 no-op，绝不阻塞调用方语义。
// prompt/completion < 0 表示未知；hasCredit=false 时 credit 不落盘。
func RecordAuth(acct *auth.Auth, model, mode string, prompt, completion int, credit float64, hasCredit bool) {
	l := global.Load()
	if l == nil || acct == nil {
		return
	}
	e := Entry{TS: time.Now().Unix(), UID: acct.UID, Account: acct.Nickname, Model: model, Mode: mode, Prompt: prompt, Completion: completion}
	if e.Account == "" {
		e.Account = acct.UID
	}
	if hasCredit {
		e.Credit = &credit
	}
	l.Record(e)
}

// FromResponse 从非流式聚合响应提取 usage 观测；缺失字段返回 -1 / hasCredit=false。
func FromResponse(resp map[string]any) (prompt, completion int, credit float64, hasCredit bool) {
	prompt, completion = -1, -1
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return prompt, completion, 0, false
	}
	if v, ok := u["prompt_tokens"].(float64); ok {
		prompt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		completion = int(v)
	}
	if v, ok := u["credit"].(float64); ok {
		return prompt, completion, v, true
	}
	return prompt, completion, 0, false
}

// Record 追加一条观测到写入当天的 JSONL；失败只记日志，不影响请求路径。
func (l *Log) Record(e Entry) {
	line, err := json.Marshal(e)
	if err != nil {
		log.Print("WARN: [usagelog] marshal: ", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := filepath.Join(l.dir, "usage-"+time.Now().Format(dayLayout)+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Print("WARN: [usagelog] open: ", err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Print("WARN: [usagelog] write: ", err)
	}
	if err := f.Close(); err != nil {
		log.Print("WARN: [usagelog] close: ", err)
	}
}

// Query 返回 [start,end) 内的观测，按时间升序。损坏行跳过（尽力读取），
// 文件缺失视为无数据。
func (l *Log) Query(start, end int64) ([]Entry, error) {
	if start < 0 || end < 0 || start >= end {
		return nil, errors.New("usagelog 查询区间无效")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entries := []Entry{}
	for day := time.Unix(start, 0); !day.After(time.Unix(end-1, 0)); day = day.AddDate(0, 0, 1) {
		data, err := os.ReadFile(filepath.Join(l.dir, "usage-"+day.Format(dayLayout)+".jsonl"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e Entry
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			if e.TS >= start && e.TS < end {
				entries = append(entries, e)
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS < entries[j].TS })
	return entries, nil
}

// Summarize 聚合观测；只累计已知值，缺失计入 Missing 计数而非按零处理。
func Summarize(entries []Entry) Summary {
	s := Summary{Calls: len(entries)}
	for _, e := range entries {
		if e.Prompt >= 0 {
			s.Prompt += int64(e.Prompt)
		} else {
			s.UsageMissing++
		}
		if e.Completion >= 0 {
			s.Completion += int64(e.Completion)
		}
		if e.Credit != nil {
			s.Credit += *e.Credit
		} else {
			s.CreditMissing++
		}
	}
	return s
}
