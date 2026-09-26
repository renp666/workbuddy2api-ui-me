package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/usagelog"
)

// newSnapshotReader 复用上游 SSE 解析路径构造观测：喂入整段流再提取快照。
func newSnapshotReader(t *testing.T, frames string) *chatStatsReader {
	t.Helper()
	r := newChatStatsReaderSince(strings.NewReader(frames), time.Now())
	for {
		if _, err := r.Read(make([]byte, 64*1024)); err != nil {
			break
		}
	}
	return r
}

// TestUsageSnapshotStreamFrames 校验流式末帧观测的提取语义：
// usage 缺失为 -1 哨兵；credit 缺失≠0；显式 credit:0 是合法免费观测。
func TestUsageSnapshotStreamFrames(t *testing.T) {
	r := newSnapshotReader(t, sseWithCredit)
	prompt, completion, credit, hasCredit := r.usageSnapshot()
	if prompt != 1000 || completion != 1000 || !hasCredit || credit != 2.0 {
		t.Fatalf("带 credit 的末帧解析错误：%d %d %v %v", prompt, completion, credit, hasCredit)
	}

	r = newSnapshotReader(t, sseUsageNoCredit)
	prompt, completion, credit, hasCredit = r.usageSnapshot()
	if prompt != 100 || completion != 100 || hasCredit {
		t.Fatalf("缺 credit 时不得视为已观测：%d %d %v", prompt, completion, hasCredit)
	}

	r = newSnapshotReader(t, sseFree)
	prompt, completion, credit, hasCredit = r.usageSnapshot()
	if prompt != 500 || completion != 500 || !hasCredit || credit != 0 {
		t.Fatalf("显式 credit:0 必须保留：%d %d %v %v", prompt, completion, credit, hasCredit)
	}

	r = newSnapshotReader(t, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	prompt, completion, _, hasCredit = r.usageSnapshot()
	if prompt != -1 || completion != -1 || hasCredit {
		t.Fatalf("无 usage 返回 -1 哨兵：%d %d %v", prompt, completion, hasCredit)
	}
}

// TestHandlerRecordsUsageLogForStreamAndSync (RED→GREEN)：handler 成功路径接上
// usagelog 后，每次成功调用恰好落一条观测（流式/同步各一），账号、模型、token
// 与 credit 数值如实入账；失败与未接线场景不产生记录。
func TestHandlerRecordsUsageLogForStreamAndSync(t *testing.T) {
	dir := t.TempDir()
	l, err := usagelog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	usagelog.Set(l)
	t.Cleanup(func() { usagelog.Set(nil) })

	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseWithCredit, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "u9", Nickname: "统计号", AccessToken: "at-u9", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[],"stream":true}`)))
	if rec.Code != 200 {
		t.Fatalf("流式请求失败: code=%d body=%s", rec.Code, rec.Body.String())
	}

	items, err := l.Query(time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("流式成功应恰好落一条观测，得到 %d：%v", len(items), items)
	}
	e := items[0]
	if e.UID != "u9" || e.Account != "统计号" || e.Model != "m" || e.Mode != "stream" {
		t.Fatalf("账号/模型/方式不符：%+v", e)
	}
	if e.Prompt != 1000 || e.Completion != 1000 || e.Credit == nil || *e.Credit != 2.0 {
		t.Fatalf("token 与 credit 数值不符：%+v", e)
	}

	// 同步路径：同一 fake 上游（恒为 SSE），非流式请求经 Aggregate 后提取 usage。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[],"stream":false}`)))
	if rec.Code != 200 {
		t.Fatalf("同步请求失败: code=%d body=%s", rec.Code, rec.Body.String())
	}
	items, err = l.Query(time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("同步成功应再落一条观测，得到 %d：%v", len(items), items)
	}
	if e = items[1]; e.Mode != "sync" || e.Prompt != 1000 || e.Completion != 1000 || e.Credit == nil || *e.Credit != 2.0 {
		t.Fatalf("同步观测数值不符：%+v", e)
	}
}
