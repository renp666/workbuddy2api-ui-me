# 交接说明书：上游安全修复回移（Qoder 实施）

- **文档编号**：HANDOFF-2026-09-22-BACKPORT
- **编写者**：需求方（本仓维护者侧）
- **实施方**：Qoder
- **验收方**：需求方（AI 助手，独立复跑判据）
- **关联文档**：
  - 需求来源（只读分析结论）：`docs/superpowers/verification/2026-09-22-security-review-tracking.md`
  - 方案设计（本文件的上游）：`docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md`
  - 进度文件（实施方必须维护）：`docs/superpowers/plans/2026-09-22-backport-progress.md`

> ⚠️ **给 T0 的隔离提醒**：做 §3.5 的盲析阶段时，请**先不要读**上面两份「关联文档」，也不要读本文 §3 / §4.4 / §5 / §附录 A。等你自己得出结论后，再回来对照。

---

### 实施方须知（先分清两个不同的任务性质，否则会自相矛盾）

- **T0（独立复析）——必须要"重新调研"。** 你的任务是**自己从头分析 v0 与 me 的差异与可更新性，先得自己的结论**，然后再读我的结论做对照。凡我方文档里的事实、行号、判断，**T0 阶段一律只作"待核验的对方主张"看待**，不得当作既定前提直接引用。详见 §3.5。
- **T1 / T2 / T3（实施）——不要重新调研。** 这三个任务的范围已经过需求方实测拍板并有明确验收判据；凡文档已给的事实、行号、锚点、命令，直接采用即可，不要另做设计选择。凡标注 `【决策】` 的，是我作为需求方已拍板，不要另选方案；凡标注 `【陷阱】` 的，是实测踩过的坑，照做即可。
- **两阶段的先后不可颠倒**：T0 的盲析部分必须先完成并落盘，才能开始 T1/T2/T3。否则实现思路会污染你的独立判定（详见 §3.5.2）。

---

## 0. 一页速览

| 项 | 内容 |
| --- | --- |
| 目标 | 先把 v0 与 me 的差异**独立复析一遍**（T0），再按已定范围实施 3 项安全/正确性修复 |
| 交付物 | T0 分析文档 + 1 个新补丁 + 1 处小修 + 进度文件 + 验证记录 |
| 硬约束 | **不更新** `upstream.lock` / `upstream/` 快照；不改既有 5 个补丁的语义 |
| 最大风险 | M1 涉及 `chatCompletions` 轮转循环，改错会导致换号策略失效 |
| 预估改动量 | M1 ≈ 1 新文件 + 2 处插入；M3a ≈ 1 文件 4 行 |
| **M2 无需新增工作** | 见 §3——**已由现有补丁实现**，本工单只要求"验证 + 补测试" |

---

## 1. 项目背景（实施方必读，避免改错层）

本仓是三层血缘的**第三层**：

| 层 | 仓库 | 职责 |
| --- | --- | --- |
| ① 原作者 | `Sliverkiss/workbuddy2api` | 账号池、调度器、上游客户端、WAF/IP 级防护 |
| ② 二创 | `baiyea/workbuddy2api-ui` | 增加 `console/`（Web 控制台）、`patches/`、`extensions/`、容器化 |
| ③ **本仓** | `renp666/workbuddy2api-ui-me` | 只改 `console/`、文档、仓库卫生 |

关键机制：

- `upstream/` 是**历史快照**（`upstream.lock` 锚定 `c576b48`），**只读**。落后原作者 `master` **214 个提交**（实测 `git rev-list --count c576b48..HEAD` = 214）。
- 上游既有文件的**任何**修改都必须走 `patches/`（`git apply` 补丁），不能直接改 `upstream/`。
- 独立新能力放 `extensions/`，**不能**用扩展覆盖已有上游文件。
- 物化流程：`python3 scripts/overlay.py prepare --output <新目录>` 会 校验摘要 → 复制快照 → 复制 `extensions/` → 按 `patches/series` 顺序应用补丁。

### 本地路径（实施机可能不同，按实际调整）

```text
D:\work\02toolsMy\workbuddy2api-ui-me   ← 本仓（改动目标）
D:\work\02toolsMy\workbuddy2api-ui      ← 二创参照副本（只读参考，不要改）
D:\work\02toolsMy\workbuddy2api_v0      ← 原作者副本（只读参考，可取 diff）
```

> **T0 实施方留意**：做 T0 盲析阶段时，**先不要读**本文 §3 / §4.4 / §5 / §附录 A，
> 也不要读 `docs/superpowers/verification/2026-09-22-security-review-tracking.md`。
> 详见 §3.5.2。

---

## 2. 任务总览

| ID | 任务 | 类型 | 规模 | 交付物 |
| --- | --- | --- | --- | --- |
| **T0** | **独立复析：v0 vs me 差异与可更新性**（先做，独立于我的结论） | AFK | 中 | `docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md` |
| **T1** | M1：WAF IP 级 fail-fast 熔断 | AFK（需容器验竞态） | 大 | `patches/0006-waf-ip-failfast.patch` |
| **T2** | M2：auth 数据竞争——**仅验证 + 补回归测试** | AFK | 小 | 新增竞态回归测试（不改生产代码） |
| **T3** | M3a：流式 usage 缺失哨兵 | AFK | 极小 | 补丁内 4 行改动 + 测试 |
| **T4** | 文档同步 | AFK | 小 | `patches/README.md` + 验证记录 |

**执行顺序**：**T0** → T3 → T2 → T1 → T4。

理由：**T0 必须在任何代码改动之前完成**——它是独立第二意见，若在实现后做会被自己的改动污染。T3 最小、最快建立"红→绿"节奏；T1 最大且最后，避免阻塞。

### 关于 T0 的定位（重要）

T0 **不是**重复劳动，是**独立复核**。我的分析是单一分析者结论，存在错判风险（已实证过一次：我曾错误声称 M2 未修，实际补丁 0001/0002 已修）。T0 的价值在两种情况下都成立：

- **结论一致** → 双人独立得到同一事实，置信度提升，后续文档可作为事实引用。
- **结论分歧** → 必须查明是**谁的错**，这是最有价值的产出。

**T0 完成后不要自行调整 T1/T2/T3 的范围**。把分歧写进分析文档的「分歧清单」，交需求方裁定后再动。理由：T1/T2/T3 的范围已经过需求方实测拍板（见 §4.4 / §3 / §5），擅自改动会与验收判据脱钩。

---

## 3. 【决策】M2 的真实状态——不要重复实现

**这是本工单最重要的一条，请先读完再做任何事。**

我实测发现了与最初设想不同的情况：

- 上游 `910b8b2` 的修法是新增 `AccessTokenValue()` / `DomainValue()` 加锁访问器，并把出站路径改为经它们取值。
- **本仓的补丁 0001 已经独立实现了等价修复**，走的是另一条路线：
  - `patches/0001`：给 `Realm()` / `BackfillRealm()` / `RealmStored()` 加 `a.mu.Lock()`；新增 `Auth.Snapshot()`（锁内私有副本）。
  - `patches/0002`：在**所有**出站函数开头插入 `a = a.Snapshot()`，包括：
    `RefreshToken`、`ChatStream`、`FetchModels`、`billingMeterJSON`、`UserResourceDetailed`、`probeGlobalModels`、`ChatHeaders`、`BillingHeaders`、`RefreshHeaders`、`billingJSON`、`CheckinAll`、`RunActivityNow`、`RunKeepaliveNow`、`RunTravelNow`。

