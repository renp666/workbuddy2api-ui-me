// 积分包到期汇聚端点（扩展能力，对标 panel 的积分到期分布）。
//
// GET /internal/v1/credit-packages：逐账号调用 upstream.UserResourcePackages，
// 汇聚成账号级明细供前端按到期窗口聚合。按账号缓存（成功 10 分钟 / 失败 2 分钟，
// refresh=1 跳过缓存读取仍回写），并发 3 个 worker 控制外呼节奏；单号失败不影响
// 其他号，错误以固定文案入响应（原始错误只进日志，避免透出上游细节）。
package bridge

import (
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

const (
	creditPackageSuccessTTL = 10 * time.Minute
	creditPackageErrorTTL   = 2 * time.Minute
	creditPackageWorkers    = 3
)

// creditPackageItem 单账号的到期明细。Error 非空表示该账号本次探测失败
// （Packages 为空）；零余额包不产生条目。
type creditPackageItem struct {
	UID      string                   `json:"uid"`
	Realm    string                   `json:"realm,omitempty"`
	Nickname string                   `json:"nickname,omitempty"`
	Credits  int64                    `json:"credits"`
	Disabled bool                     `json:"disabled"`
	Packages []upstream.CreditPackage `json:"packages"`
	Error    string                   `json:"error,omitempty"`
}

// creditPackageEntry 缓存行：at 是写入时刻，failed 决定 TTL 档。
type creditPackageEntry struct {
	item   creditPackageItem
	at     time.Time
	failed bool
}

// creditPackages 处理 GET /internal/v1/credit-packages。查询参数只认 refresh=1。
func (h *handler) creditPackages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	for key := range query {
		if key != "refresh" {
			bridgeError(w, 400, "查询参数无效")
			return
		}
	}
	if v := query.Get("refresh"); v != "" && v != "1" {
		bridgeError(w, 400, "查询参数无效")
		return
	}
	if h.cfg.Pool == nil || h.cfg.Upstream == nil {
		bridgeError(w, 503, "账号池尚未就绪，请稍后重试")
		return
	}
	refresh := query.Get("refresh") == "1"
	statuses := h.cfg.Pool.List()
	now := time.Now()
	items := make([]creditPackageItem, 0, len(statuses))
	var stale []pool.Status
	for _, st := range statuses {
		if !refresh {
			if e, ok := h.creditCache.Load(st.UID); ok {
				entry := e.(*creditPackageEntry)
				ttl := creditPackageSuccessTTL
				if entry.failed {
					ttl = creditPackageErrorTTL
				}
				if now.Sub(entry.at) < ttl {
					items = append(items, entry.item)
					continue
				}
			}
		}
		stale = append(stale, st)
	}
	if len(stale) > 0 {
		var mu sync.Mutex
		next := 0
		var wg sync.WaitGroup
		for worker := 0; worker < creditPackageWorkers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					mu.Lock()
					i := next
					next++
					mu.Unlock()
					if i >= len(stale) || r.Context().Err() != nil {
						return
					}
					item := h.probeCreditPackages(stale[i], now)
					h.creditCache.Store(item.UID, &creditPackageEntry{item: item, at: time.Now(), failed: item.Error != ""})
					mu.Lock()
					items = append(items, item)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}
	sort.SliceStable(items, func(i, j int) bool {
		ei, ej := items[i].Error != "", items[j].Error != ""
		if ei != ej {
			return !ei
		}
		ni, nj := earliestEndAt(items[i]), earliestEndAt(items[j])
		if (ni == "") != (nj == "") {
			return nj == ""
		}
		if ni != nj {
			return ni < nj
		}
		return items[i].UID < items[j].UID
	})
	writeJSON(w, 200, map[string]any{"fetched_at": now.Format(time.RFC3339), "items": items})
}

// probeCreditPackages 探测单个账号：无凭据或上游失败都收敛成固定错误文案，
// 原始错误只记日志（账号 UID 非敏感，可入日志；上游错误细节不透出响应）。
func (h *handler) probeCreditPackages(st pool.Status, now time.Time) creditPackageItem {
	item := creditPackageItem{UID: st.UID, Realm: st.Realm, Nickname: st.Nickname, Credits: st.Credits, Disabled: st.Disabled}
	a := h.cfg.Pool.AuthByUID(st.UID)
	if a == nil || a.AccessToken == "" {
		item.Error = "无可用凭据"
		return item
	}
	packages, err := h.cfg.Upstream.UserResourcePackages(a)
	if err != nil {
		log.Printf("credit-packages %s: %v", st.UID, err)
		item.Error = "查询失败，稍后重试"
		return item
	}
	item.Packages = packages
	return item
}

// earliestEndAt 返回账号所有包里最早的到期时间（RFC3339 字符串序即时间序；
// 无任何到期日返回 ""）。
func earliestEndAt(item creditPackageItem) string {
	earliest := ""
	for _, p := range item.Packages {
		if p.EndAt != "" && (earliest == "" || p.EndAt < earliest) {
			earliest = p.EndAt
		}
	}
	return earliest
}
