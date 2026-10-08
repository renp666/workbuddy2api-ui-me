# 任务计划书：新增旁路通道 + 吸收参考项目能力

- **单据编号**：TICKET-2026-10-08-CHANNEL-EXPANSION
- **派发给**：Trae（人工派发，本仓直接开发）
- **验收方**：需求方（AI 助手，独立复跑判据）
- **创建时间**：2026-10-08
- **前置状态**：trae / autoclaw 两路通道已接入（含公共面回填，工作区有 10 个未提交改动待固化）

> **给实施方**：本单按 P0 → P1 → P3 顺序执行（P2 本轮不做），**每个阶段完成后停下来等验收**，不要连续做完再报。
> 凡标注 `【决策】` / `【已拍板】` 的是需求方已拍板，不要另做设计选择。
> 开工前确认项（§10.1）**已全部闭环**，无遗留 `【待定】`。
> 凡标注 `【陷阱】` 的是实测踩过的坑，照做即可。

---

## 0. 项目背景（必读，避免改错层）

### 0.1 三层血缘（决定「能改哪一层」）

| 层 | 仓库 | 职责 |
| --- | --- | --- |
| ① 原作者 | `Sliverkiss/workbuddy2api` | 账号池、调度器、上游客户端、WAF/IP 级防护 |
| ② 二创 | `baiyea/workbuddy2api-ui` | 增加 `console/`、`patches/`、`extensions/`、容器化 |
| ③ **本仓** | `renp666/workbuddy2api-ui-me` | 只改 `console/`、文档、仓库卫生 |

**推论（本单的行动边界）**：

- **新增通道** = 纯 `console/` + `deploy/` 改动，**不碰 core**。这是本单 P1 的全部内容。
- **功能吸收**（P2）若触及 core 语义（账号池、积分、限流），必须走 `extensions/`（新能力）或 `patches/`（改上游既有文件），**必须单独设计评审**，不得与通道批次混做（见 §4 分层说明）。

### 0.2 当前通道基线（本单的起点，务必先核对）

`console/route.go` 现有 6 个通道枚举（`routeChannelOrder`）：

| 通道 | 前缀 | realm | 开关变量 | 性质 |
| --- | --- | --- | --- | --- |
| `core` | —（默认） | — | 恒开 | WorkBuddy 主上游 |
| `glm` | `glm-`（`zcodeModelPrefix`） | `zcode` | `WB2A_ZCODE_URL` | 旁路 |
| `qoder` | `qoder-` | `qoder` | `WB2A_QODER_URL` | 旁路 |
| `opencode` | `opencode-` | `opencode` | `WB2A_OPENCODE_URL` | 旁路·免费 |
| `autoclaw` | `autoclaw-` | `autoclaw` | `WB2A_AUTOCLAW_URL` | 旁路·免费 |
| `trae` | `trae-` | `trae` | `WB2A_TRAE_URL` | 旁路·免费 |

CCR（`console/ccr.go`）是**宿主机进程**，不计入通道枚举。

**「免费通道」精确语义（必须前后端同真源）**：此处的「免费」**不是「不收费」，而是「不参与积分开关、恒放行」**——
`console/credits.go` 的 `modelGate` 对 `opencode-` / `autoclaw-` / `trae-` 三前缀**在读取任何倍率规则之前直接 `return true`**，
与 `glm-`/`qoder-` 的「不下发倍率 → 视为未知 → 默认关闭」是**相反默认态**；前端 `channelModelRows` 对应 `switchable=false`。
**新增通道必须同时决定自己属于哪一侧**（恒放行 or 未知默认关），并保证后端 `modelGate` 与前端 `switchable` 同口径。
**【已拍板】本轮新增的 `raccoon-` / `loomy-` 两前缀按「恒放行」侧落地**（§3.4-4）。

### 0.3 本单方法论真源

`integrate-agent-proxy` 技能（`SKILL.md`）：
① 侦察第三方镜像 → ② Compose overlay → ③ console 分流器 → ④ 控制链路 → ⑤ 自启动协程 → ⑥ 前端页签
，外加 **「新增通道公共接入检查清单」7 子节**（教训来自 trae/autoclaw 接入时的公共面漏改）。
**每加一个通道，必须逐项过完 7 子节，缺一即补。**

---

### 0.4 【决策】开发方式：全部在 Trae 直接开发，不启用 AgentFlowMe

需求方已拍板：**本单全部在 Trae 中直接开发**。对比结论如下（6 维度）：

