package upstream

import (
	"net/http"
	"testing"
)

// TestClassify429QuotaIsSoftRate fork-scan-absorb T-3（本次回移修复点）：429 +
// 跨计费/限流两界措辞（"quota exceeded"/"额度不足" 等）必须归 ErrSoftRate。
// hardRule 先于 status==429 判会把限流误归 ErrHardCredit 硬冷却到次日 04:00，
// 白扔号约 12h。状态码是比关键词更权威的信号。
func TestClassify429QuotaIsSoftRate(t *testing.T) {
	for _, body := range []string{
		`quota exceeded`,
		`{"code":1,"msg":"quota exceeded, please wait"}`,
		`insufficient credits`,
		`{"code":1,"msg":"额度不足"}`,
		`积分不足，请充值`,
	} {
		if got := Classify(http.StatusTooManyRequests, body); got != ErrSoftRate {
			t.Errorf("Classify(429, %q)=%s want soft_rate", body, got)
		}
	}
}

// TestClassify402StillFirst 真正的余额耗尽（402）仍在所有关键词规则之前判
// ErrHardCredit（前移不影响 402 优先级）。
func TestClassify402StillFirst(t *testing.T) {
	for _, body := range []string{``, `rate limit`, `{"msg":"quota exceeded"}`} {
		if got := Classify(http.StatusPaymentRequired, body); got != ErrHardCredit {
			t.Errorf("Classify(402, %q)=%s want hard_credit", body, got)
		}
	}
}

// TestClassifyNon429QuotaKeepsHardCredit 非 429 状态码携带 quota 措辞仍走
// hardRule → ErrHardCredit（历史语义不变，防过度前移）。403 用带信封 body：
// WAF 形态只认无信封，带信封 403 走既有分类链。
func TestClassifyNon429QuotaKeepsHardCredit(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusOK, `{"code":1,"msg":"quota exceeded"}`},
		{http.StatusForbidden, `{"msg":"quota exceeded"}`},
		{http.StatusBadRequest, `额度不足`},
		{http.StatusInternalServerError, `insufficient credit`},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != ErrHardCredit {
			t.Errorf("Classify(%d, %q)=%s want hard_credit", c.status, c.body, got)
		}
	}
}

// TestClassify429AccountFaultPrecedence 429 + 账号级故障码仍归 ErrAccountFault
// （账号级层保持在 429 层之前：14017 常带 429，误归 soft_rate 会等不来自愈）；
// 401 混排 "12153"+"rate limit" 仍归 sessionDead。
func TestClassify429AccountFaultPrecedence(t *testing.T) {
	if got := Classify(http.StatusTooManyRequests, `{"code":14017,"msg":"trial not activated"}`); got != ErrAccountFault {
		t.Errorf("Classify(429, 14017)=%s want account_fault", got)
	}
	if got := Classify(http.StatusTooManyRequests, `{"error":{"data":{"code":11140,"msg":"request illegal"}}}`); got != ErrAccountFault {
		t.Errorf("Classify(429, 11140)=%s want account_fault", got)
	}
	if got := Classify(http.StatusUnauthorized, `12153 rate limit exceeded`); got != ErrSessionDead {
		t.Errorf("Classify(401, 12153+rate limit)=%s want session_dead", got)
	}
}

// TestClassify429RateLimitTextUnchanged 429 + 限流文案 / 429 无文案：均归
// ErrSoftRate（前移前后结果不变，防回归）。
func TestClassify429RateLimitTextUnchanged(t *testing.T) {
	if got := Classify(http.StatusTooManyRequests, `too many requests`); got != ErrSoftRate {
		t.Errorf("Classify(429, rate text)=%s want soft_rate", got)
	}
	if got := Classify(http.StatusTooManyRequests, ``); got != ErrSoftRate {
		t.Errorf("Classify(429, empty)=%s want soft_rate", got)
	}
}
