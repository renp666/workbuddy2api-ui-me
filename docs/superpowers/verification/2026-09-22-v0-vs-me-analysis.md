# T0 独立复析：v0（原作者）与 me（本仓）的差异与可更新性

日期：2026-09-22 ｜ 任务来源：`docs/superpowers/plans/2026-09-22-backport-handoff.md` §3.5
取证方式：三个本地克隆离线取证的命令 + 实际输出（GitHub 不可达，未执行任何 `fetch`）

## 0. 前置条件声明（必须先读）

**§3.5.2 的盲析前置条件没有满足，本文件不声称它是盲析产物。**

实施顺序上我在本文档落笔之前已经读过交接文档的 §3 / §4.4 / §5 / 附录 A 与
`2026-09-22-security-review-tracking.md`（它们是本任务的输入，且会话在中途被压缩过，
无法回退到"未读"状态）。因此：

| 验收条件 | 结论 |
| --- | --- |
| T0-a A1–A10 全部有答案，每条附实际命令 + 实际输出 | 满足（本文 §1） |
| T0-b 独立结论与对照结果分节存放且标注 | 满足（§1 与 §2 分开） |
| T0-c 分歧清单逐条给双方依据 + 判断 | 满足（§3） |
| **T0-d 盲析部分可自证** | **不满足**，无法自证未受污染 |
| T0-e 无"文档说/应该/大概"的无证据表述 | 满足；未核验项集中在 §4 |

所以 §1 的准确定位是「**独立取证结论**」：结论本身由我自己跑的命令支撑、不引用对方文档作为证据，
但它**不是**无污染的第二意见。若需求方需要真正的独立复核，应另起一次会话，
在读取本文件之前完成盲析。

## 1. 独立取证结论（A1–A10）

### A1 三个仓库的 HEAD 与血缘

```text
$ git -C D:\work\02toolsMy\workbuddy2api_v0   log -1 --format='%h %ad %s' --date=short
b71d734 2026-09-22 feat(README): with community group and acknowledgments
origin  https://github.com/Sliverkiss/workbuddy2api.git      ← 原作者（v0）

$ git -C D:\work\02toolsMy\workbuddy2api-ui  log -1 --format='%h %ad %s' --date=short
ddba4b4 2026-09-16 chore: release images 1789550372
origin  https://github.com/baiyea/workbuddy2api-ui.git       ← 二创

$ git -C D:\work\02toolsMy\workbuddy2api-ui-me log -1 --format='%h %ad %s' --date=short
0466671 2026-09-22 security: backport WAF IP-level fail-fast breaker and usage sentinel
origin   https://github.com/renp666/workbuddy2api-me
upstream https://github.com/baiyea/workbuddy2api-ui.git     ← 本仓（三创）
```

me 相对二创的位置：

```text
$ git merge-base HEAD upstream/main
ddba4b4 2026-09-16 chore: release images 1789550372
$ git rev-list --left-right --count upstream/main...HEAD
0	2
```

**结论**：me = 二创 `ddba4b4` + 2 个提交（`71fa504` 控制台安全加固、`0466671` 本轮回移），
不落后二创。血缘的另一半用祖先关系实测：

```text
$ git -C workbuddy2api-ui merge-base --is-ancestor c576b48… HEAD && echo yes
c576b48 IS ancestor of 二创 HEAD      # 二创 HEAD 共 319 个提交
```

即：二创继承原作者历史（含本仓快照基点 `c576b48`），me 继承二创。

### A2 快照锚点与落后量

```text
$ cat upstream.lock
{ "format": 1,
  "repository": "https://github.com/Sliverkiss/workbuddy2api",
  "commit": "c576b489fa22e3c156e960ee6336c4e653a0d95c",
  "source_sha256": "299e722b5dce1b17273383ebaedb277e8f2a11cce3a219383c9d4cc527e5aafb" }

$ git -C workbuddy2api_v0 rev-list --count c576b48..HEAD
214
$ git -C workbuddy2api_v0 log -1 --format='%ad' --date=short c576b48
2026-09-15
```

**结论**：快照基点 2026-09-15，落后 214 个提交 / 7 天。

### A3 本仓相对二创改了什么

