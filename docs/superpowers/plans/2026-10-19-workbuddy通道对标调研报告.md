# WorkBuddy 通道对标调研报告（供人工拍板）

> 日期：2026-10-19 ｜ 性质：纯调研，未改任何代码 ｜ 下一项通道将按同样方式调研

## 一、检索口径与关键事实

1. GitHub 检索 `workbuddy2api` 共 135 仓，头部两个对标（星数经 GitHub API 核实）：

| 项目 | 星数/fork | 语言 | 活跃度 | 血统 |
| --- | --- | --- | --- | --- |
| [linguo2625469/workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel) | ★3763 / f1077 | Go | push 2026-10-12 | Sliverkiss 增强分支，已同步至上游删库前最后更新 `ea8b1e5` |
| [ardeyouxipianyi/workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub) | ★1739 / f347 | Python | push 2026-10-18（活跃） | 独立实现，双区域（国际+国内） |

2. **原作者 Sliverkiss/workbuddy2api 已删库**（panel README 证实）；HanawaBanana/workbuddy2api（本仓 upstream.lock 源）也已 404。**本仓快照不再有上游可同步**——对标项目的增强只能人工选择性移植。
3. **本地核对结论**：本仓快照虽基点较早，但已包含大量上游后期功能（见矩阵「已有」列）。矩阵以下述 grep 断言为准，非 README 推断。
4. 网络受限：本机直连 GitHub API / git / 加速镜像均不通，未做代码级 diff；素材为两项目 README 全文（已存档于 Temp toolcall-output）。标「待核对」处为后续项。

## 二、功能矩阵（本地代码核对后）

### 已有（无需仿照）

| 功能 | 本仓落点/判据 |
| --- | --- |
| 常量时间密钥比较 | grep 命中 console/server.go、extensions bridge/anthropic、trae/raccoon sidecar |
| 6004 模型级独立冷却 + 粘性按模型判活重分配 | upstream/internal/pool/cooldown.go（modelCooldowns）、cmd/server/main.go:76 |
| tool_calls.arguments 盲区净化（content=null 工具轮） | upstream/internal/upstream/sanitize.go:146-203 + 专项回归测试 |
| DeepSeek 思维链注入 / reasoning_content 回填 / effort 档位降级 | upstream README:53 + backfill_test.go、payload.go 管线 |
| 402 余额耗尽硬冷却（CoolHard 不参与兜底） | upstream/internal/pool/pick.go:181-197 |
| 出站 UA 三段式按 realm 切换（WorkBuddy / WorkBuddy AI）、版本可配 | upstream/internal/upstream/headers.go |
| 出站 X-Request-ID / X-Conversation-* 头族、会话 ID 体系 | headers.go:243-262、internal/session/ids.go |
| 内容派生粘性键（首条 user 派生，客户端无需会话 ID） | internal/session/ids_test.go「u0:任务」 |
| 熔断/冷却/在途租限/连续失败熔断 | core pool + console circuit.go（m2c 后更全：阶梯/半开/摘链保护/请求内重试） |
| 强制 stream + SSE 白名单重建 + 指纹脱敏管线 | upstream payload.go / sanitize.go |
| 模型能力透出（effort 档位/积分倍率/上下文窗口） | upstream handler.go + 补丁 0009/0011 |
| 网页管理面板、账号热加载、在线改模型开关/别名、调用统计账本+趋势图 | console（九通道控制台，广度已超两个对标） |
| /healthz、每请求表格日志、状态原子落盘、首启自动生成密钥 | core + console keys.json |
| 上游新模型免发版进 /v1/models（目录探测） | FetchGlobalModels 探测 + 隐藏模型实测入库 |
| 调用来源 IP·UA 归因 | 补丁 0008 + console usagelog（见 §六勘误：调研时误判为缺失） |

### 缺失（候选增强，见第四节建议表）

响应侧 tool_calls 还原、余额后台刷新+解冻、客户端 X-Request-Id 响应头+成功率面板、多 API Key+按 Key 归因、每日限额护栏、成长任务一键完成 17/18、OpenRouter 价估算、X-Device-Token 设备风控头（待核对）、积分到期分布、core 配置热生效。

