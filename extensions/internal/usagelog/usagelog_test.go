package usagelog

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"workbuddy2api/internal/auth"
)

func creditPtr(v float64) *float64 { return &v }

// writeDayFile 直接落一份受控 JSONL，绕开 Record 的「写入时刻」文件选择。
func writeDayFile(t *testing.T, dir string, day time.Time, lines ...string) {
	t.Helper()
	name := filepath.Join(dir, "usage-"+day.Format(dayLayout)+".jsonl")
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQueryFiltersByRangeAndSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)
	withCredit, _ := json.Marshal(Entry{TS: today.Unix(), UID: "u1", Account: "甲", Model: "m", Mode: "stream", Prompt: 10, Completion: 20, Credit: creditPtr(1.5)})
	noCredit, _ := json.Marshal(Entry{TS: today.Unix() + 1, UID: "u2", Model: "m", Mode: "sync", Prompt: -1, Completion: -1})
	atEnd, _ := json.Marshal(Entry{TS: today.Unix() + 60, UID: "u9", Model: "m", Mode: "sync", Prompt: 0, Completion: 0})
	old, _ := json.Marshal(Entry{TS: yesterday.Unix(), UID: "u1", Model: "m", Mode: "sync", Prompt: 5, Completion: 5})
	writeDayFile(t, dir, today, string(withCredit), string(noCredit), "{corrupt", string(atEnd))
	writeDayFile(t, dir, yesterday, string(old))

	items, err := l.Query(today.Unix()-60, today.Unix()+60)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("期望当天 2 条（损坏行与 end 边界行排除），得到 %d：%v", len(items), items)
	}
	if items[0].UID != "u1" || items[0].Credit == nil || *items[0].Credit != 1.5 {
		t.Errorf("首条应为带 credit 的 u1：%v", items[0])
	}
	if items[1].Credit != nil || items[1].Prompt != -1 {
		t.Errorf("无 credit/usage 的观测不得伪造：%v", items[1])
	}
	// 覆盖昨天的区间能读到旧文件。
	items, _ = l.Query(yesterday.Unix()-60, today.Unix()+60)
	if len(items) != 3 {
		t.Fatalf("跨天查询期望 3 条，得到 %d", len(items))
	}
	if _, err := l.Query(100, 100); err == nil {
		t.Fatal("start>=end 应报错")
	}
}

func TestRecordAuthWiresNicknameFallbackAndExplicitZeroCredit(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	Set(nil)
	RecordAuth(&auth.Auth{UID: "u1", Nickname: "甲"}, "m", "stream", 1, 2, 3, true, Origin{}) // 未接线 no-op
	Set(l)
	RecordAuth(&auth.Auth{UID: "u1", Nickname: "甲"}, "cn:m", "stream", 100, 200, 1.5, true, Origin{Src: "203.0.113.0/24", Client: "claude-cli/1.2.3", API: "messages"})
	RecordAuth(&auth.Auth{UID: "u2"}, "cn:m", "sync", 10, 20, 0, true, Origin{})                  // 显式 0 是合法免费观测
	RecordAuth(&auth.Auth{UID: "u3"}, "cn:m", "sync", -1, -1, 9, false, Origin{Client: "curl/8"}) // 无 credit 观测照常落盘，但 credit 保持缺失
	RecordAuth(nil, "cn:m", "sync", 1, 1, 1, true, Origin{})

	items, err := l.Query(time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("期望 3 条（nil 账号与未观测 credit 不落盘），得到 %d：%v", len(items), items)
	}
	if items[0].Account != "甲" || items[0].UID != "u1" {
		t.Errorf("昵称应优先：%v", items[0])
	}
	if items[0].Src != "203.0.113.0/24" || items[0].Client != "claude-cli/1.2.3" || items[0].API != "messages" {
		t.Errorf("来源观测应随 Entry 落账：%+v", items[0])
	}
	if items[1].Src != "" || items[1].Client != "" || items[1].API != "" {
		t.Errorf("未提供的来源不得被补成任何值：%+v", items[1])
	}
	if items[1].Account != "u2" || items[1].Credit == nil || *items[1].Credit != 0 {
		t.Errorf("昵称缺省回落 UID，显式 0 credit 必须保留：%v", items[1])
	}
	if items[2].Credit != nil {
		t.Errorf("缺失 credit 不得写成 0：%v", items[2])
	}
	if items[2].Src != "" || items[2].Client != "curl/8" {
		t.Errorf("只给了客户端标识时来源应保持未知：%+v", items[2])
	}
	// omitempty 必须真的省键：老行缺字段与新行留空走同一条读取路径。
	lines := strings.Split(strings.TrimSpace(string(readDayFile(t, dir))), "\n")
	if strings.Contains(lines[1], `"src"`) || strings.Contains(lines[1], `"api"`) {
		t.Errorf("空来源仍被写进 JSONL，行体积会无谓膨胀：%s", lines[1])
	}
	if !strings.Contains(lines[2], `"client":"curl/8"`) || strings.Contains(lines[2], `"src"`) {
		t.Errorf("只应落有值的来源键：%s", lines[2])
	}
}