```text
$ git diff --stat ddba4b4..HEAD
 .gitattributes                                     |  21 ++
 AGENTS.md                                          |  12 +-
 README.md                                          |  13 +-
 console/main.go                                    |  30 +-
 console/main_test.go                               |  24 ++
 console/server.go                                  |  66 ++++-
 console/server_test.go                             |  94 ++++++
 console/web/app.js                                 |   6 +-
 console/web_test.cjs                               |  15 +
 .../2026-09-22-security-review-tracking.md         | 328 +++++++++++++++++++++
 extensions/internal/server/logging_usage_sentinel_test.go |  44 +++
 extensions/internal/server/wafip.go                |  77 +++++
 extensions/internal/server/wafip_test.go           | 203 +++++++++++++
 extensions/internal/upstream/credential_race_test.go     |  93 ++++++
 extensions/internal/upstream/wafip_classify_test.go      |  61 ++++
 patches/0006-waf-ip-failfast-and-usage-sentinel.patch    | 121 ++++++++
 patches/series                                     |   1 +
 17 files changed, 1195 insertions(+), 14 deletions(-)
```

**结论**：两个提交各管一件事——`71fa504` 只动 `console/`（三创本来的边界），
`0466671` 只动 `patches/` + `extensions/`（上游层的正确入口）。`upstream/`、`upstream.lock`
零改动，与 AGENTS.md 的三层血缘约束一致。

### A4 原作者 214 个提交里安全相关的是哪些

按路径统计（`git rev-list --count c576b48..HEAD -- <path>`）：
`internal/upstream` 67、`internal/server` 42、`cmd` 22、`internal/scheduler` 13、
`internal/session` 7、`internal/auth` 5、`console`/`extensions` 0（上游无这两个目录）。

从标题关键字（waf/竞态/锁/限流/冷却/签名/token/校验/脱敏…）筛出的安全向提交，
以及**本仓当前实际状态**（只对我在本会话内核验过的三项作断言，其余一律标"未核验"）：

| commit | 日期 | 标题要点 | 本仓状态 |
| --- | --- | --- | --- |
| `76fafa6` | 09-16 | `ErrWafBlock` 分类 + Retry-After 头族解析 | 部分回移：只取 `ErrWafBlock` 分类，**不含** Retry-After 族（§6 不做 `rotateBackoff`） |
| `8825c4c` | 09-16 | WAF IP 级 fail-fast——短窗多号同拦跳过轮转 | 本轮回移（补丁 0006 + `extensions/internal/server/wafip.go`） |
| `34405ca` | 09-16 | WAF 403 软冷却 + 轮转指数退避 | **有意不回移**（§6 决策）；因此本仓 WAF 403 不冷却账号，测试注释已标注该差异 |
| `910b8b2` | 09-16 | 出站头锁外直读 token/domain 与刷新写回竞争 | 等价修复已由补丁 0001/0002 走 `Snapshot()` 路线覆盖（见 §3 与分歧 D1） |
| `722ee19` | 09-16 | 同 `910b8b2`（PR #125 形态） | 同上 |
| `855e5b9` | 09-17 | scheduler 前置守卫锁外直读 `RefreshToken` | 未逐条核验；补丁 0002 已把 scheduler 四处凭据读改成 `a.Snapshot().X` |
| `af51945`/`2b8dba0` | 09-16 | 粘性路由 GC goroutine 关停竞态与泄漏 | 未核验 |
| `5f10a9c` | 09-16 | `redisstore.Close` 排空竞态 | 未核验 |
| `7fad570` | 09-17 | [Audit] 竞态/死代码/泄漏/逻辑漏洞系统扫描 | 未核验（体量大，单提交） |
| `a767465`/`8058019`/`10eefa8` | 09-18 | session 内容签名 / 粘性键调整 | 未核验 |
| `231a076` | 09-17 | sanitize 指纹字面量字节快照护栏（测试） | 未核验 |
| `145220d` | 09-16 | `Classify` 429 前移，限流带 quota 措辞不再误判硬冷却 | 未核验 |
| `4ac68b7` | 09-18 | global chat 固定走 `/v2` 绕开 `/console` WAF 内容规则 | 未核验（与本轮回移路径相邻，若将来整包更新需一起评估） |

### A5 功能相关且 me 版会感知

