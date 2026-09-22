# 回移验证记录：WAF IP 级 fail-fast + usage 哨兵（2026-09-22）

对象提交：`0466671 security: backport WAF IP-level fail-fast breaker and usage sentinel`
基线提交：`71fa504 ﻿security(console): close proxy limiter collapse and harden exposure surface`
任务书：`docs/superpowers/plans/2026-09-22-backport-handoff.md`（判据 §4.5 / §5.4 / §8 / §12.3）
分析文档：`docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md`（T0）

> 本文件只登记**实际执行过**的命令与输出。未执行的（真实上游调用、镜像发布、
> `scripts/acceptance.sh`、Git 推送、服务重启）在 §7 明确列为"未做"。

## 1. 为什么在容器里跑（§8 的入口替换）

本机是 Windows：无 Go 工具链，且 `overlay.py prepare` 的源码摘要闸口按 POSIX 模式
（100644/100755/120000）计算，宿主工作区既无执行位又受 `core.autocrlf=true` 影响，
`prepare` 必失败。因此所有 Go 侧判据改在 Linux 容器内以 **`git clone /host`** 的物化副本执行
（clone 读索引 → LF + 执行位齐全），命令内容与 §8 一致，只是入口换了。

```text
镜像   docker.m.daocloud.io/library/golang:1.23-alpine
工具   git 2.49.1 ｜ Python 3.12.14 ｜ gcc (Alpine 14.2.0)
挂载   -v <本仓>:/host:ro   -v wb2a-gomod:/go/pkg/mod
环境   GOPROXY=https://goproxy.cn,direct   CGO_ENABLED=1（-race 需要）
配方   clone → python3 scripts/overlay.py prepare --output /work/core → go test/vet/-race
```

`linux/amd64` 与发布架构一致，不是交叉环境。

## 2. 基线（改动前，HEAD `71fa504`）

```text
### stage3 prepare (digest gate)
prepare_rc=0
d8c08ffe3ac3fafc02581358bdf664a90e48b5ab455a754f291c7bb966d6fb0e   ← 5 补丁时的 identity
lock commit     = c576b489fa22e3c156e960ee6336c4e653a0d95c
lock source_sha = 299e722b5dce1b17273383ebaedb277e8f2a11cce3a219383c9d4cc527e5aafb
### stage4 go test ./...     → 20 个包全 ok（scheduler 24.2s、server 10.1s）
### stage5 go vet ./...      → 无输出
### stage6 race (key pkgs)   → auth 1.029s pool 1.299s upstream 1.560s server 2.892s scheduler 4.137s 全 ok
### stage7 console           → ok workbuddy2api-console 1.173s
```

基线全绿 ⇒ 后面的红/绿只可能由本次改动引起。

## 3. 红态（先证明缺口真实存在）

### RED-A：同一棵树，把 0006 从 `series` 摘掉（测试在场、生产代码不在场）→ 编译期红

```text
series now: [... '0005-regression-tests.patch']
# workbuddy2api/internal/upstream
vet: internal/upstream/wafip_classify_test.go:16:58: undefined: ErrWafBlock
# workbuddy2api/internal/server
vet: internal/server/wafip_test.go:125:8: h.wafIP undefined (type *Handler has no field or method wafIP)
```

### RED-B：连 WAF 扩展文件一起摘掉，只留 T3 测试 → 行为红（本仓真 bug）

```text
=== RUN   TestChatLogsStreamRowNoUsageShowsDash
    logging_usage_sentinel_test.go:39: 流式无 usage 应显示 tok=-（未观测哨兵），实际:
        | #001 | 11:20:18 | glm-5.2 | stream | 200 | uid=u1 | TTFB=0ms | tok=0 | 0.0tok/s | total=0.0s |
    logging_usage_sentinel_test.go:42: 流式无 usage 被伪造成 tok=0（伪装成测得 0 token）
--- FAIL: TestChatLogsStreamRowNoUsageShowsDash (0.00s)
```

`tok=0 | 0.0tok/s` 即 §5.1 描述的"没观测到 usage 被伪装成测得 0"。夹具 `sseNoUsage`
与上游 `b1f7bc8` 的版本按字节比对一致后才使用。

## 4. 绿态（HEAD `0466671`，6 个补丁全部 `git apply --check` 通过）

> **交付尖点复跑**：C2/C3 只动文档之后，同一配方在 **HEAD `7c94f61`** 完整重跑一次
> （`prepare_rc=0`、`go test ./...` 20 包全 ok、`vet` 无输出、
> `-race` auth 1.027s / pool 1.284s / upstream 1.562s / server 2.027s / scheduler 3.027s 全 ok、
> `console -race` ok 1.178s），`identity` 仍是 `4f6b50a4…`——文档提交不改补丁栈指纹。
> 修复后的 `base.sh`（apk 源改写用 `#` 分隔 + `\?`）也在这一跑里得到验证：
> 容器内 `upstream/*.sh` 执行位为 `-rwxr-xr-x`，摘要门照常通过。

