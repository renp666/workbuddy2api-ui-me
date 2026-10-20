package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type coreInfo struct {
	Protocol      int  `json:"protocol"`
	GlobalEnabled bool `json:"global_enabled"`
}

func (h *server) bridgeRequest(ctx context.Context, method, path, owner string) (*http.Response, error) {
	target := *h.cfg.CoreURL
	target.Path = path
	r, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+h.cfg.BridgeKey)
	if owner != "" {
		r.Header.Set("X-Console-Owner", owner)
	}
	return h.client.Do(r)
}
func (h *server) coreInfo(ctx context.Context) (coreInfo, error) {
	var info coreInfo
	response, err := h.bridgeRequest(ctx, "GET", "/internal/v1/info", "")
	if err != nil {
		return info, errors.New("核心服务不可达，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return info, errors.New("核心管理协议探测失败，请检查服务版本与桥接配置")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(body) > 8192 || json.Unmarshal(body, &info) != nil || info.Protocol != 1 {
		return coreInfo{}, errors.New("核心管理协议版本不兼容，请同步升级 console 与 core")
	}
	return info, nil
}

// coreCtl 调 core 的只读管理端点（经桥接密钥鉴权，不带 owner，因此只能取全局快照，
// 不能动账号池）。body 上限 1 MiB：账号池状态只含汇总字段。
//
// owner 传空与 raccoonCtl/trae 控制链路的差别在于：这里不注入 X-Console-Owner，
// core 就不会按 owner 过滤，返回的是整池状态。只在失败时调用（见 corePinHint），
// 不在正常路径上打 core。
func (h *server) coreCtl(ctx context.Context, method, path string, timeout time.Duration) (map[string]any, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := h.bridgeRequest(ctx, method, path, "")
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if len(raw) == 0 {
		return nil, response.StatusCode, nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, response.StatusCode, errors.New("core 返回了非 JSON")
	}
	return out, response.StatusCode, nil
}
func stripPrivateHeaders(headers http.Header) {
	for name := range headers {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-console-") || strings.HasPrefix(lower, "x-bridge-") || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-prism-") {
			headers.Del(name)
		}
	}
	// X-Request-Id 恒由网关服务端生成（响应头回给客户端的是网关 ID），入站值
	// 一律剥掉：不透传给 core/旁路，避免客户端伪造值混进转发链。
	for _, name := range []string{"Forwarded", "X-Real-IP", "Cookie", "X-CSRF-Token", "X-Request-Id"} {
		headers.Del(name)
	}
}
func (h *server) proxy(management bool) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(h.cfg.CoreURL)
			stripPrivateHeaders(pr.Out.Header)
			// stripPrivateHeaders already dropped any inbound value, so core may treat
			// this one as console-attested; core masks it to a netblock on write.
			if addr := h.clientAddress(pr.In); addr != "" {
				pr.Out.Header.Set("X-Console-Client", addr)
			}
			// 出口请求 ID：observePublicExits 生成的网关侧排障键，经内部头传给
			// core/旁路落账；管理代理与未观测路径的请求 ctx 里没有值，自然跳过。
			if id := requestIDFrom(pr.In.Context()); id != "" {
				pr.Out.Header.Set("X-Console-Request-Id", id)
			}
			// Key 归属标签：apiKeyGate 命中后恒存在（主 Key 记「默认」）。core 通道
			// 在这里统一换发 cfg.APIKey（附加 Key 的明文不出 console），core 落账侧
			// OriginOf 读 X-Console-Key-Label 归属。旁路通道由 console 侧 usage
			// 上报带 key_label，sidecar 不感知。
			if label := keyLabelFrom(pr.In.Context()); label != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
				pr.Out.Header.Set("X-Console-Key-Label", label)
			}
			if management {
				pr.Out.Header.Set("Authorization", "Bearer "+h.cfg.BridgeKey)
				pr.Out.Header.Set("X-Console-Owner", sessionFrom(pr.In).owner)
			}
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !management && r.URL.Path == "/v1/messages" {
				writeJSON(w, 503, map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": "核心服务不可达，请稍后重试"}})
				return
			}
			adminError(w, 503, "核心服务不可达，请稍后重试")
		},
	}
}
func (h *server) management(method, path string) http.HandlerFunc {
	proxy := h.proxy(true)
	return func(w http.ResponseWriter, r *http.Request) {
		oauthControl := strings.HasPrefix(path, "/internal/v1/oauth")
		if oauthControl {
			session := sessionFrom(r)
			select {
			case session.oauthGate <- struct{}{}:
				defer func() { <-session.oauthGate }()
			case <-r.Context().Done():
				adminError(w, 401, "管理会话已过期，请重新登录")
				return
			}
			if !h.sessionActive(r) {
				adminError(w, 401, "管理会话已过期，请重新登录")
				return
			}
			// Finish admitted control before owner cleanup; chat still uses the client context.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
		}
		if _, err := h.coreInfo(r.Context()); err != nil {
			adminError(w, 503, err.Error())
			return
		}
		if !h.sessionActive(r) {
			adminError(w, 401, "管理会话已过期，请重新登录")
			return
		}
		req := r.Clone(r.Context())
		req.Method = method
		req.URL.Path = strings.ReplaceAll(path, "{id}", r.PathValue("id"))
		req.URL.RawPath = ""
		req.RequestURI = ""
		if method == "GET" {
			req.Body = nil
			req.ContentLength = 0
			req.TransferEncoding = nil
			req.Header.Del("Content-Length")
			req.Header.Del("Content-Type")
		}
		proxy.ServeHTTP(w, req)
	}
}

