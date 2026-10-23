# 任务交接：FreeLLMAPI 免费池通道接入棱镜网关（代码全绿，待上线）

> 交接原因：代码与验证全部完成，剩余「console 镜像重建 + 供应商 key + 冒烟」三步。
> 本文档自包含，新 agent（Qoder）无需原会话任何上下文即可执行。
> 日期：2026-10-16。仓：`D:\work\02toolsMy\workbuddy2api-ui-me`（棱镜网关）。

## 0. 一句话定位

FreeLLMAPI（`tashfeenahmed/freellmapi`，MIT，v0.14.2 @ bf166045）作为**第 8 通道 `freellm`** 接入棱镜：免费 overflow 池 + 能力面补充（embeddings/转写）。**收益口径是产能转移/积分降幅，不是质量升级。**

**两层职责切分（定案，接手必读）**：

| 归属 | 职责 |
| --- | --- |
| sidecar（FreeLLMAPI）自管 | 供应商 key 池、池内路由与 fallback、429/5xx 熔断冷却、key 轮换、粘性会话、响应缓存、提示压缩 |
| console（棱镜）自管 | 别名与模型榜单、6 组排序、积分经济、timeout≥60s / retry=1、`X-Routed-Via` 透传日志、用量记账 |

**风险红线（不可违反）**：
- **Cohere 禁用**（试用条款禁个人用途）；**NVIDIA / Cerebras 不注册**；反向 custom provider（指回棱镜）不建（成环）。
- 供应商白名单只 5 家：Gemini、Groq、Cloudflare、Mistral、OpenRouter。
- **敏感数据不进此通道**（Groq/Gemini 免费档保留 prompt 训练权利）。
- FreeLLMAPI 自述 personal experimentation only，**不接生产/共享负载**。

## 1. 已完成且已验证（不要重做）

### 1.1 上游升级（已收口）
- FreeLLMAPI 本地仓 `D:\work\03clones\03-freeLLM\FreeLLMAPI`：`291556d → bf166045`（v0.14.2，288 提交，gh-proxy.com fetch + `--ff-only`）。
- `git ls-remote` 确认远端 main = 本地 bf166045，**上游无更新**。

### 1.2 棱镜侧代码（全部接线完成）

| 文件 | 改动 |
| --- | --- |
| `console/freellm.go`（**新**，235 行） | `freellmModelPrefix` 常量、`freellmProxy()`（剥前缀 + Authorization: Bearer 统一 key + stripPrivateHeaders）、`stripFreellmModelPrefix`、`adminFreellmStatus`（GET /admin/freellm）、`adminFreellmChat`（POST /admin/freellm/chat） |
| `console/freellm_test.go`（**新**，276 行） | `freellmPair` 助手 + 分流/剥前缀/私有头不泄漏/目录合并/统计记账/健康/别名校验全链路 |
| `console/route.go` | `channelFreellm` 枚举、`routeChannelOrder` 尾插、`modelPrefixFor`、Web 名、`validateTarget`、`channelUpstream`（FreellmURL+FreellmKey）、`channelEnabled`、seed 循环、`reservedAliasPrefixes` 加 `freellm-` |
| `console/health.go` | freellm 健康判定走 `fetchChannelModels`（拉 `/v1/models`），失败文案「模型目录不可达」；**故意不用 LLM 探测**（避免探活烧免费额度） |
| `console/probe.go` | `probeableChannels` 加 freellm；admin 探测校验文案含 freellm |
| `console/server.go` | `Config.FreellmURL/FreellmKey`、NewServer 规范化剥 path、mux 注册 `/admin/freellm`、状态 JSON 加 `freellm_enabled` |
| `console/main.go` | `WB2A_FREELLMAPI_URL` 解析（无效报错）+ Config 装配 `WB2A_FREELLMAPI_KEY` |
| `console/zcode.go` | freellm 代理创建、公共 `/v1` 三协议按 `freellm-` 前缀分流、mergedModels 加 freellmRaw goroutine（FreellmURL+Key 拉 `/v1/models`、`tagModelRealm(tagged,"freellm")`）、totalLen/all append |
| `console/usage_report.go` | usage capture `case "freellm"` → `e.Account = "FreeLLMAPI 通道"`，`uid=freellm` 落 core 账本 |
| 测试桩 | `probe_test.go`（timeout/concurrency 循环）、`health_test.go`（healthyStub `/v1/models`）、`usage_report_test.go`（usagePair freellm case） |

