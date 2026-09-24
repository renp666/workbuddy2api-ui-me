# 任务交接单：workbuddy2api-ui-me 待完成工作全量清单

- **单据编号**：TICKET-2026-09-23-FULL
- **派发给**：Trae（人工派发）
- **验收方**：需求方（AI 助手，独立复跑判据）
- **创建时间**：2026-09-23
- **前置状态**：上一轮（`0466671`）三项回移已完成并通过独立验收；L2 核验已完成，**发现 1 条真实未覆盖漏洞**

> **给实施方 Trae**：本单共 4 个任务（W1–W4），按依赖顺序排列。
> 凡标注 `【决策】` 的是需求方已拍板，不要另做设计选择。
> 凡标注 `【陷阱】` 的是实测踩过的坑，照做即可。
> **每个任务完成后停下来等验收**，不要连续做完再报（W4 除外，它是纯文档）。

---

## 0. 项目背景（必读，避免改错层）

### 0.1 三层血缘

| 层 | 仓库 | 职责 |
| --- | --- | --- |
| ① 原作者 | `Sliverkiss/workbuddy2api` | 账号池、调度器、上游客户端、WAF/IP 级防护 |
| ② 二创 | `baiyea/workbuddy2api-ui` | 增加 `console/`（Web 控制台）、`patches/`、`extensions/`、容器化 |
| ③ **本仓** | `renp666/workbuddy2api-ui-me` | 只改 `console/`、文档、仓库卫生 |

### 0.2 关键机制

- `upstream/` 是**历史快照**（`upstream.lock` 锚定 `c576b48`），**只读**，落后原作者 `master` **214 个提交**。
- 上游既有文件的**任何**修改必须走 `patches/`（`git apply` 补丁），**不能直接改 `upstream/`**。
- 独立新能力放 `extensions/`，**不能**用扩展覆盖已有上游文件。
- 物化流程：`python3 scripts/overlay.py prepare --output <新目录>` = 校验摘要 → 复制快照 → 复制 `extensions/` → 按 `patches/series` 顺序应用补丁。

### 0.3 本地路径

```text
D:\work\02toolsMy\workbuddy2api-ui-me   ← 本仓（改动目标）
D:\work\02toolsMy\workbuddy2api-ui      ← 二创参照副本（只读，不要改）
D:\work\02toolsMy\workbuddy2api_v0      ← 原作者副本（只读，可取 diff）
```

### 0.4 环境（**实测，务必照用**）

| 项 | 状态 |
| --- | --- |
| Docker | **可用**（29.7.2）。若停：`Start-Process "$env:ProgramFiles\Docker\Docker\Docker Desktop.exe"` 后等 1–3 分钟 |
| 可用镜像 | `docker.m.daocloud.io/library/golang:1.23-alpine`（`registry-1.docker.io` 本机超时） |
| Go 代理 | 容器内**必须** `GOPROXY=https://goproxy.cn,direct`（否则拉不到 `go-redis`） |
| 宿主 `python3` | **Microsoft Store 占位符，跑不了**；真解释器是 `python`（3.12.10） |
| 宿主 `overlay.py prepare` | **必失败**（`upstream source digest does not match upstream.lock`）→ 必须在容器内 `git clone` 后跑 |
| GitHub | **不可达** → 全程离线，**不要 `git fetch`** |
| 宿主 `-race` | 无 gcc → 必须进容器且 `CGO_ENABLED=1` |

**【陷阱】apk 镜像源改写的 sed 必须带转义问号**：

```sh
sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories
```

漏掉 `\?` 会**静默不匹配**（镜像源没换但无报错）；用 `|` 作分隔符在 busybox 下报 `unmatched '|'`。

**【陷阱】物化目录不会自动同步**：每次重跑用**新的**输出路径（如 `.build/w1-2`），不要复用、不要删来源不明的目录。

### 0.5 授权边界（不可越界）

| 动作 | 状态 |
| --- | --- |
| 本地提交 | ✅ 允许 |
| **推送 Git** | ❌ **禁止**（需单独授权） |
| **发布镜像** | ❌ **禁止**；发布 ≠ 部署 |
| **部署 / 重启真实服务** | ❌ **禁止** |
| **更新 `upstream.lock` / 整包更新上游** | ❌ **禁止**（实测代价：6 补丁全冲突 + `MaxBodyMB` 破坏性变更致扩展编译失败） |
| 重置工作区 / 清空数据 / 覆盖真实配置 | ❌ **禁止** |
| 修改既有补丁 0001–0006 的语义 | ❌ **禁止**（只允许新增） |

