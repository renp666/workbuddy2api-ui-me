# 上游安全修复回移设计（阶段 0 方案）

日期：2026-09-22
状态：**待人工确认**（阶段 0 门控未通过，不得进入实现）
需求来源：用户指令「修改上述 1、2、3 点，注意分析结论和后续改造方案都需要以文档驱动任务，方便后续追溯和查找需求来源」

## 需求来源追溯

本方案的上游是三轮只读对比分析的结论，全部有实测证据，不是文档复述：

| 编号 | 来源 | 关键实测证据 |
| --- | --- | --- |
| S-1 | `docs/superpowers/verification/2026-09-22-security-review-tracking.md` 第 0 节三层血缘 + 第 8 节遗留 | 本仓 `upstream/` 快照锚定 `c576b48`，落后原作者 `master` 214 个提交；`git rev-list --count c576b48..HEAD` = 214 |
| S-2 | 2026-09-22 补丁兼容性实测（本轮） | 把 `upstream HEAD` 导出独立工作树后逐个 `git apply --check`：5 个补丁**全部 CONFLICT**；`git apply -3` 全部仍失败 |
| S-3 | 同上 | 硬编译断裂：`extensions/cmd/server/extension.go:294` 引用 `cfg.Server.MaxBodyMB`，而上游 `e34cfa4 feat(server)!` 已把该字段退役为 `Server struct{}` |
| S-4 | 用户指令（本轮） | 用户从「是否整包更新」收敛为「修改 1、2、3 点」——即只回移三项安全/正确性修复，不整包更新 |

用户指定的三项（按用户原话的序号）：

1. **WAF IP 级熔断**（`internal/server/wafip.go`）—— 防整条出口 IP 被上游风控封掉。
2. **auth 数据竞争修复** —— 出站请求头锁外直读 token/domain。
3. **顺带项** —— `/v1/stats` 倍率列 + 流式 `usage` 缺失的 `-1` 哨兵（影响控制台显示准确性）。

## 需求背景

### 问题

本仓是三层血缘的第三层（原作者 `Sliverkiss/workbuddy2api` → 二创 `baiyea/workbuddy2api-ui` → 本仓 `renp666/workbuddy2api-ui-me`）。`upstream/` 是**历史快照**，落后原作者 214 个提交，导致原作者后续的安全加固不在本仓：

- 3 个账号 1 秒内全部命中 WAF 403 时，本仓会继续轮转，把一次客户端请求放大到 `MaxRotate`（默认 3）倍，持续打同一出口 IP，**加重风控**（原作者实测：WAF 拦的是网关出口 IP 而非账号）。
- 出站请求头在锁外直读 `AccessToken` / `Domain`，与 keepalive 的 `RefreshToken` 写回构成**数据竞争**（`go test -race` 可复现）。

### 用户

单人部署、自己管多个账号的使用者（本仓场景）。要 Web 控制台 + OpenAI/Anthropic 双协议，不要退回原创的纯 CLI 形态。

### 成功标准

1. 三项修复在本仓生效，且**不破坏**现有 `console/` 与 `extensions/` 能力。
2. 补丁/扩展的维护成本可控、可追溯——每项都有对应的测试与验证命令。
3. 不引入 `.env` 插值、不新增宿主启动脚本、不新增 Docker 健康检查（AGENTS.md 硬约束）。

## 边界约束

### Always

- 改 `upstream/` 既有文件**必须**走 `patches/`，同步更新 `patches/README.md` 与必要的 `patches/series`。
- 新增独立能力优先放 `extensions/`，不打补丁。
- 新增行为先补能失败的回归测试，再做最小修改（红 → 绿）。
- 只改必要点，不顺手重构相邻代码。
- 每项改动都要能追溯到本文件的修改点编号（M1/M2/M3）。

### Ask First

- 补丁编号与顺序调整（现有 `series` 为 0001–0005，新增会改变顺序语义）。
- 任何需要改动 `upstream.lock` / `upstream/` 快照的动作。
- 存量补丁 0003 中 `MaxBodyMB` 引用的处置方式（见 M4，有三种候选）。

### Never

