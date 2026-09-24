# 交接班文档：上游安全修复回移（2026-09-22 收工 → 次日续接）

- **文档编号**：SHIFT-2026-09-22-BACKPORT
- **撰写时间**：2026-09-22 收工时
- **续接对象**：次日（新会话）的自己 / 任何接手者
- **一句话状态**：**代码与文档已完成并通过独立验收；仅有 3 个文档变更未提交。次日首选动作=审阅并提交。**

> **给新会话的自己**：先读本文 §1（现在停在哪）与 §2（下一步做什么），
> 再按需读 §4 的文档地图。**不要**重新调研已有结论——本仓文档链已经把事实、证据、
> 分歧与裁定都落盘了。

---

## 1. 现在停在哪（盘上事实，已核对）

### 1.1 提交历史（本地已提交，**未推送**）

```text
9353588  2026-09-22  docs(backport): 记录交付尖点复跑结果（同一配方在 7c94f61 全绿，补丁指纹不变）
7c94f61  2026-09-22  docs(backport): 补 0006 补丁说明、T0 复析、验证记录与进度文件
6e5a18a  2026-09-22  docs(backport): 任务书三份规划文档入仓，登记本轮实测修订
0466671  2026-09-22  security: backport WAF IP-level fail-fast breaker and usage sentinel   ← 功能提交
71fa504  2026-09-22  security(console): close proxy limiter collapse and harden exposure surface
ddba4b4  2026-09-16  chore: release images 1789550372                                      ← 二创基线
```

### 1.2 **未提交的工作区**（次日第一件事）

```text
 M docs/superpowers/plans/2026-09-22-backport-handoff.md              (+17 行)
 M docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md  (+14/-5 行)
?? docs/superpowers/verification/2026-09-22-backport-acceptance-verdict.md  (新增)
```

这三处是**验收方（我）在验收过程中做的修正**，内容已定，只差提交：

| 文件 | 改了什么 | 为什么 |
| --- | --- | --- |
| `specs/…-design.md` | 依赖链表：`76fafa6` 标注"只取 `ErrWafBlock`+`IsWafBlocked`"；`34405ca` 改标"**有意不回移**"；加 §12.4-1 修正注记；候选 A 改写 | 原表把 `34405ca` 列为必做依赖，与"只做 IP 级熔断"决策**自相矛盾**（T0 的 §12.4-1 提出，我复核确认） |
| `plans/…-handoff.md` | §3 的 14 个函数名列表加 **D1 修正表**：区分"实际插入点"与"公开 wrapper 委托" | 原列表来自补丁 `@@` 上下文头，**按字面 grep 会落空**（T0 的 D1 提出，我在容器内提取外层函数复核确认） |
| `verification/…-acceptance-verdict.md` | 新增：验收裁定书全文 | 验收结论需落盘可追溯 |

### 1.3 交付物清单（已提交，状态完好）

```text
代码（提交 0466671）：
  patches/0006-waf-ip-failfast-and-usage-sentinel.patch  121 行（只碰 client.go + handler.go）
  patches/series                                          +1 行（0001–0005 顺序未动）
  extensions/internal/server/wafip.go                     77 行（wafIPGate 状态机）
  extensions/internal/server/wafip_test.go                203 行
  extensions/internal/server/logging_usage_sentinel_test.go  44 行
  extensions/internal/upstream/credential_race_test.go    93 行（T2 对抗测试）
  extensions/internal/upstream/wafip_classify_test.go     61 行
```

### 1.4 硬约束核验（已过）

```text
git diff 71fa504..HEAD -- upstream upstream.lock   → 空（快照与锁文件零改动）
patches/                                            → 仅新增 0006 + README + series 追加一行
```

### 1.5 环境状态

