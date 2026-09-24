# W1–W3 上游安全回移验证记录

- 日期：2026-09-24
- 任务来源：`docs/superpowers/plans/2026-09-23-trae-task-ticket.md`（TICKET-2026-09-23-FULL）
- 交付模式：按用户指令「不逐项验收，连续跑完 W2/W3/W4 后统一测试」，本文档汇总 W1（已提交 `f3ca86f`）与 W2/W3（本轮）的实跑证据。
- 最终补丁身份（容器内 `git clone /host` 权威值，含新 0006 + 新测试）：
  `9bf2dba80f5f427ff090b388fef078f99247c65b8f9ecc7f4cbbd40ac534882d`

## 0. 环境（实测）

| 项 | 值 |
| --- | --- |
| Docker | 29.7.2，镜像 `docker.m.daocloud.io/library/golang:1.23-alpine` |
| 容器配方 | `git clone /host` 后跑 `overlay.py prepare`（宿主 prepare 必失败：CRLF digest） |
| apk 源改写 | `sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g'`（带转义问号） |
| Go 代理 | `GOPROXY=https://goproxy.cn,direct`，`CGO_ENABLED=1` |
| 宿主 shell | PowerShell 5.1（无 `&&`、无 heredoc，`;` 分隔） |

脚本：`.build/w1-red.sh`、`.build/w1-green.sh`、`.build/w3-red.sh`、`.build/w3-green.sh`、`.build/w4-host.sh`、`.build/w4-fmt.sh`（`.build/` 已 gitignore）。

---

## 1. W1 回移 `2b8dba0`：session GC 关停竞态（补丁 0007，已提交 f3ca86f）

### 1.1 红态（基线：series 删 0007，保留新测试）

命令：`docker run ... sh /host/.build/w1-red.sh`

```text
=== RED: strip 0007 from series, keep the new test ===
--- confirm buggy read still present (case <-r.stop) ---
82:                     case <-r.stop:
RED_RC=1
=== DATA RACE count ===
3
=== relevant lines ===
WARNING: DATA RACE
--- FAIL: TestStopGCStopsGoroutine (3.05s)
    session_gc_race_test.go:53: StopGC 后 goroutine 未退出（泄漏）: baseline=2 now=11 rounds=20
WARNING: DATA RACE
WARNING: DATA RACE
--- FAIL: TestStartGCIdempotentAndRestartable (0.03s)
FAIL
FAIL    workbuddy2api/internal/session  3.107s
FAIL
```

判据：W1-a 满足（DATA RACE×3 + goroutine 泄漏 baseline=2→now=11，20 轮 Start/Stop 净泄漏）。

### 1.2 绿态（完整 series 0001–0007）

命令：`docker run ... sh /host/.build/w1-green.sh`

```text
=== GREEN: full series with 0007 ===
PREPARE_RC=0
--- confirm fix applied (case <-stop:) ---
90:                     case <-stop:
=== W1-b/c: race tests on session package ===
RACE_RC=0
=== RUN   TestStopGCStopsGoroutine
--- PASS: TestStopGCStopsGoroutine (0.05s)
=== RUN   TestStartGCIdempotentAndRestartable
--- PASS: TestStartGCIdempotentAndRestartable (0.03s)
PASS
ok      workbuddy2api/internal/session  1.096s
0
DATA_RACE_COUNT=0
=== W1-d: full session package ===
ok      workbuddy2api/internal/session  0.124s
SESSION_RC=0
=== W1-f: go build + vet + full test ===
BUILD_OK
VET_OK
（20 个包全 ok：cmd/activity, cmd/credit, cmd/login, cmd/server, cmd/signin,
 cmd/trial, internal/anthropic, internal/auth, internal/bridge, internal/config,
 internal/logfmt, internal/oauth, internal/pool, internal/prompt, internal/redisstore,
 internal/scheduler, internal/server, internal/session, internal/taskrun, internal/upstream）
ALLTEST_RC=0
```

判据：W1-b/c/d/e/f 全满足。

---

## 2. W2 核验 `7fad570` 的 `sse.go` 残留项（只读分析）

交付物：`docs/superpowers/verification/2026-09-23-sse-residual-check.md`。

- W2-a：逐 hunk 摘要（1 个 hunk，`Aggregate` 非流式 message 分支补 `gotAnyContent = true`，4+/1-）。
- W2-b：判定 **未覆盖·建议回移**，本仓代码位置 `upstream/internal/upstream/sse.go:102`（message 分支无 latch，与上游修复前逐字一致）。
- W2-c：与 M3a 无重叠无冲突（M3a 在 `handler.go` 流式 usage 哨兵；本 hunk 在 `Aggregate` 非流式路径）。
- W2-d：附实际命令 + 实际输出（见该文档 §5 证据链）。
- W2-e：**未改任何生产代码**（`git status` 仅新增该文档）。

---

## 3. W3 回移 `145220d`：Classify 429 前移（并入补丁 0006）

【决策】按任务书候选 B 并入 0006，补丁文件名不变；`patches/README.md` 0006 行补 429 前移说明。
测试作为新文件放 `extensions/internal/upstream/classify_429_test.go`（不改上游测试文件）。