| 维度 | Trae 直接开发 | AgentFlowMe 工作流 | 裁定 |
| --- | --- | --- | --- |
| 任务形态匹配 | 本单是「按既定 SKILL 逐通道复刻」，判据确定、无需多 Agent 探索 | 强在需求发散、多角色拆卡 | **Trae 胜** |
| 验收判据 | `web_test.cjs` / `go test` / compose config 都是确定性命令 | 判据需额外写卡，收益低 | **Trae 胜** |
| 并行收益 | 两路通道共享 `route.go` / `server.go` / 前端枚举，**强耦合**，并行会互相踩 | 适合独立切片，此处不成立 | **Trae 胜** |
| 环境成本 | 无 | K17（中文 Windows GBK 崩）/ K2（失败仍 exit=0）/ K43（无只读开关，派发即真实写码）/ 单轮 145s | **Trae 胜** |
| 工作区纪律 | 直接受控 | ADR-005 要求执行者自行 `git commit`，与本仓 10 个未提交改动**直接冲突** | **Trae 胜** |
| 自举耦合 | 无 | AgentFlowMe 的 `wb2a-local` provider **就是本仓 127.0.0.1:7863 网关**；用本仓工具链改本仓，网关重启窗口内派单必挂 | **Trae 胜** |

**两个决定性理由**：

1. **自举耦合（循环依赖）**：AgentFlowMe 主控与员工 profile 走 `provider: wb2a-local`，指向本仓网关。
   本单要改的正是这个网关的 console 层。改到一半重启 console → 派单链路自己先断。
2. **工作区纪律冲突**：本仓当前有 10 个未提交改动（trae/autoclaw 接入修复）需保留；
   ADR-005 要求执行者持有代码主权、自行 `git commit`，而 K43 又明确「只在干净工作树派单」——两条前提同时不成立。

**AgentFlowMe 的保留定位（不做本单，但保留）**：作为**旁支活**的备用通道，适用于
「独立目录 sidecar 骨架搭建」「参考项目源码研读/摘要」这类**与本仓工作树无耦合**的探索性任务。
本单不做任何 AgentFlowMe 派单。

---

## 1. 任务总览

| ID | 阶段 | 规模 | 依赖 | 交付物 |
| --- | --- | --- | --- | --- |
| **P0** | 基线固化 | 极小 | 无 | 已提交的 10 个改动 + 本计划书 + 基线判据读数 |
| **P1** | 首批通道（**实做小浣熊 / Loomy 两路**；QwenWork deferred） | 大 | P0 | 2 × （overlay + 分流器 + 控制链路 + 页签 + 测试）+ 跨端一致性守卫测试（§6.4） |
| ~~P2~~ | ~~参考项目能力吸收~~ | —— | —— | **【已拍板】本轮不做**，留作后续单独立项（§4） |
| **P3** | 收尾 | 小 | P1 | AGENTS.md 架构图 + README + 全量验证记录 |

**执行顺序**：P0 → P1（逐通道串行，每路完成即验收）→ P3。（P2 本轮不做。）
**本单交付范围 = P0 + P1（两路）+ P3。**

---

## 2. P0 基线固化（先做，保住产出）

### 2.1 背景

当前工作区（`git status --short` 实测）：

```text
 M console/autoclaw.go
 M console/credits.go
 M console/probe_test.go
 M console/route.go
 M console/route_test.go
 M console/usage_report_test.go
 M console/web/app.js
 M console/web/style.css
 M console/web_test.cjs
 M deploy/trae2api/trae/server/handler.go
?? .video_agent/
?? console/workbuddy2api-console.exe
?? docs/superpowers/plans/2026-10-08-channel-expansion-plan.md
```

前 10 项是 trae/autoclaw 接入的**真实修复**（公共面回填），必须保留并固化。
`console/workbuddy2api-console.exe` 是本地编译产物、`.video_agent/` 是工具目录，**不入库**。
`2026-10-08-channel-expansion-plan.md`（本计划书）是新增文档，**随 P0 一并入库**。

**实测（2026-10-08）**：`.gitignore` 当前**未覆盖**这两条 —— 无 `.video_agent/`、无 `*.exe` 规则（见 §2.2-1）。

### 2.2 任务

1. 在 `.gitignore` **只加这两条忽略规则**：`.video_agent/` 与 `*.exe`（不得扩大改动范围）。
2. 本地提交前 10 个改动 + 本计划书，提交信息体现「trae/autoclaw 公共面接入修复」。

### 2.3 验收条件

| 编号 | 条件 | 验证方式 |
| --- | --- | --- |
| P0-a | 10 个改动 + 本计划书已提交，工作区 `git status --short` 只剩被忽略项 | `git status --short` |
| P0-b | 提交**未**包含 `.exe` 与 `.video_agent/` | `git show --stat HEAD` |
| P0-c | 基线判据读数已记录（供 P1 对比） | 见 §6 命令实跑输出 |
| P0-d | **未推送、未发布、未部署** | 无 `git push` 记录 |

### 2.4 【已拍板】P0 基线提交

- **决策（2026-10-08）：先提交这 10 个改动作为 P0 基线。** 否则 P1 的 diff 会与旧改动混在一起，验收方无法分辨。

---

## 3. P1 首批通道（候选 3 路，实做 2 路）

### 3.0 通道候选与来源（已确认的参考实现）

