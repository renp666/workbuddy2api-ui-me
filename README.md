# WorkBuddy2API

**将 WorkBuddy / CodeBuddy 反向代理为通用的 OpenAI 兼容 API。**

把已授权账号的模型能力转换为兼容 OpenAI Chat Completions 的接口，让支持自定义 Base URL 的客户端和应用通过同一个网关调用。配套 Web 控制台负责账号授权、运行状态、对话测试和自动任务，Docker Compose 一条命令即可启动。

> 本项目是非官方自托管网关。这里的“OpenAI 兼容”指模型列表与 Chat Completions 等已实现接口，不代表覆盖 OpenAI 的全部 API 或所有客户端功能。

## 核心功能：用 OpenAI 接口调用 WorkBuddy

- **统一接入地址**：使用网关的 `/v1` 地址和 API Key 接入客户端，不向客户端分发上游账号凭据。
- **模型列表与对话接口**：通过 `GET /v1/models` 获取模型 ID，通过 `POST /v1/chat/completions` 发起对话。
- **流式回答**：支持 Chat Completions 流式输出，适合聊天客户端和自己的应用。
- **Anthropic 文本兼容**：同一个 API Key 和模型列表也可通过 `POST /v1/messages` 调用，支持普通与流式文本对话。
- **多账号管理**：由网关维护账号池和冷却状态，网页可查看可用账号及调用情况。
- **网页辅助配置**：控制台提供 Base URL、API Key 和调用示例，并可直接测试模型回答。

![API 接入页面：Base URL、API Key 入口和调用示例](docs/superpowers/verification/2026-09-16-openai-api-access.jpg)

*API 接入截图来自本地隔离演示环境，未展示密钥。图中的 17864 是预览端口，正式部署默认使用 7863。*

### 客户端怎么填写

选择客户端的 OpenAI 兼容接口或自定义服务商入口，填写：

| 配置项 | 填写内容 |
| --- | --- |
| Base URL | `http://服务器地址:7863/v1`，公网部署建议使用自己的 HTTPS 域名 |
| API Key | 在控制台“API 接入”页面查看，不是网页登录的管理密钥 |
| 模型 | 从 `/v1/models` 获取的完整模型 ID；有 `cn:` 或 `global:` 前缀时需要保留 |

模型列表可能包含静态候选；能否实际调用取决于账号版本、权限、额度和上游状态，以真实回答为准。

### API 调用示例

先查询模型列表：

```bash
curl http://127.0.0.1:7863/v1/models \
  -H "Authorization: Bearer <你的 API Key>"
```

再使用列表中的模型 ID 发起流式对话：

```bash
curl -N http://127.0.0.1:7863/v1/chat/completions \
  -H "Authorization: Bearer <你的 API Key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<从模型列表选择的完整 ID>",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

以上是填写示例，请替换地址、密钥和模型 ID。

模型列表按账号池实际可用性过滤：只有至少存在一个可用账号（未禁用、不在冷却）的模型才会出现在 `/v1/models` 中，某平台（国内版/国际版）没有任何可用账号时整个平台的模型都不列出；每个模型条目带 `realm` 字段（`cn`/`global`，叠加 GLM 通道时另有 `glm`）标明归属平台，并带 `accounts` 清单（`uid` 与可选 `nickname`，按 UID 升序）列出多账号池中能服务该模型的账号。成功的对话响应会附带 `X-Account`（服务本次请求的账号昵称，昵称不适合展示时回落为 UID）与 `X-Account-Realm` 响应头，方便调用方定位实际使用的账号；失败响应不附带。控制台「API 接入」页的模型下拉同样带平台标签（国内版/国际版/GLM·智谱），多账号模型标注账号数（如「国内版 · cn:glm-5.2 · 3账号」），并在 GLM 通道启用时列出可选的 `glm-*` 模型及其调用示例。

### Anthropic 文本接口

在控制台“API 接入”切换到 **Anthropic**，查看地址和示例；“前往对话测试”会带上所选协议与模型。官方 Python SDK 的 Base URL 填服务根地址 `http://服务器地址:7863`，SDK 会追加 `/v1/messages`，不要再追加 `/v1`。

