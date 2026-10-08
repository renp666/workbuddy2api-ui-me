package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
)

// newTestClient 把 Client 的三个上游根都指向同一个 httptest server；直接构造而不是
// 走 New()，避免测试受宿主环境变量影响。
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		HTTP:        srv.Client(),
		StreamHTTP:  srv.Client(),
		CatalogHTTP: srv.Client(),
		LLMBase:     srv.URL,
		AuthOrigin:  srv.URL,
		MainSite:    srv.URL,
	}
}

func testAuth(t *testing.T, withRefresh bool) *auth.Auth {
	t.Helper()
	raw := `{"uid":"u1","access_token":"at","expires_at":4102444800}`
	if withRefresh {
		raw = `{"uid":"u1","access_token":"at","refresh_token":"rt","expires_at":4102444800}`
	}
	a, err := auth.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("auth.Parse: %v", err)
	}
	a.SetFilePath(filepath.Join(t.TempDir(), "raccoon-u1.json"))
	return a
}

func TestClassifyByStatus(t *testing.T) {
	cases := map[int]ErrKind{
		http.StatusOK:                  ErrNone,
		http.StatusUnauthorized:        ErrTokenExpired,
		http.StatusTooManyRequests:     ErrQuotaLimited,
		http.StatusNotFound:            ErrNotFound,
		http.StatusBadRequest:          ErrClient,
		http.StatusInternalServerError: ErrServer,
	}
	for status, want := range cases {
		if got := Classify(status, []byte(`{"code":1005,"message":"plan"}`)); got != want {
			t.Fatalf("Classify(%d) = %v, want %v", status, got, want)
		}
	}
}

// 上游没有业务错误码，只有 HTTP 状态码；不能沿用 trae 的 1005 语义。
func TestClassifyIgnoresBusinessCode(t *testing.T) {
	if got := Classify(http.StatusOK, []byte(`{"code":1005}`)); got != ErrNone {
		t.Fatalf("Classify(200, code 1005) = %v, want ErrNone", got)
	}
}

func TestErrorKindHelpers(t *testing.T) {
	expired := &Error{Kind: ErrTokenExpired, Status: 401, Msg: "unauthorized"}
	if !IsTokenExpiredErr(expired) || IsQuotaErr(expired) {
		t.Fatal("401 error must be token-expired only")
	}
	quota := &Error{Kind: ErrQuotaLimited, Status: 429, Msg: "slow down"}
	if !IsQuotaErr(quota) || IsTokenExpiredErr(quota) {
		t.Fatal("429 error must be quota only")
	}
	if !strings.Contains(expired.Error(), "http 401") {
		t.Fatalf("error text = %q", expired.Error())
	}
}

func TestRefreshTokenReplacesTokensAndPersists(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/web/auth/v1/refresh" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at2","refresh_token":"rt2","expires_at":1700000000}`)
	}))
	a := testAuth(t, true)
	if err := c.RefreshToken(context.Background(), a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if gotBody["refresh_token"] != "rt" {
		t.Fatalf("request body = %v, want refresh_token rt", gotBody)
	}
	if a.JWT() != "at2" || a.RefreshTokenValue() != "rt2" || a.ExpiresAtValue() != 1700000000 {
		t.Fatalf("tokens after refresh = %q/%q/%d", a.JWT(), a.RefreshTokenValue(), a.ExpiresAtValue())
	}
	raw, err := readFileString(a.FilePathValue())
	if err != nil {
		t.Fatalf("credentials not persisted: %v", err)
	}
	if !strings.Contains(raw, "at2") {
		t.Fatalf("persisted credentials missing new token: %s", raw)
	}
}

func TestRefreshTokenKeepsOldCredentialsOnFailure(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	a := testAuth(t, true)
	if err := c.RefreshToken(context.Background(), a); err == nil {
		t.Fatal("RefreshToken should fail on 5xx")
	}
	if a.JWT() != "at" || a.RefreshTokenValue() != "rt" {
		t.Fatalf("failed refresh must not touch credentials, got %q/%q", a.JWT(), a.RefreshTokenValue())
	}
}

func TestRefreshTokenWithoutRefreshToken(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("upstream must not be called without a refresh token")
	}))
	a := testAuth(t, false)
	err := c.RefreshToken(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "no refreshToken") {
		t.Fatalf("err = %v, want a missing-refresh-token failure", err)
	}
}

func TestRefreshTokenWithoutTokenInResponse(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"message":"ok"}`)
	}))
	a := testAuth(t, true)
	err := c.RefreshToken(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "no token in response") {
		t.Fatalf("err = %v, want a no-token failure", err)
	}
	if a.JWT() != "at" {
		t.Fatalf("failed refresh must keep the old access token, got %q", a.JWT())
	}
}