结论：**竞争面已经覆盖**。`ChatHeaders` 开头的 `a = a.Snapshot()` 锁一次拿到私有副本，后续所有字段直读都在副本上，等价于上游的加锁访问器。

因此 T2 **不要**把上游 `AccessTokenValue()` 移植进来。那会与 `Snapshot()` 路线重复、造成双重实现。T2 只做两件事：

1. **验证**竞争确已关闭（补一个 `-race` 回归测试，对齐上游 `TestChatHeadersRacesRefreshToken` 的对抗思路）。
2. **查漏**：确认没有出站函数被漏掉 `Snapshot()`。

### 【陷阱】自锁死锁风险（T2 查漏时的重点）

`patches/0001` 给 `Realm()` 加了锁。若某处**已持 `a.mu`** 又调用 `Realm()`，会自锁死锁（`sync.Mutex` 不可重入）。

上游 `910b8b2` 的提交信息明确记录了这一点，并为此拆出无锁内部实现 `realmLocked()`：

> Realm / RealmStored / NeedsRefresh / BackfillRealm 改为持 a.mu，另拆出无锁 realmLocked 供已持锁的内部实现使用（sync.Mutex 不可重入）

**本仓的 `Snapshot()` 是按字段写的（`realm: a.realm`），不调 `Realm()`，所以当前不会自锁。** 但 T2 查漏时必须确认：没有任何**已持锁**的函数调用 `Realm()` / `RealmStored()` / `NeedsRefresh()` / `BackfillRealm()`。

`a.Lock()` 全仓调用点（上游记录，本仓需复验）：仅 `RefreshToken` 内 3 处 + `SaveAtomic` 自身。

---

## 3.5 T0 详细规格：独立复析 v0 vs me 差异与可更新性

### 3.5.1 目标

独立回答两个问题，**不得先读我的结论**（否则会确认偏误）：

1. **v0（原作者 HEAD）与 me（本仓）到底差什么？**
2. **原作者后续更新里，哪些值得更新、哪些不值得？**

### 3.5.2 强制隔离要求【关键】

为避免确认偏误，**先独立取证、后对照**：

1. **第一阶段（盲析）**：先自己跑命令、自己得出结论，写进文档的「独立结论」一节。
   **此时不要读**：本文 §3 / §4.4 / §5 / §附录 A 的结论、以及
   `docs/superpowers/verification/2026-09-22-security-review-tracking.md`。
2. **第二阶段（对照）**：写完自己的结论后，再读上述文档，逐项对照，写「对照结果」一节。
3. 文档里两个阶段的内容必须**分节存放且标明**，不得混写。

> 为什么：如果先读我的结论，你的输出会退化成"复述"，那就失去了第二意见的意义。

### 3.5.3 必须回答的具体问题（逐条给证据）

| # | 问题 | 要求 |
| --- | --- | --- |
| A1 | 三个仓库的 HEAD 与血缘关系各是什么？ | 给 commit hash + 日期 + `git remote -v` |
| A2 | 本仓 `upstream/` 快照锚在哪个 commit？落后原作者多少提交？ | 给 `upstream.lock` 内容 + `git rev-list --count` 实测值 |
| A3 | 本仓相对二创（baiyea）改了什么？ | 给 `git diff --stat` 的文件清单 |
| A4 | 原作者那 N 个提交里，**安全相关**的是哪些？ | 给 commit hash + 标题 + 一句话影响 |
| A5 | 原作者那 N 个提交里，**功能相关且 me 版会感知**的是哪些？ | 同上 |
| A6 | 哪些是**噪音**（与本项目部署无关）？ | 分类说明，给判断理由 |
| A7 | 本仓快照**实际缺失**的文件有哪些？ | 给 `git diff --name-status` 或等价证据 |
| A8 | 把 5 个既有补丁应用到上游 HEAD，能否成功？ | 自建隔离副本实测 `git apply --check`，给逐补丁结果 |
| A9 | 直接整包更新会撞什么硬阻断？ | 给出具体符号/文件/错误 |
| A10 | 结论：**建议更新、部分更新、还是不更新**？ | 给理由 + 代价估计 |

### 3.5.4 取证方法约束（保证可比性）

为了让你的结论可与我的对齐，**必须用可复现的命令取证**，不接受"文档说""应该"：

```bash
git -C D:\work\02toolsMy\workbuddy2api_v0 log -1 --format='%h %ad %s' --date=short
git -C D:\work\02toolsMy\workbuddy2api_v0 rev-list --count c576b48..HEAD
git -C D:\work\02toolsMy\workbuddy2api-ui-me diff --stat upstream/main..HEAD
git -C D:\work\02toolsMy\workbuddy2api_v0 log c576b48..HEAD --format='%h|%ad|%s' --date=short
```

**【陷阱】GitHub 当前不可达**（实测 `Failed to connect to github.com:443`）。三个 clone 本地都在，**全部分析用本地仓库离线完成**，不要尝试 `git fetch`。

**【陷阱】补丁兼容性测试不要污染工作区**。若要试应用补丁，用 `git clone --shared` 或 `git archive` 建**独立副本**（用尚不存在的新路径），不要再往本仓工作区里试。

### 3.5.5 T0 交付物

`docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md`，结构固定：

1. **独立结论**（盲析阶段，A1–A10 逐条 + 证据命令 + 实际输出）
2. **对照结果**（读我方文档后，逐条标「一致 / 分歧 / 我方遗漏 / 你方遗漏」）
3. **分歧清单**（每条写：分歧点、双方依据、你认为谁对、需谁裁定）
4. **无法判定的项**（及原因，不得隐去）

### 3.5.6 T0 验收条件

| 编号 | 条件 |
| --- | --- |
| T0-a | A1–A10 全部有答案，且每条附**实际命令 + 实际输出** |
| T0-b | 独立结论与对照结果**分节存放且标注**，无混写 |
| T0-c | 分歧清单逐条给出「双方依据 + 你的判断」 |
| T0-d | 未读我方结论前完成的盲析部分**可自证**（如保留命令执行顺序） |
| T0-e | 无"文档说/应该/大概"这类无证据表述 |

> **补充需求方说明**：T0 若发现我方结论有误，**那是最有价值的产出，不要为了"对齐"而弱化分歧**。需求方会据此修正方案文档并记录变更原因。

---

## 4. T1 详细规格：WAF IP 级 fail-fast 熔断

### 4.1 需求

WAF 403 拦的是**网关出口 IP**，不是账号。原作者实测：3 个账号 1 秒内全被 403。

现有行为的问题：账号级软冷却不够——继续轮转会**放大**请求量（`MaxRotate` 默认 3 倍）打同一出口 IP，**加重风控**。

目标行为：短窗（**60s**）内 ≥**2** 个**不同** UID 接连命中 WAF 403 → 判定 IP 级拦截 → **轮转立即终止**（放大倍数 = 1）→ 窗口到期自然解除。

### 4.2 判定口径（不要自创第二套判定）

- 单个账号反复 403（账号级偶发）**永不**触发——只数**不同 UID** 数。
- 激活期内新的命中**不续期**（保守：窗口自然解除，不做主动探测）。
- 账号级软冷却照常记账，**不受影响**（IP 级只改「是否继续轮转」）。
- 进程内状态、重启清零（窗口 60s，重建成本极低）。

### 4.3 依赖链【必读】

本仓快照**缺失**以下全部前置。我实测确认：