| 项 | 状态 |
| --- | --- |
| Docker | **可用**（29.7.2）——`-race` 能在容器里跑 |
| 可用镜像 | `docker.m.daocloud.io/library/golang:1.23-alpine`（`registry-1.docker.io` 本机超时） |
| Go 代理 | 容器内需 `GOPROXY=https://goproxy.cn,direct`（否则拉不到 `go-redis`） |
| 宿主 `python3` | Microsoft Store 占位符（跑不了）；真解释器是 `python`（3.12.10） |
| 宿主 `overlay.py prepare` | **必失败**（`source digest does not match upstream.lock`）——须在容器内 `git clone` 后跑 |
| GitHub | 不可达（本轮到收工为止） |
| 临时痕迹 | `.build/` 已清空、`D:\work\02toolsMy\_patchtest` 已删除 |

---

## 2. 次日第一件事（按优先级）

### P0 — 审阅并提交那 3 个文档变更

```powershell
cd D:\work\02toolsMy\workbuddy2api-ui-me
git diff                                    # 看 spec 与 handoff 的修正
git status --short
# 审阅无误后：
git add docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md `
        docs/superpowers/plans/2026-09-22-backport-handoff.md `
        docs/superpowers/verification/2026-09-22-backport-acceptance-verdict.md
git commit -m "docs(backport): 验收裁定 + 按 T0 分歧修正 spec 依赖链与 handoff 函数名列表"
```

> **注意**：`git diff` 里 spec/handoff 的改动**就是验收裁定的一部分**，不是无关编辑。
> 提交信息里写明这是"按 T0 分歧修正"，保持可追溯。

### P1 — 决定是否推送

- 当前分支 `main`，领先 `origin/main` 5 个提交，**从未推送**。
- 推送需**单独授权**（AGENTS.md：更新上游、发布镜像、推送 Git、重启服务分别需要授权）。
- **本轮未发布镜像、未部署、未重启服务**——已发布镜像 `wb2api-*-1789550372` 不含本轮任何改动。

### P2 — 若继续功能工作，二选一（详见 §3）

| 选项 | 价值 | 成本 |
| --- | --- | --- |
| **A. 另起会话做 T0 真盲析** | 补上本次最大过程缺陷（T0-d 未达标） | 中：需新会话 + 严格先不读我方文档 |
| **B. 核验 A4 表 8 条未核验的安全提交** | 可能发现更多值得回移的安全修复 | 中：逐条读 diff 并与 `upstream/` + 6 补丁对齐 |

---

## 3. 遗留事项（完整，不隐瞒）

### 3.1 与本次验收直接相关

| # | 遗留 | 影响 | 建议动作 |
| --- | --- | --- | --- |
| L1 | **T0 盲析未做**（T0-d 未达标） | 本次只有"独立取证"，**不是**无污染的第二意见 | 选项 A：另起会话重做 |
| L2 | **A4 表 8 条安全提交未核验** | 不得声称已覆盖。涉及 `7fad570`（竞态/泄漏系统扫描）、`af51945`/`2b8dba0`（session GC 竞态）、`5f10a9c`（redisstore Close 竞态）、`855e5b9`（scheduler token 竞争）、`a767465`/`8058019`/`10eefa8`（session 签名）、`231a076`（sanitize 护栏）、`145220d`（Classify 429 前移）、`4ac68b7`（global chat 走 /v2 绕 WAF） | 选项 B：逐条比对 diff |
| L3 | **真实上游 WAF 行为未验证** | 只证明状态机与轮转决策符合判据，**不证明**上游"2 号 60s 内同拦"阈值最优 | 需真实上游流量才能判；离线环境做不到 |
| L4 | **`scripts/acceptance.sh` 未跑** | 本轮不动镜像/Compose/密钥/持久化，按 AGENTS.md 触发条件**不适用**；但若次日要发布镜像则必须补跑 | 发布前再跑 |

### 3.2 已登记不修（09-15 遗留，与 T1–T3 不相交，见 handoff §12.5）