| 通道 | 上游服务 | 现成参考实现 | 前缀【已定稿】 | realm【已定稿】 |
| --- | --- | --- | --- | --- |
| **小浣熊** | 商汤 小浣熊 | `aimod-cc/agent2api`（支持小浣熊/商汤） | `raccoon-` | `raccoon` |
| **QwenWork** | 通义千问 QwenWork | `wicm84266964/Buddy2api`（含 QwenWork 通道） | `qwenwork-` | `qwenwork` |
| **Loomy** | 讯飞 Loomy | `wangliangdong/loomy2api`（FastAPI 封装） | `loomy-` | `loomy` |

> **前缀与 realm 已于 2026-10-08 拍板定稿**（一旦入库即为公共模型名，改名是破坏性变更）。
> 定稿原则：前缀不得与 `reservedAliasPrefixes`（现有 `glm-/qoder-/opencode-/autoclaw-/trae-/cn:/global:`）冲突；
> 且要能通过 `validateRouteEntries` 的别名校验。**两路前缀（`raccoon-` / `loomy-`）均已通过该原则**；
> `qwenwork-` 本轮 deferred（见下），仅预留、不入本单实现。

> **【决策】QwenWork 本轮暂缓**（2026-10-08 拍板，侦察见 §3.3）：QwenWork 依赖 Windows + DPAPI，
> Linux Docker 不可用，与本仓全容器部署模型冲突。前缀 `qwenwork-` / realm `qwenwork` 保留在表中，
> 状态标记为 **deferred**，待条件成熟（出现纯 Linux 可用实现）再启动。
> **本单 P1 实际落地 = 小浣熊 + Loomy 两路。**

### 3.1 逐通道统一落地骨架（小浣熊 / Loomy 两路同构，按 SKILL 六大步骤展开）

**每一路通道都必须完成下列 6 步 + 检查清单 7 子节。** 下列文件名以通道名 `<name>` 占位。

#### 步骤 1｜侦察第三方镜像（不可跳过）

- 确认：代理端口、控制端口（若有）、登录态持久化路径、启动命令、是否自带自动启动。
- **【陷阱】** 有的镜像故意只起控制端不起代理（zcode 实测），且无自动启动配置项 —— **不要改镜像/启动脚本**，
  用 console 协程拉起（步骤 5）。
- 交付：一份侦察结论（端口 / 卷路径 / 启动方式 / 是否需 OAuth）。

#### 步骤 2｜Compose overlay `deploy/compose.<name>.yml`

照 `deploy/compose.qoder.yml` 模板（**已实测结构**）：

```yaml
services:
  <name>-proxy:
    build: { context: ., dockerfile: deploy/<name>.Dockerfile }   # 或 image: <第三方镜像>
    platform: linux/amd64
    restart: unless-stopped
    network_mode: service:console          # 共享 console 网络命名空间
    environment:
      TZ: Asia/Shanghai
      PORT: "<代理端口>"
      CONTROL_PORT: "<控制端口>"           # 若有控制面
      PROXY_API_KEY: ${WB2A_<NAME>_KEY:-}
      CONTROL_API_KEY: ${WB2A_<NAME>_KEY:-}
    volumes:
      - ./runtime/<name>:/<镜像内的持久化目录>   # 已属 .gitignore 域
  console:
    environment:
      WB2A_<NAME>_URL: http://127.0.0.1:<代理端口>
      WB2A_<NAME>_KEY: ${WB2A_<NAME>_KEY:-}
      WB2A_<NAME>_CONTROL_URL: http://127.0.0.1:<控制端口>   # 仅当有控制面
    depends_on:
      core: { condition: service_started }
```

**【陷阱】** overlay 里的相对路径以**被引入文件所在目录**解析。
本仓 override 用 `include:` 的 `path:` + `project_directory: data` 长形式规避（见 AGENTS.md）；
新增 overlay 必须沿用同一约定，否则 `./runtime/<name>` 会挂空并静默新建目录。

#### 步骤 3｜console 分流器 `console/<name>.go`（仿 `console/zcode.go` / `trae.go`）

公共入口 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按 `model` 前缀分流；
`/v1/models` 合并上游列表（新通道模型加 `<前缀>` + `realm: <name>` 标签）。

**必须同步的后端枚举点（检查清单 §1，逐项核对）**：

| 文件 | 同步点 |
| --- | --- |
| `console/route.go` | `channel<Name>` 常量、`routeChannelOrder`、`prefix()`、`realm()`、`reservedAliasPrefixes`、`channelEnabled()`、`channelUpstream()`、`routeCatalog`、`validateRouteEntries` 错误文案、`collectProbeCandidates` 通道循环 |
| `console/server.go` | `Config` 的 `<Name>URL` / `<Name>Key`（+`<Name>ControlURL`）字段、`validOriginURL` 校验、`/admin/session` 与状态响应的 `<name>_enabled` 字段 |
| `console/usage_report.go` | `newUsageCapture` 的 `uid` / 账号名（旁路 usage 落 core 账本，**模型名保留公共前缀**） |
| `console/probe.go` | `probeableChannels` 白名单（决定测速探测能否选到该通道） |
| `console/credits.go` | `modelGate` 免费豁免（**本轮 `raccoon-` / `loomy-` 均为免费通道，须加入豁免前缀**） |

`route.go` 现有错误文案需追加新通道名：

