# 安全审计与加固跟踪（2026-09-22）

日期：2026-09-22。基线提交 `ddba4b4`。**性质：过程与判据记录，不是已完成声明。**

> **2026-09-22 修正**：本文件初稿曾写「本轮的工作树改动已被回退（`git status` 干净、`git stash list` 为空、HEAD 仍为 `ddba4b4`）」。
> 该句与盘上事实不符：改动一直在工作树里未提交，`stash` 为空、`reflog` 只有一条 `clone`，从未回退过。
> 现已改为本地提交，不再需要“重放”。附录保留的 diff 仅作为当时的最小改动记录。

未推送、未发布镜像；`docker compose` 起的那套仍在运行，**跑的是已发布镜像 `wb2api-*-1789550372`，不含这些改动**。

## 0. 三层血缘（本次新增结论）

分析和定级前必须先确认改的是哪一层，否则会把隔壁层的既有问题当成自己的回归：

| 层 | 仓库 | 职责 |
| --- | --- | --- |
| 原作者 | `Sliverkiss/workbuddy2api` | 账号池、调度器、上游客户端、WAF/IP 级防护 |
| 二创 | `baiyea/workbuddy2api-ui` | 增加 `console/`、`patches/`、`extensions/`、容器化部署 |
| 本仓 | 三创 | 只改 `console/`、文档与仓库卫生 |

`upstream/` 是历史快照（`upstream.lock` 记 `c576b48`），**落后原作者 `master` 约 214 个提交**。
后果：原作者后续的安全加固不在本仓，已确认缺失的非测试文件 19 个，其中安全相关的是：

- `internal/server/wafip.go` —— WAF IP 级熔断（短窗多号 WAF 403 → 熔断该出口 IP）。本仓 `handler.go` 无此状态机，`ErrWafBlock` 不存在。
- `internal/server/admin.go` —— 账号手动停用/恢复端点，默认关闭、与 `/status` 同一把 API Key 鉴权。
- `internal/server/metrics.go`、`internal/server/backoff.go`。
- `internal/session/ids.go` / `session.go`（会话 ID 内容签名、粘性键）与 `internal/auth/auth.go` 的数据竞争修复（#125 出站请求头锁外直读 token/domain）。

**这是快照策略的已知取舍，不是本次改动引入的回归**。要对齐走 `scripts/overlay.py update`，需要单独授权。

## 1. 范围是怎么定下来的

用户裁定两条，后续所有取舍以此为准：

- **只看安全风险**，功能侧「原作者已经考虑好了」→ 流式兼容严格度（原第 2 项）从清单里撤掉。
- **不做 owner 隔离**。本项目是单人 clone、自己管多个账号，跨账号共用 `conversationId` 是**想要的行为**
  （上游 `chat_5` 计数与 `first_buddy` 达标都依赖共享会话），隔离只会降低奖励，不是风控信号。
  因此该条转成纯文档修正。

## 2. 五个面的结论

| 面 | 结论 | 关键证据 |
| --- | --- | --- |
| 凭据泄露 | 干净 | 工作树与全量历史无密钥；`deploy/default-config.json`＝`{}`；`deploy/acceptance.env` 三个密钥全空 → 成品镜像不带共享 Key。 |
| HTML 注入 | 干净 | `console/` 零 innerHTML；模型文本与 SSE 增量全走 `textContent`；`auth_url` 受 `oauth.ValidAuthorizationURL` 的 https＋无 userinfo＋无端口＋4 域后缀白名单约束。 |
| 权限边界 | 干净 | 管理会话 / 公共 API Key / 内部桥接密钥三键分离，启动期校验（≥32 字节、互不相同），非法即失败，无静默替换；未知用量保持 `null`。 |
| 传输与可达性 | **两项真风险** | 见第 3 节的 P1、P2。 |
| 文档可信度 | 有漂移 | `AGENTS.md` 两句比代码更严（owner 绑定、响应体体积）；见第 3 节 P4。 |