## 三、逐项仿照建议表（核心交付）

| # | 功能 | 来源 | 描述 | 建议 | 落点 | 成本/风险 | 优先级 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | **工具调用标记还原** | panel | 上游 DeepSeek 系把工具调用写成带全角竖线的标记文本，仅当请求声明 `tools` 时才被上游解析；中转丢失 tools 声明时标记以纯文本落进 content，Codex/pi 类只认结构化 tool_calls 的客户端工具链直接断。网关在响应侧识别标记并还原为 tool_calls（流式+非流式） | **仿照** | core 出站响应（补丁/扩展） | 中 / 保守匹配防误还原；需真实上游实测样本 | **P1** |
| 2 | **余额后台刷新+冷却自动解冻** | panel | 周期（默认 5min）查余额更新池；处于硬冷却（余额耗尽）的账号余额恢复后自动解冻回池 | **仿照（已落地 2026-10-19，见 §六）** | core 调度（扩展/补丁） | 中低 / 本仓 pick.go CoolHard 语义已在，只补刷新与解冻 | **P1** |
| 3 | ~~调用来源 IP·UA 归因~~ | panel + hub 按Key归因思想 | 账本每条记录补 client_ip / UA 摘要，多客户端区分排障 | **已有（勘误：调研时误判为缺失，见 §六）** | 补丁 0008 + extensions/internal/usagelog | — / 本仓实现比 panel 更严：IP 收敛网段（/24、/64 主机位不落盘）、UA 200 字清洗截断、鉴权失败留痕（denied-）独立分文件 | — |
| 4 | 客户端 X-Request-Id + 成功率双口径 | panel | 响应头带请求 ID 并贯穿账本；面板展示「完成成功率 / HTTP 成功率 / 平均耗时」 | 仿照 | console 出口+账本 | 中低 / 公共面改动需过九通道枚举点清单 | P2 |
| 5 | 多 API Key + 用量按 Key 归因 + Key 级模型限制 | hub | 每客户端独立 Key，可限模型（通配符）、可启停删除；用量按 Key 出报表 | 仿照 | console auth 层+账本 | 中 / 涉及鉴权面，需回归测试兜底 | P2 |
| 6 | ~~404 短冷却 60s~~ | panel | 404（模型/端点不存在类）只短冷却 60s 防长罚 | **已有（2026-10-20 核对：快照自带，见 §六勘误）** | upstream/internal/server/handler.go `notFoundCooldown` | — / `upstream.Classify` 判 `ErrNotFound` → 固定 60s 软冷却，不随 soft_rate 指数退避，注释理由与 panel 同款 | — |
| 7 | **每日限额护栏** | hub | 保留积分 / 每日 Token / 每日积分 / 按模型每日Token 四条账号级护栏，本地 0 点解封；全账号达额返回 429 + Retry-After 指向 0 点 | **仿照（建议简化两条先行：每日Token+保留积分）** | core 调度 | 中高 / 护栏计数与解封需落盘；与 pin/粘性交互要理清 | P2 |
| 8 | 成长任务一键完成 17/18 | panel | 逆向行为事件链纯 API 点亮成长任务并自动领奖（≈+1950c/新号） | 观望 | core 任务层 | 高 / 事件链脆弱+风控敏感+上游已删无维护；本仓 school/cat 脚本已覆盖部分 | P3 |
| 9 | OpenRouter 价估算 | hub | token 按公布价折算等价花费，定价策略快照使历史数字不随调价漂移，未定价可见 | 仿照 | console 统计面 | 中 / 需嵌价目快照+定期刷新 | P3 |
| 10 | ~~粘性键哈希化（d- 前缀）~~ | panel | 本仓粘性键已是内容派生（u0:原文），长 prompt 会键膨胀；改为哈希 | **已顺带完成（2026-10-20 核对：键膨胀落点 sync.Map 缓存已被补丁 0018 删除，见 §六勘误）** | core session | — | — |
| 11 | X-Device-Token 设备风控头三源 | panel（吸收自上游末期） | 出站补设备风控头（auth 每号/config/文件） | 待核对 | core headers | 低 / 无实证风控问题前不动出站指纹 | P3 |
| 12 | 积分到期分布图 | panel | 面板按账号积分到期时间出分布 | 仿照 | console 前端 | 低 | P3 |
| 13 | 桌面客户端凭据导入 | hub | 从运行中的桌面客户端就地解密 token 导入 | 不仿照 | — | 容器读不到宿主 %APPDATA%（traeai 同款教训），回调/JSON 导入已覆盖 | — |
| 14 | 在线配置热生效（core 侧） | panel | 深合并+原子替换+保留未知键 | 观望 | core 配置 | 中 / console routes.json 已在线生效，core 侧收益待评估 | P3 |