```go
// 现文案
"别名 %q 的通道必须是 core、glm、qoder、opencode、autoclaw 或 trae"
// 本单落地两路后需追加（QwenWork 本轮 deferred，不入本单文案）
"别名 %q 的通道必须是 core、glm、qoder、opencode、autoclaw、trae、raccoon 或 loomy"
```

`collectProbeCandidates` 现有循环：

```go
[]routeChannel{channelCore, channelGLM, channelQoder, channelAutoClaw, channelTrae}
```

#### 步骤 4｜控制链路 `console/<name>control.go`（仿 `console/zcodecontrol.go`）

- `/admin/<name>` 状态端点 + `/admin/<name>/login|config|enable|disable|logout`（按该通道实际能力裁剪）。
- console **零 Docker 权限**；注入代理密钥时**剥掉管理 Cookie、CSRF 与部署 API Key**。
- **严格入口校验**：对话转发端点强制模型前缀，不作为通用转发口（防 SSRF/越权）。
- **【决策】参考两种既有形态**：
  - **只读形态**（qoder / opencode）：无控制面，页签只展示状态 + 对话测试。
  - **完整控制形态**（autoclaw / trae）：登录 / 账号 / 积分 / 启停。
  - **【已拍板】本单小浣熊 / Loomy 两路按「完整控制形态」落地**，但受 §3.4-3 的侦察边界裁剪
    （小浣熊=凭证导入；Loomy=noVNC 人工登录且无刷新凭据）。

#### 步骤 5｜自启动协程（仅当通道有控制面且镜像不自启）

- console 启动时（**仅配置了控制 URL 时**）起 15s ticker：
  控制端可达 + `logged_in == true` + 代理未运行 + 本周期用户没手动停过 → `startProxy`。
- 手动「停用」置内存标志，本运行周期不再拉起；手动「启用」清标志；容器重启内存归零即恢复。
- 未登录绝不拉起；只在成功拉起时打一条日志，空转不吵。

#### 步骤 6｜前端页签 `console/web/`

**必须同步的前端枚举点（检查清单 §2，逐项核对）**：

| 位置 | 同步点 |
| --- | --- |
| `console/web/app.js` | `probeChannelNames`（**必须与后端 `probeableChannels` 同真源**）、`modelChannels`、`modelKey`、`channelModelRows` |
| `channelModelRows` 的 `switchable` | 免费通道一律 `false`，须与后端 `credits.go` `modelGate` 同口径 |
| `applyProbeResults` 注释 / `renderModelTable` 复用 | 旁路状态端点把探测结论塞进 `health.modelResults` |
| 右上角刷新 | `data-refresh-for` 覆盖新页签 |

**【陷阱】`health` 字段语义冲突（检查清单 §4）**：
若旁路状态端点已用 `result["health"]` 承载 `/healthz` 探活快照（如 autoclaw 的 `{healthy,total}`），
**探测结论必须按 key 合并进同一 map，不能整体覆盖**，否则探活读数被探测结果冲掉。
接入前先确认该通道 `health` 归谁，再决定合并还是独占。

**【陷阱】页签测试**：`web_test.cjs` 的 element mock 有 handlers/append/children；
事件委托 handler 要 `return pinAction(btn)` 式返回 Promise，否则测试 await 不到完整链路。

#### 步骤 7｜测试夹具同步（检查清单 §5，否则新通道零覆盖）

| 文件 | 同步点 |
| --- | --- |
| `console/usage_report_test.go` | `usagePair` 按通道 switch 挂上游，**缺分支则该通道上报路径无测试** |
| `console/probe_test.go` | `snapshotChannel`、报错文案、`TestProbeSideChannelStatusHealth` 的通道循环 |
| `console/route_test.go` | `sideExemptConsole` 统一构造免费通道豁免用例（`TestModelSwitch<Name>Exempt`） |
| `console/web_test.cjs` | 刷新按钮 ids/view 循环、`overviewFixture` 的 `extraStatuses`、概览聚合测试的通道数 |

**做法**：对照既有通道（qoder/opencode）模板复制，比从零写更快且不漏断言。

### 3.2 每路通道的验收条件（模板，两路各自独立跑）

| 编号 | 条件 | 验证方式 |
| --- | --- | --- |
| C-a | 未设开关变量时公共出口**逐字节不变** | 关掉 `WB2A_<NAME>_URL` 跑现有测试，全绿 |
| C-b | `/v1/models` 合并了新通道模型且带 `realm` 标签 | 起栈后 `curl /v1/models` 实查 |
| C-c | 前缀分流生效（`<prefix>` 请求打到新 sidecar） | 起栈后发一次对话请求 |
| C-d | 后端枚举 6 处（route/server/usage_report/probe/credits/分流器）全部同步 | 代码走查 + `grep` |
| C-e | 前端枚举 4 处（probeChannelNames/modelChannels/modelKey/channelModelRows）全部同步 | 代码走查 |
| C-f | 测试夹具 4 文件全部同步且新通道有断言 | `go test ./...` + `node --test` |
| C-g | 公共页面 7 项自检通过 | 见 §6.2 |
| C-h | 免费通道（若适用）前后端豁免同口径 | `route_test.go` 豁免用例 + 前端 `switchable=false` |
| C-i | **跨端一致性守卫测试已补且红/绿实测通过** | 见 §6.4（后端名单 vs 前端名单集合/顺序比对） |