func readDayFile(t *testing.T, dir string) []byte {
	t.Helper()
	name := filepath.Join(dir, "usage-"+time.Now().Format(dayLayout)+".jsonl")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFromResponseDistinguishesMissingFromZero(t *testing.T) {
	full := map[string]any{"usage": map[string]any{"prompt_tokens": 100.0, "completion_tokens": 50.0, "credit": 2.5}}
	prompt, completion, credit, hasCredit := FromResponse(full)
	if prompt != 100 || completion != 50 || !hasCredit || credit != 2.5 {
		t.Fatalf("完整 usage 解析错误：%d %d %v %v", prompt, completion, credit, hasCredit)
	}
	free := map[string]any{"usage": map[string]any{"prompt_tokens": 1.0, "completion_tokens": 1.0, "credit": 0.0}}
	if _, _, credit, hasCredit = FromResponse(free); !hasCredit || credit != 0 {
		t.Fatalf("显式 credit:0 是合法免费观测：%v %v", credit, hasCredit)
	}
	noCredit := map[string]any{"usage": map[string]any{"prompt_tokens": 1.0, "completion_tokens": 1.0}}
	if prompt, completion, _, hasCredit = FromResponse(noCredit); hasCredit || prompt != 1 || completion != 1 {
		t.Fatalf("缺 credit 时 hasCredit=false，token 保留：%d %d %v", prompt, completion, hasCredit)
	}
	noUsage := map[string]any{"choices": []any{}}
	if prompt, completion, _, hasCredit = FromResponse(noUsage); hasCredit || prompt != -1 || completion != -1 {
		t.Fatalf("无 usage 返回 -1 哨兵：%d %d %v", prompt, completion, hasCredit)
	}
}

func TestSummarizeKeepsMissingApartFromZero(t *testing.T) {
	entries := []Entry{
		{Prompt: 10, Completion: 5, Credit: creditPtr(1.25)},
		{Prompt: -1, Completion: -1, Credit: creditPtr(0)}, // 免费 + usage 缺失
		{Prompt: 0, Completion: 0},                         // 无 credit 观测
		{Prompt: 7, Completion: -1},                        // 只回报输入：输出缺失不得静悄悄并入 0
	}
	s := Summarize(entries)
	if s.Calls != 4 || s.Prompt != 17 || s.Completion != 5 {
		t.Fatalf("已知值聚合错误：%+v", s)
	}
	if s.PromptMissing != 1 || s.CompletionMissing != 2 {
		t.Fatalf("输入与输出的缺失必须各自计数，否则「已观测」口径会少报：%+v", s)
	}
	if s.Credit != 1.25 || s.CreditMissing != 2 {
		t.Fatalf("缺失计数不得并入零值：%+v", s)
	}
	if math.IsNaN(s.Credit) || math.IsInf(s.Credit, 0) {
		t.Fatalf("credit 聚合出现非法值：%v", s.Credit)
	}
}

func TestMaskedPrefixKeepsNetblockAndDropsHostPart(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.42", "203.0.113.0/24"},
		{"10.0.0.7", "10.0.0.0/24"},
		{"::ffff:203.0.113.42", "203.0.113.0/24"},
		{"2001:db8:3333:4444:5555:6666:7777:8888", "2001:db8:3333:4444::/64"},
		{"fe80::1", "fe80::/64"},
		{"", ""},
		{"not-an-ip", ""},
		{"203.0.113.42:9", ""},
		{"203.0.113", ""},
	}
	for _, c := range cases {
		if got := MaskedPrefix(c.in); got != c.want {
			t.Errorf("MaskedPrefix(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeClientTextCapsLengthWithoutSplittingRune(t *testing.T) {
	if got := SanitizeClientText("  claude-cli/1.2.3 \n"); got != "claude-cli/1.2.3" {
		t.Fatalf("首尾空白与控制符应被清掉，得到 %q", got)
	}
	long := "x" + strings.Repeat("y", maxClientText-1)
	if got := SanitizeClientText(long); len(got) != maxClientText {
		t.Fatalf("纯 ASCII 应截到上限，得到 %d", len(got))
	}
	multi := strings.Repeat("码", maxClientText)
	got := SanitizeClientText(multi)
	if len(got) > maxClientText || !utf8.ValidString(got) {
		t.Fatalf("按字节截断把多字节字符劈坏了：%d 字节 valid=%v", len(got), utf8.ValidString(got))
	}
	if SanitizeClientText(strings.Repeat("a", maxClientText-1)+"码") != strings.Repeat("a", maxClientText-1) {
		t.Fatalf("放不下完整字符时应丢弃尾部残字节")
	}
}

func TestOriginOfMasksInjectedClientAndCapsUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Console-Client", "203.0.113.42")
	r.Header.Set("User-Agent", "claude-cli/1.2.3 (external, cli)")
	o := OriginOf(r.Context(), r)
	if o.Src != "203.0.113.0/24" || o.Client != "claude-cli/1.2.3 (external, cli)" {
		t.Fatalf("来源应脱敏到网段、客户端原样保留（清洗后），得到 %+v", o)
	}
	if o.API != "chat" {
		t.Fatalf("未标注协议入口时应按公开路径判定，得到 %q", o.API)
	}

	offList := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if got := OriginOf(offList.Context(), offList); got.API != "" {
		t.Fatalf("表外路径不得被标成任何协议名：%q", got.API)
	}

	hostile := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	hostile.Header.Set("X-Console-Client", "203.0.113.42:9999")
	hostile.Header.Set("User-Agent", "a\r\nb")
	h := OriginOf(hostile.Context(), hostile)
	if h.Src != "" {
		t.Fatalf("带端口的来源值应视为未知，不得当成地址落账：%q", h.Src)
	}
	if h.Client != "a b" {
		t.Fatalf("控制字符应被压成空格：%q", h.Client)
	}

	bare := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if got := OriginOf(bare.Context(), bare); got.Src != "" || got.Client != "" {
		t.Fatalf("缺少注入头与 UA 时应双双留空（页面显示「—」），得到 %+v", got)
	}
	if got := OriginOf(WithAPI(context.Background(), "messages"), nil); got.API != "messages" || got.Src != "" {
		t.Fatalf("协议入口应能独立标注且不受请求缺失影响：%+v", got)
	}
	// 适配层标注必须压过路径判定：/v1/messages 到落账点时路径已被改写成
	// /v1/chat/completions，只按路径判会把 Anthropic 入口记成 chat。
	if got := OriginOf(WithAPI(context.Background(), "messages"), bare); got.API != "messages" {
		t.Fatalf("已标注的协议入口应压过路径判定：%q", got.API)
	}
}

// TestOriginOfReadsRequestID 锁定出口请求 ID 的读取与清洗：console 注入的内部头
// 原样入账；缺失留空；含控制字符/非 ASCII 的值整体不留（ID 是排障关联键，宁可
// 缺席不可留半截混合物）。
func TestOriginOfReadsRequestID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Console-Request-Id", "0123456789abcdef0123456789abcdef")
	if got := OriginOf(r.Context(), r); got.RequestID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("内部头应原样入账：%q", got.RequestID)
	}

	empty := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if got := OriginOf(empty.Context(), empty); got.RequestID != "" {
		t.Fatalf("缺头应留空：%q", got.RequestID)
	}

	hostile := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	hostile.Header.Set("X-Console-Request-Id", "abc\nlog-injection")
	if got := OriginOf(hostile.Context(), hostile); got.RequestID != "" {
		t.Fatalf("含控制字符的 ID 应整体不留：%q", got.RequestID)
	}

	nonASCII := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	nonASCII.Header.Set("X-Console-Request-Id", "id-含中文")
	if got := OriginOf(nonASCII.Context(), nonASCII); got.RequestID != "" {
		t.Fatalf("非 ASCII 的 ID 应整体不留：%q", got.RequestID)
	}
}

