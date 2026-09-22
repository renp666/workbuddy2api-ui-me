# Qoder 交接提示词（v3）

- **用途**：复制本文「提示词正文」一节，粘贴给 Qoder
- **编写日期**：2026-09-22
- **关联文档**：
  - 完整工单（以文件为准）：`docs/superpowers/plans/2026-09-22-backport-handoff.md`
  - 方案与需求来源：`docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md`
  - 需求来源（只读分析结论）：`docs/superpowers/verification/2026-09-22-security-review-tracking.md`

> **本文定位**：提示词是**摘要**，完整工单以 `2026-09-22-backport-handoff.md` 为准。
> 若两者冲突，一律以工单文件为准。

## 修订记录

| 版本 | 日期 | 变更 | 原因 |
| --- | --- | --- | --- |
| v1 | 2026-09-22 | 初版，4 个任务（T1–T4） | 需求方单方分析后编写 |
| v2 | 2026-09-22 | 新增 T0 独立复析任务 + 盲析隔离要求 + Q5 | 用户要求 Qoder 独立分析一遍两极差异再与我方结论对照 |
| v3 | 2026-09-22 | **修正开头「不要重新调研」的自相矛盾**；改为两阶段结构 | v2 开头写「不要重新调研已给结论」（原文为 T1–T3 而写）却与 T0 的独立复析要求直接冲突 |

---

## 提示词正文

> 以下整段可原样复制给 Qoder。

````text
你是本任务实施方。本任务分两阶段：先独立复析（T0），再实施三项修复（T1–T3）。

===============================================================
第一阶段 T0：独立复析 v0 与 me 的差异与可更新性
===============================================================

## T0 的目标
自己从头分析「原作者版本 v0」与「本仓 me 版」到底差什么、
原作者后续更新里哪些值得跟、哪些不值得跟，**先得出你自己的结论**。

我方文档里的事实、行号、判断，T0 阶段一律只当
「待核验的对方主张」看待，**不要当既定前提引用**。

## T0 强制隔离（关键，否则变成复述）
分两步，顺序不可颠倒：

第 1 步（盲析）——先自己跑命令、自己得结论、**先落盘**。
   此时【不要读】以下内容：
   - docs/superpowers/verification/2026-09-22-security-review-tracking.md
   - docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md
   - 交接工单里的 §3 / §4.4 / §5 / §附录 A
   （工单其他部分可以读，用来了解项目机制与目录结构）

第 2 步（对照）——自己的结论写完后，再读上述文档，逐项对照。

两阶段内容必须分节存放并标明，不得混写。

## T0 必须逐条回答（A1–A10，每条附实际命令 + 实际输出）
A1  三个仓库的 HEAD 与血缘关系各是什么？（commit hash + 日期 + git remote -v）
A2  本仓 upstream/ 快照锚在哪个 commit？落后原作者多少提交？
    （upstream.lock 内容 + git rev-list --count 实测值）
A3  本仓相对二创 baiyea 改了什么？（git diff --stat 文件清单）
A4  原作者那 N 个提交里，安全相关的是哪些？（hash + 标题 + 一句话影响）
A5  其中功能相关、且 me 版会感知的是哪些？
A6  哪些是噪音（与本项目部署无关）？给判断理由
A7  本仓快照实际缺失的文件有哪些？（git diff --name-status 或等价证据）
A8  把 5 个既有补丁应用到上游 HEAD，能否成功？（自建隔离副本实测
    git apply --check，给逐补丁结果）
A9  直接整包更新会撞什么硬阻断？（给具体符号/文件/错误）
A10 结论：建议更新、部分更新、还是不更新？给理由 + 代价估计

## T0 取证约束
必须用可复现命令取证，不接受"文档说/应该/大概"。

  git -C <v0路径> log -1 --format='%h %ad %s' --date=short
  git -C <v0路径> rev-list --count c576b48..HEAD
  git -C <me路径> diff --stat upstream/main..HEAD
  git -C <v0路径> log c576b48..HEAD --format='%h|%ad|%s' --date=short

【环境陷阱】GitHub 当前不可达（Failed to connect to github.com:443）。
三个 clone 本地都在，全部分析离线完成，不要 git fetch。

