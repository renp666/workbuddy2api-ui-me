package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// zcodeModelPrefix routes model names starting with this prefix to the optional
// zcode-proxy upstream. Core model ids are all "cn:"/"global:" namespaced, so there is no collision.
const zcodeModelPrefix = "glm-"

// zcodeRouteBodyLimit bounds the buffered request body we inspect for the model field.
const zcodeRouteBodyLimit = 32 << 20

// zcodeModelsBodyLimit bounds each upstream /v1/models response we merge.
const zcodeModelsBodyLimit = 4 << 20

func (h *server) zcodeProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.ZCodeURL)
			stripPrivateHeaders(pr.Out.Header)
			// Never leak the deployment API key (Anthropic clients send it as x-api-key)
			// to the third-party container; only the dedicated zcode key may cross that hop.
			pr.Out.Header.Del("X-Api-Key")
			if h.cfg.ZCodeKey != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
			} else {
				pr.Out.Header.Del("Authorization")
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "GLM 服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "GLM 服务不可达，请稍后重试")
		},
	}
}

// traeDispatch 是一条 Trae 区域通道在公共分流里的条目（国内 trae-* / 国际
// traeai-*）。两条通道的公共面由 console/trae.go 的 traeChannel 描述符循环注册；
// 提升到包级供请求内重试的通道派发（retryDeps）复用。
type traeDispatch struct {
	prefix string
	uid    string
	proxy  *httputil.ReverseProxy
}