// TestSanitizeRequestID 锁定清洗函数本身：去首尾空白、超限截断、合法字符集。
func TestSanitizeRequestID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  abc  ", "abc"},
		{strings.Repeat("a", maxRequestID+10), strings.Repeat("a", maxRequestID)},
		{"ok-id_09", "ok-id_09"},
		{"with space", ""}, // ID 内空白非法：整体不留
		{"tab\there", ""},
	}
	for _, tc := range cases {
		if got := SanitizeRequestID(tc.in); got != tc.want {
			t.Fatalf("SanitizeRequestID(%q)=%q，want %q", tc.in, got, tc.want)
		}
	}
}

// TestOriginOfReadsKeyLabel 锁定 Key 归属标签的入账路径：console 换发时注入的
// 内部头进 Origin；缺头留空；不合规形态（控制字符/非 ASCII/空白）整体不留。
func TestOriginOfReadsKeyLabel(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("X-Console-Key-Label", "ci-bot")
	if got := OriginOf(r.Context(), r); got.KeyLabel != "ci-bot" {
		t.Fatalf("内部头应原样入账：%q", got.KeyLabel)
	}

	empty := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if got := OriginOf(empty.Context(), empty); got.KeyLabel != "" {
		t.Fatalf("缺头应留空：%q", got.KeyLabel)
	}

	hostile := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	hostile.Header.Set("X-Console-Key-Label", "a\tb")
	if got := OriginOf(hostile.Context(), hostile); got.KeyLabel != "" {
		t.Fatalf("含空白的标签应整体不留：%q", got.KeyLabel)
	}

	nonASCII := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	nonASCII.Header.Set("X-Console-Key-Label", "标签")
	if got := OriginOf(nonASCII.Context(), nonASCII); got.KeyLabel != "" {
		t.Fatalf("非 ASCII 的标签应整体不留：%q", got.KeyLabel)
	}
}

