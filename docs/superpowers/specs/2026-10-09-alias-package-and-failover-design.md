# 别名套餐与热备切换设计（Agent 接入）

- 日期：2026-10-09
- 范围：`console/route.go`、`console/web/app.js`、`console/web/index.html`、`console/route_test.go`
- 状态：设计待确认，未改代码

## 1. 需求

1. 在多个 agent（Claude Code / ChatBox / 脚本等）里配置**统一的一套别名套餐**。
2. 每个别名代表**一类模型**，例如「别名 A = GLM 5.3，来自通道 1」。
3. 同一个别名要有**热备**：主通道挂了能切到备用通道，且模型不变。
4. 别名**永久固定**：换主通道时不改名字，agent 侧配置不动。
5. 名称不限编号，但要**便于自己识别**（命名方案本次一并策划）。

## 2. 现状核查（实测，非推断）

「Agent 接入」页（`routes`）已经存在一套别名机制，能力比预期完整：

| 能力 | 现状 | 位置 |
| --- | --- | --- |
| 别名 → 单一 `(通道, 模型)` | ✅ 已实现 | `routeEntry`（`console/route.go:50`） |
| 别名在 `/v1/models` 广告 | ✅ 已实现，带 `virtual/alias/routed_model` | `aliasModelEntries`（`route.go:147`） |
| 保存即生效、原子落盘 | ✅ 已实现 | `routeStore.save/saveFile`（`route.go:507/394`） |
| 响应头回传路由决策 | ✅ `X-Prism-Routed-Model` / `-Channel` / `-Reason` / `-Fallback` | `prismHeaderWriter`（`route.go:841`） |
| **跨通道自动优选** | ✅ 已实现，但**只有全局虚拟模型 `gateway-auto`** | `resolveAuto`（`route.go:757`） |
| 探测结论引擎 | ✅ band 0/1/3 分档 + 30s 缓存 + 按耗时排序 | `probeCache`/`sortCandidates`（`route.go:600/700`） |
| 通道可达性常态探活 | ✅ 30s 一轮，留存 20 轮 | `health.go` |
| 别名级热备 / 自动切换 | ❌ **缺失** | — |

结论：**核心基础设施（探测 + 选路 + 响应头）已经建好，只差「把选路下沉到别名级」这一步。**
`gateway-auto` 本质是「全局单例的自动选路」，需求要的是「每个别名各自一条候选链的自动选路」。

### 2.1 顺带发现的缺陷

`console/web/app.js:1877` 的 `routeChannelLabels` **漏了 `raccoon`**：

```js
const routeChannelLabels={core:'核心账号池',glm:'GLM 通道',qoder:'Qoder 通道',opencode:'OpenCode 通道',autoclaw:'AutoClaw 通道',trae:'Trae 通道'};
```

只有这一处定义。后果：小浣熊通道的别名在「Agent 接入」页的「通道」列会显示裸字符串 `raccoon`，
而不是「小浣熊通道」。其余 6 个通道都正常。修法一行。

### 2.2 线上实际配置（`runtime/wb2api/console/routes.json`，只读核对）

```json
{ "alias": "no1",  "channel": "qoder",    "model": "qoder-qwen3.8-flash",             "note": "qwen3.8-f" },
{ "alias": "no2",  "channel": "autoclaw", "model": "autoclaw-glm-5.3-flash",          "note": "智谱5.3flash" },
{ "alias": "no21", "channel": "glm",      "model": "glm-5.3",                         "note": "wb-GLM" },
{ "alias": "no3",  "channel": "core",     "model": "global:deepseek-v4.1-flash",      "note": "国际ds4.1" },
{ "alias": "no4",  "channel": "opencode", "model": "opencode-OC · MiMo-V2.6-Flash Free", "note": "小米？" },
{ "alias": "no5",  "channel": "opencode", "model": "opencode-OC · Space Bunny Free",  "note": "兔子" },
{ "alias": "no22", "channel": "autoclaw", "model": "autoclaw-glm-5.3-flash" }
```

三个可直接指出的事实：

- `no21` / `no22` 已经是「同名模型的第二个通道」的手工版本，正是需求 3 的雏形。
- `no2` 与 `no22` 指向**完全相同**的目标（`autoclaw` + `autoclaw-glm-5.3-flash`），是冗余条目。
- `no1..no5` 这类编号**不携带语义**：三个月后无法从名字判断 `no4` 是小米还是别的。

## 3. 命名方案（本次重点）

### 3.1 硬约束（来自 `validateRouteEntries`，`route.go:194`）