### 1.3 前端（三件全接，与后端通道序一致）

| 文件 | 改动 |
| --- | --- |
| `console/web/app.js` | nav-muted、`freellmEnabled` 复位/装配、pageNames+页派发 `loadFreellm`、`loadMissingChannelStatuses`、usage 四张表（names/order/tags/usageChannelOf）、`healthChannelKeys`、`modelChannels`、`modelKey` 剥 `freellm-`、`channelHasCredits` 排除 freellm、`realmLabel`/`modelRealm`、`renderFreellm`/`renderFreellmHealth`/模型表/下拉、`/admin/freellm/chat` 表单（非流式）、探针按钮接线（probeChannelNames）、刷新按钮 `runTopRefresh` |
| `console/web/index.html` | nav `FreeLLMAPI` 按钮、topbar 刷新钮、freellm 整页（状态/模型/对话三子页 + probe-freellm + progress）、概览与明细两处 usage 下拉加「FreeLLMAPI 通道」、别名保留前缀提示加 `freellm-` |
| `console/web/style.css` | `--ch-freellm` 四色令牌（`#17795a` / ink `#135f47` / wash `#e9f5f0` / lit `#3fd9a2`，对比度实测 ink/wash 6.82:1、lit/sidebar 10.40:1，均 ≥4.5）、`.ch-/.stroke-/.fill-/.sw-freellm`、spectrum 两条谱线按色相插入 |
| `console/web_test.cjs` | modelNotes 加 freellm（与 channelHasCredits 排除集对齐）、速度列计数 8→9 |

### 1.4 部署面

| 文件 | 改动 |
| --- | --- |
| `deploy/compose.freellm.yml`（**新**） | sidecar `freellmapi`：ghcr 镜像、`network_mode: service:console`、`127.0.0.1:7868`、`FREEAPI_DB_PATH=/data/freeapi.db`、`ENCRYPTION_KEY`（production 强制，崩坏已修）、`FREEAPI_DB_DIR_HARDENING=1`、`FREEAPI_CONFIG_JSON` 从 .env 注入、`PROVIDER_DAILY_REQUEST_CAP_OPENROUTER=50` / `_GROQ=500`；console 段 `WB2A_FREELLMAPI_URL=http://127.0.0.1:7868` + `WB2A_FREELLMAPI_KEY` |
| `docker-compose.override.yml` | include 加 `deploy/compose.freellm.yml`（本机专用文件，已被 .gitignore 排除） |
| `deploy/test_compose.py` | 新测试 `test_freellm_sidecar_joins_console_namespace_and_keeps_db_in_mounted_volume` |
| `runtime/freellm/` | 已建（`.gitignore` 已有 `runtime/`，不入库） |

### 1.5 密钥链（已全部打通，值不在本文档）

`.env` 现有 3 键（已 gitignore）：`WB2A_OPENCODE_KEY`（原有）、`FREEAPI_ENCRYPTION_KEY`（64-hex，本次生成）、`WB2A_FREELLMAPI_KEY`（59 字符 `freellmapi-`+48hex，**已从 SQLite 提取回填**）。

统一 key 提取法（实测可用，settings 表是 **key/value 结构，不是 unified_api_key 列**）：
```bash
docker exec workbuddy2api-ui-me-freellmapi-1 node -e \
  "const db=require('better-sqlite3')('/data/freeapi.db');process.stdout.write(db.prepare(\"SELECT value FROM settings WHERE key='unified_api_key'\").get().value)"
```

### 1.6 验证矩阵（2026-10-16 全绿，复跑命令见 §4）