### 3.3 参考项目侦察结论（2026-10-08，决定接入形态的硬约束）

| 参考项目 | 形态 | 与本仓「一通道一 sidecar」的匹配度 | 关键约束 |
| --- | --- | --- | --- |
| `wicm84266964/Buddy2api` | **单进程 Python 多通道网关**（端口 8787，默认 4 通道：WorkBuddy/CodeBuddy、QClaw、QwenWork、TraeWork） | **不匹配**：要接 QwenWork 得从网关里抽取单通道，或把整网关当一个 sidecar（会带进它自带的其他通道） | **QwenWork 登录态在 `%APPDATA%\QwenWorkCN`，DPAPI 加密、依赖 Windows 本机客户端 → Linux Docker 读不了**；管理页免 Token 仅本机可用 |
| `aimod-cc/agent2api` | **Tauri 桌面（Rust）+ Docker 双形态**（端口 3065，多提供商含小浣熊） | **不匹配**：多提供商单进程 | 小浣熊在 Docker 下**网页登录与「导入本机桌面端登录态」不可用 → 只能粘贴凭证**；License = **MIT + 使用声明限制（禁商用/禁二次分发牟利/禁批量账号运营），非纯 MIT** |
| `wangliangdong/loomy2api` | **单通道 FastAPI + Docker**（端口 7865） | **匹配度最高**：单通道、容器化，最接近本仓 sidecar 形态 | 需容器内 Chromium + noVNC（登录端口 6080）；**明确不支持工具调用**（Loomy Web 只收单 `content` 字段）；Cookie 需人工续期（无 refresh token）；License = **「仅供个人学习与自用」**（非标准开源） |

**由侦察直接推出的三条硬约束（写入本单边界）**：

1. **禁止把整个第三方多通道网关当作本仓 sidecar 直接引入** —— 会带进它的其他通道，破坏「一通道一前缀」的公共命名空间纪律。若用其代码，必须**只抽取目标单通道**。
2. **自建 Dockerfile 而非直接依赖第三方镜像** —— 尤其 agent2api（MIT+限制）与 loomy2api（非标准 License）有合规风险；照 `deploy/trae.Dockerfile` / `deploy/autoclaw.Dockerfile` 模式自建，代码引用须在交付说明中标注来源与 License。
3. **登录能力受限于容器环境** —— 小浣熊 Docker 下**只能粘贴凭证**，故其控制形态至少需「凭证导入」能力（参照 trae 的凭据导入），不能假设有网页登录。

### 3.4 需求方拍板记录（开工前确认项）

1. ~~三路通道的**前缀与 realm 定稿**~~ —— **已拍板**（§3.0）。
2. ~~QwenWork 接法~~ —— **已拍板：本轮暂缓**（§3.0）。
3. ~~小浣熊 + Loomy 两路的**形态**~~ —— **已拍板：两路都完整控制形态**（2026-10-08）。
   在侦察约束下的**可行边界**（不是照搬 autoclaw 的手机号登录）：
   - **小浣熊**：Docker 下无网页登录 → 登录环节 = **凭证导入**；完整链路 = 凭证导入 + 账号列表 + 启停 / 删除 + 状态刷新。
   - **Loomy**：登录 = 打开容器内 noVNC（`6080`）由**人工完成**，console 不代填；**无 refresh token → 不做「刷新凭据」按钮**（只标记「Cookie 需人工续期」）；完整链路 = 账号列表 + 启停 / 删除 + 额度 / 状态刷新。
   - **硬前提（由本决策引出）**：完整控制形态要求 sidecar **暴露控制 API**，而 loomy2api / agent2api 均**未暴露与本仓 admin 链路对齐的控制 API** → 见 §3.5 决策。
4. ~~两路通道**是否免费**~~ —— **已拍板：两路都免费（恒放行）**（2026-10-08）。
   即 `credits.go` `modelGate` 对 `raccoon-` / `loomy-` 在读取任何倍率规则前直接 `return true`；
   前端 `channelModelRows` 对应 `switchable=false`。**前后端必须同口径。**
5. ~~**完整控制形态的实现载体**~~ —— **已拍板：自建 sidecar + 自写控制 API**（§3.5）。
6. ~~P2 的**实施顺序与是否本轮做**~~ —— **已拍板：本轮不做 P2**（§4）。

### 3.5 【已拍板】完整控制形态的实现载体

**决策（2026-10-08）：路径 (a) 自建 sidecar + 自写控制 API。**
照 `deploy/trae.Dockerfile` / `deploy/autoclaw.Dockerfile` 模式，**从目标项目只抽取单通道数据面**，
由本仓补写控制 API（账号 / 登录 / 启停 / 状态），console 走 `/admin/<name>/*`。