### 3.1 红态（当前 0006 无 429 前移 + 新测试）

命令：`docker run ... sh /host/.build/w3-red.sh`

```text
=== RED: materialize with CURRENT 0006 (no 429 precedence) + new ext tests ===
PREPARE_RC=0
--- confirm hardRule BEFORE status==429 (unfixed order) ---
340:    if hardRule.hit(body, lower) {
352:    if status == http.StatusTooManyRequests {
=== W3 red assertions: new tests on unfixed baseline (expect FAIL on 429-quota) ===
RED_TEST_RC=1
--- FAIL: TestClassify429QuotaIsSoftRate (0.00s)
--- PASS: TestClassify402StillFirst (0.00s)
--- PASS: TestClassifyNon429QuotaKeepsHardCredit (0.00s)
--- PASS: TestClassify429AccountFaultPrecedence (0.00s)
--- PASS: TestClassify429RateLimitTextUnchanged (0.00s)
FAIL
=== W3-d red: existing WAF tests still pass on baseline ===
RED_WAF_RC=0
ok      workbuddy2api/internal/upstream 0.006s
=== existing upstream package tests on baseline (must be all ok) ===
RED_PKG_RC=1
FAIL    workbuddy2api/internal/upstream 0.560s
```

判据：红态成立——`TestClassify429QuotaIsSoftRate` 在未修复基线 FAIL（429+quota 被误判 hard_credit），
其余 4 条防回归用例基线即 PASS（符合预期，它们断言的是「不变」的语义）；
既有 WAF 分类测试基线全绿（`RED_WAF_RC=0`），既有 `TestClassify` 表用例基线全绿。

### 3.2 补丁重生成（关键工程点）

`145220d` 的 429 前移**不能照抄上游 HEAD diff**（本仓快照 + 0006 已有 WAF 改动，行号/上下文不同）。
做法（`.build/w3-green.sh` STEP1–5）：

1. 物化树 A = 当前 series（旧 0006 已应用）；
2. 物化树 B = series 删 0006/0007（pre-0006 状态，即纯快照）；
3. 对树 A 的 `client.go` 应用 W3 编辑（`.build/w3-edit.py`，三处精确替换，count==1 校验）；
4. `git diff`（容器内，tab 保留）生成 `B→A` 的 client.go 补丁段，拼上原 0006 的 handler.go 段，得到新 0006；
5. 用新 0006 回写 clone，`overlay.py prepare` 全 series 通过。

**tab 陷阱**：首版用 busybox `diff -u` 生成，把 Go 源码 tab 展开成 8 空格，污染补丁缩进
（`git diff --check` 报 `space before tab`，且物化后 client.go 缩进错乱）。改用容器内
`git init` + `git diff` 重生成，`NO_SPACE_INDENT_ADDED_LINES` 确认新增行保留 tab。

### 3.3 绿态（新 0006 并入 429 前移）

命令：`docker run ... sh /host/.build/w3-green.sh`

```text
=== STEP1: materialize tree A (current series 0001-0007) ===
PREPARE_A_RC=0
=== STEP2: materialize tree B (series minus 0006/0007 = pre-0006 state) ===
PREPARE_B_RC=0
=== STEP3: apply W3 edit to tree A client.go ===
EDIT_OK
=== STEP4: regenerate 0006 = original handler.go part + new client.go diff (git diff, tab-preserving) ===
REGEN_DONE
HUNK_COUNT=8
--- tab check: added code lines must start with +TAB, not +spaces ---
NO_SPACE_INDENT_ADDED_LINES
=== STEP5: install new 0006 into repo clone, full prepare must pass ===
PREPARE_GREEN_RC=0
--- confirm new order: 429 before hardRule ---
360:    if status == http.StatusTooManyRequests {
363:    if hardRule.hit(body, lower) {
=== STEP6: W3-a/b/c green tests ===
W3_TEST_RC=0
--- PASS: TestClassify429QuotaIsSoftRate (0.00s)
--- PASS: TestClassify402StillFirst (0.00s)
--- PASS: TestClassifyNon429QuotaKeepsHardCredit (0.00s)
--- PASS: TestClassify429AccountFaultPrecedence (0.00s)
--- PASS: TestClassify429RateLimitTextUnchanged (0.00s)
ok      workbuddy2api/internal/upstream 0.012s
=== STEP7: W3-d WAF tests + W3-e full upstream package ===
WAF_RC=0
ok      workbuddy2api/internal/upstream 0.005s
UPSTREAM_PKG_RC=0
ok      workbuddy2api/internal/upstream 0.505s
=== STEP8: build + vet + full test ===
BUILD_OK
VET_OK
ALLTEST_RC=0
OK_PKG_COUNT=20
=== STEP9: race on upstream+server ===
RACE_RC=0
0
DATA_RACE_COUNT=0
ok      workbuddy2api/internal/upstream 1.598s
ok      workbuddy2api/internal/server   14.660s
```

判据：W3-a/b/c/d/e/f 全满足。

### 3.4 最终提交态全量复跑（含新 0006 + 新测试）

命令：`docker run ... sh /host/.build/w4-host.sh`

