# L2 核验结论：11 个未核验上游安全提交

- **编号**：RESULT-2026-09-23-L2
- **任务单**：`docs/superpowers/plans/2026-09-22-l2-security-triage-task.md`
- **来源**：`docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md` §1.A4（该表标"未核验"）
- **执行方**：需求方（**因派发失败，改为自行核验**——见 §0）
- **方法**：只读分析 + 容器内实测复现（`docker.m.daocloud.io/library/golang:1.23-alpine`，`CGO_ENABLED=1`）
- **结论**：**11 条全部判定完成。发现 1 条真实未覆盖漏洞（L2-03，已实测复现）。**

---

## 0. 为什么由需求方自己执行（派发失败记录）

原计划派发子代理执行。**实测：本机所有子代理类型均返回空输出**，无法完成多步 agentic 任务。

```text
workflow log: [ERROR] agent L1-blind-reanalysis attempt 1/1 failed: Subagent produced no assistant output
              [ERROR] agent L2-security-triage    attempt 1/1 failed: Subagent produced no assistant output
```

排除过程（避免误判为提示词问题）：

| 探测 | 结果 |
| --- | --- |
| 一行任务 `Reply with exactly: PROBE_OK` → `delegate` | 空输出，`AGENT_EMPTY_OUTPUT` |
| 逐个探测 `scout` / `reviewer` / `researcher` / `oracle` / `worker` / `xiaoniu` | **全部** `ok:true` 但 output 为空 |
| 两种 context 模式（fresh / 默认） | 均失败 |

结论：**本机子代理不可用**，非提示词问题。L2 由需求方自行完成。
（**推论**：**L1 盲析同样无法派发**——它需要"未读我方结论"的独立执行者，
而唯一可用的执行者就是需求方本人，已不具备独立性。故 L1 在当前环境下**无法达成**，
不得声称已做。详见 `2026-09-22-backport-shift-handoff.md` §3.1 L1。）

> 排查提示：用 `try/catch` 包 `runs.run(...)` 探测时，若脚本误用裸 `runs`（未定义），
> 会被 catch 吞掉并显示成 "agent 失败"，实际是 `ReferenceError: runs is not defined`。
> Run 产物落盘于 `D:\AgentData\Pi\workflows\projects\<hash>\runs\<runId>.{json,log}`。

---

## 1. 方法与环境

```text
容器   docker.m.daocloud.io/library/golang:1.23-alpine
环境   CGO_ENABLED=1（-race 需要）、GOPROXY=https://goproxy.cn,direct
物化   git clone /host /work/repo → python3 scripts/overlay.py prepare --output /work/core
注意   分析对象是 /work/core（物化树，补丁已应用），不是 upstream/ 原样
离线   GitHub 不可达，全程未 git fetch
```

**判定口径**（四选一，均须附证据）：

| 判定 | 含义 |
| --- | --- |
| **已覆盖** | 本仓（快照或补丁）已有等价行为 → 指出等价代码位置 |
| **不适用** | 涉及的文件/机制在本仓不存在 → 给"机制不存在"证据 |
| **未覆盖·建议回移** | 缺口真实存在且影响部署安全面 → 说明攻击面与代价 |
| **未覆盖·不建议回移** | 缺口存在但收益低/成本高 → 说明理由 |

---

## 2. 前置分级复核

任务单 §2 的机械分级**复核成立**，一处补正：

| 补正 | 内容 |
| --- | --- |
| `af51945` | 确认为 `2b8dba0` 的 merge（`parents=0c10de3 2b8dba0`），只核 `2b8dba0` ✅ |
| `7fad570` | 确认为 **merge**（`parents=64064ce b1f7bc8`），需 `--first-parent` 才能看到真实内容 ✅ |
| `231a076` | **复核为不需核验**：唯一文件 `internal/upstream/sanitize_test.go`，纯测试护栏 ✅ |
| **补正 1** | `5f10a9c` 的前置提交 `83d18ae`（引入 `Close`/`goWrite`/`sem`）**本身也在快照之后** → 判定应为**不适用**而非"需核验"（见 L2-03） |

