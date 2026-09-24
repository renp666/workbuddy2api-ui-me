# 验收裁定：上游安全修复回移（需求方独立复跑）

- **验收方**：需求方（本仓维护者侧）
- **验收日期**：2026-09-22
- **被验收对象**：`0466671`（代码）+ `6e5a18a` / `7c94f61` / `9353588`（文档）
- **验收方式**：**独立复跑**（不采信实施方自述），含对抗测试与红态复现
- **验收环境**：`docker.m.daocloud.io/library/golang:1.23-alpine`，`CGO_ENABLED=1`，`GOPROXY=https://goproxy.cn,direct`，容器内 `git clone /host` → `overlay.py prepare`
- **结论**：**通过**（附 3 项需求方文档缺陷已就地修正 + 1 项验收条件降级认定）

---

## 1. 裁定摘要

| 项 | 裁定 | 依据 |
| --- | --- | --- |
| T0 独立复析 | **降级通过**（T0-d 未达标） | 实施方主动声明盲析前置未满足（分析文档 §0），未隐去 |
| T1 WAF IP 级熔断 | **通过** | 8 项验收条件全绿，含 T1-d 调用数=2 的**红态复现** |
| T2 auth 竞争（仅验证） | **通过** | 生产代码零改动；`-race` 实测绿；函数级查漏有据 |
| T3 usage 哨兵 | **通过** | 红态 `tok=0` 复现 → 绿态 `tok=-` |
| T4 文档同步 | **通过** | `patches/README.md` 0006 行 + 移除条件 + 三份文档 |
| 硬约束遵守 | **通过** | `upstream/`、`upstream.lock` 零改动；0001–0005 语义未动 |
| 需求方文档缺陷 | **3 项确认成立，已修正** | D1 / §12.4-1 / D3-D4（见 §4） |

---

## 2. 需求方独立复跑的实际结果

以下全部由验收方本人在容器内实跑，**非引用实施方输出**。

### 2.1 物化与补丁栈

```text
$ python3 scripts/overlay.py prepare --output /work/core
（无输出，退出码 0）
$ ls /work/core
Dockerfile LICENSE README.md checkin.sh cmd config.example.json credit.sh
docker-compose.yml go.mod go.sum internal login.sh scripts signin.sh trial.sh
```

**结论**：6 个补丁按 `series` 顺序全部应用成功。物化两次结果 `diff -rq` 完全一致（`IDENTICAL_MATERIALIZATION`），补丁栈确定性成立。

### 2.2 关键改动确实落地（物化树内实测）

```text
internal/server/handler.go:70   wafIP   wafIPGate // WAF IP 级熔断状态机，零值可用
internal/server/handler.go:621  if kind == upstream.ErrWafBlock && h.wafIP.noteWaf(acct.UID) {
internal/server/handler.go:699  if h.wafIP.active() {
internal/upstream/client.go:36  ErrWafBlock  // 403 + 无业务信封体…
internal/upstream/client.go:58  case ErrWafBlock:  → "waf_block"
internal/upstream/client.go:363 return ErrWafBlock
internal/server/handler.go:639  toks, hasUsage := stats.Tokens()
internal/server/handler.go:640  if hasUsage {
internal/server/handler.go:647  } else if hasUsage {
```

### 2.3 构建、静态检查、全量测试

```text
BUILD_OK
VET_DONE（无输出）
go test ./... → 20 个包全 ok
  cmd/activity cmd/credit cmd/login cmd/server cmd/signin cmd/trial
  internal/anthropic auth bridge config logfmt oauth pool prompt
  internal/redisstore scheduler server session taskrun upstream
```

### 2.4 **竞态（关键判据，验收方实跑）**

```text
$ CGO_ENABLED=1 go test -race -count=1 ./internal/server ./internal/upstream ./internal/auth
ok  workbuddy2api/internal/server    2.469s
ok  workbuddy2api/internal/upstream  1.571s
ok  workbuddy2api/internal/auth      1.028s
```

**结论**：`-race` 真实通过。这是 T1-e（并发安全）与 T2 的核心判据，实施方声称的 `-race` 绿**经独立复现成立**。

### 2.5 控制台与页面不回归