被否决的路径：

| 路径 | 做法 | 否决理由 |
| --- | --- | --- |
| (b) 复用第三方 sidecar 数据文件 | 挂载 `accounts.json` / `pool_state.json`，console 直接读写 | **违反架构纪律**（console 不加载账号凭据、不挂载账号目录）；第三方文件格式无契约、易碎 |
| (c) 不做控制面（回退只读） | 页签只展示状态 + 对话测试 | 与 §3.4-3 已拍板的「完整控制形态」相违 |

**自建的连带要求**：
- 目标项目代码引用须在交付说明标注来源与 License（agent2api=MIT+限制、loomy2api=非标准）。
- sidecar 只监听 `127.0.0.1`，`network_mode: service:console`，与本仓既有五只旁路同构。
- 控制 API 须与 `console/<name>control.go` 的 `/admin/<name>/*` 契约对齐（参考 `zcodecontrol.go` / `traecontrol.go`）。

---

## 4. P2 参考项目能力吸收（候选，需再评审后实施）

> **【已拍板】本轮不做 P2**（2026-10-08）。P2 六项多数触及 core 语义，必须单独出设计文档 + 评审，
> 不与 P1 通道批次混做；本单交付范围 = P0 + P1（小浣熊 + Loomy 两路）。下列内容留作**后续单独立项**的输入。

> **分层前提（重要）**：下列能力多数触及 **core 语义**（账号池 / 积分 / 限流 / 调度），
> 属 ①②层，本仓要落地必须走 `extensions/`（新能力）或 `patches/`（改上游既有文件），
> 且**必须单独出设计文档 + 评审**，不得与 P1 通道批次混做。
> 只有「报表页 / 前端展示」这类纯 console 项可直接落 `console/`。

| ID | 能力 | 来源项目 | 落点层 | 建议优先级 |
| --- | --- | --- | --- | --- |
| **A** | 模型容量自动发现（探测真实输出上限） | Buddy2api / workbuddy2api-panel | core（patches/extensions）+ console 展示 | 中 |
| **B** | 多档思考强度 | Buddy2api | core（patches）+ console 页签 | 中 |
| **C** | 积分保底 `pool.credit_floor` + 企业版账号自动识别 | workbuddy2api-panel | **core（patches）** | 高（影响账号存活） |
| **D** | 四条限额护栏 + 多 API Key 与出口绑定 + 设备指纹隔离 | workbuddy2api-hub | **core（patches）** | 高（安全相关） |
| **E** | 429 `code=6004` 模型级限流冷却语义 | workbuddy2api-panel | **core（patches）** | 高（与补丁 0006/0008 同族，需交互分析） |
| **F** | SWRR 平滑加权轮询 + 会话粘性 + 错误分类故障转移 | loomy2api | **core（patches）** | 中（改调度器，风险高） |

**流程要求**：

1. **先出设计文档**（`docs/superpowers/specs/`），逐项说明：现状差异、改动层、补丁编号规划、移除条件、回归风险。
2. 逐项**单独验收**，一项通过再做下一项。
3. **C / D / E 三项与安全/账号存活强相关，建议优先**；F 改调度器风险最高，建议最后。
4. 每项落地后更新 `patches/README.md`（含**移除条件**）与 `patches/series`。

### 4.1 P2 验收条件（通用）

| 编号 | 条件 |
| --- | --- |
| F-a | 每项先有设计文档，且文档写明「改动层」与「移除条件」 |
| F-b | 触及上游文件的改动**一律走补丁**，不直接改 `upstream/` |
| F-c | 补丁基于**本仓快照的实际文本**生成（不得照抄上游 HEAD 的 diff） |
| F-d | 物化时 `git apply --check` 全部通过（`overlay.py prepare` rc=0） |
| F-e | 现有回归测试全绿；新增行为有能先失败的回归测试 |
| F-f | `patches/README.md` 与 `patches/series` 同步更新 |

---

## 5. P3 收尾

| 文件 | 更新内容 |
| --- | --- |
| `AGENTS.md` | 架构图补每条新通道分支（与既有 zcode/qoder/opencode/autoclaw/trae 段落同格式） |
| `README.md` | 补新通道使用说明（面向使用者，不写成开发手册） |
| `docs/superpowers/verification/2026-10-08-channel-expansion-verification.md` | 新增：逐通道红态/绿态实测记录、公共页面自检结果 |

### 5.1 验收条件

| 编号 | 条件 |
| --- | --- |
| P3-a | AGENTS.md 架构图每条新通道有独立分支描述 |
| P3-b | README 说明可被使用者照做（含开关变量与模型前缀） |
| P3-c | 验证记录逐条贴**实际命令 + 实际输出** |
| P3-d | 明确列出**未验证项**及原因（不得隐去） |

---

## 6. 通用验证清单（每阶段都必须实跑）

### 6.1 命令

```powershell
cd D:\work\02toolsMy\workbuddy2api-ui-me

# 宿主侧轻量判据
node --test console/web_test.cjs
git diff --check
docker compose --env-file /dev/null -f docker-compose.yml config --quiet

# console 编译与测试（宿主无 go 时进容器）
go -C console build ./...
go -C console test ./...
```

