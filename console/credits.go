package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现「模型积分消耗规则」的控制面：console 从 core 的 /v1/models 采集
// credits 倍率（补丁 0011 透出），供模型表的倍率展示、能力页花费估算与计费参考。
// 开关口径（用户已裁定，2026-10-10）：调用「统一对外开放」——未配置的直名默认放行，
// 只有显式停用（禁用表）才拒绝；公共 /v1/models 的公布走启用表白名单，只有显式
// 启用的直名才上榜。倍率不再参与放行判定（trae 的倍率语义是「套餐额度消耗率」，
// opencode/autoclaw/raccoon 免费通道不采集倍率，两者都不该当停用依据）。
// 开关表复用 WB2A_ROUTE_FILE 落盘，零新增容器。

// creditCacheTTL 与探测缓存同口径：倍率规则变化以「天」为尺度，30 秒的抖动没有意义。
const creditCacheTTL = 30 * time.Second

// creditCache 缓存 core /v1/models 下发的模型积分倍率原始串。haveData=false 表示
// 从未成功取到过列表，此时 core 模型按「放行」处理——core 目录不可达时对话本身
// 也会失败，用 404 拦截只会把可诊断的 502 变成误导性的「模型不存在」。
type creditCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	byModel   map[string]string
	haveData  bool
}

// consoleCreditRules 给别名页的「模型能力 TOP」返回倍率原始串。从未取到 core 目录
// 时返回空表：页面上表现为「未公布」，缺失不等于零价。
func (h *server) consoleCreditRules(ctx context.Context) map[string]string {
	rules, haveData := h.credits.rules(h, ctx)
	if !haveData || rules == nil {
		return map[string]string{}
	}
	return rules
}

// parseCreditRule 解析上游 credits 原始串：实测形态为 "x0.79 credits"、"x0.00"、
// "x3.47"（后缀可有可无）。解析失败返回 ok=false，调用方按「未知」处理，不得伪造零。
func parseCreditRule(raw string) (float64, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, false
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "credits"))
	s = strings.TrimSpace(strings.TrimPrefix(s, "x"))
	value, err := strconv.ParseFloat(s, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// rules 返回倍率表快照；缓存过期时向 core 重新抓取，失败沿用上一份。
func (c *creditCache) rules(h *server, ctx context.Context) (map[string]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < creditCacheTTL {
		return c.byModel, c.haveData
	}
	rules, ok := h.fetchCoreCreditRules(ctx)
	c.fetchedAt = time.Now()
	if ok {
		c.byModel = rules
		c.haveData = true
	}
	return c.byModel, c.haveData
}

// store 用一份已经拿到的 core 模型目录刷新缓存，省掉公共出口的一次重复抓取。
func (c *creditCache) store(rules map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byModel = rules
	c.haveData = true
	c.fetchedAt = time.Now()
}

// creditRulesFromList 从 core /v1/models 的条目里提取 id → credits 原始串；
// 缺 credits 字段的模型不入表（缺失即「未知」，不是零）。
func creditRulesFromList(list []json.RawMessage) map[string]string {
	rules := map[string]string{}
	for _, item := range list {
		var entry struct {
			ID      string `json:"id"`
			Credits string `json:"credits"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		if entry.Credits != "" {
			rules[entry.ID] = entry.Credits
		}
	}
	return rules
}

// fetchCoreCreditRules 抓 core 的公共 /v1/models 并提取倍率表。走部署 API Key
// 的公共鉴权，与控制台管理链路无关；3 秒超时与通道目录抓取同口径。
func (h *server) fetchCoreCreditRules(ctx context.Context) (map[string]string, bool) {
	target := *h.cfg.CoreURL
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil, false
	}
	if h.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
	}
	short, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	list, ok := parseModelList(h.fetchUpstream(req.WithContext(short)))
	if !ok {
		return nil, false
	}
	return creditRulesFromList(list), true
}

// modelGate 报告公共模型名 model 当前是否放行调用（对话出口）。调用是「统一对外
// 开放」的：未配置的直名默认放行，只有管理员在模型列表/别名表显式停用（写入禁用表）
// 才拒绝——网关不锁死没配置过的模型，公布列表的收敛由 modelPublished 单独负责。
// 路由功能未启用（没有可写配置文件）时恒放行，公共出口与现状完全一致。
func (h *server) modelGate(model string) bool {
	if h.routes == nil || model == "" {
		return true
	}
	if enabled, has := h.routes.modelOverride(model); has {
		return enabled
	}
	return true
}

// modelPublished 报告直名模型是否出现在公共 /v1/models。公布走启用表白名单：
// 只有管理员显式启用的直名才上榜（「配置了哪些就开放哪些」）；停用表与未配置的
// 直名一律不公布，但仍可按名字直连调用（modelGate 不拦）。别名与 gateway-auto
// 的公布不经过这里，分别在 aliasModelEntries 与 mergedModels 里处理。
// 路由功能未启用时保持现状全公布。
func (h *server) modelPublished(model string) bool {
	if h.routes == nil || model == "" {
		return true
	}
	enabled, has := h.routes.modelOverride(model)
	return has && enabled
}

// writeModelDisabledError 按请求路径分协议返回「模型已停用」。状态用 404，
// 与「模型不存在」同构——被停用的模型同时从 /v1/models 公布名单消失，语义一致。
func writeModelDisabledError(w http.ResponseWriter, r *http.Request, model string) {
	// 只说事实与出路，不写死原因：停用只会来自管理员在模型列表或别名表的显式操作。
	message := fmt.Sprintf("模型 %s 已停用，可在控制台模型列表或别名表点「启用」恢复", model)
	if r.URL.Path == "/v1/messages" {
		writeJSON(w, 404, map[string]any{"type": "error", "error": map[string]string{"type": "not_found_error", "message": message}})
		return
	}
	writeJSON(w, 404, map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": "model_disabled"}})
}

// modelSwitchState 组装开关端点的响应：功能是否启用 + 人工覆盖表。
// 默认态由前端按同一口径从倍率推导，服务端 modelGate 才是最终裁决者。
func (h *server) modelSwitchState() map[string]any {
	overrides := map[string]bool{}
	if h.routes != nil {
		disabled, enabled := h.routes.switchLists()
		for _, model := range enabled {
			overrides[model] = true
		}
		for _, model := range disabled {
			overrides[model] = false
		}
	}
	return map[string]any{"enabled": h.routes != nil, "overrides": overrides}
}

func (h *server) adminModelSwitchState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.modelSwitchState())
}

// adminModelSwitch 落盘一次人工开关并返回最新覆盖表；保存即生效。
func (h *server) adminModelSwitch(w http.ResponseWriter, r *http.Request) {
	if h.routes == nil {
		adminError(w, 409, "模型开关未启用：未配置 console 可写路由目录")
		return
	}
	var body struct {
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeRouteBody(w, r, &body) {
		return
	}
	if err := h.routes.setModelSwitch(body.Model, body.Enabled); err != nil {
		adminError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, h.modelSwitchState())
}