- 不整包更新上游（用户未授权，且实测 5 补丁全冲突、有 1 处破坏性 API 变更）。
- 不重置工作区、不清空数据、不覆盖真实配置。
- 不删除或改写既有 5 个补丁来「让新补丁好写」。
- 不发布镜像、不推送 Git、不重启真实服务（均需单独授权）。
- 不把「模拟验收通过」说成「真实上游授权成功」。
- 不修改 `upstream/`（快照只读，靠 overlay 物化）。

## 修改点清单

### M1 — WAF IP 级 fail-fast 熔断

**目标**：短窗（60s）内 ≥2 个**不同** UID 接连命中 WAF 403 → 判定 IP 级拦截，轮转立即终止（放大倍数 = 1），窗口到自然解除。

**关键发现：这不是「复制一个文件」**。实测依赖链是三段，缺一不可：

| 上游提交 | 提供 | 本仓是否有 |
| --- | --- | --- |
| `76fafa6` | `upstream.ErrWafBlock` 分类 + `IsWafBlocked()` + `ParseRetryAfter()` + `Error.RetryAfter` 字段 | **无**（实测快照 `ErrKind` 只到 `ErrClient`，无 `ErrWafBlock`）。**回移时只取 `ErrWafBlock` + `IsWafBlocked`/`hasBusinessEnvelope`**，不带 `ParseRetryAfter`/`Error.RetryAfter` |
| `34405ca` | WAF 403 软冷却 + 轮转间指数退避，`applyErrorPolicy` 改由分类信封驱动 | **无**。**有意不回移**：IP 级熔断的核心是「停止轮转以止损」，不需要账号级惩罚；见 §M1「最小化回移范围」 |
| `8825c4c` | `internal/server/wafip.go` 状态机 + 在 `chatCompletions` 轮转循环插入调用 | **无**（回移，但 `wafip.go` 放 `extensions/`） |

> **【2026-09-22 修正·§12.4-1】** 本表初版把 `34405ca` 列为必做依赖，且下方候选 A 写成「把三段链 `76fafa6`+`34405ca`+`8825c4c` 最小化回移」——
> 与「只做 IP 级熔断、不做软冷却/退避」的决策**自相矛盾**。
> 由 T0 实施方（Qoder）在 `2026-09-22-backport-handoff.md` §12.4 提出，需求方核实确认。
> **以「最小集」为准**：`ErrWafBlock` + `IsWafBlocked` + `wafip.go` + 轮转 `break`，
> 不含 `34405ca` 的账号级软冷却与 `rotateBackoff`。
> 代价（已知取舍）：本仓 WAF 403 **不冷却账号**，仅 IP 级止损；该差异已写入补丁 0006 的测试注释。

且插入点所在的 `applyErrorPolicy` **签名已变**：

- 本仓快照：`func (h *Handler) applyErrorPolicy(uid string, kind upstream.ErrKind, body, model string)`
- 上游 HEAD：`(uid, kind, body, model, uerr)`（多了 `*upstream.Error`）

**涉及文件**：

- `patches/0006-waf-ip-failfast.patch`（新增）—— 触及 `internal/upstream/client.go`（Kind + Classify + RetryAfter）、`internal/server/handler.go`（字段 + 轮转插入点 + 末端文案）、`internal/server/wafip.go`（新增文件）
- 或（候选 B，见「待确认问题」）改为在 `extensions/` 内实现等价状态机

### M2 — auth 出站请求头数据竞争修复

**目标**：出站请求头取值一律经加锁读取器，消除 `RefreshToken` 写回与锁外直读的竞争。

**两条路线（必须二选一，不能都要）**：

| 路线 | 实现 | 优点 | 缺点 |
| --- | --- | --- | --- |
| **上游路线**（推荐） | `AccessTokenValue()` / `DomainValue()` / `RefreshTokenValue()`（`910b8b2`+`855e5b9`） | 已随上游演进，未来对齐成本低；上游已配套修了 `headers.go` 调用点 | 需替换补丁 0001 现有的 `Snapshot()` 思路 |
| 本仓现路线 | `Auth.Snapshot()` 私有副本 | 补丁 0001 已实现并可编译 | 与上游分叉，未来更新需手工合并两套语义 |

**实测冲突点**：补丁 0001 往 `auth.Auth` 加 `Snapshot()` / `ActivationPending` / `PendingActivation()`，与上游独立修的 `AccessTokenValue()` 家族**目标重叠、实现不同**。这是全流程唯一需要真正设计决策的地方。