// publicRouter dispatches the public /v1/* surface: glm-* model chat/messages/responses requests
// go to zcode-proxy, qoder-* model requests go to qoder-proxy, opencode-* model requests go to
// the opencode sidecar, autoclaw-* model requests go to the autoclaw2api sidecar,
// trae-* model requests go to the trae2api sidecar, raccoon-* model requests go to
// the raccoon2api sidecar, freellm-* model requests go to the FreeLLMAPI
// sidecar, everything else stays on core.
func (h *server) publicRouter(core *httputil.ReverseProxy) http.HandlerFunc {
	var zcode, qoder, opencode, autoclaw, raccoon, freellm *httputil.ReverseProxy
	// 前缀必须互不包含：traeai-gpt-5.4 不以 trae- 开头，所以两条分支不会互相剥错名字。
	var traeRoutes []traeDispatch
	if h.cfg.ZCodeURL != nil {
		zcode = h.zcodeProxy()
	}
	if h.cfg.QoderURL != nil {
		qoder = h.qoderProxy()
	}
	if h.cfg.OpenCodeURL != nil {
		opencode = h.opencodeProxy()
	}
	if h.cfg.AutoClawURL != nil {
		autoclaw = h.autoclawProxy()
	}
	for _, ch := range h.enabledTraeChannels() {
		traeRoutes = append(traeRoutes, traeDispatch{prefix: ch.prefix, uid: string(ch.channel), proxy: h.traeProxy(ch)})
	}
	if h.cfg.RaccoonURL != nil {
		raccoon = h.raccoonProxy()
	}
	if h.cfg.FreellmURL != nil {
		freellm = h.freellmProxy()
	}
	deps := retryDeps{core: core, zcode: zcode, qoder: qoder, opencode: opencode, autoclaw: autoclaw, trae: traeRoutes, raccoon: raccoon, freellm: freellm}
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			h.mergedModels(w, r, core)
			return
		case "POST /v1/chat/completions", "POST /v1/messages", "POST /v1/responses":
			// Buffer body once, inspect model field, restore for downstream.
			body, err := io.ReadAll(io.LimitReader(r.Body, zcodeRouteBodyLimit+1))
			if err != nil {
				adminError(w, 400, "请求体读取失败")
				return
			}
			if len(body) > zcodeRouteBodyLimit {
				adminError(w, 413, "请求体超过 32 MiB 分流检查上限")
				return
			}
			var envelope struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if json.Unmarshal(body, &envelope) != nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				r.TransferEncoding = nil
				core.ServeHTTP(w, r)
				return
			}
			// 别名与 auto 虚拟模型在这一步解析：命中就把 model 改写成该链路的
			// 公共形态（带通道前缀），下面的既有前缀分流就能原样复用。未命中
			// 时 envelope.Model 不变，出口行为与没有该功能时完全一致。
			// 有效候选链 ≥2 跳时进入请求内重试（§九 9.2）：链序已按选路结论排好，
			// 只在首字节前换跳；单跳别名与 auto 维持既有单跳直发，行为零变化。
			if decision, chain, indexes, ok := h.resolveAliasChain(r.Context(), envelope.Model, true); ok {
				if len(chain) >= 2 {
					h.serveAliasChain(w, r, deps, decision, chain, indexes, body, envelope.Stream)
					return
				}
				if rewritten, changed := rewriteModelField(body, decision.RoutedModel); changed {
					body = rewritten
				}
				envelope.Model = decision.RoutedModel
				w = withPrismHeaders(w, decision)
			} else if h.routes != nil {
				// 配置了但停用的别名：明确报「已停用」。lookup 只认启用条目，
				// 不加这一步，请求会带着别名原名落到 core，变成莫名其妙的 404。
				if _, isAlias := h.routes.lookupAny(envelope.Model); isAlias {
					writeModelDisabledError(w, r, envelope.Model)
					return
				}
			}
			// 模型调用闸门：只认管理员显式停用（禁用表），未配置的直名统一对外
			// 放行。auto 解析后的目标同样受检；公布名单的收敛与这里是两件事。
			if !h.modelGate(envelope.Model) {
				writeModelDisabledError(w, r, envelope.Model)
				return
			}
			if zcode != nil && strings.HasPrefix(envelope.Model, zcodeModelPrefix) {
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// 旁路通道不经过 core，响应在这里捕获 usage 并异步回传 core 落账。
				capture := h.newUsageCapture(w, r, envelope.Model, "zcode", envelope.Stream)
				zcode.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if qoder != nil && strings.HasPrefix(envelope.Model, qoderModelPrefix) {
				modified := stripQoderModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// qoder 上报保留公共前缀模型名（剥前缀前的 envelope.Model）便于区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, "qoder", envelope.Stream)
				qoder.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if opencode != nil && strings.HasPrefix(envelope.Model, opencodeModelPrefix) {
				modified := stripOpenCodeModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// opencode 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, "opencode", envelope.Stream)
				opencode.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if autoclaw != nil && strings.HasPrefix(envelope.Model, autoclawModelPrefix) {
				modified := stripAutoClawModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// autoclaw 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, "autoclaw", envelope.Stream)
				autoclaw.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			for _, tr := range traeRoutes {
				if !strings.HasPrefix(envelope.Model, tr.prefix) {
					continue
				}
				modified := stripTraeModelPrefix(body, tr.prefix)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// trae 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, tr.uid, envelope.Stream)
				tr.proxy.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if raccoon != nil && strings.HasPrefix(envelope.Model, raccoonModelPrefix) {
				modified := stripRaccoonModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// raccoon 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, "raccoon", envelope.Stream)
				raccoon.ServeHTTP(capture, r)
				h.finishUsageCapture(capture)
				return
			}
			if freellm != nil && strings.HasPrefix(envelope.Model, freellmModelPrefix) {
				modified := stripFreellmModelPrefix(body)
				r.Body = io.NopCloser(bytes.NewReader(modified))
				r.ContentLength = int64(len(modified))
				r.TransferEncoding = nil
				r.Header.Del("Content-Length")
				// freellm 上报同样保留公共前缀模型名，便于与 zcode/core 区分通道。
				capture := h.newUsageCapture(w, r, envelope.Model, "freellm", envelope.Stream)
				freellm.ServeHTTP(capture, r)
				logFreellmAttribution(capture.Header(), envelope.Model)
				h.finishUsageCapture(capture)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.TransferEncoding = nil
			core.ServeHTTP(w, r)
			return
		}
		core.ServeHTTP(w, r)
	}
}

// routesToZCode buffers the JSON body to inspect its top-level model field and restores it
// for the downstream proxy. Malformed or model-less bodies fall through to core unchanged.
// The bool pair is (route to zcode, response already written).
func (h *server) routesToZCode(w http.ResponseWriter, r *http.Request) (bool, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, zcodeRouteBodyLimit+1))
	if err != nil {
		adminError(w, 400, "请求体读取失败")
		return false, true
	}
	if len(body) > zcodeRouteBodyLimit {
		adminError(w, 413, "请求体超过 32 MiB 分流检查上限")
		return false, true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	r.Header.Del("Content-Length")
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false, false
	}
	return strings.HasPrefix(envelope.Model, zcodeModelPrefix), false
}

type modelsEnvelope struct {
	Object string            `json:"object"`
	Data   []json.RawMessage `json:"data"`
}

// normalizeZcodeModelName 把「展示名」与「模型 id」归一到同一个比较键。
// /quota 的 balances[].showName 写的是 "GLM-5.3-Flash"，而 /v1/models 的 id 是
// "glm-5.3-flash"：大小写、空格、下划线都可能不同，直接比字符串必然对不上。
func normalizeZcodeModelName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "_", "-")
	// 内部空白折成连字符：Fields 顺带吃掉连续空格与制表符。
	return strings.Join(strings.Fields(s), "-")
}