| 上游提交 | 提供 | 本仓实测状态 |
| --- | --- | --- |
| `76fafa6` | `upstream.ErrWafBlock` 分类 + `IsWafBlocked()` + `ParseRetryAfter()` + `Error.RetryAfter` 字段 | **全缺**。快照 `ErrKind` 只到 `ErrClient`；`RetryAfter` 搜不到 |
| `34405ca` | WAF 403 软冷却 + 轮转间指数退避 | **缺**。快照无 `rotateBackoff`（实测 grep 为空） |
| `8825c4c` | `internal/server/wafip.go` + 轮转循环插入点 | **缺** |

### 4.4 【决策】最小化回移范围——只做 IP 级熔断，不做整条链

**不要**照搬 `76fafa6` 的全部内容。它包含 `ParseRetryAfter` / `retryAfterHeaderCandidates` / `isAllDigits` / `parseRetryNumber` / `hasBusinessEnvelope` / `IsWafBlocked` / `Error.RetryAfter` / `ChatStreamContext` 返回值改造——**大部分与 IP 级熔断无关**。

T1 **只需要**以下最小集合：

1. **`ErrWafBlock` 枚举值 + `String()` 分支**（`internal/upstream/client.go`）
   - 加在 `ErrClient` **之前**（保持 `ErrClient` 为最后兜底）。
   - `String()` 返回 `"waf_block"`。
2. **`IsWafBlocked(status, body)` + `hasBusinessEnvelope(body)`**（`internal/upstream/client.go`）
   - 判定：`status == 403 && !hasBusinessEnvelope(body)`。
   - `hasBusinessEnvelope`：body 含 `"code":` 或 `"msg":` 即视为业务信封（**不解析 JSON**，只做字符串包含——畸形 JSON 含该字样仍按业务 403 处理，宁漏判 WAF 也不误罚业务 403）。
   - **不引入** `RetryAfter` / `ParseRetryAfter`：IP 级熔断的时长是本地固定窗口（60s），不依赖上游头。
3. **`Classify()` 插入 WAF 分支**（`internal/upstream/client.go`）
   - 位置：**在 `status >= 500` 判断之后、内容策略/参数错误/通用 4xx 兜底之前**（上游实测的正确位置）。
   - 本仓 `Classify` 的实际结构：见 `upstream/internal/upstream/client.go`，按实际锚点插入，勿照抄上游行号。
   - 必须保证带业务信封的 403（如 `11140 request illegal` → `ErrAccountFault`）**不受影响**——它们在更上层已被捕获。
4. **`internal/server/wafip.go`**（**新增文件**，74 行）
   - 从原作者副本取全文：`D:\work\02toolsMy\workbuddy2api_v0\internal\server\wafip.go`（我已读全文，内容可直接用）。
   - `wafIPWindow = 60 * time.Second`（`var`，供测试注入短窗）；`wafIPThreshold = 2`（`const`）。
   - `type wafIPGate struct { mu sync.Mutex; hits map[string]time.Time; until time.Time }`，**零值可用**。
   - `func (g *wafIPGate) noteWaf(uid string) bool`：激活期内恒 `true` 且不续期；未激活则记 `hits[uid]=now`、清窗口外过期项、`len(hits) >= wafIPThreshold` 时激活到 `now+wafIPWindow` 并打 WARN、清空窗口。
   - `func (g *wafIPGate) active() bool`。
5. **`Handler` 加字段 + 轮转插入点**（`internal/server/handler.go`）
   - `Handler` struct 加 `wafIP wafIPGate`（零值可用，与现有 `degrade degradeGate` 同风格）。
   - **插入点（本仓实测行号）**：`upstream/internal/server/handler.go` 第 **616-619** 行区域，当前内容为：

     ```go
     lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
     h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel)
     fail(acct.UID)
     continue
     ```

     **在第 617 行的 `applyErrorPolicy` 之后、`fail(acct.UID)` 之前**插入：

     ```go
     if kind == upstream.ErrWafBlock && h.wafIP.noteWaf(acct.UID) {
         break
     }
     ```

     【陷阱】本仓此处**没有** `rotateBackoff`（上游才有）。**不要**引用不存在的符号。本仓该分支是 `continue`（直接下一轮），IP 级熔断在此 `break` 即可达成"终止轮转"。
   - **末端错误文案**：在写错误响应的分支加 `ErrWafBlock` 的 case。参考上游：

     ```go
     case upstream.ErrWafBlock:
         if h.wafIP.active() {
             code = "waf_ip_blocked"
             msg = "waf ip-level block: upstream firewall is blocking the gateway IP, rotation stopped; retry after the block window expires"
         }
     ```

     【陷阱】本仓错误文案分支的实际结构与上游不同，**按本仓实际的分支变量名适配**（找到设置 `code` / `msg` 的那个 switch）。若找不到合适锚点，可退化为：不改文案分支（IP 级熔断的核心价值是"止损"，文案是次要的）。**但要在交付说明里写清是否做了文案**。

### 4.5 T1 验收条件（缺一不可）

| 编号 | 条件 | 怎么验 |
| --- | --- | --- |
| T1-a | 窗内 2 个不同 UID → 激活 | 单测：`noteWaf("u1")==false`，`noteWaf("u2")==true` |
| T1-b | 单号反复命中（任意多次）→ 恒 `false` | 同一 UID 循环 N 次断言全 `false` |
| T1-c | 窗口过期自然解除；激活期内不续期 | 注入短窗（改 `wafIPWindow`）推进时间断言 `active()` 翻转 |
| T1-d | **激活期轮转终止：上游调用数 = 2（非 3）** | 端到端：假上游返回 403，断言被调用次数 |
| T1-e | 并发安全 | 并发 `noteWaf`/`active` 混发测试 + **`-race`** |
| T1-f | 账号级软冷却不受影响 | `applyErrorPolicy` 现有测试全绿 |
| T1-g | 带业务信封的 403 仍走原分类 | `Classify(403, '{"code":11140,...}')` 仍返回 `ErrAccountFault` |
| T1-h | 现有路由/测试不回归 | `go test ./...` 全绿 |

**【陷阱】T1-d 必须真的计数。** 上游为此专门断言"调用数 = 2 非 3"，这是"止损成立"的唯一硬证据。只断言"返回 403"是不够的。

### 4.6 T1 参考实现

- `wafip.go` 全文：`D:\work\02toolsMy\workbuddy2api_v0\internal\server\wafip.go`（74 行，可直接取用）
- 上游测试参考：`workbuddy2api_v0` 的 `internal/server/wafip_test.go` + `wafip_conc_test.go`
  （**注意**：上游测试可能引用本仓不存在的符号，如 `rotateBackoff`。**参考断言思路，不要整文件照搬**。）

---

## 5. T3 详细规格：流式 usage 缺失哨兵

### 5.1 需求（已实测确认是本仓真 bug）

- `upstream/internal/server/logging.go:41`：`newChatStat` 以 `toks: -1` 初始化，注释「toks 默认 -1（usage 缺失）」。
- `logging.go:29`：字段注释「<0 表示 usage 缺失 → 显示 "-"」。
- `logging.go:202`：按 `toks >= 0` 决定显示 `-` 还是数字。
- **`upstream/internal/server/handler.go:633`（bug）**：`st.toks, _ = stats.Tokens()` —— **无条件**用零值 `0` 覆盖哨兵，并丢弃 `hasUsage`。
- 非流式分支（`handler.go:656`）走 `completionTokens(resp)`，缺失显式返回 `-1`，**不受影响**。

后果：两条路径口径不一致，流式把「没观测到 usage」伪装成「测得 0 token」，表格日志输出 `tok=0 | 0.0tok/s`。

