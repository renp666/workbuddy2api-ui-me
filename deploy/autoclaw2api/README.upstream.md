# autoclaw2api

把 [AutoClaw](https://autoclaw.z.ai/) 云端沙箱封装成 **OpenAI 兼容 API** 的聚合网关（Python 实现）。单端口同时服务**国内（cn）**与**国际（global）**两个区，支持多账号池、积分加权轮换、冷却/禁用状态机、token 自动续期、每日签到。

> 仅供学习与技术交流。使用前请阅读并遵守 AutoClaw 的 [服务条款](https://www.z.ai/agreement)、[隐私政策](https://www.z.ai/privacy) 及各适用地区法律法规。请勿用于对外收费转售、暴力破解、账号共享滥用等违反条款的行为；请合理控制并发，避免造成平台负载压力。

## ⚠️ 使用须知：提示词注入与违法行为会导致对话被拦截（务必先读）

本网关本身**不做任何内容安全判定，也不提供任何绕过手段**；判定发生在上游模型侧，本网关只做原样透传。

**禁止项**

- **提示词注入（prompt injection）**：在对话、代码、注释、文件名、工具输出、README/网页内容中夹带「忽略上述指令」「你现在是……」「输出你的系统提示词」等指令性文本，均属攻击行为，禁止使用。
- **违法违规用途**：绕过验证码 / 反爬与风控、批量注册或买卖账号、滥用免费额度、诈骗、侵权、生成恶意代码等，一律禁止。

**被拦截时的表现**

上游不会返回 HTTP 错误码。它会把固定哨兵字符串（实测为 `DANGEROUS_ACTIVITY_BLOCKED`）作为**正常助手回复的正文**返回：HTTP 200、`finish_reason: stop`、内容长度恰好等于该字符串。因此它在编辑器里看起来像一条普通消息或「客户端报错」，实际是模型拒绝回复。

**请不要试图规避**

该判定基于对话内容的语义，**换账号、换区域（cn/global）、换模型、重试或改请求头都不会改变结果**。本网关**不会**为绕过该判定做自动重试、自动换 session 之类的处理——被拦截即视为请求内容本身有问题，不做兼容。

**被拦截后怎么办**

回到请求内容本身：删掉注入/违规部分，然后重新、清楚地描述你的合法需求。

## 特性

- **OpenAI 兼容**：`/v1/chat/completions`（支持 `stream`）+ `/v1/models` + `/status` + `/healthz`
- **国内 + 国际双区**：单端口同时服务 `cn` 与 `global` 账号，模型用 `cn/` / `global/` 前缀、`X-AutoClaw-Region` 头或 body `"region"` 字段路由
- **多账号池**：自动扫描 `auths/autoclaw-*.json`，积分 **Top5 加权随机** 挑号，避免并发撞号
- **状态机**：余额不足→长冷却、429→短冷却、连续错误→中冷却、token 失效→禁用，全部落盘 `data/`
- **自动维护**：每 30 分钟 token 预刷新 + 积分刷新（恢复的账号自动解冻）+ 沙箱确保/唤醒
- **每日签到**：独立工具 `check.py`，`task_id=daily_signin`，服务端幂等

## 技术栈

Python 3.10+（`bridge.py` 依赖 `httpx` + `websockets`；`check.py` 纯标准库，零依赖）。

## 目录结构

```
.
├── bridge.py           # 主服务：HTTP 网关 + 账号池 + 调度器 + Web Console
├── check.py            # 每日签到工具（CLI，独立维护；Console 签到页复用其逻辑）
├── console.html        # Web Console 单页（状态 / 模型 / 签到）
├── config.json         # 服务配置（不入库）
├── config.example.json
├── auths/              # 账号凭证（不入库）
├── data/               # 池状态（不入库）
└── docs/               # 逆向备忘
```

## 快速开始

### 1. 配置 `config.json`

```jsonc
{
  "listen": "127.0.0.1:7865",
  "api_key": "",            // 留空不鉴权；建议设为随机长串
  "auth_dir": "./auths",
  "mode": "auto"            // auto | openai | relay | proxy
}
```

### 2. 运行服务

```bash
pip install httpx websockets   # 仅 bridge.py 需要
python3 bridge.py -config config.json
```

> **所有模型统一走 cn 沙箱 relay**（`x-openclaw-model` 头指定后端模型），无桌面端依赖。2026-09-23 起 zai-proxy 直连被阿里 WAF 406 拦截（`X-Request-Model: zai_*` 头触发 Bot 规则），已回退 relay 方案——cn relay 实测 tdpsk_*/zaicoding_*/zai_* 全通。

### 3. 打开 Web Console

```bash
open http://127.0.0.1:7865/          # 或 /console
```

单页控制台（`console.html`，由 bridge 托管），含三个页签：

| 页签 | 功能 |
|---|---|
| 状态 | 账号池健康/积分/冷却（5s 自动刷新）+ healthz |
| 模型 | Model List + 在线测试对话（stream/非流式） |
| 签到 | 一键签到 / dry-run、查任务列表（复用 `check.py` 逻辑） |

若配置了 `api_key`，页面会弹窗要求输入。

### 4. 调用 API

```bash
# 国内
curl http://127.0.0.1:7865/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"cn/openclaw","messages":[{"role":"user","content":"你好"}],"stream":true}'

# 国际
curl http://127.0.0.1:7865/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"global/zai_auto","messages":[{"role":"user","content":"hi"}],"stream":true}'

# 全部模型
curl http://127.0.0.1:7865/v1/models
```

也可用请求头 `X-AutoClaw-Region: cn|global`，或 body 里加 `"region":"cn|global"` 指定通道。

### 5. 每日签到

Web Console「状态」页：
- 账号池上方：**一键签到国内** / **一键签到海外** 两个按钮
- 账号行内：每账号独立签到按钮

CLI / API：

```bash
python3 check.py --dry-run     # 预览将请求什么（不发真实请求）
python3 check.py               # 全部账号签到
python3 check.py --region cn   # 仅国内
python3 check.py --status      # 只查任务列表，不签到
python3 check.py --json        # 机器可读输出

# API：region=all|cn|global，可选 uid 指定单账号
curl -X POST http://127.0.0.1:7865/api/checkin \
  -H 'Content-Type: application/json' \
  -d '{"region":"cn","dry_run":false}'

curl 'http://127.0.0.1:7865/api/tasks?region=all'
```

签到端点：`POST {region基址}/autoclaw-proxy/proxy/autoclaw-task-complete`，body `{"task_id":"daily_signin"}`。服务端幂等（`already_completed=true` 表示今日已签），重复执行安全。建议 crontab 每天跑一次。

### 6. 查看状态

```bash
curl http://127.0.0.1:7865/status
```

## Model List

`/v1/models` 同时返回两个区的模型。

### 国内（cn）

无前缀（或 `cn/` 前缀）走沙箱 relay / OpenAI 端点：

| model id | 说明 | 通道 |
|---|---|---|
| `openclaw` | 默认 agent | 沙箱 relay |
| `openclaw/default` | 默认 agent 稳定别名 | 沙箱 relay |
| `glm-5.3-flash` | 后端模型（`x-openclaw-model` 头） | 沙箱 relay |
| `glm-5.2` | 后端模型 | 沙箱 relay |
| `zai_auto` | Auto 自动选模 | 沙箱 relay / 本地 model-proxy |
| `zai_auto-fast` | 快速自动（图文） | 本地 model-proxy |
| `zai_glm-5.3-flash` | GLM-5.3-Flash（图文） | 本地 model-proxy |
| `zaicoding_glm-5.3` | GLM-5.3 Coding | 本地 model-proxy |
| `tdpsk_deepseek-v4-flash-202605` | DeepSeek-V4.1-Flash | 本地 model-proxy |
| `tdpsk_deepseek-v4-pro-202606` | DeepSeek-V4-Pro | 本地 model-proxy |

### 国际（global）

`global/` 前缀，走桌面端本地 model-proxy（model-config 远程拉取 + 静态回退）：

| model id | 说明 |
|---|---|
| `global/zai_auto` | Auto 自动选模 |
| `global/zai_auto-fast` | 快速自动（图文） |
| `global/zaicoding_glm-5.3` | GLM-5.3 Coding |
| `global/tdpsk_deepseek-v4-flash-202605` | DeepSeek-V4.1-Flash |
| `global/tdpsk_deepseek-v4-pro-202606` | DeepSeek-V4-Pro |
| `global/zai_glm-5.3-flash` | GLM-5.3-Flash（图文） |
| `global/zai_glm-5-turbo` | GLM-5-Turbo |
| `global/anthropic_claude-sonnet-5` | Claude Sonnet 5（agent-config 白名单） |
| `global/anthropic_claude-opus-5` | Claude Opus 5（agent-config 白名单） |

> 模型 id 与可用性以上游 `autoclaw-model-config` / `autoclaw-agent-config` 实时返回为准，上表为 2026-09-22 逆向快照。

### 模型路由规则

| 模型形态 | 路由 |
|---|---|
| `openclaw` / `openclaw/*` / `agent:*` | CN 沙箱 relay（后端模型经 `x-openclaw-model` 头） |
| `global/*` | 桌面端本地 model-proxy |
| `zai_*` / `zaicoding_*` / `tdpsk_*` | 桌面端本地 model-proxy（不分区，route 头自动带） |
| `cn/xxx` 且 xxx 为沙箱模型 | CN 沙箱 relay |

## 上游通道

| 通道 | 端点 | 适用 |
|---|---|---|
| CN 沙箱 relay | `POST {proxy}/api/electron/agent/send` + `GET {proxy}/api/events`（SSE） | `openclaw*`；需先 `wss://.../v1/client/ws` + `auth.inject` 上线设备 |
| CN 沙箱 OpenAI | `POST {proxy}/v1/chat/completions` | 网关开启 `chatCompletions` 时可选 |
| 本地 model-proxy | `POST 127.0.0.1:<port>/internal/model-proxy/v1/chat/completions` | global 与 `zai_*`/`tdpsk_*`；复用桌面端已登录会话，免 client-sign |

端点基址、签名、事件流等细节见 `docs/reverse-notes.md`。

## 账号池与错误分类

| 上游表现 | 处理 |
|---|---|
| 余额/额度不足（`402` 或「余额不足/insufficient」） | 冷却 12h |
| HTTP `429` | 冷却 60s |
| 连续报错达阈值（默认 3） | 冷却 10m |
| token 失效（`410000`/`401`/`Unauthorized`） | 禁用，需人工重登凭证 |
| 所有账号不可用 | `503 no_healthy_account` |

积分每 30 分钟拉一次，余额恢复后自动解冻被冷却账号。

## 环境变量覆盖（`AC2A_*`）

| 变量 | 覆盖配置项 |
|---|---|
| `AC2A_LISTEN` | `listen` |
| `AC2A_API_KEY` | `api_key` |
| `AC2A_AUTH_DIR` | `auth_dir` |
| `AC2A_STATE_FILE` | `state_file` |
| `AC2A_MODE` | `mode` |
| `AC2A_HARD_CREDIT` | `cooldown.hard_credit`（秒） |
| `AC2A_SOFT_RATE` | `cooldown.soft_rate`（秒） |
| `AC2A_ERR_THRESHOLD` | `cooldown.err_threshold` |
| `AC2A_ERR_COOLDOWN` | `cooldown.err_cooldown`（秒） |
| `AC2A_MAINTENANCE_MINUTES` | `schedule.maintenance_minutes` |
| `AC2A_USER_API_BASE` | `upstream.user_api_base_override`（调试） |

## 两区账号说明

- **国内（cn）**：手机号 + 短信验证码登录（`/userapi/v1/agent-send-code` → `/userapi/v1/agent-login/`），凭证落盘 `auths/autoclaw-*.json`，`region` 填 `cn`。
- **国际（global）**：Google / Z.AI OAuth 登录（`SendCode` 不适用）。本项目不实现 OAuth 前端，可手工把 `accessToken` 填入凭证文件并设 `"region": "global"`。对话通道依赖桌面端本地 model-proxy（桌面端需保持登录）。

userapi 基址：
- 国内：`https://autoglm-acceleration-api.zhipuai.cn`
- 国际：`https://autoglm-api.autoglm.ai`

## 测试

```bash
python3 -m py_compile bridge.py check.py
python3 check.py --dry-run
```

## 免责声明

本项目与 AutoClaw / Zhipu / Z.AI 无隶属关系。接口与截至编写时点的逆向约定，可能随上游更新失效；使用风险自负。