## 四、明确不建议仿照项及理由

| 功能 | 理由 |
| --- | --- |
| 本地网络工具代跑 web_search（hub） | 超出反向代理语义，主动出网+多耗账号额度，与「core 语义不动」冲突 |
| 智能体一键写本机配置（hub） | 本仓为远程容器部署，无本机配置可写 |
| Upstash Redis 状态镜像（panel） | 单机部署徒增外部依赖 |
| `?pwd=` URL 免密进面板（hub） | 密码留浏览器历史/代理日志，安全反模式；本仓管理会话+CSRF+限速体系更强 |
| Docker HEALTHCHECK / Watchtower / latest 标签 / 双架构镜像 | AGENTS.md 明确约束：不加健康检查、不维护 latest、仅 linux/amd64 |
| PBKDF2 面板密码 | 本仓已有管理会话体系，无需另立密码 |
| 面板明暗主题等 UI 翻新 | 本仓九通道控制台已成型，如做应单独立项而非顺手改 |

## 五、与 integrate-agent-proxy skill 的关系

- 该 skill 管「新增旁路通道」接入，本次候选功能多数落 core/console 公共面，**不走该流程**。
- 但 skill 的核心教训直接适用：任何公共面改动（#3 #4 #5 等）必须过其「新增通道公共接入检查清单」的枚举点（route.go / probe.go / usage_report.go / app.js 九通道枚举），防止新通道在某页面隐身。
- 建议：拍板后将「公共面功能改动检查清单」作为附录沉淀进该 skill（届时小改）。

## 六、验证记录

- 素材：panel README（163 行）、hub README（160 行）全文经 WebFetch 获取并落盘；星数经 GitHub API 核实。
- 本地核对（grep 断言，均实际执行）：`｜` 在 upstream/ 无匹配（缺响应侧还原）；`balance_refresh` 全仓无匹配（缺余额刷新）；console/ 无 `APIKeys`（单 Key 确认）；`ConstantTimeCompare` 命中 5 文件（已有）；`6004`/`sanitize tool_calls`/`reasoning_content`/`sticky` 均命中（已有）。
- 未做：代码级 diff（网络不通，标待核对）、真实上游调用验证、任何代码修改。

### §三 #2 落地记录（2026-10-19，余额后台刷新+冷却自动解冻）

实现：`extensions/internal/pool/balance.go`（`HardCooledUIDs`/`ReviveHardCooledIfCredits`，零补丁）+ `extensions/internal/scheduler/balance.go`（周期循环，默认 5 分钟，`WB2A_BALANCE_REFRESH_MINUTES` 覆盖/关闭）+ `extension.go` 一行接线；10 个回归测试全绿，Docker race 通过。无补丁（全部新文件）。

与 panel 的有意偏差：
1. **只刷硬冷却号，不刷全池**——panel 每轮刷新全部账号；本仓只外呼处于 402 硬冷却的账号（通常 0~N 个）。理由：解冻是核心目标，软冷却/熔断由时间机制自恢复，正常账号余额由签到（每日两次）与对话路径保持新鲜；全池刷新对个人账号多出数百次/日的 billing 外呼，风控面不划算。
2. **解冻判据比签到解冻更严**——签到用的 `ReenableIfCredits` 不区分冷却类型；余额刷新场景只有硬冷却（`coolKind==CoolHard`）可解冻，软冷却（429 限流窗口未过）与熔断（chat 通道健康未证明）不得因「余额>0」被提前打回轮换。