- 字符集：`[A-Za-z0-9._:-]`，长度 1–64。
- **不得**以 `glm-`、`qoder-`、`opencode-`、`autoclaw-`、`trae-`、`raccoon-`、`cn:`、`global:` 开头
  （会静默遮蔽同名真实模型）。
- 不得等于保留名 `gateway-auto`。

### 3.2 命名原则

1. **别名描述「用途/档位」，不描述「模型」，也不描述「通道」。**
   别名是 agent 侧的长期契约，改一次要改 N 个 agent；而模型会下线、通道会挂、价格会变。
   把模型名写进别名 = 模型一换代，全部 agent 配置作废。
2. **主备不另起别名。** 备用通道进同一别名的候选链（见第 4 节），
   所以**不需要** `no21` / `模型1.1` 这种「主备各一个别名」的写法 —— 这是本次方案最大的简化。
3. **全小写、只用 `a-z0-9-`、长度尽量 ≤ 16。** 短名在 agent 的 model 字段里好填、不易打错。
4. **放弃无语义编号。** `no1/no2/no3` 换不来任何信息量。

### 3.3 推荐命名（方案 A：档位别名）

| 别名 | 语义 | 主模型 | 备选链（按序） |
| --- | --- | --- | --- |
| `daily` | 日常主力，性价比最高 | DeepSeek V4.1 Flash @ core | GLM 5.3 Flash @ autoclaw |
| `fast` | 要快要便宜（批量、草稿、总结） | GLM 5.3 Flash @ autoclaw | MiMo-V2.6-Flash @ opencode → qwen3.8-flash @ qoder |
| `strong` | 难任务，要质量 | GLM 5.3 @ glm | Kimi K3 @ core |
| `max` | 最强，不惜成本 | Kimi K3 @ core | GLM 5.3 @ glm |
| `free` | 零成本优先（免费通道） | MiMo-V2.6-Flash @ opencode | Space Bunny @ opencode |
| `try` | 尝鲜 preview 模型 | Hy4 preview @ core | — |

配进 agent 的只有这 6 个，且**换模型/换通道都不用改**。

### 3.4 备选命名（方案 B：模型别名）

若确实想「一眼看出是哪个模型」，用 `<家族><版本><变体>`：

`glm53`、`glm53f`（flash）、`ds41f`、`mimo`、`hy4`、`qwen38f`、`kimi-k3`

代价：7 个名字要记；模型换代时名字会撒谎。
**建议做法：方案 A 配进 agent，方案 B 只作为临时/对比用的调试别名**（如 `dbg-glm53`、`dbg-ds41f`）。

### 3.5 需要强制指定通道时

只有调试或想烧某条通道额度时才用，加 `-<通道>` 后缀：

`pin-glm53-glm`、`pin-glm53-trae`、`pin-ds41f-core`

日常别名**不要**带通道后缀 —— 那等于把热备能力关掉。

## 4. 数据模型（P1 核心）

**纯增量扩展，不新增版本号、不破坏现有 v1 配置**（符合 AGENTS.md「不为历史版本新增兼容层」：
这不是兼容层，而是给同一版 schema 加可选字段）：

```go
// routeTarget 是别名候选链上的一跳。
type routeTarget struct {
    Channel routeChannel `json:"channel"`
    Model   string       `json:"model"`
}

type routeEntry struct {
    Alias     string        `json:"alias"`
    Channel   routeChannel  `json:"channel"`             // 主目标，语义不变
    Model     string        `json:"model"`               // 主目标，语义不变
    Fallbacks []routeTarget `json:"fallbacks,omitempty"` // 新增：备用链路，按序
    Strategy  string        `json:"strategy,omitempty"`  // 新增：priority(默认) | health
    Enabled   bool          `json:"enabled"`
    Note      string        `json:"note,omitempty"`
}
```

两种策略：

- `priority`（默认）：恒用主目标，`fallbacks` 仅作记录与页面提示。**等价于现状**，零行为变更。
- `health`：复用既有探测引擎，在该别名**自己的**候选链里挑 band 最优的一跳；
  无任何探测结论时按 `[主目标] + fallbacks` 的列表顺序回退（**不随机挑**，与 `resolveAuto` 的克制口径一致）。

### 4.1 解析改动（`resolveAlias`，`route.go:738`）

```text
普通别名：
  strategy == "priority" → 主目标（现状行为）
  strategy == "health"   → 候选链 = 主目标 + fallbacks
                           在 h.probes.get() 的结论里挑 band 最小者
                           无结论 → 取候选链第 1 个
```

