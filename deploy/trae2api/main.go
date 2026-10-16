// main.go trae2api sidecar 入口：加载 trae 账号池、起调度器与 OpenAI 兼容 HTTP 服务。
//
// 本进程是 workbuddy2api 控制台的并列旁路通道（见仓库 AGENTS.md），只暴露：
//   - 数据面：POST /v1/chat/completions、GET /v1/models、GET /status、GET /healthz
//   - 管理面：GET /admin、/admin/api/*（账号 CRUD、网页登录、排程、用量）
//   - 登录回调：GET /authorize（TRAE 浏览器 302 落点，无 Bearer）
//
// 配置全部经环境变量注入（本仓 sidecar 不引入 config.json 体系）：
//
//	TRAE_LISTEN          监听地址，默认 127.0.0.1:7866
//	TRAE_DATA_DIR        数据根目录，默认 /data
//	TRAE_AUTH_DIR        账号凭证目录，默认 <DATA_DIR>/auths
//	TRAE_STATE_FILE      账号池状态文件，默认 <DATA_DIR>/state.json
//	TRAE_USAGE_FILE      用量记录文件，默认 <DATA_DIR>/trae-usage.json
//	TRAE_API_KEY         鉴权密钥（console 注入 WB2A_TRAE_KEY），空 = 不鉴权
//	TRAE_CALLBACK_LISTEN 独立回调监听地址（仅 /authorize），默认 127.0.0.1:18080；"0"/空 = 不起
//	TRAE_DEFAULT_MODEL   默认模型，默认 glm-5.2
//	TRAE_CHECKIN_HOUR    每日签到时点，默认 9
//	TRAE_REFRESH_HOURS   token 预刷新时点（逗号分隔），默认 3
//	TRAE_TIMEOUT_SECONDS 短 RPC 超时秒数，默认 120
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

	"workbuddy2api-trae/trae/auth"
	"workbuddy2api-trae/trae/pool"
	"workbuddy2api-trae/trae/scheduler"
	"workbuddy2api-trae/trae/server"
	"workbuddy2api-trae/trae/upstream"
)

func main() {
	ed := upstream.FromEnv()
	dataDir := envOr("TRAE_DATA_DIR", "/data")
	authDir := envOr("TRAE_AUTH_DIR", filepath.Join(dataDir, "auths"))
	stateFile := envOr("TRAE_STATE_FILE", filepath.Join(dataDir, "state.json"))
	usageFile := envOr("TRAE_USAGE_FILE", filepath.Join(dataDir, "trae-usage.json"))
	listen := envOr("TRAE_LISTEN", "127.0.0.1:7866")
	callbackListen := envOr("TRAE_CALLBACK_LISTEN", "127.0.0.1:18080")
	apiKey := os.Getenv("TRAE_API_KEY")
	defaultModel := envOr("TRAE_DEFAULT_MODEL", ed.DefaultModelName)

	log.Printf("trae 区域=%s（%s）agent=%s oauth=%s ug=%s 签到=%v 权益口径=%s",
		ed.Key, ed.Label, ed.AgentHost, ed.OAuthHost, ed.UgHost, ed.CheckinSupported, ed.EntitlementMode)

	if err := os.MkdirAll(authDir, 0o700); err != nil {
		log.Fatalf("mkdir auths: %v", err)
	}

	traeAuths, err := auth.LoadDir(authDir)
	if err != nil {
		log.Fatalf("load trae auths: %v", err)
	}
	log.Printf("loaded %d trae account(s) from %s", len(traeAuths), authDir)

	p := pool.New(stateFile)
	p.SyncToDir(traeAuths)

	up := upstream.New(ed)
	timeout := time.Duration(envInt("TRAE_TIMEOUT_SECONDS", 120)) * time.Second
	up.HTTP.Timeout = timeout
	if tr, ok := up.StreamHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = timeout
	}

	sched := scheduler.New(scheduler.Config{
		Pool:         p,
		Upstream:     up,
		CheckinHour:  envInt("TRAE_CHECKIN_HOUR", 9),
		RefreshHours: envIntList("TRAE_REFRESH_HOURS", []int{3}),
		RefreshSkew:  24 * time.Hour,
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
	log.Printf("trae 调度器已启动：签到 %d 点 / token 预刷新 %v", envInt("TRAE_CHECKIN_HOUR", 9), envIntList("TRAE_REFRESH_HOURS", []int{3}))

	// 独立回调 server：仅 /authorize（TRAE 浏览器 302 落点）。
	// 远程部署时 console 会以 /admin/trae/authorize 代理该端点，此处可置 "0" 关闭。
	if callbackListen != "" && callbackListen != "0" {
		cbSrv := &http.Server{Addr: callbackListen, Handler: th, ReadHeaderTimeout: 30 * time.Second}
		go func() {
			<-ctx.Done()
			sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = cbSrv.Shutdown(sc)
		}()
		go func() {
			log.Printf("trae callback server on %s (TRAE login /authorize)", callbackListen)
			if err := cbSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// 端口被占用不致命：网页登录降级为手动粘贴回调链接。
				log.Printf("trae callback server (%s) failed: %v — web 登录降级为手动粘贴回调链接", callbackListen, err)
			}
		}()
	}

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

	log.Printf("trae2api sidecar listening on %s (auth=%v, accounts=%d)", listen, apiKey != "", len(p.List()))
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