通道统一性结论（八条旁路通道不适用本能力）：
- 旁路通道（zcode/qoder/opencode/autoclaw/trae/traeai/raccoon/freellm）的账号状态管理在各自 sidecar 进程内自洽，无「硬冷却等外部事件解锁」缺口：zcode/qoder/trae 系失败由自身重试与重启恢复；raccoon/autoclaw 的账号启停是显式控制动作；opencode/freellm 是供应商 key 池自轮换。core 的 402 CoolHard 是唯一「余额耗尽→等恢复」语义，余额刷新只在 core 有意义。
- sidecar 均无周期余额刷新同类行为（trae/autoclaw 有积分查询但为页签手动触发，非自动解冻闭环；上游权益到期时 zcode/trae 模型数归 0 是上游侧状态，非账号冷却，刷新无对象）。

### §三 #3 勘误（2026-10-20 复核：「调用来源 IP·UA 归因」本仓已有，调研时误判为缺失）

调研报告撰写（2026-10-19）时未对该项做 grep 断言即标缺失。2026-10-20 P1 #3 立项考证时实测推翻：

- **账本字段**：`extensions/internal/usagelog/usagelog.go` `Entry` 结构自带 `Src`（来源网段）/`Client`（UA 清洗自报）/`API`（协议入口）三字段；`Denied`（鉴权失败留痕）同样带 `Src`/`Client`。
- **成功路径落账**：补丁 `0008-call-usage-log.patch` 在 core 流式与非流式两个成功点调用 `RecordAuth(..., usagelog.OriginOf(r.Context(), r))`；`/v1/messages` 适配层同样接线（`anthropic/adapter.go`，含失败留痕 `RecordDeniedAuth`）。
- **来源传递链**：console `proxy.go` 的 `stripPrivateHeaders` 先剥客户端伪造的 `X-Console-Client`/`X-Forwarded-*` 同前缀头，再以 `clientAddress(r)`（含可信代理 CIDR 判定）覆写 `X-Console-Client`；core 侧 `OriginOf` 读该头，经 console 部署时不可伪造（源码模式裸跑 core 时按客户端自报处理，有注释与测试钉死）。
- **脱敏口径**：`MaskedPrefix` 收敛到网段（IPv4 /24、IPv6 /64，主机位不写盘）；`SanitizeClientText` UA 控制字符清洗+200 字节截断（字符边界回退）。比 panel 存完整 client_ip 更严。
- **前端展示**：`console/web/app.js` 调用统计表已有 `src` 列与 `clientLabel`（UA 认得出真名显示「真名（原始串）」，另配 `clientAgentNames` 通道标签）；鉴权失败概览展示「来源网段 × 次数」Top5。
- **旁路通道**：`console/usage_report.go` 旁路账本条目同样带 `Src`（原始对端地址）+`Client`（原始 UA），脱敏由 core 落账前收口完成——九通道口径统一，无需另做通道统一处理。
- **测试**：`TestOriginOfMasksInjectedClientAndCapsUserAgent`、`TestRecordAuthWiresNicknameFallbackAndExplicitZeroCredit`、console `proxy_test.go` 伪造头剥离断言、anthropic adapter 来源测试均在库。

结论：P1 #3 无需实现，本项关闭；真正缺失的相邻功能是 §三 #5「多 API Key + 按 Key 归因」（P2）。

### §三 #6 核对记录（2026-10-20：404 短冷却 60s 快照自带，无需实现）