【陷阱】试应用补丁不要污染工作区。用 git clone --shared 或 git archive
建独立副本（用尚不存在的新路径），不要往本仓工作区里试。

## T0 交付物
docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md
固定结构：
  1. 独立结论（盲析阶段，A1–A10 逐条 + 证据命令 + 实际输出）
  2. 对照结果（读我方文档后，逐条标「一致 / 分歧 / 我方遗漏 / 你方遗漏」）
  3. 分歧清单（每条写：分歧点、双方依据、你认为谁对、需谁裁定）
  4. 无法判定的项（及原因，不得隐去）

## T0 完成后
不要自行调整 T1/T2/T3 的范围。把分歧写进「分歧清单」，
交需求方裁定后再动。范围变更由需求方确认并改文档。

如果 T0 发现我方结论有误——那是最有价值的产出，
不要为了"对齐"而弱化分歧。

===============================================================
第二阶段 T1–T3：按已定范围实施（不要重新调研）
===============================================================

这部分才适用「不要重新调研」：范围已实测拍板，文档给的行号、锚点、
命令直接采用即可。凡标注【决策】的不另选方案；凡标注【陷阱】的照做。

## 项目背景（一句话）
本仓 me 版是三层血缘第三层。upstream/ 是只读历史快照
（upstream.lock 锚 c576b48），落后原作者 214 提交。
改上游既有文件必须走 patches/；新能力放 extensions/。
不得为控制台另建账号池或调度器。

## 执行顺序（不可调）
T0 → T3 → T2 → T1 → T4
理由：T0 必须先做且先落盘，否则实现思路污染独立判定；
T3 最小、最快建立"红→绿"节奏；T1 最大且最后，避免阻塞。

## T3  流式 usage 缺失哨兵（极小）
这是个真 bug，实测确认：
- upstream/internal/server/logging.go:41 用 toks:-1 做哨兵，
  :29 注释「<0 表示 usage 缺失 → 显示 "-"」，:202 按 toks>=0 渲染
- 但 upstream/internal/server/handler.go:633 `st.toks, _ = stats.Tokens()`
  无条件用零值 0 覆盖哨兵，并丢弃 hasUsage
- 非流式分支 handler.go:656 走 completionTokens(resp) 返回 -1，不受影响

修法（4 行）：只在 hasUsage 为真时写 st.toks；
同一 hasUsage 复用到下方成本账本分支（原第 638 行），
消除原先二次调用 Tokens()。

【陷阱】这是服务端日志口径 bug，不是客户端 usage 字段。
客户端侧 upstream/internal/upstream/sse.go:252「usage 缺失 → null」
本来就正确，不要动。

验收：末帧无 usage → 日志含 tok=- 且不含 tok=0；有 usage → 仍正确；
非流式不回归。

## T2  auth 数据竞争（小）——只验证，不要实现修复
【最重要的一条，先读完再做】
补丁 0001/0002 **已经实现**了等价修复：
- patches/0001 已给 Realm() / BackfillRealm() / RealmStored() 加 a.mu.Lock()，
  并新增 Auth.Snapshot()
- patches/0002 已在 14 处出站函数插入 `a = a.Snapshot()`，包括
  RefreshToken / ChatStream / FetchModels / billingMeterJSON /
  UserResourceDetailed / probeGlobalModels / ChatHeaders / BillingHeaders /
  RefreshHeaders / billingJSON / CheckinAll / RunActivityNow /
  RunKeepaliveNow / RunTravelNow

上游 910b8b2 走的是 AccessTokenValue()/DomainValue() 加锁访问器路线，
与本仓 Snapshot() 路线**目标重叠、实现不同**。
**不要移植 AccessTokenValue**，会双重实现。

你只做三件事：
1. 验证竞争确已关闭（补一个 -race 回归测试，对齐
   upstream.TestChatHeadersRacesRefreshToken 的对抗思路：
   一 goroutine 反复 RefreshToken，另一反复 ChatHeaders，同一 *auth.Auth）
2. 查漏：确认没有出站函数被漏掉 Snapshot()
3. 确认无自锁死锁