| commit | 要点 | 对 me 的感知 |
| --- | --- | --- |
| `733d348` | `/v1/stats` 按模型聚合 token/缓存/延迟 | 即 M3b，本轮用户已拍板不做（§6.1） |
| `1c9aef9` | PR #178 stats 积分倍率 | 同上，`enrichCredits` 依赖全局模型快照 |
| `41d4714` + `7218307` | `/v1/models` 四级查找链 + `model.json` 本地缓存 | 改 `internal/server` 与 `internal/upstream`，与补丁 0002 的插入点相邻，整包更新时的冲突源 |
| `edb9e97` | `max_completion_tokens → max_tokens` 翻译 | 客户端兼容面，影响 README 的"兼容范围"表述 |
| `c2c0201` | auths 目录热加载 | 本仓已有 `extensions/internal/pool/install.go`，属重复实现风险点 |
| `d80a97f` | pool 连败降权参数 | 新增配置键，会影响 `deploy/default-config.json` 口径 |
| `213e362`/`f044e5c`/`5d5223d` | usage 合成补齐 / 限流重置时间英文文案 / 11135 JSON 空白容错 | 与 T3 同一文件族（`sse.go`、`logging.go`、`client.go`） |

### A6 噪音（与本项目部署无关）

- `.github/actions/ai-governance/`：新增 12 个 JS 文件（PR 治理 AI 流水线），镜像里没有 GitHub Actions 运行环境。
- `cmd/stats/`（8 个文件，终端 TUI + ANSI/term 平台分支）、`cmd/acct/`、`acct.sh`、`dev.sh`：作者本机工具链，`core.Dockerfile` 不启动它们。
- `README.md` 18 个提交 + `docs`：纯文档。
- 判断依据：`git diff --name-only --diff-filter=A c576b48..HEAD` 的 100 个新增文件里，
  `.github` 12、`cmd` 15、`internal` 67、`scripts` 1、顶层 5（见 A7）。前四类中的 `.github` + `cmd/stats` + `cmd/acct` 即噪音主体。

### A7 本仓快照实际缺失的文件

```text
$ git diff --name-status --diff-filter=A c576b48..HEAD | wc -l
100
$ git diff --name-status --diff-filter=D c576b48..HEAD | wc -l
0
```

分组：`.github` 12 ／ `cmd` 15 ／ `internal` 67 ／ `scripts` 1 ／ 顶层 5。**上游没有删除任何文件**。
`internal` 的 67 个里含 `internal/server/wafip.go`、`internal/pool/degrade.go` 及大量 `*_test.go`
（快照基点之后新增的文件，本仓 `upstream/` 里都不存在）。

### A8 六个补丁能否落到上游 HEAD

隔离副本实测（`/work/try` = v0 `b71d734` 的独立拷贝，`--check` 不落盘）：

```text
0001-auth-pool-consistency.patch          -> CONFLICT (8 error lines)
   error: patch failed: internal/auth/auth.go:49
   error: internal/auth/auth.go: patch does not apply
0002-upstream-credential-snapshots.patch  -> CONFLICT (10 error lines)
   error: patch failed: internal/upstream/client.go:728
0003-core-wiring.patch                    -> CONFLICT (4 error lines)
   error: patch failed: cmd/server/main.go:188
0004-scheduler-observation.patch          -> CONFLICT (6 error lines)
   error: patch failed: internal/scheduler/scheduler.go:194
0005-regression-tests.patch               -> CONFLICT (2 error lines)
   error: patch failed: internal/scheduler/school_test.go:2
0006-waf-ip-failfast-and-usage-sentinel.patch -> CONFLICT (4 error lines)
   error: patch failed: internal/server/handler.go:67
```

**6/6 全部冲突**，与二创期实测一致（我把本轮新增的 0006 一并测了，也冲突）。

### A9 整包更新的硬阻断

1. **补丁全废**：A8 显示 6 个补丁需逐个重写；补丁 0002 的插入点（`ChatStreamContext` /
   `billingJSONContext` / `headers.go`）在 214 个提交里已被原作者改过（`client.go:728`、
   `global_models.go:156` 直接 patch failed）。
2. **破坏性变更撞扩展**：`e34cfa4 feat(server)!: 移除 server.max_body_mb 预拦截——大请求交由上游自然响应`
   删掉了配置键，而本仓 `extensions/cmd/server/extension.go:293-294` 仍把
   `cfg.Server.MaxBodyMB` 传给 `anthropic.New` 与 `bridge.Config.MaxBodyBytes`：
   快照一动，扩展直接编译失败。`4646039 fix(server): 请求体超限明确 413` 是同一族的语义变更。
3. **重复实现风险**：`c2c0201` auths 热加载、`8825c4c` 的 IP 级熔断、`733d348` 的 stats
   与本仓扩展/补丁重叠，整包更新要先决定"上游版取代本地版"还是并存。