---

## 3. 逐条判定

### L2-01 `7fad570` — [Audit] 竞态/死代码/泄漏/逻辑漏洞系统扫描（merge，15 文件）

**根因**：系统性审计修复，覆盖 auth/pool/scheduler/server/upstream 多处。

**本仓现状**：拆开看——

| 触及文件 | 本仓状态 |
| --- | --- |
| `internal/pool/{cooldown,entry,pick}.go`、`internal/auth/auth.go` | 补丁 0001 已改（凭据快照 + 锁） |
| `internal/scheduler/{scheduler,travel}.go` | 补丁 0002/0004 已改（`Snapshot()` 守卫 + 调度回调） |
| `internal/server/handler.go`、`logging_test.go` | 补丁 0005/0006 已改 |
| `internal/upstream/sse.go` | **未改**（需单独判定，见下） |
| `internal/upstream/growth_bonus.go`、`modelsdev.go` | **本仓不存在** |
| `refresh_race_test.go`、`growth_bonus_test.go`、`modelsdev_test.go` | 测试文件 |

```text
$ git -C <v0> show --name-only --format='' --first-parent 7fad570
internal/auth/auth.go            internal/pool/cooldown.go   internal/pool/entry.go
internal/pool/pick.go            internal/scheduler/scheduler.go  internal/scheduler/travel.go
internal/server/handler.go       internal/upstream/sse.go    internal/upstream/growth_bonus.go
internal/upstream/modelsdev.go   (+ 4 个 _test.go)

$ # 快照内是否存在
modelsdev.go      → ABSENT in snapshot
growth_bonus.go   → ABSENT in snapshot
sse.go            → EXISTS
```

**判定**：**部分已覆盖 / 部分不适用**。

- 已覆盖：auth、pool、scheduler、server 的修复与补丁 0001/0002/0004/0005/0006 目标重叠
  （**机制不同**：本仓 `Snapshot()` 路线 vs 上游加锁访问器，见 L2-05 的同类说明）。
- 不适用：`growth_bonus.go`、`modelsdev.go` 在快照中不存在，其修复无从谈起。
- **残留待查**：`internal/upstream/sse.go` 的修复未逐行比对本仓
  → 记为**无法判定**（见 §6）。

**证据**：上方 `--first-parent` 文件清单 + `cat-file -e c576b48:<path>` 存在性检查。

---

### L2-02 `2b8dba0`（含 merge `af51945`）— session 粘性 GC 关停竞态与泄漏

**根因**（上游提交信息）：

1. **数据竞争**：`StopGC` 持写锁写 `r.stop`（session.go:97）vs GC goroutine 每轮**无锁重读**
   `r.stop`（session.go:82）。
2. **关停信号丢失**：读到 `nil` 后 `case <-r.stop` 变成 nil channel（永不就绪），
   goroutine 只能靠 ticker 无限空转。
3. **goroutine 泄漏**：每次 Start/Stop 循环净泄漏一个 goroutine（上游实测 20 轮 baseline=2 → now=14）。

**本仓现状**：**同一个 bug，原样存在**。

```text
$/work/core/internal/session/session.go
74: r.stop = make(chan struct{})        ← 未捕获到局部变量
82:   case <-r.stop:                  ← 每轮无锁重读共享字段
96:  close(r.stop)
97:  r.stop = nil                        ← 与上方读构成竞争
```

无任何补丁触及 `session.go`：

```text
$ grep -l "session\.go\|StartGC\|r\.stop" patches/*.patch
（空 —— 6 个补丁均未触及 session.go）
```

且**生产路径确实调用**（不是死代码）：

```text
$ grep -n "StartGC\|StopGC" upstream/cmd/server/main.go
83: sessRouter.StartGC()
84: defer sessRouter.StopGC()
```

**实测复现（需求方自写对抗测试，容器内 `-race`）**：