| 项 | 结果 |
| --- | --- |
| console `go test ./...` | ok（golang:1.23-alpine 容器内） |
| console `go vet ./...` | OK |
| console `go test -race ./...` | ok（apk add gcc musl-dev 后） |
| core `go build ./... && go vet ./...` | OK |
| core `go test ./...`（22 包） | 全 ok |
| core `go test -race`（7 包：auth/pool/upstream/scheduler/taskrun/bridge/anthropic） | 全 ok |
| `node --test console/web_test.cjs` | **187/187 pass** |
| `python3 deploy/test_compose.py` | **8/8 OK**（compose config 9 服务：core、console、7 旁路） |
| core scripts unittest | 15/15 OK |
| gofmt | 本次改动文件全部干净；`gofmt -l` 另有 11 个文件为**基线预存在**（CRLF 检出产物，与本次无关，未动） |
| sidecar 实测 | `Up (healthy)`，`:7868`，`/v1/models` 带 Bearer 统一 key 返回 **200 + 259 模型** |

## 2. 剩余步骤（Qoder 执行清单）

### 步骤 1：重建 console 镜像并重启（核心一步）

当前 `workbuddy2api-console:local` 是 **2026-10-13 旧镜像，不含 freellm 代码**——通道还没上线。

```bash
cd D:/work/02toolsMy/workbuddy2api-ui-me
# 二选一：
./1-重建镜像并重启.bat          # 会重建并重启
# 或只构建 console 模块：
python .build/local-build.py   # 确认 console 目标的构建参数（deploy/console.Dockerfile + 本地 retag）
docker compose up -d console freellmapi
```

⚠️ **重建会短暂重启网关**（当前会话流量正走棱镜 2-fast-v2），建议低峰执行；重启后 `.env` 里的 `WB2A_FREELLMAPI_URL/KEY` 会被 console 自动读到，无需手工注入。

### 步骤 2：供应商 key 注入（需要用户提供的真实 key）

`.env` 追加一行（JSON 内联，不落仓库）：
```
FREEAPI_CONFIG_JSON={"keys":[{"platform":"groq","key":"gsk_...","label":"main"},{"platform":"gemini","key":"...","label":"main"},...],"routing":{"strategy":"balanced"}}
```
- 白名单 5 家：gemini / groq / cloudflare / mistral / openrouter（platform 小写，与 FreeLLMAPI 内部命名一致）。
- **不加 Cohere（红线）、不加 NVIDIA/Cerebras、不加反向 custom provider。**
- 生效：`docker compose up -d freellmapi`（声明式配置幂等重放）。
- 可选限帽已在 compose：`PROVIDER_DAILY_REQUEST_CAP_OPENROUTER=50`、`_GROQ=500`。

### 步骤 3：冒烟验收（逐条，全部要过）

1. `curl -s http://127.0.0.1:7863/healthz` → console 在线。
2. 浏览器 `http://127.0.0.1:7863` 登录 → 侧栏出现 **FreeLLMAPI** 页签 → 三子页（通道状态/模型/对话测试）。
3. 「通道状态」：freellm 行显示在线、模型数 >0（目录来自 sidecar `/v1/models` 合并）。
4. 「对话测试」：选一个模型发一条 → 返回文本 + 用量行「本次通道：FreeLLMAPI（免费池）」。
5. 公共口直连（`freellm-` 前缀分流）：向公共 `/v1/chat/completions` 发 `model:"freellm-<真实id>"` → 200；同请求再发不带前缀的模型 → 走核心池不受影响。
6. 调用统计页：出现 `uid=freellm`、账号名「FreeLLMAPI 通道」的记账行；概览 usage 下拉选「FreeLLMAPI 通道」能过滤。
7. 「Agent 接入」页：添加别名 `fl-test → 通道 freellm → 真实模型`，保存 200；别名页提示里能看到 `freellm-` 已列入保留前缀。

### 步骤 4：AGENTS.md 补录（Hermes 写入被权限挡，需人工/Qoder 落）

