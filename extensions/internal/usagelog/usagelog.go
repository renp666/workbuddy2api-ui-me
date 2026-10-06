// Package usagelog 按次记录成功模型调用的用量观测（账号、模型、token、积分扣费），
// 供管理台「调用统计」按时间范围查询。数据按天落 JSONL 到 core 数据目录的
// usage/ 子目录；credit 缺失时保持 nil——缺失≠0，不把未观测写成免费。
// 鉴权失败留痕落在同目录的 denied- 前缀文件里，与用量彻底分开：猜 key 的流量
// 不得进入「调用次数」。地址只存网段（IPv4 /24、IPv6 /64），主机位不写盘。
package usagelog

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"workbuddy2api/internal/auth"
)

// Entry 单次成功调用的用量观测。Prompt/Completion 为 -1 表示上游未回报 usage；
// Credit 为 nil 表示上游未回报 credit（显式 0 是合法免费观测，用指针区分）。
// Src/Client/API 是来源观测，空串表示未提供，页面按「—」展示，不补假值。
type Entry struct {
	TS         int64    `json:"ts"` // Unix 秒
	UID        string   `json:"uid"`
	Account    string   `json:"account"` // 昵称；空时回落 UID
	Model      string   `json:"model"`
	Mode       string   `json:"mode"` // "stream" | "sync"
	Prompt     int      `json:"prompt_tokens"`
	Completion int      `json:"completion_tokens"`
	Credit     *float64 `json:"credit,omitempty"`
	Src        string   `json:"src,omitempty"`    // 来源网段（已脱敏，不含主机位）
	Client     string   `json:"client,omitempty"` // User-Agent 清洗后的自报标识
	API        string   `json:"api,omitempty"`    // 协议入口："chat" | "messages" | "responses"
}

// Denied 一次鉴权失败留痕。只有来源观测，不带账号与模型：key 不对时
// core 并不知道这次尝试想冒充谁，硬填一个值就是伪造。
type Denied struct {
	TS     int64  `json:"ts"`
	Src    string `json:"src,omitempty"`
	Client string `json:"client,omitempty"`
}

