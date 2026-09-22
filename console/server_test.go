package main

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func adminRequest(h http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://console.test"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://console.test")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := adminRequest(h, "POST", "/admin/login", `{"key":"`+strings.Repeat("a", 32)+`"}`, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var v struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.CSRF == "" {
		t.Fatal("missing csrf")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session")
	}
	return cookies[0], v.CSRF
}
func TestSessionSecurityAndLogout(t *testing.T) {
	h, cfg := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		if !mockInfo(w, r) {
			w.Write([]byte(`{"ok":true}`))
		}
	})
	for _, path := range []string{"/admin/status", "/admin/models", "/admin/session"} {
		if w := adminRequest(h, "GET", path, "", nil, ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	cookie, csrf := login(t, h)
	if !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin" || cookie.MaxAge != 8*3600 {
		t.Fatalf("unsafe cookie %+v", cookie)
	}
	if w := adminRequest(h, "POST", "/admin/access", "{}", cookie, ""); w.Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	other, otherCSRF := login(t, h)
	if w := adminRequest(h, "POST", "/admin/access", "{}", cookie, otherCSRF); w.Code != 403 {
		t.Fatal("cross-session csrf accepted")
	}
	w := adminRequest(h, "POST", "/admin/access", "{}", cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), cfg.APIKey) || strings.Contains(w.Body.String(), cfg.BridgeKey) {
		t.Fatal("access credential wrong", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "http://console.test/admin/access", strings.NewReader("{}"))
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "http://evil.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	w = adminRequest(h, "POST", "/admin/logout", "{}", cookie, csrf)
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout failed")
	}
	if w := adminRequest(h, "GET", "/admin/session", "", cookie, csrf); w.Code != 401 {
		t.Fatal("revoked session accepted")
	}
	if w := adminRequest(h, "GET", "/admin/session", "", other, otherCSRF); w.Code != 200 {
		t.Fatal("logout canceled another session")
	}
	s := h.(*server)
	s.mu.Lock()
	s.sessions[other.Value].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if w := adminRequest(h, "GET", "/admin/session", "", other, otherCSRF); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}
func TestLoginLimitsAndInputValidation(t *testing.T) {
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) })
	for _, body := range []string{`{`, `{"key":"wrong"}`, `{"key":"a","extra":true}`, `{"key":"a"} {}`, `{"key":"` + strings.Repeat("x", 9000) + `"}`} {
		if w := adminRequest(h, "POST", "/admin/login", body, nil, ""); w.Code != 400 && w.Code != 401 {
			t.Fatal("invalid login accepted", w.Code)
		}
	}
	for i := 0; i < 5; i++ {
		adminRequest(h, "POST", "/admin/login", `{"key":"wrong"}`, nil, "")
	}
	if w := adminRequest(h, "POST", "/admin/login", `{"key":"`+strings.Repeat("a", 32)+`"}`, nil, ""); w.Code != 429 {
		t.Fatal("login limit bypassed", w.Code)
	}
}
func TestPublicOriginAndSecurityHeaders(t *testing.T) {
	for _, tc := range []struct {
		origin string
		tls    bool
	}{{"https://public.test/", false}, {"", true}} {
		cfg := testConfig("http://127.0.0.1:1")
		cfg.PublicOrigin = tc.origin
		h, err := NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "https://public.test/admin/login", strings.NewReader(`{"key":"`+cfg.AdminKey+`"}`))
		r.Header.Set("Origin", "https://public.test")
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		} else {
			r.TLS = nil
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || !w.Result().Cookies()[0].Secure {
			t.Fatal("secure origin lost", w.Code)
		}
	}
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) { mockInfo(w, r) })
	r := httptest.NewRequest("POST", "http://console.test/admin/login", strings.NewReader(`{"key":"`+strings.Repeat("a", 32)+`"}`))
	r.Header.Set("Origin", "https://console.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("trusted forged forwarded origin")
	}
	for _, path := range []string{"/", "/app.js", "/style.css", "/admin/session"} {
		w := adminRequest(h, "GET", path, "", nil, "")
		for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "Cache-Control"} {
			if w.Header().Get(header) == "" {
				t.Errorf("missing %s on %s", header, path)
			}
		}
	}
}
func TestConfigRejectsUnsafeTargetsAndOrigins(t *testing.T) {
	for _, raw := range []string{"ftp://core", "http://user:pass@core", "http://core?a=1", "http://core?", "http://core#fragment", "http://core/internal", "http://core/%2f", "//core", "http:///"} {
		cfg := testConfig(raw)
		if _, err := NewServer(cfg); err == nil {
			t.Errorf("unsafe target accepted %s", raw)
		}
	}
	cfg := testConfig("http://core")
	cfg.CoreURL = nil
	if _, err := NewServer(cfg); err == nil {
		t.Fatal("nil target accepted")
	}
	for _, raw := range []string{"ftp://public", "https://user:pass@public", "https://public/path", "https://public?x=1", "https://public?", "https://public#x", "//public"} {
		cfg := testConfig("http://core")
		cfg.PublicOrigin = raw
		if _, err := NewServer(cfg); err == nil {
			t.Errorf("unsafe origin %s", raw)
		}
	}
	for _, edit := range []func(*Config){func(c *Config) { c.AdminKey = "" }, func(c *Config) { c.BridgeKey = "short" }, func(c *Config) { c.APIKey = c.AdminKey }, func(c *Config) { c.BridgeKey = c.APIKey }} {
		cfg := testConfig("http://core")
		edit(&cfg)
		if _, err := NewServer(cfg); err == nil {
			t.Error("unsafe keys accepted")
		}
	}
	cfg = testConfig("http://core")
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CoreURL.Host = "attacker"
	if h.(*server).cfg.CoreURL.Host != "core" {
		t.Error("target changed through caller's pointer")
	}
}