**容器内跑 core 侧（宿主无 go / 无 gcc 时）**：参考 `2026-09-23-trae-task-ticket.md` §6 的
`golang:1.23-alpine` + `GOPROXY` + `git clone /host` 物化流程。

**【陷阱】本机环境**：

- `go` 不在 PATH，实际在 `D:\gogogo\go\bin\go.exe`；须先 `$env:GOROOT=''` 才能编译。
- 本机无 gcc → **`-race` 跑不了**，跳过时必须如实说明，不得谎报通过。
- 宿主 `python3` 是 Microsoft Store 占位符，真解释器是 `python`。
- PowerShell 不支持 `head`（改落盘 + Read）。

### 6.2 公共页面 7 项自检（用户视角逐页过一遍，检查清单 §6）

- 【运行概览】通道分布聚合是否含新通道。
- 【调用统计】通道筛选与聚合是否含新通道。
- 【Agent 接入】客户端示例 / 别名表 / 能力说明是否含新通道。
- 【模型列表】`/v1/models` 是否合并了新通道模型（带 `realm` 标签）。
- 【测速探测】白名单是否含新通道（免费通道是否按设计排除）。
- 右上角刷新按钮：`data-refresh-for` 是否覆盖新页签。
- 【API 接入】页是否含新通道说明。

### 6.3 已知环境性失败（**不是回归**，不要试图修）

| 项 | 现象 | 定性 |
| --- | --- | --- |
| `python -m unittest discover -s scripts` | 若干 failures/errors | 环境性（WinError 1314 无 symlink 特权、无 `os.mkfifo`、NTFS 无执行位等） |
| `python -m unittest discover -s deploy` | 若干 failures | 环境性（NTFS 无 POSIX 权限位、Windows 路径分隔符） |
| `gofmt -l` | 把所有文件列为未格式化 | CRLF 工作副本假阳性 |

**判据**：这些项在「改动前基线」与「改动后」必须**计数相同**，要贴两次对比。

### 6.4 【必须新增】跨端一致性守卫测试（本轮根因修复项）

**根因（2026-10-08 侦察确认）**：探测白名单有**两个真源**——
后端 `console/probe.go` 的 `probeableChannels`（5 项，排除 opencode）与
前端 `console/web/app.js` 的 `probeChannelNames`（5 键，值 core/glm/qoder/autoclaw/trae）。
二者被要求「必须同真源」，但**当前仓库无任何测试守卫**：
`console/*_test.go` 对 `probeableChannels` / `probeChannelNames` / `routeChannelOrder` **零匹配**，
`web_test.cjs` 只从 `probeChannelNames` 现读名单去核对 **HTML 按钮是否存在**（`probe-<name>` / `probe-<name>-progress`），
**不核对后端名单**。因此二者漂移时测试全绿，这正是 trae / autoclaw 接入时「公共面漏改」的未修复根因。

**要求**：P1 每新增一路通道时，必须同时补一条**能失败的守卫测试**，覆盖下列两组同真源关系：

| 守卫 | 后端真源 | 前端真源 | 断言 |
| --- | --- | --- | --- |
| G-1 探测白名单 | `probe.go` `probeableChannels` | `app.js` `probeChannelNames`（值） | 集合相等；且**都必须排除 opencode** |
| G-2 通道顺序 | `route.go` `routeChannelOrder` | `app.js` `usageChannelOrder`（值） | 顺序与集合一致（`workbuddy` 对应后端 `channelCore`） |

**实现方式（择一，倾向前者）**：

- **Go 侧**（推荐）：在 `console` 加一个测试，读 `web/app.js` 源码文本，
  用正则抽出 `probeChannelNames` / `usageChannelOrder` 的字面量，与 `probeableChannels` / `routeChannelOrder` 比对。
  好处：与后端真源同进程、可在 `go test ./...` 一次跑出，且**改动任一真源都会立刻红**。
- **Node 侧**：在 `web_test.cjs` 里同时读 `probe.go` 与 `app.js` 文本比对。
  好处：复用既有 `objectKeysFrom` 现读手法；代价：需在 CJS 里解析 Go 源码，脆性更高。

**验收**：故意把 `app.js` 的 `probeChannelNames` 删掉 `trae` 一项，守卫测试必须**红**；
恢复后必须**绿**。这条红/绿实测要贴进验证记录（§5 表 P3-c）。

---

## 7. 交付要求

### 7.1 每个阶段的交付说明必须包含

- 改了哪些文件（完整路径）
- 每个文件的变更摘要
- 每条验收条件的**实跑结果**（贴输出摘要）
- **未能验证的项目**，及原因（不得隐去）
- 与任务书的任何**偏离**，及理由
- 是否产生孤立代码

### 7.2 禁止行为