func TestRefreshTokenIfNeededSkipsWhenNotExpiring(t *testing.T) {
	calls := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"access_token":"at2"}`)
	}))
	a := testAuth(t, true)
	refreshed, err := c.RefreshTokenIfNeeded(context.Background(), a, RefreshMargin)
	if err != nil || refreshed {
		t.Fatalf("RefreshTokenIfNeeded = %v/%v, want no refresh", refreshed, err)
	}
	if calls != 0 {
		t.Fatalf("upstream called %d time(s), want 0", calls)
	}
}

func TestRefreshTokenIfNeededRefreshesExpiring(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"at2","refresh_token":"rt2"}`)
	}))
	a := testAuth(t, true)
	a.SetTokens("at", "rt", time.Now().Add(time.Minute).Unix())
	refreshed, err := c.RefreshTokenIfNeeded(context.Background(), a, RefreshMargin)
	if err != nil || !refreshed {
		t.Fatalf("RefreshTokenIfNeeded = %v/%v, want a refresh", refreshed, err)
	}
	if a.JWT() != "at2" {
		t.Fatalf("access token = %q, want at2", a.JWT())
	}
}

func TestBalanceParsesWallets(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpBalance {
			t.Errorf("path = %q, want %q", r.URL.Path, EpBalance)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer at" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"available_points":120,"daily_points":10,"monthly_points":20,"reward_points":30,"topup_points":60}}`)
	}))
	bal, err := c.Balance(context.Background(), testAuth(t, true))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	want := Balance{Available: 120, Daily: 10, Monthly: 20, Reward: 30, TopUp: 60}
	if bal != want {
		t.Fatalf("Balance = %+v, want %+v", bal, want)
	}
}

func TestBalanceRejectsNonZeroBusinessCode(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":1001,"message":"未登录"}`)
	}))
	_, err := c.Balance(context.Background(), testAuth(t, true))
	if err == nil || !strings.Contains(err.Error(), "未登录") {
		t.Fatalf("err = %v, want the upstream message", err)
	}
}

func TestBalancePropagatesUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	_, err := c.Balance(context.Background(), testAuth(t, true))
	if !IsTokenExpiredErr(err) {
		t.Fatalf("err = %v, want token-expired", err)
	}
}

func TestEntitlementPrefersOffice(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpEntitlement {
			t.Errorf("path = %q, want %q", r.URL.Path, EpEntitlement)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"office":{"pro_enable":true,"active_plan":"pro","pro_expired_time":"2027-01-01"},"code":{"pro_enable":false,"active_plan":"free"}}}`)
	}))
	ent, err := c.Entitlement(context.Background(), testAuth(t, true))
	if err != nil {
		t.Fatalf("Entitlement: %v", err)
	}
	if ent.Status != "active" || ent.ActivePlan != "pro" || ent.ExpiredTime != "2027-01-01" {
		t.Fatalf("entitlement = %+v, want the office plan", ent)
	}
}

func TestEntitlementFallsBackToCode(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"code":{"pro_enable":false,"active_plan":"free"}}}`)
	}))
	ent, err := c.Entitlement(context.Background(), testAuth(t, true))
	if err != nil {
		t.Fatalf("Entitlement: %v", err)
	}
	if ent.Status != "none" || ent.ActivePlan != "free" {
		t.Fatalf("entitlement = %+v, want the code plan with status none", ent)
	}
}

func TestEntitlementWithoutPlan(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}))
	ent, err := c.Entitlement(context.Background(), testAuth(t, true))
	if err != nil || ent.Status != "none" {
		t.Fatalf("entitlement = %+v, err = %v", ent, err)
	}
}

func TestBillsAcceptsSeveralShapes(t *testing.T) {
	shapes := []string{
		`{"code":0,"data":{"list":[{"points":10}]}}`,
		`{"code":0,"data":{"records":[{"points":10}]}}`,
		`{"code":0,"data":{"items":[{"points":10}]}}`,
		`{"code":0,"data":[{"points":10}]}`,
	}
	for _, shape := range shapes {
		c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, shape)
		}))
		bills, err := c.Bills(context.Background(), testAuth(t, true), 20, 0)
		if err != nil {
			t.Fatalf("Bills(%s): %v", shape, err)
		}
		if len(bills) != 1 || bills[0].delta() != 10 {
			t.Fatalf("Bills(%s) = %+v", shape, bills)
		}
	}
}