```text
=== patch_identity (authoritative, inside Linux clone, final state) ===
9bf2dba80f5f427ff090b388fef078f99247c65b8f9ecc7f4cbbd40ac534882d
=== final materialize + full build/vet/test ===
PREPARE_RC=0
BUILD_OK
VET_OK
ALLTEST_RC=0
OK_PKG_COUNT=20
=== upstream+session race (both W1/W3 touched pkgs) ===
RACE_RC=0
0
DATA_RACE_COUNT=0
ok      workbuddy2api/internal/upstream 1.629s
ok      workbuddy2api/internal/session  1.146s
ok      workbuddy2api/internal/server   2.138s
```

---

## 4. 宿主侧轻量判据（任务书 §6）

| 判据 | 命令 | 结果 |
| --- | --- | --- |
| console 页面测试 | `node --test console/web_test.cjs` | **30/30 pass，0 fail** |
| compose 配置合法 | `docker compose --env-file NUL -f docker-compose.yml config --quiet` | **rc=0** |
| 源码空白 | `git diff --check`（工作树） | **rc=0**（无真实空白问题） |
| gofmt | 容器内 `gofmt -l` 新测试文件 + 物化后 client.go/session.go | **空（干净）** |

### 4.1 `git diff --check` 对 `.patch` 的告警说明（**非回归，既有状态**）

`git diff --check` 扫描 `patches/*.patch` 时报 `space before tab in indent` / `trailing whitespace`。
经核：这是**补丁文件固有的 git 标准格式**——unified diff 的上下文行本就是「空格 + 原文件 tab」，
被当成源码扫描时误报。**原始 0006（提交 `0466671`）与 0007（提交 `f3ca86f`）在各自提交时同样触发**
（实测 `git diff --check 0466671~1 0466671` 与 `git diff --check f3ca86f~1 f3ca86f` 均 rc=2、
报同类行），因此本轮并入 0006 的新 client.go 段沿用同一格式，未引入新类型问题。
真实源文件（`patches/README.md`、新测试 `.go`）经 `git diff --cached --check` 与 python 字节校验：无 CRLF、无行尾空格、tab 缩进正确。

---

## 5. 已知环境性失败（**非回归，未修**）

任务书 §6 登记的宿主环境性失败本轮未重跑（宿主 `python3` 是 Store 占位符、无 bash、无 symlink 特权），
其定性不变：`scripts`/`deploy` 的 unittest 失败为 Windows 环境性（`WinError 1314`、无 `os.mkfifo`、
NTFS 无 POSIX 权限位/执行位、Windows 路径分隔符），与本次补丁改动无关。Go 侧全部判据在容器内实跑通过。

## 6. 未验证项（如实登记）

| 项 | 状态 | 原因 |
| --- | --- | --- |
| 真实上游 429+quota 响应到账行为 | 未验证 | 任务书禁止接触真实凭据/上游；仅单测覆盖分类逻辑 |
| 真实上游 WAF 403 拦截页 | 未验证 | 同上，WAF 分类为 mock 单测 |
| `scripts/check.sh` 全量 | 未跑 | 依赖宿主 bash/python3（环境受限）；其覆盖的 Go/Node/容器判据已在容器内逐项实跑 |
| `scripts/acceptance.sh` 容器隔离验收 | 未跑 | 本轮为纯补丁/分类逻辑改动，不涉镜像、Compose、启动参数、密钥或持久化；AGENTS 约定「涉及镜像/Compose/密钥时才需」acceptance |
| W2 建议的 `sse.go` 回移实施 | 未做 | 任务书 W2 授权范围仅只读核验，回移未授权 |

## 7. 与任务书的偏离

- **执行节奏**：任务书 §前言要求「每任务停下等验收」，本轮按用户新指令连续跑完 W2/W3/W4 后统一交付。
- **W3 实现方式**：选任务书候选 B（并入 0006），非候选 A（新增 0008）；补丁文件名保持
  `0006-waf-ip-failfast-and-usage-sentinel.patch` 不变。
- **补丁重生成方法**：任务书未规定如何安全重生成含 tab 的 Go 补丁；实测 busybox `diff` 破坏缩进，
  改用容器内 `git diff` 生成（见 §3.2），这是对任务书「基于 0006 应用后的文本重生成」的落地细化。
- 无孤立代码：新测试文件随扩展物化进入 `internal/upstream`，被 `go test` 执行。

## 8. 交付物清单

| 文件 | 变更 |
| --- | --- |
| `patches/0006-waf-ip-failfast-and-usage-sentinel.patch` | 并入 `145220d` 429 前移（client.go 段重生成，tab 保留） |
| `extensions/internal/upstream/classify_429_test.go` | 新增 W3 回归测试（5 个函数） |
| `patches/README.md` | 0006 行补 429 前移说明 + 测试名；新增 0007 行（含移除条件）；series 顺序补 0007；0006/0007 移除条件段落 |
| `docs/superpowers/verification/2026-09-23-sse-residual-check.md` | W2 只读核验结论 |
| `docs/superpowers/verification/2026-09-23-w1-w3-verification.md` | 本记录 |
