# AI 开发指南

本文面向在本仓库工作的 AI 编程助手，维护架构、开发、验证、上游更新与镜像发布约定。`README.md` 面向使用者，主打 **将 WorkBuddy / CodeBuddy 反向代理为 OpenAI 兼容 API**；Web 控制台是配套能力，不要把 README 写成开发手册。

## 优先遵守

- 当前处于开发阶段，以当前设计为准，不为历史版本新增兼容层、迁移分支或双轨配置。
- 不直接修改 `upstream/`。新增 core 能力放 `extensions/`，修改上游既有文件通过 `patches/`，控制台直接改 `console/`。
- 生产部署保持 `docker compose up -d` 一条命令启动，不新增宿主启动脚本、独立 init 服务或服务器端构建依赖。
- 生产 Compose 的 `environment` 默认只有 `TZ` 和注释形式的可选密钥；固定内部参数内置在镜像。不重新引入 `.env` 插值或必填密钥。
- 保留相对目录挂载、源码构建入口和 `linux/amd64` 发布架构，不新增 Docker 健康检查。
- 修改前检查 `git status --short`，保留用户已有改动；不能为通过验证而重置工作区、清空数据或覆盖真实配置。
- 更新上游、发布镜像、推送 Git、重启实际服务分别需要对应授权。发布镜像不等于部署授权。

## 架构

```text
浏览器 / OpenAI 或 Anthropic 文本客户端
            │ :7863
            ▼
console（独立 Go 服务，内嵌 HTML/CSS/JavaScript）
  ├─ /admin/*：管理会话、CSRF、网页操作
  │                 └─ 桥接密钥 → core /internal/v1/*
  └─ /v1/*、/status、/healthz：公共接口代理 → core
                                                    │
                                  同一个账号池、上游客户端、调度器
                                                    │
                                          WorkBuddy / CodeBuddy
```

