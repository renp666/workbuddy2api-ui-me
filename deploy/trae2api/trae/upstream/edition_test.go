package upstream

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"workbuddy2api-trae/trae/auth"
)

func TestEditionCNPresetMatchesConstants(t *testing.T) {
	cn := EditionCN()
	if cn.AgentHost != AgentHost || cn.UgHost != UgHost || cn.OAuthHost != OAuthHost || cn.ConsoleHost != ConsoleHost {
		t.Fatalf("CN 预设必须与 constants.go 逐字一致，否则国内版行为随重构漂移: %+v", cn)
	}
	if !cn.CheckinSupported || cn.KeepCustomModels || cn.EntitlementMode != "credits" {
		t.Errorf("CN 权益口径被改动: %+v", cn)
	}
	if cn.DefaultModelName != DefaultConfigName {
		t.Errorf("CN 默认模型 = %q, want %q", cn.DefaultModelName, DefaultConfigName)
	}
}

func TestEditionIntlPreset(t *testing.T) {
	intl := EditionIntl()
	// 这四个 host 是 2026-10-16 无凭据存在性探测 + 真账号目录/对话实测的结果：
	// api.trae.ai 域名最像但对本通道不可用，不能回到它。
	for _, f := range []struct{ got, want string }{
		{intl.AgentHost, "https://coresg-normal.trae.ai"},
		{intl.UgHost, "https://ug-normal.trae.ai"},
		{intl.OAuthHost, "https://ug-normal.trae.ai"},
		{intl.ConsoleHost, "https://www.trae.ai"},
	} {
		if f.got != f.want {
			t.Errorf("intl host = %q, want %q", f.got, f.want)
		}
	}
	if intl.CheckinSupported {
		t.Error("intl 无签到体系，CheckinSupported 必须为 false")
	}
	if intl.EpEntUsage != "" || intl.EpCheckinStatus != "" {
		t.Error("intl 不应带国内版积分/签到端点，否则会有人真的去打它拿 404")
	}
	if intl.EpSubscription != "/trae/api/v1/pay/ide_user_pay_status" {
		t.Errorf("intl 权益端点 = %q（实测是 v1，不是国内版的 v2）", intl.EpSubscription)
	}
	if intl.EntitlementMode != "subscription" {
		t.Errorf("intl 权益口径 = %q, want subscription", intl.EntitlementMode)
	}
	// client_id 由"是不是 SOLO"决定，不由区域决定（本机 product.json 差分实测）。
	if intl.ClientID != ClientID {
		t.Errorf("intl client_id = %q, want 与国内版同值 %q", intl.ClientID, ClientID)
	}
	if !intl.KeepCustomModels {
		t.Error("intl 的 custom_model_* 是 BYOK 槽位而非本网关别名回流，不应剔除")
	}
}

// TestZeroEditionInheritsCNBools 钉住那个最容易咬人的点：测试与旧代码习惯用
// Edition{AgentHost: srv.URL} 这种只填 host 的字面量，此时两个布尔必须落在国内版上。
// 逐字段兜底会把 CheckinSupported 判成 false，于是签到用例静默走了国际版分支还全绿。
func TestZeroEditionInheritsCNBools(t *testing.T) {
	ed := Edition{AgentHost: "https://srv.example"}.normalized()
	if !ed.CheckinSupported {
		t.Error("零 Key Edition 继承了国际版的『无签到』，国内版用例会静默跳过签到链路")
	}
	if ed.KeepCustomModels {
		t.Error("零 Key Edition 丢了别名回环剔除")
	}
	if ed.AgentHost != "https://srv.example" {
		t.Errorf("显式 host 覆盖被兜底冲掉: %q", ed.AgentHost)
	}
	if ed.UgHost != UgHost || ed.ClientID != ClientID {
		t.Errorf("未显式设置的字段没有回落 CN: %+v", ed)
	}
}

func TestFromEnvEditionAndOverrides(t *testing.T) {
	t.Setenv("TRAE_EDITION", "intl")
	ed := FromEnv()
	if ed.Key != "intl" {
		t.Fatalf("TRAE_EDITION=intl 没生效: %q", ed.Key)
	}
	t.Setenv("TRAE_AGENT_HOST", "https://coreva-normal.trae.ai") // 美区入口
	if got := FromEnv().AgentHost; got != "https://coreva-normal.trae.ai" {
		t.Errorf("单项覆盖没生效: %q", got)
	}

	t.Setenv("TRAE_EDITION", "")
	if got := FromEnv().Key; got != "cn" {
		t.Errorf("未设 TRAE_EDITION 应为 cn（生产入口零变更），got %q", got)
	}

	t.Setenv("TRAE_EDITION", "中文别名")
	if got := FromEnv().Key; got != "cn" {
		t.Errorf("未知区域应回落 cn，got %q", got)
	}
}

