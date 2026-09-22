package upstream

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
)

// TestOutboundHeadersRaceRefreshToken 并发下出站请求头读取与 RefreshToken 写入的
// 数据竞争回归（对齐上游 910b8b2 的 TestChatHeadersRacesRefreshToken 对抗思路）。
//
// 上游用 AccessTokenValue()/DomainValue() 加锁访问器关竞争；本仓走另一条路线：
// 补丁 0001 给 auth 加锁访问器 + Auth.Snapshot()，补丁 0002 在每个出站函数开头
// `a = a.Snapshot()` 拿锁内私有副本，后续字段直读都落在副本上。本测试就是这条
// 路线的 -race 守卫：删掉任一 Snapshot() 即复现告警。
//
// 写侧模拟 scheduler keepalive 周期刷新（与账号是否有在途请求无关），读侧模拟
// 在途请求构造 chat / billing 头——Pool 返回的是池内同一个 *auth.Auth 指针，
// 两侧在生产里确实并发。必须断言刷新真的成功过，否则竞争面为空也会计为通过。
func TestOutboundHeadersRaceRefreshToken(t *testing.T) {
	var refreshed atomic.Int64
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		// domain 也返回非空值：让 RefreshToken 的 a.Domain 赋值真正执行，
		// 把 Domain 读侧一并拉进竞争面。
		refreshed.Add(1)
		return jsonResp(200, `{"code":0,"data":{"accessToken":"newat","refreshToken":"newrt","domain":"chat.example.com","expiresIn":3600}}`), nil
	})
	a := &auth.Auth{UID: "u-race", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = c.RefreshToken(a)
		}
	}()

	chatReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, "https://chat.example/v2/chat/completions", nil)
		if err != nil {
			t.Fatalf("new chat request: %v", err)
		}
		return req
	}
	billReq := func() *http.Request {
		req, err := http.NewRequest(http.MethodGet, "https://billing.example/v2/billing/quota", nil)
		if err != nil {
			t.Fatalf("new billing request: %v", err)
		}
		return req
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		req := chatReq()
		for i := 0; i < 300; i++ {
			c.ChatHeaders(req, a, "", ChatMeta{})
			_ = req.Header.Get("Authorization")
		}
		close(stop)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := billReq()
		for i := 0; i < 300; i++ {
			c.BillingHeaders(req, a)
			_ = req.Header.Get("Authorization")
		}
	}()

	wg.Wait()
	if refreshed.Load() == 0 {
		t.Fatal("refresh never succeeded: race surface is empty, test would pass vacuously")
	}
}