func (h *server) sessionActive(r *http.Request) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sessionID(r)]
	return s != nil && time.Now().Before(s.expires) && s.owner == sessionFrom(r).owner
}

// —— 公共出口观测（健康检查 M1 · F1 采集点）———————————————————————
//
// 采集点放在这里而不是各通道的 handler：/v1/* 的出口只有两处注册（publicRouter 或
// 裸 core 代理），在 mux 层包一层就能覆盖「上游非 2xx、超时、上游连接失败、网关侧
// 拒绝（模型停用/别名停用）」的全部失败分支，不必逐通道改 handler，也不会漏掉将来
// 新增的通道。观测本身只读已产生的响应与请求，不产生任何上游调用。
//
// 纪律（红线 1）：观测是同步的轻量记账（计时 + 有界缓冲），落账在 errors.go 的独立
// 协程里跑；观测绝不改变状态码、响应头与响应体。

// exitPeekLimit 是提取「请求模型名 / stream 标志」时保留的请求体前缀上限。JSON 请求
// 的 model 字段总在开头，256 KiB 足以覆盖任何真实对话体；超出即放弃提取，退化为
// 「按 X-Prism-* 响应头 + 响应 Content-Type 判定」，绝不整份缓存请求体。
const exitPeekLimit = 256 << 10

// exitObservation 是一次公共出口调用的观测结果，交给 errors.go 归档。
type exitObservation struct {
	status       int
	body         string
	api          string
	mode         string
	model        string
	selfOpen     bool
	prismChannel string
	routedModel  string
	client       string
	durationMs   int64
	retryCount   int
	retryAfter   string
	requestID    string
	emptyStream  bool
	wroteBytes   int64
	keyLabel     string
}

// observedExitPath 判定该请求是否属于要观测的对话出口。只看三个对话端点：
// /v1/models、/status 这类读取端点不是「调用」，不进错误账本。
func observedExitPath(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/messages", "/v1/responses":
		return true
	}
	return false
}

// apiForPath 把出口路径映射成 api 字段（chat/messages/responses）。
func apiForPath(path string) string {
	switch path {
	case "/v1/chat/completions":
		return "chat"
	case "/v1/messages":
		return "messages"
	case "/v1/responses":
		return "responses"
	}
	return ""
}