**涉及文件**：`patches/0001-auth-pool-consistency.patch`（改）或 `patches/0007-*`（新增覆盖）；`patches/README.md`（同步说明）。

### M3 — `/v1/stats` 倍率 + `usage` 缺失哨兵

**目标**：用户点名的「顺带项」，实为**两件规模差一个数量级的事**，因此拆成 M3a / M3b 分别决策。

#### M3a — 流式 usage 缺失哨兵（小、高价值）

**实测确认本仓快照存在此 bug**：

- `upstream/internal/server/logging.go:41` —— `newChatStat` 以 `toks: -1` 初始化，注释写明「toks 默认 -1（usage 缺失）」；`logging.go:29` 字段注释「<0 表示 usage 缺失 → 显示 "-"」；`logging.go:202` 按 `toks >= 0` 决定显示 `-` 还是数字。
- `upstream/internal/server/handler.go:633` —— 流式分支却写 `st.toks, _ = stats.Tokens()`，**无条件**用零值 `0` 覆盖哨兵，同时丢弃 `hasUsage`。
- 非流式分支（`handler.go:656`）走 `completionTokens(resp)`，缺失显式返回 `-1`，**不受影响**。

结果：两条路径口径不一致，流式把「没观测到 usage」伪装成「测得 0 token」，请求表格日志输出 `tok=0 | 0.0tok/s`。

**注意**：这是**服务端日志/统计口径**的修复，**不是**客户端可见的 `usage` 字段。实测本仓 `upstream/internal/upstream/sse.go:252` 已有「usage 缺失 → null」的正确行为，客户端侧无需改动。这一点与最初的粗略描述不同，以本条为准。

**修法**（对齐 `b1f7bc8`）：只在 `hasUsage` 为真时写入 `st.toks`；同一 `hasUsage` 复用到下方成本账本的 WARN 分支（原先为拿 `hasUsage` 二次调用 `Tokens()`）。

**涉及文件**：`internal/server/handler.go`（1 行改 4 行）+ `internal/server/logging_test.go`（新增夹具与测试）。

#### M3b — `/v1/stats` 端点 + 积分倍率（大，建议本轮不做）

**修正前一轮的口径错误**：实测本仓快照 `handler.go` 只注册了 **4 条路由**（`/v1/chat/completions`、`/v1/models`、`/status`、`/healthz`），且 `upstream/internal/server/` 下**没有** `metrics.go`。

因此 M3b **不是**「给已有端点加一列」，而是**从零引入整个统计端点**：

| 上游提交 | 提供 | 规模 |
| --- | --- | --- |
| `733d348` | `/v1/stats` + `/v1/stats/reset`，按模型聚合 token/缓存/延迟 | 新建 `internal/server/metrics.go`（HEAD 实测 360 行）+ 配套测试 |
| `5009a1f` | 单模型载荷透出积分倍率 `credits`（与 `/v1/models` 同源目录只读缓存合入，冷缓存/未见下发则整体省略） | 改 `metrics.go` + 新增 `metrics_credits_test.go`（173 行） |
| `6925fb2` / `ffeaad1` | `cmd/stats` 终端查看入口 + 倍率列 | CLI 侧，与控制台无关 |

依赖：`5009a1f` 需要 `internal/upstream/global_models.go` 的 `GlobalModelInfosSnapshot` 只读快照（`1dfe750`）作前置——本仓快照的 `global_models.go` 已被补丁 0002 改造过，存在上下文耦合。

**建议**：本轮**只做 M3a**（1 个文件的 4 行改动，直接修掉一个已实测确认的显示口径 bug）；M3b **已裁定不做**（2026-09-22），替代方案与边界登记见交接书 §6.1。理由见 Q5。

**涉及文件**（若将来做）：`patches/0009-stats-endpoint.patch`（新增）——`internal/server/metrics.go`（新增文件）、`handler.go`（注册 2 条路由）、`internal/upstream/global_models.go`（快照读取口）。

### M4 — （关联）`MaxBodyMB` 退役字段处置