定向判据（任务书编号）：

```text
--- PASS: TestChatStatsReaderNoUsage / TestChatStatsReaderCreditNoUsage          (T3-b/T3-c 口径)
--- PASS: TestLogChatRowNoUsageShowsDash / TestChatLogsStreamRowNoUsageShowsDash (T3-a)
--- PASS: TestWafIPGateMultiAccountTriggers   (T1-a 窗内两号激活 / T1-b 单号反复恒不触发)
--- PASS: TestWafIPGateWindowExpiry           (T1-c 过期解除 + 激活不续期)
--- PASS: TestWafIPGateConcurrent             (T1-e 并发混发，配合 -race)
--- PASS: TestChatWafIPFailFastStopsRotation  (T1-d 上游调用数=2 而非 3；u3 零调用)
--- PASS: TestChatWafSingleAccountStillRotates(单号 WAF 仍换到健康号，200)
--- PASS: TestChatWafIPGateClearsAfterExpiryThenRotates (过期后恢复轮转)
--- PASS: TestClassifyWafBlockShape / TestClassifyWafEnvelopeKeepsExistingKind (T1-g 11140 仍 ErrAccountFault)
--- PASS: TestErrWafBlockStringAndOrdering
```

```text
### GREEN: -race on the touched packages
ok workbuddy2api/internal/server 2.082s ｜ upstream 1.568s ｜ auth 1.026s ｜ pool 1.273s
### GREEN: full suite + vet          → 20 个包全 ok（含 internal/scheduler 1.808s）
### console (独立模块)               → ok workbuddy2api-console 1.170s（-race）
```

T1-f（`applyErrorPolicy` 现有测试不回归）由全量 `internal/server` 绿覆盖；
本轮**有意不引入**账号级 WAF 软冷却，因此 `wafip_test.go` 断言的是"WAF 403 不冷却账号"，
与上游测试口径的这处差异写在测试注释里，不是回归。

## 5. T2 查漏（竞争面 + 自锁死锁陷阱）

### 5.1 函数级扫描：首次凭据读 vs `Snapshot()` 位置

对物化树 `internal/upstream/*.go` 逐函数比较"第一次读凭据字段"与"`a = a.Snapshot()`"的行号：

| 判定 | 函数 |
| --- | --- |
| 快照先于读（OK） | `ChatStreamContext` `FetchModels` `globalModelsOnce` `ChatHeaders` `BillingHeaders` `RefreshHeaders` `billingJSONContext` `growthJSON` |
| 无自身快照（复核后安全） | `globalOn` `originRefererFor` `defaultWorkBuddyUAFor` `acceptLanguageFor` `injectGlobalChatHeaders` `ReportChatActivity` `ClaimTrialContext` `RefreshToken` |

后一组的逐条理由（全部有源码依据）：

- `RefreshToken`：读 `AccessToken`/`RefreshToken` 的位置在 `a.Lock(); defer a.Unlock()` 区内
  （物化 `client.go:714-735`），是写回前的一致性校验，本就该持锁。
- `globalOn` / 四个 header 助手：只做 `a.Realm()` / `a.IsGlobal()`，而 `Realm()`/`RealmStored()`/
  `NeedsRefresh()`/`BackfillRealm()` 自身 `a.mu.Lock()`；助手又从已 `Snapshot()` 的入口点调用（参数是私有副本）。
- `ReportChatActivity` 读 `a.UID`，`ClaimTrialContext` 读 `a.Realm()`：本仓唯一的**原地写者**是
  `RefreshToken` 锁内的 4 个字段（`AccessToken`/`RefreshToken`/`Domain`/`ExpiresAt`），
  `UID` 从不原地改写 → 不构成竞争。`DeviceToken` 同理（只有构造/替换时赋值，无 in-place 写）。

### 5.2 自锁死锁（§3 陷阱）

- `patches/0001` 的 `Snapshot()` 按字段写（`realm: a.realm`），不调 `Realm()` → 不自锁。
- 全仓持锁点复验：`client.go:714`（`RefreshToken` 写回段，区域内无锁方法调用）、
  `auth.go` 内 10 处 `a.mu.Lock()`（`Lock/Unlock` 定义、`Snapshot`、`PendingActivation`、
  `CompleteActivation`、`Realm`、`BackfillRealm`、`RealmStored`、`NeedsRefresh`、`SaveAtomic`、`ReplaceAndSave`）。
