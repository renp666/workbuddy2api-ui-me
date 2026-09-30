package bridge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/usagelog"
)

// TestUsageReportValidatesAndRecords：console 旁路通道用量上报端点——
// 参数与内容校验、未接线 503、落账后 GET /internal/v1/usage 可查询，
// has_credit=false 保持缺失语义（缺失≠0）。
func TestUsageReportValidatesAndRecords(t *testing.T) {
	none := New(context.Background(), Config{Key: testKey})
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"未接线", "/internal/v1/usage/reports", validUsageReport(), 503},
		{"多余查询参数", "/internal/v1/usage/reports?x=1", validUsageReport(), 400},
		{"未知字段", "/internal/v1/usage/reports", `{"entries":[],"extra":1}`, 400},
		{"空条目", "/internal/v1/usage/reports", `{"entries":[]}`, 400},
		{"超量条目", "/internal/v1/usage/reports", `{"entries":[` + strings.Repeat(`{"model":"glm-4.6"},`, 65) + `{"model":"glm-4.6"}]}`, 400},
		{"缺失模型名", "/internal/v1/usage/reports", `{"entries":[{"prompt_tokens":1}]}`, 400},
		{"空白模型名", "/internal/v1/usage/reports", `{"entries":[{"model":"  "}]}`, 400},
		{"超长模型名", "/internal/v1/usage/reports", `{"entries":[{"model":"` + strings.Repeat("m", 201) + `"}]}`, 400},
		{"尾随 JSON", "/internal/v1/usage/reports", validUsageReport() + `{}`, 400},
	} {
		w := bridgeRequest(none, "POST", tc.path, tc.body, "")
		if w.Code != tc.want {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.name, w.Code, tc.want, w.Body)
		}
	}

	dir := t.TempDir()
	l, err := usagelog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := New(context.Background(), Config{Key: testKey, Usage: l})
	credit := 0.25
	body := `{"entries":[
		{"uid":"zcode","account":"GLM 通道","model":"glm-4.6","mode":"stream","prompt_tokens":120,"completion_tokens":80,"credit":` + fmtJSONFloat(credit) + `,"has_credit":true},
		{"uid":"qoder","account":"Qoder 通道","model":"qoder-glm-4.6","mode":"sync","prompt_tokens":-1,"completion_tokens":-1}
	]}`
	w := bridgeRequest(h, "POST", "/internal/v1/usage/reports", body, "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"accepted":2`) {
		t.Fatalf("上报被拒：%d %s", w.Code, w.Body)
	}
	list := bridgeRequest(h, "GET", "/internal/v1/usage?range=today", "", "")
	if list.Code != 200 {
		t.Fatalf("查询失败：%d %s", list.Code, list.Body)
	}
	var got struct {
		Items   []usagelog.Entry `json:"items"`
		Summary usagelog.Summary `json:"summary"`
	}
	if json.Unmarshal(list.Body.Bytes(), &got) != nil || len(got.Items) != 2 {
		t.Fatalf("落账条数不符：%s", list.Body)
	}
	first, second := got.Items[0], got.Items[1]
	if first.UID != "zcode" || first.Model != "glm-4.6" || first.Prompt != 120 || first.Completion != 80 || first.Credit == nil || *first.Credit != credit {
		t.Fatalf("第一条观测不符：%+v", first)
	}
	if second.UID != "qoder" || second.Model != "qoder-glm-4.6" || second.Prompt != -1 || second.Completion != -1 || second.Credit != nil {
		t.Fatalf("第二条观测不符：%+v", second)
	}
	if got.Summary.Calls != 2 || got.Summary.Prompt != 120 || got.Summary.Completion != 80 || got.Summary.Credit != credit || got.Summary.CreditMissing != 1 || got.Summary.UsageMissing != 1 {
		t.Fatalf("汇总缺失语义不符：%+v", got.Summary)
	}
}

// TestUsageReportMethodAndAuthGate：GET 方法和错误密钥不得触达上报逻辑。
// 上报端点只注册 POST，方法不匹配落到 mux 的「/」兜底，返回 404（与本包其他端点一致）。
func TestUsageReportMethodAndAuthGate(t *testing.T) {
	h := New(context.Background(), Config{Key: testKey})
	r := httptest.NewRequest("GET", "/internal/v1/usage/reports", strings.NewReader(validUsageReport()))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("GET 上报端点应 404：%d", w.Code)
	}
	r = httptest.NewRequest("POST", "/internal/v1/usage/reports", strings.NewReader(validUsageReport()))
	r.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("错误密钥应 401：%d", w.Code)
	}
}

func validUsageReport() string {
	return `{"entries":[{"model":"glm-4.6","mode":"stream","prompt_tokens":1,"completion_tokens":2}]}`
}

func fmtJSONFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
