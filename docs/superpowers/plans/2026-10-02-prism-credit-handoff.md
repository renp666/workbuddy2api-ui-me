# 交接班文档：积分消耗规则功能（2026-10-02 收工 → 次日续接）

- **文档编号**：SHIFT-2026-10-02-PRISM-CREDIT
- **撰写时间**：2026-10-02 收工时
- **续接对象**：次日（新会话）的自己 / 任何接手者
- **一句话状态**：**本轮 5 个提交已落本地（overlay 强化 + console 前端重组 + 别名/auto 虚拟模型），未推送；积分消耗规则功能已完成探测与代码落点测绘，尚未动笔写补丁 0011。次日第一件事=写 `patches/0011-*.patch`。**

> **给新会话的自己**：先读本文 §1（现在停在哪）与 §2（下一步做什么），
> 再按需读 §4 的文档地图。**不要**重新调研积分字段——§6 已把探测结论钉死。
> 探测脚本 `.build/probe-credit*.js` 是临时件，用户重启前**不要删**。

---

## 1. 现在停在哪（盘上事实，已核对）

### 1.1 提交历史（本地已提交，**未推送**）

```text
3dc6d34  feat: 为 overlay 添加强化的 Git 模式处理和 CRLF 补丁支持      ← 本轮最后提交（t0-overlay）
01f72ea  feat(console): 前端重组与视觉重写，含 Agent 接入页文案
591c099  feat(deploy): 挂载别名配置目录供 console 读写
958865f  feat(console): 别名接入公共出口与 auto 虚拟模型
18073d0  feat(console): 路由别名与 auto 虚拟模型后端
30570f1  feat(console): 模型能力与探测状态展示层统一到四个页签
ceb17d1  chore(scripts): 新增只读的 API 使用健康度观察脚本
ca4f899  fix(qoder): 修复 Linux 下超长 system prompt 触发的 spawn E2BIG
```

### 1.2 分支与远端

```text
* main 3dc6d34 [origin/main: ahead 7]
```

- 当前分支 `main`，**领先 `origin/main` 7 个提交**，**从未推送**。
- 推送需**单独授权**（AGENTS.md：更新上游、发布镜像、推送 Git、重启服务分别需要授权）。

### 1.3 未提交的工作区（次日第一件事之外）

```text
?? .zcodeignore
```

- 唯一未跟踪项是 `.zcodeignore`，**与本次积分功能无关**，保留原样，不清理。

### 1.4 硬约束核验（已过）

```text
git diff -- upstream upstream.lock   → 空（快照与锁文件零改动）
patches/series                       → 0001–0010 共十行，顺序未动
```

### 1.5 t0-overlay 本轮改动摘要（已提交）

`scripts/overlay.py`：

- 新增 `_indexed_git_modes(root)`：Windows 上读 **Git 索引 mode**，而非 NTFS 文件系统执行位（NTFS 不保存执行位）。
- 新增 `_normalized_patch(patch, scratch)`：把 CRLF 补丁写成 **LF 临时副本**再 apply。
- `_tree_entries` / `source_digest` / `materialize` / `update` 增加 `modes` 透传。
- `main()` 的 prepare/update 分支注入 `_indexed_git_modes(repository / "upstream")`。

`scripts/test_overlay.py`：新增 `IndexedModeTests` 三类（含 `skipIf(os.name != "nt")` / `skipIf(os.name == "nt")`）与 `test_materialize_applies_crlf_patch_against_lf_snapshot`。

验证记录：`python -m unittest discover -s scripts` 47 用例，失败 4 / 错误 26 / skip 1，**与基线一致**（Windows 缺 POSIX 能力，非本轮回归）；`prepare --output .build/core-t0b` 退出码 0、产出 230 文件、10 个补丁全部目标被覆盖。

### 1.6 环境状态