// TestSanitizeKeyLabel 锁定清洗函数本身：与 SanitizeRequestID 同族口径，
// 上限 64 字节。「默认」是 console 主 Key 的保留标签，落账侧原样透传不做特判
//（保留约束由 console 创建入口 enforce，core 不重复实现；中文标签在清洗层
// 因非 ASCII 整体丢弃，console 创建入口同样只收 ASCII，口径闭环）。
func TestSanitizeKeyLabel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"  ci-bot  ", "ci-bot"},
		{strings.Repeat("k", maxKeyLabel+10), strings.Repeat("k", maxKeyLabel)},
		{"默认", ""},
		{"with space", ""},
		{"a\nb", ""},
		{"tag-01_x", "tag-01_x"},
	} {
		if got := SanitizeKeyLabel(tc.in); got != tc.want {
			t.Fatalf("SanitizeKeyLabel(%q)=%q，want %q", tc.in, got, tc.want)
		}
	}
}

func TestDeniedRecordsStayOutOfUsageQueries(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start, end := now.Add(-time.Minute).Unix(), now.Add(time.Minute).Unix()

	Set(nil)
	RecordDeniedAuth(Origin{Src: "203.0.113.0/24"}) // 未接线 no-op
	Set(l)
	RecordDeniedAuth(Origin{Src: "203.0.113.0/24", Client: "curl/8"})
	RecordDeniedAuth(Origin{}) // 来源与客户端都不知道，仍要留下这一次尝试

	used, err := l.Query(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 0 {
		t.Fatalf("鉴权失败不得进入用量查询，否则「调用次数」会被猜 key 的流量灌高：%+v", used)
	}
	denied, err := l.QueryDenied(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 2 {
		t.Fatalf("两次失败尝试都应留痕（含来源未知的那次），得到 %d：%+v", len(denied), denied)
	}
	if denied[0].Src != "203.0.113.0/24" || denied[0].Client != "curl/8" {
		t.Errorf("失败留痕应带上来源网段与客户端：%+v", denied[0])
	}
	if denied[1].TS == 0 {
		t.Errorf("留痕必须带时刻，供按天聚合：%+v", denied[1])
	}
	if raw := readDeniedFile(t, dir); strings.Contains(string(raw), `"203.0.113.42"`) {
		t.Fatalf("完整主机地址绝不能落盘：%s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "denied-"+now.Format(dayLayout)+".jsonl")); err != nil {
		t.Fatalf("失败留痕应与用量分文件存放：%v", err)
	}
}

// TestSummarizeDeniedGroupsSourcesDeterministically：鉴权失败要能被页面直接回答
// 「有多少次、来自哪些网段」；同次数按网段名升序，避免每次刷新顺序跳动；
// 来源未知的尝试单独成组，不并进任何网段。
func TestSummarizeDeniedGroupsSourcesDeterministically(t *testing.T) {
	if got := SummarizeDenied(nil); got.Calls != 0 || len(got.Sources) != 0 {
		t.Fatalf("无留痕应给出零值：%+v", got)
	}
	items := []Denied{
		{TS: 1, Src: "10.0.0.0/24"}, {TS: 2, Src: "203.0.113.0/24"}, {TS: 3, Src: "203.0.113.0/24"},
		{TS: 4}, {TS: 5}, {TS: 6, Src: "192.168.1.0/24"}, {TS: 7, Src: "172.16.0.0/24"}, {TS: 8, Src: "10.10.0.0/24"},
	}
	got := SummarizeDenied(items)
	if got.Calls != 8 {
		t.Fatalf("失败次数=%d，期望 8", got.Calls)
	}
	if len(got.Sources) != maxDeniedSources {
		t.Fatalf("来源分组应被限流到 %d 组，得到 %d：%+v", maxDeniedSources, len(got.Sources), got.Sources)
	}
	if got.Sources[0].Src != "203.0.113.0/24" || got.Sources[0].Calls != 2 {
		t.Errorf("次数多的来源应排在最前：%+v", got.Sources[0])
	}
	if got.Sources[1].Src != "" || got.Sources[1].Calls != 2 {
		t.Errorf("来源未知同样成组，但在同次数里排在已知网段之后：%+v", got.Sources[1])
	}
	for i := 1; i < len(got.Sources); i++ {
		prev, cur := got.Sources[i-1], got.Sources[i]
		if prev.Calls < cur.Calls {
			t.Fatalf("未按次数降序：%+v", got.Sources)
		}
		if prev.Calls == cur.Calls && cur.Src != "" && prev.Src > cur.Src {
			t.Fatalf("同次数需按网段名升序才稳定：%+v", got.Sources)
		}
	}
}

func readDeniedFile(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "denied-"+time.Now().Format(dayLayout)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// noonOf 把时刻收敛到那个自然日的正午，测试因此不会踩在日界上。
func noonOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, t.Location())
}

func dateOf(t time.Time) string { return t.Format("2006-01-02") }

// writeRaw 按受控内容覆写某一天的明细文件，绕开 Record 的「写入时刻」选择。
func writeRaw(t *testing.T, dir, prefix string, day time.Time, lines ...string) {
	t.Helper()
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, prefix+day.Format(dayLayout)+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func entryLine(t *testing.T, e Entry) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDailyRollupRebuildsOnlyWhenDetailSizeChanges(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().AddDate(0, 0, -3)
	first := Entry{TS: noonOf(day).Unix(), UID: "u1", Account: "甲", Model: "cn:a", Mode: "sync", Prompt: 10, Completion: 5}
	line := entryLine(t, first)
	writeRaw(t, dir, "usage-", day, line)

	days, err := l.AllDaily()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("期望 1 个自然日汇总，得到 %d：%+v", len(days), days)
	}
	got := days[0]
	if got.Date != dateOf(day) || got.Calls != 1 || got.Prompt != 10 || got.Completion != 5 {
		t.Fatalf("日度汇总读数不符：%+v", got)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "cn:a" || got.Models[0].Calls != 1 || got.Models[0].Prompt != 10 {
		t.Fatalf("按模型分解不符：%+v", got.Models)
	}
	if _, err := os.Stat(filepath.Join(dir, "daily-"+day.Format(dayLayout)+".json")); err != nil {
		t.Fatalf("日度汇总必须落盘：%v", err)
	}

	// 字节数没变就不许重算：把明细换成等长的坏行，重算会得到 Calls=0，
	// 而缓存必须保住上一次的真实读数。
	writeRaw(t, dir, "usage-", day, strings.Repeat("x", len(line)))
	days, err = l.AllDaily()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Calls != 1 {
		t.Fatalf("明细未变化时不得重算（等长坏行不该被读进来）：%+v", days)
	}

	// 字节数变了就必须重算。
	second := Entry{TS: noonOf(day).Unix() + 60, UID: "u2", Account: "乙", Model: "cn:b", Mode: "stream", Prompt: 7, Completion: 3}
	writeRaw(t, dir, "usage-", day, line, entryLine(t, second))
	days, err = l.AllDaily()
	if err != nil {
		t.Fatal(err)
	}
	if days[0].Calls != 2 || days[0].Prompt != 17 || days[0].Completion != 8 {
		t.Fatalf("明细追加后必须重算：%+v", days[0])
	}
	// 排序口径：调用次数降序，同次数按模型名升序，让同一份数据永远画出同一张图。
	if days[0].Models[0].Model != "cn:a" || days[0].Models[1].Model != "cn:b" {
		t.Fatalf("模型排序不符：%+v", days[0].Models)
	}
	// 日度文件绝不能被明细查询读到，否则「调用明细」里会冒出汇总行。
	if items, err := l.Query(noonOf(day).Unix(), noonOf(day).AddDate(0, 0, 1).Unix()); err != nil || len(items) != 2 {
		t.Fatalf("明细查询被日度文件污染：%d 条 err=%v", len(items), err)
	}
}

func TestDailyRollupKeepsMissingApartFromZeroAndCountsDenied(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().AddDate(0, 0, -1)
	zero := 0.0
	dark := Entry{TS: noonOf(day).Unix(), UID: "u1", Model: "cn:a", Mode: "sync", Prompt: -1, Completion: -1}
	free := Entry{TS: noonOf(day).Unix() + 1, UID: "u2", Model: "cn:a", Mode: "sync", Prompt: -1, Completion: -1, Credit: &zero}
	writeRaw(t, dir, "usage-", day, entryLine(t, dark), entryLine(t, free))
	denied := Denied{TS: noonOf(day).Unix(), Src: "203.0.113.0/24"}
	bad, _ := json.Marshal(denied)
	writeRaw(t, dir, "denied-", day, string(bad), "{corrupt")

	days, err := l.AllDaily()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("期望 1 天，得到 %d：%+v", len(days), days)
	}
	got := days[0]
	if got.Calls != 2 || got.Prompt != 0 || got.Completion != 0 {
		t.Fatalf("未回报的 token 不得凭空产生数值：%+v", got)
	}
	if got.PromptMissing != 2 || got.CompletionMissing != 2 {
		t.Fatalf("输入/输出缺失要各自计数：%+v", got)
	}
	if got.Credit != 0 || got.CreditMissing != 1 {
		t.Fatalf("显式 0 与未观测必须区分（合计 0、缺 1 条）：%+v", got)
	}
	if got.Denied != 1 {
		t.Fatalf("鉴权失败按天留数、坏行跳过，得到 %d：%+v", got.Denied, got)
	}
	if len(got.Models) != 1 || got.Models[0].Calls != 2 || got.Models[0].PromptMissing != 2 {
		t.Fatalf("模型分解必须带上各自的缺失计数：%+v", got.Models)
	}
}

func TestSummarizeLifetimeStreaksAndPeak(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, loc) // 今天还没有调用
	mk := func(offsetDays int, calls int, prompt, completion int64) Daily {
		day := noonOf(now).AddDate(0, 0, offsetDays)
		return Daily{Date: dateOf(day), Calls: calls, Prompt: prompt, Completion: completion,
			PromptMissing: 0, CompletionMissing: 0, CreditMissing: 0}
	}
	days := []Daily{
		mk(-11, 1, 5, 5), mk(-10, 1, 5, 5), // 早期一段 2 天连续
		mk(-9, 1, 900, 100),                               // 峰值日
		mk(-3, 1, 1, 1), mk(-2, 1, 1, 1), mk(-1, 1, 1, 1), // 昨天收尾的 3 天连续
	}
	got := SummarizeLifetime(days, now)
	if got.Calls != 6 || got.Prompt != 913 || got.Completion != 113 {
		t.Fatalf("全历史累计不符：%+v", got)
	}
	if got.Days != 6 || got.FirstDate != dateOf(noonOf(now).AddDate(0, 0, -11)) || got.LastDate != dateOf(noonOf(now).AddDate(0, 0, -1)) {
		t.Fatalf("活跃天数与首末日期不符：%+v", got)
	}
	if got.CurrentStreak != 3 {
		t.Fatalf("今天没有调用时，连续天数应停在昨天：得到 %d", got.CurrentStreak)
	}
	if got.LongestStreak != 3 {
		t.Fatalf("最长连续应为 -3..-1 的 3 天：得到 %d", got.LongestStreak)
	}
	if got.PeakDate != dateOf(noonOf(now).AddDate(0, 0, -9)) || got.PeakTokens != 1000 {
		t.Fatalf("峰值日读数不符：%+v", got)
	}

	// 今天有调用就要把今天算进连续段。
	today := mk(0, 2, 3, 4)
	got = SummarizeLifetime(append(append([]Daily{}, days...), today), now)
	if got.CurrentStreak != 4 {
		t.Fatalf("今天的调用应接上昨天那段：%+v", got)
	}
	// 今天和昨天都没有 → 连续段断了。
	got = SummarizeLifetime([]Daily{mk(-3, 1, 1, 1), mk(-2, 1, 1, 1)}, now)
	if got.CurrentStreak != 0 || got.LongestStreak != 2 {
		t.Fatalf("断档后当前连续必须归零、最长保留：%+v", got)
	}
	// 峰值取更大那天；并列时取更早那天，保证同一份数据永远指出同一个峰值日。
	if g := SummarizeLifetime([]Daily{mk(-2, 1, 10, 10), mk(-1, 1, 20, 10)}, now); g.PeakDate != dateOf(noonOf(now).AddDate(0, 0, -1)) || g.PeakTokens != 30 {
		t.Fatalf("峰值应取 token 更大的那天：%+v", g)
	}
	if g := SummarizeLifetime([]Daily{mk(-2, 1, 20, 0), mk(-1, 1, 10, 10)}, now); g.PeakDate != dateOf(noonOf(now).AddDate(0, 0, -2)) {
		t.Fatalf("并列峰值日应取更早那天：%+v", g)
	}
	// 有调用但整组未观测：数值是 0，读数必须是「未观测」而不是「0 消耗」。
	dark := mk(-1, 3, 0, 0)
	dark.PromptMissing, dark.CompletionMissing = 3, 3
	if g := SummarizeLifetime([]Daily{dark}, now); g.PeakTokens != 0 || !g.PeakTokensMissing {
		t.Fatalf("整组未观测的峰值日不得读成 0 token：%+v", g)
	}
	if g := SummarizeLifetime([]Daily{dark}, now); g.Tokens != 0 || !g.TokensMissing {
		t.Fatalf("全历史一条 token 观测都没有时累计读数也必须区分未观测：%+v", g)
	}
	// 只要有任何一条回报，累计就不再是「未观测」，哪怕另一天整组缺失。
	if g := SummarizeLifetime([]Daily{dark, mk(-2, 1, 7, 7)}, now); g.Tokens != 14 || g.TokensMissing {
		t.Fatalf("部分回报的累计不得被读成未观测：%+v", g)
	}
	if g := SummarizeLifetime([]Daily{mk(-1, 1, 0, 0)}, now); g.Tokens != 0 || g.TokensMissing {
		t.Fatalf("回报了 0 就是观测到 0，不是未观测：%+v", g)
	}
	// 空账本不得编出任何日期或天数。
	if g := SummarizeLifetime(nil, now); g.Days != 0 || g.CurrentStreak != 0 || g.LongestStreak != 0 || g.PeakDate != "" || g.FirstDate != "" {
		t.Fatalf("空账本读数必须全零：%+v", g)
	}
}

func TestPruneDetailsDropsOldFilesOnlyAfterRollupIsSafe(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := noonOf(now).AddDate(0, 0, -90)
	kept := noonOf(now).AddDate(0, 0, -89)
	today := noonOf(now)
	for _, day := range []time.Time{old, kept, today} {
		e := Entry{TS: day.Unix(), UID: "u1", Account: "甲", Model: "cn:a", Mode: "sync", Prompt: 10, Completion: 5}
		writeRaw(t, dir, "usage-", day, entryLine(t, e))
		d, _ := json.Marshal(Denied{TS: day.Unix(), Src: "203.0.113.0/24"})
		writeRaw(t, dir, "denied-", day, string(d))
	}
	removed, err := l.PruneDetails(90, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("只应裁掉 -90 那一天，得到 %d", removed)
	}
	for _, day := range []time.Time{kept, today} {
		if _, err := os.Stat(filepath.Join(dir, "usage-"+day.Format(dayLayout)+".jsonl")); err != nil {
			t.Fatalf("保留窗口内（-%d 天）的明细不得被删：%v", int(noonOf(now).Sub(day).Hours()/24), err)
		}
	}
	for _, prefix := range []string{"usage-", "denied-"} {
		path := filepath.Join(dir, prefix+old.Format(dayLayout)+".jsonl")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s 旧明细应已删除，stat=%v", prefix, err)
		}
	}
	// 裁掉明细后，日度汇总必须还在，并且带着被裁那天用量与失败次数。
	days, err := l.AllDaily()
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 {
		t.Fatalf("三个自然日的日度汇总都应存在：%+v", days)
	}
	if days[0].Date != dateOf(old) || days[0].Calls != 1 || days[0].Prompt != 10 || days[0].Denied != 1 {
		t.Fatalf("被裁明细那天的日度读数必须原样保留：%+v", days[0])
	}
	// 再裁一次不得重复计数，也不得动日度文件。
	again, err := l.PruneDetails(90, now)
	if err != nil || again != 0 {
		t.Fatalf("裁剪必须幂等，得到 %d err=%v", again, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "daily-"+old.Format(dayLayout)+".json")); err != nil {
		t.Fatalf("日度汇总永久保留：%v", err)
	}
	// 全历史口径不依赖明细文件。
	stats := SummarizeLifetime(days, now)
	if stats.Calls != 3 || stats.Prompt != 30 {
		t.Fatalf("明细裁掉后累计读数不得缩水：%+v", stats)
	}
}