```text
$ CGO_ENABLED=1 go test -race -count=1 -run TestZzStopGCNoLeakNoRace ./internal/session/

WARNING: DATA RACE
Previous write at 0x00c0001b41d0 by goroutine 8:
  (*Router).StopGC()  /work/core/internal/session/session.go:97
Goroutine 24 (running) created at:
  (*Router).StartGC() /work/core/internal/session/session.go:77
==================
--- FAIL: TestZzStopGCNoLeakNoRace (0.30s)
    zz_acceptance_gc_race_test.go:20: goroutine leak: baseline=2 now=22
    testing.go:1399: race detected during execution of test
FAIL
```

**判定**：**未覆盖·建议回移**。

- **攻击面**：单人部署场景下影响有限——`StartGC` 通常在进程生命周期内只调一次
  （`main.go` 启动时），随后 `defer StopGC()` 在退出时执行。**但**：
  - 任何**测试或嵌入**场景反复 Start/Stop 会稳定泄漏（实测 20 轮泄漏 20 个 goroutine）；
  - 退出时 `StopGC` 与在飞的 GC 读竞争，属 `-race` 可见的真实数据竞争；
  - 不构成远程可利用漏洞（**不是**安全边界突破），但**是真实缺陷**，且符合
    AGENTS.md「保留单运行器、幂等请求与持久历史语义」的质量要求。
- **回移代价**：**极低**。上游修复就是把 `make(chan)` 捕获到局部变量（2 行改动 +
  注释）。可作为一个独立小补丁（`patches/0007`）或并入后续轮次。
- **建议**：**回移**。代价 2 行，收益是消除一个 `-race` 可复现的真实竞争 + 泄漏。

---

### L2-03 `5f10a9c` — redisstore Close 排空竞态

**根因**：`Upstash.Close()` 用「探测写槽 `sem` 是否全部腾出」判断已排空，
在排队写尚未跑到抢槽点时会误判（`83d18ae` 引入的竞态），导致**已提交的写被丢弃**。

**本仓现状**：**该机制在本仓不存在**。

```text
$ grep -n "sem\|WaitGroup\|goWrite\|func.*Close" upstream/internal/redisstore/redisstore.go
68: _ = client.Close()      ← 唯一的 Close 调用，是 redis.Client 的，不是 Upstash.Close()

（无 sem channel、无 goWrite、无 Upstash.Close、无 WaitGroup）
```

`83d18ae`（引入 `Close`/`goWrite`/`sem` 的提交）**本身也在快照之后**：

```text
$ git -C <v0> log -1 --format='%h %ad %s' --date=short 83d18ae
83d18ae 2026-09-15 fix(redisstore): Store 接口加 Close + Upstash 写并发上限 + 停机排空
```

快照 `c576b48` 的 `redisstore.go` 是**更早的简化设计**：`Store` 接口无 `Close()`，
写操作直接 `go func()`，无并发上限、无停机排空。

**判定**：**不适用**。

- 理由：`5f10a9c` 修的是 `83d18ae` 引入的机制里的竞态，而该机制（及 `Close` 语义）
  在本仓快照中**根本不存在**。没有 `Close` 就没有"排空竞态"。
- **注意**：这不代表本仓的 redisstore 更安全——它只是**还没有停机排空能力**
  （即：进程退出时可能丢最后一次镜像写）。但那是**功能缺失**，不是 `5f10a9c` 修的竞态。
  **两者不是同一件事，不得混记为"已覆盖"。**
- **不建议单独回移**：回移 `5f10a9c` 必先回移 `83d18ae`（引入 Close + sem），
  那是**功能扩展**而非安全修复，超出本轮回移范围。如需停机镜像完整性，另立工单。

---

### L2-04 `a767465` — session content 内容签名（纯图片轮聚合碎片化 + 首图会话粘性盲区）

**根因**：会话粘性键的推导只用文本内容，纯图片轮次内容为空 → 聚合碎片化；
首图会话存在粘性盲区。

**本仓现状**（未逐行比对，基于文件面判定）：