- **core**：固定上游加扩展和补丁，负责账号池、公共 API、OAuth、任务执行及持久化。不得为控制台另建账号池或调度器。
- **console**：负责网页、管理认证和代理；不加载账号凭据，不直接执行签到等业务。
- 可选并列 zcode-proxy（`deploy/compose.zcode.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `glm-` 前缀分流到 zcode-proxy，`/v1/models` 合并两个上游列表；由 `WB2A_ZCODE_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 zcode 建账号池或调度。`/admin/*` 管理链路默认不经过 zcode-proxy，仅网页 Zcode 页签的 `/admin/zcode`、`/admin/zcode/chat` 两端点直连它（console 注入 `WB2A_ZCODE_KEY`，剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `glm-` 前缀，不作为通用转发口）。overlay 下 zcode-proxy 以 android entry 常驻并 `network_mode: service:console` 共享 console 网络命名空间，代理与控制 API 均只监听 `127.0.0.1`；`WB2A_ZCODE_CONTROL_URL` 配置后，页签经 `/admin/zcode/login|config|enable|disable|logout` 驱动容器进程内 OAuth 登录、provider/plan 档位切换与代理启停（console 零 Docker 权限，与 WorkBuddy 桥接授权同构；plan 仅 `coding-plan`/`start-plan`，运行中上游拒绝变更），未配置时页签退化为只读状态展示。
- 可选并列 qoder-proxy（`deploy/compose.qoder.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `qoder-` 前缀分流到 qoder-proxy，`/v1/models` 合并两个上游列表（qoder 模型加 `qoder-` 前缀和 `realm: qoder` 标签）；由 `WB2A_QODER_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 qoder 建账号池或调度。`/admin/*` 管理链路默认不经过 qoder-proxy，仅网页 Qoder 页签的 `/admin/qoder`、`/admin/qoder/chat` 两端点直连它（console 注入 `WB2A_QODER_KEY`，剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `qoder-` 前缀，不作为通用转发口）。overlay 下 qoder-proxy 以 `network_mode: service:console` 共享 console 网络命名空间，只监听 `127.0.0.1`；qoder-proxy 无控制面/OAuth/生命周期，由环境变量 `WB2A_QODER_PAT` 提供 Personal Access Token，console 只读状态与对话测试，无启停控制。
- 可选并列 opencode-proxy（`deploy/compose.opencode.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `opencode-` 前缀分流到 OW Bridge sidecar，`/v1/models` 合并上游列表（opencode 模型加 `opencode-` 前缀和 `realm: opencode` 标签）；由 `WB2A_OPENCODE_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 opencode 建账号池或调度。`/admin/*` 管理链路默认不经过它，仅网页 OpenCode 页签的 `/admin/opencode`、`/admin/opencode/chat` 两端点直连（console 注入 `WB2A_OPENCODE_KEY` 并剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `opencode-` 前缀）。OW Bridge 硬约束：全部路由要求 `Authorization: Bearer`、任何带 `Origin` 头的请求一律 403，因此代理必须 `Del("Origin")` 并注入 Bearer（entrypoint 与 console 共享同一密钥文件）。overlay 下 sidecar 以 `network_mode: service:console` 共享 console 网络命名空间，只监听 `127.0.0.1:41980`；镜像由 `deploy/opencode.Dockerfile` 从 `louchi1984-coder/ow-bridge` 源码构建，基础必须是 Debian（opencode 运行时二进制为 glibc 构建，alpine 跑不动），首启在持久卷 `runtime/opencode/` 内下载 opencode 运行时；entrypoint 清理残留 `service.pid` 锁文件并预写 api-key。opencode-proxy 无控制面/OAuth/生命周期，console 只读状态（透传其 `/health` 的 phase/版本/模型探测分级与在途探测）与对话测试，无启停控制；旁路 usage 上报以 `uid=opencode`、账号名「OpenCode 通道」落 core 账本，模型名保留公共前缀。
- 可选并列 autoclaw-proxy（`deploy/compose.autoclaw.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `autoclaw-` 前缀分流到 autoclaw2api sidecar，`/v1/models` 合并上游列表（autoclaw 模型加 `autoclaw-` 前缀和 `realm: autoclaw` 标签）；由 `WB2A_AUTOCLAW_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 autoclaw 建账号池或调度。`/admin/*` 管理链路默认不经过它，仅网页 AutoClaw 页签的 `/admin/autoclaw`、`/admin/autoclaw/chat` 与登录/账号/积分/维护控制端点直连（console 注入 `WB2A_AUTOCLAW_KEY`，剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `autoclaw-` 前缀，不作为通用转发口）。sidecar 是 Python 自研桥接（`deploy/autoclaw2api/bridge.py` + `check.py`，镜像由 `deploy/autoclaw.Dockerfile` 从 python:3.12-slim 构建），数据面只实现 OpenAI `/v1/chat/completions`，Anthropic `/v1/messages` 客户端不适用（分流到 sidecar 会得到 404，console 侧 `autoclawProxy` 的 ErrorHandler 只在 sidecar 不可达时给出 503 错误包）；数据与凭据持久化在宿主 `runtime/autoclaw/`，overlay 下 sidecar 以 `network_mode: service:console` 共享 console 网络命名空间，只监听 `127.0.0.1:7865`，`WB2A_AUTOCLAW_KEY` 为空时 bridge 视为免鉴权。与 qoder/opencode 只读页签不同，AutoClaw 页签有完整控制链路：国内版手机号+短信验证码登录（验证码 60s 重发倒计时）、国际版 OAuth 回调导入与桌面客户端凭据导入、账号启停/删除、积分刷新、维护与逐账号签到（签到成功后级联维护），控制端点超时 15s、签到/维护逐账号超时 180s；旁路 usage 上报以 `uid=autoclaw`、账号名「AutoClaw 通道」落 core 账本，模型名保留公共前缀。
- 可选并列 trae-proxy（`deploy/compose.trae.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `trae-` 前缀分流到 trae-proxy sidecar，`/v1/models` 合并上游列表（trae 模型加 `trae-` 前缀和 `realm: trae` 标签）；由 `WB2A_TRAE_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 trae 建账号池或调度。`/admin/*` 管理链路默认不经过它，仅网页 Trae 页签的 `/admin/trae`、`/admin/trae/chat` 与登录/账号/积分/排程/用量控制端点直连（console 注入 `WB2A_TRAE_KEY`，剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `trae-` 前缀，不作为通用转发口）。sidecar 是从 AgentPoints 的 trae 实现移植的纯 Go 网关（`deploy/trae2api/`，模块 `workbuddy2api-trae`，镜像由 `deploy/trae.Dockerfile` 构建）；数据与凭据持久化在宿主 `runtime/trae/`，overlay 下 sidecar 以 `network_mode: service:console` 共享 console 网络命名空间，只监听 `127.0.0.1:7866`。Trae 页签是带控制链路的完整页签：OAuth 登录（console 把 sidecar 返回的 `login_url` 的 `auth_callback_url` 改写成 `http://127.0.0.1:<调用方实际访问 console 的端口>/authorize`：trae.cn 授权页在服务端（SSR）对该参数做形状校验，主机必须是字面量 `127.0.0.1`、路径必须是 `/authorize`，端口任意，`localhost` 或 `/admin/trae/authorize`、乃至缺该参数都会让 SSR 返回空数据并渲染「登录失败 网络错误，请刷新页面重试。」；因此回调实际落在 console 根路径 `GET /authorize`（公开无 CSRF，与 `/admin/trae/authorize` 共用同一处理器），由它把回调 query 代理到 sidecar `/authorize`。该硬约束也使浏览器必须与 console 同机 —— 远程浏览器访问 `127.0.0.1` 只会打到它自己）、凭据导入（回调 URL 或凭据 JSON；容器已有更新凭据时原样透传 409 不覆盖）、账号启停/删除/刷新、积分刷新、排程查看与立即签到/立即刷新凭据、通道用量；控制端点超时 15s，签到/积分/凭据刷新 180s。与 autoclaw 不同，trae 控制链路用标准 HTTP 状态码表达业务结果（400/404/409 原样透传），前端因此不需要 autoclaw 那种 200 + error 包络的解包函数；sidecar `/status` 的账号条目没有凭据有效期，`adminTraeStatus` 再取一次 `/admin/api/accounts` 按 uid 合并 `expires_at` / `expired_soon` / `has_auth`，任一侧失败只降级缺字段、不影响主响应。旁路 usage 上报以 `uid=trae`、账号名「Trae 通道」落 core 账本，模型名保留公共前缀。
- 可选并列 traeai-proxy（`deploy/compose.traeai.yml`，默认不启动）：**与 trae-proxy 同一个镜像**，只是 `TRAE_EDITION=intl`，把同一套 sidecar 打到 Trae 国际版（trae.ai）上游。console 公共三出口按 `traeai-` 前缀分流，`/v1/models` 合并（`traeai-` 前缀 + `realm: traeai`）；由 `WB2A_TRAEAI_URL` 开关，未设置时公共出口与现状完全一致。两条 Trae 通道的公共面（页签路由、探测、用量归类、别名保留前缀、通道色）各自登记一份，console 侧实现由 `console/trae.go` 的 `traeChannel` 描述符驱动，注册走循环不再复制第二份。前缀必须互不包含：`trae-intl-` 会以 `trae-` 开头而被国内版分支先剥名，因此国际版另起 `traeai-`。区域差异全在 `deploy/trae2api/trae/upstream/edition.go`：对话与模型目录 `coresg-normal.trae.ai`（SG 入口，可用 `TRAE_AGENT_HOST` 换 `coreva-normal`/`core-normal`）、鉴权与订阅态 `ug-normal.trae.ai`、授权页 `www.trae.ai/authorization`；`client_id` 与 `function` 作用域两区共用（由「是不是 SOLO」决定，不由区域决定）。**国际版没有签到与积分体系**：`/trae/api/v2/ug/checkin_credits/*` 与 `ide_user_ent_usage` 在国际 host 上一律 404（对照同路径打 `api.trae.cn` 返回 JSON 信封），权益改读 `/trae/api/v1/pay/ide_user_pay_status` 并显示订阅档位——**不折算成积分数字**，上游从没给过那个量；页签因此没有签到按钮，`POST /admin/traeai/schedule/checkin` 直接 409。国际版的主要凭据入口是**粘贴桌面客户端 `storage.json` 全文**（容器读不到宿主 `%APPDATA%`；sidecar 用 Go 标准库解 `tc`/AES-128-CBC 信封，零第三方依赖），凭据自带的 `host` 落进 `Auth.ApiHost` 并优先于进程默认值——国际版凭据被塞进国内版进程时也不会被改写成国内 host，否则 ExchangeToken 会打到错误区域且静默失败。`/authorize` 根路径两条区域共用，由「谁发起登录」分发，无提示时回落国内版。数据持久化在宿主 `runtime/traeai/`，只监听 `127.0.0.1:7869`。旁路 usage 上报以 `uid=traeai`、账号名「Trae 国际版通道」落 core 账本。⚠️ overlay 给 sidecar 注入的是 `TRAE_API_KEY`（二进制真正读的名字）而不是 `WB2A_TRAEAI_KEY`；`deploy/compose.trae.yml` 至今注入的是 `WB2A_TRAE_KEY`，那条通道因此实际处于免鉴权状态（回环内不致命），属在册未修项。
- 可选并列 raccoon-proxy（`deploy/compose.raccoon.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `raccoon-` 前缀分流到 raccoon-proxy sidecar，`/v1/models` 合并上游列表（raccoon 模型加 `raccoon-` 前缀和 `realm: raccoon` 标签）；由 `WB2A_RACCOON_URL` 开关，未设置时公共出口与现状完全一致。core 固定上游语义不变，console 不为 raccoon 建账号池或调度。`/admin/*` 管理链路默认不经过它，仅网页 Raccoon 页签的 `/admin/raccoon`、`/admin/raccoon/chat` 与账号导入/启停/删除/刷新/积分/用量控制端点直连（console 注入 `WB2A_RACCOON_KEY`，剥离管理 Cookie、CSRF 与部署 API Key；对话端点强制校验 `raccoon-` 前缀，不作为通用转发口）。sidecar 是参考 aimod-cc/agent2api（MIT+限制）自建的纯 Go 网关（`deploy/raccoon2api/`，模块 `workbuddy2api-raccoon`，零第三方依赖，镜像由 `deploy/raccoon.Dockerfile` 构建），上游为小浣熊 `xiaohuanxiong.com` 的 OpenAI 兼容 LLM 端点（Bearer access_token、SSE 中把上游内部模型名回写成请求名）；小浣熊登录态依赖宿主 DPAPI，Docker 下无网页登录，页签入口是凭证 JSON 导入（409 保护已有更新凭据），控制链路与 trae 同构、用标准 HTTP 状态码表达业务结果；每日积分链路为桌面 grant → setting_info 触发 → 账单核对（权威结果，失败降级），无原生签到。数据与凭据持久化在宿主 `runtime/raccoon/`，overlay 下 sidecar 以 `network_mode: service:console` 共享 console 网络命名空间，只监听 `127.0.0.1:7867`。旁路 usage 上报以 `uid=raccoon`、账号名「Raccoon 通道」落 core 账本，模型名保留公共前缀。
- 可选并列 freellmapi（`deploy/compose.freellm.yml`，默认不启动）：console 公共 `/v1/chat/completions`、`/v1/messages`、`/v1/responses` 按请求 `model` 的 `freellm-` 前缀分流到 FreeLLMAPI sidecar，`/v1/models` 合并上游列表（加 `freellm-` 前缀和 `realm: freellm` 标签）；由 `WB2A_FREELLMAPI_URL` 开关，未设置时公共出口与现状完全一致。与前几条旁路的分工不同：本通道的定案是**供应商池归 sidecar、网关只做出口与账**——供应商 key 池、池内路由与 fallback、429/5xx 熔断冷却、key 轮换、粘性会话、响应缓存全部留在 sidecar，console 不重复实现（拆到网关只会得到一个更差的碎片版本），因此它的页签是 opencode 型的**只读状态 + 对话测试**，没有账号/登录/签到/用量控制链路。sidecar 是上游官方镜像 `ghcr.io/tashfeenahmed/freellmapi`（不是本仓源码构建），数据落在宿主 `runtime/freellm/`（SQLite），overlay 下以 `network_mode: service:console` 共享 console 网络命名空间、只监听 `127.0.0.1:7868`。两个必须知道的运行条件：① 镜像把 `NODE_ENV=production` 烤死，production 强制要求 64 位 hex 的 `ENCRYPTION_KEY`（上游 README：必填无默认，且换 key 会让已存的供应商 key 全部解不开），非 production 又要求手动跑迁移——没有"降级到开发模式"这条捷径；② 统一 API key 由 sidecar 首启在 SQLite 里生成，没有声明式入口，取法是首启后从 `runtime/freellm/freeapi.db` 的 `settings.unified_api_key` 读出再回填 `WB2A_FREELLMAPI_KEY`，console 只认这把统一 key、绝不持有供应商 key。模型目录由 sidecar 自己从 `api.freellmapi.co` 周期同步（实测一次同步 357 new），**与供应商 key 是否配齐无关**，所以 `/v1/models` 有货不代表能调用：无供应商 key 时对话一律 503 `no_providers_configured`，判通道可用性必须看实对话。上游自己实现了 `/v1/messages` 与 `/v1/responses`，因此本通道**不在**"仅支持 OpenAI 协议"的旁路名单里（与 autoclaw/trae 相反）。旁路 usage 上报以 `uid=freellm`、账号名「FreeLLMAPI 通道」落 core 账本，模型名保留公共前缀。诊断面用三个统一 key/免鉴权端点，各自回答一件事且都不进 sidecar 的分钟限流器（`/v1/models` 进）：`/livez` 答进程活着、`/readyz` 答池内有无此刻能服务的上游并给机器可读原因档（`no_upstreams_configured` / `all_upstreams_rate_limited` / `all_upstreams_unhealthy` / `db_unreachable`）、`/v1/providers` 答逐平台 key 健康与顶层 `counts` 汇总，`/v1/quota-forecast` 答日帽余量与距重置秒数（`used/remaining/limit/remaining_pct` 可能为 null，未知不拿 0 冒充）。对外目录按上游 `execution_status` 裁剪：`needsKey`/`exhausted` 不进 `/v1/models` 合并、别名候选与探测种子（三处共用 `freellmCallable` 同一判据），字段缺失的虚拟条目（`auto`/`fusion`）保留由调用时判定；页签同时给全集条数与可调用条数，因为「目录 259 条」与「能调用」是两件事——overlay 因此把 `PROXY_RATE_LIMIT_RPM` 显式抬到 600（console 剥 `x-forwarded-*` 且旁路共享 console 网络命名空间，棱镜全部流量在 sidecar 眼里是同一个回环桶，默认 120 会把整条通道钉死）。每次旁路调用把 `X-Routed-Via`、`X-Fallback-Attempts/Trail`、`X-FreeLLM-Cache` 记一条归因日志：账本只有 token 数，谁真正服务了这次调用只存在于响应头。
- 仅 console 发布宿主端口。core 只在 Compose 网络内访问；console 仅只读挂载密钥目录，不挂载账号和状态目录。
- 当前 README 重点介绍 `/v1/models`、`/v1/chat/completions` 与流式调用；不得把“OpenAI 兼容”宣传为完整覆盖所有 OpenAI API 或客户端功能。
- `/v1/messages` 由 core 的 Anthropic 文本适配器在进程内复用原 OpenAI Handler；不另建账号池、HTTP 回环或协议代理服务。只在启用 core 桥接的分支包装，未启用桥接的源码模式保持原 Handler。

## 目录与代码入口

| 路径 | 职责 |
| --- | --- |
| `upstream/`、`upstream.lock` | 上游普通源码快照；锁文件记录来源、commit 和源码摘要。 |
| `extensions/cmd/server/extension.go` | core 初始化、密钥、桥接和任务生命周期接线。 |
| `extensions/internal/bridge/` | 内部管理接口，复用公共能力。 |
| `extensions/internal/anthropic/` | Messages 文本请求、普通响应与增量 SSE 适配，传递取消与真实用量。 |
| `extensions/internal/oauth/`、`extensions/internal/pool/` | 授权流程、账号热加载等扩展；`pool/availability.go` 提供补丁 0009 的按模型可用性静态健康口径。 |
| `extensions/internal/scheduler/`、`extensions/internal/taskrun/` | 任务目录、执行观察、单运行器和持久历史。 |
| `extensions/internal/server/`、`extensions/internal/session/`、`extensions/internal/usagelog/` | 补丁 0006-0009 与 0018 配套的扩展文件与回归测试（`wafip.go`、`session_gc_race_test.go`、调用统计账本、`account_header.go` 与 `model_filter_test.go` 的归属头；0018 的会话头派生 `conversation.go`、上游码标签 `upcode.go` 与连败降权 `pool/degrade.go`），移除条件见 `patches/README.md`。 |
| `extensions/scripts/` | Python 任务结果事件与测试。 |
| `patches/series`、`patches/README.md` | 补丁顺序、修改原因、验证方式和移除条件。 |
| `console/main.go`、`console/server.go`、`console/proxy.go` | 配置、管理会话、路由与代理；`console/zcode.go` 为可选 glm- 分流与模型列表合并，`console/zcodecontrol.go` 为页签内登录/启停控制链路；`console/qoder.go` 为可选 qoder- 分流与 Qoder 页签只读状态；`console/opencode.go` 为可选 opencode- 分流与 OpenCode 页签只读状态（含 OW Bridge /health 透传）；`console/autoclaw.go` 为可选 autoclaw- 分流与 AutoClaw 页签（含登录/账号/积分/维护控制链路）；`console/trae.go` 由 `traeChannel` 描述符同时驱动 **trae-（国内版）与 traeai-（国际版）两条通道**：页签路由与公共分流按描述符循环注册（含 OAuth 登录轮询、账号/排程/权益/用量控制链路，`/status` 与 `/admin/api/accounts` 按 uid 合并凭据有效期），新增一条 Trae 区域只加一个描述符条目，不再复制第二份 handler；`console/raccoon.go` 为可选 raccoon- 分流与 Raccoon 页签（`console/raccooncontrol.go` 为凭证导入/账号/积分/用量控制链路，上游 xiaohuanxiong.com）；`console/ccr.go` 为可选 CCR 页签，只借宿主 claude-code-router 的管理 RPC 启停它的模型网关（控制台无宿主进程权限，`ccr` 服务本身仍须宿主机 `ccr ui` 拉起），管理 Token 每次请求现读挂载的 `service.json`，不写进环境变量、不出现在日志与状态响应里。 |
| `console/web/`、`console/web_test.cjs` | 无前端框架的页面资源及 Node 测试；通过 Go embed 打包。 |
| `deploy/` | Dockerfile、core 镜像启动入口、配置、各可选旁路的 overlay（`compose.{zcode,qoder,opencode,autoclaw,trae,traeai,raccoon,freellm}.yml`，以目录实际文件为准）和隔离验收工具。 |
| `scripts/overlay.py` | 源码物化、补丁标识和上游更新。 |
| `scripts/check.sh`、`scripts/acceptance.sh` | 本地检查和真实容器隔离验收。 |
| `scripts/local-release.sh`、`scripts/channel-probe.sh` | 本机发版（不发布镜像，见「本机发版」）和只读通道诊断（网关活体 + 旁路通道端口与合并模型数）。 |
| `scripts/release.sh`、`scripts/release.py` | 时间戳镜像发布入口与实现。 |
| `docs/superpowers/plans/`、`docs/superpowers/specs/` | 任务书、交接班记录与设计文档；进行中的安全回溯工作以此为准。 |
| `docs/superpowers/verification/` | 验证记录、界面截图；模拟素材不代表真实上游结果。 |

## 上游与补丁机制

唯一上游为 `https://github.com/Sliverkiss/workbuddy2api`。实际版本以 `upstream.lock` 为准，不在多份文档中重复写死 commit。

本仓是三层血缘的第三层，排障和评估安全面时必须分清改的是哪一层：

1. `Sliverkiss/workbuddy2api`（原作者）—— 账号池、调度器、上游客户端与 WAF/IP 级防护。
2. `baiyea/workbuddy2api-ui`（二创）—— 在原作者基础上增加 `console/`（网页控制台）、`patches/`、`extensions/`（桥接、Anthropic 适配、任务记录）与容器化部署。
3. 本仓（三创）—— 只改 `console/`、文档与仓库卫生，不动上游业务层。

`upstream/` 是**历史快照**，不等于原作者当前 `master`：快照基点比原作者 `master` 落后约 214 个提交，因此原作者后续的安全加固（如 `internal/server/wafip.go` 的 WAF IP 级熔断、`internal/server/admin.go`、`internal/session/*` 的会话 ID 签名、以及多处数据竞争修复）**不在本仓**。这是快照策略的已知取舍，不是本次改动引入的回归；要对齐需走下面的上游更新流程。评估「本仓有没有某个安全修复」时，先确认它在哪一层；其中 WAF 熔断分类与 session GC 停止竞态已通过补丁 0006、0007 定点回溯进本仓（配套扩展文件与移除条件见 `patches/README.md`），其余原作者加固仍不在。

`scripts/overlay.py prepare` 的步骤：校验源码摘要 → 复制快照到新目录 → 复制扩展文件 → 按 `patches/series` 执行 `git apply --check` 并应用补丁。不会改写 `upstream/`。

1. 新文件和测试放到 `extensions/` 的对应相对路径；不能通过扩展覆盖已有上游文件。
2. 需要改既有上游文件时，在物化目录验证后维护补丁，同时更新补丁说明和必要测试。
3. `.build/` 是临时产物。不能只改物化代码而不回写扩展或补丁。
4. `console/` 是独立 Go 模块，不向上游公共 Handler 注入网页管理实现。
5. 上游提供等价行为且相关回归测试无需补丁也通过时，才移除对应补丁。

### 更新上游

仅在明确授权后执行；构建相关目录须无未提交改动，显式指定 commit 或 tag：

```bash
python3 scripts/overlay.py update --ref COMMIT_OR_TAG
git diff -- upstream upstream.lock
git diff -- patches extensions deploy console scripts
```

脚本在候选目录验证补丁、扩展、测试及隔离容器验收；成功才更新快照和锁文件。失败保留诊断候选，不自动提交、推送、发布或重启服务。不要绕过摘要检查、强行应用失败补丁或手改锁文件掩盖不一致。

## 开发与测试

开发机需要 Git、Bash、Python 3、Go、Node.js、curl、Docker Compose 和 Buildx。Go 最低版本以各模块 `go.mod` 为准，镜像使用 Go 1.23；竞态测试需要支持 CGO 的本机编译环境。部署成品镜像的服务器不需要这些开发工具。

以下命令从仓库根目录执行。根目录不是 Go 模块，不要直接在根目录运行 `go test ./...`。

```bash
python3 -m unittest discover -s scripts -p 'test_*.py' -v
python3 -m unittest discover -s deploy -p 'test_*.py' -v
bash scripts/check.sh
docker compose --env-file /dev/null -f docker-compose.yml config --quiet
git diff --check
```

`check.sh` 在新临时目录物化 core，运行 Go 测试、vet、关键包竞态测试、console 竞态测试、Node 页面测试和 Python 任务测试。

定点调试时使用尚不存在的输出路径：

```bash
python3 scripts/overlay.py prepare --output .build/core-review
go -C .build/core-review test ./internal/bridge ./internal/taskrun
go -C console test -race ./...
node --test console/web_test.cjs
```

修改后重新物化，旧物化目录不会自动同步；重复运行使用新的输出路径，不删除不明来源的目录。

涉及镜像、Compose、启动参数、密钥或持久化时，还需运行 `bash scripts/acceptance.sh`。它在隔离项目、端口和存储中验证直接启动、YAML 密钥覆盖与取消覆盖、日志、时区、数据保留以及 mock API/任务流程。禁止将测试指向真实凭据或数据；模拟验收不证明真实上游授权成功或奖励到账。

### Anthropic 文本验证

公共 `/v1/messages` 使用同一个模型 API Key（`x-api-key`）和固定 `anthropic-version: 2023-06-01`。`New(next, apiKey, maxBodyBytes)` 仅截获该路径，其他 OpenAI 路径不变；未知用量保持 `null`，不得伪造零或宣称完整 Claude Code 兼容。

网页 `POST /admin/messages` 沿用管理会话、同源、CSRF 和退出取消，代理至 `/internal/v1/messages`（console 仍会附上会话 owner 头，但 bridge 对该路径只校验 owner 格式，真正的 owner 绑定只存在于 OAuth 流程接口；同一部署内多账号共用 `conversationId` 是预期行为，不要把它当成隔离边界去加补丁）。请求 envelope 为 `{conversation_id, request}`；ID 限 `[A-Za-z0-9_-]{1,128}`，request 为完整 Anthropic 文本请求，不加私有字段。bridge 有界读取并验证 envelope，通过 `WithConversation` 注入可信上下文，由 adapter 写入 OpenAI `conversationId`；不接受公共私有头伪造会话。`server.max_body_mb` 限制的是请求体（含管理 envelope）；非流式响应体另有 `adapter.go` 内置的 32 MiB 上限，两者不是同一个开关。

SDK 仅用于隔离测试，不进入镜像或生产依赖。使用 Python 3.12 临时虚拟环境安装 `anthropic==0.67.0 httpx==0.28.1` 后执行：

```bash
# 内存契约：未知/晚到用量、raw events、text_stream、最终消息聚合
/path/to/venv/bin/python scripts/check_anthropic_sdk.py
# 实际访问验收脚本创建的隔离 mock 网关；执行后沿原机制清理
WB2A_SDK_PYTHON=/path/to/venv/bin/python bash scripts/acceptance.sh
```

验收保留 OpenAI 流式断言，并覆盖 Anthropic JSON、SSE、错误包和管理入口；`WB2A_SDK_PYTHON` 未设置时仅跳过 SDK 步骤，不能将这次运行报告为 SDK 通过。实际网关 SDK 验收与内存 fixture、真实上游调用分开记录。Python 3.14 不作为 SDK 0.67.0 验收环境；不得为绕过解析失败而删断言。

新增行为先补能失败的回归测试，再做最小修改。纯文档改动核对事实、命令和链接即可，不因此重建发布镜像。只报告有实际执行结果的验证。

### 界面截图

可复用现有隔离预览，不接触真实账号、容器或上游服务：

```bash
WB2A_BROWSER_PREVIEW=1 go -C console test -run '^TestAdminBrowserPreview$' -v -timeout 20m
```

预览地址和测试登录方式以该测试输出为准；使用完关闭预览进程。图片保存到 `docs/superpowers/verification/`，README 用相对路径引用并标明模拟环境。不要展示真实密钥、账号信息，或通过修改 DOM 伪造成功响应。

## Docker 与运行配置

- `docker-compose.yml`：只拉取成品，两个镜像同时间戳、`linux/amd64`，对外端口 `0.0.0.0:7863:7863`。
- `docker-compose.build.yaml`：继承运行版，以本地 `:dev` 标签构建，只读挂载 `deploy/default-config.json`。两种入口共享数据目录，不应同时启动。
- 两个服务 YAML 保留 `TZ: Asia/Shanghai` 和可选密钥注释。core Dockerfile 内置 core 模式、监听地址、账号/状态路径；console 内置 core 地址、监听地址和密钥文件路径。
- core 入口调整三个挂载根目录为 UID/GID 10001、权限 700，再降权 exec 业务进程；不递归改写既有文件。console 以普通用户运行。
- 生产不定义 Docker 健康检查；console 等待 core 生成共享密钥。`/livez` 表示存活，空账号时 `/healthz` 可以返回 503。

| 宿主相对路径 | 容器路径 | 用途 |
| --- | --- | --- |
| `./runtime/wb2api/auths` | `/app/auths` | core 账号凭据 |
| `./runtime/wb2api/data` | `/app/data` | core 状态和任务历史 |
| `./runtime/wb2api/keys` | `/run/wb2a` | core 写入，console 只读 |

密钥缺失或为空时，使用自动生成并持久化到 `runtime/wb2api/keys/keys.json` 的基础密钥。YAML 覆盖不改写基础文件，取消覆盖恢复基础值；两个服务的同名覆盖必须一致。

管理和桥接密钥至少 32 字节，API Key 非空，三者不同；非法配置应失败，不静默替换。按产品要求，console 启动日志显示有效管理密钥，但禁止输出 API/桥接密钥。不创建或回写 `.env`，不修改宿主或其他容器的环境变量。

维护 `.gitignore`、`.dockerignore` 的边界，不提交或打包真实密钥、配置、账号、备份、`runtime/`。仓库使用 `.gitattributes` 固定 `eol=lf`：工作区可能是 CRLF（Windows `core.autocrlf=true`），但索引与提交一律 LF，新增文件不要混入裸 LF 片段。公网代理需在 console 的 `environment` 指定 `WB2A_PUBLIC_ORIGIN` 为完整 HTTPS origin（不带路径）；不得关闭同源、CSRF、会话归属或桥接认证。console 只在明文 HTTP 上监听：非回环监听且未声明 HTTPS origin 时启动会打一条警告，`WB2A_REQUIRE_HTTPS=1` 才把该情形改成拒绝启动（默认关闭）。仅在声明了 HTTPS origin 时下发 `Strict-Transport-Security`，明文部署不下发以免把浏览器钉死在本进程不提供的方案上。登录限速按连接对端分桶，反代后会把所有来源塌缩成一个桶；只有 `WB2A_TRUSTED_PROXY_CIDRS`（逗号分隔 CIDR，非法即启动失败）列出直连代理时，才取该请求单个 `X-Forwarded-For` 的最右段作为客户端地址，其余判定一律仍用直连对端；配置成 `0.0.0.0/0` 或 `::/0` 等于信任全部直连对端，启动会打警告，仅当 console 只经可信代理可达时才安全。

需要自定义业务配置时，保存到 `runtime/wb2api/config.json`，通过显式只读挂载启动：

```bash
docker compose -f docker-compose.yml -f deploy/compose.config.yml up -d
```

该命令需要实际部署授权。缺失路径、目录或非法 JSON 拒绝启动；后续操作保持相同的 `-f` 参数。API Key 覆盖必须由两个服务共享，不能只改 core 的 JSON。

## 账号与任务语义

- 六类任务 ID 为 `checkin`、`travel`、`activity`、`keepalive`、`school`、`cat`。网页读 core 调度器的目录，不维护第二份开关或排程。
- 手动运行覆盖全部符合条件账号，不能强行启动禁用任务，也不能扩展成任意 shell 执行入口。
- 保留单运行器、幂等请求与持久历史语义；历史损坏不能绕过记录继续调度。
- 未知积分或奖励不能变成零；余额差额、脚本退出码不能证明到账，只采集上游明确的奖励事件。
- OAuth 登录、扫码、验证码、必要的地区选择由用户完成，不绕过验证或激活。
- 账号更新先持久化再发布到现有账号池，保留凭据并发快照与账号状态，避免旧刷新覆盖新授权。
- 管理会话、公共 API Key、内部桥接密钥是不同权限，不能混用。

## 构建与发布

仅构建本地镜像、不启动服务：

```bash
docker compose -f docker-compose.build.yaml build
```

成品镜像发布到下列固定仓库，每次使用同一个新的 Unix 秒级时间戳：

```text
registry.cn-hangzhou.aliyuncs.com/cateyes/go:wb2api-core-<timestamp>
registry.cn-hangzhou.aliyuncs.com/cateyes/go:wb2api-webui-<timestamp>
```

获得发布授权、核对测试并提交本次构建输入后执行：

```bash
docker login registry.cn-hangzhou.aliyuncs.com
bash scripts/release.sh
```

复用已有 Docker 登录，不读取或输出凭据文件。发布脚本：检查标签未占用 → 测试 → 构建两个 amd64 镜像 → 隔离验收 → 推送 → 回拉比对镜像 ID → 原子更新 Compose 标签。

- 不跳过验收，不复用已发布时间戳，不维护 `latest`，串行发布。
- 网络或鉴权错误不等于标签不存在。部分推送失败不自动删标签，查明原因后用新时间戳重试。
- 回拉检查使用当前登录身份，不代表匿名拉取可用；不得擅自修改仓库公开权限。
- 成功后审阅并提交 Compose 标签，交付版本和验证结果；脚本不推送 Git、不部署服务器。
- 当前仅发布 `linux/amd64`，Apple Silicon 可模拟运行，不得误发 ARM 镜像。
- 保留 LICENSE 和上游来源；镜像中不包含用户数据。

确有需要时显式传入构建代理，例如本机 Docker Desktop：

```bash
WB2A_BUILD_HTTP_PROXY=http://host.docker.internal:7890 \
WB2A_BUILD_HTTPS_PROXY=http://host.docker.internal:7890 \
bash scripts/release.sh
```

`WB2A_BUILD_*` 也适用于 `scripts/acceptance.sh`，仅传入构建步骤。不要把此地址设置为宿主 HTTP_PROXY/HTTPS_PROXY，或硬编码为项目默认值；Docker 后台拉基础镜像的代理由 Docker 自身管理。

### 本机发版（不发布镜像时）

本机入口由 `docker-compose.override.yml` 钉住，该文件是**本机专用**、已被 `.gitignore` 排除，生产入口仍然是 `docker compose up -d` 只读 `docker-compose.yml`。override 做两件事：把 core/console 钉到 `workbuddy2api-core:local` 与 `workbuddy2api-console:local` 并加 `pull_policy: never`（`:local` 缺失时 `up -d` 直接失败，不会静默回退到 `docker-compose.yml` 里钉的已发布镜像对），以及用 `include:` 引入 `deploy/compose.{zcode,qoder,opencode,autoclaw,trae,traeai,raccoon,freellm}.yml`，让一条命令同时对齐 core、console 与全部已登记的旁路（名单以 `docker-compose.override.yml` 里的 `include:` 为准，别在这里数条数）。

`include` 必须写成 `path:` + `project_directory: data` 的长形式。默认形式以被引入文件所在目录解析相对路径，overlay 里的 `./runtime/zcode` 会变成 `<root>/deploy/runtime/zcode`，实测把凭据和运行时数据挂空并静默新建目录。

override 本体不入库，内容存一份 `deploy/compose.local-entry.example.yml`；本机文件丢失时按字面复制回去：`cp deploy/compose.local-entry.example.yml docker-compose.override.yml`。

每次更新内容后发版：

```bash
bash scripts/local-release.sh            # 脏工作区默认拒绝
bash scripts/local-release.sh --allow-dirty
```

同机还有一套可选**演练环境** `prism-lab`（`deploy/compose.lab.yml`，默认不启动），用来在改生产码时当安全垫：把 core/console 钉到 `:lab` 标签（`pull_policy: never`）、宿主端口 7864（容器内监听仍是镜像内置 `:7863`）、数据与密钥落在 `runtime/lab/`、默认不含任何旁路 overlay。产出 `:lab` 复用同一个发版脚本，只是走 `--tag` 分叉（不滚动 `:local`/`:local-prev`、不重启生产）：

```bash
bash scripts/local-release.sh --allow-dirty --tag lab
docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml up -d
docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml down
```

`-p prism-lab` 不能省（省了会挂进生产 project）。lab 与生产是两套独立 project，互不共用 `runtime/wb2api/`；要验证某条通道时再叠加该通道的 overlay，并使用 lab 专属凭据。用 `-f` 叠加（不是 `include`）时相对路径按 project 目录解析，实测 `./runtime/lab/keys` 落在 `<root>/runtime/lab/keys`。

构建上下文是 `git archive HEAD`，未提交的改动不进镜像，所以先提交再发版。标签滚动为 `:local`（在用）+ `:local-prev`（上一个可回退），其余 dangling 由脚本末尾 `docker image prune -f` 清掉。回退：

```bash
docker tag workbuddy2api-console:local-prev workbuddy2api-console:local
docker tag workbuddy2api-core:local-prev workbuddy2api-core:local
docker compose up -d
```

开机自启不需要宿主脚本：Docker Desktop 注册在 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`，服务都是 `restart: unless-stopped`，实测 `docker desktop stop` + `start` 与整机重启后全部自动恢复（2026-10-06 冷启动读数：五只 Up、镜像仍是 `:local` 而非已发布对、healthz/livez 200、`bash scripts/channel-probe.sh` 一次读全旁路通道；该读数早于 autoclaw 接入，未含它），daemon 自己按依赖顺序先起 console 再起旁路。

必须知道的代价与约束：

- 旁路以 `network_mode: service:console` 绑定 console 的**容器 ID**。单独重建 console 会让全部旁路永久 `Exited (255)`，`State.Error` 为 `joining network namespace of container: No such container`，此时 `restart: unless-stopped` 也救不回来。带 override 的 `docker compose up -d` 会在同一次调用里连旁路一起重建，因此不要改用更短的 `-f` 链。
- zcode 的「启用」是一次性动作，不是每次开机都要点：启用状态落在挂载卷的 `runtime/zcode/config.yaml`。实测冷启动（真重启电脑）后无人点击，`scripts/channel-probe.sh` 直接读到 8080 服务 `glm-` 模型；单独 `docker restart` 旁路容器后约 9 秒同样自行恢复。只有从未启用过的全新挂载才需要先点一次。
- CCR 通道是宿主机进程，不是本仓的容器：override 给 console 加 `WB2A_CCR_URL: http://host.docker.internal:3458` 与 `WB2A_CCR_TOKEN_FILE: /run/ccr/service.json`，并把 `%APPDATA%\claude-code-router\service.json` 单文件只读挂进去（CCR 配置目录里还有它的 sqlite 库和 provider profiles，控制台不该看到）。实测两条闸：非回环 `Host` 恒 `403 unexpected Host header`（`console/ccr.go` 一律改写成 `127.0.0.1:<port>`），无 `x-ccr-web-auth` 恒 `401`；Token 真源是 `service.json` 的 `url` 查询参数 `ccr_web_token`，不是同文件里的 `serviceToken`。VirtioFS 下单文件只读绑定会跟随宿主的原子替换，实测 CCR 重启换 Token 后无需重建容器即读到新值；仍不匹配时页面报「Token 不匹配」而不是「不可达」。
- 旁路都在 console 的网络命名空间里，宿主回环看不见它们，探针必须 `--network container:<console>` 进去，并且**发回环 Host**：qoder-proxy 拒绝非回环 Host，用 `Host: probe` 这类自造值会拿到一条看着像故障的 `403 unexpected Host header`。opencode 的 `/health` 裸探恒返回 `401 Local proxy API key required`（按设计只认 console 注入的 Bearer），它自己的 `/v1/models` 条目数会浮动——同一天实测合并列表 6 → 冷启动后 2，而 bridge 侧 `/health` 报 `phase=ready`，console 只透传不补齐。

清理尺度上别动的几样：`wb2a-gomod`、`wb2a-gomodcache`、`wb2a-go-cache`、`wb2a-gobuildcache` 四个卷是「不联网也能重建」的前提；**凡被入库 Compose 按名字钉住的镜像都不进清理范围**（`docker-compose.yml` 钉的已发布对就是本机生产入口的离线保底，删了它等于让 tracked 文件指向本机不存在的镜像，而这一步从 `git status` 上看不出来）；BuildKit 缓存约 16 GB 中仅约 0.9 GB 可按年龄回收，`--filter until=168h` 与 `--reserved-space` 实测都清不动其余活跃部分，全量 `builder prune -a` 会连离线重建能力一起删掉，除非确认网络可用否则不做。发版脚本内也不要全局导出 `MSYS_NO_PATHCONV=1`，它会反过来破坏 `git archive -o` 的路径写入，上下文一律写仓库内相对路径。被删掉的代际要在 `docs/superpowers/verification/` 留下 image ID 再删。

## 交付

README 用中文介绍功能和使用，AGENTS 维护技术事实。版本以 Compose 和 `upstream.lock` 为准，避免在文档重复写死。不得在文档工作中顺便删 `.github/`、用户数据或其他历史文件。

交付时说明修改点、实际验证、是否发布/部署，以及保留的无关工作区改动；未做的操作不声称已完成。