func TestHostDomain(t *testing.T) {
	cases := []struct {
		ed   Edition
		want string
	}{
		{EditionCN(), "trae.com.cn"},
		{EditionIntl(), "trae.ai"},
	}
	for _, c := range cases {
		if got := c.ed.HostDomain(); got != c.want {
			t.Errorf("%s HostDomain = %q, want %q", c.ed.Key, got, c.want)
		}
	}
}

// TestNewUsesGivenEdition 保证 New(ed) 之后客户端真正用该区域，而不是内部又取默认。
func TestNewUsesGivenEdition(t *testing.T) {
	c := New(EditionIntl())
	if c.Edition().AgentHost != "https://coresg-normal.trae.ai" {
		t.Errorf("New(EditionIntl()) 的 agentBase = %q", c.agentBase())
	}
	if c.ugBase() != "https://ug-normal.trae.ai" {
		t.Errorf("ugBase = %q", c.ugBase())
	}
}

// failRT 一被调用就让测试失败：用来证明"国际版签到在发出请求前就短路"，
// 而不是靠读代码相信它没发。
type failRT struct{ t *testing.T }

func (f failRT) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("不该发出这个请求")
	return nil, errors.New("unreachable")
}

func TestCheckinUnsupportedShortCircuits(t *testing.T) {
	// 国际版发签到请求必得 404，客户端必须在发出之前就短路，且不能碰网络。
	c := New(EditionIntl())
	c.HTTP = &http.Client{Transport: failRT{t}}
	if _, _, _, err := c.CheckinStatus(nil); err != ErrCheckinUnsupported {
		t.Errorf("CheckinStatus err = %v, want ErrCheckinUnsupported", err)
	}
	if err := c.CheckinClaim(nil); err != ErrCheckinUnsupported {
		t.Errorf("CheckinClaim err = %v, want ErrCheckinUnsupported", err)
	}
	if _, _, _, _, err := c.EntUsage(nil); err != ErrCheckinUnsupported {
		t.Errorf("EntUsage err = %v, want ErrCheckinUnsupported", err)
	}
}

// TestFetchSubscriptionParsesRealAPIShape 用真 API 的响应形状做锚定。
//
// 之所以单独钉这条：客户端本地缓存 iCubeEntitlementInfo 里是另一份相近但**不同名**的
// 数据（identityStr/hasPackage/enableEntitlement）。按那份解析真接口会全员解成空值，
// 页面显示"未知"却查不出原因（2026-10-16 实测踩过）。
func TestFetchSubscriptionParsesRealAPIShape(t *testing.T) {
	const payStatusBody = `{
		"billing_version":3,"has_package":false,"is_dollar_usage_billing":true,
		"user_pay_identity":1,"user_pay_identity_str":"Pro",
		"detail":{"fast_request_per":6,"permission":[]}}`
	var path string
	c := New(EditionIntl())
	c.HTTP = &http.Client{Transport: rtCapture{fn: func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		return &http.Response{StatusCode: 200,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(payStatusBody))}, nil
	}}}
	sub, err := c.FetchSubscription(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/trae/api/v1/pay/ide_user_pay_status" {
		t.Errorf("path = %q, want v1 权益端点", path)
	}
	if sub.Identity != "Pro" || sub.IdentityID != 1 || !sub.DollarUsageBilling {
		t.Errorf("解错: %+v", sub)
	}
	if sub.FastRequest != 6 {
		t.Errorf("FastRequest = %d, want 6（detail.fast_request_per）", sub.FastRequest)
	}
	if sub.HasPackage {
		t.Errorf("HasPackage = true, want false")
	}
}

type rtCapture struct {
	fn func(*http.Request) (*http.Response, error)
}

func (r rtCapture) RoundTrip(req *http.Request) (*http.Response, error) { return r.fn(req) }

func TestFetchSubscriptionUnsupportedOnCN(t *testing.T) {
	c := New(EditionCN())
	c.HTTP = &http.Client{Transport: failRT{t}}
	if _, err := c.FetchSubscription(nil); err != ErrCheckinUnsupported {
		t.Errorf("国内版没有 EpSubscription，应短路: %v", err)
	}
}