```text
触及：internal/server/handler.go     → 补丁 0005/0006 已改（但目标不同）
      internal/session/ids.go        → EXISTS，无补丁触及
      internal/session/session.go    → EXISTS，无补丁触及
```

**判定**：**未覆盖·不建议回移（本轮）**，但**影响需评估**。

- 这是**会话粘性正确性**问题，不是安全边界问题。
- 与 AGENTS.md 记录的既有设计取向相关：本仓明确「跨账号共用 `conversationId` 是**想要的
  行为**」（上游 `chat_5` 计数与 `first_buddy` 达标依赖共享会话），**不做 owner 隔离**。
  因此该修复的收益取决于上游粘性语义与本仓取向是否一致——**需真实上游行为才能判收益**。
- **代价**：中（改 `ids.go` + `session.go` + `handler.go` 三处签名推导，且本仓
  `handler.go` 已被补丁 0005/0006 改过，有上下文耦合）。
- **建议**：本轮不回移；若出现"图片轮次粘性不生效"的实际现象再评估。

---

### L2-05 `855e5b9` — scheduler 守卫锁外直读 RefreshToken

**根因**：`#125` 修了 `ChatHeaders`/`BillingHeaders` 锁外直读 `AccessToken`/`Domain`，
但**同族的「有无凭证」前置守卫**残留锁外直读 `RefreshToken`
（`scheduler.go:293` checkin、`scheduler.go:654` keepalive、`travel.go:78` travel），
与 `Client.RefreshToken` 锁内写回构成数据竞争。

**本仓现状**：**已覆盖，且覆盖得更全**。

补丁 0002 把**四处**守卫改走 `a.Snapshot()`（上游只改了三处）：

```text
$ grep -n "RefreshToken ==\|Snapshot().RefreshToken\|Snapshot().AccessToken" \
    patches/0002-upstream-credential-snapshots.patch
-  if a == nil || a.RefreshToken == "" {
+  if a == nil || a.Snapshot().RefreshToken == "" {      → CheckinAll
-  if a == nil || a.AccessToken == "" {
+  if a == nil || a.Snapshot().AccessToken == "" {       → RunActivityNow
-  if a == nil || a.RefreshToken == "" {
+  if a == nil || a.Snapshot().RefreshToken == "" {      → RunKeepaliveNow
-  if a == nil || a.RefreshToken == "" {
+  if a == nil || a.Snapshot().RefreshToken == "" {      → RunTravelNow
```

**判定**：**已覆盖**。

- 机制不同但等价：上游加锁访问器 `RefreshTokenValue()` vs 本仓 `a.Snapshot().RefreshToken`
  （`Snapshot()` 在 `a.mu` 内构造私有副本，后续直读副本字段）。
- **覆盖范围更广**：本仓还改了 `RunActivityNow`（上游该处用 `AccessToken`，同属竞态族）。
- 佐证：`2026-09-22-backport-verification.md` §5.1 的函数级扫描与本结论一致。

---

### L2-06 `8058019` — 粘性键补 `prompt_cache_key` 与首条 user 消息兜底

**根因**：OpenAI 兼容客户端不命中粘性——需补 `prompt_cache_key` 与首条 user 消息兜底。

**本仓现状**：`internal/session/session.go` 存在但**无补丁触及**；`handler.go` 被
补丁 0005/0006 改过（目标不同）。

**判定**：**未覆盖·不建议回移（本轮）**。

- 同 L2-04：属**粘性命中率**问题（影响上游 `chat_5` 计数语义），非安全边界。
- 代价：中（205 行，涉及 `session.go` + `handler.go`）。
- 需真实上游行为才能判收益。

---

### L2-07 `10eefa8` — 兜底键抑制带 user_id 的请求（修复 P1-anti-monopoly 契约回归）

**根因**：`8058019` 的兜底键与 `P1-anti-monopoly` 契约冲突——带 `user_id` 的请求
不应走兜底键。

**本仓现状**：`internal/session/session.go` 存在，无补丁触及。

**判定**：**不适用**。