**不是用户点名的三项之一**，但 M1/M2 一旦碰到 `extension.go` 就会连带。当前状态：`extensions/cmd/server/extension.go:294` 引用 `cfg.Server.MaxBodyMB`，而上游已退役该字段为 `Server struct{}`。

**本方案不改上游快照**，所以 M4 在本轮**不触发**（快照没变，字段还在）。仅在本文件登记为「整包更新时会被引爆的地雷」，避免后续会话重复踩。

## 验收条件

每项都要**红 → 绿**实证，不接受「应该可以」。

| 编号 | 验收条件 | 验证方式 |
| --- | --- | --- |
| M1-a | 短窗内 2 个不同 UID 命中 WAF 403 → 状态机激活 | 状态机单测：`noteWaf("u1")=false`、`noteWaf("u2")=true` |
| M1-b | 单号反复命中**永不**触发（账号级偶发不误判） | 同一 UID 调用 N 次全部 `false` |
| M1-c | 窗口过期自然解除；激活期内不续期 | 推进时间断言 `active()` 翻转 |
| M1-d | 激活期轮转终止：上游调用数 = 2（非 3） | 端到端 chat 测试 + 调用计数器 |
| M1-e | 并发安全（`noteWaf`/`active` 混发不 panic） | 并发压力测试 + `-race` |
| M1-f | 账号级软冷却**不受影响**（协同不叠加） | `applyErrorPolicy` 现有测试全绿 |
| M2-a | 竞争修复后 `go test -race` 无 DATA RACE | `go test -race ./internal/auth ./internal/upstream` |
| M2-b | 出站头取值为锁内快照（不再直读字段） | grep 断言无锁外直读 + race 测试 |
| M3a-a | 流式末帧无 usage 时日志输出 `tok=-`，且**不含** `tok=0` | `TestChatLogsStreamRowNoUsageShowsDash`（新增 `sseNoUsage` 夹具） |
| M3a-b | 流式末帧**有** usage 时仍正确输出 token 数（不回归） | 现有 `TestChatLogsStreamRow` 全绿 |
| M3a-c | 非流式路径口径不变（`completionTokens` 已返回 -1） | 现有非流式日志测试全绿 |
| M3b | `/v1/stats` 含倍率列；缺倍率显示 `-`，与 `x0.00` 区分 | 表格渲染测试（**本轮不做，仅登记**） |
| M4 | 本轮不触发（快照未变） | 无需验证；登记备查 |
| ALL-1 | 补丁在物化目录 `git apply --check` 通过 | `python3 scripts/overlay.py prepare --output <新目录>` |
| ALL-2 | 物化后 Go 测试 + vet 通过 | `go test ./... && go vet ./...` |
| ALL-3 | 现有 console / Node 测试不回归 | `go -C console test ./...`、`node --test console/web_test.cjs` |
| ALL-4 | Compose 配置合法 | `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` |

## 影响分析

### 受影响模块

| 模块 | 影响 | 风险 |
| --- | --- | --- |
| `patches/` | 新增 2–3 个补丁；0001 可能改造 | 🔴 高——补丁顺序与既有 5 个补丁的上下文耦合 |
| `upstream/` | **不改**（只读快照） | 🟢 无 |
| `extensions/` | M2 可能调整调用点 | 🟡 中 |
| `console/` | M3a 修复后日志口径变化（`tok=-`） | 🟢 低 |
| `deploy/` | 无 | 🟢 无 |

### 风险点

1. **补丁上下文脆弱**：现有 5 个补丁已因 `gofmt` 重排与语义重叠而全部冲突。新增补丁 0006 必须基于**本仓快照 `c576b48` 的实际文本**写，不能照抄上游 HEAD 的 diff（行号与上下文都不同）。
2. **M2 是设计分叉不是机械替换**：选上游路线要重写补丁 0001 的 `Snapshot()` 语义，需重新跑 0001 声明的全部竞态测试。
3. **验证环境受限（2026-09-22 复核并更正归因）**：原文写「本机无 gcc → `-race` 只能进 Linux 容器」，归因不准——本机**连 Go 工具链都没有**（`which go` 空，常见安装目录全无），`gcc` 只是容器侧的附带条件；`scripts/check.sh` 依赖 `python3`，但真实门槛不是 Store 占位符，而是 **Windows 平台语义**（无执行位、建 symlink 需特权、POSIX 模式不成立），用真 `python` 3.12.10 同样 3 failures + 21 errors（§12.1）。解除方式已实测：启动 Docker Desktop 引擎 + 容器内 `git clone` 后验证（§12.3）。已知环境性失败**不是回归**，但要在验证记录里标注。
4. **`applyErrorPolicy` 签名差异**：若 M1 选补丁路线，要么同时改签名（波及更大），要么在补丁里做最小插入而不动签名。