【陷阱·自锁】patches/0001 已给 Realm() 加锁。任何**已持 a.mu** 处
再调 Realm() / RealmStored() / NeedsRefresh() / BackfillRealm()
会死锁（sync.Mutex 不可重入）。
上游为此专门拆了无锁 realmLocked()。
本仓 Snapshot() 是按字段写（realm: a.realm）不调 Realm()，故当前安全。
查漏时必须确认：没有任何已持锁的函数调用上述四个方法。
a.Lock() 全仓调用点应仅 RefreshToken 内 3 处 + SaveAtomic 自身（需复验）。

## T1  WAF IP 级 fail-fast 熔断（大）

需求：WAF 403 拦的是网关出口 IP，不是账号。
短窗（60s）内 ≥2 个**不同** UID 接连命中 WAF 403 → 判定 IP 级拦截
→ 轮转立即终止（放大倍数 = 1）→ 窗口到期自然解除。
现状问题：继续轮转会放大请求量（MaxRotate 默认 3 倍）打同一出口 IP，
加重风控。

判定口径（不要自创第二套）：
- 单账号反复 403（账号级偶发）永不触发——只数不同 UID 数
- 激活期内新命中不续期（保守：窗口自然解除，不主动探测）
- 账号级软冷却照常记账，不受影响（IP 级只改「是否继续轮转」）
- 进程内状态、重启清零

### 依赖链（本仓快照全缺，已实测）
| 上游提交 | 提供 | 本仓状态 |
| 76fafa6 | ErrWafBlock + IsWafBlocked + ParseRetryAfter + Error.RetryAfter | 全缺 |
| 34405ca | WAF 403 软冷却 + 轮转指数退避 | 缺（无 rotateBackoff）|
| 8825c4c | internal/server/wafip.go + 轮转插入点 | 缺 |

### 【决策】最小化回移——只做 IP 级熔断，不搬整条链
不要照搬 76fafa6 的全部内容。它含 ParseRetryAfter /
retryAfterHeaderCandidates / isAllDigits / parseRetryNumber /
Error.RetryAfter / ChatStreamContext 返回值改造——大部分与 IP 级熔断无关。

只做以下最小集合：
1. ErrWafBlock 枚举值 + String() 分支（internal/upstream/client.go）
   - 加在 ErrClient **之前**（保持 ErrClient 为最后兜底）
   - String() 返回 "waf_block"
2. IsWafBlocked(status, body) + hasBusinessEnvelope(body)（同文件）
   - 判定：status == 403 && !hasBusinessEnvelope(body)
   - hasBusinessEnvelope：body 含 "code": 或 "msg": 即视为业务信封
     （不解析 JSON，只做字符串包含——畸形 JSON 含该字样仍按业务 403
     处理，宁漏判 WAF 也不误罚业务 403）
   - 不引入 RetryAfter / ParseRetryAfter（IP 级熔断时长是本地固定窗口）
3. Classify() 插入 WAF 分支（同文件）
   - 位置：status >= 500 判断之后、内容策略/参数错误/通用 4xx 兜底之前
   - 本仓 Classify 实际结构见文件，按实际锚点插入，勿照抄上游行号
   - 必须保证带业务信封的 403（如 11140 request illegal →
     ErrAccountFault）不受影响
4. internal/server/wafip.go（新增文件，74 行）
   - 取自 D:\work\02toolsMy\workbuddy2api_v0\internal\server\wafip.go
   - wafIPWindow = 60 * time.Second（var，供测试注入短窗）
   - wafIPThreshold = 2（const）
   - type wafIPGate struct { mu sync.Mutex; hits map[string]time.Time;
     until time.Time }，零值可用
   - noteWaf(uid) bool：激活期内恒 true 且不续期；未激活则记
     hits[uid]=now、清窗口外过期项、len(hits) >= wafIPThreshold 时
     激活到 now+wafIPWindow 并打 WARN、清空窗口
   - active() bool