```text
go test ./... (console)         → ok workbuddy2api-console 0.091s
node --test console/web_test.cjs → tests 30 / pass 30 / fail 0
git diff --check                 → WHITESPACE_CLEAN
```

Compose 配置结构人工核对：两服务镜像同时间戳 `1789550372`、`platform: linux/amd64`、
仅 console 发布 `0.0.0.0:7863:7863`、console 只读挂载 `keys` —— 与 AGENTS.md 一致。

---

## 3. 对抗测试（验收方自写，试图推翻实现）

### 3.1 T1-d 「轮转调用数 = 2 而非 3」——**红态复现成功**

我把交付物复制一份，**只删掉** `handler.go` 里的 fail-fast `break` 三行，重跑：

```text
--- FAIL: TestChatWafIPFailFastStopsRotation
    wafip_test.go:117: upstream calls=3 want 2
           (fail-fast stops rotation at threshold; u3 must not be hit)
--- FAIL: TestChatWafIPGateClearsAfterExpiryThenRotates
    wafip_test.go:184: stage1 must activate the IP gate
FAIL  workbuddy2api/internal/server  0.206s
```

**裁定**：修复**确实关掉了一个真实缺口**。删掉 `break` 后请求被放大到 3 倍（`MaxRotate` 打满），
正是本方案要消除的行为。测试**真的在计数**（`calls atomic.Int64`），不是只断言状态码——
这一点是我预设的最易造假处，实测经得起。

### 3.2 T1-g 「带业务信封的 403 不得被误判为 WAF」

```text
--- PASS: TestClassifyWafBlockShape
--- PASS: TestClassifyWafEnvelopeKeepsExistingKind   （Classify(403, 11140) == ErrAccountFault）
--- PASS: TestClassify
--- PASS: TestErrWafBlockStringAndOrdering
```

对照组断言齐备：`hasBusinessEnvelope('{"code":11140,…}')` 为真、`{"msg":"x"}` 为真、
`not json at all "msg": 1` 为真（保守）、`Request blocked by WAF` 为假。**符合"宁漏判 WAF 不误罚业务 403"的设计口径。**

### 3.3 T1-b 「单号反复 403 永不触发」

```text
--- PASS: TestChatWafSingleAccountStillRotates
    calls=2（bad 一次 + good 一次），rec.Code==200，h.wafIP.active()==false
```

断言包含"单号命中不得激活 IP 级状态"与"必须继续轮转到健康号"，两个方向都覆盖了。

### 3.4 T3 哨兵

```text
--- PASS: TestChatStatsReaderNoUsage / TestChatStatsReaderCreditNoUsage
--- PASS: TestLogChatRowNoUsageShowsDash
--- PASS: TestChatLogsStreamRow            （有 usage 时仍正确，无回归）
--- PASS: TestChatLogsStreamRowNoUsageShowsDash
```

实施方的 RED-B 红态（`tok=0 | 0.0tok/s`）与我读到的 `handler.go:639-647` 实现一致：
`sse.go:252`（客户端 `usage:null`）**未被误改**。

---

## 4. 需求方文档缺陷裁定（T0 的核心价值）

T0 提出 5 项分歧。验收方**逐条独立复核**，裁定如下：

| 分歧 | 实施方主张 | 验收方复核方法 | 裁定 |
| --- | --- | --- | --- |
| **D1** | §3 的 14 个函数名按字面 grep 会落空，实际插入点是 `*Context` / `globalModelsOnce` 等 | 容器内对物化树 `internal/upstream/*.go` 提取每个 `Snapshot()` 的**外层函数** | **成立**。实测：`ChatStreamContext`、`billingJSONContext`、`billingMeterJSONContext`、`globalModelsOnce`、`growthJSON`、`ChatHeaders`、`BillingHeaders`、`RefreshHeaders`、`FetchModels`、`RefreshToken`。原列表中的 `ChatStream`/`billingJSON`/`probeGlobalModels`/`UserResourceDetailed` **并非插入点** |
| **D2** | `ReportChatActivity`(读 `UID`)、`ClaimTrialContext`(读 `Realm()`) 无自身快照但安全 | 读 `RefreshToken` 锁内写者集合（`AccessToken`/`RefreshToken`/`Domain`/`ExpiresAt`），确认 `UID` 从不原地改写 | **成立**，非缺口。裁定"无 Snapshot ≠ 不安全"应在验收中分开判 |
| **D3** | §12.3 的 `sed` 配方不可照抄 | 验收方**本人在容器内跑**原配方 vs `\?` 版 | **成立**。验收方首次用 `s#…https\?://…#` 成功；未加 `\?` 会静默不匹配 |
| **D4** | `overlay.py identity` 在 Windows 有三个值，只有 Linux clone 路径可引用 | 验收方亲历：**宿主 `python scripts/overlay.py prepare` 直接失败**（`upstream source digest does not match upstream.lock`） | **成立**。这是硬证据——宿主编译路径根本不可用 |
| **§12.4-1** | spec 把 `34405ca`（软冷却+退避）列为必做依赖，与"只做 IP 级熔断"决策自相矛盾；§7.2 与 §7.3 对 `wafip.go` 归属亦互斥 | 读 spec 第 78-80 行与第 262 行 | **成立**。spec 原表 3 个 commit 全列"无"，候选 A 写"三段链最小化回移"，确与本轮最小集决策冲突 |

