package upstream

import (
	"bytes"
	"strings"
	"testing"
)

func TestRewriteModelInSSERewritesModelField(t *testing.T) {
	in := "data: {\"model\":\"upstream-internal\",\"choices\":[]}\n\n" +
		"data: {\"model\":\"upstream-internal\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var out bytes.Buffer
	if err := RewriteModelInSSE(&out, strings.NewReader(in), "raccoon-chat-ml-5-5"); err != nil {
		t.Fatalf("RewriteModelInSSE: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "upstream-internal") {
		t.Fatalf("upstream model name leaked: %s", got)
	}
	if n := strings.Count(got, `"model":"raccoon-chat-ml-5-5"`); n != 2 {
		t.Fatalf("model rewritten %d time(s), want 2: %s", n, got)
	}
	if !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("payload content must survive verbatim: %s", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Fatalf("sentinel must survive: %s", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("trailing blank line must survive: %q", got)
	}
}

func TestRewriteModelInSSEPassesThroughWhenModelEmpty(t *testing.T) {
	in := "data: {\"model\":\"upstream-internal\"}\n"
	var out bytes.Buffer
	if err := RewriteModelInSSE(&out, strings.NewReader(in), ""); err != nil {
		t.Fatalf("RewriteModelInSSE: %v", err)
	}
	if out.String() != in {
		t.Fatalf("empty model must be a pure passthrough, got %q", out.String())
	}
}

// 注释行、event 行、非 JSON 的 data 行都要原样透传，不能被改坏。
func TestRewriteModelInSSELeavesNonJSONLines(t *testing.T) {
	in := ": keep-alive\nevent: message\ndata: oops\n"
	var out bytes.Buffer
	if err := RewriteModelInSSE(&out, strings.NewReader(in), "raccoon-chat-ml-5-5"); err != nil {
		t.Fatalf("RewriteModelInSSE: %v", err)
	}
	if out.String() != in {
		t.Fatalf("non-JSON SSE lines must pass through, got %q", out.String())
	}
}

func TestRewriteModelInSSEKeepsCRLF(t *testing.T) {
	in := "data: {\"model\":\"upstream-internal\"}\r\n"
	var out bytes.Buffer
	if err := RewriteModelInSSE(&out, strings.NewReader(in), "m"); err != nil {
		t.Fatalf("RewriteModelInSSE: %v", err)
	}
	if !strings.HasSuffix(out.String(), "\r\n") {
		t.Fatalf("CRLF must be preserved, got %q", out.String())
	}
}

func TestRewriteModelInJSON(t *testing.T) {
	got := string(RewriteModelInJSON([]byte(`{"id":"x","model":"upstream-internal"}`), "raccoon-8c4485"))
	if !strings.Contains(got, `"model":"raccoon-8c4485"`) {
		t.Fatalf("model not rewritten: %s", got)
	}
	if !strings.Contains(got, `"id":"x"`) {
		t.Fatalf("other fields must survive: %s", got)
	}
	if raw := RewriteModelInJSON([]byte("not json"), "m"); string(raw) != "not json" {
		t.Fatalf("unparsable body must pass through, got %q", raw)
	}
	if raw := RewriteModelInJSON([]byte(`{"model":"already"}`), "already"); string(raw) != `{"model":"already"}` {
		t.Fatalf("already-matching body must not be rewritten, got %q", raw)
	}
}
