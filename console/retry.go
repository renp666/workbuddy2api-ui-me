package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"
)

// —— 请求内重试（§九 9.2 拍板口径）———————————————————————————
//
// 现状只有「请求前选路」：别名候选链 + 探测 + 熔断在请求发出前把 model 改写成
// 某一跳的公共形态，之后单跳直发——主目标 429/5xx 时请求直接失败，容错只能靠
// agent 自己换模型重试。本文件补上「请求内重试」：别名命中且有效候选链 ≥2 跳时，
// 按链序逐跳尝试，只在一跳的响应头阶段判定失败（首字节写出前）才换下一跳；
// auth/model_error 等 4xx 原样透传；一旦向下提交（含任何 body 写出）绝不回退。
//
// 留痕：每个被切掉的失败跳就地落错误账本与滚动窗口（RetryCount=第几次尝试），
// 最终响应头带完整 X-Prism-* 决策（含 X-Prism-Attempts）。最后一跳的结果无论
// 成败由外层 observePublicExits 统一观测，因此这里只记录「被切掉」的跳，不重复。

// retryBodyCacheLimit 是暂扣失败响应时保留响应体的上限。错误响应体只需要兜底
// 透传与错误摘要（摘要截 200 字符），64 KiB 覆盖任何真实错误包；更大的失败响应
// 从未发生过，超限即截断。
const retryBodyCacheLimit = 64 << 10

// retryDeps 聚合公共出口在各通道上的 ReverseProxy，供重试主循环按
// routeTarget.Channel 派发。字段与 publicRouter 里构建的局部变量一一对应；未配置
// 的通道为 nil，重试循环会跳过该跳。
type retryDeps struct {
	core     *httputil.ReverseProxy
	zcode    *httputil.ReverseProxy
	qoder    *httputil.ReverseProxy
	opencode *httputil.ReverseProxy
	autoclaw *httputil.ReverseProxy
	trae     []traeDispatch
	raccoon  *httputil.ReverseProxy
	freellm  *httputil.ReverseProxy
}

// hop 返回该通道的 (代理, 剥前缀函数, 用量上报 uid)。剥前缀函数为 nil 表示请求体
// 保持改写后的公共模型名直发（core 与 zcode 的上游本就按公共形态收模型名）；uid
// 为空表示 core 通道，不建 usageCapture、不旁路上报。
func (d retryDeps) hop(ch routeChannel) (*httputil.ReverseProxy, func([]byte) []byte, string) {
	switch ch {
	case channelCore:
		return d.core, nil, ""
	case channelGLM:
		return d.zcode, nil, "zcode"
	case channelQoder:
		return d.qoder, stripQoderModelPrefix, "qoder"
	case channelOpenCode:
		return d.opencode, stripOpenCodeModelPrefix, "opencode"
	case channelAutoClaw:
		return d.autoclaw, stripAutoClawModelPrefix, "autoclaw"
	case channelRaccoon:
		return d.raccoon, stripRaccoonModelPrefix, "raccoon"
	case channelFreellm:
		return d.freellm, stripFreellmModelPrefix, "freellm"
	}
	for _, tr := range d.trae {
		if routeChannel(tr.uid) == ch {
			return tr.proxy, func(body []byte) []byte { return stripTraeModelPrefix(body, tr.prefix) }, tr.uid
		}
	}
	return nil, nil, ""
}

