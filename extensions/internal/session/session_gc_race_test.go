package session

import (
	"runtime"
	"testing"
	"time"

	"workbuddy2api/internal/redisstore"
)

// newGCRouter 构造显式 GCInterval 的 Router，供 GC 关停回归测试使用。
// GCInterval 必须在 StartGC **之前**注入：GC goroutine 会读 r.cfg.GCInterval
// 起 ticker，启动之后再写 cfg 本身就是数据竞争（cfg 非并发安全），测试自身即犯规。
func newGCRouter(ttl, gcInterval time.Duration) *Router {
	return New(Config{
		TTL:        ttl,
		GCInterval: gcInterval,
		Store:      redisstore.Noop{},
		Available:  func() []string { return []string{"a1"} },
	})
}

// TestStopGCStopsGoroutine 关停语义回归：StopGC 后后台 GC goroutine 必须真的退出
// （N 轮「StartGC → StopGC」不泄漏 goroutine）。
//
// 回归背景（上游 2b8dba0）：原实现 goroutine 在 select 里每轮无锁重读 r.stop，
// 而 StopGC 持写锁把它置 nil——① 数据竞争（-race 报 Write@StopGC vs
// Read@StartGC.func1）；② 一旦读到 nil，case <-r.stop 变成「select 里的 nil
// channel」永不就绪，关停信号彻底丢失，goroutine 只能靠 ticker 无限空转；
// ③ 每次 Start/Stop 循环净泄漏一个 goroutine。修复把 stop channel 捕获进局部
// 变量，让 close 与 select 观测同一个 channel。
func TestStopGCStopsGoroutine(t *testing.T) {
	const rounds = 20
	baseline := runtime.NumGoroutine()

	for i := 0; i < rounds; i++ {
		r := newGCRouter(time.Minute, time.Millisecond)
		r.StartGC()
		// 1ms tick：让 ticker 在 goroutine 退出前至少触发数轮，覆盖
		// 「select 重新求值 r.stop」的窗口（原缺陷的触发路径）。
		time.Sleep(2 * time.Millisecond)
		r.StopGC()
	}

	// 轮询等待所有 GC goroutine 退出（有界，防挂死）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("StopGC 后 goroutine 未退出（泄漏）: baseline=%d now=%d rounds=%d",
		baseline, runtime.NumGoroutine(), rounds)
}

// TestStartGCIdempotentAndRestartable StartGC 幂等、StopGC 后可重启：
// 重复 StartGC 不叠加 goroutine，StopGC 后再 StartGC 仍能恢复 GC。
func TestStartGCIdempotentAndRestartable(t *testing.T) {
	r := newGCRouter(10*time.Millisecond, 5*time.Millisecond)
	baseline := runtime.NumGoroutine()

	r.StartGC()
	r.StartGC() // 幂等：不应起第二个
	r.StartGC()
	time.Sleep(20 * time.Millisecond)
	if n := runtime.NumGoroutine(); n > baseline+2 {
		t.Errorf("重复 StartGC 叠加了 goroutine: baseline=%d now=%d", baseline, n)
	}

	r.StopGC()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > baseline+1 {
		time.Sleep(5 * time.Millisecond)
	}

	// 重启：StopGC 已把 r.stop 置 nil，StartGC 应能重新拉起可用的 GC。
	// TTL=10ms、GCInterval=5ms：绑定建立后必然被重启的 GC 清掉。
	r.Resolve("c1")
	r.StartGC()
	r.Resolve("c2")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.Count() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.Count(); got != 0 {
		t.Errorf("重启后 GC 未生效: count=%d want 0", got)
	}
	r.StopGC()
}