| 坑 | 风险 | 为何本轮不动 |
| --- | --- | --- |
| `overlay.py update` 把宿主环境原样传给候选 `acceptance.sh`；若 shell 残留 `WB2A_ACCEPTANCE_SKIP_BUILD=true`，候选验收会指向**生产镜像**而非候选构建 | 坏快照可带"验收通过"被装上 | 本轮不跑 update；修法=update 传干净 env |
| 回退无文档：README/AGENTS 全无 rollback；`.upstream-update-backup/` 是**单代**备份且被下次 update 静默删除；崩溃残留 journal 会让**每次 prepare（含镜像构建）**失败 | 出事没法回退 | 本轮不跑 update；记为文档工单 |
| 全局无超时：`Scheduled` 在 `Run` 的 select 分支里**同步**调用，脚本子进程只绑 core 生命周期 ctx、无 deadline | 卡死子进程占死任务槽与调度主循环，页面停在"运行中"直到重启 | 属自动任务侧，与 T1 的 chat 轮转不相交 |
| `task_events.py` 的 detail 白名单只约束脚本，Go 侧 `observe` 的 detail 不受约束 | 口径不一致但无害 | 登记 |
| `travel` 零奖励记 `success`+`reward_claimed`，测试名却叫 `…RemainsUnknown` | 命名误导，非行为错误 | 登记 |

### 3.3 已排除的伪坑（**不要**再去"修"，见 handoff §12.6）

- 「错过的小时点永久丢失」→ `nextFire` 只看未来（`scheduler.go:123-135`）是**设计**，计划明确「不做停机补跑」。
- 「`next_at` 缺失应显示"已禁用"」→ `console/web/app.js:90` 的 `暂不可用 / 未知` 比计划字面**更正确**。

---

## 4. 文档地图（按需读，不要全读）

```text
docs/superpowers/
├── specs/
│   └── 2026-09-22-upstream-security-backport-design.md      【方案·需求来源】
│       读它当你要知道：为什么做这三项、边界约束、验收条件、待确认问题
├── plans/
│   ├── 2026-09-22-backport-handoff.md                       【完整工单·权威】
│   │   读它当你要知道：精确行号锚点、依赖链、陷阱、Δ1–Δ9 差异、§12.4 裁决、
│   │   §12.5 登记不修、§12.6 伪坑、§附录 A 事实索引
│   └── 2026-09-22-backport-qoder-prompt.md                  【给实施方的提示词 v3】
│   └── 2026-09-22-backport-progress.md                      【进度·Δ1–Δ9 差异表】
└── verification/
    ├── 2026-09-22-security-review-tracking.md               【上一轮控制台安全审计】
    ├── 2026-09-22-v0-vs-me-analysis.md                      【T0 独立复析·A1–A10 + D1–D5】
    ├── 2026-09-22-backport-verification.md                  【实施方验证记录·红态/绿态】
    └── 2026-09-22-backport-acceptance-verdict.md             【验收方裁定书】★未提交
```

**文档引用链是单向可追溯的**：提示词 → 工单 → 方案 → 审计记录。
顺着任一份都能回溯到需求来源。

---

## 5. 关键路径与命令（次日会用到）

### 5.1 物化 + 全量验证（容器内，**唯一可用入口**）

```powershell
cd D:\work\02toolsMy\workbuddy2api-ui-me
$env:MSYS_NO_PATHCONV=1
docker run --rm -v "D:\work\02toolsMy\workbuddy2api-ui-me:/host:ro" -w /work `
  -e HOME=/tmp -e CGO_ENABLED=1 -e GOPROXY=https://goproxy.cn,direct `
  docker.m.daocloud.io/library/golang:1.23-alpine sh -c "set -e; sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories; apk add --no-cache git python3 gcc musl-dev >/dev/null 2>&1; git clone -q /host /work/repo; cd /work/repo; python3 scripts/overlay.py prepare --output /work/core >/dev/null; cd /work/core; go build ./... && echo BUILD_OK; go vet ./...; go test ./... ; CGO_ENABLED=1 go test -race -count=1 ./internal/server ./internal/upstream ./internal/auth ./internal/pool"