- 唯一的 `BackfillRealm()` 调用点在 `LoadDir`（`auth.go:349`），该函数不持 `a.mu` → 无自锁路径。
- 上游为死锁拆出的 `realmLocked` 在本仓快照中不存在（实测 `defined: False`），因为本仓没有持锁调 `Realm()` 的路径。

### 5.3 对抗性竞态回归（新增，不改生产代码）

`extensions/internal/upstream/credential_race_test.go` 的
`TestOutboundHeadersRaceRefreshToken`：一个 goroutine 持续 `RefreshToken(a)`，
另两个持续 `ChatHeaders(...)` / `BillingHeaders(...)`，并先断言"刷新至少成功一次"
（否则竞争面为空、测试是假绿）。`-race` 下通过。

## 6. 宿主侧检查（§8.1 的非 Go 部分）

| 命令 | 结果 |
| --- | --- |
| `node --test console/web_test.cjs` | 30 tests / 30 pass / 0 fail（164.9ms） |
| `docker compose --env-file /dev/null -f docker-compose.yml config --quiet` | rc=0，无输出 |
| `git diff --check` / `git diff --check HEAD` | 均干净（无空白/裸 CR 混入） |
| `python -m unittest discover -s scripts -p 'test_*.py'` | Ran 43，FAILED (failures=4, errors=26) — **全部环境性**，见下 |
| `python -m unittest discover -s deploy -p 'test_*.py'` | Ran 13，FAILED (failures=2) — 同为环境性 |
| `python scripts/overlay.py identity`（宿主） | `7b1edc83…` — **不可引用**，见 §6.1 |

环境性失败的实际根因（逐条读 traceback，不是"python3 是 Store 占位符"这一条能概括的）：

```text
OSError [WinError 1314] 客户端没有所需的特权   → 建 symlink（5 例）
AttributeError: module 'os' has no attribute 'mkfifo' → POSIX 设备节点
PermissionError [Errno 13]/[WinError 5]        → temp 目录 ACL / .git objects
subprocess.CalledProcessError: ['bash','-n', ...] / WSL execvpe(/bin/bash) → 无 bash
FAIL ..._tracks_content_and_executable_mode    → NTFS 无执行位
```

按 §9.3「不许把环境性失败算成回归或算成通过」：这里既不计回归，也不计通过，
POSIX 语义侧（执行位/symlink/摘要闸口）的等价验证由 §1–§4 的 Linux 容器路径承担。

### 6.1 `patch_identity` 只在 Linux clone 内可引用

同一份已提交内容，三种取法三个值：

```text
宿主 python scripts/overlay.py identity                 7b1edc83f45cec95…
容器只读挂载宿主工作区跑                                  4891f37c8874b4d9…
容器内 git clone /host 后跑（权威）  0001–0006            4f6b50a444de5fd76c8787aa395696343cb611beec8d6c1cc0e3d8d118373a70
同上，仅把 0006 从 series 摘掉（对照组）                   faba20a3c2d575f494ed9db0f5d559e866a85997804bd1cd9889cbf2a53d4670
基线 HEAD 71fa504（5 补丁，clone 路径）                    d8c08ffe3ac3fafc02581358bdf664a90e48b5ab455a754f291c7bb966d6fb0e
```

根因：`overlay_identity()` = `sha256(source_digest(extensions))` ⊕ `series` 字节 ⊕ 各补丁字节，
而 `source_digest` 编码 POSIX 模式；宿主工作区是 CRLF（实测 `patches/series` 6/7 行为 CRLF、
`upstream/.../client.go` 全文 CRLF）且 Docker 挂载把权限位报成可执行。索引里一律 LF，
所以**只有 clone 路径的值代表提交内容**。clone 内 `patches/` 与 `extensions/` 实测 LF only，
扩展文件模式全 `100644`。

## 7. 本轮没做（不得被理解为已做）

- 未 `git push`、未发布镜像、未部署、未重启任何真实服务（各自需单独授权）。
- 未跑 `bash scripts/acceptance.sh`：本轮不动镜像/Compose/密钥/持久化，按 AGENTS.md 的触发条件不适用。
- 未改 `upstream/`、`upstream.lock`，未改既有 0001–0005 的语义（`patches/` 只新增 0006 与 `series` 追加一行）。
- 未做真实上游 WAF 调用：只证明状态机与轮转决策符合 T1 判据，不证明上游阈值最优。
- 未引入 `rotateBackoff` / `ParseRetryAfter` / 账号级 WAF 软冷却 / `/v1/stats`（§6 与 §6.1 决策）。
- T0 的盲析前置未达成（`2026-09-22-v0-vs-me-analysis.md` §0 已声明）。
