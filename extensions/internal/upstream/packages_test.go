package upstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestUserResourcePackagesSplitsByExpiry 到期明细逐包透出：升序、零余额包剔除、
// 无到期日排在最后、空到期时间不产生 end_at。
func TestUserResourcePackagesSplitsByExpiry(t *testing.T) {
	now := time.Now()
	in3d := now.Add(3 * 24 * time.Hour).Format(packageEndLayout)
	in30d := now.Add(30 * 24 * time.Hour).Format(packageEndLayout)
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/v2/billing/meter/get-user-resource") {
			return nil, errors.New("wrong path: " + r.URL.Path)
		}
		return mkDetailedResp(
			`{"PackageName":"签到包","PackageEndTime":"`+in30d+`","CycleCapacitySize":2000,"CycleCapacityRemain":300,"CycleCapacityUsed":1700},` +
				`{"PackageName":"奖励包","PackageEndTime":"`+in3d+`","CycleCapacitySize":1500,"CycleCapacityRemain":1200,"CycleCapacityUsed":300},` +
				`{"PackageName":"已耗尽","PackageEndTime":"`+in3d+`","CycleCapacitySize":500,"CycleCapacityRemain":0,"CycleCapacityUsed":500},` +
				`{"PackageName":"长期包","CycleCapacitySize":900,"CycleCapacityRemain":900,"CycleCapacityUsed":0}`), nil
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at"}
	packages, err := c.UserResourcePackages(a)
	if err != nil {
		t.Fatalf("packages: %v", err)
	}
	if len(packages) != 3 {
		t.Fatalf("packages=%+v want 3 (零余额包剔除)", packages)
	}
	if packages[0].Name != "奖励包" || packages[0].Remain != 1200 || packages[0].EndAt == "" {
		t.Errorf("first=%+v want 奖励包(3天)", packages[0])
	}
	if _, perr := time.Parse(time.RFC3339, packages[0].EndAt); perr != nil {
		t.Errorf("end_at %q should be RFC3339: %v", packages[0].EndAt, perr)
	}
	if packages[1].Name != "签到包" {
		t.Errorf("second=%+v want 签到包(30天)", packages[1])
	}
	if packages[2].Name != "长期包" || packages[2].EndAt != "" {
		t.Errorf("last=%+v want 长期包 无到期日", packages[2])
	}
}

// TestUserResourcePackagesCNPathUnchanged 零回归：CN 账号仍是单条 /v2 路径。
func TestUserResourcePackagesCNPathUnchanged(t *testing.T) {
	var calls []string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		return jsonResp(200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[]}}}}`), nil
	})
	a := &auth.Auth{UID: "cn1", AccessToken: "at"}
	packages, err := c.UserResourcePackages(a)
	if err != nil {
		t.Fatalf("packages: %v", err)
	}
	if len(packages) != 0 {
		t.Errorf("packages=%+v want empty", packages)
	}
	if len(calls) != 1 || calls[0] != "/v2/billing/meter/get-user-resource" {
		t.Errorf("calls=%v want single /v2 path", calls)
	}
}

// TestUserResourcePackagesBadEndKept 解析失败的到期时间按「无到期日」处理，
// 不让整个账号的分布查询失败。
func TestUserResourcePackagesBadEndKept(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		return mkDetailedResp(`{"PackageName":"怪包","PackageEndTime":"not-a-date","CycleCapacitySize":100,"CycleCapacityRemain":80,"CycleCapacityUsed":20}`), nil
	})
	packages, err := c.UserResourcePackages(&auth.Auth{UID: "u2", AccessToken: "at"})
	if err != nil {
		t.Fatalf("packages: %v", err)
	}
	if len(packages) != 1 || packages[0].Remain != 80 || packages[0].EndAt != "" {
		t.Errorf("packages=%+v want 怪包 remain=80 无到期日", packages)
	}
}