- `upstream/internal/server/handler.go:53-57` `notFoundCooldown = 60 * time.Second`，注释明确：404 是上游**偶发**路径缺失而非限流，若共用 soft_rate（600s 起步+指数升级）会把好账号罚 10 分钟并逐次加倍，故固定 60s 防雪崩。
- 分类权威在 `upstream.Classify`（`upstream/internal/upstream/client.go`）：404 → `ErrNotFound`；`applyErrorPolicy`（handler.go:737-740）→ `Cooldown(uid, CoolSoft, notFoundCooldown, "upstream 404")`，不参与 softStreak 指数退避。
- 与 panel 语义一致，属快照基点自带（非本仓补丁引入）；该行为有 `handler_test.go` 覆盖。本项关闭。

### §三 #4 落地记录（2026-10-20，客户端 X-Request-Id + 成功率双口径）

实现：`console/proxy.go`（`newRequestID` 32-hex crypto/rand、`withRequestID`/`requestIDFrom` ctx 键、`observePublicExits` 生成点：响应头 `X-Request-Id` 回客户端 + 内部头 `X-Console-Request-Id` 传 core/旁路；入站 `X-Request-Id` 一律剥除恒服务端生成）+ `console/errors.go`（errorEvent 增 `RequestID`/`Produced`；完成口径 `produced := ok && wroteBytes>0 && !emptyStream`，`emptyStreamMarker` 精确匹配 core 空流兜底帧 `data: {"error":{"message":"empty upstream stream"`）+ `extensions/internal/usagelog`（Entry/Origin 增 RequestID，`SanitizeRequestID` ≤128 字节可打印 ASCII 含空白整体不留）+ bridge/console 两侧 usage_report 贯穿。管理端点新增 HTTP 成功率/完成成功率/平均耗时三卡与错误流水 request_id 列。

grill 拍板三处：请求 ID 恒服务端生成（防客户端撞号污染账本）；空流判定精确匹配空流帧前缀（「字节>0」被实测证伪——core 空流回 200+error 帧约 60 字节）；健康页错误区块扩展（不新建页签）。

通道统一性：core 与八条旁路通道统一贯穿（usage_report 同字段），与 panel 单网关形态不同处在于本仓是 console+九通道两层，请求 ID 在 console 出口层生成、经内部头进各通道账本。

### §三 #5 落地记录（2026-10-20，多 API Key 归因）

实现（commit e813afe）：
- **console 权威**（grill 拍板）：`console/apikeys.go` 附加 Key 清单（`apikeys.json` 与 routes.json 同目录、0600、原子落盘、损坏 fail-closed 降级只认主 Key、上限 32 条、标签「默认」保留给主 Key）；`apiKeyGate` 挂 `/v1/*` 与 `GET /status`（`/healthz` 保持免鉴权）——**清单为空时直通**（行为与现状完全一致，既有测试零改动全绿），清单非空时未命中自产 401（OpenAI 错误形态，401 也带 X-Request-Id 且进错误账本）。
- **出口换发**：core 通道 Rewrite 里按 ctx 标签统一换发 `Authorization: Bearer cfg.APIKey` + 注入 `X-Console-Key-Label`（附加 Key 明文不出 console）；旁路通道 Rewrite 本就换 sidecar key，天然可用；旁路 usage 上报带 `key_label`，sidecar 不感知。
- **core 落账侧**：usagelog `Origin`/`Entry` 增 `KeyLabel`（`OriginOf` 读 `X-Console-Key-Label` 头 + `SanitizeKeyLabel` ≤64 字节可打印 ASCII 含空白整体不留），补丁 0008/0014 调用点零改动；bridge usage_report 收口清洗。经 console 部署时不可伪造（stripPrivateHeaders 剥 `x-console-` 前缀），源码模式按客户端自报处理。
- **管理页签「API 密钥」**：清单整表编辑（添加/启停/删除进缓冲、保存整表提交即生效）、Key 自动生成（32 字节 base64url）、按 key_label 聚合用量（缺标签历史调用归「（未标注）」、缺失 tokens 不冒充 0、has_credit=false 记「—」）。

grill 拍板三处：鉴权权威 console（core 零补丁）；本批范围归因+报表（按 Key 限制可用模型留下一批）；主 Key 保留为「默认」标签不可删。