### A10 建议：不整包更新，按缺口逐补丁回移

- **建议**：维持锁定快照 + 缺口回移（本轮做法），不做整包 `overlay.py update`。
- **理由**：A8/A9 —— 收益（214 提交）与代价（6 个补丁全部重写 + 扩展编译断裂 + 每轮重跑隔离验收）不成比例，
  且真正影响部署安全面的缺口是有限且可枚举的（A4 表中前三行）。
- **代价估计**：本轮这种粒度约"1 个新补丁 + 1 个扩展文件 + 5 个测试文件"；
  整包更新则是"6 补丁重写 + 上游 100 个新增文件的取舍判断 + 扩展与 Anthropic 适配器重新对齐"。
- **例外条款**：若上游出现**新的**IP 级/WAF 类止损修复或认证绕过修复，应按 A4 逐条评估继续回移；
  只有当上游把 `wafip.go` 等纳入且我们能整体验证时，才值得一次性重排补丁栈。

## 2. 对照结果（读完对方文档后逐项比）

标注：一致 ／ 分歧 ／ 我方遗漏 ／ 你方遗漏。

| 项 | 对方结论 | 我的独立结论 | 判定 |
| --- | --- | --- | --- |
| 快照落后量 | 214 提交 | 214 | 一致 |
| 补丁兼容性 | 5 个补丁全冲突 | 6 个（含新增 0006）全冲突 | 一致（我多测了 0006） |
| `max_body_mb` | 破坏性变更阻断 | 同一提交 `e34cfa4`，并定位到 `extension.go:293-294` 的调用点 | 一致 + 补充 |
| M1 范围 | 只回移 IP 级熔断，不做软冷却与退避 | 同 | 一致 |
| M2 | `Snapshot()` 路线已覆盖竞争面 | 复核为真，但插入点名称与对方列表不同（见 D1） | 一致 + 更正 |
| M3a | `handler.go` 无条件覆盖 `-1` 哨兵 | 同，修法一致 | 一致 |
| M3b | 不做，登记 `/status` 替代方案并划边界 | 同 | 一致 |
| `wafip.go` 归属 | 决策：放 `extensions/`，0006 只碰 `client.go`+`handler.go` | 同 | 一致 |
| 验证入口 | §8.1/§8.2 在 Windows 不可用，改容器 clone | 同，另发现两处新的容器侧坑（D3/D4） | 一致 + 补充 |
| 盲析前置 | 要求 T0 先盲析 | 未满足，已在 §0 声明 | **我方（实施方）未达标** |

## 3. 分歧清单

### D1 「所有出站函数开头插入 `a = a.Snapshot()`」——表述不准确

- **对方依据**：§3 列出 14 个名字：`RefreshToken`、`ChatStream`、`FetchModels`、`billingMeterJSON`、
  `UserResourceDetailed`、`probeGlobalModels`、`ChatHeaders`、`BillingHeaders`、`RefreshHeaders`、
  `billingJSON`、`CheckinAll`、`RunActivityNow`、`RunKeepaliveNow`、`RunTravelNow`。
- **我的依据**：读 `patches/0002` 全文 + 物化后源码。实际插入点是
  `ChatStreamContext`（不是 `ChatStream`，后者只是委托）、`billingJSONContext`（不是 `billingJSON`）、
  `billingMeterJSONContext`、`userResourceDetailed`（私有改名）、`DailyCheckinContext` 走的也是
  `billingMeterJSONContext`、`globalModelsOnce`（不是 `probeGlobalModels`）、`growthJSON`、
  `ChatHeaders`、`BillingHeaders`、`RefreshHeaders`、`FetchModels`；
  `RefreshToken` 用的是 `snapshot := a.Snapshot()`（不重绑定 `a`），scheduler 四处是内联
  `a.Snapshot().Field`。
- **判断**：对方结论（竞争面已覆盖）为真，但**函数名列表按字面 grep 会落空**，
  后来者若照单验收会误判"补丁没做到位"。
- **需谁裁定**：需求方修正 §3 的列表为"实际插入点 + 委托关系"两段式表述。

### D2 有两个出站函数确实没有 `Snapshot()`——但都是安全的