```bash
curl -N http://127.0.0.1:7863/v1/messages \
  -H "x-api-key: <你的 API Key>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<从模型列表选择的完整 ID，保留 cn: 或 global: 前缀>",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

`max_tokens` 必须是正整数；删除 `stream` 或设为 `false` 可获取普通 JSON 响应。支持 user/assistant 多轮消息、字符串或纯文本内容块，以及可选 `system`；不支持工具、图像、文件、扩展思考、缓存、beta、采样参数等高级字段，未支持字段会明确报错。

仅传递上游实际返回的用量，未知 token 数返回 `null`，不会估算或填 0。这与原生 API 的整数用量字段有差异，严格要求整数的客户端可能不兼容。已验证的 SDK 基线为 Python 3.12 + `anthropic==0.67.0`，不代表所有版本或客户端可用，也不代表完整支持 Claude Code；模型能力仍由 WorkBuddy / CodeBuddy 上游决定。

![Anthropic 文本接入页面](docs/superpowers/verification/anthropic-access-desktop-mock.jpg)

*截图来自隔离 mock 页面，未展示真实密钥，不代表真实模型调用验收。*

## 一条命令启动

准备一台已安装 Docker 和 Docker Compose 的 **Intel / AMD 64 位服务器（linux/amd64）**，将 [docker-compose.yml](docker-compose.yml) 保存到部署目录：

```bash
docker compose up -d
docker compose logs console
```

Compose 从阿里云仓库拉取成品镜像，**无需下载源码、构建镜像、准备 .env 或额外启动脚本**。私有镜像仓库需要先完成 Docker 登录。

1. 打开 `http://服务器地址:7863/`，使用日志中的“管理密钥”登录。
2. 进入“账号管理”，选择国内版或国际版，点击“浏览器授权”。
3. 在上游页面完成登录、扫码或验证码，再回到控制台等待结果；国际版如要求地区信息，按提示选择真实注册地区。
4. 授权成功后账号自动加载，无需重启。先在“对话测试”确认可用，再通过“API 接入”连接客户端。

登录和人工验证需要你本人完成，程序不会绕过激活或验证码。

## 可选：接入 GLM 编码套餐