func TestBillsSendsPaging(t *testing.T) {
	var query string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"code":0,"data":{"list":[]}}`)
	}))
	if _, err := c.Bills(context.Background(), testAuth(t, true), 5, 10); err != nil {
		t.Fatalf("Bills: %v", err)
	}
	if !strings.Contains(query, "paging.limit=5") || !strings.Contains(query, "paging.offset=10") {
		t.Fatalf("query = %q", query)
	}
}

func TestBillsRejectsUnknownShape(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"unexpected":true}}`)
	}))
	if _, err := c.Bills(context.Background(), testAuth(t, true), 20, 0); err == nil {
		t.Fatal("Bills should reject an unrecognized shape")
	}
}

// 账单核对是「今日积分确有入账」的权威口径，跨日 / 负流水 / 毫秒都必须判对。
func TestGrantedToday(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, loc)
	todayNoon := time.Date(2026, 10, 8, 11, 0, 0, 0, loc)
	yesterdayNoon := time.Date(2026, 10, 7, 11, 0, 0, 0, loc)

	cases := []struct {
		name  string
		bills []BillEntry
		want  int64
	}{
		{"空账单", nil, 0},
		{"今日正向入账", []BillEntry{{CreatedAt: rawInt(todayNoon.Unix()), ChangePoint: rawInt(100)}}, 100},
		{"今日多笔累加", []BillEntry{
			{CreatedAt: rawInt(todayNoon.Unix()), ChangePoint: rawInt(100)},
			{CreatedAt: rawInt(todayNoon.Unix()), ChangePoint: rawInt(20)},
		}, 120},
		{"负数流水不算入账", []BillEntry{{CreatedAt: rawInt(todayNoon.Unix()), ChangePoint: rawInt(-50)}}, 0},
		{"跨日不入账", []BillEntry{{CreatedAt: rawInt(yesterdayNoon.Unix()), ChangePoint: rawInt(100)}}, 0},
		{"毫秒时间戳同样识别", []BillEntry{{CreatedAt: rawInt(todayNoon.Unix() * 1000), ChangePoint: rawInt(30)}}, 30},
		{"字符串时间", []BillEntry{{CreatedAt: json.RawMessage(`"2026-10-08 09:00:00"`), ChangePoint: rawInt(10)}}, 10},
		{"amount 字段兜底", []BillEntry{{CreateTime: rawInt(todayNoon.Unix()), Amount: rawInt(8)}}, 8},
	}
	for _, tc := range cases {
		if got := GrantedToday(tc.bills, now); got != tc.want {
			t.Fatalf("%s: GrantedToday = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseCatalogFlattensAndMapsKeys(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"categories":[
		{"type":"chat","models":[{"name":"a","displayName":"A","contextWindow":1000,"maxTokens":100,"supportImage":true,"supportThinking":true}]},
		{"type":"work","models":[{"name":"a","displayName":"dup"},{"name":"","displayName":"empty"},{"name":"b"}]}
	]}}`)
	got := parseCatalog(raw)
	if len(got) != 2 {
		t.Fatalf("parseCatalog = %+v, want 2 models", got)
	}
	if got[0].ID != "a" || got[0].Name != "A" {
		t.Fatalf("first-wins mapping broken: %+v", got[0])
	}
	if got[0].ContextWindow != 1000 || got[0].MaxTokens != 100 || !got[0].SupportsImages || !got[0].SupportsThinking {
		t.Fatalf("capability mapping broken: %+v", got[0])
	}
	if got[0].Object != "model" || got[0].OwnedBy != "xiaohuanxiong" {
		t.Fatalf("missing OpenAI defaults: %+v", got[0])
	}
	if got[1].ID != "b" || got[1].Name != "b" {
		t.Fatalf("display-name fallback broken: %+v", got[1])
	}
}

func TestParseCatalogAcceptsRootWithoutData(t *testing.T) {
	got := parseCatalog([]byte(`{"categories":[{"models":[{"name":"x"}]}]}`))
	if len(got) != 1 || got[0].ID != "x" {
		t.Fatalf("parseCatalog = %+v, want one model", got)
	}
}

func TestParseCatalogOnGarbage(t *testing.T) {
	if got := parseCatalog([]byte("not json")); got != nil {
		t.Fatalf("parseCatalog(garbage) = %+v, want nil", got)
	}
}

func TestFetchModelsMergesStaticFallbackAndCaches(t *testing.T) {
	calls := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != EpCatalog {
			t.Errorf("path = %q, want %q", r.URL.Path, EpCatalog)
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"categories":[{"models":[{"name":"remote-only"}]}]}}`)
	}))
	a := testAuth(t, true)
	got := c.FetchModels(context.Background(), a)
	if len(got) != len(StaticModels())+1 {
		t.Fatalf("FetchModels = %d entries, want remote + static fallback", len(got))
	}
	again := c.FetchModels(context.Background(), a)
	if len(again) != len(got) || calls != 1 {
		t.Fatalf("catalog must be cached: calls = %d, entries = %d", calls, len(again))
	}
}

