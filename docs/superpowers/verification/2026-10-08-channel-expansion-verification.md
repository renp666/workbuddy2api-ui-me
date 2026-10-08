# 通道扩展 P1 验收记录（小浣熊 raccoon）

任务书：`docs/superpowers/plans/2026-10-08-channel-expansion-plan.md` §3.2 验收条件 C-a~C-i。
本文件记录小浣熊（raccoon）通道的逐项实测证据。Loomy 通道尚未开工，无记录。

验证日期：2026-10-08。环境：本机 Docker Desktop + 本机发版（`scripts/local-release.sh`），
镜像 `workbuddy2api-console:local` / `workbuddy2api-core:local`（core 无改动未重建），
raccoon-proxy 由 compose 现场构建（`deploy/raccoon.Dockerfile`）。

## 测试结果汇总

| 套件 | 结果 |
| --- | --- |
| `go -C console test ./...`（无 `-race`，本机无 gcc） | ok（全绿） |
| `go -C console test -v -run Raccoon` | 3 项 PASS：TestModelSwitchRaccoonExempt / TestUsageReportRaccoonKeepsPublicModelName / TestRaccoonPrefixRoutesAndMerges |
| `go -C console vet ./...` | 静默通过 |
| `node --test console/web_test.cjs` | 153/153 全绿（p1-3d 完成时实测） |
| `go -C console test -run 'TestProbeChannelWhitelistMatchesFrontend\|TestRouteChannelOrderMatchesFrontend'` | 绿（G-1/G-2 双守卫） |

## C-a 未设开关变量时公共出口逐字节不变

- 测试用 `testConfig` 默认不设 `RaccoonURL`，全量 `go test ./...` 绿即覆盖；
  `TestOtherPublicPathsStayOnCore`、`TestGlmPrefixRoutesToZCode` 等既有路由语义测试无回归。
- 生产入口 `docker-compose.yml` 不引入 `deploy/compose.raccoon.yml`。

## C-b `/v1/models` 合并新通道模型且带 realm 标签（起栈实查）

起栈后带部署 API Key 实查 `GET /v1/models`，raccoon 条目 5 条、全部带 `realm=raccoon`：

```text
raccoon-8c4485  realm=raccoon
raccoon-19b265  realm=raccoon
raccoon-405a1c  realm=raccoon
raccoon-chat-ml-5-5  realm=raccoon
raccoon-sn-sensenova-6-8-flash-lite  realm=raccoon
```

`scripts/channel-probe.sh` 合并列表计数：`total=125 zcode=1 qoder=1 opencode=0 autoclaw=16 trae=89 raccoon=5 workbuddy (core)=13`。
（首次探针输出 core=18 系脚本尚无 raccoon 桶、5 条被误归 core；补脚本后归位，总数 125 恒定。）

## C-c 前缀分流生效（起栈后发一次对话请求）

`POST /v1/chat/completions`（model=`raccoon-chat-ml-5-5`）→ HTTP 503：

```json
{"error":{"code":"no_healthy_account","message":"无可用小浣熊账号：请先导入凭证，或检查账号是否被禁用 / 冷却","type":"api_error"}}
```

该错误包来自 raccoon sidecar 自身（core 的未知模型错误不是该形状），证明请求经前缀剥名转发到达 sidecar；
503 是新通道无导入凭证的预期状态。真实上游对话出流待用户导入凭证后另行验收。

## C-d 后端枚举 6 处（grep 走查）

| 位置 | 证据 |
| --- | --- |
| route.go | `channelRaccoon` 常量、`publicChannelPrefix`、`channelName`、`reservedAliasPrefixes`、别名通道校验文案 |
| server.go | `Config.RaccoonURL`、URL 规整、session info `raccoon_enabled`（登录/刷新两处）、8 条 `/admin/raccoon/*` 路由 |
| usage_report.go | `case "raccoon": e.Account = "Raccoon 通道"` |
| probe.go | `probeableChannels` 含 raccoon；400 文案「…trae 或 raccoon」 |
| credits.go | `modelGate` 对 `raccoonModelPrefix` 免费豁免（读倍率前直接放行） |
| zcode.go（分流器） | raccoon 分流 + usage capture；mergedModels 抓取、补前缀、`realm: raccoon` 标签 |