## 3. 处置方案（4 项，均已验证过一遍）

- **P1 登录限速在反代后塌缩** —— 限速按连接对端分桶，运行代码从不读转发头（`X-Forwarded-For` 只出现在 `proxy_test.go`）。
  按 README 反代部署时所有访客共用 10 次/分一个桶：能把管理员自己锁在门外，也让限速失去区分来源的意义。
  方案：新增 `WB2A_TRUSTED_PROXY_CIDRS`（逗号分隔 CIDR，非法值直接启动失败）。**只有直连对端落在该网段**时才采信
  请求中那一个 `X-Forwarded-For` 的最右段作为客户端地址；空值＝行为完全不变。同源/CSRF/桥接认证一律仍用直连对端；
  转发给 core 前照旧剥 `X-Forwarded-*`，所以这不是新增信任面。
- **P2 console 只提供明文 HTTP，而 compose 把 `0.0.0.0:7863` 发到宿主** —— 无 TLS、无兜底。
  方案：非回环监听且未声明 https 的 `WB2A_PUBLIC_ORIGIN` 时启动打一条警告；`WB2A_REQUIRE_HTTPS=1` 把同一情形改成拒绝启动（默认关闭）。
  不加 TLS、不自签证书、不引入 `.env`（沿用现有 `WB2A_PUBLIC_ORIGIN` 作为唯一 https 信号，与 Secure Cookie 判据同源）。
