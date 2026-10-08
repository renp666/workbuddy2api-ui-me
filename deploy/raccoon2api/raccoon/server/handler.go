package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
	"workbuddy2api-raccoon/raccoon/pool"
	"workbuddy2api-raccoon/raccoon/upstream"
)

const maxBodyBytes = 8 << 20

// SchedulerBridge 是调度器对外契约，便于 server 与 scheduler 解耦。
type SchedulerBridge interface {
	Status() map[string]any
	RunCheckinNow(ctx context.Context) (int, error)
	RunRefreshNow(ctx context.Context) (int, error)
}

type Config struct {
	Pool          *pool.Pool
	Upstream      *upstream.Client
	APIKey        string
	AuthDir       string
	MaxRotate     int
	QuotaCooldown time.Duration
	SoftCooldown  time.Duration
	ErrThreshold  int
	ErrCooldown   time.Duration
	// RefreshSkew 是预刷新余量（协议约束，默认 upstream.RefreshMargin）。
	RefreshSkew time.Duration
	// DisplaySkew 只喂账号列表的 expired_soon（页签展示，默认 24h）。
	DisplaySkew  time.Duration
	DefaultModel string
	Sched        SchedulerBridge
	UsageFile    string
}

type Handler struct {
	cfg   Config
	mux   *http.ServeMux
	usage *usageStore
}

func NewHandler(cfg Config) *Handler {
	if cfg.MaxRotate <= 0 {
		cfg.MaxRotate = 3
	}
	if cfg.QuotaCooldown <= 0 {
		cfg.QuotaCooldown = 60 * time.Second
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 60 * time.Second
	}
	if cfg.ErrThreshold <= 0 {
		cfg.ErrThreshold = 3
	}
	if cfg.ErrCooldown <= 0 {
		cfg.ErrCooldown = 10 * time.Minute
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = upstream.RefreshMargin
	}
	// DisplaySkew 只服务页签的临期提示，与预刷新余量是两件事，单独给一个 UI 量级的值。
	if cfg.DisplaySkew <= 0 {
		cfg.DisplaySkew = 24 * time.Hour
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = upstream.DefaultModel
	}
	h := &Handler{cfg: cfg, mux: http.NewServeMux(), usage: NewUsageStore(cfg.UsageFile)}
	h.routes()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /healthz", h.healthz)
	h.mux.HandleFunc("GET /v1/models", h.withAuth(h.models))
	h.mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chatCompletions))
	h.mux.HandleFunc("GET /status", h.withAuth(h.status))

	// 账号列表无鉴权：控制台页签用它渲染，且只返回脱敏字段。
	h.mux.HandleFunc("GET /admin/api/accounts", h.adminAccounts)
	h.mux.HandleFunc("POST /admin/api/accounts/import", h.withAuth(h.adminImportAccount))
	h.mux.HandleFunc("DELETE /admin/api/accounts/{uid}", h.withAuth(h.adminDeleteAccount))
	h.mux.HandleFunc("PATCH /admin/api/accounts/{uid}", h.withAuth(h.adminPatchAccount))
	h.mux.HandleFunc("POST /admin/api/accounts/{uid}/refresh", h.withAuth(h.adminRefreshAccount))
	h.mux.HandleFunc("GET /admin/api/credits", h.withAuth(h.adminCredits))
	h.mux.HandleFunc("GET /admin/api/usage", h.withAuth(h.adminUsage))
	h.mux.HandleFunc("GET /admin/api/schedule", h.withAuth(h.adminScheduleStatus))
	h.mux.HandleFunc("POST /admin/api/schedule/checkin", h.withAuth(h.adminRunCheckin))
	h.mux.HandleFunc("POST /admin/api/schedule/refresh", h.withAuth(h.adminRunRefresh))
}

// authorized APIKey 为空时不鉴权（仅本机调试用；overlay 始终注入密钥）。
func (h *Handler) authorized(r *http.Request) bool {
	if h.cfg.APIKey == "" {
		return true
	}
	provided := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(provided) >= 7 && strings.EqualFold(provided[:7], "bearer ") {
		provided = strings.TrimSpace(provided[7:])
	}
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.cfg.APIKey)) == 1
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authorized(r) {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
			return
		}
		next(w, r)
	}
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"accounts": h.cfg.Pool.List()})
}

type modelListResponse struct {
	Object string               `json:"object"`
	Data   []upstream.ModelInfo `json:"data"`
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	// 无健康账号时也返回目录：静态兜底至少让客户端知道有哪些模型可用。
	models := upstream.StaticModels()
	if a, ok := h.cfg.Pool.Pick(); ok {
		models = h.cfg.Upstream.FetchModels(r.Context(), a)
	}
	writeJSON(w, http.StatusOK, modelListResponse{Object: "list", Data: models})
}