### 5.2 【陷阱】这是服务端日志口径，不是客户端 `usage` 字段

- 客户端侧 `upstream/internal/upstream/sse.go:252` 注释「usage 缺失 → null」——**本来就正确，不要动**。
- 本任务**只改服务端日志统计**。

### 5.3 修法（对齐上游 `b1f7bc8`，4 行改动）

`upstream/internal/server/handler.go` 第 633 行：

```go
// 改前
st.toks, _ = stats.Tokens()
```

```go
// 改后
toks, hasUsage := stats.Tokens()
if hasUsage {
    st.toks = toks
}
```

并复用同一 `hasUsage` 到下方成本账本分支（第 638 行），消除原先为拿 `hasUsage` 而二次调用 `Tokens()`：

```go
// 改前（第 638 行）
} else if _, hasUsage := stats.Tokens(); hasUsage {
// 改后
} else if hasUsage {
```

### 5.4 T3 验收条件

| 编号 | 条件 | 怎么验 |
| --- | --- | --- |
| T3-a | 末帧无 usage → 日志含 `tok=-`，且**不含** `tok=0` | 新增测试，含新夹具 `sseNoUsage`（末帧不带 usage 子对象） |
| T3-b | 末帧有 usage → 仍输出正确 token 数 | 现有流式日志测试全绿 |
| T3-c | 非流式路径口径不变 | 现有非流式日志测试全绿 |

**测试写法参考**：上游 `b1f7bc8` 新增了 `internal/server/logging_test.go` 的 `sseNoUsage` 常量和 `TestChatLogsStreamRowNoUsageShowsDash`。可参考它在 `workbuddy2api_v0` 的 diff（`git -C D:\work\02toolsMy\workbuddy2api_v0 show b1f7bc8`）。

---

## 6. 【决策】明确不做的事（写在这里防止范围蔓延）

| 不做 | 原因 |
| --- | --- |
| `ParseRetryAfter` / `Error.RetryAfter` / 轮转指数退避 `rotateBackoff` | 与 IP 级熔断无关；引入会牵动 `ChatStreamContext` 返回值签名，波及面大 |
| `upstream/` 快照更新 / `upstream.lock` 改动 | 用户未授权；实测 5 个补丁全冲突 + `MaxBodyMB` 破坏性变更 |
| 既有 5 个补丁的语义修改 | 只允许"新增补丁"，不动老补丁的行为 |
| `/v1/stats` 端点（M3b） | **用户已拍板：本轮不做。** 规模与耦合见 §6.1，替代方案一并登记交 pi 验收定论 |
| `AccessTokenValue()` / `DomainValue()` 移植 | 与现有 `Snapshot()` 路线重复，见 §3 |
| `cmd/acct` / `cmd/credit` / `cmd/trial` / `cmd/stats` | 原作者本机工具链，与部署无关 |
| `.github/actions/ai-governance/` | 原作者的 PR 治理 AI 流水线，14+ 测试文件，与本项目无关 |
| 发布镜像 / 推送 Git / 重启服务 | 均需单独授权 |

### 6.1 M3b（`/v1/stats`）——本轮不做，替代方案登记备查

**结论（2026-09-22 用户拍板）**：不做。请 pi 在最终验收时对本节替代方案单独定论，不要当作"已实现"。

**规模实测**（本地克隆 `D:\work\02toolsMy\workbuddy2api_v0` @ `b71d734`）：

- `internal/server/metrics.go` = **360 行**（本节原写 324，已按实测更正）；`metrics_credits_test.go` = 173 行；另有 `metrics_test.go`。
- 路由 `GET /v1/stats`（`metrics.go:350`）+ `POST /v1/stats/reset`（`metrics.go:357`）。
- 前置耦合：倍率来自 `enrichCredits`（`metrics.go:290`），依赖 `GlobalModelInfosSnapshot` 只读快照；本仓 `internal/upstream/global_models.go` 已被补丁 0002 改过（`Snapshot()` 插入点），是本轮**冲突风险最高**的一处耦合，且本仓快照只有 4 条路由、`internal/server/` 下无 `metrics.go`，属"从零引入"而非"加一列"。

**替代方案登记**：如果真实需求是"界面看到运行状态"，现成链路已经够了，零补丁：

```text
core /status  →  bridge GET /internal/v1/status  →  console GET /admin/status
handler.go:138    bridge.go:64                      server.go:125
```

**必须诚实标注的边界**（pi 验收时按这条判，不要按"替代方案已覆盖统计"判）：`/status` 载荷只有账号池与网关状态（`total/healthy/cooling/disabled/in_flight_full/realm_totals/sticky_sessions/redis_mode` + `accounts`），**没有**按模型聚合的 token / 延迟 / 积分倍率。本仓也不存在任何可读 usage 的 HTTP 出口——`internal/server/logging.go` 的 `chatStat` 只写进程日志，既无聚合存储也无端点。所以"看 token 统计"这一项**无法**由 `/status` 满足；若将来要满足，需要独立设计（M3b 或本地聚合日志），而不是把本节当成已解决。

---

## 7. 补丁编写规范

### 7.1 新补丁文件

- 命名：`patches/0006-waf-ip-failfast.patch`（T1）
- T3 若需独立补丁：`patches/0007-usage-sentinel.patch`。
  **【决策】** 允许把 T3 并入 0006 以减少补丁数，但要在 `series` 与 README 里写清；若并入，补丁名改为 `patches/0006-waf-ip-failfast-and-usage-sentinel.patch`。
- 追加到 `patches/series` 末尾（**不改**现有 0001–0005 顺序）。
- 补丁必须基于**本仓快照 `c576b48` 的实际文本**生成，**不要**照抄上游 HEAD 的 diff——行号与上下文都不同（实测：上游 HEAD 与快照在 `handler.go` / `client.go` 上差异巨大）。

### 7.2 生成方式（推荐）

```bash
# 1. 物化一份干净快照（用尚不存在的输出路径）
python3 scripts/overlay.py prepare --output .build/backport-work

# 2. 在物化目录里改代码（改 upstream 侧文件）
cd .build/backport-work
# ... 编辑器改 internal/upstream/client.go, internal/server/handler.go, 新增 wafip.go ...

# 3. 生成补丁（相对物化目录根）
git diff > /tmp/waf.patch      # 或 git add -N 后 git diff --binary

# 4. 回写为仓库补丁并校准路径前缀
#    确认补丁头是 a/internal/... b/internal/...（-p1 可应用）
```

**【陷阱】** 物化目录旧了不会自动同步。每次重跑用**新的**输出路径，不要复用、不要删除来源不明的目录。

### 7.3 测试放哪

- **新增独立测试**（不覆盖上游文件的测试）优先放 `extensions/` 的对应相对路径。
- 若必须改**既有上游测试文件**（如 `logging_test.go`），那必须进 `patches/`。
- 我不强求：若 `wafip_test.go` 作为**新文件**加入 `internal/server/`，可放 `extensions/internal/server/wafip_test.go`（overlay 会复制过去），这样补丁只含生产代码，更干净。**建议这么做。**

---

## 8. 验证清单（实施方必须逐条实跑并贴输出）

> ⚠️ **2026-09-22 复核**：本节 §8.1 与 §8.2 的配方在本机 Windows 上**一行都跑不了**（`prepare` 摘要必失败、本机无 Go、此前 daemon 未起）。执行入口改用 **§12.3 的容器 clone 配方——该配方已实测跑通，HEAD `71fa504` 的基线结果列在 §12.3 末尾**；命令内容（测哪些包、哪些判据）不变。