---

## 1. 任务总览

| ID | 任务 | 规模 | 依赖 | 交付物 |
| --- | --- | --- | --- | --- |
| **W1** | 回移 `2b8dba0`：session GC 关停竞态 + goroutine 泄漏 | 小（2 行 + 测试） | 无 | `patches/0007-*.patch` + 测试 |
| **W2** | 核验并决定 `7fad570` 的 `sse.go` 残留项 | 小（只读分析） | 无 | `docs/.../2026-09-23-sse-residual-check.md` |
| **W3** | 回移 `145220d`：Classify 429 前移（限流误判为硬冷却） | 小（单文件） | 无 | 并入 0007 或新增 `patches/0008` |
| **W4** | 文档收尾：更新 README 表格 + 验证记录 + 提交待提交文档 | 极小 | W1–W3 | 更新的文档 |

**执行顺序建议**：W4 先做（先提交现有 6 个未提交文档，保住产出）→ W1 → W2 → W3 → W4 收尾。
**若时间有限，优先级：W1 > W4 > W3 > W2。**

---

## 2. W1 回移 `2b8dba0`：session GC 关停竞态 + goroutine 泄漏

### 2.1 需求（**已实测确认为真实漏洞**）

上游 `2b8dba0` 修复三个同时存在的问题：

1. **数据竞争**：`StopGC` 持写锁写 `r.stop`（`session.go:97`）vs GC goroutine **每轮无锁重读** `r.stop`（`session.go:82`）。
2. **关停信号丢失**：一旦读到 `nil`，`case <-r.stop` 变成 nil channel（**永不就绪**），该 case 再也无法被选中，goroutine 只能靠 ticker 无限空转。
3. **goroutine 泄漏**：每次 Start/Stop 循环净泄漏一个 goroutine。

**本仓现状：同一个 bug 原样存在。**

```text
$ grep -n "r.stop" upstream/internal/session/session.go
74: r.stop = make(chan struct{})     ← 未捕获到局部变量
82:   case <-r.stop:               ← 每轮无锁重读共享字段
96:  close(r.stop)
97:  r.stop = nil                     ← 与上方读构成竞争
```

**生产路径确实调用**（不是死代码）：

```text
$ grep -n "StartGC\|StopGC" upstream/cmd/server/main.go
83: sessRouter.StartGC()
84: defer sessRouter.StopGC()
```

**需求方已实测复现（容器内 `-race`，对抗测试）**：

```text
WARNING: DATA RACE
Previous write at 0x00c0001b41d0 by goroutine 8:
  (*Router).StopGC()  /work/core/internal/session/session.go:97
Goroutine 24 (running) created at:
  (*Router).StartGC() /work/core/internal/session/session.go:86
==================
--- FAIL: TestZzStopGCNoLeakNoRace (0.30s)
    zz_acceptance_gc_race_test.go:20: goroutine leak: baseline=2 now=22
    testing.go:1399: race detected during execution of test
FAIL
```

### 2.2 修法（对齐上游 `2b8dba0`，**2 行**）

`upstream/internal/session/session.go` 的 `StartGC()`：

```go
// 改前（第 74 行）
r.stop = make(chan struct{})
r.mu.Unlock()

go func() {
    ...
    for {
        select {
        case <-r.stop:      // 第 82 行：无锁重读共享字段
            return
```

```go
// 改后
stop := make(chan struct{})   // 捕获到局部变量
r.stop = stop
r.mu.Unlock()

go func() {
    ...
    for {
        select {
        case <-stop:           // 只 select 局部变量
            return
```

**原理**：`close(stop)` 与 `select` 观测的是**同一个 channel**，关停必然生效、且不再触碰共享字段。

`StopGC()` 无需改动（`close(r.stop); r.stop = nil` 保持原样即可 —— 因为 goroutine 已不再读 `r.stop`）。

### 2.3 实现要求