// Summary 时间范围内的聚合。Prompt/Completion/Credit 只累计已知观测；
// Missing 按字段各自计数（输入回报了、输出没回报是真实存在的一种响应），
// 让展示层区分「0 消耗」与「未观测」，不把缺失当零。
type Summary struct {
	Calls             int     `json:"calls"`
	Prompt            int64   `json:"prompt_tokens"`
	Completion        int64   `json:"completion_tokens"`
	Credit            float64 `json:"credit"`
	CreditMissing     int     `json:"credit_missing"`
	PromptMissing     int     `json:"prompt_missing"`
	CompletionMissing int     `json:"completion_missing"`
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
func RecordAuth(acct *auth.Auth, model, mode string, prompt, completion int, credit float64, hasCredit bool, o Origin) {
	l := global.Load()
	if l == nil || acct == nil {
		return
	}
	e := Entry{TS: time.Now().Unix(), UID: acct.UID, Account: acct.Nickname, Model: model, Mode: mode, Prompt: prompt, Completion: completion, Src: o.Src, Client: o.Client, API: o.API}
	if e.Account == "" {
		e.Account = acct.UID
	}
	if hasCredit {
		e.Credit = &credit
	}
	l.Record(e)
}

// RecordDeniedAuth 记录一次鉴权失败留痕；未接线时 no-op。全部来源字段可为空，
// 空值照常落盘——「有一次尝试但来源未知」本身就是需要留痕的事实。
func RecordDeniedAuth(o Origin) {
	l := global.Load()
	if l == nil {
		return
	}
	l.RecordDenied(Denied{TS: time.Now().Unix(), Src: o.Src, Client: o.Client})
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
func (l *Log) Record(e Entry) { l.appendLine("usage-", e) }

// appendLine 按「写入当天」的日文件名追加一行 JSON。用量与鉴权失败留痕
// 共用这一条写入路径，只在文件名前缀上分开。
func (l *Log) appendLine(prefix string, v any) {
	line, err := json.Marshal(v)
	if err != nil {
		log.Print("WARN: [usagelog] marshal: ", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := filepath.Join(l.dir, prefix+time.Now().Format(dayLayout)+".jsonl")
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
	lines, err := l.scan("usage-", start, end)
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	for _, line := range lines {
		var e Entry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.TS >= start && e.TS < end {
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].TS < entries[j].TS })
	return entries, nil
}

// RecordDenied 追加一条鉴权失败留痕，与用量分文件存放。
func (l *Log) RecordDenied(d Denied) { l.appendLine("denied-", d) }

// QueryDenied 返回 [start,end) 内的鉴权失败留痕，按时间升序。日界与容错
// 规则和 Query 完全一致，只是读另一个前缀的文件。
func (l *Log) QueryDenied(start, end int64) ([]Denied, error) {
	lines, err := l.scan("denied-", start, end)
	if err != nil {
		return nil, err
	}
	items := []Denied{}
	for _, line := range lines {
		var d Denied
		if json.Unmarshal(line, &d) != nil {
			continue
		}
		if d.TS >= start && d.TS < end {
			items = append(items, d)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].TS < items[j].TS })
	return items, nil
}

// scan 读出 [start,end) 覆盖到的每个自然日文件里的非空 JSON 行。
// 用量与失败留痕共用这一条日界 walk 与读取路径，只在前缀上分开。
func (l *Log) scan(prefix string, start, end int64) ([][]byte, error) {
	if start < 0 || end < 0 || start >= end {
		return nil, errors.New("usagelog 查询区间无效")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var lines [][]byte
	for day := time.Unix(start, 0); !day.After(time.Unix(end-1, 0)); day = day.AddDate(0, 0, 1) {
		data, err := os.ReadFile(filepath.Join(l.dir, prefix+day.Format(dayLayout)+".jsonl"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, []byte(line))
			}
		}
	}
	return lines, nil
}

// DeniedSource 单个来源网段的鉴权失败次数；Src 为空表示来源未知。
type DeniedSource struct {
	Src   string `json:"src"`
	Calls int    `json:"calls"`
}

// DeniedStats 是鉴权失败概览，供管理台直接回答「这段时间有没有人在猜 key」。
// Sources 按次数降序、同次数按网段名升序（未知来源排在末位），最多 maxDeniedSources 组。
type DeniedStats struct {
	Calls   int            `json:"calls"`
	Sources []DeniedSource `json:"sources"`
}

// maxDeniedSources 限制展示用的来源组数：猜 key 的流量可以造出任意多个 /24，
// 页面只需要头部的少数几个。
const maxDeniedSources = 5

// SummarizeDenied 聚合鉴权失败留痕。次数按全部留痕统计，与 Sources 是否被截断无关。
func SummarizeDenied(items []Denied) DeniedStats {
	counts := map[string]int{}
	for _, d := range items {
		counts[d.Src]++
	}
	stats := DeniedStats{Calls: len(items), Sources: []DeniedSource{}}
	for src, calls := range counts {
		stats.Sources = append(stats.Sources, DeniedSource{Src: src, Calls: calls})
	}
	sort.Slice(stats.Sources, func(i, j int) bool {
		a, b := stats.Sources[i], stats.Sources[j]
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		if (a.Src == "") != (b.Src == "") {
			return b.Src == ""
		}
		return a.Src < b.Src
	})
	if len(stats.Sources) > maxDeniedSources {
		stats.Sources = stats.Sources[:maxDeniedSources]
	}
	return stats
}

// Summarize 聚合观测；只累计已知值，缺失计入 Missing 计数而非按零处理。
func Summarize(entries []Entry) Summary {
	s := Summary{Calls: len(entries)}
	for _, e := range entries {
		if e.Prompt >= 0 {
			s.Prompt += int64(e.Prompt)
		} else {
			s.PromptMissing++
		}
		if e.Completion >= 0 {
			s.Completion += int64(e.Completion)
		} else {
			s.CompletionMissing++
		}
		if e.Credit != nil {
			s.Credit += *e.Credit
		} else {
			s.CreditMissing++
		}
	}
	return s
}

// maxClientText 限制客户端自报文本（User-Agent 等）的落盘长度：
// 头内容由调用方控制，不设上限就能把每行账本撑大。
const maxClientText = 200

// MaskedPrefix 把对端地址收敛到所属网段：IPv4 取 /24，IPv6 取 /64，
// IPv4 映射地址按 IPv4 处理。解析不出地址返回空串表示「来源未知」，
// 不返回 0.0.0.0 之类的假值。带端口的写法一律视为解析失败，
// 注入方必须先剥端口。
func MaskedPrefix(addr string) string {
	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return net.IP([]byte{v4[0], v4[1], v4[2], 0}).String() + "/24"
	}
	var b [16]byte
	copy(b[:], ip.To16())
	for i := 8; i < 16; i++ {
		b[i] = 0
	}
	return net.IP(b[:]).String() + "/64"
}

// SanitizeClientText 清理客户端自报文本：控制字符压成空格、空白 run 折叠成单个空格
// 并去首尾（防止把换行写进一行 JSONL 的字段值里），再按字节上限截断。
// 截断点回退到字符边界，放不下完整字符时丢弃尾部残字节而不是劈坏它。
func SanitizeClientText(s string) string {
	s = strings.Trim(s, " \t\r\n\v\f")
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		b.WriteRune(r)
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	if len(s) <= maxClientText {
		return s
	}
	cut := maxClientText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Origin 是一次调用的来源观测，随 Entry 落账。
type Origin struct {
	Src    string
	Client string
	API    string
}

// clientHeader 由 console 在转发公开接口前覆写：console 会先剥掉入站同前缀头
// （见 console/proxy.go 的 stripPrivateHeaders），所以经 console 部署时该值不可由客户端伪造。
// 源码模式（不起 console 直接跑 core）下没有这层剥离，该值只能当客户端自报处理。
const clientHeader = "X-Console-Client"

type apiContextKey struct{}

// WithAPI 标注协议入口。core 的 /v1/messages 在进程内复用 OpenAI Handler，
// 到落账点时请求路径已被改写，只有适配层知道自己接的是 Messages，故由它显式标注。
func WithAPI(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, apiContextKey{}, name)
}

// APIFrom 读取协议入口标注；未标注返回空串。
func APIFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(apiContextKey{}).(string)
	return name
}