通道统一性：九通道账本统一带 key_label——core 经换发头落账、八条旁路经 usage_report 上报落账；按 Key 的模型限制（下一批）同样要在 gate 层统一实现，不逐通道做。

测试：console apikeys_test.go 6 例（校验/持久化降级/gate 三态/status+models 约束/上报贯穿）+ core usagelog/bridge 新增断言；console go 全量 + race（Docker golang:1.23）全绿，node 213/213，usagelog/bridge race 绿（bridge 既有 Windows 基线失败 Access is denied 在案除外）。

lab 实测补漏（commit 225b3a2）：`GET /v1/models` 在 publicRouter 中直发 core（mergedModels），不经过公共代理的 Rewrite，附加 Key 原样透传被 core 拒（英文 401）；对话端点走 retry 链换发正常，矛盾由 401 响应体中西文分辨 + `/status` 对照实验定位。修复为 mergedModels 按 ctx 标签换发主 Key，配套把 core mock 改为记录并断言 Authorization（原 mock 无条件 200 掩盖了缺陷）。lab 复测四态全过（附加 Key 200 出列表/无 Key 错 Key 401 中文/`/status` 200）。生产发版后核对 `zcode=0` 属公布语义（`enabled_models` 无 `glm-*` 直名即不上公共列表，调用面不受影响），非本批回归。

### §三 #7 落地记录（2026-10-20，每日限额护栏——简化两条先行）

实现（补丁 0017 + extensions 新文件）：
- **护栏 1 · 每日 Token**（hub 简化）：`extensions/internal/pool/quota.go` `DailyQuota`——按账号累计当日已知 token 观测（prompt+completion，-1 哨兵缺失不计），喂入点为 `usagelog.TokenObserver`（RecordAuth 落账后同步喂，账本侧 `extensions/internal/usagelog/usagelog.go` 新增 observer 挂点）；达额即 `CooldownQuotaUntil`（新 `CoolKind=CoolQuota`，until=本地次日 0 点）；重启重建由 `usagelog.DailyTokenTotals` 从当天 usage JSONL 按 uid 聚合（账本本身即持久化，不新增落盘文件）。`WB2A_DAILY_TOKEN_LIMIT` 环境变量开关（<=0 关闭，grill 拍板：环境变量注入 compose environment，与 WB2A_BALANCE_REFRESH_MINUTES 同模式）。
- **护栏 2 · 保留积分**：`Pool.reserveCredits`（`WB2A_RESERVE_CREDITS`，<=0 关闭）——credits 低于保护线的账号不参与轮换（normal healthy 口径、全冷却兜底、PickByUID/PickByUIDForModel 直取四处同口径）；credits 由签到/余额刷新更新后 healthy 实时复判自然回池，无需解封动作。
- **补丁 0017**（pool+server+main 三处）：①`CoolQuota` 冷却档（String()=quota_daily）；②`pick` healthy 口径并入 `belowReserve`；③`pickEarliestExpiryLocked` 排除有效 CoolQuota 与 belowReserve（防兜底击穿护栏）；④`reviveCoolingLocked` 不解封有效 CoolQuota（签到/余额刷新不误放，到 0 点由 until 自然解封）；⑤handler 末端新增 `QuotaGuard` 接口——全部非禁用账号达额时返回 **429 `daily_quota_exhausted` + Retry-After 指向本地 0 点**（grill 拍板，与 503 no_healthy_account 分流：429 是标准限流信号可指导客户端退避；保留积分护栏无日界恢复，维持 503 语义不适用 Retry-After）；⑥main 接线 `quotaLimits()`/`SetReserveCredits`/`Config.Quota`。

三个必踩坑的设计规避（现状核对发现）：护栏摘除走独立入口**不喂熔断器**（喂入会把全达额升级成指数级封禁拖长解封，与「pin 类错误不计入熔断统计」同口径）；签到解冻（ReenableIfCredits/reviveCoolingLocked）对有效 CoolQuota 不动作；全冷却兜底排除护栏号（否则全部健康号达额后兜底必中护栏号）。