- ❌ 不许说「完成」而不贴验证输出
- ❌ 不许把环境性失败算成回归或算成通过
- ❌ 不许在容器起不来时声称 `-race` 通过
- ❌ 不许直接改 `upstream/` 或 `upstream.lock`（上游既有文件改动一律走 `patches/`）
- ❌ 不许 `git push`、发布镜像、重启真实服务
- ❌ 不许重置工作区、清空数据、覆盖真实配置
- ❌ 不许照抄上游 HEAD 的 diff 当补丁（行号上下文不同，会 apply 失败）
- ❌ 不许为通过验证而关闭同源 / CSRF / 会话归属 / 桥接认证
- ❌ 不许把 `WB2A_<NAME>_URL` 未设置时的公共出口行为改动哪怕一个字节

---

## 8. 验收方将如何验收

验收方（需求方 AI）会**独立复跑**，不依赖实施方自述：

1. `git status --short` 检查工作区是否被污染、有无未跟踪垃圾。
2. 代码走查：新通道是否在**后端 6 处 + 前端 4 处 + 测试 4 文件**全部同步（对照 §3.1 步骤 3/6/7 表格逐项打钩）。
3. 独立跑 §6.1 全部命令，把输出与**自己的基线计数**对比。
4. **对抗测试**：
   - 关掉 `WB2A_<NAME>_URL`，确认公共出口与现状逐字节一致（`diff` 前后 `/v1/models` 输出）。
   - 故意发一个**不带新前缀**的请求到新通道对话端点，确认被拒（入口强校验）。
   - 故意发一个**带新前缀**的请求到 `/admin/*`，确认管理链路不受影响。
   - 免费通道：确认前端 `switchable=false` 与后端 `modelGate` 豁免同口径（任一侧漏改即为缺陷）。
   - **守卫测试红/绿**：按 §6.4，先故意删掉前端一处通道，确认守卫**红**；恢复后确认**绿**。
5. 检查 `AGENTS.md` / `README.md` / `patches/README.md` 是否同步。

**验收不通过的典型原因**（提前告知）：

- 只断言「测试通过」而没验证「未设开关时逐字节不变」；
- 新通道只改了业务链路，漏改公共面枚举（这正是 trae/autoclaw 接入时的教训）；
- 免费通道前后端豁免口径不一致；
- 把 `go test` 绿当成 `-race` 绿；
- 功能吸收（P2）混进通道批次，导致改动层越界。

---

## 9. 授权边界（不可越界）

| 动作 | 状态 |
| --- | --- |
| 本地提交 | ✅ 允许 |
| **推送 Git** | ❌ **禁止**（需单独授权） |
| **发布镜像** | ❌ **禁止**；发布 ≠ 部署 |
| **部署 / 重启真实服务** | ❌ **禁止** |
| **更新 `upstream.lock` / 整包更新上游** | ❌ **禁止** |
| 重置工作区 / 清空数据 / 覆盖真实配置 | ❌ **禁止** |
| 修改既有补丁 0001–0007 的语义 | ❌ **禁止**（只允许新增） |

---

## 10. 附：本单依赖的既有事实（避免重复劳动）

- **当前 6 通道**：`core` / `glm` / `qoder` / `opencode` / `autoclaw` / `trae`；CCR 是宿主进程不计通道。
- **免费通道三前缀**：`opencode-` / `autoclaw-` / `trae-`（`credits.go` `modelGate` 豁免 + 前端 `switchable=false`）。
- **探测白名单双真源**：后端 `probe.go` `probeableChannels`（现 5 项）= 前端 `app.js` `probeChannelNames`（现 5 键）。
  （opencode 被排除，因 OW Bridge 自带探测。）**当前无任何测试守卫二者漂移** → 见 §6.4，P1 必须补。
- **通道枚举全部同步点**已在 §3.1 步骤 3/6/7 列全，直接照做即可，不必重新盘点。
- **overlay 模板**：`deploy/compose.qoder.yml`（已实测结构，含 `network_mode: service:console` 与 loopback-only）。
- **方法论真源**：`integrate-agent-proxy` 技能（六大步骤 + 公共接入检查清单 7 子节）。

### 10.1 开工前需需求方拍板的清单（**全部已闭环**）

1. ~~P0 是否先提交现有 10 个改动~~ —— **已拍板：先提交为 P0 基线**（§2.4）。
2. ~~三路通道的**前缀与 realm**定稿~~ —— **已拍板**（§3.0）。
3. ~~QwenWork 接法~~ —— **已拍板：本轮暂缓**（§3.0 / §3.3）。
4. ~~小浣熊 + Loomy 两路的**形态**~~ —— **已拍板：完整控制形态**（§3.4-3，含边界裁剪）。
5. ~~两路通道**是否免费**~~ —— **已拍板：两路都免费（恒放行）**（§3.4-4）。
6. ~~**完整控制形态的实现载体**~~ —— **已拍板：自建 sidecar + 自写控制 API**（§3.5）。
7. ~~P2 是否本轮做~~ —— **已拍板：本轮不做**（§4）。
8. ~~是否补跨端一致性守卫测试~~ —— **已拍板：必须补**（§6.4，本轮根因修复项，验收含红/绿实测）。
