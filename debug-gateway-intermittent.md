# Debug Session: gateway-intermittent

- 症状：生产棱镜网关（7863）间歇性不可用（时好时坏），用户主观报告
- 开始：2026-10-19
- 约束：生产环境只读诊断优先；Steps 1-4 禁改业务逻辑；复用项目既有日志 sink（errors-*.jsonl / docker logs / channel-probe.sh）
- DEBUG_EVIDENCE_DIR: C:\Users\Administrator\AppData\Local\Temp\trae-debug\gateway-intermittent\
- 环境基线：console:local f6248249ee5e（2026-10-19 新发版，含合并批次改动）/ core:local 807e736bc75b / 8 旁路

## 假设表（可证伪）

| # | 假设 | 观察点 | 状态 |
|---|---|---|---|
| H1 | 上游通道间歇性故障（腾讯CN/智谱等上游抖动，429/5xx/timeout） | errors-*.jsonl 按(时间,通道,类别)分布 | 待验 |
| H2 | Windows Hyper-V/winnat 端口转发失效（7863 被吞或转发丢失） | docker inspect NetworkSettings.Ports 是否为空、netstat 7863 监听 | 待验 |
| H3 | 旁路网络命名空间失联（console 重建后旁路 Exited(255)） | docker ps 旁路状态、State.Error | 待验 |
| H4 | 新发版代码回归（合并批次 retryWriter/冷却准入引入 503/挂起） | 今版发版后错误账本新增类别、X-Prism-Attempts 头、容器重启次数 | 待验 |
| H5 | 熔断器过度摘链或账号池耗尽（503 uid=- / 全链 403） | 错误账本 uid 分布、/admin/errors r429_daily、circuit 状态 | 待验 |
| H6 | Docker Desktop/宿主网络层抖动（Docker 重启、winnat 重启后转发失效） | Docker Desktop 事件、容器 StartedAt 与用户感知坏时段对照 | 待验 |

## 证据记录（2026-10-19 采集）

1. 网关当前健康：healthz/livez/root 全 200（23ms）、12 容器全 Up 零重启、7863 转发与监听正常 → 排除 H2/H3/H6
2. 错误账本 10 时 19 败 / 11 时 56 败；core 29 条 upstream_error 的 snippet 全部是网关自产 `circuit_open`「候选链全部熔断」503（11:28-11:45 风暴），非上游真实 5xx
3. core 日志 429 soft_rate（腾讯 14003 too many requests）：10:02:23、10:15:35（uid=725a93fd）、11:32:28（725a93fd）、11:43:28（80a2b661）——间歇性软限流
4. core state.json：4 账号中 725a93fd（冷却至 11:42）与 80a2b661（冷却至 12:03）reason 均「429 rate limit」；a7433fa8/fd34954f 未冷却但分别 09:39/10-13 后无成功——deepseek/hy4（global 域）实际可用池仅 2 号
5. review-usage 24h：成功调用全部走 725a93fd（deepseek 24 次 + hy4 12 次）+ 80a2b661（auto 2 次），错误全为 503（uid=- 池空 9 笔）
6. server.go:308 中间件链 `observePublicExits(circuitExitGuard(publicRouter))`：circuitExitGuard 在内层，其自产 503 被 observePublicExits 记为 upstream_error（failureCategory(503)，cooldownCategory=true）→ outcomes.add 喂回熔断器 —— **自反馈缺陷实锤**（circuit.go:522-546 + proxy.go:228-265 + errors.go:737-778）
7. 11:54:32 仍两笔 503 uid=-：冷却到期后主力号 11:43 又吃 429 再冷却，锯齿持续

## 根因链（裁决）

**H1 CONFIRMED**：腾讯上游 429 软限流（14003）间歇打击 deepseek/hy4 仅有的两个可用号 → core 双号冷却、global 域池空 → 503（uid=-）。
**H5 CONFIRMED（放大器）**：console 熔断摘链 → circuitExitGuard 503 风暴（快速失败不碰上游）；冷却 30min 锯齿 + agent 端重试 → 「时好时坏」振荡。
**H4 PARTIALLY CONFIRMED（缺陷）**：合并批次本身工作正常（重试/冷却按设计），但发现既有自反馈缺陷——自产 circuit_open 503 被喂回熔断器与统计窗口。
**H2/H3/H6 REJECTED**：端口转发、容器状态、宿主网络全部正常。
**旁路独立问题（不混入本轮）**：opencode 22 条 `model_not_found`（OW Bridge 目录漂移）、raccoon 11 条上游超时（xiaohuanxiong.com）、pin 13 条（1-daily 锁定号不可用 503，设计语义）。

## 修复方案

- B1（最小代码修复）：circuitExitGuard 自产 503 加内部标记头 `X-Prism-Circuit-Open: 1`；finishExitObserve 检测到该头跳过 outcomes.add（不再喂熔断器），错误账本落账保留（保留风暴观察证据）
- 运维建议（另行拍板）：别名链 fallbacks 增加健康旁路候选（zcode/qoder），core 池空时自动兜底；池薄（global 域仅 2 号）需补号

## 修复记录（已实施）

- circuit.go：circuitExitGuard 自产 503 加 `X-Prism-Circuit-Open: 1` 标记头（常量 headerPrismCircuitOpen）
- proxy.go：exitObservation 加 selfOpen 字段，observePublicExits 从响应头读取传入
- errors.go：finishExitObserve 在 obs.selfOpen 时跳过 outcomes.add（不喂熔断器），错误账本照常落账
- retry_test.go：新增 TestCircuitOpenNotFedBack（直接组装 observePublicExits(circuitExitGuard(next)) 子链验证组合行为；不依赖 NewServer 接线时序——NewServer 仅在 cfg.HealthProbe=true 时接好 circuits）
- 验证：go vet 零输出、go test 全绿 3.9s、go test -race 全绿 6.3s（Docker golang:1.23 内执行）
- 待办：生产发版（local-release.sh）待授权；发版后观察下一轮 429 风暴应只看到错误账本记录而无熔断样本回流

## progress_status: fixed-awaiting-release
