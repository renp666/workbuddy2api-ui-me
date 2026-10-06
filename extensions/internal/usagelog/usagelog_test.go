package usagelog

import (
	"context"
	"encoding/json"
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
	}
	s := Summarize(entries)
	if s.Calls != 3 || s.Prompt != 10 || s.Completion != 5 {
		t.Fatalf("已知值聚合错误：%+v", s)
	}
	if s.Credit != 1.25 || s.CreditMissing != 1 || s.UsageMissing != 1 {
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