- **新增补丁** `patches/0007-session-gc-stop-race.patch`（**不要**改 0001–0006）。
- 追加到 `patches/series` 末尾。
- **补丁必须基于本仓快照 `c576b48` 的实际文本生成**，不要照抄上游 HEAD 的 diff（行号与上下文不同，会 apply 失败）。
- 测试文件作为**新文件**放 `extensions/internal/session/session_gc_race_test.go`
  —— 这样补丁只含生产代码，更干净（`extensions/` 会自动复制到物化树）。

### 2.4 验收条件

| 编号 | 条件 | 验证方式 |
| --- | --- | --- |
| W1-a | **红态**：改动前，`-race` 报 DATA RACE + goroutine 泄漏 | 在基线（删掉 0007）上跑测试，必须 FAIL |
| W1-b | **绿态**：改动后 `-race` 通过，不泄漏 | 20 轮 Start/Stop 后 goroutine 数回落基线 |
| W1-c | 关停真的生效（goroutine 真的退出） | 测试断言 Start 后 goroutine 增长、Stop 后回落 |
| W1-d | 现有 session 测试不回归 | `go test ./internal/session/` 全绿 |
| W1-e | 补丁在物化时 `git apply --check` 通过 | `overlay.py prepare` rc=0 |
| W1-f | 全量不回归 | `go test ./...` 20 包全 ok + `go vet ./...` 无输出 |

**参考上游测试**：`git -C D:\work\02toolsMy\workbuddy2api_v0 show 2b8dba0 -- internal/session/session_test.go`
（上游新增 `TestStopGCStopsGoroutine`；**参考断言思路，不要整文件照搬**——上游测试可能引用本仓不存在的辅助函数）

---

## 3. W2 核验 `7fad570` 的 `sse.go` 残留项

### 3.1 背景

上一轮 L2 核验（见 `docs/superpowers/verification/2026-09-23-l2-security-commit-triage.md`）
把 11 个上游安全提交逐条判定，其中 **`7fad570` 拆开后有一个残留项无法判定**：

`7fad570`（[Audit] 竞态/死代码/泄漏/逻辑漏洞系统扫描，**merge commit**）触及
`internal/upstream/sse.go`，而该文件在本仓快照**存在**，且**没有任何补丁触及**。
需逐行比对上游修了什么、本仓是否已覆盖。

**额外注意**：`sse.go` 与本轮已回移的 **M3a（usage 哨兵）** 同文件，比对时须**一并检查交互**
（M3a 改的是 `handler.go` 的哨兵逻辑，但 `sse.go` 是流式帧处理，两者相邻）。

### 3.2 方法

```bash
# 1. 看上游修了什么（注意是 merge，用 --first-parent）
git -C D:\work\02toolsMy\workbuddy2api_v0 show --first-parent 7fad570 -- internal/upstream/sse.go

# 2. 看本仓快照现状
#    在物化树里看（补丁会改），不是 upstream/ 原样
#    物化命令见 §0.4
grep -n "..." /work/core/internal/upstream/sse.go
```

### 3.3 交付物

`docs/superpowers/verification/2026-09-23-sse-residual-check.md`，含：

1. 上游 `7fad570` 对 `sse.go` 的具体改动（逐 hunk 摘要）
2. 本仓现状（文件:行）
3. 判定：**已覆盖 / 不适用 / 未覆盖·建议回移 / 未覆盖·不建议回移**
4. 与 M3a 的交互说明
5. 证据：实际命令 + 实际输出

### 3.4 验收条件

| 编号 | 条件 |
| --- | --- |
| W2-a | 逐 hunk 说明上游改了什么 |
| W2-b | 四选一判定，附本仓代码位置（文件:行） |
| W2-c | 明确说明与 M3a 的交互（有无重叠/冲突） |
| W2-d | 附实际命令 + 实际输出，不接受"应该/大概" |
| W2-e | **未改任何生产代码**（只读分析 + 一份文档） |

---

## 4. W3 回移 `145220d`：Classify 429 前移

### 4.1 需求

**根因**：`Classify()` 的分类顺序里 `hardRule`（计费关键词）判在 `status==429` **之前**，
而 429 响应的 body 高频携带 `"quota exceeded"` / `"额度不足"` 等**跨计费/限流两界**的措辞
→ 限流被误判为 `ErrHardCredit`（**硬冷却到次日 04:00，白扔号约 12h**）。