### 8.1 基础

```bash
# 物化（新路径）
python3 scripts/overlay.py prepare --output .build/backport-verify

# Go 测试 + vet
go -C .build/backport-verify test ./...
go -C .build/backport-verify vet ./...

# 关键包定向
go -C .build/backport-verify test ./internal/server ./internal/upstream ./internal/auth ./internal/pool

# console 不回归
go -C console test ./...
node --test console/web_test.cjs

# Python 任务测试
python3 -m unittest discover -s scripts -p 'test_*.py' -v

# Compose 合法性
docker compose --env-file /dev/null -f docker-compose.yml config --quiet

# 行尾一致性
git diff --check
```

### 8.2 竞态（**必须**，本机无 gcc 时进容器）

```bash
docker run --rm -v "$(pwd -W):/src:ro" -w /src/.build/backport-verify -e HOME=/tmp \
  docker.m.daocloud.io/library/golang:1.23-alpine sh -c \
  "sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories \
   && apk add --no-cache gcc musl-dev >/dev/null && CGO_ENABLED=1 go test -race -count=1 ./internal/server ./internal/upstream ./internal/auth"
```

> 配方来源：`2026-09-22-security-review-tracking.md` §6 实测。`registry-1.docker.io` 本机超时，`docker.m.daocloud.io` 可拉；Git Bash 下 docker 的路径参数需 `MSYS_NO_PATHCONV=1`。

**【陷阱】`-race` 不能省。** T1-e（并发安全）与 T2 的全部价值都在 `-race` 上。若容器起不来，**必须在交付说明里明确写「竞态未验证」**，不得用 `go test` 通过冒充。

### 8.3 已知环境性失败（**不是回归**，不要试图修）

| 项 | 现象 | 定性 |
| --- | --- | --- |
| `python3 -m unittest discover -s scripts` | **真 `python` 3.12.10 实测：`Ran 36 tests` → 3 failures + 21 errors**。失败样例：`test_digest_tracks_content_and_executable_mode`（`chmod(0o755)` 在 NTFS 是空操作）、`test_export_uses_committed_blobs_and_preserves_license_and_modes`（`0o755 != 0o666`）、`test_digest_hashes_symlink_text_without_following_it`（`os.symlink` → `OSError [WinError 1314] 客户端没有所需的特权`） | **Windows 平台语义**（执行位 / symlink 特权 / POSIX 模式），与 `python3` 是否为 Store 占位符无关。本文原写「4 failures + 26 errors + 归因 Store 占位符」是占位符解释器下的旧计数，见 §12.4 #3 |
| `python3 -m unittest discover -s deploy` | **两个环境都无法全绿，必须合判**（2026-09-22 双实测）：本机（Windows + 有 docker CLI）`Ran 13 → 2 failures`；容器（Linux + 镜像内**无** docker CLI）`Ran 13 → 6 errors`，全部落在 `test_compose.py`（`subprocess.run` 起 `docker` 时 `FileNotFoundError`），而 `python3 -m unittest test_migrate` 单独跑 **8 tests OK** | 环境性，互补盲区。本机那 2 条已定位：`test_compose.py:42` 断言 `mount["source"].startswith(str(directory / "runtime/wb2api") + "/")`，Windows 下该路径渲染成反斜杠而 compose 输出正斜杠 → 假失败；另一条是 `test_migrate` 的 `restricted_modes`（NTFS 无 POSIX 权限位）。判据分工：**`test_migrate` 认容器**，**`test_compose` 需"Linux + docker CLI"环境**（我们两个环境都不满足，按已知环境性登记，不得改断言迁就） |
| `gofmt -l` | **本机无法复现**：本机没有 Go 工具链（§12.2），`gofmt` 也不存在 | 格式化判据挪到容器内，对 clone 出来的 LF 树执行 |
| 本机 `go build` / `go test` / `go vet` | `go: command not found`（`C:\Program Files\Go`、`C:\Go`、`%USERPROFILE%\go\bin` 均无） | 环境性。本文其余处方（§8.1、§8.2）在本机不可执行，原因见 §12.1–§12.2 |
| 本机 `-race` | 无 gcc 且无 Go | 竞态判据只在容器内有效（§12.3 已装 `gcc 14.2.0` + `musl-dev`） |
| `git diff --check` | exit 0（本机可跑，真实判据） | 有效 |
| `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` | exit 0（本机可跑，真实判据） | 有效 |

**判据**：这些项在"改动前基线"与"改动后"必须**计数相同**。要在验证记录里贴两次计数对比。

**【陷阱】"本机计数相同"不等于"改前改后一致"** —— 本机环境没变，同一条 Python 命令跑两次必然相同，这只证明命令稳定，不证明改动无害。**能区分二者的只有 §12.3 容器内那套**：实测 Linux 下 `python3 -m unittest discover -s scripts -p 'test_*.py'` → **`Ran 43 tests` / `OK`**（§12.1 那 3 个本机失败项在这里全部转绿，这才是 G1 的真实判据）；Go 侧必须与 HEAD 基线逐项同结果。Python 侧的本机计数只作辅助。

---

## 9. 交付要求

### 9.1 必须产出

1. **代码**：`patches/0006-*.patch`（+ 可选 0007）、`patches/series` 更新、新增测试文件。
2. **文档**：更新 `patches/README.md` 表格——为 0006 增加一行，列「修改的文件」「验证方式」「移除条件」（照现有行格式）。
3. **进度文件**：维护 `docs/superpowers/plans/2026-09-22-backport-progress.md`，含任务总览（✅/⬜/🔄）+ 中断保护区。
4. **验证记录**：`docs/superpowers/verification/2026-09-22-backport-verification.md`，逐条贴**实际命令 + 实际输出**。

### 9.2 交付说明必须包含

- 改了哪些文件（完整路径）
- 每个文件的变更摘要
- 每条验收条件的实跑结果（贴输出摘要）
- **未能验证的项目**，及原因（不得隐去）
- 与本文档的任何**偏离**，及理由
- 是否产生孤立代码（无用 import / 变量 / 函数）

### 9.2b 【约定】提交必须按「与本文档的差异」切分

用户指令（2026-09-22）：**与本文档（pi 原交接清单）有差异的内容单独一次提交**，便于验收和后续修改。落地为三棒，顺序固定：

| 棒 | 内容 | 判据 |
| --- | --- | --- |
| C1 | **照单执行**：严格按本文档原方案落地的实现（T1/T3 + §9.1 的补丁、`series`、测试） | 只含照单内容，验收方可逐条对 §4.5 / §5.4 打勾 |
| C2 | **对交接清单本身的修订**：所有偏离与复核结论（§6.1 M3b 裁定、§12 全部、§8/§8.2 作废指针、spec 的 324→360 与 Q5 已裁定、`wafip.go` 归 `extensions`） | 验收方据此判定"实施方改了任务书哪些地方"，不必读 diff 反推 |
| C3 | 收尾：进度文件终态、验证记录、`patches/README.md` 表格行 | — |

**执行期就按这个切分暂存**：C2 触及的文件（两份 09-22 文档）与 C1 的代码文件天然不重叠，若中途混改要在交付说明里点名。本地提交即可，推送仍需单独授权（§6）。

### 9.3 【决策】禁止行为