// apiByPath 把公开入口路径收敛为账本里的协议名。表外路径一律留空：
// 协议名会展示在统计页上，不接受任意文本。
var apiByPath = map[string]string{
	"/v1/chat/completions": "chat",
	"/v1/messages":         "messages",
	"/v1/responses":        "responses",
}

// APIOfPath 返回公开入口路径对应的协议名，表外路径为空。
func APIOfPath(path string) string { return apiByPath[path] }

// KnownAPI 把关外部传入的协议名（旁路通道回传），不在名单内一律留空。
// 名单只有 apiByPath 一处，展示层因此不会见到自造值。
func KnownAPI(name string) string {
	for _, api := range apiByPath {
		if api == name {
			return name
		}
	}
	return ""
}

// OriginOf 从请求里取出来源观测：网段脱敏与自报文本清洗都在这里收口，
// 调用点不得各自复制这套规则。协议入口优先取适配层标注（/v1/messages 到
// 落账点时路径已被改写），未标注时按请求路径判定。r 为 nil 时只回标注值。
func OriginOf(ctx context.Context, r *http.Request) Origin {
	o := Origin{API: APIFrom(ctx)}
	if r == nil {
		return o
	}
	o.Src = MaskedPrefix(r.Header.Get(clientHeader))
	o.Client = SanitizeClientText(r.Header.Get("User-Agent"))
	if o.API == "" {
		o.API = APIOfPath(r.URL.Path)
	}
	return o
}