**修法**：把 `status==429` 判定**前移到 `hardRule` 之前**。
状态码是比关键词更权威的信号：上游既然给了 429，就按限流语义处理（宁可短冷却自愈，不可长冷却弃号）。
真正的余额耗尽由 **402** 捕获；**非 429** 状态码的 quota 措辞仍走 `hardRule`。

### 4.2 上游改动（`145220d`，49 insertions / 20 deletions，单文件）

分类顺序从 8 层调整为 9 层，关键变化：

```text
改前顺序：0 IsModelBlocked → 1 (402/hardRule) → 2 sessionDead → 3 accountFault
          → 4 softRate → 5 status==429 → 6 404/5xx → 7 IsWafBlocked → 8 兜底

改后顺序：0 IsModelBlocked → 1 402 → 2 sessionDead → 3 accountFault
          → 4 status==429  ← 前移到这里
          → 5 hardRule      ← 降到这里（只接非 429）
          → 6 softRate → 7 404/5xx → 8 IsWafBlocked → 9 兜底
```

### 4.3 【陷阱】与已回移的补丁 0006 的交互

**补丁 0006 也改 `internal/upstream/client.go`**（新增 `ErrWafBlock` + `IsWafBlocked` +
`Classify()` 插入 WAF 分支）。

需求方已分析：**两者插入点不重叠** ——

- 0006 插的是 **403 无业务信封**分支（在 `status >= 500` 之后、内容策略/参数错误/通用 4xx 兜底之前）
- `145220d` 改的是 **429 分类位置**（在 `hardRule` 之前）

理论上可共存，但**因为同文件同函数，落地时必须实测确认**：

1. 补丁必须基于**本仓快照 + 已有 0006** 的文本生成（即相对于 0006 应用后的状态）；
2. 或把改动**并入 0006**（更简单，避免上下文耦合）。

### 4.4 【决策】实现方式二选一，推荐后者

- **候选 A**：新增 `patches/0008-classify-429-precedence.patch`
  - 优点：职责单一
  - 风险：与 0006 同文件同函数，补丁上下文耦合，后续维护易冲突
- **候选 B（推荐）**：**把改动并入 0006**，补丁名保持
  `0006-waf-ip-failfast-and-usage-sentinel.patch` 不变，在 `patches/README.md` 里补充说明
  - 优点：避免同文件双补丁叠加的上下文脆弱性；两者都是"上游分类链修复"，职责相容
  - 注意：**这不违反"禁止修改既有补丁"**——0006 是**本轮新增**的补丁，尚未被外部依赖；
    真正禁止改的是 0001–0005

**若选候选 B**，必须在 `patches/README.md` 的 0006 行里补一句说明包含了 `145220d` 的 429 前移。

### 4.5 验收条件

| 编号 | 条件 | 验证方式 |
| --- | --- | --- |
| W3-a | `Classify(429, '{"msg":"quota exceeded"}')` 返回 `ErrSoftRate`（**不是** `ErrHardCredit`） | 单测 |
| W3-b | `Classify(402, ...)` 仍返回 `ErrHardCredit`（402 仍最先判） | 单测 |
| W3-c | **非 429** + quota 措辞仍走 `hardRule` → `ErrHardCredit` | 单测（防回归） |
| W3-d | 补丁 0006 的 WAF 分类**不受影响** | 现有 `TestClassifyWaf*` 全绿 |
| W3-e | 现有 Classify 全部测试不回归 | `go test ./internal/upstream/` 全绿 |
| W3-f | 物化时 `git apply --check` 通过 | `overlay.py prepare` rc=0 |

**参考上游测试**：`git -C D:\work\02toolsMy\workbuddy2api_v0 show 145220d`（看它加的测试断言）

---

## 5. W4 文档收尾

### 5.1 先提交现有未提交的文档（**最高优先，先做**）

当前工作区有 6 个文档未提交，含**验收裁定书**与**漏洞结论**，会话中断即丢失：

```text
 M docs/superpowers/plans/2026-09-22-backport-handoff.md
 M docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md
?? docs/superpowers/plans/2026-09-22-backport-shift-handoff.md
?? docs/superpowers/plans/2026-09-22-l2-security-triage-task.md
?? docs/superpowers/verification/2026-09-22-backport-acceptance-verdict.md
?? docs/superpowers/verification/2026-09-23-l2-security-commit-triage.md
```

```powershell
cd D:\work\02toolsMy\workbuddy2api-ui-me
git add docs/
git commit -m "docs(backport): 验收裁定 + L2 安全提交核验结论 + 交接班文档"
```