5. Handler 加字段 + 轮转插入点（internal/server/handler.go）
   - Handler struct 加 wafIP wafIPGate（零值可用，与现有
     degrade degradeGate 同风格）
   - 插入点（本仓实测行号）：handler.go 第 616-619 行区域，当前为：
       lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
       h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel)
       fail(acct.UID)
       continue
     在第 617 行 applyErrorPolicy 之后、fail(acct.UID) 之前插入：
       if kind == upstream.ErrWafBlock && h.wafIP.noteWaf(acct.UID) {
           break
       }
   【陷阱】本仓此处没有 rotateBackoff（上游才有），不要引用不存在的符号。
   本仓该分支是 continue（直接下一轮），IP 级熔断在此 break 即可。
   - 末端错误文案：在写错误响应的分支加 ErrWafBlock 的 case，参考上游：
       case upstream.ErrWafBlock:
           if h.wafIP.active() {
               code = "waf_ip_blocked"
               msg = "waf ip-level block: upstream firewall is blocking
                      the gateway IP, rotation stopped; retry after the
                      block window expires"
           }
   【陷阱】本仓错误文案分支结构与上游不同，按本仓实际分支变量名适配。
   若找不到合适锚点，可退化为不改文案分支（IP 级熔断核心价值是"止损"，
   文案次要），但要在交付说明里写清是否做了文案。

### T1 验收条件（缺一不可）
T1-a 窗内 2 个不同 UID → 激活（noteWaf("u1")==false, noteWaf("u2")==true）
T1-b 单号反复命中（任意多次）→ 恒 false
T1-c 窗口过期自然解除；激活期内不续期（注入短窗推进时间断言 active() 翻转）
T1-d **激活期轮转终止：上游调用数 = 2（非 3）**（端到端，假上游返 403，
     断言被调用次数）
T1-e 并发安全（并发 noteWaf/active 混发 + -race）
T1-f 账号级软冷却不受影响（applyErrorPolicy 现有测试全绿）
T1-g 带业务信封的 403 仍走原分类
     （Classify(403, '{"code":11140,...}') 仍返回 ErrAccountFault）
T1-h 现有路由/测试不回归（go test ./... 全绿）

【陷阱】T1-d 必须真的计数。"调用数 = 2 非 3"是"止损成立"的唯一硬证据。
只断言"返回 403"不算通过。

### T1 参考
wafip.go 全文：D:\work\02toolsMy\workbuddy2api_v0\internal\server\wafip.go
上游测试：workbuddy2api_v0 的 internal/server/wafip_test.go + wafip_conc_test.go
（注意：上游测试可能引用本仓不存在的符号如 rotateBackoff，
参考断言思路，不要整文件照搬）

## T4  文档同步（小）
- 更新 patches/README.md 表格：为 0006 增加一行，列「修改的文件」
  「验证方式」「移除条件」（照现有行格式）
- 维护 docs/superpowers/plans/2026-09-22-backport-progress.md
  （任务总览 ✅/⬜/🔄 + 中断保护区，每任务开始前写、完成后清）
- 写 docs/superpowers/verification/2026-09-22-backport-verification.md
  （逐条贴实际命令 + 实际输出）

===============================================================
明确不做的事（防止范围蔓延）
===============================================================
- ParseRetryAfter / Error.RetryAfter / rotateBackoff
  （与 IP 级熔断无关，会牵动 ChatStreamContext 返回值签名）
- upstream/ 快照更新 / upstream.lock 改动（未授权；实测 5 补丁全冲突
  + MaxBodyMB 破坏性变更）
- 既有 5 个补丁的语义修改（只允许新增，不动老补丁行为）
- /v1/stats 端点（本仓快照只有 4 条路由、无 metrics.go，
  实为"从零引入 324 行端点 + 173 行测试"，单列待评估）
- AccessTokenValue()/DomainValue() 移植（与现有 Snapshot() 重复）
- cmd/acct / cmd/credit / cmd/trial / cmd/stats（原作者本机工具链）
- .github/actions/ai-governance/（原作者 PR 治理流水线，14+ 测试文件）
- 发布镜像 / 推送 Git / 重启服务（均需单独授权）

===============================================================
补丁编写规范
===============================================================
- 命名：patches/0006-waf-ip-failfast.patch
- T3 允许并入 0006 以减少补丁数，但要在 series 与 README 写清；
  若并入改名 0006-waf-ip-failfast-and-usage-sentinel.patch
- 追加到 patches/series 末尾（不改现有 0001–0005 顺序）
- 【关键】补丁必须基于本仓快照 c576b48 的实际文本生成，
  不要照抄上游 HEAD 的 diff（行号与上下文都不同，会 apply 失败）

生成方式（推荐）：
  # 1. 物化干净快照（用尚不存在的输出路径）
  python3 scripts/overlay.py prepare --output .build/backport-work
  # 2. 在物化目录里改代码
  # 3. git diff > /tmp/waf.patch
  # 4. 回写为仓库补丁，确认补丁头是 a/internal/... b/internal/...
