package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
	"workbuddy2api-raccoon/raccoon/pool"
	"workbuddy2api-raccoon/raccoon/upstream"
)

func newTestHandler(t *testing.T, apiKey string) (*Handler, *pool.Pool, string) {
	t.Helper()
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := pool.New(filepath.Join(dir, "state.json"))
	h := NewHandler(Config{
		Pool:      p,
		Upstream:  upstream.New(),
		APIKey:    apiKey,
		AuthDir:   authDir,
		UsageFile: filepath.Join(dir, "usage.json"),
	})
	return h, p, authDir
}

func call(t *testing.T, h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	return out
}

func credentials(token string, withRefresh bool, expiresAt int64) string {
	out := `{"uid":"u1","access_token":"` + token + `"`
	if withRefresh {
		out += `,"refresh_token":"rt"`
	}
	if expiresAt > 0 {
		out += `,"expires_at":` + strconv.FormatInt(expiresAt, 10)
	}
	return out + `}`
}

// console 的探活要求 200 且 body trim 后等于 "ok"，这里守住这条契约。
func TestHealthzNeedsNoAuth(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodGet, "/healthz", "", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", w.Code, w.Body.String())
	}
}

func TestModelsRequiresAPIKey(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodGet, "/v1/models", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if code, _ := decode(t, w)["error"].(map[string]any)["code"].(string); code != "invalid_api_key" {
		t.Fatalf("error code = %q", code)
	}
}

func TestModelsFallsBackToStaticCatalog(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodGet, "/v1/models", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	out := decode(t, w)
	if out["object"] != "list" {
		t.Fatalf("object = %v", out["object"])
	}
	data, _ := out["data"].([]any)
	if len(data) != len(upstream.StaticModels()) {
		t.Fatalf("data has %d entries, want the static fallback", len(data))
	}
}

func TestStatusRequiresAPIKeyAndListsAccounts(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	if w := call(t, h, http.MethodGet, "/status", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status without key = %d, want 401", w.Code)
	}
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(24*time.Hour).Unix()))
	w := call(t, h, http.MethodGet, "/status", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	accounts, _ := decode(t, w)["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %v", accounts)
	}
}

func TestChatWithoutAccountsIsUnavailable(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodPost, "/v1/chat/completions", "secret", `{"model":"raccoon-8c4485"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if code, _ := decode(t, w)["error"].(map[string]any)["code"].(string); code != "no_healthy_account" {
		t.Fatalf("error code = %q", code)
	}
}

func TestImportCreatesThenUpdates(t *testing.T) {
	h, _, authDir := newTestHandler(t, "secret")
	expires := time.Now().Add(24 * time.Hour).Unix()
	w := importCredentials(t, h, "secret", credentials("a1", true, expires))
	out := decode(t, w)
	if out["action"] != "created" || out["uid"] != "u1" {
		t.Fatalf("first import = %v", out)
	}
	if _, err := os.Stat(auth.FilePathFor(authDir, "u1")); err != nil {
		t.Fatalf("credential file not written: %v", err)
	}
	w = importCredentials(t, h, "secret", credentials("a2", true, expires))
	if out := decode(t, w); out["action"] != "updated" {
		t.Fatalf("second import = %v", out)
	}
}

// 已有更新的凭证时导入更旧的凭证必须 409，且不覆盖磁盘。
func TestImportRejectsOlderCredentials(t *testing.T) {
	h, _, authDir := newTestHandler(t, "secret")
	newer := time.Now().Add(48 * time.Hour).Unix()
	importCredentials(t, h, "secret", credentials("fresh-token", true, newer))
	w := importCredentials(t, h, "secret", credentials("stale-token", true, time.Now().Add(time.Hour).Unix()))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if out := decode(t, w); out["action"] != "skipped_existing_newer" {
		t.Fatalf("body = %v", out)
	}
	raw, err := os.ReadFile(auth.FilePathFor(authDir, "u1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "fresh-token") {
		t.Fatalf("older import overwrote the stored credential: %s", raw)
	}
}

func TestImportRejectsMissingAccessToken(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodPost, "/admin/api/accounts/import", "secret", `{"uid":"u1"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if code, _ := decode(t, w)["error"].(map[string]any)["code"].(string); code != "invalid_credentials" {
		t.Fatalf("error code = %q", code)
	}
}

func TestImportRejectsOversizedBody(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodPost, "/admin/api/accounts/import", "secret", strings.Repeat("a", maxDecodeBody+1))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestAccountsListNeedsNoAuthAndExposesCredentialState(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	// 没有 refresh token 的凭证永远无法自助恢复，即使上游没声明有效期也要报临期。
	importCredentials(t, h, "secret", `{"uid":"u1","access_token":"a1"}`)
	w := call(t, h, http.MethodGet, "/admin/api/accounts", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	accounts, _ := decode(t, w)["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %v", accounts)
	}
	row, _ := accounts[0].(map[string]any)
	if row["has_auth"] != true {
		t.Fatalf("has_auth = %v, want true", row["has_auth"])
	}
	if row["expired_soon"] != true {
		t.Fatalf("expired_soon = %v, want true without a refresh token", row["expired_soon"])
	}
	if _, ok := row["expires_at"]; !ok {
		t.Fatalf("expires_at must always be present: %v", row)
	}
	if _, leaked := row["access_token"]; leaked {
		t.Fatal("account summary must not leak the access token")
	}
}

// 有 refresh token 且有效期还远时不该报临期（展示余量独立于 5min 预刷新余量）。
func TestAccountsListHealthyCredentialIsNotExpiring(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(72*time.Hour).Unix()))
	w := call(t, h, http.MethodGet, "/admin/api/accounts", "", "")
	accounts, _ := decode(t, w)["accounts"].([]any)
	row, _ := accounts[0].(map[string]any)
	if row["expired_soon"] != false {
		t.Fatalf("expired_soon = %v, want false", row["expired_soon"])
	}
}