| 项 | 状态 |
| --- | --- |
| 本机 Go 工具链 | **无**——Go 编译/测试须走 Docker |
| Node.js | **可用**（`node --check`、`node --test`） |
| Docker | 可用（编译测试入口） |
| `.build/` 临时件 | `probe-credit.js`、`probe-credit2.js` 在场，**重启前保留** |

---

## 2. 次日第一件事（按优先级）

### P0 — 写 `patches/0011-*.patch`（积分倍率透传）

core 侧（`upstream/`，只能通过 patches 改）：

1. `upstream/internal/upstream/client.go`：`FetchModels` 的 env 结构体（第 898-940 行）当前**只解析 8 字段**，`credits` 被 `json.Unmarshal` **静默丢弃**——在此加 `Credits string \`json:"credits"\``。
2. `client.go` 第 957-994 行的搬运链 `dynEntry` → `dynMap` → `out []ModelInfo`：**三处都要加 `Credits`**。
3. `client.go` 第 842-851 行 `ModelInfo` struct：加 `Credits string`（原始串透传；解析可交给展示层，或 core 加 `CreditFactor float64` + `CreditsKnown bool`）。
4. `upstream/internal/server/handler.go` 第 224-297 行 `modelList()`：CN 分支（`"id":"cn:"+mi.ID`）与 global 分支（`"id":"global:"+id`）各加 `entry["credits"]`（原始串）与/或 `credit_factor`（数值）。

> **注意**：`upstream/internal/upstream/global_models.go` 第 1-4 行有**明确设计声明**「只产模型名，不产倍率（PLAN §3.D2『模型名目录 ≠ 倍率表』），credits 数值一律不进入本包实现」。patch 0011 若覆盖此文件，**必须在补丁说明里显式交代这个口径变更**，不能悄悄改。

### P1 — 同步补丁登记

- `patches/series`：追加 `0011-*.patch`（保持 0001–0010 顺序不动）。
- `patches/README.md`：补补丁顺序、修改原因、验证方式、移除条件。

### P2 — 本地物化验证（Docker 跑 Go）

```powershell
python scripts/overlay.py prepare --output .build/core-credit
# 经 Docker 跑：
go -C .build/core-credit test ./internal/upstream ./internal/server
```

> 物化目录**不会自动同步**；重跑务必用**新的**输出路径，不要复用、不要删来源不明的目录。

### P3 — console 侧实现（开关 + 排序，零新增容器）

- `console/route.go`：扩展 `routeEntry` 加 `enabled`（或独立模型开关表）；复用 `WB2A_ROUTE_FILE` 承载开关状态。
- `console/web/app.js`：加倍率列 + 每行开关 + 按倍率倒序。
- `console/web_test.cjs`：同步断言。
- 本机验证：`node --check console/web/app.js` + `node --test console/web_test.cjs`。

### P4 — 构建 `:dev` 镜像本地验证

用户已选「**先只做本地验证**」，**不发布镜像**。改前端必须重建镜像（`//go:embed web/*`）。

---

## 3. 遗留事项（完整，不隐瞒）

### 3.1 本轮主任务（未完成）：积分消耗规则功能

用户原始需求：

1. 所有通道的模型列表都要增加"当前模型积分消耗规则"字段的采集（先调研如何获取不同通道的积分规则）；
2. 在每个指标后增加模型启动按钮，积分规则 >0 的模型默认关闭，否则开启；
3. opencode 通道因为是免费模型，不加此功能；
4. 模型列表按积分消耗倒序排序。

用户关键口径纠正：「这个积分消耗不是消耗后的积分记录，是**积分消费规则**，模型的耗费价格规则。比如 workbuddy 中 hy4 目前活动是免费，积分消耗就是 X0。」

已定决策（AskUserQuestion）：

| 问题 | 裁定 |
| --- | --- |
| 「积分消耗规则」口径 | **去上游探真实倍率**（不是读本地调用账本历史累加） |
| 「模型开关作用」 | **隐藏 + 拒绝请求**（推荐方案） |
| 「core 发布」 | **先只做本地验证**（不发布镜像） |