> **注意**：`docs/.../2026-09-22-backport-handoff.md` 与 `...-design.md` 的改动**是验收裁定的一部分**
> （按 L2/T0 发现的分歧修正了依赖链与函数名列表），不是无关编辑。提交信息要体现这点。

### 5.2 W1/W3 完成后的文档更新

| 文件 | 更新内容 |
| --- | --- |
| `patches/README.md` | 为 0007（若新增）加一行：修改的文件 / 验证方式 / **移除条件**；若 W3 并入 0006 则补充说明 |
| `patches/series` | 追加 0007（若新增） |
| `docs/superpowers/verification/2026-09-23-w1-w3-verification.md` | 新增：W1/W3 的红态/绿态实测记录 |

### 5.3 验收条件

| 编号 | 条件 |
| --- | --- |
| W4-a | 6 个待提交文档已提交 |
| W4-b | `patches/README.md` 的 0007 行（若有）含**移除条件** |
| W4-c | 验证记录逐条贴**实际命令 + 实际输出** |
| W4-d | 明确列出**未验证项**及原因（如容器起不来则写"竞态未验证"） |

---

## 6. 通用验证清单（W1/W3 都必须实跑）

```powershell
cd D:\work\02toolsMy\workbuddy2api-ui-me
$env:MSYS_NO_PATHCONV=1
docker run --rm -v "D:\work\02toolsMy\workbuddy2api-ui-me:/host:ro" -w /work `
  -e HOME=/tmp -e CGO_ENABLED=1 -e GOPROXY=https://goproxy.cn,direct `
  docker.m.daocloud.io/library/golang:1.23-alpine sh -c "set -e; sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories; apk add --no-cache git python3 gcc musl-dev >/dev/null 2>&1; git clone -q /host /work/repo; cd /work/repo; python3 scripts/overlay.py prepare --output /work/core >/dev/null; cd /work/core; go build ./... && echo BUILD_OK; go vet ./...; go test ./... ; CGO_ENABLED=1 go test -race -count=1 ./internal/session ./internal/upstream ./internal/server"