// observePublicExits 包装 /v1/* 出口：记录响应状态、失败响应体摘要、请求模型名与
// 耗时，请求结束后交给 finishExitObserve 归档。非对话端点原样透传，零额外开销。
//
// 同时是出口请求 ID 的生成点：每个公共请求生成 32 hex 的网关侧排障键，响应头
// X-Request-Id 回给客户端（客户端日志可与网关账本对齐），内部头 X-Console-Request-Id
// 传给 core/旁路贯穿落账。入站 X-Request-Id 一律剥掉（恒服务端生成，见 stripPrivateHeaders）。
func (h *server) observePublicExits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.outcomes == nil || !observedExitPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		r = r.WithContext(withRequestID(r.Context(), id))
		peek := newRequestPeek(exitPeekLimit)
		if r.Body != nil {
			r.Body = &peekReader{src: r.Body, peek: peek}
		}
		observer := &exitObserver{ResponseWriter: w, start: time.Now()}
		next.ServeHTTP(observer, r)
		prefix := peek.snapshot()
		stream, hasStream := streamFromRequestPrefix(prefix)
		mode := ""
		switch {
		case hasStream && stream:
			mode = "stream"
		case hasStream:
			mode = "sync"
		case strings.Contains(strings.ToLower(observer.Header().Get("Content-Type")), "text/event-stream"):
			mode = "stream"
		default:
			mode = "sync"
		}
		h.finishExitObserve(exitObservation{
			status:       observer.status,
			body:         observer.snippet(),
			api:          apiForPath(r.URL.Path),
			mode:         mode,
			model:        modelFromRequestPrefix(prefix),
			selfOpen:     observer.Header().Get(headerPrismCircuitOpen) != "",
			prismChannel: observer.Header().Get("X-Prism-Channel"),
			routedModel:  observer.Header().Get("X-Prism-Routed-Model"),
			client:       strings.TrimSpace(h.clientAddress(r) + " " + r.Header.Get("User-Agent")),
			durationMs:   time.Since(observer.start).Milliseconds(),
			retryCount:   retryCountFromHeader(observer.Header()),
			retryAfter:   observer.Header().Get("Retry-After"),
			requestID:    id,
			emptyStream:  observer.emptyStream,
			wroteBytes:   observer.wroteBytes,
			keyLabel:     observer.keyLabel,
		})
	})
}

// requestIDCtxKey 是出口请求 ID 的 context 键；值只由 observePublicExits 写入，
// Rewrite 与 usage 上报处读取。
type requestIDCtxKey struct{}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDCtxKey{}, id)
}

func requestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDCtxKey{}).(string)
	return id
}

// newRequestID 生成 32 hex 的请求 ID（16 字节随机）：与 core 会话 ID 同形态，
// 可直接用作 B3 TraceId。失败时回落时间戳形态——ID 是排障键，不能因为没有
// 随机源就缺位。
func newRequestID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// emptyStreamMarker 是 core 空流兜底帧的精确前缀（upstream/internal/upstream/sse.go
// 在 0 有效帧时写 `data: {"error":{"message":"empty upstream stream",...}`）。出口观测
// 用它把「HTTP 200 但零产出」的流从完成口径里剔除。匹配整段结构化前缀而非裸
// "upstream_error" 字样：消息内容经 JSON 转义后不会形成这个字节序列，误标率趋零。
// 口径局限（有意为之）：有帧但 content 全空的流、以及 Anthropic 侧其他无产出形态
// 不在此列——它们由 usage 账本 completion=0 兜底可见。
var emptyStreamMarker = []byte(`data: {"error":{"message":"empty upstream stream"`)

// exitObserver 记录响应状态码，并只在失败（>=400）时保留响应体摘要，成功响应
// （含 SSE 流）零缓冲。同时累计写出字节数并检测 core 空流兜底帧（完成口径的
// 「无产出」信号）：检测只做一次 bytes.Contains 内存扫描，不缓冲、不保留内容。
// Flush/Unwrap 与 prismHeaderWriter、usageCaptureWriter 同源，保证流式响应不被本包装截断。
type exitObserver struct {
	http.ResponseWriter
	status      int
	capture     bool
	buf         []byte
	start       time.Time
	wroteBytes  int64
	emptyStream bool
	keyLabel    string // apiKeyGate 命中的标签（「默认」或附加 Key），错误账本归属用
}