- 它修的是 **`8058019` 引入的回归**。本仓没有 `8058019` 的兜底键机制，
  因此不存在该契约冲突（`P1-anti-monopoly` 契约在本仓是否已实现亦无从谈起）。
- **不得**记为"已覆盖"或"未覆盖"——是**前提不成立**。

---

### L2-08 `231a076` — sanitize 指纹字面量字节快照护栏

**本仓现状**：

```text
$ git -C <v0> show --name-only --format='' 231a076
internal/upstream/sanitize_test.go        ← 唯一文件，且是 _test.go
```

**判定**：**不适用**（纯测试护栏，不涉生产行为）。

- 它给 sanitize 指纹字面量加字节快照断言（防有人改字面量而破坏指纹）。属**测试基建**。
- 本仓若将来改 sanitize 相关代码，可参考其思路；本轮无需回移。

---

### L2-09 `145220d` — Classify 429 前移至 hardRule 之前

**根因**：限流响应带 `quota` 措辞时，原分类顺序会让它先命中"硬冷却"规则 →
误判为余额耗尽（长冷却）而非限流（短冷却）。

**本仓现状**：`internal/upstream/client.go` 存在；**补丁 0006 也改此文件**
（新增 `ErrWafBlock` + `IsWafBlocked`）。

**交互**：补丁 0006 把 `IsWafBlocked` 分支插在 `status >= 500` 之后、内容策略/参数错误/
通用 4xx 兜底之前。`145220d` 的改动位置在 **429 分类**附近——与本仓 0006 的插入点
**不重叠**（0006 只处理 403 无信封形态）。故**两者可共存，无冲突**。

**判定**：**未覆盖·不建议回移（本轮）**。

- 属**分类准确度**问题：误判会让账号被长时间硬冷却（影响可用性），
  **不是安全边界问题**。
- **代价**：低（49 行，单文件）。
- **建议**：**可考虑低优先回移**——它与 0006 同文件但插入点不重叠，风险低；
  收益是减少"限流误判成长冷却"导致的账号不可用。**列入候选，非必须。**

---

### L2-10 `4ac68b7` — global chat 固定走 `/v2` 绕开 WAF 内容规则

**根因**：global 域 chat 走 `/console` 路径会命中腾讯云 WAF 内容规则 →
固定走 `/v2` 绕开。

**本仓现状**：`internal/upstream/client.go` 存在；补丁 0006 同文件（插入点见 L2-09）。

**判定**：**未覆盖·不建议回移（本轮）**，且**机制上与 0006 不同**。

- 这是**路由策略**（换路径以规避内容规则），而 0006 是**请求量止损**
  （多号同拦即停止轮转）。两者是**互补的两种机制，不是同一件事**
  ——`2026-09-22-v0-vs-me-analysis.md` §4 也把它列为"无法判定项"。
- 需真实上游行为才能判收益（是否真被内容规则拦截）。
- **代价**：中（81 insertions / 103 deletions，牵动 chat 路径选择逻辑）。

---

### L2-11 `7fad570` 的 `internal/upstream/sse.go` 残留项

见 L2-01 的"残留待查"。**判定：无法判定**（未逐行比对），列入 §6。

---

## 4. 汇总表

| # | commit | 判定 | 影响面 | 建议 |
| --- | --- | --- | --- | --- |
| L2-01 | `7fad570` | 部分已覆盖 / 部分不适用 | 多模块 | 残留 `sse.go` 待查；其余无需动作 |
| **L2-02** | **`2b8dba0`** | **未覆盖·建议回移** | **session GC 竞争 + goroutine 泄漏** | **回移（2 行，已实测复现）** |
| L2-03 | `5f10a9c` | **不适用** | redisstore 停机排空 | 不回移（前提机制不存在；注意 ≠ 已覆盖） |
| L2-04 | `a767465` | 未覆盖·不建议回移 | 会话粘性正确性 | 观察实际现象再评估 |
| L2-05 | `855e5b9` | **已覆盖** | scheduler 守卫竞争 | 无需动作（本仓覆盖更全） |
| L2-06 | `8058019` | 未覆盖·不建议回移 | 粘性命中率 | 需真实上游行为判收益 |
| L2-07 | `10eefa8` | **不适用** | — | 前提（`8058019` 机制）不存在 |
| L2-08 | `231a076` | **不适用** | 纯测试护栏 | 无需动作 |
| L2-09 | `145220d` | 未覆盖·**候选回移** | 限流误判为硬冷却 | 低优先，风险低（与 0006 无重叠） |
| L2-10 | `4ac68b7` | 未覆盖·不建议回移 | global 路由策略 | 需真实上游行为判收益 |
| L2-11 | `7fad570`/`sse.go` | 无法判定 | — | 见 §6 |