- **P3 前端 ID 未编码** —— `app.js` 三处把 core 下发的 ID 裸拼进 `/admin/*` 路径，`..` 会被浏览器在发送前归一化。
  方案：补 `encodeURIComponent`。**严重度已下调**：三个 ID 都由 core 生成，且 console 在 ServeMux 前拒绝 `%`、`.`、`//`、`\`，
  Go 1.22 的 `{id}` 只匹配单个路径段 → 最坏是走错路由拿 404，换不到别的 `/admin` 端点、绕不开 `withAdmin`＋CSRF＋同源。
  价值是少依赖那道过滤网、ID 原样送达，属 I/O 边界一致性而非权限边界。
- **P4 文档与代码不符** —— `AGENTS.md` 的「代理至带 owner 的 `/internal/v1/messages`」（bridge 对该路径只校验 owner 格式，
  真绑定只在 OAuth 接口）和「体积限制沿用 `server.max_body_mb`」（非流式响应侧是 `adapter.go` 硬编码 `32<<20`）。按代码为准改文档。

## 4. 判据（红→绿，实跑输出）

先失败再最小改：`server_test.go` 报 `cfg.TrustedProxyCIDRs undefined`、`main_test.go` 报 `undefined: httpsExposure`，
`web_test.cjs` 新断言实测到 `'/admin/tasks/../../owners/other/tasks/runs'`。实现后：

| 命令 | 实际结果 |
| --- | --- |
| 容器 `go test -race -count=1 ./...`（`-w /src/console`） | `ok workbuddy2api-console 1.172s` |
| 容器 `go vet ./...` | `VET_OK` |
| 宿主 `go test -count=1 ./...` | `ok workbuddy2api-console 0.943s` |
| `node --test console/web_test.cjs` | `tests 30 / pass 30 / fail 0` |
| `python -m unittest discover -s scripts -p 'test_*.py'` | 4 failures + 26 errors —— **在 `git archive HEAD` 的未改动副本上计数完全相同**，环境性失败非回归 |
| `python -m unittest discover -s deploy -p 'test_*.py'` | 2 failures，同上（NTFS 无 POSIX 权限位、Windows 路径分隔符） |
| `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` | 通过 |
| `git diff --check` | 干净 |

未做：`bash scripts/check.sh`（它调 `python3`，本机解析到 Store 占位符；且要 race）；`scripts/acceptance.sh`；真实上游调用。

## 5. 被推翻或下调的结论（下次别再照抄）

1. **「`len(h.limits)>=1024` 可被多源 IP 持续锁死」** → limits 条目 60 秒过期（`server.go` `cleanupLocked`），要维持全局 429 需每分钟 >1024 个**不同**源 → 理论项 P2，不是可稳定触发的 DoS。
2. **「上游请求缺超时」** → 记错了。`client{Timeout: 3s}` 与 oauthControl 的 `context.WithTimeout(..., 30s)` 都在，不是缺口。
3. **「响应体 32MiB 硬编码要改成跟随配置」** → 已有界，不构成风险；改成文档说清两个开关不是一回事即可。
4. **审计代理给的证据行会错配** —— 例如声称「`X-Forwarded-*` 被无条件剥离（proxy.go:52）」，实际 :56 的清单里没有 XFF；真因是"没有任何运行代码读转发头"。合并前自己复跑 grep 并按触发成本重定级。
5. **`gofmt -l console/` 在这台机上把所有文件都列为未格式化**（含没碰过的），是 CRLF 工作副本假阳性；判格式化要 `tr -d '\r'` 到临时目录再比。

## 6. 本机验证配方（没有装 Go 也能验）

```bash
# 一次性：解压式工具链，不动 PATH、不装系统
curl -sSL -o ~/.cache/gotoolchain/go.zip https://go.dev/dl/go1.23.12.windows-amd64.zip
/c/Windows/System32/tar.exe -xf ~/.cache/gotoolchain/go.zip -C ~/.cache/gotoolchain
export GOROOT=~/.cache/gotoolchain/go GOPATH=~/.cache/gopath GOCACHE=~/.cache/gobuild
~/.cache/gotoolchain/go/bin/go.exe -C console test -count=1 ./...
```

- `-race` 需要 gcc，本机没有 → CGO_ENABLED=0，别把它当代码问题。要 race 就进 Linux 容器（基镜像同 `deploy/*.Dockerfile`）：

```bash
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src:ro" -w /src/console -e HOME=/tmp \
  docker.m.daocloud.io/library/golang:1.23-alpine sh -c \
  "sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories \
   && apk add --no-cache gcc musl-dev >/dev/null && CGO_ENABLED=1 go test -race -count=1 ./..."
```

- `registry-1.docker.io` 在本机超时，`docker.m.daocloud.io` 可拉；alpine apk 走 `mirrors.aliyun.com`。
- Git Bash 里 docker 的 `/src` 参数必须 `MSYS_NO_PATHCONV=1`，否则被改写成 `C:/Program Files/Git/...`。
- `golang:alpine` 默认 `CGO_ENABLED=0`，且镜像里**不带** gcc。
- 外网是**间歇可用**：同一天早上 `curl` 全 exit 7，下午 `go.dev` 200。别把一次失败当永久结论。

## 7. 运行现状与一条无关告警

`docker compose up -d` 起的是发布镜像：`/livez` 200、`/healthz` 起初 `total:0` 返回 503（空账号池的预期行为）、
`/v1/models` 无 Key 401、页面 200；密钥自动生成在 `runtime/wb2api/keys/keys.json`，`git status` 无未跟踪项（`runtime/` 已忽略）。

用户贴来的 `429 AccountQuotaExceeded ... reset at 2026-09-22 23:59:59 +0800`（带官方 Request id）**不是本项目的限速**
（本仓 429 文案是「尝试过于频繁，请一分钟后再试」），是上游账号月度额度；本机栈近 30 分钟日志里三次请求全为 200，
故该报错来自别处部署或直接打官方接口的客户端。额度按账号计，单账号池无可轮换对象，`global` realm `servable:false`
（未授权 global 账号）→ 只能再授权一个 cn 账号或等重置。

## 8. 遗留

- 第 3 节四项已由本地提交落地（不再是“待重放”）；`scripts/acceptance.sh` 未跑（涉及运行配置时才需要）。
- **本仓无 CI**（`.github/workflows/` 不存在）—— 文档与代码漂移没有拦截点，是第 2 节所有"文档比代码严"问题的共同根因。
- 若将来要验 `app.js` 之外的前端边界，注意 `web_test.cjs` 目前只有一条 XSS 断言（`task-log` 的 `textContent`），聊天渲染无转义断言。
- 第 0 节血缘：快照落后 214 个提交，原作者的安全加固不在本仓。要对齐需单独授权的上游更新，不在本轮范围。

## 9. 第二轮：独立复核与本轮修复（2026-09-22 同日晚）

第一轮结论逐条重验，并补上第一轮未覆盖的对抗面。

### 9.1 P1 漏洞独立复现（不依赖作者测试）

在本机装了**解压式 Go 1.23.12**（阿里云镜像，`CGO_ENABLED=0`），用自写的对抗测试在**改动前的 `ddba4b4` 基线**上复现：

```text
访客 B 用正确密钥登录的结果 = 429
复现成功：反代后所有访客共用 10 次/分一个桶，无关流量可把管理员锁在门外
```

同一场景在加 `WB2A_TRUSTED_PROXY_CIDRS=10.0.0.0/8` 后：`访客 B = 200`，塔缩消除。
定级确认：这是**真漏洞**（匿名第三方可自锁管理员 + 限速失去区分来源的意义），不是仅可用性问题。

### 9.2 新增对抗测试（试图打破新信任面，均末遂）

| 攻击尝试 | 结果 |
| --- | --- |
| 非可信对端自带 XFF，冒充受害者 IP 去锁死受害者 | 拦下（受害者仍 401） |
| 多段 XFF `"203.0.113.99, 10.0.0.1"` 落到别人桶 | 按最右段语义正确归桶 |
| 8 种畸形值（带端口 / `::ffff:` / 空字节 / 非 IP / 前后空白） | 均退化到对端，无 panic、无 500 |
| 畸形 CIDR（裸 IP、`garbage`、`/33`、`a,,bad`） | 一律启动失败 |
| 伪造 `X-Forwarded-Host/Proto/Forwarded/X-Real-IP` 骗同源 | 全部 403 |
| `X-Forwarded-Proto: http` 把 Cookie 降级 | 拦下（Secure/HttpOnly/SameSite=Strict 均在） |
| **重复 XFF 头**（`Header.Add` 两次）拼桶 | 整体忽略，退化为对端桶 |
| `/admin/tasks/{id}/runs` 的 `{id}` 注入 `../`、`%2e%2e%2f` 等 6 种 | 全部 console 400；合法 ID 正向确认能到达 core |

另确认：`loginKey` 不影响 `sameOrigin`（配置了 `PublicOrigin` 时只认它，与 XFF 无关），也不影响 Cookie 的 `Secure` 判据。

### 9.3 本轮新增修复

- **HSTS 缺失**：响应头只有 CSP / Referrer-Policy / nosniff / no-store。
  补：仅当 `PublicOrigin` 为 `https://` 时下发 `Strict-Transport-Security: max-age=31536000`；
  明文部署不下发（避免把浏览器钉死在本进程不提供的方案上）。已跑红→绿：移除实现后只有 `https_origin_sends_hsts` 失败，其他两个子测仍绿。
- **`0.0.0.0/0` / `::/0` 可信 CIDR 静默接受**：不报错也无提示，配错等于信任全部直连对端。
  补：启动时打一条包含实际网段的警告（仍接受，不 fail）。
- **混合行尾**：`main_test.go`(22 行) / `server_test.go`(55 行) / `web_test.cjs`(15 行) 的新增块是裸 LF，混在 CRLF 工作副本里，且仓库无 `.gitattributes`。
  补：新增 `.gitattributes`（`* text=auto eol=lf` + 逐类型 + 二进制），并把这 3 个文件归一为 LF。
  验证：`git check-attr` 返回 `eol: lf`；索引中 CR 字节数 = 0；`git diff` 不再出现整文件重写。
- **新测试缺子测试名**：`TestPlainHTTPExposureGuard` 失败只报 `main_test.go:176`。
  补：`TestPlainHTTPExposureGuard` 与 `TestLoginLimitKeysClientsThroughTrustedProxy` 均改为 `t.Run`，共 15 个子测具名。
- **文档修正**：上文的“已回退”句、三层血缘、HSTS 与宽 CIDR 语义、`.gitattributes` 约定。

### 9.4 本轮实测判据

| 命令 | 实际结果 |
| --- | --- |
| `go test -count=1 ./...`（LF 检出，含本轮新增） | `ok workbuddy2api-console 1.108s` |
| `go vet ./...` | 退出码 0 |
| `go test -v`（3 个目标测试） | 15 个子测全部具名且 PASS |
| 自写对抗测试 10 项 + 作者测试 15 项 | 25/25 PASS |
| `node --test console/web_test.cjs` | `tests 30 / pass 30 / fail 0` |
| HSTS 红态（临时移除实现） | `--- FAIL: .../https_origin_sends_hsts`，其余 2 子测仍 PASS |
| `git diff` 行尾污染 | 无整文件重写 |

### 9.5 本轮未做

`-race`（本机无 gcc）、`scripts/check.sh`、`scripts/acceptance.sh`、真实上游调用。
所以**不得**把本轮说成“完整通过 check.sh”。首次分析时 `docker` 守护进程未运行，也无法复用第一轮的容器判据。

### 9.6 保留：附录 diff 与盘上实文不完全一致

附录 A/B 是本轮之前的最小改动记录，**当前盘上的 `server.go` / `main.go` 已额外包含 HSTS、宽 CIDR 警告与 `log` 导入**。
以盘上代码为准；附录用于理解改动意图，不要照抄回写。

## 附录：可原样重放的改动

### A. `console/server.go`

```go
// Config 追加两个字段（含注释），并在 NewServer 里 PublicOrigin 规范化之后解析：
 // TrustedProxyCIDRs lists proxies allowed to name the client in X-Forwarded-For.
 // It is consumed by the login limiter only; same-origin and CSRF checks keep using the connection peer.
 TrustedProxyCIDRs string
 RequireHTTPS      bool

 var trusted []*net.IPNet
 for _, raw := range strings.Split(cfg.TrustedProxyCIDRs, ",") {
  raw = strings.TrimSpace(raw)
  if raw == "" {
   continue
  }
  _, network, err := net.ParseCIDR(raw)
  if err != nil {
   return nil, errors.New("WB2A_TRUSTED_PROXY_CIDRS 须是逗号分隔的 CIDR 列表")
  }
  trusted = append(trusted, network)
 }
 // h := &server{...} 追加 trusted: trusted；server 结构体追加 trusted []*net.IPNet

// adminLogin 里的 SplitHostPort 三行整段换成：
 ip := h.loginKey(r)

// 新增：
func (h *server) loginKey(r *http.Request) string {
 ip, _, err := net.SplitHostPort(r.RemoteAddr)
 if err != nil {
  ip = r.RemoteAddr
 }
 peer := net.ParseIP(ip)
 if peer == nil || len(h.trusted) == 0 {
  return ip
 }
 trusted := false
 for _, network := range h.trusted {
  if network.Contains(peer) {
   trusted = true
   break
  }
 }
 // A single header is the only shape a reversing proxy appends to; repeated headers leave the last hop ambiguous.
 forwarded := r.Header.Values("X-Forwarded-For")
 if !trusted || len(forwarded) != 1 {
  return ip
 }
 entry := forwarded[0]
 if comma := strings.LastIndexByte(entry, ','); comma >= 0 {
  entry = entry[comma+1:]
 }
 client := net.ParseIP(strings.TrimSpace(entry))
 if client == nil {
  return ip
 }
 return client.String()
}
```

### B. `console/main.go`

import 追加 `net`、`strings`；`configFromEnv` 两个 Config 字面量各追加：

```go
TrustedProxyCIDRs: os.Getenv("WB2A_TRUSTED_PROXY_CIDRS"), RequireHTTPS: os.Getenv("WB2A_REQUIRE_HTTPS") == "1"
```

新增纯函数（便于无服务器测试），并在 `main` 里 `NewServer` 之后调用、在 `console listening` 之后打印：

```go
func httpsExposure(listen, publicOrigin string, requireHTTPS bool) (string, error) {
 host := listen
 if split, _, err := net.SplitHostPort(listen); err == nil {
  host = split
 }
 if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
  return "", nil
 }
 if strings.HasPrefix(publicOrigin, "https://") {
  return "", nil
 }
 const plain = "console 只在明文 HTTP 上监听，且监听地址不是回环，管理密钥与会话 Cookie 会随请求明文传输"
 if requireHTTPS {
  return plain, errors.New(plain + "；WB2A_REQUIRE_HTTPS 已开启，拒绝启动。请改为回环监听或把 WB2A_PUBLIC_ORIGIN 设为 https://你的域名")
 }
 return plain + "。公网暴露前请在反向代理终止 TLS 并把 WB2A_PUBLIC_ORIGIN 设为 https://你的域名", nil
}

 warning, err := httpsExposure(listen, cfg.PublicOrigin, cfg.RequireHTTPS)
 if err != nil {
  log.Fatal(err)
 }
 // ...log.Printf("console listening on %s", listen) 之后：
 if warning != "" {
  log.Printf("[console] 警告：%s", warning)
 }
```

### C. `console/web/app.js` 三处

```js
const run=await jsonAPI('tasks/'+encodeURIComponent(taskID)+'/runs',{request_id:requestID});
try{const flow=await jsonAPI(`oauth/${encodeURIComponent(id)}/poll`,{});if(id!==flowID)return;renderFlow(flow);
try{const flow=await jsonAPI(`oauth/${encodeURIComponent(flowID)}/region`,{region:$('region').value});renderFlow(flow);
```

不要改成"在 `api()` 里统一编码"—— `app.js:120` 的 path 带查询串（`'task-runs?limit=20&before=…'`），统一编码会产出 `%3Flimit%3D20`，把任务历史翻页打死。

### D. 回归测试

追加到 `console/server_test.go`、`console/main_test.go`、`console/web_test.cjs` 的三份断言，
本次会话内已完整跑过红→绿；其失败信息（`undefined: httpsExposure`、`'/admin/tasks/../../owners/other/tasks/runs'`）
即重放时的预期红态。测试意图：

- 可信代理：同一代理网段的两个不同 XFF 客户端各自限速，一个耗尽不牵连另一个；直连非可信对端不得继承别人桶。
- 不可信：`TrustedProxyCIDRs` 为空或不含直连对端时，XFF 一律忽略（保持改动前行为）；裸 IP 当作 CIDR 要启动失败。
- `httpsExposure`：`:7863`/`0.0.0.0:7863` 且无 https origin → 警告；`127.0.0.1`/`[::1]` 或已配 https → 静默；`requireHTTPS` 时同一条件转为 error。
- 前端：core 下发的含 `../` 的 taskID/flowID 必须变成 `..%2F..%2F…` 单段，不得重塑 `/admin` 路径。

### E. 文档

`AGENTS.md`：Anthropic 验证段两句改法（owner 仅格式校验＋跨账号共享 `conversationId` 是预期；请求体与响应体上限不是一个开关）；
运行配置段补 `WB2A_TRUSTED_PROXY_CIDRS`、`WB2A_REQUIRE_HTTPS` 语义与默认值。
`README.md` 「使用边界」：在公网 HTTPS 那条后，追加"明文监听会警告／`WB2A_REQUIRE_HTTPS` 可 fail-closed"
和"反代后登录限速按直连地址计数，需按访客分别限速时填 `WB2A_TRUSTED_PROXY_CIDRS`"两条。