pin/粘性交互（现状核对确认天然正确）：严格锁定走 PickByUIDForModel（校验 healthy），达额号不 healthy → 自动 503 pinned_account_unavailable，符合「不换号一律 503」在册规则；粘性号达额 → 解绑换号。

通道统一性结论（八条旁路不适用本能力，与 §三 #2 同构）：护栏依赖「账号级冷却/解封 + 按账号用量计数」这套 core 池语义；八条旁路 sidecar 账号状态进程内自洽——zcode/qoder 单凭据、opencode/freellm 供应商 key 池自轮换（freellm 自带 429/5xx 熔断冷却）、autoclaw/trae/traeai/raccoon 多账号启停为显式控制动作，均无「账号冷却等外部事件解封」缺口，上游 429 限流本身即兜底；旁路 usage_report 账本（uid=zcode 等）不进 core 池，不构成护栏对象。无同类行为需统一。

测试：pool `quota_test.go` 10 例（达额摘除/未知观测不计/跨天清零/不喂熔断/幂等/签到不误放/兜底排除/保留积分四处覆盖/RealmExhausted 六态/0 点窗口）+ usagelog `quota_test.go` 2 例（按日聚合/observer 喂入）+ server `quota_guard_test.go` 2 例（429+Retry-After/nil 守卫零回归，补丁内新增文件）；Docker golang:1.23 vet + 全量 + race 全绿；补丁链全新物化（core-quota-v2）复现验证通过。

### §三 #10 核对记录（2026-10-20：键膨胀落点已被补丁 0018 顺带消除，判空关闭）

立项时依据「粘性键已是内容派生（u0:原文），长 prompt 会键膨胀」。实测核对键膨胀的实际落点：`internal/session/ids.go` 的 `requestIDs sync.Map` 惰性缓存——以轮键原文为 key 无界增长（无会话键客户端每个对话轮新增一条长字符串 entry、永不删除），长 prompt 下才是真内存问题。补丁 0018（灰度移植批次）已顺带完成核心修复：

- `RequestIDForKey` 改 `sha256(盐|键)` 纯派生，删除整个 sync.Map 缓存（内存不再随键数增长）；
- `contentSignature`（extensions 新文件）对多模态非文本 part 只入 `[type:8-hex 摘要]`，data: base64 内联图不再进键；
- `TurnKey`/`firstUserText` 派生键仅在请求内瞬时存在（handler 循环外取一次作 sha256 输入），不驻留内存、不进日志、不落盘。

剩余可做的只有「纯文本形态也入哈希摘要」（对齐 panel d- 形态），收益为每请求省一次 O(prompt) 字符串拷贝；派生盐每进程随机、键值本不跨进程稳定，改了无兼容风险但也无实质收益。2026-10-20 拍板判空关闭，不再单独立项。

### §三 #12 落地记录（2026-10-21，积分到期分布）

实现（commit 6e0a76f）：
- **core 侧**：`extensions/internal/upstream/packages.go` `UserResourcePackages`——复用 `get-user-resource` 既有请求路径与 remain 口径（CycleCapacity 优先回退 Capacity），但逐包透出 `PackageName`/`PackageEndTime`/剩余，不再折叠成快过期桶；到期时间按 `softRateResetLoc`（UTC+8）解析成 RFC3339，解析失败降级「无到期日」不让整账号失败；零余额包剔除。`extensions/internal/bridge/credit_packages.go` `GET /internal/v1/credit-packages`——逐账号汇聚，成功缓存 10 分钟/失败 2 分钟（`refresh=1` 强制回源），3 worker 并发限速，单号失败固定文案入响应、原始错误仅入日志；按最早到期排序、错误条目垫底。
- **console 侧**：`/admin/credit-packages` 只读代理（管理会话+CSRF，query 全透传）；账号管理 pane 新增「积分到期分布」表——最早到期、包数、分布条（3 天/7 天/30 天/更远四档配色，段宽按账号内最大包剩余归一，悬停 tooltip 逐包明细）；首次进入账号页签自动查询一次（`creditPackagesLoaded` 幂等），手动刷新走 `refresh=1`；登出态重置回未查询。