复用点（**不新建第二套机制**）：

- `h.probes.get(ctx, h)` / `collectProbeCandidates` / `sortCandidates`：探测与排序
- `publicModel()`：候选 model 的公共前缀幂等处理（qoder/opencode 上游是裸名，务必走这个函数）
- `prismHeaderWriter`：加一个 `X-Prism-Target-Index`（0 = 主目标，1..n = 第几个备用）便于排障
- `validateRouteEntries` / `saveFile` / `snapshot`：校验、落盘、快照

### 4.2 校验新增

- 候选链总长度 ≤ 8（够用且防失控）。
- `fallbacks` 内 `(channel, model)` 不得与主目标或彼此重复。
- `strategy` 只允许 `priority` / `health`，空串按 `priority`。
- `fallbacks` 里的 channel 同样受「非 core 不得含冒号、core 必须带 `cn:`/`global:`」约束。

## 5. 前端（P2）

「Agent 接入」页现有别名表是「一行一别名」。改造：

1. 别名行可展开候选链，每跳显示「通道 + 模型 + 当前探测状态」（复用既有 `modelProbeState` 渲染）。
2. 候选链支持上移/下移 —— **换主通道 = 把目标上移到第一位**，别名不变。
3. 每行加「自动 / 固定」切换（`strategy`），带 tooltip 说明「自动」依赖探测结论、重启后会短暂退化为按顺序。
4. 修 `routeChannelLabels` 补 `raccoon: '小浣熊通道'`（见 2.1）。
5. 空态文案补一句「备用通道填在这里，不要另起别名」。

## 6. 分阶段实施

| 阶段 | 内容 | 代码量 | 风险 |
| --- | --- | --- | --- |
| **P0** | 零代码。用现有页面按 3.3 命名建别名；热备靠「手动改 channel」+ 看探测结论 | 0 | 无 |
| **P1** | 后端加 `fallbacks` + `strategy`，`resolveAlias` 接入探测引擎 | ~150 行 + 测试 | 低（纯增量，v1 配置照常读） |
| **P2** | 前端候选链编辑 UI + 修 raccoon 标签 | ~250 行 + web_test | 中（动 app.js） |
| **P3** | 可选：别名级探测 TTL 调短（热备要更快切换）；`gateway-auto` 保留为全局兜底 | 小 | 低 |

## 7. 测试（tests-first，AGENTS.md 要求「先补能失败的回归测试」）

`console/route_test.go` 新增：

- `TestAliasPriorityStrategyUsesPrimary`（默认策略不因探测结论改变目标）
- `TestAliasHealthStrategyPicksAvailableFallback`（主目标探测失败 → 切备用）
- `TestAliasHealthStrategyNoProbeDataUsesPrimary`（无结论 → 按列表顺序，不随机）
- `TestAliasFallbackChainOrderPreserved`（同 band 按配置顺序）
- `TestValidateRouteEntriesRejectsDuplicateFallback`
- `TestAliasFallbacksSurviveSaveAndReload`（落盘 → reload 后候选链完整）
- `TestAliasFallbackRespectsCoreNamespace`（core 跳必须带 `cn:`/`global:`）
- `TestAliasEntriesAdvertiseRoutedModelOnly`（`/v1/models` 仍只广告主目标或解析结果，不暴露整条链）

`console/web_test.cjs`：候选链渲染、排序按钮、raccoon 标签。

## 8. 风险与取舍

- **探测结论是内存态**（`probe.go` 注释：重启即清空）。`health` 策略在 console 重启后会短暂
  退化为「按列表顺序」，直到下一次手工探测或后台探活补齐。这是可接受的，且与 `gateway-auto`
  的既有取舍一致；页面需明说，不能让用户以为「自动」是永久的。
- **`health` 只覆盖有探测结论的通道**。`probeableChannels` 不含 opencode（它由 OW Bridge 自带
  `/health` 提供），`collectProbeCandidates` 已单独处理 opencode，二者口径必须保持同步。
- **别名解析在前缀分流之前**，所以候选链里的 model 必须是通道原生名或已带公共前缀的形态；
  统一走 `publicModel()` 的幂等逻辑，避免拼出 `qoder-qoder-x`。
- **不引入跨别名共享的全局回退**。`gateway-auto` 保持现状（全局兜底），别名级热备与它互不干扰。
- **不做「失败后自动重试另一跳」**。本设计是**请求前选路**（按探测结论），不是**请求中重试**。
  若要「主通道 5xx 后同请求内换通道重试」，那是另一套机制（会牵涉流式响应已开始写出、幂等性、
  计费重复），本设计明确不做，避免把两件事混在一起。