func TestLoginLimitKeysClientsThroughTrustedProxy(t *testing.T) {
	forward := func(h http.Handler, peer, forwarded, key string) int {
		r := httptest.NewRequest("POST", "http://console.test/admin/login", strings.NewReader(`{"key":"`+key+`"}`))
		r.Header.Set("Origin", "http://console.test")
		r.RemoteAddr = peer + ":1234"
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	newHandler := func(trusted string) (http.Handler, Config) {
		t.Helper()
		cfg := testConfig("http://127.0.0.1:1")
		cfg.TrustedProxyCIDRs = trusted
		h, err := NewServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return h, cfg
	}
	t.Run("trusted proxy isolates forwarded clients", func(t *testing.T) {
		h, cfg := newHandler("192.0.2.0/24")
		for i := 0; i < 10; i++ {
			if code := forward(h, "192.0.2.1", "203.0.113.7", "wrong"); code != 401 {
				t.Fatalf("attempt %d limited too early: %d", i+1, code)
			}
		}
		if code := forward(h, "192.0.2.1", "203.0.113.7", cfg.AdminKey); code != 429 {
			t.Fatalf("forwarded client limit not applied: %d", code)
		}
		if code := forward(h, "192.0.2.1", "198.51.100.9", cfg.AdminKey); code != 200 {
			t.Fatalf("one client lockout spread to another behind the same proxy: %d", code)
		}
		if code := forward(h, "198.51.100.9", "203.0.113.7", cfg.AdminKey); code != 200 {
			t.Fatalf("untrusted peer inherited a bucket named by its own header: %d", code)
		}
	})
	for _, trusted := range []string{"", "198.51.100.0/24"} {
		t.Run("untrusted peer ignores forwarded header", func(t *testing.T) {
			h, cfg := newHandler(trusted)
			for i := 0; i < 10; i++ {
				if code := forward(h, "192.0.2.1", "203.0.113.7", "wrong"); code != 401 {
					t.Fatalf("attempt %d limited without a trusted proxy: %d", i+1, code)
				}
			}
			if code := forward(h, "192.0.2.1", "203.0.113.8", cfg.AdminKey); code != 429 {
				t.Fatalf("X-Forwarded-For honoured with trusted=%q: %d", trusted, code)
			}
		})
	}
	t.Run("bare address is not a cidr", func(t *testing.T) {
		bad := testConfig("http://127.0.0.1:1")
		bad.TrustedProxyCIDRs = "192.0.2.1"
		if _, err := NewServer(bad); err == nil {
			t.Fatal("bare address accepted as a trusted proxy CIDR")
		}
	})
}

// HSTS must be sent only when the deployment declares an HTTPS origin; asserting it on a
// plaintext origin would pin browsers against a scheme this process does not serve.
func TestHSTSOnlyOnDeclaredHTTPSOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		want   bool
	}{
		{"https origin sends hsts", "https://console.test", true},
		{"plain origin omits hsts", "http://console.test", false},
		{"no origin omits hsts", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig("http://127.0.0.1:1")
			cfg.PublicOrigin = tc.origin
			h, err := NewServer(cfg)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "http://console.test/livez", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			got := w.Header().Get("Strict-Transport-Security")
			if (got != "") != tc.want {
				t.Fatalf("origin=%q: Strict-Transport-Security=%q, want present=%v", tc.origin, got, tc.want)
			}
			if tc.want && got != "max-age=31536000" {
				t.Fatalf("unexpected hsts value: %q", got)
			}
		})
	}
}