// filterZcodeModels 只保留当前账号真正能服务的模型。
//
// 判据是账号在 Z.AI 侧的真实权益（entitled，来自 sidecar GET /quota 的
// balances[].showName），不是档位名。早期这里是「plan == start-plan 就只留 -flash」的
// 启发式，前提是「start-plan 等于领取的试用/周末包」——实测不成立：同一份 start-plan
// 配置下，账号权益可能是 GLM-5.3 + GLM-5.3-Flash（两个都可用），也可能只有 flash。
// 用档位推断权益会误伤：本机就出现过账号能跑 glm-5.3、页面却只列 glm-5.3-flash。
// 所以权益可用时以权益为准；取不到权益（sidecar 不可达 / 未登录 / 账单接口报错）时
// 才退回旧的档位启发式，避免整个目录一次性摊开给客户端去撞 3006。
//
// 其余计划档（coding-plan / 未知）在两种判据下都不做过滤，与既有行为一致。
func filterZcodeModels(list []json.RawMessage, entitled map[string]bool, plan string) []json.RawMessage {
	if len(entitled) == 0 && plan != "start-plan" {
		return list
	}
	out := make([]json.RawMessage, 0, len(list))
	for _, item := range list {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" {
			continue
		}
		if len(entitled) > 0 {
			if entitled[normalizeZcodeModelName(entry.ID)] {
				out = append(out, item)
			}
			continue
		}
		// 权益不可达时的兜底：start-plan 只留 -flash。
		if strings.Contains(entry.ID, "-flash") {
			out = append(out, item)
		}
	}
	return out
}

// zcodeModelFilterMode 描述本次目录过滤用的是哪条判据，供页面解释「为什么列表比目录短」。
// entitlement = 按账号真实权益；plan = 权益取不到，退回 start-plan 只留 -flash；空 = 未过滤。
func zcodeModelFilterMode(entitled map[string]bool, plan string) string {
	if len(entitled) > 0 {
		return "entitlement"
	}
	if plan == "start-plan" {
		return "plan"
	}
	return ""
}

// zcodeQuotaTTL 是权益快照的缓存时长。/quota 会打 Z.AI 的账单控制面（两次外呼），
// 而权益以「天」为尺度变化，页面每开一次就打一次没有意义，也容易被上游限流。
const zcodeQuotaTTL = 60 * time.Second

// zcodeQuotaCache 缓存账号的模型权益。haveData=false 表示从未成功取到过，
// 此时调用方退回档位启发式——「没取到」和「一个模型都没有」是两件事，不能混。
type zcodeQuotaCache struct {
	mu        sync.Mutex
	fetchedAt time.Time
	entitled  map[string]bool
	haveData  bool
}

// zcodeEntitlements 返回账号可用的模型键集合；从未取到过返回 nil。
// 取到过一次之后即使后续抓取失败也沿用上一份：权益不会凭空消失，抖动时清空列表
// 会让整个 GLM 通道在页面上短暂「一个模型都没有」。
func (h *server) zcodeEntitlements(ctx context.Context) map[string]bool {
	c := h.zcodeQuota
	if c == nil || h.cfg.ZCodeURL == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetchedAt) < zcodeQuotaTTL {
		return c.snapshotLocked()
	}
	entitled, ok := h.fetchZcodeEntitlements(ctx)
	c.fetchedAt = time.Now()
	if ok {
		c.entitled = entitled
		c.haveData = true
	}
	return c.snapshotLocked()
}