- ❌ 不许说"完成"而不贴验证输出
- ❌ 不许把环境性失败算成回归或算成通过
- ❌ 不许在容器起不来时声称 `-race` 通过
- ❌ 不许修改既有 5 个补丁的语义
- ❌ 不许改 `upstream/` 或 `upstream.lock`
- ❌ 不许 `git push`、发布镜像、重启真实服务
- ❌ 不许重置工作区、清空数据、覆盖真实配置

---

## 10. 我（验收方）将如何验收

我会**独立复跑**，不依赖你的自述：

1. `git status --short` 检查工作区是否被污染、有无未跟踪垃圾。
2. 在**全新输出路径**物化，确认 6 个补丁按 `series` 顺序全部 `git apply --check` 通过。
3. 独立跑 §8.1 / §8.2 全部命令，把输出与我自己的基线计数对比。
4. **对抗测试**：我会自己写测试尝试推翻你的实现，重点：
   - 同一 UID 反复 403 是否真的不触发（T1-b）；
   - 轮转调用数是否真的 = 2（T1-d）——**这是最容易做假的地方**；
   - 带业务信封的 403（`11140`）是否仍走 `ErrAccountFault`（T1-g）；
   - 在**改动前基线**上复现红态，证明修复真关掉了一个真实缺口；
   - 竞态：`-race` 下反复构造 `ChatHeaders` × `RefreshToken` 并发（T2）。
5. 对照本文档 §4.5 / §5.4 逐条判定 ✅/❌。
6. 检查 `patches/README.md` 是否同步、移除条件是否写明。

**验收不通过的典型原因**（提前告知）：只断言"返回 403"而没断言调用次数；把 `go test` 绿当成 `-race` 绿；改了老补丁；补丁基于上游 HEAD 而非本仓快照生成导致物化失败。

---

## 11. 待你确认的问题

| # | 问题 | 我的建议 |
| --- | --- | --- |
| Q1 | T3 是否并入 0006 一个补丁？ | 建议**并入**，减少补丁数；但若你更看重"一补丁一职责"，可拆 0007 |
| Q2 | `wafip_test.go` 放 `extensions/` 还是进补丁？ | 建议放 `extensions/internal/server/`（补丁只含生产代码，更干净） |
| Q3 | T1 末端错误文案（`waf_ip_blocked`）是否必须？ | 建议**尽力做**；若本仓分支结构不匹配，可省略并在交付说明写明 |
| Q4 | 是否有 gcc / 可用的 Linux 容器环境？ | **必须回复**——决定 T1-e 与 T2 能否给出 `-race` 判据 |
| Q5 | T0 与 T1–T3 是否由**同一人/同一会话**做？ | 建议**分开**：T0 盲析若与实现同会话，容易带着实现思路污染判定。若只能同会话，则必须先完成 T0 盲析并落盘，再开始 T3 |

---

## 12. 【复核 2026-09-22】09-15 计划遗留坑与本轮影响

对 `docs/superpowers/plans/2026-09-15-upstream-overlay-task-console.md` Task 1–9 的逐项复盘。每条都给了实测证据，分三类：**卡本轮的**、**登记不修的**、**已排除的伪坑**。

### 12.1 🔴 G1：`overlay.py` 是 Unix-only 契约 —— §8 的全部命令在本机不可执行

09-15 Task 1 把「Git 模式（100644/100755）」编进了 `source_digest`，`upstream.lock` 因此含 7 条 `100755`；Windows 原生 Python 的 `Path.stat().st_mode` 不区分执行位 → 摘要必然不匹配 → `prepare` 报 `upstream source digest does not match upstream.lock`（`scripts/overlay.py:50-55`、`:69-75`、`:258`）。

实测（本机，真 `python` 3.12.10，非 Store 占位符）：

```text
python -m unittest discover -s scripts -p 'test_overlay.py'   → Ran 36 tests → 3 failures + 21 errors
FAIL  test_digest_tracks_content_and_executable_mode      chmod(0o755) 在 NTFS 是空操作
FAIL  test_export_uses_committed_blobs_and_preserves_...   AssertionError: 493 != 438（0o755 vs 0o666）
ERROR test_digest_hashes_symlink_text_without_following_it OSError [WinError 1314] 客户端没有所需的特权
```

- 结论：**§8.3 的归因不完整**——不是只缺 `python3`，而是整个 Task 1 工具链的契约（执行位、symlink、POSIX 权限）在 Windows 上不可能成立。计划自己的 GREEN 判据「两次不同新目录物化的源码摘要一致」也只在 Unix 可证。
- 旁证该链在 macOS 上编写：计划 Global Constraints 根路径写死 `/Users/zeelin/WorkCode/workbuddy2api`；AGENTS.md 还留着「不得误发 ARM 镜像」的告诫。
- 这不是回归，是**平台假设没写进文档**。影响面：`check.sh`、`acceptance.sh`、`release.sh`、`deploy/core.Dockerfile:9` 全走 `prepare`，本轮 T1–T3 的每条验收命令都压在它上面。
- **容器内实测（2026-09-22，见 §12.3）：`prepare` rc=0，锁摘要 `299e722b5dce…` 校验一致。** ⇒ G1 只是"本机 Windows 不适配"，**不是 §12.3 配方有缺陷，本仓代码也不需要为此改一行**——`deploy/core.Dockerfile:9` 在 Linux 构建层里一直是正常的。

### 12.2 🔴 G2：本机没有 Go 工具链，Docker 守护进程也没起

```text
which go                          → 空；C:\Program Files\Go、C:\Go、%USERPROFILE%\go\bin 均不存在
docker version                    → failed to connect to the docker API at npipe:////./pipe/dockerDesktopLinuxEngine
```

所以文档里每一条 `go -C ...` 在本机都跑不了，"容器内跑"落地需要三个前置：

1. Docker Desktop 引擎在跑 —— **已满足**（2026-09-22 启动后 `docker version` → server 29.7.2）。
2. 有 `docker.m.daocloud.io/library/golang:1.23-alpine` —— **已满足**（本地已有，`docker images` 实测 370MB，pull 返回 up to date）。
3. 容器内能取到上游依赖 —— **已满足**（本次显式 `GOPROXY=https://goproxy.cn,direct`，`go-redis`/`xxhash`/`go-rendezvous`/`uber.org/atomic` 四个都下载成功。默认 `proxy.golang.org` 未测，不宣称它不通）。

**⇒ G2 已解除。** 容器网络实测可达（`apk add` 从 `mirrors.aliyun.com` 装齐 `git 2.49.1` / `Python 3.12.14` / `bash` / `gcc 14.2.0` + `musl-dev`），基线全绿见 §12.3。

**【误判更正，备查】** 本轮分析过程中曾两次归因错误，都不成立，写在这里防止 pi 复述：

- ~~"本机缺 gcc，所以 `-race` 才要进容器"~~ —— 本机连 Go 工具链都没有，`-race` 从来不在本机跑；容器也确实装了 gcc。真正的门槛是 daemon 没启动。
- ~~"§12.3 的 `apk add` 漏了 `python3`/`bash`，stage3 会 `python3: not found`，需改成 `apk add git python3 bash gcc musl-dev`"~~ —— 假缺陷。`overlay.py` 只用标准库，容器解释器就是容器里的 `python3`；按原文配方**一次跑通，一行未改**。
- ~~"基线是 16 或 17 个包 / 31 个包"~~ —— 实测 `go test ./...` 是 **20 个包全 ok**（`cmd/` 6 个 + `internal/` 14 个）。

### 12.3 验证的正确姿势（替代 §8.1 与 §8.2）

必须**先 clone 再验证**，不能直挂 Windows 目录——直挂等于摘要仍不匹配，且只读挂载写不出 `.build`：