func TestFetchModelsFallsBackWhenCatalogFails(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	got := c.FetchModels(context.Background(), testAuth(t, true))
	if len(got) != len(StaticModels()) {
		t.Fatalf("FetchModels = %d entries, want the static fallback", len(got))
	}
}

func TestChatStreamPassesUpstreamHeaders(t *testing.T) {
	var got http.Header
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path != EpChat {
			t.Errorf("path = %q, want %q", r.URL.Path, EpChat)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	rc, status, respBody, err := c.ChatStream(context.Background(), testAuth(t, true), []byte(`{"model":"raccoon-8c4485"}`))
	if err != nil || rc == nil || status != http.StatusOK || respBody != nil {
		t.Fatalf("ChatStream = %v/%d/%v/%v", rc, status, respBody, err)
	}
	defer rc.Close()
	if got.Get("Authorization") != "Bearer at" || got.Get("Accept") != "*/*" {
		t.Fatalf("upstream headers = %v", got)
	}
	if !strings.HasPrefix(got.Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type = %q", got.Get("Content-Type"))
	}
	body, _ := io.ReadAll(rc)
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream body = %q", body)
	}
}

func TestChatStreamSurfacesUpstreamError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"slow down"}`)
	}))
	rc, status, respBody, err := c.ChatStream(context.Background(), testAuth(t, true), []byte(`{}`))
	if rc != nil || err != nil || status != http.StatusTooManyRequests || len(respBody) == 0 {
		t.Fatalf("ChatStream = %v/%d/%v/%v, want a nil body plus the upstream status", rc, status, respBody, err)
	}
	if Classify(status, respBody) != ErrQuotaLimited {
		t.Fatalf("429 must classify as quota limited")
	}
}

func TestDesktopGrantIsIdempotent(t *testing.T) {
	var platform string
	granted := true
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		platform = r.Header.Get("X-Client-Platform")
		if r.URL.Path != EpDesktopGrant {
			t.Errorf("path = %q, want %q", r.URL.Path, EpDesktopGrant)
		}
		_, _ = io.WriteString(w, `{"granted":`+strconv.FormatBool(granted)+`}`)
	}))
	a := testAuth(t, true)
	got, err := c.DesktopGrant(context.Background(), a)
	if err != nil || !got {
		t.Fatalf("DesktopGrant = %v/%v", got, err)
	}
	if platform != clientPlatform {
		t.Fatalf("X-Client-Platform = %q, want %q", platform, clientPlatform)
	}
	granted = false
	got, err = c.DesktopGrant(context.Background(), a)
	if err != nil || got {
		t.Fatalf("DesktopGrant(already granted) = %v/%v, want false", got, err)
	}
}

func TestSettingInfoReturnsGrantNotice(t *testing.T) {
	var lang string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang = r.Header.Get("X-Raccoon-Language")
		if r.URL.Path != EpSettingInfo {
			t.Errorf("path = %q, want %q", r.URL.Path, EpSettingInfo)
		}
		_, _ = io.WriteString(w, `{"point_grant_toast":"+10 积分","point_grant_popups":[{}]}`)
	}))
	toast, popups, err := c.SettingInfo(context.Background(), testAuth(t, true))
	if err != nil {
		t.Fatalf("SettingInfo: %v", err)
	}
	if toast != "+10 积分" || popups != 1 {
		t.Fatalf("toast/popups = %q/%d", toast, popups)
	}
	if lang != "zh" {
		t.Fatalf("X-Raccoon-Language = %q, want zh", lang)
	}
}

func TestStaticModelsReturnsCopy(t *testing.T) {
	first := StaticModels()
	if len(first) == 0 {
		t.Fatal("static fallback must not be empty")
	}
	first[0].ID = "mutated"
	if StaticModels()[0].ID == "mutated" {
		t.Fatal("StaticModels must return a copy")
	}
	if !strings.Contains(strings.Join(modelIDs(StaticModels()), ","), DefaultModel) {
		t.Fatalf("default model missing from the static fallback: %v", modelIDs(StaticModels()))
	}
}

func modelIDs(models []ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

func rawInt(n int64) json.RawMessage { return json.RawMessage(strconv.FormatInt(n, 10)) }

func readFileString(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