func (c *zcodeQuotaCache) snapshotLocked() map[string]bool {
	if !c.haveData {
		return nil
	}
	return c.entitled
}

// fetchZcodeEntitlements 拉一次 sidecar 的 /quota 快照并取出权益模型名。
// 任何一步失败都返回 ok=false，由调用方决定降级方式，不在这里编造空权益。
func (h *server) fetchZcodeEntitlements(ctx context.Context) (map[string]bool, bool) {
	target := *h.cfg.ZCodeURL
	target.Path = "/quota"
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return nil, false
	}
	if h.cfg.ZCodeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
	}
	timeout, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(timeout))
	if response.status != 200 {
		return nil, false
	}
	var snapshot struct {
		Balances []struct {
			ShowName string `json:"showName"`
		} `json:"balances"`
	}
	if json.Unmarshal(response.body, &snapshot) != nil {
		return nil, false
	}
	entitled := make(map[string]bool, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		if key := normalizeZcodeModelName(balance.ShowName); key != "" {
			entitled[key] = true
		}
	}
	if len(entitled) == 0 {
		return nil, false
	}
	return entitled, true
}

// tagModelRealm injects a "realm" attribution tag into a raw model-list entry
// (patch-0009 parity: core already tags cn/global server-side; glm entries come
// from a third-party upstream whose list has no realm, so console adds it here).
// Non-object or id-less entries pass through untouched.
func tagModelRealm(item json.RawMessage, realm string) json.RawMessage {
	var entry map[string]any
	if json.Unmarshal(item, &entry) != nil || entry == nil {
		return item
	}
	if id, ok := entry["id"].(string); !ok || id == "" {
		return item
	}
	entry["realm"] = realm
	out, err := json.Marshal(entry)
	if err != nil {
		return item
	}
	return out
}

type upstreamResponse struct {
	status      int
	contentType string
	body        []byte
}