### 3.2 其他待办（用户此前提出的多通道增强需求，尚未开工）

| 代号 | 内容 |
| --- | --- |
| t2-tabs | WorkBuddy 通道拆出独立「模型列表」子页签（现状模型列表与对话测试并存于同一页） |
| t3-heat | 新增「全球热度」列，只接 OpenRouter 官方公开源（`/api/v1/models?sort=most-popular`，按 `canonical_slug` 对齐） |
| t1-ctx | 补齐各通道模型的上下文长度与能力元数据（走 patches/0011） |

### 3.3 探测脚本小瑕疵（已知，不影响结论）

- `.build/probe-credit2.js` 的脱敏正则 `/token|secret|key|password/i` 误伤 `maxInputTokens` / `maxOutputTokens`（打成 `<redacted>`），**不影响关键结论**。
- 两个脚本是临时件（在 `.build/` 内，已 gitignore），**用完即删**；用户重启前保留。

---

## 4. 文档地图（按需读，不要全读）

```text
docs/superpowers/
├── plans/
│   ├── 2026-10-02-prism-credit-handoff.md                   【本文·积分功能权威交接】
│   │   读它当你要知道：探测结论、代码落点、已定口径、次日下一步
│   └── 2026-09-22-backport-shift-handoff.md                 【上一轮·安全回移交接·格式范本】
├── specs/
│   └── 2026-09-22-upstream-security-backport-design.md      【上一轮·安全回移方案】
└── verification/
    └── （本轮的探测证据落在 .build/probe-credit*.js，非 docs）
```

**文档引用链是单向可追溯的**：本文 → 探测脚本 → 上游接口。

---

## 5. 关键路径与命令（次日会用到）

### 5.1 core 物化 + Go 测试（走 Docker）

```powershell
cd d:\work\02toolsMy\workbuddy2api-ui-me
python scripts/overlay.py prepare --output .build/core-credit
# 经 Docker 进入物化目录跑：
go -C .build/core-credit test ./internal/upstream ./internal/server
```

### 5.2 宿主侧（轻量判据）

```powershell
node --check console/web/app.js
node --test console/web_test.cjs
git diff --check
```

### 5.3 环境/工具陷阱（Windows，供次日避坑）

| 陷阱 | 说明 |
| --- | --- |
| 无本机 Go | Go 编译/测试**必须走 Docker**；本机只有 Node.js |
| PowerShell 5.1 | `Invoke-WebRequest` 需 `-UseBasicParsing`；`docker inspect --format` 报错可改 `Select-String`；`/dev/null` 会被解析成 `D:\dev\null` |
| `.gitattributes` | `* text=auto eol=lf`，只约束**索引/提交**；工作区在 Windows 上仍是 **CRLF** |
| NTFS 执行位 | NTFS 不保存执行位，`upstream.lock` 记录 7 个 `100755` 文件，Windows 上须以 **Git 索引 mode** 为准（t0-overlay 已修） |
| 物化目录 | 不会自动同步；重跑用**新**输出路径，不要删来源不明的目录 |

---

## 6. 已知契约与语义（避免次日误判）

### 6.1 探测结论（**决定性，已实测拿到，直接采用**）

**倍率字段** = 上游模型接口返回的 `data.models[].credits`，是**字符串**，形如：

```text
"x0.79 credits"  /  "x0.00 credits"  /  "x0.05"  /  "x3.47"
```

（可带 `credits` 后缀也可不带，**解析需容错**。）

**CN 端点** `https://copilot.tencent.com/console/enterprises/personal/models`：30 个模型。

- `hy3` = `"x0.00 credits"`（限时免费已并入，tags 含 `badge:限时免费:#FF0000`）
- `hy4-preview` = `"x0.29"`
- `glm-5.3` = `"x0.79"`
- `default` = `"x2.20 credits"`
- `auto` / `hunyuan-chat` / `hunyuan-image-alpha` **无 `credits` 字段**

**GLOBAL 端点** `https://www.workbuddy.ai/v2/enterprises/personal/models`：18 个模型。