```

**【陷阱】`sed` 必须带 `\?`**：漏掉会**静默不匹配**（镜像源没换但无报错）；
用 `|` 分隔在 busybox 下报 `unmatched '|'`。

**【陷阱】物化目录不会自动同步**：重跑务必用**新的**输出路径（`.build/xx-2`），
不要复用、不要删来源不明的目录。

### 5.2 宿主侧（轻量判据）

```powershell
node --test console/web_test.cjs                                      # 期望 30/30
git diff --check                                                      # 期望干净
python scripts/overlay.py identity                                    # 注意：宿主值不可引用！
docker compose --env-file /dev/null -f docker-compose.yml config --quiet   # 期望 rc=0
```

### 5.3 `patch_identity` 只在 Linux clone 内可引用（**重要**）

同一份已提交内容三个值：

```text
宿主 python scripts/overlay.py identity        7b1edc83…   ← 不可引用（CRLF + 挂载权限位）
容器只读挂载宿主工作区                            4891f37c…   ← 不可引用
容器内 git clone /host 后跑（权威）               4f6b50a4…   ← 引用这个
```

对照组：摘掉 0006 = `faba20a3…`；基线 `71fa504`（5 补丁）= `d8c08ffe…`。

---

## 6. 已知契约与语义（避免次日误判）

### 6.1 本轮**有意**与上游不同（不是回归）

| 项 | 本仓行为 | 上游行为 | 原因 |
| --- | --- | --- | --- |
| WAF 403 账号级软冷却 | **不做**，仅 IP 级止损 | `34405ca` 做了软冷却 + 指数退避 | 工单 §4.4 决策：IP 级熔断的核心是"停止轮转以止损"，不需要账号级惩罚。代价已写入 0006 测试注释 |
| `ParseRetryAfter` / `Error.RetryAfter` | **不做** | `76fafa6` 有 | 会牵动 `ChatStreamContext` 返回值签名，波及面大 |
| `/v1/stats` 端点（M3b） | **不做** | 上游有 | 本仓快照只有 4 条路由、无 `metrics.go`，实为"从零引入 324 行端点"，单列待评估 |
| auth 竞争修法 | `Snapshot()` 路线（补丁 0001/0002） | `AccessTokenValue()` 路线（`910b8b2`） | **目标重叠、实现不同**；本仓已覆盖，**不要**移植上游版（会双重实现） |

### 6.2 M3a 是**服务端日志口径** bug，不是客户端字段

- 修的：`handler.go` 流式分支无条件用零值 `0` 覆盖 `-1` 哨兵 → 日志显示 `tok=0` 而非 `tok=-`。
- **没动**：`upstream/internal/upstream/sse.go:252`（客户端 `usage` 缺失 → `null`）本来就对。

### 6.3 T2 **没有红态**（设计使然）

竞争面已由补丁 0001/0002 关闭，无法造出红态。**不要**按 T1/T3 的"红→绿"格式要求 T2——
它只能证明"关掉后不回归 + 对抗测试在场"。

---

## 7. 授权边界（不得越界）

| 动作 | 授权状态 |
| --- | --- |
| 本地提交 | ✅ 本轮已做，次日可继续 |
| **推送 Git** | ❌ **未授权**，需单独批准 |
| **发布镜像** | ❌ **未授权**；且发布≠部署 |
| **部署 / 重启真实服务** | ❌ **未授权** |
| **更新 `upstream.lock` / 整包更新上游** | ❌ **未授权**。实测代价：6 补丁全冲突 + `MaxBodyMB` 破坏性变更致扩展编译失败 |
| 重置工作区 / 清空数据 / 覆盖真实配置 | ❌ **禁止**（AGENTS.md 硬约束） |

---

## 8. 验收状态一句话总结

> **三项回移（WAF IP 级熔断 / auth 竞争验证+竞态测试 / usage 哨兵）全部通过独立验收**，
> 含 `-race` 实跑与**红态复现**（删掉 fail-fast `break` 后调用数 3≠2）。
> T0 独立复析**降级通过**（盲析前置未达标，但 A1–A10 结论经我逐条复核成立，
> 且提出 3 项我方文档缺陷均已修正）。
> **未推送、未发布、未部署。**