```

宿主侧（轻量判据，必须单独跑）：

```powershell
node --test console/web_test.cjs                                          # 期望 30/30
git diff --check                                                          # 期望干净
docker compose --env-file /dev/null -f docker-compose.yml config --quiet  # 期望 rc=0
```

### 已知环境性失败（**不是回归**，不要试图修）

| 项 | 现象 | 定性 |
| --- | --- | --- |
| `python -m unittest discover -s scripts` | 4 failures + 26 errors | 环境性（`WinError 1314` 无 symlink 特权、无 `os.mkfifo`、temp 目录 ACL、无 bash、NTFS 无执行位） |
| `python -m unittest discover -s deploy` | 2 failures | 环境性（NTFS 无 POSIX 权限位、Windows 路径分隔符） |
| `gofmt -l` | 把所有文件列为未格式化 | CRLF 工作副本假阳性 |

**判据**：这些项在"改动前基线"与"改动后"必须**计数相同**，要贴两次对比。

### 【陷阱】`patch_identity` 只在 Linux clone 内可引用

同一份内容三个值，**只有容器内 `git clone /host` 后的值代表提交内容**：

```text
宿主 python scripts/overlay.py identity     7b1edc83…   ← 不可引用（CRLF + 挂载权限位）
容器只读挂载宿主工作区                         4891f37c…   ← 不可引用
容器内 git clone /host 后跑（权威）            4f6b50a4…   ← 引用这个
```

---

## 7. 交付要求

### 7.1 每个任务的交付说明必须包含

- 改了哪些文件（完整路径）
- 每个文件的变更摘要
- 每条验收条件的**实跑结果**（贴输出摘要）
- **未能验证的项目**，及原因（不得隐去）
- 与任务书的任何**偏离**，及理由
- 是否产生孤立代码

### 7.2 禁止行为

- ❌ 不许说"完成"而不贴验证输出
- ❌ 不许把环境性失败算成回归或算成通过
- ❌ 不许在容器起不来时声称 `-race` 通过
- ❌ 不许修改既有补丁 0001–0005 的语义
- ❌ 不许改 `upstream/` 或 `upstream.lock`
- ❌ 不许 `git push`、发布镜像、重启真实服务
- ❌ 不许重置工作区、清空数据、覆盖真实配置
- ❌ 不许照抄上游 HEAD 的 diff 当补丁（行号上下文不同，会 apply 失败）

---

## 8. 验收方将如何验收

验收方（需求方 AI）会**独立复跑**，不依赖你的自述：

1. `git status --short` 检查工作区是否被污染、有无未跟踪垃圾。
2. 在**全新输出路径**物化，确认所有补丁按 `series` 顺序全部 `git apply --check` 通过。
3. 独立跑 §6 全部命令，把输出与**自己的基线计数**对比。
4. **对抗测试**，重点：
   - **W1**：在**改动前基线**上复现红态（`DATA RACE` + goroutine 泄漏），证明修复真关掉了一个真实缺口；
     并自写测试验证 20 轮 Start/Stop 不泄漏。
   - **W3**：构造 `429 + "quota exceeded"` 断言返回 `ErrSoftRate`；
     构造 `402` 断言仍 `ErrHardCredit`；构造**非 429 + quota** 断言仍 `ErrHardCredit`（防过度前移）。
   - **W2**：抽查判定是否有代码位置支撑，尤其"已覆盖"结论。
5. 检查 `patches/README.md` 是否同步、移除条件是否写明。

**验收不通过的典型原因**（提前告知）：

- 只断言"测试通过"而没在基线上复现红态（无法证明修复有意义）；
- 把 `go test` 绿当成 `-race` 绿；
- 改了 0001–0005；
- 补丁基于上游 HEAD 而非本仓快照生成，导致物化失败。

---

## 9. 附：上一轮已完成的工作（**不要重做**）

| 项 | 提交 | 状态 |
| --- | --- | --- |
| WAF IP 级 fail-fast 熔断（M1） | `0466671` | ✅ 已回移并通过独立验收（含 `-race` + 红态复现） |
| auth 数据竞争验证（M2） | 补丁 0001/0002 | ✅ 已覆盖（`Snapshot()` 路线），**不要**移植 `AccessTokenValue()` |
| 流式 usage 缺失哨兵（M3a） | `0466671` | ✅ 已回移 |
| `/v1/stats` 端点（M3b） | — | ❌ **有意不做**（本仓快照只有 4 条路由，实为从零引入 324 行端点） |
| L2 核验 11 个安全提交 | — | ✅ 已完成，见 `docs/superpowers/verification/2026-09-23-l2-security-commit-triage.md` |

### 9.1 L2 核验结论速查（**避免重复劳动**）

| commit | 判定 | 处置 |
| --- | --- | --- |
| `2b8dba0` | **未覆盖·建议回移** | → **W1** |
| `855e5b9` | **已覆盖** | 补丁 0002 已用 `Snapshot()` 覆盖四处守卫，**不要重做** |
| `5f10a9c` | **不适用** | 本仓无 `Close`/`goWrite`/`sem` 机制（≠ 已覆盖） |
| `10eefa8` | **不适用** | 前提（`8058019` 机制）不存在 |
| `231a076` | **不适用** | 纯测试护栏 |
| `145220d` | 未覆盖·**候选回移** | → **W3** |
| `a767465` | 未覆盖·不建议回移 | 需真实上游现象才能判收益 |
| `8058019` | 未覆盖·不建议回移 | 同上 |
| `4ac68b7` | 未覆盖·不建议回移 | 路由策略，与 0006 机制不同，需真实上游行为 |
| `7fad570` | 部分已覆盖 / 部分不适用 | 残留 `sse.go` → **W2** |
| `af51945` | — | 是 `2b8dba0` 的 merge，不单独处理 |

### 9.2 已知过程缺陷（如实登记，不要当作待办）

- **L1 盲析未达成**：需要"未读我方结论的独立执行者"，但本机子代理不可用，
  唯一可用执行者已读过自己的结论 → **无法达成**，不得声称已完成。
- **本机子代理不可用**：实测 6 种 agent 类型（`delegate`/`scout`/`reviewer`/`researcher`/`oracle`/`worker`/`xiaoniu`）
  在 `deepseek-v4/deepseek-flash` 下**全部返回空输出**（`AGENT_EMPTY_OUTPUT`）。
  一行任务也失败 → 非提示词问题。**如需多代理协作，须先换支持工具调用的模型。**