- `hy4-preview` / `hy3` = `"x0.00"`（因 `modelPromotions` 折扣 factor=0）
- 另有 `data.modelPromotions[]` 折扣表，结构：

```text
{id, kind:"discount", modelIds:[...], priority, enabled,
 badge:{label:"Free now"},
 discount:{discountedCredits:"0x", displayMode:"replace", factor:0},
 hover:{textZh:"..."},
 schedule:{timezone:"Asia/Shanghai", validFrom, validUntil}}
```

**模型字段并集**：

```text
credits, descriptionEn/Zh, disabledMultimodal, iconUrl, id, isDefault, maxAllowedSize,
maxInputTokens, maxOutputTokens, name, onlyReasoning, reasoning, relatedModels,
supportsImages, supportsReasoning, supportsToolCall, tags, temperature, top_p, top_k,
vendor, contextWindow, summary, canDisableThinking, repetition_penalty
```

**实测注意（关键）**：CN 账号打 global 端点返回 200 且内容与 global 账号一致（都是 18 模型 global 表）→ **该端点按端点分表，不按账号 realm 分表**；CN 账号打 CN 端点得 30 模型表。

**sidecar 通道**：zcode / qoder / opencode 的状态端点只返回 `{id, realm}`，**不含倍率**；opencode 按需求**不加此功能**。

### 6.2 已固定口径（避免返工）

| 情形 | 默认状态 |
| --- | --- |
| 倍率 = 0 | 默认**开** |
| 倍率 > 0 | 默认**关** |
| 字段缺失 | 标「**未知**」且默认**关** |
| opencode | **不加**倍率列也不加开关 |

- 开关（隐藏 + 拒绝请求）与排序**全在 console 实现**，复用 `WB2A_ROUTE_FILE`，**零新增容器**。

### 6.3 积分功能代码落点（已读实，供 0011 补丁使用）

**core 侧（`upstream/`，只能通过 patches 改）**：

| 文件 | 位置 | 说明 |
| --- | --- | --- |
| `upstream/internal/upstream/client.go` | 842-851 | `ModelInfo` struct（ID/Name/ContextWindow/MaxTokens/Efforts/DefaultEffort/SupportsImages） |
| 同上 | 857-860 | 常量 `cnModelsPath` / `globalModelsPath` |
| 同上 | 898-940 | `FetchModels` 的 env 结构体**只解析 8 字段**（id/name/maxInputTokens/maxOutputTokens/disabled/supportsImages/tags/reasoning），**`credits` 在此被静默丢弃——0011 要补的位置** |
| 同上 | 957-994 | `dynEntry`→`dynMap`→`out []ModelInfo` 搬运链（加 credits 要**同时改这三处**） |
| 同上 | 878-894 | `nonChatModel` 过滤（id 前缀 `nes-`/`completion-`/`codewise-`、`maxOutputTokens≤256`、tags 含 `text-to-image`） |
| `upstream/internal/upstream/global_models.go` | 1-4 | **明确设计声明**「只产模型名，不产倍率」；覆盖需在补丁说明显式交代 |
| 同上 | 65-68 / 178 | `globalModelsProbePaths` / `globalModelEntry` |
| `upstream/internal/server/handler.go` | 181-192 | `staticModels`（10 条兜底表） |
| 同上 | 194-205 | `dynamicModelsCache`（TTL 1h、失败冷却 5min） |
| 同上 | 224-297 | `modelList()`——CN 分支把 `mi` 映射为 `{"id":"cn:"+mi.ID,"object","created","owned_by","context_length":mi.ContextWindow,"max_output_tokens":mi.MaxTokens}` 再条件加 `supports_images`/`reasoning_supported_efforts`/`reasoning_default_effort`，**这是加 `credits` 字段的位置** |
| 同上 | 274-295 | global 分支 `{"id":"global:"+id,...}`，`context_length` 硬编码 131072 |
| `upstream/internal/upstream/headers.go` | 128-144 | `CommonHeaders`；另有 `defaultClientVersion="5.5.4"`、`defaultCliVersion="2.137.1"`、`originRefererCN="https://www.codebuddy.cn"`、`originRefererGlobal="https://www.workbuddy.ai"` |

