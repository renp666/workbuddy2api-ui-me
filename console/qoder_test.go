package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type qoderPair struct {
	coreHandler  http.HandlerFunc
	qoderHandler http.HandlerFunc
}

func testConsoleWithQoder(t *testing.T, pair qoderPair) (http.Handler, Config) {
	t.Helper()
	core := httptest.NewServer(pair.coreHandler)
	t.Cleanup(core.Close)
	qoder := httptest.NewServer(pair.qoderHandler)
	t.Cleanup(qoder.Close)
	cfg := testConfig(core.URL)
	cfg.QoderURL = mustParseURL(t, qoder.URL)
	cfg.QoderKey = strings.Repeat("q", 32)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h, cfg
}

func TestQoderPrefixRoutesToQoder(t *testing.T) {
	var coreHits, qoderHits int
	var mu sync.Mutex
	var qoderAuth, qoderBody, qoderAPIKey string
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			coreHits++
			mu.Unlock()
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			qoderHits++
			mu.Unlock()
			qoderAuth = r.Header.Get("Authorization")
			qoderAPIKey = r.Header.Get("X-Api-Key")
			body, _ := io.ReadAll(r.Body)
			qoderBody = string(body)
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	// qoder- prefix routes to qoder
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("qoder-qwen3.8-max")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	req.Header.Set("X-Api-Key", strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if qoderHits != 1 || coreHits != 0 {
		t.Fatalf("expected qoder=1 core=0, got qoder=%d core=%d", qoderHits, coreHits)
	}
	mu.Unlock()
	// key injected, API key stripped
	if qoderAuth != "Bearer "+strings.Repeat("q", 32) {
		t.Fatalf("qoder auth = %q", qoderAuth)
	}
	if qoderAPIKey != "" {
		t.Fatalf("X-Api-Key leaked to qoder: %q", qoderAPIKey)
	}
	// prefix stripped in body
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal([]byte(qoderBody), &envelope) != nil {
		t.Fatalf("qoder body not JSON: %s", qoderBody)
	}
	if envelope.Model != "qwen3.8-max" {
		t.Fatalf("qoder model prefix not stripped: %q", envelope.Model)
	}
}

func TestQoderPrefixDisabledFallsToCore(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("qoder should not be hit")
		},
	})
	// non-qoder model goes to core
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("cn:workbuddy")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestQoderNotConfiguredFallsToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	// QoderURL nil
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody("qoder-qwen3.8-max")))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMergedModelsIncludesQoder(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:workbuddy","realm":"cn"}]}`)
				return
			}
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-max"}]}`)
				return
			}
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Data []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"data"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &result) != nil {
		t.Fatalf("response not JSON: %s", rec.Body.String())
	}
	found := false
	for _, m := range result.Data {
		if m.ID == "qoder-qwen3.8-max" && m.Realm == "qoder" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("qoder model not found in merged list: %+v", result.Data)
	}
}

func TestAdminQoderStatusDisabled(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"core":true}`)
	}))
	t.Cleanup(core.Close)
	cfg := testConfig(core.URL)
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/admin/qoder", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// without admin session, expect 401
	if rec.Code != 401 {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestAdminQoderStatusEnabled(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models" {
				fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-max"},{"id":"qwen3.8-pro"}]}`)
				return
			}
			fmt.Fprint(w, `{"qoder":true}`)
		},
	})
	// login to get session
	cookie, _ := login(t, h)
	// get qoder status
	req := httptest.NewRequest("GET", "/admin/qoder", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var status struct {
		Enabled   bool `json:"enabled"`
		Reachable bool `json:"reachable"`
		ModelCount int `json:"model_count"`
		Models    []struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		} `json:"models"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &status) != nil {
		t.Fatalf("status not JSON: %s", rec.Body.String())
	}
	if !status.Enabled || !status.Reachable || status.ModelCount != 2 {
		t.Fatalf("unexpected status: %+v", status)
	}
	for _, m := range status.Models {
		if !strings.HasPrefix(m.ID, "qoder-") {
			t.Fatalf("model missing prefix: %q", m.ID)
		}
		if m.Realm != "qoder" {
			t.Fatalf("model missing realm: %q", m.Realm)
		}
	}
}

func TestAdminQoderChatRejectsNonPrefix(t *testing.T) {
	h, _ := testConsoleWithQoder(t, qoderPair{
		coreHandler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"core":true}`)
		},
		qoderHandler: func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("qoder should not be hit")
		},
	})
	// login
	cookie, csrf := login(t, h)
	// chat with non-prefixed model
	w := adminRequest(h, "POST", "/admin/qoder/chat", chatBody("cn:workbuddy"), cookie, csrf)
	if w.Code != 400 {
		t.Fatalf("expected 400 for non-qoder model, got %d: %s", w.Code, w.Body.String())
	}
}