另：main.go 解析 `WB2A_RACCOON_URL` / `WB2A_RACCOON_KEY`；raccoon.go / raccooncontrol.go 为该通道控制链路实现。

## C-e 前端枚举 4 处（grep 走查，app.js）

| 位置 | 证据 |
| --- | --- |
| probeChannelNames | `{…,trae:'trae',raccoon:'raccoon'}` |
| modelChannels | `raccoon:{body:'raccoon-models-body',empty:'raccoon-models-empty',namePrefix:'小浣熊 · '}` |
| modelKey | 剥 `^raccoon-` 前缀 |
| channelModelRows | `switchable` 排除 raccoon（免费不可开关）；列数 8 与 opencode/autoclaw/trae 同组 |

另：usageChannelNames/Order/Tags、realmLabel/modelRealm、renderAccess、go-chat 路由、回车提交、概览聚合、nav-muted 均含 raccoon。

## C-f 测试夹具 4 文件同步

probe_test.go（400 文案 + 探测/health 全链路）、usage_report_test.go（通道 case + 保留公共前缀上报）、
route_test.go（免费豁免）、zcode_test.go（分流 + 合并目录）均有 raccoon 断言；
web_test.cjs 新增 6 个 raccoon 用例。两套套件全绿（见汇总表）。

## C-g 公共页面 7 项自检

| 项 | 覆盖证据 |
| --- | --- |
| 运行概览通道分布 | web_test 概览聚合：第 7 行 `['小浣熊','在线',…]`、通道卡片 7 张 |
| 调用统计筛选与聚合 | usageChannelNames/Order/Tags/Of 含 raccoon；明细行通道标签经 clientLabel |
| Agent 接入示例/别名表/能力说明 | web_test access 合并用例：别名表条目、`/raccoon2api 桥接转发`示例、Anthropic 拒绝文案 |
| 模型列表合并（realm 标签） | C-b 起栈实查 + renderModelTable('raccoon') |
| 测速探测白名单 | probeChannelNames 含 raccoon（免费通道参与探测，与 trae/autoclaw 同口径）；G-1 守卫锁定 |
| 右上角刷新按钮 | railButtons 含 `['raccoon-refresh','raccoon']`；web_test rail ids/可见性断言 |
| API 接入页说明 | renderAccess raccoon 分支：OpenAI 示例带 raccoon2api 说明，Anthropic 提示切换协议 |

## C-h 免费通道前后端豁免同口径

- 后端：`TestModelSwitchRaccoonExempt`（route_test.go）——gateChat 对 `raccoon-` 模型在未配置倍率时放行。
- 前端：channelModelRows `switchable` 排除 raccoon；switchOpCell/capPool 同口径（app.js:306、2019）。

## C-i 跨端一致性守卫红/绿实测

- 红：从 app.js `probeChannelNames` 删除 `raccoon` 键 → `TestProbeChannelWhitelistMatchesFrontend` FAIL：
  `探测白名单漂移：前端 probeChannelNames 值 = [autoclaw core glm qoder trae]，后端 probeableChannels = [autoclaw core glm qoder raccoon trae]`
- 绿：恢复后同命令 ok。
- G-2（routeChannelOrder vs usageChannelOrder）随全量测试绿。

## 栈上状态

- `docker compose ps`：8 只服务全 Up（core 沿用 9 小时前旧容器，console + 6 sidecar 本次重建）。
- `scripts/channel-probe.sh`：gateway healthz/livez 200；raccoon data 7867 `/healthz` 200。
- 本机发版：`scripts/local-release.sh` 干净工作区执行，`:local`/`:local-prev` 标签滚动正常。

## 已知边界（非回归）

- raccoon 真实上游对话出流、每日积分账单核对链路未验收（无导入凭证；属用户后续操作）。
- 本机无 gcc，`-race` 未跑（与既有通道验收同口径）。
- scripts/deploy 两目录 Python 测试存在既有环境性失败（NTFS/WinError 1314），与本改动无关，未重跑对比。
