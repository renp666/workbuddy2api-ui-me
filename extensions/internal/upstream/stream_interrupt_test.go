package upstream

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// 补丁 0012a 回归：流中断必须显式暴露，半截流不再伪装成正常结束。

// TestStreamFinishReasonWithoutDoneTolerated 校验漏发哨兵豁免：内容已带非空
// finish_reason（语义完整）但 EOF 无 [DONE] → 仍兜底补恰好一个 [DONE]、返回 nil，
// 不误报中断（sawFinish 分支）。
func TestStreamFinishReasonWithoutDoneTolerated(t *testing.T) {
	raw := "data: {\"id\":\"x1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"x1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	rec := httptest.NewRecorder()
	if err := Stream(rec, strings.NewReader(raw)); err != nil {
		t.Fatalf("complete content without sentinel must stay success: %v", err)
	}
	body := rec.Body.String()
	if n := strings.Count(body, "data: [DONE]"); n != 1 {
		t.Errorf("[DONE] count=%d want 1: %q", n, body)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("unexpected error frame: %q", body)
	}
}

// TestStreamReadErrorInterrupts 校验非 EOF 读错误（空闲超时 cancel / 传输中断）
// 同样走中断收尾：写 error 帧、不补 [DONE]、返回原读错误。
func TestStreamReadErrorInterrupts(t *testing.T) {
	rec := httptest.NewRecorder()
	err := Stream(rec, &errAfterFrameReader{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("must return original read error, got %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("read-error stream must NOT fake [DONE]: %q", body)
	}
	if !strings.Contains(body, "upstream stream interrupted") {
		t.Errorf("missing interrupt error frame: %q", body)
	}
}

// errAfterFrameReader 先吐一帧有效数据、再以非 EOF 错误终止的 io.Reader。
type errAfterFrameReader struct{ sent bool }

func (r *errAfterFrameReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, "data: {\"id\":\"x1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		return n, nil
	}
	return 0, errors.New("boom")
}

// TestHasFinishReason 单元：非空 finish_reason=true；空串/null/缺失/坏 JSON=false。
func TestHasFinishReason(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, true},
		{`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, true},
		{`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`, false},
		{`{"choices":[{"index":0,"finish_reason":""}]}`, false},
		{`{"choices":[{"index":0,"finish_reason":null}]}`, false},
		{`{"choices":[]}`, false},
		{`{}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		if got := hasFinishReason(c.raw); got != c.want {
			t.Errorf("hasFinishReason(%q)=%v want %v", c.raw, got, c.want)
		}
	}
}