func (o *exitObserver) WriteHeader(code int) {
	if o.status == 0 {
		o.status = code
	}
	if code >= 400 {
		o.capture = true
	}
	o.ResponseWriter.WriteHeader(code)
}

func (o *exitObserver) Write(p []byte) (int, error) {
	if o.status == 0 {
		o.status = http.StatusOK
	}
	if o.capture {
		if room := errorSnippetScanLimit - len(o.buf); room > 0 {
			if len(p) < room {
				room = len(p)
			}
			o.buf = append(o.buf, p[:room]...)
		}
	}
	o.wroteBytes += int64(len(p))
	if !o.emptyStream && o.status < 400 && len(p) > 0 {
		o.emptyStream = bytes.Contains(p, emptyStreamMarker)
	}
	return o.ResponseWriter.Write(p)
}

func (o *exitObserver) Flush() {
	_ = http.NewResponseController(o.ResponseWriter).Flush()
}

func (o *exitObserver) Unwrap() http.ResponseWriter { return o.ResponseWriter }

func (o *exitObserver) snippet() string { return string(o.buf) }

// requestPeek 保存请求体的一段前缀。请求体的读取可能发生在传输层协程里，与
// ServeHTTP 返回后取快照的时刻并发，因此读写都要加锁。
type requestPeek struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRequestPeek(limit int) *requestPeek { return &requestPeek{limit: limit} }

func (p *requestPeek) capture(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if room := p.limit - len(p.buf); room > 0 {
		if len(b) < room {
			room = len(b)
		}
		p.buf = append(p.buf, b[:room]...)
	}
}

func (p *requestPeek) snapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.buf...)
}

// peekReader 边透传请求体边留存前缀，不额外整份拷贝：前缀上限之外的内容原样流过。
type peekReader struct {
	src  io.Reader
	peek *requestPeek
}

func (r *peekReader) Read(b []byte) (int, error) {
	n, err := r.src.Read(b)
	if n > 0 {
		r.peek.capture(b[:n])
	}
	return n, err
}

// Close 透传底层请求体的关闭，保证包装后仍满足 io.ReadCloser（r.Body 的契约）。
func (r *peekReader) Close() error {
	if c, ok := r.src.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// modelFieldPattern 是截断前缀里的 model 字段兜底提取（前缀被截断时 JSON 整体
// 反序列化会失败）。
var modelFieldPattern = regexp.MustCompile(`"model"\s*:\s*"([^"]*)"`)

var streamFieldPattern = regexp.MustCompile(`"stream"\s*:\s*(true|false)`)

// modelFromRequestPrefix 从请求体前缀取 model 字段；取不到返回空串（按未知处理，
// 绝不猜）。
func modelFromRequestPrefix(prefix []byte) string {
	if len(prefix) == 0 {
		return ""
	}
	var envelope struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(prefix, &envelope) == nil && envelope.Model != "" {
		return envelope.Model
	}
	if m := modelFieldPattern.FindSubmatch(prefix); m != nil {
		return string(m[1])
	}
	return ""
}

// streamFromRequestPrefix 从请求体前缀取 stream 标志。第二个返回值为「是否找到」，
// 用来区分「显式 false」与「前缀里没有这个字段」。
func streamFromRequestPrefix(prefix []byte) (bool, bool) {
	m := streamFieldPattern.FindSubmatch(prefix)
	if m == nil {
		return false, false
	}
	return string(m[1]) == "true", true
}

// retryCountFromHeader 取重试跳数。优先读本网关请求内重试写的 X-Prism-Attempts
// （实际服务客户端的那次尝试序号）；回落读旁路上游回报的 X-Fallback-Attempts
// （console 不掌握 core 内部的重试计数，只有旁路上游会报），取不到就留 0。
func retryCountFromHeader(header http.Header) int {
	for _, name := range []string{"X-Prism-Attempts", "X-Fallback-Attempts"} {
		raw := strings.TrimSpace(header.Get(name))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100 {
			continue
		}
		return n
	}
	return 0
}