## 验证策略

> ⚠️ **2026-09-22 复核**：本节命令（含 `python3 scripts/overlay.py prepare` 与容器 `-race` 配方）在本机 Windows 均不可执行，且根目录不是 Go 模块、`go test -race ./...` 在 `-w /src` 也会失败。执行入口以交接书 **§12.3** 为准，本机可跑的部分仅剩 `docker compose config --quiet` 与 `git diff --check`。

**实测依据**（2026-09-22）：

- 容器内把仓库根当模块 → `pattern ./...: directory prefix . does not contain main module or its selected dependencies`。⇒ 下面第 3 步的 `-w /src` 类配方作废，只能对**物化目录**执行。
- 本机 `python3 scripts/overlay.py prepare --output <新目录>` → `upstream source digest does not match upstream.lock`（§12.1：Windows 无执行位/symlink 语义）。⇒ 下面第 1、2 步作废。
- 本机 `go` 不存在（`which go` 空；`C:\Program Files\Go`、`C:\Go`、`%USERPROFILE%\go\bin` 均无）⇒ 第 3、4 步的 `go -C ...` 与 `-race` 作废。
- 本机 `node --test console/web_test.cjs` → **30 tests / 30 pass / 0 fail** ⇒ 第 4 步的 Node 判据在本机真实有效，保留。
- 本机 `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` → exit 0；`git diff --check` → exit 0 ⇒ 第 5、6 步保留。

### 本机仍可跑的部分（其余全部走交接书 §12.3 容器入口）

```bash
docker compose --env-file /dev/null -f docker-compose.yml config --quiet   # exit 0
git diff --check                                                            # exit 0
node --test console/web_test.cjs                                            # 30/30 pass
```

容器内负责：`prepare` + `go test ./...` + `go vet ./...` + `-race` + `overlay.py` 的 36 项单测 + `test_migrate.py`。HEAD `71fa504` 的基线结果（20 个包全 ok / vet 干净 / 5 个关键包 `-race` 无 DATA RACE / console `-race` ok）列在交接书 §12.3 末尾。

### 本地（不依赖容器）——**2026-09-22 作废，保留原文以便追溯**

以下 6 步中只有第 5、6 步在本机可跑（第 4 步的 `node --test` 也可跑，见上一节）；其余步骤的原因见 §12.1–§12.2，执行入口为交接书 §12.3。另外第 6 步的 `wafip.go` 路径已按裁决变更为 `extensions/internal/server/wafip.go`（§12.4 #2）。

```bash
# 1. 物化到全新目录（旧物化目录不会自动同步）
python3 scripts/overlay.py prepare --output .build/core-backport

# 2. 补丁逐个可应用
python3 scripts/overlay.py prepare --output .build/core-backport-2

# 3. Go 测试 + vet
go -C .build/core-backport test ./...
go -C .build/core-backport vet ./...

# 4. console 与页面不回归
go -C console test ./...
node --test console/web_test.cjs

# 5. Compose 合法性
docker compose --env-file /dev/null -f docker-compose.yml config --quiet

# 6. 行尾一致性（本机 CRLF 工作副本）
git diff --check && git check-attr -a upstream/internal/server/wafip.go
```

### 竞态（在 Linux 容器内跑，配方见交接书 §12.3）

> **原配方作废**（两条都跑不通）：`-v "$(pwd -W):/src:ro"` 直挂 Windows 目录 → `source_digest` 仍不匹配（§12.1）且只读挂载写不出 `.build`；`go test -race ./...` 落在 `-w /src` → 仓库根不是 Go 模块，直接 `go.mod file not found`。
>
> **有效入口**：容器内 `git clone /host /work/repo` 后再 `prepare`，对物化目录执行 `-race`。完整 stage1–7 脚本与 HEAD `71fa504` 的基线结果见交接书 §12.3；本次实测 5 个关键包（auth/pool/upstream/server/scheduler）+ console 全 ok、无 DATA RACE。
>
> 镜像源配方来自 `2026-09-22-security-review-tracking.md` 第 6 节实测（`registry-1.docker.io` 本机超时，`docker.m.daocloud.io` 可拉）。