**console 侧（可直接改）**：

| 文件 | 位置 | 说明 |
| --- | --- | --- |
| `console/web/app.js` | 106-119 | `modelChannels` 映射（zcode/qoder/opencode/core 各自 body/empty/namePrefix）与 `modelKey(id)`、`badgeCell`/`chipsCell` |
| 同上 | 108 | `let modelHeat=new Map(),modelHeatRank=new Map();` |
| 同上 | 129-141 | `modelCapabilityBadges`/`modelLimitText`/`modelVariantNames` |
| 同上 | 142-171 | 热度机制 `modelHeatCell(key)`/`applyModelHeat(items)`/`loadModelHeat()`（**新增列的实现范本**） |
| 同上 | 172-206 | `channelModelRows(channel,list,health)`，末行 `.sort(...)` 是**排序改点** |
| 同上 | 207-219 | `renderModelTable(channel,list,health)` 行拼装（212-215 `tr.append(...)`） |
| 同上 | 317-342 | `accessModelOptions` 与 `const models=(await jsonAPI('models')).data||[];` |
| 同上 | 534-576 / 695-763 / 868-898 | zcode / qoder / opencode 页签 |
| 同上 | 1063-1167 | 路由别名 `currentRouteAliases`/`saveRouteAliases`/`jsonAPI('route/save',...)` |
| `console/route.go` | 57 | `routeFile{Version, Models []routeEntry, AutoFallback}` |
| 同上 | 104 / 182 / 258 | `validateRouteEntries` / `normalizeRoutes` / `save(models, autoFallback)` |
| 同上 | 701-731 | `routeCatalog`/`fetchChannelModels`（并行抓各通道 `/v1/models`）→ **开关状态复用 `WB2A_ROUTE_FILE`，零新增容器** |
| `console/server.go` | 197-210 | 注册 `/admin/zcode*`、`/admin/qoder*`、`/admin/opencode*` |
| 同上 | 223 | `{"GET /admin/models","GET","/internal/v1/models"}` 代理映射 |
| 同上 | — | Go embed `//go:embed web/*`（**改前端必须重建镜像**） |
| `console/opencode.go` / `console/qoder.go` | — | 状态端点仅返回 `models:[{id,realm}]`，无倍率字段 |
| `console/go.mod` | — | 仅 `module workbuddy2api-console` + `go 1.22.5`，**零第三方依赖**；前端无框架无构建步骤 |

---

## 7. 授权边界（不得越界）

| 动作 | 授权状态 |
| --- | --- |
| 本地提交 | ✅ **已授权**（用户明确说「后续改为就自动提交」） |
| **推送 Git** | ❌ **未授权**，需单独批准 |
| **发布镜像** | ❌ **未授权**；且发布 ≠ 部署 |
| **部署 / 重启真实服务** | ❌ **未授权** |
| **更新 `upstream.lock` / 整包更新上游** | ❌ **未授权** |
| 重置工作区 / 清空数据 / 覆盖真实配置 | ❌ **禁止**（AGENTS.md 硬约束） |

---

## 8. 一句话总结

> **本轮 5 个提交（overlay 强化 + console 前端重组 + 别名/auto 虚拟模型）已落本地 `main`，
> 领先 `origin/main` 7 个提交，未推送、未发布、未部署。**
> 积分消耗规则功能已完成**上游倍率探测**（`credits` 字符串字段，CN 30 模型 / GLOBAL 18 模型）
> 与 **core+console 代码落点全测绘**，决策与口径已定，**尚未动笔写补丁 0011**。
> 次日第一件事=写 `patches/0011-*.patch` 透传倍率，再物化验证、console 加倍率列与开关、构建 `:dev` 本地验证。