通道统一性结论（书面考证：仅 core WorkBuddy 适用）：积分包/到期日是 WorkBuddy 上游 billing 的 `get-user-resource` 数据源特有概念；trae 国际版上游无积分体系（订阅档位展示在 traeai 页签），trae 国内版/autoclaw/raccoon 的积分在各自 sidecar 页签内自管（无「包到期」上游字段，autoclaw 积分刷新、raccoon 账单核对均不返回到期时间）；zcode/qoder 是订阅权益到期（sidecar /status 已展示）。到期分布查询走 core 内部凭据直连上游 billing，旁路 sidecar 无对应上游端点，无需也无法统一。

测试：upstream 3 例（逐包拆分升序/零余额剔除/CN 路径零回归/坏到期降级）+ bridge 2 例（聚合与缓存命中计费调用数/无凭据 503）；console go test 全绿，web_test 214 全绿（含新增结构断言：panel 归属 pane、refresh 接线、首次自动加载）。既有 Windows 基线失败（taskrun store.Put）在案除外。

### §三 #9 落地记录（2026-10-21，OpenRouter 价估算）

实现（commit c852d98 + 旁路快照补齐）：
- **快照语义**：`extensions/internal/usagelog/prices.go`——`RecordAuth` 落账前按当前价目表折算 `cost_usd` 写入 Entry，此后调价只影响新调用，历史账目不漂移；输入/输出 token 任一缺失（负值哨兵）或模型未定价均不折算（缺失≠0，与积分口径同款），`Summary` 增 `Cost`/`CostMissing`。
- **价目来源**：data 目录 `prices.json`（`Open` 时由 usage 目录推导 `../prices.json`），size/mtime 变化热重载（RecordAuth 热路径代价一次 stat），坏 JSON 沿用上次有效表、文件「从有到无」回落默认表；内置默认表**刻意为空**——启用模型全是内部命名（cn:/trae- 等前缀），OpenRouter 没有同名公布价，编数字即伪造；用户把公布价复制进 prices.json 即生效，键用剥前缀裸名，`priceCandidates` 九通道公共前缀循环剥离（`autoclaw-cn:glm-5.3-flash` 双层前缀可命中 `glm-5.3-flash`）。
- **console 侧**：调用统计页新增「等价花费」指标卡、通道分布与明细各加花费列（<0.01 给 4 位小数防「$0.00」假零）；全组未定价写「—」，未定价条数进底部说明，不伪造 $0。
- **旁路统一（九通道口径）**：旁路 usage 上报走 bridge `Usage.Record` 直落、不过 RecordAuth，初版会全部未定价——补 `usage_report.go` 落账层 `CostUSDFor(item.Model, ...)` 快照，旁路条目与 core 本地调用同口径；`TestUsageReportSnapshotsCost` 钉死（直名/前缀剥离/token 缺失三态）。

测试：usagelog 4 例（前缀剥离与折算矩阵/热重载与坏文件降级/落账快照与历史不漂移/JSON 往返）+ bridge 1 例；console go 全绿；web_test 216 全绿（2 条新花费断言 + 6 处列移位索引更新）。生产发版 core:local 22dcb3be4bd4 / console:local 60cffba4e95d，九通道探针全绿（zcode=0 上游验证码挑战在案、trae/traeai data 200 模型 0 属上游账号状态）。

## 七、待拍板事项

1. ~~P1 三项（#1 #2 #3）是否立项？~~ 已全部收尾（#1 补丁 0016、#2 落地记录见上、#3 勘误已有，见 §六）。
2. ~~限额护栏（#7）做全四条还是简化两条（每日 Token + 保留积分）先行？~~ 已拍板简化两条先行并落地（2026-10-20，见 §三 #7 落地记录）。
3. 成长任务一键完成（#8）做不做（成本高+风控敏感）？
4. ~~OpenRouter 价估算（#9）是否纳入后续统计批次？~~ 已落地（2026-10-21，见 §三 #9 落地记录）。