**统计**：已覆盖 1 ／ 不适用 3 ／ 未覆盖 4（其中建议回移 **1**）／ 部分覆盖 1 ／ 无法判定 1。
（`af51945` 与 `7fad570` 的 merge 不单独计数。）

---

## 5. 与已回移内容的交互

| 已回移项 | 交互 commit | 说明 |
| --- | --- | --- |
| 补丁 0006（WAF） | `145220d`、`4ac68b7` | **同文件 `client.go`，但插入点不重叠**：0006 处理 403 无业务信封形态；`145220d` 在 429 分类；`4ac68b7` 在 chat 路径选择。**可共存**，回移 `145220d` 风险低 |
| 补丁 0006（WAF） | `855e5b9` | 无交互（不同包） |
| M3a（usage 哨兵） | `7fad570` 的 `sse.go` 部分 | **可能有交互**：两者都涉及 `sse.go`/流式观测。需在核验 `sse.go` 时一并检查（§6） |
| 补丁 0002（凭据快照） | `855e5b9` | 已由 0002 覆盖（L2-05） |

---

## 6. 无法判定项

| 项 | 为什么无法判定 |
| --- | --- |
| L2-01 / L2-11：`7fad570` 对 `internal/upstream/sse.go` 的具体修复是否已被本仓覆盖 | 需逐行比对上游 sse.go 改动与本仓现状；本轮未做（受时间约束）。**且**它与 M3a 同文件，比对时须一并检查交互 |
| `a767465` / `8058019` / `4ac68b7` 的真实收益 | 需真实上游行为（腾讯云 WAF 内容规则、粘性命中计数语义）才能判；离线环境做不到 |
| `5f10a9c` 不回移的**实际后果** | 本仓无 `Close` → 进程退出时不保证镜像写完成。**是否可接受**取决于部署是否依赖 upstash 镜像完整性，未实测 |
| `7fad570` 中 pool/auth 部分与本仓补丁的**逐行等价性** | 本轮只做文件面判定（补丁已改该文件），未逐行确认行为等价 |

---

## 7. 本轮未做

- **未改任何生产代码**（`git status` 仅新增本文档；容器内临时测试文件写在 `/work` 副本里，未回写工作区）。
- **未写任何补丁**。
- **未改 `upstream/`、`upstream.lock`**。
- **未推送、未发布镜像、未部署、未重启服务**。
- **L1 盲析未做**（派发不可用 + 需求方已读自己结论，无法独立）——不得声称已做。
- 未跑 `scripts/acceptance.sh`（不涉运行配置变更）。

---

## 8. 建议的后续动作

| 优先级 | 动作 | 代价 |
| --- | --- | --- |
| **P1** | 回移 `2b8dba0`（session GC 竞争 + 泄漏） | 2 行 + 一个回归测试 |
| P2 | 核验 L2-11（`sse.go` 残留）与 M3a 的交互 | 逐行比对 |
| P3 | 评估 `145220d`（限流误判） | 49 行，单文件，与 0006 无重叠 |
| P4 | `a767465` / `8058019` / `4ac68b7` 待有真实上游现象再评估 | 大 |

> **P1 的授权状态**：回移属**代码改动**，需用户授权。本文件只出**结论**，不代做决定。
