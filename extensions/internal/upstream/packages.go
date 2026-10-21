// 积分包到期明细（扩展能力，对标 panel 的积分到期分布）。
//
// 上游 billing 的 get-user-resource 本就按积分包返回 PackageEndTime（"2006-01-02
// 15:04:05" UTC+8 墙钟），UserResourceDetailed 只把它折叠成 remain + 快过期桶，
// 到期时间被丢弃。本文件补一个不折叠的查询：逐包给出名称、剩余与到期时刻，
// 供 bridge 汇聚成账号级「积分到期分布」。请求路径与鉴权完全复用既有 helpers，
// 不新增上游端点、不改既有行为。
package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"workbuddy2api/internal/auth"
)

// CreditPackage 单个积分包的到期观测。EndAt 为零值字符串（""）表示无到期日
// （上游 PackageEndTime 缺省/空）；非空时为 RFC3339（UTC+8 墙钟换算的绝对时刻）。
type CreditPackage struct {
	Name   string `json:"name"`
	EndAt  string `json:"end_at,omitempty"`
	Remain int64  `json:"remain"`
}

// UserResourcePackages 拉取账号积分包明细（仅保留 remain>0 的包，按到期时间升序、
// 无到期日在后）。与 UserResourceDetailed 相同的请求体与 remain 口径
// （CycleCapacity 优先，回退 Capacity），只是不做窗口折叠。
func (c *Client) UserResourcePackages(a *auth.Auth) ([]CreditPackage, error) {
	now := time.Now()
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format(packageEndLayout),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format(packageEndLayout),
	}
	data, err := c.billingMeterJSON(a, c.billingMeterPaths(a), http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			Data struct {
				Accounts []struct {
					PackageName         string `json:"PackageName"`
					PackageEndTime      string `json:"PackageEndTime"`
					CapacitySize        int64  `json:"CapacitySize"`
					CapacityRemain      int64  `json:"CapacityRemain"`
					CapacityUsed        int64  `json:"CapacityUsed"`
					CycleCapacitySize   int64  `json:"CycleCapacitySize"`
					CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
					CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
				} `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("resource parse: %w", err)
	}
	packages := make([]CreditPackage, 0, len(resp.Response.Data.Accounts))
	for _, acct := range resp.Response.Data.Accounts {
		var r int64
		switch {
		case acct.CycleCapacitySize > 0:
			r = acct.CycleCapacityRemain
		case acct.CycleCapacityRemain > 0 || acct.CycleCapacityUsed > 0:
			r = acct.CycleCapacityRemain
		default:
			r = acct.CapacityRemain
		}
		if r <= 0 {
			continue // 零余额包对到期分布无贡献，不占条目
		}
		pkg := CreditPackage{Name: acct.PackageName, Remain: r}
		if acct.PackageEndTime != "" {
			if end, perr := time.ParseInLocation(packageEndLayout, acct.PackageEndTime, softRateResetLoc); perr == nil {
				pkg.EndAt = end.Format(time.RFC3339)
			}
		}
		packages = append(packages, pkg)
	}
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].EndAt == packages[j].EndAt {
			return packages[i].Name < packages[j].Name
		}
		if packages[i].EndAt == "" {
			return false
		}
		if packages[j].EndAt == "" {
			return true
		}
		return packages[i].EndAt < packages[j].EndAt
	})
	return packages, nil
}
