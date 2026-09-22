package upstream

import (
	"net/http"
	"testing"
)

// TestClassifyWafBlockShape WAF 形态判定（T1-g 的正面）：403 且 body 无业务信封
// （空体 / HTML 拦截页 / 纯文本）→ ErrWafBlock，且不再落 ErrClient 兜底。
func TestClassifyWafBlockShape(t *testing.T) {
	for _, body := range []string{
		"",
		"<html><body>Request blocked by WAF</body></html>",
		"Forbidden",
	} {
		if got := Classify(http.StatusForbidden, body); got != ErrWafBlock {
			t.Errorf("Classify(403, %q)=%s want waf_block", body, got)
		}
	}
	// 只有 403 才是该形态：其它 4xx 分类不变。
	if got := Classify(http.StatusBadRequest, ""); got != ErrClient {
		t.Errorf("Classify(400, \"\")=%s want client (兜底不变)", got)
	}
	if got := Classify(http.StatusForbidden, `{"code":403}`); got != ErrClient {
		t.Errorf("Classify(403, 带 code 信封)=%s want client", got)
	}
}

// TestClassifyWafEnvelopeKeepsExistingKind 带业务信封的 403 必须走既有权威分类，
// 不被 WAF 形态判定劫持（宁漏判 WAF 也不误罚业务 403）。
func TestClassifyWafEnvelopeKeepsExistingKind(t *testing.T) {
	if got := Classify(http.StatusForbidden, `{"code":11140,"msg":"request illegal"}`); got != ErrAccountFault {
		t.Fatalf("Classify(403, 11140)=%s want account_fault", got)
	}
	if !hasBusinessEnvelope(`{"code":11140,"msg":"request illegal"}`) {
		t.Error("hasBusinessEnvelope must recognise the code field")
	}
	if !hasBusinessEnvelope(`{"msg":"x"}`) {
		t.Error("hasBusinessEnvelope must recognise the msg field")
	}
	// 口径是字符串包含，不解析 JSON：畸形 JSON 含 "msg": 字样仍按业务响应处理。
	if !hasBusinessEnvelope(`not json at all "msg": 1`) {
		t.Error("malformed body carrying \"msg\": must stay on the business side")
	}
	if hasBusinessEnvelope("Request blocked by WAF") {
		t.Error("plain WAF text must not look like a business envelope")
	}
}

// TestErrWafBlockStringAndOrdering 分类值可观测且保持 ErrClient 仍是最后兜底位。
func TestErrWafBlockStringAndOrdering(t *testing.T) {
	if got := ErrWafBlock.String(); got != "waf_block" {
		t.Fatalf("ErrWafBlock.String()=%q want waf_block", got)
	}
	if ErrWafBlock == ErrClient {
		t.Fatal("ErrWafBlock must be a distinct kind")
	}
	if ErrWafBlock > ErrClient {
		t.Errorf("ErrWafBlock(%d) must sit before ErrClient(%d)", ErrWafBlock, ErrClient)
	}
}
