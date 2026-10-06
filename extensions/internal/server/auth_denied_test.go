package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
	"workbuddy2api/internal/usagelog"
)

// TestAuthFailureRecordsDeniedTrail（RED→GREEN）：公开接口鉴权失败要留下来源网段与
// 客户端自报标识，供管理台回答「有没有人在猜 key」；同时绝不进入用量查询，
// 否则「调用次数」会被攻击流量灌高。地址只按网段落盘，主机位不写盘。
func TestAuthFailureRecordsDeniedTrail(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream: upstream.New(),
		APIKey:   "secret",
	})

	deny := func(client, userAgent string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer wrong")
		if client != "" {
			req.Header.Set("X-Console-Client", client)
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("错误 key 应 401，得到 %d", rec.Code)
		}
	}

	// 未接线（扩展没配置账本）时留痕必须是 no-op，不产生文件、不影响 401 语义。
	usagelog.Set(nil)
	deny("203.0.113.42", "python-requests/2.31")
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatalf("未接线时不得落盘：%v %v", files, err)
	}

	l, err := usagelog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	usagelog.Set(l)
	t.Cleanup(func() { usagelog.Set(nil) })
	deny("203.0.113.42", "python-requests/2.31")
	deny("", "") // 来源与客户端都不知道，这一次尝试仍要留痕

	start, end := time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Minute).Unix()
	items, err := l.QueryDenied(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("两次失败都应留痕，得到 %d：%+v", len(items), items)
	}
	if items[0].Src != "203.0.113.0/24" || items[0].Client != "python-requests/2.31" {
		t.Errorf("留痕应带脱敏网段与客户端标识：%+v", items[0])
	}
	if used, err := l.Query(start, end); err != nil || len(used) != 0 {
		t.Errorf("鉴权失败不得进入用量查询：%+v %v", used, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "denied-"+time.Now().Format("20060102")+".jsonl"))
	if err != nil {
		t.Fatalf("失败留痕应与用量分文件存放：%v", err)
	}
	if strings.Contains(string(raw), "203.0.113.42") {
		t.Fatalf("完整主机地址绝不能落盘：%s", raw)
	}
}