【陷阱】物化目录旧了不会自动同步。每次重跑用新的输出路径，
不要复用、不要删除来源不明的目录。

测试放哪：
- 新增独立测试优先放 extensions/ 对应相对路径
- 若必须改既有上游测试文件（如 logging_test.go），那必须进 patches/
- 建议：wafip_test.go 作为新文件放 extensions/internal/server/，
  这样补丁只含生产代码，更干净

===============================================================
验证与交付
===============================================================
## 基础验证（逐条实跑并贴输出）
【2026-09-22 更正】原方案在本机不可执行：本机没有 Go 工具链（which go 空），且 prepare 的摘要门
在 Windows 上必失败（交接书 §12.1/§12.2）。除末尾本机三项外，以下全部在交接书 §12.3 的 Linux
容器入口里跑（容器内先 git clone /host /work/repo，再 prepare 到 /work/core）：
python3 scripts/overlay.py prepare --output /work/core     # rc=0；锁摘要 299e722b… 校验一致
go -C /work/core test ./...                                 # HEAD 71fa504 基线：20 个包全 ok
go -C /work/core vet ./...                                  # 无输出
go -C /work/core test ./internal/server ./internal/upstream ./internal/auth ./internal/pool
go -C /work/repo/console test ./...
python3 -m unittest discover -s scripts -p 'test_*.py' -v    # 容器内才是真判据（36 项应全绿）

本机实测可跑的三项：
node --test console/web_test.cjs                            # 30 tests / 30 pass / 0 fail
docker compose --env-file /dev/null -f docker-compose.yml config --quiet   # exit 0
git diff --check                                            # exit 0

## 竞态验证（必须，在 Linux 容器内）
CGO_ENABLED=1 go -C /work/core test -race -count=1   ./internal/auth ./internal/pool ./internal/upstream ./internal/server ./internal/scheduler
go -C /work/repo/console test -race -count=1 ./...
（容器里 apk add git python3 bash gcc musl-dev；HEAD 基线 5 个关键包 + console 全 ok，无 DATA RACE）

【陷阱】-race 不能省。T1-e 与 T2 的全部价值都在 -race 上。
若容器起不来，必须在交付说明里明确写"竞态未验证"，不得用 go test 通过冒充。
【2026-09-22 更新】引擎已可启动、基线已实测，此项不再是待决风险。原配方里的
docker run -v "$(pwd -W):/src:ro" -w /src/.build/backport-verify 已作废：只读挂载写不出 .build，
且 Windows 目录直挂摘要仍不匹配。改用 §12.3 的 clone 入口。

## 已知环境性失败（不是回归，不要试图修）
- python3 -m unittest discover -s scripts → 本机 3 failures + 21 errors（真 python 3.12.10）
  根因是 Windows 平台语义：无执行位、建 symlink 需特权、POSIX 模式不成立。
  原文写"4 failures + 26 errors + Store 占位符"是旧解释器下的旧计数与旧归因。
- python3 -m unittest discover -s deploy → 两个环境都无法全绿，必须合判：
  本机（Windows + docker CLI）Ran 13 → 2 failures（test_compose.py:42 Windows 路径分隔符假失败
  + test_migrate 的 POSIX 权限位）；容器（Linux + 镜像内无 docker CLI）Ran 13 → 6 errors 全在
  test_compose.py，而 python3 -m unittest test_migrate 单独跑 Ran 8 → OK。
  分工：test_migrate 认容器，test_compose 需要 Linux+docker CLI 环境，不得改断言迁就
- python3 -m unittest discover -s scripts（容器内）→ Ran 43 → OK（本机那 3 个失败在这里转绿）
- gofmt -l 判据：本机无法复现（本机没有 Go 工具链，gofmt 不存在）。格式化改到容器内
  对 clone 出来的 LF 树执行，不在本机判
判据：这些项在改动前基线与改动后必须计数相同，贴两次对比。
【陷阱】「本机计数相同」不等于「改前改后一致」——本机环境没变，同一条 Python 命令跑两次必然
相同，那只能证明命令稳定，不证明改动无害。能区分二者的只有 §12.3 容器内那套：Linux 下
test_overlay.py 36 项必须全绿（这才是 G1 的真实判据），Go 侧必须与 HEAD 基线逐项同结果；
Python 侧的本机计数只作辅助。