```bash
# 0) WIP 先本地提交（不推送），否则 clone 拿不到未提交改动；三份 09-22 文档也一并入库
MSYS_NO_PATHCONV=1 docker run --rm -it -v "$(pwd -W):/host" \
  docker.m.daocloud.io/library/golang:1.23-alpine sh
# 容器内：
sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories
apk add --no-cache git python3 bash gcc musl-dev
export GOPROXY=https://goproxy.cn,direct   # 模块缓存为空时必须显式指定，否则直连 proxy.golang.org
git clone /host /work/repo
python3 /work/repo/scripts/overlay.py prepare --output /work/core
go -C /work/core test ./... && go -C /work/core vet ./...
CGO_ENABLED=1 go -C /work/core test -race -count=1 ./internal/server ./internal/upstream ./internal/auth
go -C /work/repo/console test -race ./...
```

`git clone` 读对象库 → 自动得到 LF 与正确执行位（`.gitattributes` 已固定 `eol=lf`）。**§8.2 原配方（`-v $(pwd -W):/src:ro` 再进 `/src/.build/backport-verify`）不可用，按本节替换。**

**已实测通过（2026-09-22，HEAD `71fa504`）**：宿主机把脚本写到仓库外的目录，再单条命令拉起容器——

```bash
mkdir -p /d/work/02toolsMy/_wb2a-verify            # 仓库外，不污染工作区
# base.sh 内容：见下方 stage1–7；先 `sed -i 's/\r$//' base.sh` 去掉 CRLF
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "D:/work/02toolsMy/workbuddy2api-ui-me:/host:ro" \
  -v "D:/work/02toolsMy/_wb2a-verify:/out" \
  -v wb2a-gomod:/go/pkg/mod \
  -w /work -e GOPROXY=https://goproxy.cn,direct \
  docker.m.daocloud.io/library/golang:1.23-alpine sh /out/base.sh > base.log 2>&1
```

`base.sh` 的 stage 顺序（容器内 root，一次跑完 7 步）：

```sh
set -eu
# 1) 换阿里云源 + 装依赖，base image 里没有 git/python3/bash/gcc
sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories
apk add --no-cache git python3 bash gcc musl-dev
# 2) clone 到容器本地盘（Linux 文件系统，才有真正的 exec 位）
git config --global --add safe.directory '*'
git clone -q --no-hardlinks /host /work/repo && cd /work/repo
# 3) 物化 + 摘要门（这一步就是 G1 的判据）
python3 scripts/overlay.py prepare --output /work/core
# 4)–7) 测试
go -C /work/core test ./... ; go -C /work/core vet ./...
CGO_ENABLED=1 go -C /work/core test -race -count=1 \
  ./internal/auth ./internal/pool ./internal/upstream ./internal/server ./internal/scheduler
cd /work/repo/console && go test -race -count=1 ./...
```

基线结果（**改动前**，后续每条验收都要与这张表逐项对比）：

| 步骤 | 实际输出 |
| --- | --- |
| `prepare` 摘要门 | rc=0；`upstream.lock` commit `c576b48`、`source_sha256 299e722b5dce…` 校验一致 |
| `go test ./...` | **20 个包全 ok**（`cmd/` 6 + `internal/` 14），最慢 `internal/scheduler 24.207s`、`internal/server 10.130s` |
| `go vet ./...` | 无任何输出（干净） |
| `-race` 5 个关键包 | auth 1.029s / pool 1.299s / upstream 1.560s / server 2.892s / scheduler 4.137s 全 ok，**无 DATA RACE** |
| `console test -race ./...` | ok 1.173s |
| `python3 -m unittest discover -s scripts`（容器） | **`Ran 43 tests` / `OK`** |
| `python3 -m unittest discover -s deploy`（容器） | `Ran 13` → 6 errors，全在 `test_compose.py`（镜像内无 docker CLI）；`test_migrate` 单独跑 **`Ran 8` / `OK`** |
| `node --test console/web_test.cjs`（本机，Node v24.18.0） | **30 tests / 30 pass / 0 fail** |
| `discover -s deploy`（本机，真 python + docker CLI） | `Ran 13` → 2 failures，定位见 §8.3（`test_compose.py:42` Windows 路径分隔符假失败 + `test_migrate` POSIX 权限位） |
| 本机仍可跑的两项 | `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` exit 0；`git diff --check` exit 0 |

**注意**：`git clone` 只能看到已提交内容，验证 WIP 改动前必须先**本地提交**（不推送，见 §9.2b 的 C1/C2/C3 切分）。

**【陷阱】`base.sh` stage1 的 sed 分隔符**（本轮实测追加）：上面这一行原先写作 `s|https\?://…|…|g`，
在 `golang:1.23-alpine` 的 busybox sed 下直接 `sed: unmatched '|'`；改成 `#` 分隔但漏掉 `\?`
（写成 `https?://`）则**静默不匹配**——镜像源没换、apk 却可能因网络可达而成功，等于埋一个间歇性坑。
两个条件必须同时满足：`#` 分隔 + `\?` 转义，改完用 `head -1 /etc/apk/repositories` 复核。

### 12.3b 本轮改动后（HEAD `0466671`）实测与两条新坑

同一配方（clone → prepare → 全量测试）在 6 个补丁下的结果，逐行对上表：

| 步骤 | 改动后实际输出 |
| --- | --- |
| `prepare` 摘要门 | rc=0，6 个补丁按 `series` 顺序 `git apply --check` 全过 |
| `gofmt -l` | 无输出（扩展与补丁产物格式干净） |
| `go test ./...` | 20 个包全 ok（`internal/server 0.875s`、`internal/scheduler 1.808s`） |
| `go vet ./...` | 无输出 |
| `-race` 关键包 | server 2.082s / upstream 1.568s / auth 1.026s / pool 1.273s 全 ok |
| `console test -race ./...` | ok 1.170s |
| `node --test console/web_test.cjs`（本机） | 30 tests / 30 pass / 0 fail |
| `docker compose … config --quiet` / `git diff --check`（本机） | rc=0 / 干净 |
| `python -m unittest discover -s scripts`（本机） | `Ran 43` → 4 failures + 26 errors，**全部环境性**（`WinError 1314` 无 symlink 特权、无 `os.mkfifo`、temp 目录 ACL、`WSL execvpe(/bin/bash)`、NTFS 无执行位），POSIX 语义侧由容器路径承担 |

红→绿证据链与 T1/T2/T3 逐条判据见 `docs/superpowers/verification/2026-09-22-backport-verification.md`；
与本文档的偏差集中在 `docs/superpowers/plans/2026-09-22-backport-progress.md` §3 的 Δ1–Δ9。

**【新坑 A】`overlay.py identity` 在 Windows 上不是稳定值。** 同一份已提交内容：宿主
`python scripts/overlay.py identity` = `7b1edc83…`；容器**只读挂载宿主工作区**跑 = `4891f37c…`；
容器内 `git clone /host` 后跑 = `4f6b50a4…`。根因是 `overlay_identity()` 把
`source_digest(extensions)`（编码 POSIX 模式）与 `series`/补丁**字节**混在一起，而宿主工作区
受 `core.autocrlf=true` 影响（实测 `patches/series` 6/7 行为 CRLF）且挂载点把权限位报成可执行。
**只有 clone 路径的值可引用**：摘掉 0006 = `faba20a3…`，含 0006 = `4f6b50a4…`，
基线 `71fa504`（5 补丁）= `d8c08ffe…`。