本部署可以并列挂一个 [zcode-proxy](https://github.com/TriDefender/zcode-proxy) 容器，把你的智谱 GLM 编码套餐变成第三个模型来源。启用后仍是同一个 `:7863` 出口：控制台 `/v1/models` 会合并显示 GLM 模型，客户端请求 `model` 以 `glm-` 开头时由 console 自动转发给 zcode-proxy，其余模型仍走 WorkBuddy 账号池；本服务不管理 GLM 账号，额度与登录状态由 zcode-proxy 自持。

前置：拉取镜像并放置配置。zcode-proxy 容器与 console 共享网络命名空间，只监听 `127.0.0.1`，不对外暴露端口；凭据、配置和设备标识持久化在宿主 `runtime/zcode/` 下。

```bash
mkdir -p runtime/zcode
cp deploy/zcode.config.yaml runtime/zcode/config.yaml
```

配置模板默认国内智谱（`provider: bigmodel`）；使用 Z.AI 国际站改成 `provider: zai`。如需加密凭据，在 overlay 的 `ZCODE_PROXY_CREDENTIAL_SECRET` 设置一串你自己知道的口令并保持一致，否则容器重建后凭据无法解密（忘记口令只能重新登录）。

第一步，用 overlay 启动。容器未登录时也能常驻（代理处于停止态），登录和启用都在控制台内完成：

```bash
docker compose -f docker-compose.yml -f deploy/compose.zcode.yml up -d
```

第二步，打开控制台 **Zcode 页签**（页内分为「通道状态 / GLM 登录 / GLM 模型 / 对话测试」子菜单，按当前状态显示）：在「GLM 登录」选择服务商并点击「开始登录」，浏览器会自动打开智谱授权页面（若被拦截可点击页签内的链接；手机或任意设备浏览器均可，无需回调页面），完成登录后页签每 3 秒自动检测到并刷新状态；随后在「通道状态」点击「启用通道」，状态变为在线即可使用。页签还提供「停用通道」与「退出登录」（清除容器内凭据，二次点击确认）。

套餐档位（plan）：默认 `coding-plan` 走编码套餐的直连端点与永久 API Key；周末活动等赠送的试用额度属于 `start-plan`，走 zcode.z.ai 网关与 JWT 鉴权，通常只放行一两个免费模型（如 `glm-5.3-flash`）。切换档位需在页签「通道状态」卡中选择并点「切换档位」，**要求通道处于停用状态**（运行中上游会拒绝变更）；用哪个档位以上游实际授予你的额度为准，选错档位会报余额/资源不足（如 1113）。处于 `start-plan` 时，控制台会按档位过滤模型列表：页签「GLM 模型」与公共 `/v1/models` 只展示 `-flash` 类可用模型，避免客户端选到必然报错的模型；其它档位或控制端不可达时不过滤。

之后客户端用法不变：`/v1/models` 里选 `glm-*` 模型即可，OpenAI 与 Anthropic 两种协议都支持分流。Zcode 页签同时可查看通道状态与 GLM 模型列表，并直接做流式对话测试。未叠加该 overlay 时页签会置灰并提示未启用；只读模式下（未配置控制链路）页签仅展示状态与模型。不想要该通道时直接回到 `docker compose -f docker-compose.yml up -d` 启动即可，不影响原有服务。GLM 通道的可用性、额度和模型行为由 zcode-proxy 与其上游决定，本仓未对其做真实上游验收。

无法使用页签时（例如页签登录入口不可用），可在宿主机用一次性容器完成命令行登录，凭据文件与页签共用 `runtime/zcode/`：

```bash
docker run --rm -it \
  -e ZCODE_PROXY_CREDENTIAL_SECRET="<你的口令>" \
  -e ZCODE_PROXY_STORE_DIR=/data \
  -v "$(pwd)/runtime/zcode:/data" \
  --entrypoint bun ghcr.io/tridefender/zcode-proxy:4.7.0 \
  run src/index.ts auth login bigmodel
```

## 可选：接入 Qoder 国内版

本部署还可以并列挂一个 [qoder-proxy](https://github.com/avaritiachaos/qoder-proxy) 容器，把你的 Qoder 国内版账号变成第四个模型来源。与 GLM 通道相同，启用后仍是同一个 `:7863` 出口：控制台 `/v1/models` 会合并显示 Qoder 模型（统一加 `qoder-` 前缀并标注 `realm: qoder`），客户端请求 `model` 以 `qoder-` 开头时由 console 自动转发给 qoder-proxy，其余模型仍走 WorkBuddy 账号池；本服务不管理 Qoder 账号，额度与登录状态由 qoder-proxy 自持。

与 GLM 通道不同，Qoder 通道使用 **Personal Access Token（PAT）** 认证，没有登录/启停生命周期：在 qoder.com.cn 的「账号设置 → Integrations」页面创建 PAT，启动前通过环境变量传给容器即可，之后无需在控制台做任何授权操作。

```bash
# 方式一：临时环境变量
export WB2A_QODER_PAT="<你的 PAT>"
docker compose -f docker-compose.yml -f deploy/compose.qoder.yml up -d

# 方式二：写进部署目录的 .env（Compose 自动读取）
echo 'WB2A_QODER_PAT=<你的 PAT>' >> .env
docker compose -f docker-compose.yml -f deploy/compose.qoder.yml up -d
```

qoder-proxy 容器与 console 共享网络命名空间，只监听 `127.0.0.1`，不对外暴露端口；CLI 凭据缓存在宿主 `runtime/qoder/` 下。console 到 qoder-proxy 的内部调用可用 `WB2A_QODER_KEY` 加一层密钥（两个服务的该值保持一致）；留空则不校验——由于代理只监听 console 网络命名空间内的回环地址，外部无法直连。

之后客户端用法不变：`/v1/models` 里选 `qoder-*` 模型即可，OpenAI 与 Anthropic 两种协议都支持分流。控制台 **Qoder 页签**展示通道状态与模型列表，并可直接做流式对话测试；未叠加该 overlay 时页签置灰并提示未启用。不想要该通道时回到 `docker compose -f docker-compose.yml up -d` 启动即可，不影响原有服务。Qoder 通道的可用性、额度和模型行为由 qoder-proxy 与其上游决定，本仓未对其做真实上游验收。

## 配套 Web 控制台

### 对话测试

选择 OpenAI 或 Anthropic 协议、模型并发送问题，直接检查模型响应与流式显示。Anthropic 模式可设置最大输出 tokens，默认 1024；切换协议会清空当前测试对话，生成中可停止。回答完成后用量行会显示本次服务请求的账号与平台（来自响应归属头），Zcode 与 Qoder 页签的测试则分别标明 GLM 与 Qoder 通道。真实部署中的测试会消耗账号额度；页面内的对话刷新后清空。

![对话测试页面：选择模型并查看流式回答](docs/superpowers/verification/2026-09-16-openai-chat-playground.jpg)

*截图使用模拟回答和演示模型列表，仅展示界面，不是某个真实模型当前可用的证明。*

### 账号与运行状态

“运行概览”展示账号总数、可用状态、冷却情况和当前请求；“账号管理”用于浏览器授权，并查看账号最近观测到的积分、调用成功与错误记录。

### 自动任务

集中查看签到、猫猫旅行、活跃上报、Token 保活、开学季、夜猫子六类任务的开关、北京时间排程和执行历史。可手动执行已启用任务，查看账号结果与脱敏日志摘要。

![桌面端自动任务执行记录与详情](docs/superpowers/verification/2026-09-15-overlay-task-console-desktop.jpg)

*截图来自隔离测试环境，包含模拟账号，不代表真实奖励到账。*

任务是否可用、账号是否符合条件以及是否获得奖励，取决于上游平台与活动规则。保活等维护任务不等同于积分奖励；页面目前用于查看与执行，不提供修改排程或开关的功能。“立即执行”覆盖全部符合条件账号，不能强行运行已禁用任务。

### 手机端查看

控制台支持窄屏布局，方便在手机上查看状态和任务。

![手机端自动任务页面](docs/superpowers/verification/2026-09-15-overlay-task-console-mobile.jpg)

*手机端测试截图；示例任务时间和开关不是所有部署的默认配置。*

## 密钥与数据

默认自动生成管理密钥、API Key 和内部通信密钥，重启后复用。管理密钥用于登录网页，API Key 用于接口调用，两者不同。

如需自定义，在 Compose 的 **core 和 console 两个服务中**填写并取消相应注释，同名密钥保持一致：

```yaml
environment:
  TZ: Asia/Shanghai
  # WB2A_ADMIN_KEY: "" # 至少 32 字节，且与 API Key 不同
  # WB2A_API_KEY: ""
```

不配置或留空的项使用自动生成的值。手动配置不改写基础密钥，取消配置后恢复基础值；默认 Compose 不从 `.env` 读取密钥。

运行数据保存在部署目录中的 `runtime/wb2api/`：

| 子目录 | 内容 |
| --- | --- |
| `auths/` | 已授权账号凭据 |
| `data/` | 账号状态与任务历史 |
| `keys/` | 自动生成的密钥 |

普通重启和容器重建不会清空这些目录。备份时保存整个目录和实际使用的 Compose 文件，不要公开上传凭据或备份。

## 使用边界

- **保护密钥**：console 启动日志会显示管理密钥，不显示 API 或内部通信密钥。不要公开分享日志或含真实密钥的 YAML。
- **公网使用 HTTPS**：通过反向代理连接 console，并在 console 的 `environment` 中添加 `WB2A_PUBLIC_ORIGIN: "https://你的域名"`，不要带路径。console 自身只提供 HTTP：非回环监听且没有声明 HTTPS 地址时启动日志会给出一条警告，需要硬性保证时设置 `WB2A_REQUIRE_HTTPS: "1"`，不满足条件直接拒绝启动。声明了 HTTPS 地址时响应会带上 HSTS，明文部署不会。
- **反代后的登录限速**：管理登录按直连地址计数，经反向代理时所有访客会共用同一份额度。需要按访客分别限速时，把代理所在网段写入 `WB2A_TRUSTED_PROXY_CIDRS`（逗号分隔 CIDR），仅该网段提供的 `X-Forwarded-For` 会被采信。不要填 `0.0.0.0/0`：那等于信任所有直连对端，只在 console 仅经可信代理可达时才安全。
- **积分仅供观察**：待确认不等于零，余额差额不等于奖励；仅将上游明确返回的奖励展示为已确认。
- **遵守平台规则**：仅使用本人授权账号，不向未授权用户开放，不绕过平台验证。模型、额度、活动及服务可用性受上游规则影响。

## 开发与来源

本项目的血缘分三层，报告问题或对比安全修复时请先分清改的是哪一层：

| 层 | 仓库 | 职责 |
| --- | --- | --- |
| 原作者 | [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) | 账号池、调度器、上游客户端与 WAF/IP 级防护 |
| 二创 | [baiyea/workbuddy2api-ui](https://github.com/baiyea/workbuddy2api-ui) | 增加独立 Web 控制台、内部桥接、Anthropic 文本适配与 Docker 部署 |
| 本仓 | 本仓库 | 在二创基础上做安全加固与仓库卫生修正 |

上游业务层以源码快照方式引入（见 [upstream.lock](upstream.lock)）。快照基点落后原作者 `master` 约 214 个提交，因此原作者后续新增的安全加固（WAF IP 级熔断、管理端点、会话 ID 签名等）**不在本仓**。这是快照策略的已知取舍，需要时按 [AGENTS.md](AGENTS.md) 的上游更新流程对齐。

架构、开发、测试、上游更新和镜像发布说明见 [AGENTS.md](AGENTS.md)。源码构建入口保留在 [docker-compose.build.yaml](docker-compose.build.yaml)。

遵循 [MIT License](LICENSE)，再分发时请保留原作者版权声明与许可。