// serveAliasChain 在别名候选链上做首字节前的逐跳重试。链序由 resolveEntryChain
// 排好（已选跳在链首、熔断跳已过滤）；每跳改写 model → 旁路剥公共前缀 → 复位
// 请求体 → 交给该通道的 ReverseProxy。跳失败且后面还有候选时暂扣切换并落失败账；
// 链走完仍失败时把最后一跳的真实响应兜底透传给客户端。
func (h *server) serveAliasChain(w http.ResponseWriter, r *http.Request, deps retryDeps, decision routeDecision, chain []routeTarget, indexes []int, body []byte, stream bool) {
	rw := &retryWriter{decision: decision, prism: true}
	switched := false
	for i, hop := range chain {
		if err := r.Context().Err(); err != nil {
			return // 客户端已断开，再写什么都没有意义
		}
		public := hop.publicModel()
		if !h.modelGate(public) {
			continue // 停用的候选跳过，与单跳路径的 modelGate 口径一致
		}
		proxy, strip, uid := deps.hop(hop.Channel)
		if proxy == nil {
			continue // 通道未启用：跳过该候选，而不是落到 core 上 404
		}
		hopBody := body
		if rewritten, changed := rewriteModelField(body, public); changed {
			hopBody = rewritten
		}
		if strip != nil {
			hopBody = strip(hopBody)
		}
		r.Body = io.NopCloser(bytes.NewReader(hopBody))
		r.ContentLength = int64(len(hopBody))
		r.TransferEncoding = nil
		r.Header.Del("Content-Length")
		hopDecision := decision
		hopDecision.RoutedModel = public
		hopDecision.Channel = hop.Channel
		hopDecision.TargetIndex = indexes[i]
		hopDecision.Reason = decision.Reason
		if switched {
			hopDecision.Reason = retrySwitchReason(decision.Reason, rw.heldStatus, indexes[i])
		}
		rw.begin(hopDecision, i < len(chain)-1, i+1)
		start := time.Now()
		var inner http.ResponseWriter = w
		var capture *usageCaptureWriter
		if uid != "" {
			capture = h.newUsageCapture(w, r, public, uid, stream)
			inner = capture
		}
		rw.ResponseWriter = inner
		proxy.ServeHTTP(rw, r)
		if !rw.held {
			if capture != nil {
				h.finishUsageCapture(capture)
			}
			return
		}
		// 该跳在响应头阶段被暂扣：失败身份以链上原生模型记账（账本与熔断的
		// 归属口径），落完账换下一跳。
		snippet := rw.buf.String()
		ev := errorEvent{
			Ts:             time.Now().UnixMilli(),
			Alias:          decision.Alias,
			Channel:        string(hop.Channel),
			ModelResolved:  hop.nativeModel(),
			API:            apiForPath(r.URL.Path),
			Mode:           exitModeOf(stream),
			UpstreamStatus: rw.heldStatus,
			Category:       failureCategory(rw.heldStatus, snippet),
			ErrorSnippet:   sanitizeSnippet(snippet),
			DurationMs:     time.Since(start).Milliseconds(),
			RoutedVia:      routedViaFor(hop.Channel),
			RetryCount:     rw.attempts,
			RetryAfterSec:  parseRetryAfter(rw.retryAfter),
		}
		if h.outcomes != nil {
			h.outcomes.add(ev, false)
		}
		h.recordErrorAsync(ev)
		switched = true
	}
	if rw.held {
		// 全链失败：兜底透传最后一跳的真实响应（链尾 hop 本不该被暂扣，
		// 走到这里说明它之后只有被跳过的候选）。
		rw.commitHeld(w)
		return
	}
	// 链上没有任何可用候选（全被停用/未配置）：按别名停用的既有口径答复。
	writeModelDisabledError(w, r, decision.Alias)
}

// retrySwitchReason 拼接切跳后的 X-Prism-Reason：保留选路依据，追加「第几跳因什么
// 失败被切」，排障时能从响应头直接读出完整切换链。index 是被切掉的跳在原链中的
// 下标（0=主目标），展示为 1 起始的跳序号。
func retrySwitchReason(base string, status, index int) string {
	note := fmt.Sprintf("上游 %d", status)
	switch {
	case status == http.StatusTooManyRequests:
		note = "上游 429 限流"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		note = "上游超时"
	case status >= 500 && status <= 599:
		note = "上游 5xx 故障"
	}
	return fmt.Sprintf("%s；%s，重试切第 %d 跳", base, note, index+1)
}