// tryChat 打一次上游；遇 401 且持有 refresh token 时刷新后同账号重试一次。
func (h *Handler) tryChat(ctx context.Context, a *auth.Auth, body []byte) (io.ReadCloser, int, []byte, error) {
	rc, status, respBody, err := h.cfg.Upstream.ChatStream(ctx, a, body)
	if err != nil {
		return nil, 0, nil, err
	}
	if rc != nil {
		return rc, status, nil, nil
	}
	if status == http.StatusUnauthorized && a.CanRefresh() {
		if rerr := h.cfg.Upstream.RefreshToken(ctx, a); rerr == nil {
			rc2, status2, respBody2, err2 := h.cfg.Upstream.ChatStream(ctx, a, body)
			return rc2, status2, respBody2, err2
		}
	}
	return nil, status, respBody, nil
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "read_error", err.Error())
		return
	}
	if len(body) > maxBodyBytes {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 8 MiB")
		return
	}
	var peek struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &peek)
	model := h.resolveModel(peek.Model)
	body = setModelInBody(body, model)

	tried := make(map[string]bool, h.cfg.MaxRotate)
	lastStatus := 0
	var lastBody []byte
	var lastErr string

	for i := 0; i < h.cfg.MaxRotate; i++ {
		a, ok := h.cfg.Pool.PickExcluding(tried)
		if !ok {
			break
		}
		uid := a.UID()
		tried[uid] = true

		if _, rerr := h.cfg.Upstream.RefreshTokenIfNeeded(r.Context(), a, h.cfg.RefreshSkew); rerr != nil {
			// 预刷新失败不致命：后续 401 分支还会再试一次刷新。
			log.Printf("raccoon 预刷新 %s 失败：%v", uid, rerr)
		}

		rc, status, respBody, err := h.tryChat(r.Context(), a, body)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			lastErr = err.Error()
			h.cfg.Pool.NoteError(uid, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
			continue
		}
		if rc != nil {
			h.cfg.Pool.NoteSuccess(uid)
			h.serveChat(w, uid, model, peek.Stream, rc)
			return
		}
		lastStatus, lastBody = status, respBody
		switch upstream.Classify(status, respBody) {
		case upstream.ErrTokenExpired:
			if a.CanRefresh() {
				h.cfg.Pool.Disable(uid, "token_expired: 刷新后仍被拒，请重新导入凭证")
			} else {
				h.cfg.Pool.Disable(uid, "token_expired: 无 refresh token，请重新导入凭证")
			}
		case upstream.ErrQuotaLimited:
			h.cfg.Pool.Cooldown(uid, pool.CoolQuota, h.cfg.QuotaCooldown, "quota_limited")
		case upstream.ErrNotFound:
			h.cfg.Pool.Cooldown(uid, pool.CoolSoft, h.cfg.SoftCooldown, "not_found")
		default:
			h.cfg.Pool.NoteError(uid, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
		}
	}

	if h.cfg.Pool.HealthyCount() == 0 {
		writeOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_account", "无可用小浣熊账号：请先导入凭证，或检查账号是否被禁用 / 冷却")
		return
	}
	if lastStatus != 0 {
		msg := truncate(strings.TrimSpace(string(lastBody)), 300)
		if msg == "" {
			msg = lastErr
		}
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", msg)
		return
	}
	if lastErr != "" {
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", lastErr)
		return
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_account", "无可用小浣熊账号")
}

// serveChat 透传上游响应：流式逐行回写 model 字段，非流式整体回写。
func (h *Handler) serveChat(w http.ResponseWriter, uid, model string, stream bool, rc io.ReadCloser) {
	defer rc.Close()
	if !stream {
		raw, err := io.ReadAll(io.LimitReader(rc, maxBodyBytes))
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_read_error", err.Error())
			h.usage.record(uid, model, 0, 0, true)
			return
		}
		prompt, completion := extractUsage(raw)
		out := upstream.RewriteModelInJSON(raw, model)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		h.usage.record(uid, model, prompt, completion, false)
		return
	}
	cw := &capWriter{ResponseWriter: w}
	cw.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	cw.Header().Set("Cache-Control", "no-cache")
	cw.Header().Set("Connection", "keep-alive")
	cw.Header().Set("X-Accel-Buffering", "no")
	cw.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		defer f.Flush()
	}
	err := upstream.RewriteModelInSSE(cw, rc, model)
	prompt, completion := extractUsage(cw.tail)
	h.usage.record(uid, model, prompt, completion, err != nil)
}

// resolveModel 小浣熊无「默认模型」语义，空值或 auto 才落到本仓约定的默认模型。
func (h *Handler) resolveModel(requested string) string {
	m := strings.TrimSpace(requested)
	if m == "" || m == "auto" {
		return h.cfg.DefaultModel
	}
	return m
}

func setModelInBody(body []byte, model string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	if cur, _ := obj["model"].(string); cur == model {
		return body
	}
	obj["model"] = model
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