func TestDeleteAccount(t *testing.T) {
	h, p, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(24*time.Hour).Unix()))
	w := call(t, h, http.MethodDelete, "/admin/api/accounts/u1", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if decode(t, w)["deleted"] != true {
		t.Fatalf("body = %v", decode(t, w))
	}
	if _, ok := p.Status("u1"); ok {
		t.Fatal("account must be gone from the pool")
	}
	if w := call(t, h, http.MethodDelete, "/admin/api/accounts/u1", "secret", ""); w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", w.Code)
	}
}

func TestPatchTogglesEnabledAndRenames(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(24*time.Hour).Unix()))
	w := call(t, h, http.MethodPatch, "/admin/api/accounts/u1", "secret", `{"enabled":false}`)
	if w.Code != http.StatusOK || decode(t, w)["enabled"] != false {
		t.Fatalf("patch disable = %d %v", w.Code, decode(t, w))
	}
	w = call(t, h, http.MethodPatch, "/admin/api/accounts/u1", "secret", `{"nickname":"浣熊一号"}`)
	if w.Code != http.StatusOK || decode(t, w)["nickname"] != "浣熊一号" {
		t.Fatalf("patch rename = %d %v", w.Code, decode(t, w))
	}
	if w := call(t, h, http.MethodPatch, "/admin/api/accounts/nope", "secret", `{"enabled":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("patch unknown = %d, want 404", w.Code)
	}
}

func TestPatchRejectsBrokenBody(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(24*time.Hour).Unix()))
	w := call(t, h, http.MethodPatch, "/admin/api/accounts/u1", "secret", `{`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRefreshWithoutRefreshToken(t *testing.T) {
	h, p, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", `{"uid":"u1","access_token":"a1"}`)
	w := call(t, h, http.MethodPost, "/admin/api/accounts/u1/refresh", "secret", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if st, _ := p.Status("u1"); !st.Disabled {
		t.Fatalf("account must be marked unusable, got %+v", st)
	}
}

func TestRefreshUnknownAccount(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	w := call(t, h, http.MethodPost, "/admin/api/accounts/nope/refresh", "secret", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// 重新导入新凭证必须让 401 硬禁用的账号复活（Q3 的端到端口径）。
func TestReimportRevivesDisabledAccount(t *testing.T) {
	h, p, _ := newTestHandler(t, "secret")
	importCredentials(t, h, "secret", credentials("a1", true, time.Now().Add(24*time.Hour).Unix()))
	p.Disable("u1", "token_expired: 刷新后仍被拒")
	importCredentials(t, h, "secret", credentials("a2", true, time.Now().Add(24*time.Hour).Unix()))
	st, ok := p.Status("u1")
	if !ok || st.Disabled {
		t.Fatalf("status = %+v, want a revived account", st)
	}
	if _, ok := p.Pick(); !ok {
		t.Fatal("revived account must be pickable")
	}
}

func TestScheduleEndpointsWithoutScheduler(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/schedule"},
		{http.MethodPost, "/admin/api/schedule/checkin"},
		{http.MethodPost, "/admin/api/schedule/refresh"},
	} {
		if w := call(t, h, tc.method, tc.path, "secret", `{}`); w.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", tc.method, tc.path, w.Code)
		}
	}
}

func TestUsageStartsEmptyAndRequiresKey(t *testing.T) {
	h, _, _ := newTestHandler(t, "secret")
	if w := call(t, h, http.MethodGet, "/admin/api/usage", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("usage without key = %d, want 401", w.Code)
	}
	w := call(t, h, http.MethodGet, "/admin/api/usage", "secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("usage = %d", w.Code)
	}
	if _, ok := decode(t, w)["days"]; !ok {
		t.Fatalf("usage body must carry days: %v", decode(t, w))
	}
}

func importCredentials(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := call(t, h, http.MethodPost, "/admin/api/accounts/import", key, body)
	if w.Code != http.StatusOK && w.Code != http.StatusConflict {
		t.Fatalf("import = %d %s", w.Code, w.Body.String())
	}
	return w
}