## 9. 实施记录（2026-10-09）

P1 + P2 已按本设计落地，P0 交付了具体配置提案。

### 改动清单

| 文件 | 改动 |
| --- | --- |
| `console/route.go` | 新增 `routeTarget`、`strategyPriority/strategyHealth`、`maxRouteTargets`；`routeEntry` 加 `fallbacks`/`strategy` 与 `strategy()`/`targets()`；抽出 `validateTarget`/`validateRouteStrategy` 供主目标与备用共用；`resolveAlias` 拆出 `resolveEntry` + `pickChainTarget` + `probeBandFor`；`routeDecision` 加 `TargetIndex`；`prismHeaderWriter` 加 `X-Prism-Target-Index`；`writeRouteState` 下发 `fallbacks`/`chain`/`strategy`/`target_index`；`reload` 补规范化 |
| `console/route_test.go` | 新增 10 个用例（见下） |
| `console/web/app.js` | `routeChannelLabels` 补 `raccoon`；别名表改为「别名 / 切换 / 候选链 / 对外模型 ID / 状态 / 说明 / 操作」；新增 `routeStrategyCell`、`routeChainCell`、备用行增删与回填、`resetRouteAliasForm` |
| `console/web/index.html` | 表头换列；表单加「切换方式」与「备用链路」编辑区；保留前缀说明补 `raccoon-` |
| `console/web/style.css` | 新增 `.route-fallbacks` / `.route-fallback-row` / `.route-chain*` 样式 |
| `console/web_test.cjs` | 调整 2 处既有断言；新增 4 个用例 |

### 新增回归测试

- `TestAliasPriorityStrategyUsesPrimary`（默认策略无视探测结论）
- `TestAliasHealthStrategyPicksAvailableFallback`（热备切备用）
- `TestAliasHealthStrategyNoProbeDataUsesPrimary`（无结论按链序）
- `TestAliasFallbackChainOrderPreserved`（同档取链序靠前，不按耗时）
- `TestValidateRouteEntriesRejectsBadFallbackChains`（7 类非法链）
- `TestValidateRouteEntriesAcceptsFallbackChain`（合法链 + 上限）
- `TestAliasFallbacksSurviveSaveAndReload`（落盘重读）
- `TestRouteTargetPublicModelIsIdempotent`（裸名/带前缀等价，不拼双前缀）
- `TestAliasWithFallbacksAdvertisesPrimaryOnly`（`/v1/models` 不摊开整条链）
- `TestAliasWithoutFallbacksKeepsLegacyBehavior`（存量条目零行为变更）

前端：保存带备用链路与策略、半填行丢弃、编辑回填、raccoon 中文名。

### 实际验证

- `go build ./...` / `go vet ./...` 通过
- `go test ./...`（console）全绿；`gofmt -l console/route.go` 干净
- `node --test console/web_test.cjs`：**162 通过 / 0 失败**
- 交付的 `runtime/wb2api/console/routes.proposed-20261009.json` 经后端全部校验函数
  实测通过（5 个别名，链长 3/4/2/2/1）

### 未做的验证（环境限制，非未执行）

- **竞态测试未能运行**：本机无 `gcc`，`CGO_ENABLED=1 go test -race` 报
  `cgo: C compiler "gcc" not found`。`scripts/check.sh` 的 race 步骤需在有 CGO 的
  环境（CI / Docker）执行。
- **`scripts/` 与 `deploy/` 的 Python 测试未能运行**：它们会调起 `wsl.exe`，
  被本机安全策略拦截。本次未改动这两个目录。
- **未做镜像重建与真实容器验收**：需要 Docker 与部署授权。

### 已知遗留

- 线上 `routes.json` 的 `disabled_models` 与 `enabled_models` 存在重叠
  （`cn:deepseek-v4-pro`、`cn:glm-5.2` 同时在两张表里）。代码不报错（`modelOverride`
  让启用表优先），但违反「两表互斥」的口径；提案文件已清理。
- 运行时用的是旧镜像，**必须重建 console 镜像**才会生效（`fallbacks`/`strategy`
  在旧二进制里会被 JSON 解码静默忽略，不会报错，但也不起作用）。

### 交付边界

- 未覆盖线上 `runtime/wb2api/console/routes.json`，只新增了 `routes.proposed-20261009.json`。
- 未提交 Git、未发布镜像、未重启服务。
