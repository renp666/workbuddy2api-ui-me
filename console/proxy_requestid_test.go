package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// —— 出口请求 ID（X-Request-Id / X-Console-Request-Id）——

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestPublicExitIssuesRequestID 锁定：每个公共对话请求带响应头 X-Request-Id（32 hex），
// 同一 ID 经 X-Console-Request-Id 传给 core，入站伪造的 X-Request-Id 被剥掉不透传。
func TestPublicExitIssuesRequestID(t *testing.T) {
	var coreSeenID, coreInboundID string
	h, _, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		coreSeenID = r.Header.Get("X-Console-Request-Id")
		coreInboundID = r.Header.Get("X-Request-Id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	rec := postChat(h, "/v1/chat/completions", chatBody("cn:test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	got := rec.Header().Get("X-Request-Id")
	if !hex32.MatchString(got) {
		t.Fatalf("响应头 X-Request-Id 不是 32 hex：%q", got)
	}
	if coreSeenID != got {
		t.Fatalf("core 收到的 X-Console-Request-Id=%q，与响应头 %q 不一致", coreSeenID, got)
	}
	if coreInboundID != "" {
		t.Fatalf("入站 X-Request-Id 未被剥离，core 收到 %q", coreInboundID)
	}
}

// TestPublicExitRequestIDUniquePerRequest 锁定：两个请求各自生成不同的 ID。
func TestPublicExitRequestIDUniquePerRequest(t *testing.T) {
	h, _, _ := errorConsole(t, chatCore(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	first := postChat(h, "/v1/chat/completions", chatBody("cn:test")).Header().Get("X-Request-Id")
	second := postChat(h, "/v1/chat/completions", chatBody("cn:test")).Header().Get("X-Request-Id")
	if first == "" || second == "" || first == second {
		t.Fatalf("请求 ID 未按请求区分：%q vs %q", first, second)
	}
}

// TestRequestIDHeaderNotOnManagementProxy 锁定：管理代理请求没有 ID 注入（ctx 无值时
// Rewrite 不写 X-Console-Request-Id），管理链路不进账本。
func TestRequestIDHeaderNotOnManagementProxy(t *testing.T) {
	var coreSeenID string
	h, _ := testConsole(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/info":
			mockInfo(w, r)
		default:
			coreSeenID = r.Header.Get("X-Console-Request-Id")
			w.WriteHeader(http.StatusOK)
		}
	})
	w := adminRequest(h, "GET", "/admin/usage", "", nil, "")
	if w.Code == 0 {
		t.Fatal("请求未发出")
	}
	if coreSeenID != "" {
		t.Fatalf("管理代理不应携带 X-Console-Request-Id，收到 %q", coreSeenID)
	}
}

// TestExitObserverEmptyStreamMarker 锁定完成口径的无产出信号：core 空流兜底帧命中
// emptyStream=true；正常流与失败响应不命中。观测包装零缓冲——marker 检测不改变透传字节。
func TestExitObserverEmptyStreamMarker(t *testing.T) {
	frame := append(append([]byte{}, emptyStreamMarker...), []byte(`"type":"upstream_error"}}`)...)
	cases := []struct {
		name    string
		status  int
		chunks  [][]byte
		want    bool
		wantOK  bool
		wantLen int64
	}{
		{"空流兜底帧", 200, [][]byte{frame, []byte("data: [DONE]\n\n")}, true, true, int64(len(frame) + len("data: [DONE]\n\n"))},
		{"正常流", 200, [][]byte{[]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"), []byte("data: [DONE]\n\n")}, false, true, 0},
		{"marker 跨块不误判（按块扫描的已知口径）", 200, [][]byte{emptyStreamMarker[:10], emptyStreamMarker[10:]}, false, true, 0},
		{"失败响应", 503, [][]byte{frame}, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			o := &exitObserver{ResponseWriter: rec, start: time.Now()}
			o.WriteHeader(tc.status)
			var total int64
			for _, chunk := range tc.chunks {
				if _, err := o.Write(chunk); err != nil {
					t.Fatal(err)
				}
				total += int64(len(chunk))
			}
			if o.emptyStream != tc.want {
				t.Fatalf("emptyStream=%v，want %v", o.emptyStream, tc.want)
			}
			if tc.wantLen != 0 && o.wroteBytes != tc.wantLen {
				t.Fatalf("wroteBytes=%d，want %d", o.wroteBytes, tc.wantLen)
			}
			if o.wroteBytes != total {
				t.Fatalf("wroteBytes=%d 与透传字节 %d 不一致", o.wroteBytes, total)
			}
			if o.status != tc.status {
				t.Fatalf("status=%d", o.status)
			}
			if o.status < 400 != tc.wantOK {
				t.Fatalf("ok 判定不符：status=%d", o.status)
			}
		})
	}
}

// TestNewRequestIDShape 锁定生成形态：32 hex；随机源不可用时的回落值也非空可辨。
func TestNewRequestIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id := newRequestID()
		if !hex32.MatchString(id) {
			t.Fatalf("ID 形态非法：%q", id)
		}
		if seen[id] {
			t.Fatalf("ID 重复：%q", id)
		}
		seen[id] = true
	}
	if strings.Contains(newRequestID(), "req-") {
		t.Fatal("正常随机源不应走回落形态")
	}
}