// mergedModels fetches /v1/models from core and zcode-proxy in parallel and returns the union,
// core entries first, deduplicated by model id. Anything other than a parseable core list is
// replayed to the client verbatim (preserving auth and error semantics); a zcode failure simply
// degrades to the core list alone.
func (h *server) mergedModels(w http.ResponseWriter, r *http.Request, core *httputil.ReverseProxy) {
	coreURL := *h.cfg.CoreURL
	coreURL.Path = "/v1/models"
	coreURL.RawQuery = r.URL.RawQuery
	coreReq, err := http.NewRequestWithContext(r.Context(), "GET", coreURL.String(), nil)
	if err != nil {
		core.ServeHTTP(w, r)
		return
	}
	// 多 Key 归因（对标项 #5）：apiKeyGate 命中后 ctx 带标签，这里换发主 Key——
	// mergedModels 直发 core 不经过公共代理的 Rewrite，不换发会把附加 Key 明文
	// 送到 core 被 withAuth 拒掉。清单为空（gate 直通）时保持客户端原样透传。
	if label := keyLabelFrom(r.Context()); label != "" {
		coreReq.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
	} else if auth := r.Header.Get("Authorization"); auth != "" {
		coreReq.Header.Set("Authorization", auth)
	}
	var (
		wg            sync.WaitGroup
		coreResp      upstreamResponse
		zcodeRaw      []json.RawMessage
		qoderRaw      []json.RawMessage
		opencodeRaw   []json.RawMessage
		autoclawRaw   []json.RawMessage
		traeRaw       []json.RawMessage
		raccoonRaw    []json.RawMessage
		freellmRaw    []json.RawMessage
		zcodePlan     string
		zcodeEntitled map[string]bool
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		coreResp = h.fetchUpstream(coreReq)
	}()
	if h.cfg.ZCodeURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zcodeURL := *h.cfg.ZCodeURL
			zcodeURL.Path = "/v1/models"
			zcodeReq, err := http.NewRequestWithContext(r.Context(), "GET", zcodeURL.String(), nil)
			if err != nil {
				return
			}
			if h.cfg.ZCodeKey != "" {
				zcodeReq.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
			}
			zcodeRaw, _ = h.fetchModels(zcodeReq)
		}()
		// 权益与档位并行取：两者都只用于过滤 GLM 目录，谁先到都不影响其它通道。
		wg.Add(1)
		go func() {
			defer wg.Done()
			zcodeEntitled = h.zcodeEntitlements(r.Context())
		}()
		if h.cfg.ZCodeControlURL != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				zcodePlan = h.zcodePlan(r.Context())
			}()
		}
	}
	if h.cfg.QoderURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qoderURL := *h.cfg.QoderURL
			qoderURL.Path = "/v1/models"
			qoderReq, err := http.NewRequestWithContext(r.Context(), "GET", qoderURL.String(), nil)
			if err != nil {
				return
			}
			if h.cfg.QoderKey != "" {
				qoderReq.Header.Set("Authorization", "Bearer "+h.cfg.QoderKey)
			}
			list, _ := h.fetchModels(qoderReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the qoder-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, qoderModelPrefix) {
					entry["id"] = qoderModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				qoderRaw = append(qoderRaw, tagModelRealm(tagged, "qoder"))
			}
		}()
	}
	if h.cfg.OpenCodeURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opencodeURL := *h.cfg.OpenCodeURL
			opencodeURL.Path = "/v1/models"
			opencodeReq, err := http.NewRequestWithContext(r.Context(), "GET", opencodeURL.String(), nil)
			if err != nil {
				return
			}
			// OW Bridge guards every route (incl. /v1/models) with Bearer auth.
			if h.cfg.OpenCodeKey != "" {
				opencodeReq.Header.Set("Authorization", "Bearer "+h.cfg.OpenCodeKey)
			}
			list, _ := h.fetchModels(opencodeReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the opencode-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, opencodeModelPrefix) {
					entry["id"] = opencodeModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				opencodeRaw = append(opencodeRaw, tagModelRealm(tagged, "opencode"))
			}
		}()
	}
	if h.cfg.AutoClawURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			autoclawURL := *h.cfg.AutoClawURL
			autoclawURL.Path = "/v1/models"
			autoclawReq, err := http.NewRequestWithContext(r.Context(), "GET", autoclawURL.String(), nil)
			if err != nil {
				return
			}
			// autoclaw2api guards /v1/models with the bridge key.
			if h.cfg.AutoClawKey != "" {
				autoclawReq.Header.Set("Authorization", "Bearer "+h.cfg.AutoClawKey)
			}
			list, _ := h.fetchModels(autoclawReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the autoclaw-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, autoclawModelPrefix) {
					entry["id"] = autoclawModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				autoclawRaw = append(autoclawRaw, tagModelRealm(tagged, "autoclaw"))
			}
		}()
	}
	if traeCh := h.enabledTraeChannels(); len(traeCh) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, ch := range traeCh {
				traeURL := *ch.url
				traeURL.Path = "/v1/models"
				traeReq, err := http.NewRequestWithContext(r.Context(), "GET", traeURL.String(), nil)
				if err != nil {
					continue
				}
				// trae2api guards /v1/models with the bridge key.
				if ch.key != "" {
					traeReq.Header.Set("Authorization", "Bearer "+ch.key)
				}
				list, _ := h.fetchModels(traeReq)
				for _, item := range list {
					// Add routing prefix so public /v1/models entries match the trae-*/traeai-* dispatch.
					var entry map[string]any
					if json.Unmarshal(item, &entry) != nil || entry == nil {
						continue
					}
					id, ok := entry["id"].(string)
					if !ok || id == "" {
						continue
					}
					if !strings.HasPrefix(id, ch.prefix) {
						entry["id"] = ch.prefix + id
					}
					tagged, err := json.Marshal(entry)
					if err != nil {
						continue
					}
					traeRaw = append(traeRaw, tagModelRealm(tagged, ch.realm))
				}
			}
		}()
	}
	if h.cfg.RaccoonURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raccoonURL := *h.cfg.RaccoonURL
			raccoonURL.Path = "/v1/models"
			raccoonReq, err := http.NewRequestWithContext(r.Context(), "GET", raccoonURL.String(), nil)
			if err != nil {
				return
			}
			// raccoon2api guards /v1/models with the bridge key.
			if h.cfg.RaccoonKey != "" {
				raccoonReq.Header.Set("Authorization", "Bearer "+h.cfg.RaccoonKey)
			}
			list, _ := h.fetchModels(raccoonReq)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the raccoon-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, raccoonModelPrefix) {
					entry["id"] = raccoonModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				raccoonRaw = append(raccoonRaw, tagModelRealm(tagged, "raccoon"))
			}
		}()
	}
	if h.cfg.FreellmURL != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			freellmURL := *h.cfg.FreellmURL
			freellmURL.Path = "/v1/models"
			freellmReq, err := http.NewRequestWithContext(r.Context(), "GET", freellmURL.String(), nil)
			if err != nil {
				return
			}
			// FreeLLMAPI 的 /v1/models 走统一 key 鉴权。
			if h.cfg.FreellmKey != "" {
				freellmReq.Header.Set("Authorization", "Bearer "+h.cfg.FreellmKey)
			}
			list, _ := h.fetchModels(freellmReq)
			// 公共目录只公布此刻真能服务的条目：上游目录是全网能力清单，
			// needsKey/exhausted 一律 503，列出来就是给客户端递假候选。
			list = filterFreellmModels(list)
			for _, item := range list {
				// Add routing prefix so public /v1/models entries match the freellm-* dispatch.
				var entry map[string]any
				if json.Unmarshal(item, &entry) != nil || entry == nil {
					continue
				}
				id, ok := entry["id"].(string)
				if !ok || id == "" {
					continue
				}
				if !strings.HasPrefix(id, freellmModelPrefix) {
					entry["id"] = freellmModelPrefix + id
				}
				tagged, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				freellmRaw = append(freellmRaw, tagModelRealm(tagged, "freellm"))
			}
		}()
	}
	wg.Wait()
	zcodeRaw = filterZcodeModels(zcodeRaw, zcodeEntitled, zcodePlan)
	coreList, ok := parseModelList(coreResp)
	if !ok {
		if coreResp.status == 0 {
			adminError(w, 503, "核心服务不可达，请稍后重试")
			return
		}
		if ct := coreResp.contentType; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(coreResp.status)
		w.Write(coreResp.body)
		return
	}
	totalLen := len(coreList) + len(zcodeRaw) + len(qoderRaw) + len(opencodeRaw) + len(autoclawRaw) + len(traeRaw) + len(raccoonRaw) + len(freellmRaw)
	seen := make(map[string]bool, totalLen)
	merged := make([]json.RawMessage, 0, totalLen)
	all := append(append([]json.RawMessage{}, coreList...), zcodeRaw...)
	all = append(all, qoderRaw...)
	all = append(all, opencodeRaw...)
	all = append(all, autoclawRaw...)
	all = append(all, traeRaw...)
	all = append(all, raccoonRaw...)
	all = append(all, freellmRaw...)
	// 用刚拿到的 core 目录刷新倍率缓存，倍率只用于模型表展示与计费参考，
	// 不再参与放行/公布判定。
	if h.credits != nil {
		h.credits.store(creditRulesFromList(coreList))
	}
	for _, item := range all {
		var entry struct {
			ID    string `json:"id"`
			Realm string `json:"realm"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		if entry.Realm == "" && strings.HasPrefix(entry.ID, zcodeModelPrefix) {
			item = tagModelRealm(item, "glm")
		}
		// 公布语义：直名模型走启用表白名单——只有显式启用的直名才出现在公共
		// /v1/models 里（「配置了哪些就开放哪些」）；未配置与停用的直名不公布，
		// 但调用出口不拦（统一对外开放，可按名字直连）。
		if !h.modelPublished(entry.ID) {
			continue
		}
		merged = append(merged, item)
	}
	// 别名条目是虚拟间接层：客户端按无空格别名请求，网关改写为所属通道的真实模型。
	// 与 gateway-auto 一样只在路由功能启用时出现；配置了的别名全部公布，停用的
	// 别名调用时报「已停用」，不在这里隐藏。
	for _, item := range h.aliasModelEntries() {
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(item, &entry) != nil || entry.ID == "" || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		merged = append(merged, item)
	}
	// auto 是 console 自建的虚拟模型：它不在任何上游目录里，但客户端需要能在
	// /v1/models 里看到并选中它，否则各 agent 无法把它写进配置。路由未启用
	// （没有可写配置）时不能暴露它——那时没有解析器，请求会原样落到 core。
	if h.routes != nil && !seen[autoModelName] {
		if virtual, err := json.Marshal(map[string]any{
			"id":       autoModelName,
			"object":   "model",
			"owned_by": "prism",
			"realm":    "auto",
			"virtual":  true,
		}); err == nil {
			seen[autoModelName] = true
			merged = append(merged, virtual)
		}
	}
	writeJSON(w, 200, modelsEnvelope{Object: "list", Data: merged})
}

// adminZcodeStatus reports the optional GLM channel for the console Zcode tab.
// An unreachable upstream is still a successful status answer with reachable:false.
func (h *server) adminZcodeStatus(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeURL == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	var control map[string]any
	if h.cfg.ZCodeControlURL != nil {
		control = h.zcodeControlStatus(r.Context())
	}
	plan, _ := control["plan"].(string)
	// 判据是账号权益，档位只在权益取不到时兜底（见 filterZcodeModels）。
	entitled := h.zcodeEntitlements(r.Context())
	target := *h.cfg.ZCodeURL
	target.Path = "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		adminError(w, 500, "Zcode 状态探测请求构造失败")
		return
	}
	if h.cfg.ZCodeKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.ZCodeKey)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	response := h.fetchUpstream(req.WithContext(ctx))
	models := []map[string]string{}
	reachable := false
	if list, ok := parseModelList(response); ok {
		reachable = true
		for _, item := range filterZcodeModels(list, entitled, plan) {
			var entry struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item, &entry) == nil && entry.ID != "" {
				models = append(models, map[string]string{"id": entry.ID, "realm": "glm"})
			}
		}
	}
	out := map[string]any{"enabled": true, "reachable": reachable, "model_count": len(models), "models": models}
	// 告诉页面这次目录是被哪条判据裁短的：页面据此解释「为什么列表比目录短」，
	// 而不是自己拿 plan 再猜一遍（权益过滤时 plan 仍是 start-plan，猜不出来）。
	out["model_filter"] = zcodeModelFilterMode(entitled, plan)
	// 手工测速探测的结论以 health.modelResults 形态下发，前端模型表原样复用既有渲染。
	if health := h.probeHealth(channelGLM); health != nil {
		out["health"] = health
	}
	for k, v := range control {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

// adminZcodeChat proxies the console test dialog straight to zcode-proxy. The glm-* prefix
// check keeps this endpoint from becoming a general-purpose forwarding surface; zcodeProxy
// strips management cookies, CSRF and the deployment API key before the request leaves this process.
func (h *server) adminZcodeChat(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ZCodeURL == nil {
		adminError(w, 409, "GLM 通道未启用")
		return
	}
	route, handled := h.routesToZCode(w, r)
	if handled {
		return
	}
	if !route {
		adminError(w, 400, "Zcode 测试仅支持 glm- 前缀模型")
		return
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/v1/chat/completions"
	req.URL.RawPath = ""
	req.RequestURI = ""
	h.zcodeProxy().ServeHTTP(w, req)
}

func parseModelList(response upstreamResponse) ([]json.RawMessage, bool) {
	if response.status != 200 {
		return nil, false
	}
	var envelope modelsEnvelope
	if json.Unmarshal(response.body, &envelope) != nil || envelope.Data == nil {
		return nil, false
	}
	return envelope.Data, true
}

// fetchUpstream captures a complete small response; status 0 means the upstream was unreachable.
func (h *server) fetchUpstream(req *http.Request) upstreamResponse {
	response, err := h.zcodeClient.Do(req)
	if err != nil {
		return upstreamResponse{}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, zcodeModelsBodyLimit+1))
	if err != nil || len(body) > zcodeModelsBodyLimit {
		return upstreamResponse{status: response.StatusCode}
	}
	return upstreamResponse{status: response.StatusCode, contentType: response.Header.Get("Content-Type"), body: body}
}

func (h *server) fetchModels(req *http.Request) ([]json.RawMessage, error) {
	response := h.fetchUpstream(req)
	list, ok := parseModelList(response)
	if !ok {
		return nil, errors.New("models upstream unavailable")
	}
	return list, nil
}