### 不做的验证（不得声称完成）

- `scripts/acceptance.sh`（涉及运行配置时才需要）
- 真实上游调用
- 镜像发布与部署

## 待确认问题

### Q1 [需人工确认] M1 的实现路线

实测结论：`wafip.go` **不能**作为纯 `extensions/` 新增实现，因为它必须同时（a）让 `upstream.ErrKind` 多出 `ErrWafBlock`（改上游既有文件），（b）在 `chatCompletions` 轮转循环内部插入 `break`（改上游既有文件）。两条都必然走 `patches/`。

- **候选 A（推荐）**：新增 `patches/0006-waf-ip-failfast.patch`，**只回移 IP 级熔断的最小集**
  （`ErrWafBlock` + `IsWafBlocked` + `wafip.go` + 轮转 `break`），**不带** `34405ca` 的账号级 WAF 软冷却/退避，也不夹带 `/v1/stats`、admin 端点等无关改动。
  详见上文 §M1 修正注记与交接工单 §4.4。
- **候选 B**：在 `extensions/` 内做等价状态机 + 用 `extensions/` 包住公共 Handler 实现 fail-fast。代价：仍要改 `ErrWafBlock`（否则无法从 `Classify` 拿到 WAF 信号），且包一层会与 `extensions/internal/bridge` 的既有包装顺序冲突。

### Q2 [需人工确认] M2 的路线

- **候选 A（推荐）**：改用上游路线 `AccessTokenValue()`/`DomainValue()`/`RefreshTokenValue()`，替换补丁 0001 的 `Snapshot()` 实现。理由：上游已配套修好 `headers.go` 调用点，长期维护成本低。
- **候选 B**：保留本仓 `Snapshot()`，只补 `headers.go` 的锁外直读点。理由：改动面小。代价：与上游分叉永久化。

### Q3 [需人工确认] 是否接受「不整包更新」这个前提

用户已明确只做 1/2/3 三点，本方案据此不碰 `upstream.lock`。若你其实希望顺带把快照升到原作者某个较近的 commit（从而让 M1/M2/M3 直接变成「摘上游现成代码」而不是「回移」），那是另一条路线，返工量更大但一次到位。**默认按「不更新快照」执行。**

### Q4 [需人工确认] 执行节奏

- 连续推进（默认，每任务完成自动进下一个）
- 逐任务暂停确认（适合易跑偏的改动）

### Q5 [已裁定 2026-09-22] M3b（`/v1/stats` 端点）本轮是否做

**结论：选候选 A，本轮不做 M3b。** 替代方案与「`/status` 不含 usage 统计」的边界登记在交接书 §6.1，最终由 pi 验收定论。

实测它比最初描述大一个数量级（新建 360 行的 `metrics.go` + 173 行测试 + 两条路由 + 依赖 `global_models.go` 快照，而该文件已被补丁 0002 改造过）。

- **候选 A（推荐）**：本轮只做 M3a（哨兵修复，4 行改动、直修一个已实测 bug），M3b 登记待评估。
- **候选 B**：M3a + M3b 一起做。代价：补丁数量 +1，且 `metrics.go` 会与补丁 0002 在 `global_models.go` 上上下文耦合，实测冲突风险升高。

---

## 工作规范（本方案执行期内严格生效）

1. 每个修改点实现前，先在进度文件写「中断保护区」，完成后清理。
2. 先写失败测试（红），确认确实失败，再写最小实现（绿）。
3. 每步都报告：改了哪些文件、变更摘要、验证命令与实际输出。**不接受「命令返回 0 就算完成」**。
4. 不顺手重构。发现方案外的问题 → 记入进度文件备查，不修复。
5. 遇不确定 → 停下列出候选方案，问用户，不自行裁决。
6. 所有验证结论必须来自**实际执行过的命令输出**；环境性失败要标注为环境性，不得混入回归统计。
7. 本地提交、不推送；不发布镜像、不部署。
