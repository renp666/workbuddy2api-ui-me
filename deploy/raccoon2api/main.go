// main.go raccoon2api sidecar 入口：加载小浣熊账号池、起调度器与 OpenAI 兼容 HTTP 服务。
//
// 本进程是 workbuddy2api 控制台的并列旁路通道（见仓库 AGENTS.md），只暴露：
//   - 数据面：POST /v1/chat/completions、GET /v1/models、GET /healthz
//   - 控制面：GET /status、/admin/api/*（账号 CRUD、额度、用量）
//
// 小浣熊没有网页登录通道（桌面登录态依赖宿主 DPAPI），因此控制面以凭证导入为登录环节。
// 上游协议口径来自 https://github.com/aimod-cc/agent2api 的 raccoon provider（MIT + 附加限制）。
//
// 配置全部经环境变量注入（本仓 sidecar 不引入 config.json 体系）：
//
//	RACCOON_LISTEN          监听地址，默认 127.0.0.1:7867
//	RACCOON_DATA_DIR        数据根目录，默认 /data
//	RACCOON_AUTH_DIR        账号凭证目录，默认 <DATA_DIR>/auths
//	RACCOON_STATE_FILE      账号池状态文件，默认 <DATA_DIR>/state.json
//	RACCOON_USAGE_FILE      用量记录文件，默认 <DATA_DIR>/raccoon-usage.json
//	RACCOON_API_KEY         鉴权密钥（console 注入 WB2A_RACCOON_KEY），空 = 不鉴权
//	RACCOON_DEFAULT_MODEL   默认模型，默认 raccoon-chat-ml-5-5
//	RACCOON_CHECKIN_HOUR    每日积分发放触发时点，默认 9
//	RACCOON_REFRESH_HOURS   token 预刷新时点（逗号分隔），默认 3
//	RACCOON_TIMEOUT_SECONDS 短 RPC 超时秒数，默认 120
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"workbuddy2api-raccoon/raccoon/auth"
	"workbuddy2api-raccoon/raccoon/pool"
	"workbuddy2api-raccoon/raccoon/scheduler"
	"workbuddy2api-raccoon/raccoon/server"
	"workbuddy2api-raccoon/raccoon/upstream"
)

func main() {
	dataDir := envOr("RACCOON_DATA_DIR", "/data")
	authDir := envOr("RACCOON_AUTH_DIR", filepath.Join(dataDir, "auths"))
	stateFile := envOr("RACCOON_STATE_FILE", filepath.Join(dataDir, "state.json"))
	usageFile := envOr("RACCOON_USAGE_FILE", filepath.Join(dataDir, "raccoon-usage.json"))
	listen := envOr("RACCOON_LISTEN", "127.0.0.1:7867")
	apiKey := os.Getenv("RACCOON_API_KEY")
	defaultModel := envOr("RACCOON_DEFAULT_MODEL", upstream.DefaultModel)

	if err := os.MkdirAll(authDir, 0o700); err != nil {
		log.Fatalf("mkdir auths: %v", err)
	}

	raccoonAuths, err := auth.LoadDir(authDir)
	if err != nil {
		log.Fatalf("load raccoon auths: %v", err)
	}
	log.Printf("loaded %d raccoon account(s) from %s", len(raccoonAuths), authDir)

	p := pool.New(stateFile)
	p.SyncToDir(raccoonAuths)

	up := upstream.New()
	timeout := time.Duration(envInt("RACCOON_TIMEOUT_SECONDS", 120)) * time.Second
	up.HTTP.Timeout = timeout
	if tr, ok := up.StreamHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = timeout
	}

	sched := scheduler.New(scheduler.Config{
		Pool:         p,
		Upstream:     up,
		CheckinHour:  envInt("RACCOON_CHECKIN_HOUR", 9),
		RefreshHours: envIntList("RACCOON_REFRESH_HOURS", []int{3}),
		RefreshSkew:  upstream.RefreshMargin,
		StatePath:    filepath.Join(dataDir, "state", "schedule.json"),
	})

	th := server.NewHandler(server.Config{
		Pool:         p,
		Upstream:     up,
		APIKey:       apiKey,
		AuthDir:      authDir,
		DefaultModel: defaultModel,
		Sched:        sched,
		UsageFile:    usageFile,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sched.Run(ctx)
	log.Printf("raccoon 调度器已启动：每日积分触发 %d 点 / token 预刷新 %v", envInt("RACCOON_CHECKIN_HOUR", 9), envIntList("RACCOON_REFRESH_HOURS", []int{3}))

	srv := &http.Server{
		Addr:              listen,
		Handler:           th,
		ReadHeaderTimeout: 30 * time.Second,
		// ReadTimeout 覆盖整个请求读取（含 body）：防慢速 body 拖死连接。
		ReadTimeout: 60 * time.Second,
		// 不设全局 WriteTimeout：长流式生成合法时长可达数分钟，会误杀在途 SSE。
		IdleTimeout: 120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("raccoon2api sidecar listening on %s (auth=%v, accounts=%d)", listen, apiKey != "", len(p.List()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("env %s=%q 非法，回落默认 %d", key, raw, def)
		return def
	}
	return n
}

func envIntList(key string, def []int) []int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			log.Printf("env %s=%q 含非法项，回落默认 %v", key, raw, def)
			return def
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return def
	}
	return out
}