- **对方依据**：§3 隐含"所有出站函数已覆盖"。
- **我的依据**：函数级扫描（`internal/upstream/*.go` 逐函数比对首次凭据读与 `Snapshot()` 位置）指出
  `ReportChatActivity`（读 `a.UID`）与 `ClaimTrialContext`（读 `a.Realm()`）没有自己的 `Snapshot()`。
  进一步核验：本仓唯一的原地写者是 `RefreshToken` 锁内的 4 个字段
  （`AccessToken`/`RefreshToken`/`Domain`/`ExpiresAt`，见物化 `client.go:714-735`）；
  `UID` 从不原地改写，`Realm()`/`RealmStored()`/`NeedsRefresh()`/`BackfillRealm()` 自身取 `a.mu`。
  所以这两处不构成竞争。
- **判断**：不是缺口；但"没有 Snapshot"和"不安全"是两件事，验收时应分开判。
- **需谁裁定**：无需裁定，登记为查漏结论的一部分。

### D3 busybox `sed` 的镜像改写配方必须同时满足两个条件

- **对方依据**：§12.3 的容器配方里 `sed -i 's|https\?://dl-cdn.alpinelinux.org|...|g'`。
- **我的依据**：该形态在 `golang:1.23-alpine` 下直接 `sed: unmatched '|'`；
  换 `#` 分隔但漏掉 `\?`（写成 `https?://`）则**静默不匹配**，镜像源没换 yet 表面无报错。
  实测只有 `s#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g` 可用
  （改完 `head -1 /etc/apk/repositories` 确认）。
- **判断**：对方配方不可照抄。
- **需谁裁定**：需求方把 §12.3 与仓库内脚本统一改成可跑形态（我已修 `_wb2a-verify/base.sh`，该脚本不在仓内）。

### D4 `overlay.py identity` 在 Windows 上不是稳定值

- **对方依据**：文档把 `patch_identity` 当作单一事实（如 `d8c08ffe…` 类前缀）。
- **我的依据**：同一份已提交内容，三种算法得到三个值——
  宿主 `python scripts/overlay.py identity` = `7b1edc83f45cec95…`；
  Linux 容器**直接只读挂载宿主工作区**跑 = `4891f37c8874b4d9…`；
  容器内 `git clone /host` 后跑 = `4f6b50a444de5fd7…`。
  根因是 `overlay_identity()` 把 `source_digest(extensions)` 与 `patches/**.read_bytes()` 混在一起：
  宿主工作区受 `core.autocrlf=true`（`patches/series` 实测 CRLF=6/7 行、
  `upstream/internal/upstream/client.go` 全文件 CRLF）与挂载点权限位影响，索引里的 LF 才是提交事实。
- **判断**：**只有 clone 路径的值可引用**。本轮权威值：
  加入 0006 前 `faba20a3c2d575f4…`，加入后 `4f6b50a444de5fd7…`（同一算法、同一容器、只差 series 里那一行）。
- **需谁裁定**：需求方在 §12 补一条"identity 必须在 Linux clone 内取"，避免验收方拿宿主值比对失败。

### D5 `T4` 的口径

- **对方依据**：任务表把 T4 定为"文档同步"，交付物 `patches/README.md` + 验证记录。
- **我的依据**：同。本会话早期曾出现"T4 = API Key 常数时间比较"的说法，
  来自一次工具输出串扰，仓库与文档中都没有该要求，已按 T4=文档同步执行。
- **判断**：按 §2 表格执行即可。
- **需谁裁定**：无需。

## 4. 无法判定的项（不隐去）

| 项 | 为什么无法判定 |
| --- | --- |
| A4 中标"未核验"的 8 条安全提交是否已被本仓等价覆盖 | 需要逐条读 diff 并与 `upstream/` + 6 个补丁对齐；本轮只按 T1/T2/T3 范围核验了 WAF 与 auth 竞争两条链 |
| `4ac68b7`（global chat 改走 `/v2`）是否算 WAF 止损的一部分 | 它是绕开内容规则的**路由策略**，与本轮回移的**请求量止损**是两种机制，需要真实上游行为才能判收益 |
| 整包更新后 `/v1/stats` 与本仓 `console` 的耦合成本 | M3b 已裁定不做，未实测 |
| 真实上游 WAF 是否真的按"2 个号 60s 内同拦"的形态触发 | 离线环境；本轮只证明状态机与轮转决策符合判据（T1-a…T1-h），不证明上游判定阈值最优 |
| 上游对 `wafIPWindow`/阈值是否已改为可配置 | 未读 v0 HEAD 之后的 `wafip.go` 演进（本轮取的是二创期读到的版本） |