**【新坑 B】本机 `python3` 是 Microsoft Store 占位符**（退出码 49、无输出），真解释器是
`python`（3.12.10）。§8.3 原归因需按此补全：占位符只解释"`python3` 跑不了"，
不解释 overlay 测试失败——失败是 Windows 平台语义（见上表最后一行），换真 `python` 同样不过。

### 12.4 两份 09-22 文档需要就地裁决的三处

| # | 冲突 | 裁决 | 依据 |
| --- | --- | --- | --- |
| 1 | spec 的 M1 依赖链表称需要 `34405ca`（软冷却 + 退避）整条链；§4.4 裁定只做最小集 | **以 §4.4 为准**，spec 表按本节注记更正 | §6「明确不做 `rotateBackoff`」与 §4.4 更晚且自洽 |
| 2 | §7.2 把 `wafip.go` 写进补丁 0006，§7.3 又说新增文件优先放 extensions | **`wafip.go` 与 `wafip_test.go` 都放 `extensions/internal/server/`，0006 只碰 `client.go` + `handler.go`** | AGENTS.md「新增独立能力优先放 `extensions/`，不打补丁」；扩展只禁止**同名覆盖**，快照里没有 `internal/server/wafip.go` → 合法新增；补丁体积与上下文耦合同时下降。Q2 随之自动定案 |
| 3 | §8.3 把 overlay 测试失败归因于 `python3` 是 Store 占位符 | 归因补全为 **Windows 平台语义**（执行位 / symlink 特权 / POSIX 权限），真 `python` 同样不过 | §12.1 实测输出 |

### 12.5 登记不修（09-15 遗留，与 T1–T3 不相交）

| 坑 | 证据 | 为何本轮不动 |
| --- | --- | --- |
| `overlay.py update` 把宿主环境原样传给候选 `acceptance.sh`；若 shell 里残留 `WB2A_ACCEPTANCE_SKIP_BUILD=true`（`release.py:69` 会设它），候选验收会指向**生产镜像**而不是候选构建，坏快照可带着"验收通过"被装上 | `scripts/overlay.py:589-596`（`subprocess.run(..., check=True)` 无 `env=`）+ `scripts/acceptance.sh:31` | 本轮不跑 update；修法=update 传干净 env，另立工单 |
| 回退无文档：README 与 AGENTS 全无「回退 / rollback」；`.upstream-update-backup/` 是**单代**备份且被下次 update 静默删除；崩溃残留 `.upstream-update-journal.json` 会让**每次 `prepare`（含 `core.Dockerfile:9` 的镜像构建）**直接失败 | `grep -rn "回退\|rollback" README.md AGENTS.md` → 空；`overlay.py:241-246`、`:459-480` | 本轮不跑 update，工作区当前无 journal 文件（已实测）；记为文档工单 |
| 全局无超时：`Scheduled` 在 `Run` 的 `select` 分支里**同步**调用，脚本子进程只绑 core 生命周期 ctx、无 deadline → 卡死的子进程同时占死任务槽与调度主循环，页面停在"运行中"直到重启转 `interrupted` | `patches/0004-scheduler-observation.patch:44-55`；`extensions/internal/scheduler/console.go` 内 `grep WithTimeout\|Deadline` → 空 | 属自动任务侧，T1 只动 chat 轮转，不相交；另立工单 |
| `task_events.py` 的 detail 白名单只约束脚本，Go 侧 `observe` 的 detail（`streak_failed`、`departed`…）不受约束 | `extensions/scripts/task_events.py:12-21` | 口径不一致但无害，登记 |
| `travel` 零奖励记 `success` + `reward_claimed`，测试名却叫 `…RemainsUnknown` | `patches/0004:295-300` vs `extensions/internal/scheduler/console_test.go:119-139` | 命名误导，非行为错误，登记 |
| Task 1–9 复选框**全部仍是 `- [ ]`**，而九项均已落地（`f3e55b9`…`ef358c2`）；Global Constraints 根路径是别人的 macOS 目录 | `git log --oneline`；§12.1 | 纯文档债，但会持续误导后续会话沿用 Unix 假设；建议补一次「计划已执行完毕」的收尾提交 |

### 12.6 已复核、**排除**的伪坑（不要再去"修"）

- 「错过的小时点永久丢失」：`nextFire` 只看未来（`upstream/internal/scheduler/scheduler.go:123-135`）是设计——计划明确「不做停机补跑」。
- 「`next_at` 缺失应显示"已禁用"」：`console/web/app.js:90` 的 `暂不可用 / 未知` 比计划字面更正确（enabled 但无 next_at ≠ 已禁用）。
- 「`patch_identity` 被改名」：函数叫 `overlay_identity`（`overlay.py:227`），但 JSON key 与 ldflag 仍是 `patch_identity` / `main.patchIdentity`（`extensions/internal/bridge/bridge.go:62`）——对外契约未变。
- CRLF：索引与提交一律 LF，`git clone` 后不存在该问题，不需要"修工作区"。

---

## 附录 A：事实索引（我已实测，可直接引用）

| 事实 | 值 | 验证方式 |
| --- | --- | --- |
| 快照 commit | `c576b48` | `upstream.lock` |
| 落后提交数 | 214 | `git -C workbuddy2api_v0 rev-list --count c576b48..HEAD` |
| 本仓快照路由数 | **4**（chat/completions、models、status、healthz） | `grep 'mux.HandleFunc("' upstream/internal/server/handler.go` |
| 快照有无 `metrics.go` | **无** | `ls upstream/internal/server/*.go` |
| 快照有无 `ErrWafBlock` | **无** | grep |
| 快照有无 `rotateBackoff` / `RetryAfter` | **无** | grep |
| 快照 `applyErrorPolicy` 签名 | `(uid, kind, body, model)` | `handler.go:716` |
| 上游 HEAD `applyErrorPolicy` 签名 | `(uid, kind, body, model, uerr)` | 上游 diff |
| 轮转循环 | `handler.go:516` `for i := 0; i < h.cfg.MaxRotate; i++` | 实测 |
| 插入锚点 | `handler.go:616-619` | 实测 |
| usage 哨兵 bug | `handler.go:633` 无条件覆盖 | 实测 |
| 哨兵初值/渲染 | `logging.go:41` / `:29` / `:202` | 实测 |
| 客户端 usage 正确 | `sse.go:252` | 实测 |
| 5 个既有补丁对上上游 HEAD | **全部 CONFLICT**（`git apply -3` 亦全败） | 导出 HEAD 后逐个 check |
| `MaxBodyMB` 破坏性变更 | 上游 `Server struct{}` 空壳 | `cmd/server/config.go:23` |

## 附录 B：关键文件位置

```text
本仓：D:\work\02toolsMy\workbuddy2api-ui-me
  patches/0001-auth-pool-consistency.patch      ← 含 Realm/RealmStored/BackfillRealm 加锁 + Snapshot()
  patches/0002-upstream-credential-snapshots.patch ← 含全部出站 a = a.Snapshot()
  patches/series
  patches/README.md                             ← 需同步更新
  extensions/cmd/server/extension.go            ← 注意 MaxBodyMB（本轮不触发）
  docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md

原作者副本（只读参考）：D:\work\02toolsMy\workbuddy2api_v0
  internal/server/wafip.go                      ← T1 可取全文
  internal/server/wafip_test.go, wafip_conc_test.go
  internal/upstream/client.go                   ← ErrWafBlock/IsWafBlocked 参考
  git show 8825c4c / 76fafa6 / b1f7bc8          ← 上游原始提交
```