注意：**AGENTS.md / README.md / console/trae.go / deploy/trae2api/** 现有另一批并行会话的 trae/traeai 改动**，落这段文案时只追加、不动其它段落：

> 在 AGENTS.md「可选并列 raccoon-proxy…」条目之后追加一条：
> `- 可选并列 freellmapi（deploy/compose.freellm.yml，默认不启动）：console 公共 /v1/chat/completions、/v1/messages、/v1/responses 按请求 model 的 freellm- 前缀分流到 FreeLLMAPI sidecar，/v1/models 合并上游目录（模型加 freellm- 前缀和 realm: freellm 标签）；由 WB2A_FREELLMAPI_URL 开关，未设置时公共出口与现状完全一致。sidecar 是第三方镜像 ghcr.io/tashfeenahmed/freellmapi:latest（tashfeenahmed/freellmapi，MIT），监听 127.0.0.1:7868、以 network_mode: service:console 共享 console 网络命名空间，数据落宿主 runtime/freellm/。两层职责切分是本通道的定案口径：池内路由与 fallback、429/5xx 熔断冷却、key 轮换、粘性会话、响应缓存与提示压缩全部下放 sidecar（免费池的生存策略需要池内全局视野，网关不重复实现）；console 只保留别名与模型榜单、排序、积分经济、timeout>=60s / retry=1、X-Routed-Via 透传日志与用量记账。密钥面：console 只持 FreeLLMAPI 首启在 SQLite（settings.unified_api_key）生成的统一 key（freellmapi- 前缀），不持任何供应商 key；供应商 key 由宿主经 FREEAPI_CONFIG_JSON 声明式注入（白名单只 5 家：不注册 Cohere 与 NVIDIA，不建反向 custom provider，避免与棱镜成环），仓内不落密钥文件。健康判定用目录拉取（/v1/models）而非 LLM 探测，避免探活本身消耗免费额度。旁路 usage 上报以 uid=freellm、账号名「FreeLLMAPI 通道」落 core 账本，模型名保留公共前缀。`

### 步骤 5：提交（分离并行改动）

工作区同时有**两批**改动，commit 必须分离：
- **本任务（freellm）**：`console/freellm.go`、`console/freellm_test.go`、`deploy/compose.freellm.yml`（新增 3）+ `console/{route,health,probe,server,main,zcode,usage_report}.go`、`console/{probe_test,health_test,usage_report_test}.go`、`console/web/{app.js,index.html,style.css}`、`console/web_test.cjs`、`deploy/test_compose.py`（修改 14）+ AGENTS.md 补录段。
- **另一批（trae/traeai，非本任务）**：`AGENTS.md`/`README.md` 其余改动、`console/trae.go`、`deploy/trae2api/**`、`deploy/compose.traeai.yml`、`deploy/compose.local-entry.example.yml` —— 不要混进 freellm 的 commit。

建议 message：`feat(console): FreeLLMAPI 免费池作为第 8 通道（freellm- 前缀分流/目录合并/记账/admin 面/前端页签）`

## 3. 环境变量契约（速查）

| 变量 | 侧 | 作用 |
| --- | --- | --- |
| `WB2A_FREELLMAPI_URL` | console（compose 注入 `http://127.0.0.1:7868`） | 通道开关：设置才分流 |
| `WB2A_FREELLMAPI_KEY` | console（.env 回填，59 字符统一 key） | sidecar `/v1` 与 `/v1/models` 的 Bearer |
| `FREEAPI_ENCRYPTION_KEY` | sidecar（.env，64-hex） | production 强制；加密 SQLite 里的供应商 key |
| `FREEAPI_CONFIG_JSON` | sidecar（.env，内联 JSON） | 白名单 key + 路由策略，幂等重放 |
| `FREEAPI_DB_PATH` | sidecar（`/data/freeapi.db`） | SQLite 落挂载卷 `runtime/freellm/` |
| `PROVIDER_DAILY_REQUEST_CAP_OPENROUTER/_GROQ` | sidecar（compose 固定 50/500） | 日帽；超额换池不重试到死 |

## 4. 测试跑法（宿机无 Go 工具链，全走容器）

```bash
# console（纯标准库，CGO=0 即可；-race 需先 apk add gcc musl-dev）
MSYS_NO_PATHCONV=1 docker run --rm -v "D:/work/02toolsMy/workbuddy2api-ui-me/console:/src" \
  -v wb2a-gobuildcache:/root/.cache/go-build -w /src \
  -e GOFLAGS=-mod=mod -e GOPROXY=off -e CGO_ENABLED=0 golang:1.23-alpine go test -count=1 ./...

# core（需 goproxy.cn 拉依赖）
MSYS_NO_PATHCONV=1 docker run --rm -v "<overlay prepare 输出目录>:/src" \
  -v wb2a-gobuildcache:/root/.cache/go-build -w /src \
  -e GOFLAGS=-mod=mod -e GOPROXY=https://goproxy.cn,direct golang:1.23-alpine sh -c \
  "go build ./... && go vet ./... && go test ./..."

# 前端
node --test console/web_test.cjs

# compose
python3 deploy/test_compose.py
```

⚠️ `scripts/check.sh` 在 Windows 有 MSYS 路径坑（原生 python3 把 `D:\work\...` 变成 `D:\d\work\...`），**整跑会挂在 overlay.py 一步**；分段跑（上面四段）或用 `cygpath -w` 转路径。

## 5. 关键坑位（接手必读，都是踩过的）

1. **前后端通道序必须同改**：`TestRouteChannelOrderMatchesFrontend`（channel_consistency_test.go）要求前端通道序数组与 `routeChannelOrder` 一致；web_test.cjs 还会从 app.js 现读通道表反查 HTML——**单改一边必挂测试**。本次已同步改齐，后续加通道照此办理。
2. **channelHasCredits 与 index.html 的「不采集积分倍率」备注是双向对齐测试**：改一边必须同时改另一边（freellm 已双双对齐）。
3. **健康判定故意用目录拉取（fetchChannelModels → /v1/models）而非 LLM 探测**——探活本身会烧免费额度，勿改回探测口径；手工探测白名单（probeableChannels）里 freellm 只出目录种子。
4. **ENCRYPTION_KEY 缺失会 crash-loop**（production 镜像强制；本次已修，报错特征是 `Failed to start: ENCRYPTION_KEY is required`）。
5. **settings 表是 key/value 结构**：统一 key 在 `WHERE key='unified_api_key'` 的 value 里，不是列名（第一次查错过）。
6. **模型下拉来自 routeCatalog → fetchChannelModels**：sidecar 没起/没 key 时下拉为空、页签显示「未启用」属正常，不是前端 bug。
7. **OpenRouter :free 降智是最大隐形坑**（免费池的固有属性，不是接线问题）；配额 50/天（<$10）→1000/天（≥$10）。
8. **网络**：github.com/Docker Hub 直连不通；git 走 `gh-proxy.com`、镜像走 `ghcr.io`（401 属正常握手）、Go 模块走 `goproxy.cn`、npm 走 `npmmirror`。
9. **数字口径**：sidecar 实测目录 **259 模型**（2026-10-16 现场）；历史简报的 635 是 README 快照，已过时，引用以现场为准。

## 6. 本任务产物索引

- 代码：§1.2/§1.3/§1.4 三张表（19 个文件：新增 3、修改 16 含并行会话分离说明见步骤 5）。
- 分析简报（前序会话产物，`/root/.openclaw-autoclaw/workspace/artifacts/`）：`2026-10-14-FreeLLMAPI分析与棱镜网关接入简报.html`、`2026-10-14-FreeLLMAPI模型质量评估.html`、`2026-10-14-FreeLLMAPI免费池LLM质量评估.html`。
- 交接文档（本文件）：`docs/superpowers/plans/2026-10-16-freellm-handoff.md`（随同目录既有交接文档体例）。