// exitModeOf 把流式标志翻译成账本的 mode 字段口径（sync/stream）。
func exitModeOf(stream bool) string {
	if stream {
		return "stream"
	}
	return "sync"
}

// retryWriter 是请求内重试的响应拦截层：包住真实 ResponseWriter（或当前跳的
// usageCapture），在 WriteHeader 阶段判定该跳成败。可重试失败且后面还有候选时
// 「暂扣」——不把状态码与响应头放行，丢弃后续 body（有界缓存供兜底与摘要），并
// 清空 Header map，防止上一跳的 Retry-After/Server 等头经 ReverseProxy 的 Add 语义
// 泄漏进下一跳的成功响应。其余情况一律立即提交，提交后绝不回退（首字节已写出）。
type retryWriter struct {
	http.ResponseWriter
	decision   routeDecision
	prism      bool
	committed  bool
	held       bool
	hasNext    bool
	attempts   int
	heldStatus int
	retryAfter string
	ctype      string
	buf        bytes.Buffer
}

// begin 在每一跳开始前复位暂扣状态并登记本跳的决策与尝试序号。
func (rw *retryWriter) begin(decision routeDecision, hasNext bool, attempts int) {
	rw.decision = decision
	rw.hasNext = hasNext
	rw.attempts = attempts
	rw.held = false
	rw.heldStatus = 0
	rw.retryAfter = ""
	rw.ctype = ""
	rw.buf.Reset()
}

func (rw *retryWriter) WriteHeader(code int) {
	if rw.committed || rw.held {
		return
	}
	// 暂扣条件 = 可冷却失败类（429/5xx/超时）且后面还有候选；4xx 原样透传。
	// 状态码先于 body 判类，等拿到完整 body 再判会失去「首字节前切换」的窗口。
	if code >= 400 && rw.hasNext && cooldownCategory(failureCategory(code, "")) {
		rw.held = true
		rw.heldStatus = code
		rw.retryAfter = rw.Header().Get("Retry-After")
		rw.ctype = rw.Header().Get("Content-Type")
		clearHeader(rw.Header())
		return
	}
	rw.commit(code)
}

func (rw *retryWriter) commit(code int) {
	rw.committed = true
	if rw.prism {
		writePrismHeaders(rw.Header(), rw.decision, rw.attempts)
	}
	rw.ResponseWriter.WriteHeader(code)
}

// commitHeld 把最后一跳的暂扣响应兜底透传：客户端拿到的是最后一次真实上游错误
//（状态码、Content-Type、Retry-After、错误体），而不是网关编造的错误包。
func (rw *retryWriter) commitHeld(w http.ResponseWriter) {
	body := append([]byte(nil), rw.buf.Bytes()...)
	header := w.Header()
	if rw.ctype != "" {
		header.Set("Content-Type", rw.ctype)
	}
	if rw.retryAfter != "" {
		header.Set("Retry-After", rw.retryAfter)
	}
	if rw.prism {
		writePrismHeaders(header, rw.decision, rw.attempts)
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(rw.heldStatus)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func (rw *retryWriter) Write(p []byte) (int, error) {
	if rw.held {
		if rw.buf.Len() < retryBodyCacheLimit {
			rw.buf.Write(p)
		}
		return len(p), nil
	}
	return rw.ResponseWriter.Write(p)
}

func (rw *retryWriter) Flush() {
	if rw.held {
		return // 暂扣期间没有可冲刷的东西
	}
	_ = http.NewResponseController(rw.ResponseWriter).Flush()
}

// Unwrap 让 http.ResponseController 能穿过本包装找到底层 writer。
func (rw *retryWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// clearHeader 清空响应头 map。ReverseProxy 的响应头拷贝是 Add 语义，上一跳失败
// 响应的头（Retry-After/Server/…）若不清，会叠加进下一跳的成功响应里。
func clearHeader(header http.Header) {
	for key := range header {
		delete(header, key)
	}
}