### 4.1 就地修正（验收方执行）

| 文档 | 修正内容 |
| --- | --- |
| `specs/2026-09-22-upstream-security-backport-design.md` | 依赖链表：`76fafa6` 标注"只取 `ErrWafBlock`+`IsWafBlocked`，不带 `ParseRetryAfter`"；`34405ca` 改标"**有意不回移**"；加 §12.4-1 修正注记 |
| 同上 | 候选 A 由"三段链最小化回移"改为"只回移 IP 级熔断最小集" |
| `plans/2026-09-22-backport-handoff.md` | §3 的 14 函数名列表加 D1 修正表：区分"实际插入点"与"公开 wrapper 委托" |

**这两处修正是 T0 存在的直接价值**：若无独立复析，后来者会照错误列表验收而误判"补丁没做到位"，
或照矛盾依赖链去实现整条 `34405ca` 链。

---

## 5. 验收条件最终判定

### T1

| 编号 | 条件 | 判定 | 证据 |
| --- | --- | --- | --- |
| T1-a | 窗内 2 个不同 UID → 激活 | ✅ | `TestWafIPGateMultiAccountTriggers` PASS |
| T1-b | 单号反复 → 恒 false | ✅ | `TestChatWafSingleAccountStillRotates` + 并发版 |
| T1-c | 过期解除 + 不续期 | ✅ | `TestWafIPGateWindowExpiry`、`…ClearsAfterExpiryThenRotates` |
| T1-d | **轮转调用数 = 2（非 3）** | ✅ | PASS + **红态复现 calls=3** |
| T1-e | 并发安全 | ✅ | `TestWafIPGateConcurrent` + **验收方实跑 `-race`** |
| T1-f | 账号级软冷却不受影响 | ✅ | `internal/server` 全量绿（注：本仓有意不引入 WAF 软冷却） |
| T1-g | 业务信封 403 仍 `ErrAccountFault` | ✅ | `TestClassifyWafEnvelopeKeepsExistingKind` |
| T1-h | 路由/测试不回归 | ✅ | 20 包全 ok |

### T2

| 编号 | 条件 | 判定 | 说明 |
| --- | --- | --- | --- |
| T2-a | 竞争关闭，`-race` 无 DATA RACE | ✅ | 验收方实跑 auth/upstream/server `-race` 全绿 |
| T2-b | 出站头经快照，不再锁外直读 | ✅ | 函数级复核（D1 修正后名单） |
| T2-c | 无自锁死锁 | ✅ | `Realm()` 等只在 `LoadDir`(不持锁) 调用；`Snapshot()` 按字段写 |
| — | **无红态**（Δ7） | ✅ 接受 | 竞争面已由 0001/0002 关闭，设计上无法造红态；**不得按红→绿格式要求** |
| — | 生产代码零改动 | ✅ | `extensions/internal/upstream/credential_race_test.go` 为纯新增测试 |

### T3

| 编号 | 条件 | 判定 |
| --- | --- | --- |
| T3-a | 无 usage → `tok=-` 且不含 `tok=0` | ✅（含 RED-B 红态） |
| T3-b | 有 usage → 正确 | ✅ |
| T3-c | 非流式口径不变 | ✅ |
| — | 客户端 `usage:null` 未被误改 | ✅（`sse.go:252` 未动） |

