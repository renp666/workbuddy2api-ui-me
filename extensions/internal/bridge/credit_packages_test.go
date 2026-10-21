package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

type creditRT func(*http.Request) (*http.Response, error)

func (f creditRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newCreditTestClient 构造指向 httptest 的 upstream.Client，计数 billing 调用次数。
func newCreditTestClient(t *testing.T, calls *int64) *upstream.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v2/billing/meter/get-user-resource") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		atomic.AddInt64(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"data":{"Response":{"Data":{"Accounts":[`+
			`{"PackageName":"奖励包","PackageEndTime":"2026-10-25 12:00:00","CycleCapacitySize":1500,"CycleCapacityRemain":1200,"CycleCapacityUsed":300},`+
			`{"PackageName":"长期包","CycleCapacitySize":900,"CycleCapacityRemain":900,"CycleCapacityUsed":0}]}}}}`)
	}))
	t.Cleanup(srv.Close)
	return &upstream.Client{HTTP: &http.Client{Transport: creditRT(srv.Client().Do)}, BillingBaseCN: srv.URL}
}

func creditRequest(h http.Handler, query string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/internal/v1/credit-packages"+query, nil)
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeCreditItems(t *testing.T, body string) (map[string]map[string]any, []string) {
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	items := map[string]map[string]any{}
	order := []string{}
	for _, item := range out.Items {
		uid, _ := item["uid"].(string)
		items[uid] = item
		order = append(order, uid)
	}
	return items, order
}

func TestCreditPackagesAggregatesAndCaches(t *testing.T) {
	var calls int64
	client := newCreditTestClient(t, &calls)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn1", AccessToken: "at"})
	p.Add(&auth.Auth{UID: "cn2"}) // 无 token → 无可用凭据
	h := New(context.Background(), Config{Key: testKey, APIKey: "core-api", Pool: p, Upstream: client})

	w := creditRequest(h, "")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	items, order := decodeCreditItems(t, w.Body.String())
	if len(items) != 2 {
		t.Fatalf("items=%v want 2 accounts", order)
	}
	ok := items["cn1"]
	if ok == nil || ok["error"] != nil || ok["credits"] != float64(0) {
		t.Fatalf("cn1=%v want success", ok)
	}
	pkgs, _ := ok["packages"].([]any)
	if len(pkgs) != 2 {
		t.Fatalf("cn1 packages=%v want 2 (remain>0)", pkgs)
	}
	first, _ := pkgs[0].(map[string]any)
	if first["name"] != "奖励包" || first["remain"] != float64(1200) || first["end_at"] == "" {
		t.Fatalf("first package=%v want 奖励包 1200 带到期", first)
	}
	noauth := items["cn2"]
	if noauth == nil || noauth["error"] != "无可用凭据" {
		t.Fatalf("cn2=%v want 无可用凭据", noauth)
	}
	if order[len(order)-1] != "cn2" {
		t.Fatalf("order=%v want error entry last", order)
	}
	// 第二次请求命中缓存：不产生新的 billing 调用。
	w = creditRequest(h, "")
	if w.Code != 200 {
		t.Fatalf("cached status=%d", w.Code)
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Fatalf("calls=%d want 1 (cache hit)", n)
	}
	// refresh=1 绕过缓存：重新外呼。
	w = creditRequest(h, "?refresh=1")
	if w.Code != 200 {
		t.Fatalf("refresh status=%d", w.Code)
	}
	if n := atomic.LoadInt64(&calls); n != 2 {
		t.Fatalf("calls=%d want 2 after refresh", n)
	}
	// 非法查询参数拒绝。
	if w := creditRequest(h, "?foo=1"); w.Code != 400 {
		t.Fatalf("bad query status=%d want 400", w.Code)
	}
}

func TestCreditPackagesRequiresPoolAndUpstream(t *testing.T) {
	h := New(context.Background(), Config{Key: testKey, APIKey: "core-api"})
	if w := creditRequest(h, ""); w.Code != 503 {
		t.Fatalf("status=%d want 503", w.Code)
	}
}
