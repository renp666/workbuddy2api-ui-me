package main

// Optional browser fixture. It never contacts accounts, containers, or upstream services.
// WB2A_BROWSER_PREVIEW=1 go -C console test -run TestAdminBrowserPreview -v -timeout 20m
import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type previewTask struct {
	ID       string     `json:"id"`
	Enabled  bool       `json:"enabled"`
	Hours    []int      `json:"hours"`
	Timezone string     `json:"timezone"`
	NextAt   *time.Time `json:"next_at"`
}
type previewRun struct {
	ID           string           `json:"id"`
	TaskID       string           `json:"task_id"`
	Source       string           `json:"source"`
	RequestID    string           `json:"request_id"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   *time.Time       `json:"finished_at"`
	DurationMS   *int64           `json:"duration_ms"`
	Status       string           `json:"status"`
	Accounts     []map[string]any `json:"accounts"`
	Log          string           `json:"log"`
	LogTruncated bool             `json:"log_truncated"`
}

type previewDailyModel struct {
	Model             string  `json:"model"`
	Calls             int     `json:"calls"`
	Prompt            int64   `json:"prompt_tokens"`
	Completion        int64   `json:"completion_tokens"`
	PromptMissing     int     `json:"prompt_missing"`
	CompletionMissing int     `json:"completion_missing"`
	Credit            float64 `json:"credit"`
	CreditMissing     int     `json:"credit_missing"`
}

type previewDaily struct {
	Date              string              `json:"date"`
	Calls             int                 `json:"calls"`
	Prompt            int64               `json:"prompt_tokens"`
	Completion        int64               `json:"completion_tokens"`
	Credit            float64             `json:"credit"`
	PromptMissing     int                 `json:"prompt_missing"`
	CompletionMissing int                 `json:"completion_missing"`
	CreditMissing     int                 `json:"credit_missing"`
	Denied            int                 `json:"denied"`
	Models            []previewDailyModel `json:"models"`
}

// previewDailySeries 造近一年多的日度汇总，数值纯属模拟，但每种边界都得在图里看得见：
// 周日不干活（热力图留空档）、整日没回报 Token 的日子（斜纹档，不能画成空）、
// 只在个别日子出现且从不回报 Token 的模型（页面单列，不并入占比）、鉴权失败留痕。
func previewDailySeries(now time.Time) []previewDaily {
	names := []string{"cn:glm-5.2", "global:claude-sonnet-4.6", "glm-5.3-flash", "qoder-glm-5.3-flash", "opencode-OC · Free", "cn:deepseek-v3"}
	out := make([]previewDaily, 0, 380)
	origin := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for back := 380; back >= 0; back-- {
		day := origin.AddDate(0, 0, -back)
		if day.Weekday() == time.Sunday {
			continue
		}
		d := previewDaily{Date: day.Format("2006-01-02"), Denied: (back * 11) % 5}
		for j, name := range names {
			if (back+j)%5 == 4 {
				continue
			}
			calls := 1 + (back*(j+3))%7
			if back%37 == 5 {
				calls++
				d.Models = append(d.Models, previewDailyModel{Model: name, Calls: calls, PromptMissing: calls, CompletionMissing: calls, CreditMissing: calls})
				d.Calls += calls
				d.PromptMissing += calls
				d.CompletionMissing += calls
				d.CreditMissing += calls
				continue
			}
			prompt := int64(180+j*95) * int64(1+(back*13)%6) * int64(calls)
			completion := int64(70+j*45) * int64(1+(back*7)%5) * int64(calls)
			credit := float64(prompt+completion) / 25000
			d.Models = append(d.Models, previewDailyModel{Model: name, Calls: calls, Prompt: prompt, Completion: completion, Credit: credit})
			d.Calls += calls
			d.Prompt += prompt
			d.Completion += completion
			d.Credit += credit
		}
		if back%53 == 11 {
			d.Models = append(d.Models, previewDailyModel{Model: "glm-4.7-noquota", Calls: 3, CreditMissing: 3})
			d.Calls += 3
			d.CreditMissing += 3
		}
		if d.Calls > 0 {
			out = append(out, d)
		}
	}
	return out
}

// previewLifetime 按 core SummarizeLifetime 的同一口径汇总全部日度（与窗口无关），
// 夹具里的连续天数因此和页面读数对得上。
func previewLifetime(days []previewDaily, now time.Time) map[string]any {
	type lifetime struct {
		Calls             int64   `json:"calls"`
		Prompt            int64   `json:"prompt_tokens"`
		Completion        int64   `json:"completion_tokens"`
		Tokens            int64   `json:"tokens"`
		TokensMissing     bool    `json:"tokens_missing"`
		Credit            float64 `json:"credit"`
		Days              int     `json:"days"`
		FirstDate         string  `json:"first_date"`
		LastDate          string  `json:"last_date"`
		PeakDate          string  `json:"peak_date"`
		PeakTokens        int64   `json:"peak_tokens"`
		PeakTokensMissing bool    `json:"peak_tokens_missing"`
		CurrentStreak     int     `json:"current_streak"`
		LongestStreak     int     `json:"longest_streak"`
	}
	s := lifetime{}
	active := map[string]bool{}
	var promptMissing, completionMissing int64
	dates := make([]string, 0, len(days))
	for _, d := range days {
		s.Calls += int64(d.Calls)
		s.Prompt += d.Prompt
		s.Completion += d.Completion
		s.Credit += d.Credit
		if d.Calls == 0 {
			continue
		}
		promptMissing += int64(d.PromptMissing)
		completionMissing += int64(d.CompletionMissing)
		s.Days++
		if s.FirstDate == "" || d.Date < s.FirstDate {
			s.FirstDate = d.Date
		}
		if s.LastDate == "" || d.Date > s.LastDate {
			s.LastDate = d.Date
		}
		active[d.Date] = true
		dates = append(dates, d.Date)
		tokens := d.Prompt + d.Completion
		dark := d.PromptMissing == d.Calls && d.CompletionMissing == d.Calls
		if s.PeakDate == "" || tokens > s.PeakTokens || (tokens == s.PeakTokens && d.Date < s.PeakDate) {
			s.PeakDate, s.PeakTokens, s.PeakTokensMissing = d.Date, tokens, dark
		}
	}
	s.Tokens = s.Prompt + s.Completion
	s.TokensMissing = s.Calls > 0 && promptMissing == s.Calls && completionMissing == s.Calls
	sort.Strings(dates)
	run := 0
	for i, date := range dates {
		if i > 0 {
			prev, _ := time.Parse("2006-01-02", dates[i-1])
			cur, _ := time.Parse("2006-01-02", date)
			if cur.Sub(prev) != 24*time.Hour {
				run = 0
			}
		}
		run++
		if run > s.LongestStreak {
			s.LongestStreak = run
		}
	}
	cur := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !active[cur.Format("2006-01-02")] {
		cur = cur.AddDate(0, 0, -1)
	}
	for active[cur.Format("2006-01-02")] {
		s.CurrentStreak++
		cur = cur.AddDate(0, 0, -1)
	}
	return map[string]any{
		"calls": s.Calls, "prompt_tokens": s.Prompt, "completion_tokens": s.Completion,
		"tokens": s.Tokens, "tokens_missing": s.TokensMissing, "credit": s.Credit,
		"days": s.Days, "first_date": s.FirstDate, "last_date": s.LastDate,
		"peak_date": s.PeakDate, "peak_tokens": s.PeakTokens, "peak_tokens_missing": s.PeakTokensMissing,
		"current_streak": s.CurrentStreak, "longest_streak": s.LongestStreak,
	}
}

func TestAdminBrowserPreview(t *testing.T) {
	if os.Getenv("WB2A_BROWSER_PREVIEW") != "1" {
		t.Skip("opt-in browser fixture")
	}
	now := time.Now().UTC()
	next := now.Add(time.Hour)
	tasks := []previewTask{
		{"checkin", true, []int{9, 21}, "Asia/Shanghai", &next},
		{"travel", true, []int{9, 21}, "Asia/Shanghai", &next},
		{"activity", true, []int{10}, "Asia/Shanghai", &next},
		{"keepalive", false, []int{22}, "Asia/Shanghai", nil},
		{"school", true, []int{12}, "Asia/Shanghai", nil},
		{"cat", true, []int{1}, "Asia/Shanghai", &next},
	}
	taskIDs := []string{"cat", "school", "travel", "activity", "checkin", "keepalive"}
	statuses := []string{"success", "unknown", "partial_failure", "failed", "skipped", "interrupted", "running"}
	runs := make([]previewRun, 0, 28)
	for i := 0; i < 24; i++ {
		started := now.Add(-time.Duration(i+1) * time.Hour)
		finished, duration := started.Add(2*time.Minute), int64(120000)
		status := statuses[i%len(statuses)]
		if status == "running" {
			status = "success"
		}
		run := previewRun{ID: fmt.Sprintf("preview-history-%02d", i), TaskID: taskIDs[i%len(taskIDs)], Source: "scheduled", StartedAt: started, FinishedAt: &finished, DurationMS: &duration, Status: status, Accounts: []map[string]any{}, Log: "mock_history_event"}
		switch i {
		case 0:
			run.Log, run.LogTruncated = strings.Repeat("mock-safe-log ", 400), true
		case 1:
			run.Status, run.DurationMS = "unknown", nil
			run.Accounts = []map[string]any{{"uid": "mock-school", "status": "unknown", "detail": "result_unconfirmed", "before": nil, "after": nil, "reward": nil}}
		case 2:
			run.Status = "partial_failure"
			run.Accounts = []map[string]any{
				{"uid": "mock-ok", "status": "success", "detail": "mock_confirmed", "before": map[string]any{"value": 100, "observed_at": started}, "after": map[string]any{"value": 100, "observed_at": finished}, "reward": 0},
				{"uid": "mock-failed", "status": "failed", "detail": "mock_network_failure", "before": nil, "after": nil, "reward": nil},
			}
		}
		runs = append(runs, run)
	}
	var mu sync.Mutex
	requests := map[string]string{}
	var serial atomic.Int32
	var lostOnce atomic.Bool
	var inFlight atomic.Int32
	dailyNow := time.Now()
	dailySeries := previewDailySeries(dailyNow)
	dailyLifetime := previewLifetime(dailySeries, dailyNow)

	findRun := func(id string) (previewRun, bool) {
		for _, run := range runs {
			if run.ID == id {
				return run, true
			}
		}
		return previewRun{}, false
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/internal/") && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 32) {
			w.WriteHeader(401)
			return
		}
		if mockInfo(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/internal/v1/status":
			fmt.Fprintf(w, `{"accounts":[{"uid":"demo-account","nickname":"演示账号（模拟）","realm":"cn","credits":1200,"credits_known":true,"in_flight":%d,"success_count":2,"err_total":0,"disabled":false,"cooling":false}],"total":1,"healthy":1,"cooling":0,"disabled":0,"in_flight_full":0,"realm_totals":{"cn":{"total":1,"healthy":1,"cooling":0,"disabled":0,"in_flight_full":0},"global":{"total":0,"healthy":0,"cooling":0,"disabled":0,"in_flight_full":0}},"sticky_sessions":0,"redis_mode":"noop"}`, inFlight.Load())
		case r.URL.Path == "/internal/v1/models" || r.URL.Path == "/v1/models":
			// 两条模拟倍率：一条 0 积分、一条按量计费，让积分开关列与别名页的
			// 「模型能力 TOP」花费档位能在夹具里演示出差异（数值纯属模拟）。
			fmt.Fprint(w, `{"object":"list","data":[{"id":"cn:glm-5.2","object":"model","credits":"x0.79 credits","reasoning_supported_efforts":["low","high"]},{"id":"global:claude-sonnet-4.6","object":"model","credits":"x0.00"}]}`)
		case r.URL.Path == "/internal/v1/usage":
			// 四通道混合账本：旁路占位 uid、缺失 credit 与免费通道各来一条，
			// 让运行概览的通道分布、缺失标注和单通道筛选都有真实形态的数据。
			now := time.Now().Unix()
			fmt.Fprintf(w, `{"range":%q,"items":[`+
				`{"ts":%d,"uid":"demo-account","account":"演示账号（模拟）","model":"cn:glm-5.2","mode":"stream","prompt_tokens":120,"completion_tokens":860,"credit":3.5},`+
				`{"ts":%d,"uid":"demo-account","account":"演示账号（模拟）","model":"global:claude-sonnet-4.6","mode":"sync","prompt_tokens":400,"completion_tokens":1500},`+
				`{"ts":%d,"uid":"zcode","account":"GLM 通道","model":"glm-5.3-flash","mode":"stream","prompt_tokens":80,"completion_tokens":640,"credit":0},`+
				`{"ts":%d,"uid":"qoder","account":"Qoder 通道","model":"qoder-glm-5.3-flash","mode":"stream","prompt_tokens":60,"completion_tokens":520,"credit":1.2},`+
				`{"ts":%d,"uid":"opencode","account":"OpenCode 通道","model":"opencode-OC · FreeModel","mode":"stream","prompt_tokens":30,"completion_tokens":240,"credit":0}`+
				`],"summary":{"calls":5}}`, r.URL.Query().Get("range"), now-300, now-240, now-180, now-120, now-60)
		case r.URL.Path == "/internal/v1/usage/daily":
			// 日度汇总：lifetime 恒按全历史给，窗口只切 days，与 core 端点同构。
			name := r.URL.Query().Get("window")
			visible := dailySeries
			if span := map[string]int{"30d": 30, "90d": 90, "365d": 365}[name]; span > 0 {
				cutoff := time.Date(dailyNow.Year(), dailyNow.Month(), dailyNow.Day(), 0, 0, 0, 0, dailyNow.Location()).AddDate(0, 0, -(span - 1)).Format("2006-01-02")
				visible = make([]previewDaily, 0, len(dailySeries))
				for _, d := range dailySeries {
					if d.Date >= cutoff {
						visible = append(visible, d)
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"window": name, "days": visible, "lifetime": dailyLifetime})
		case r.URL.Path == "/internal/v1/chat":
			inFlight.Add(1)
			defer inFlight.Add(-1)
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{"你好！", "这是控制台的模拟回答。", "流式显示和页面交互已连通。"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					t.Log("mock chat canceled: browser disconnect propagated to core")
					return
				case <-time.After(3 * time.Second):
				}
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":20,\"total_tokens\":25}}\n\ndata: [DONE]\n\n")
		case r.URL.Path == "/internal/v1/messages":
			inFlight.Add(1)
			defer inFlight.Add(-1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_mock\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"mock-model\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":null,\"output_tokens\":null}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			for _, part := range []string{"你好！", "这是 Anthropic 协议的模拟回答。", "文本与流式交互已连通。"} {
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", part)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					t.Log("mock messages canceled: browser disconnect propagated to core")
					return
				case <-time.After(3 * time.Second):
				}
			}
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":null,\"output_tokens\":null}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case r.Method == "GET" && r.URL.Path == "/internal/v1/tasks":
			mu.Lock()
			defer mu.Unlock()
			var active *previewRun
			latest, seen := []previewRun{}, map[string]bool{}
			for i := range runs {
				if runs[i].Status == "running" && active == nil {
					copy := runs[i]
					active = &copy
				}
				if !seen[runs[i].TaskID] {
					seen[runs[i].TaskID] = true
					latest = append(latest, runs[i])
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"items": tasks, "active_run": active, "latest_runs": latest})
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/internal/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/runs"):
			taskID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/tasks/"), "/runs")
			var body struct {
				RequestID string `json:"request_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"bad mock body"}`)
				return
			}
			mu.Lock()
			if oldID := requests[body.RequestID]; oldID != "" {
				run, _ := findRun(oldID)
				mu.Unlock()
				w.WriteHeader(202)
				json.NewEncoder(w).Encode(run)
				return
			}
			for _, run := range runs {
				if run.Status == "running" {
					mu.Unlock()
					w.WriteHeader(409)
					json.NewEncoder(w).Encode(map[string]any{"error": "已有 mock 任务正在运行", "run_id": run.ID})
					return
				}
			}
			id := fmt.Sprintf("preview-manual-%02d", serial.Add(1))
			started := time.Now().UTC()
			run := previewRun{ID: id, TaskID: taskID, Source: "manual", RequestID: body.RequestID, StartedAt: started, Status: "running", Accounts: []map[string]any{}, Log: "mock_execution_count_1"}
			requests[body.RequestID] = id
			runs = append([]previewRun{run}, runs...)
			mu.Unlock()
			if taskID == "checkin" && !lostOnce.Swap(true) {
				mu.Lock()
				finished, duration := started.Add(time.Second), int64(1000)
				runs[0].FinishedAt, runs[0].DurationMS, runs[0].Status = &finished, &duration, "success"
				runs[0].Accounts = []map[string]any{{"uid": "mock-checkin", "status": "success", "detail": "accepted_before_connection_loss", "before": nil, "after": map[string]any{"value": 0, "observed_at": finished}, "reward": 0}}
				mu.Unlock()
				panic(http.ErrAbortHandler)
			}
			if taskID == "activity" {
				go func(id string) {
					time.Sleep(3 * time.Second)
					mu.Lock()
					defer mu.Unlock()
					for i := range runs {
						if runs[i].ID == id {
							finished, duration := time.Now().UTC(), int64(3000)
							runs[i].FinishedAt, runs[i].DurationMS, runs[i].Status = &finished, &duration, "partial_failure"
							runs[i].Accounts = []map[string]any{{"uid": "mock-ok", "status": "success", "detail": "one_click_only", "before": nil, "after": nil, "reward": nil}, {"uid": "mock-failed", "status": "failed", "detail": "mock_partial_failure", "before": nil, "after": nil, "reward": nil}}
						}
					}
				}(id)
			}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(run)
		case r.Method == "GET" && r.URL.Path == "/internal/v1/task-runs":
			mu.Lock()
			defer mu.Unlock()
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit == 0 {
				limit = 20
			}
			start := 0
			if before := r.URL.Query().Get("before"); before != "" {
				start = len(runs)
				for i := range runs {
					if runs[i].ID == before {
						start = i + 1
						break
					}
				}
			}
			end := start + limit
			if end > len(runs) {
				end = len(runs)
			}
			var nextBefore any
			if end < len(runs) && end > start {
				nextBefore = runs[end-1].ID
			}
			json.NewEncoder(w).Encode(map[string]any{"items": runs[start:end], "next_before": nextBefore})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/internal/v1/task-runs/"):
			mu.Lock()
			defer mu.Unlock()
			run, ok := findRun(strings.TrimPrefix(r.URL.Path, "/internal/v1/task-runs/"))
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":"mock run not found"}`)
				return
			}
			json.NewEncoder(w).Encode(run)
		default:
			if strings.HasPrefix(r.URL.Path, "/internal/v1/owners/") {
				fmt.Fprint(w, `{"ok":true}`)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/internal/v1/oauth") {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"浏览器夹具不发起真实授权；OAuth 由自动测试验证"}`)
				return
			}
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"mock route not found"}`)
		}
	}))
	defer ts.Close()
	// 旁路通道夹具：让运行概览的四通道表、模型探测按钮与速度列都能在模拟环境展示。
	zcodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.3-flash"},{"id":"glm-5.3"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":12}}\n\ndata: [DONE]\n\n")
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer zcodeTS.Close()
	qoderTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"glm-5.3-flash"},{"id":"qwen3-coder"}]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":9}}\n\ndata: [DONE]\n\n")
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer qoderTS.Close()
	opencodeTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"OC · FreeModel"},{"id":"OC · Reason"}]}`)
		case "/health":
			fmt.Fprint(w, `{"phase":"ready","version":"mock","modelResults":{"OC · FreeModel":{"ok":true,"category":"available","durationMs":120},"OC · Reason":{"ok":true,"chatOnly":true,"durationMs":400}}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer opencodeTS.Close()
	cfg := testConfig(ts.URL)
	cfg.AdminKey = "browser-preview-key-mock-only-12345"
	cfg.APIKey = "preview-api-key-mock-only"
	cfg.ZCodeURL, _ = url.Parse(zcodeTS.URL)
	cfg.ZCodeKey = strings.Repeat("z", 32)
	cfg.QoderURL, _ = url.Parse(qoderTS.URL)
	cfg.QoderKey = strings.Repeat("q", 32)
	cfg.OpenCodeURL, _ = url.Parse(opencodeTS.URL)
	cfg.OpenCodeKey = strings.Repeat("o", 32)
	cfg.RouteFile = filepath.Join(t.TempDir(), "routes.json")
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 默认只绑回环。WB2A_BROWSER_PREVIEW_ADDR 是给「本机没有 Go 工具链、夹具得在容器里跑」
	// 留的口子：容器里绑回环的端口宿主发布不到，改成 0.0.0.0:<port> 才能从浏览器打开。
	addr := os.Getenv("WB2A_BROWSER_PREVIEW_ADDR")
	if addr == "" {
		addr = "127.0.0.1:17864"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	t.Logf("Browser fixture: http://127.0.0.1:%s · admin key: browser-preview-key-mock-only-12345 (mock only) · bind %s", port, addr)
	t.Log("Tasks: checkin loses its first accepted response then retries same ID; activity stays running 3s for rapid repeat; seeded partial/unknown/truncated records and 24-item pagination survive reloads.")
	<-time.After(15 * time.Minute)
}