### T0

| 编号 | 条件 | 判定 |
| --- | --- | --- |
| T0-a | A1–A10 全有答案 + 证据 | ✅ |
| T0-b | 独立结论与对照分节 | ✅ |
| T0-c | 分歧清单双方依据 + 判断 | ✅ |
| T0-d | **盲析可自证** | ❌ **未达标**（实施方主动声明，未隐去） |
| T0-e | 无无证据表述 | ✅ |

**T0 总判定：降级通过。** 理由：`T0-d` 是本次最有价值的验收条件，未达成属实；
但实施方**主动声明**而非掩盖，且其 A1–A10 结论**经验收方独立复核全部成立**
（214 提交、6/6 补丁冲突、`MaxBodyMB` 阻断、4 条路由等）。故实质分析价值成立，
独立性打折属实且已如实登记。**若需真正的第二意见，须另起会话重做盲析。**

---

## 6. 与预计划的差异（Δ1–Δ9）逐条裁定

| Δ | 内容 | 验收方裁定 |
| --- | --- | --- |
| Δ1 | T3 并入 0006 单一补丁 | **接受**。工单 §7.1 已授权该可选方案；`series` 6 行、README 写清 |
| Δ2 | D1 函数名更正 | **接受并已修正我方文档** |
| Δ3 | 两个函数无 Snapshot 但安全 | **接受**，登记为查漏结论 |
| Δ4 | sed 配方修正 | **接受**；验收方实跑验证修正版可用 |
| Δ5 | identity 仅 clone 可引用 | **接受**；验收方亲历宿主 prepare 直接失败 |
| Δ6 | `python3` 是 Store 占位符，真解释器 `python` | **接受**；归因按 Windows 平台语义补全 |
| Δ7 | T2 无红态（设计如此） | **接受**。不得按 T1/T3 格式要求 T2 |
| Δ8 | M3b 仍不做 | **接受**。未实现，不得记为已覆盖 |
| Δ9 | 执行顺序偏离（T0 在实现后落笔） | **认定成立且为本次最大过程缺陷**。后果即 T0-d 未达标；已在 §5 降级处理 |

---

## 7. 遗留（不隐瞒）

1. **T0 盲析未做**——需另起会话才能获得真正独立的第二意见。当前只有"独立取证"。
2. **A4 表中 8 条安全提交未核验**（`7fad570`、`af51945`、`5f10a9c`、`a767465`、`855e5b9` 等）——
   本轮未逐条比对 diff，**不得**声称已覆盖。
3. **真实上游 WAF 行为未验证**：只证明状态机与轮转决策符合判据，不证明上游"2 号 60s 内同拦"阈值最优。
4. **`scripts/acceptance.sh` 未跑**：本轮不动镜像/Compose/密钥/持久化，按 AGENTS.md 触发条件不适用。
5. **§12.5 四项登记不修**（update 传干净 env、回退无文档、任务无超时、白名单口径）——另立工单。
6. **未推送 / 未发布镜像 / 未部署 / 未重启**：各需单独授权。

---

## 8. 验收方执行痕迹

```text
验收方本次实跑的命令（容器内）：
  overlay.py prepare --output /work/core        → rc=0
  go build ./...                                 → BUILD_OK
  go vet ./...                                   → 无输出
  go test ./...                                  → 20 包全 ok
  go test -race -count=1 ./internal/server ./internal/upstream ./internal/auth
                                                 → 3 包全 ok
  go test ./internal/server -run 'Waf' -v        → 7 个 WAF 测试 PASS
  go test ./internal/server -run 'NoUsage|StreamRow' -v → 5 个哨兵测试 PASS
  go test ./internal/upstream -run 'Classify|Waf' -v    → 4 个分类测试 PASS
  diff -rq /work/c1 /work/c2                     → IDENTICAL_MATERIALIZATION
  【对抗】删除 fail-fast break 后重跑             → calls=3 FAIL（红态复现）
验收方实跑（宿主）：
  node --test console/web_test.cjs               → 30/30 pass
  git diff --check                               → 干净
  python scripts/overlay.py prepare              → 失败（digest 不匹配，印证 D4）
```

未采信任何实施方自述数值；上表全部为验收方独立执行所得。