## 硬约束（违反即打回）
- 不改 upstream/ 与 upstream.lock
- 不改既有 5 个补丁（0001~0005）的语义，只允许新增
- 不 git push、不发布镜像、不重启真实服务
- 不重置工作区、不清空数据、不覆盖真实配置

## 交付说明必须包含
- 改了哪些文件（完整路径）
- 每个文件的变更摘要
- 每条验收条件的实跑结果（贴输出摘要）
- 未能验证的项目，及原因（不得隐去）
- 与工单的任何偏离，及理由
- 是否产生孤立代码（无用 import/变量/函数）

## 禁止行为
❌ 不许说"完成"而不贴验证输出
❌ 不许把环境性失败算成回归或算成通过
❌ 不许在容器起不来时声称 -race 通过
❌ 不许修改既有 5 个补丁的语义
❌ 不许改 upstream/ 或 upstream.lock
❌ 不许 git push、发布镜像、重启真实服务
❌ 不许重置工作区、清空数据、覆盖真实配置

===============================================================
我会怎么验收（提前告知）
===============================================================
我会独立复跑，不依赖你的自述：
- T0：比对你的独立结论与我方结论，逐条判「一致/分歧」；
  对分歧项我自己复跑命令做仲裁
- T1–T3：在全新输出路径物化；跑全部验证命令并把输出与我的基线比对；
  自写对抗测试重点打三处：
  · 轮转调用数是否真的 = 2（不是 3）
  · 改动前基线能否复现红态（证明修复真关掉了一个真实缺口）
  · go test 绿 ≠ -race 绿
- 检查 patches/README.md 是否同步、移除条件是否写明

验收不通过的典型原因（提前告知）：
只断言"返回 403"没断言调用次数；把 go test 绿当 -race 绿；
改了老补丁；补丁基于上游 HEAD 而非本仓快照生成导致物化失败。

===============================================================
现在请回复我（不要先动手）
===============================================================
1. 先复述你对 T0 / T1 / T2 / T3 的理解，各 2-3 句，我确认后再开工
2. ~~你这台机器有没有 gcc / 可用的 Linux 容器？~~ **已答复（2026-09-22）**：Docker Desktop 引擎可启动，
   容器内 apk 装齐 gcc 14.2.0 + musl-dev，HEAD 基线 5 个关键包 -race 全 ok、无 DATA RACE。
   本机确实没有 Go 工具链（不是「只缺 gcc」），所以 -race 一律在容器内跑。
3. T0 与 T1–T3 能否分两个会话/两人做？
   （若只能同会话，必须先把 T0 盲析落盘，再开始 T3）
4. T3 是否并入 0006？wafip_test.go 放 extensions/ 还是进补丁？
   T1 末端错误文案是否必做？

提示：完整工单在 docs/superpowers/plans/2026-09-22-backport-handoff.md，
方案与需求来源在 docs/superpowers/specs/2026-09-22-upstream-security-backport-design.md。
工单以文件为准，本提示词是摘要。
````

---

## 使用说明（给需求方）

1. 复制上面 ````text 代码块内**全部内容**（不含外层的```` 标记）粘贴给 Qoder。
2. 若 Qoder 运行在别的机器，路径 `D:\work\02toolsMy\...` 让它按实际调整（工单 §1 已说明）。
3. 收到 Qoder 回复后，重点核对两件事：
   - 它是否复述出了 **T2 不要实现修复** 这条（最易错）；
   - 它是否承诺 **T0 盲析先落盘**。
4. Qoder 交付后，把产物交回需求方验收。验收流程与判据见工单 §10。

## 版本差异对照

| 位置 | v2 | v3（本文） |
| --- | --- | --- |
| 开头指令 | 「不要重新调研已给结论」——**与 T0 矛盾** | 拆成两段：T0 **必须**重新调研；T1–T3 不要 |
| 结构 | 平铺 5 个任务 | 明确「第一阶段 T0 / 第二阶段 T1–T3」两段式 |
| T0 隔离 | 仅工单 §3.5.2 有 | 提示词内前置强调，含「不要先读」清单 |
