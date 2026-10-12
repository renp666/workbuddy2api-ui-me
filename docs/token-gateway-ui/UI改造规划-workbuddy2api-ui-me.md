# UI 改造规划：棱镜网关控制台 → Helicone 风格对齐

> **状态：待执行（规划阶段，未改动任何代码）**
> 撰写时间：2026-10-07
> 前置条件：当前功能改造完成、`workbuddy2api-ui-me` 工作区落定后再启动

---

## 0. 一句话结论

**不建议整体切换到 Helicone 风格。** 本项目已有一套成熟且自洽的「棱镜工作台」设计系统（紫色主色 + 四通道一束色 + 扁平默认/状态才抬的口径），而 Helicone 是另一套语言（天蓝 + 毛玻璃 + 圆角更小）。直接换皮会**丢掉通道谱系这套核心识别资产**，且与 `DESIGN.md` 声明的终态冲突。

推荐路线：**保留棱镜视觉语言，吸收 Helicone 的 5 个结构性做法**（详见 §4）。改造量小、风险低、收益明确。

---

## 1. 项目现状（已核对）

### 1.1 技术栈 —— 关键约束

| 项 | 事实 |
|---|---|
| 前端框架 | **无**。原生 HTML + CSS + ES Module |
| 打包方式 | Go `//go:embed web/*` |
| 源码规模 | `index.html` 237 行 / `style.css` 502 行 / `app.js` 2189 行 |
| 测试 | `console/web_test.cjs`（Node 内置 test runner） |
| 本机环境 | **无 Go 工具链**，编译/测试须走 Docker；Node 可用 |

⚠️ **改前端必须重建镜像才能生效**（embed 特性）。这是每次 UI 改动的固定成本。

### 1.2 现有设计系统（已相当成熟）

`style.css` 第 6–60 行定义完整令牌体系，`DESIGN.md` 逐令牌对齐：

- **主色**：`--accent: #5b4bf0`（棱镜紫），渐变 `--prism: linear-gradient(135deg,#5b4bf0, #7c4bec, #0a7f96)`
- **四通道一束色**（全站唯一取色处，设计亮点，不应破坏）
  | 通道 | 本色 | 文字版 | 极浅底 | 暗底文字版 |
  |---|---|---|---|---|
  | WorkBuddy | `#4f52d6` | `#3d40a8` | `#eef0fa` | `#8f92ff` |
  | Zcode | `#0a7f96` | `#076b80` | `#e6f4f7` | `#4fd6e8` |
  | Qoder | `#7a3fc4` | `#6a36ab` | `#f2ecfb` | `#b78cf5` |
  | OpenCode | `#c4356f` | `#b02f63` | `#fbeaf1` | `#ff8fb8` |
  - 文字版全部实测 ≥4.5:1，**这套无障碍达标度已做实测**，换色需重测
- **暗色侧栏**：`--sidebar: #0f1120`（机架面），`--sidebar-hi: #262b50`
- **圆角**：`8 / 10 / 14px`（Helicone 是 6/8/12px，略小）
- **口径**：*默认扁平、状态才抬；棱镜识别只留在品牌标记与通道配色*

### 1.3 页面结构 —— 10 个页面

| `data-page` | 导航标签 | 主要内容 |
|---|---|---|
| `overview` | 总览 | 通道卡片、统计条、趋势折线 |
| `workbuddy` | WorkBuddy | 4 子页签：账号 / 任务 / 模型 / 对话 |
| `zcode` | Zcode | 状态 / 登录 / 模型 / 对话 |
| `qoder` | Qoder | 只读状态 + 对话测试 |
| `opencode` | OpenCode | 只读状态 + 对话测试 |
| `ccr` | CCR | 借宿主 claude-code-router 管理 RPC |
| `autoclaw` | AutoClaw | 新增中（`console/autoclaw.go` 未跟踪） |
| `usage` | 调用统计 | 热力图 / 每日趋势 / 模型占比 / 调用明细（分页） |
| `routes` | 路由别名 | 路由配置 + auto 虚拟模型 |
| `access` | 接入 | 协议切换 + 接入网格 |

`style.css` 共 29 个章节（外壳侧栏 / 顶栏 / 统计条 / 通道机架 / 热力图 / 面板 / 表格 / 分页 / 对话面板 / 任务面板 / 协议接入 / 子页签 / 响应式×3 …）。

### 1.4 当前改造进行中（`git status` 实况）

**未提交改动 13 个已跟踪文件 + 6 个未跟踪项**：

```
M console/web/app.js          ← 155KB，正在改
M console/web/index.html      ← 47KB，正在改
M console/web_test.cjs
M console/main.go / route.go / server.go / probe.go / usage_report.go
M console/browser_preview_test.go
M console/zcode.go / AGENTS.md / deploy/compose.local-entry.example.yml
?? console/autoclaw.go + autoclaw_test.go + ccr.go + ccr_test.go   ← 新模块
?? deploy/autoclaw-entrypoint.sh / autoclaw.Dockerfile / autoclaw2api/
?? deploy/compose.autoclaw.yml
?? console/workbuddy2api-console.exe                                  ← 构建产物，不应入库
```

**最近 8 次提交几乎全是 UI/交互**：
```
2aa3039 一束色认一条通道贯穿全站，调用明细改为翻页
9563cf0 刷新按钮吸到顶栏控制轨，状态徽章按事实分五档
ce98031 y 轴标签按自身宽度留位，亿级读数不再被 viewBox 裁掉
451182c 调用统计页改为全历史指标条 + 热力图 + 按模型趋势 + 用量环形
```

**未完成的后端任务**（来自 `docs/superpowers/plans/2026-10-02-prism-credit-handoff.md`）：
- P0：`patches/0011-*.patch` 积分倍率透传（credits 字段，**尚未动笔**）
- P3：console 侧模型开关 + 倍率列 + 按倍率倒序
- 分支 `main` 领先 `origin/main` **7 个提交，从未推送**

---

## 2. 为什么不该整体换成 Helicone 风格

| 维度 | 棱镜（现状） | Helicone | 冲突 |
|---|---|---|---|
| 主色 | 紫 `#5b4bf0` | 天蓝 `#0ea5e9` | 直接冲突，通道色里已有靛/紫，换色会撞 |
| 圆角 | 8/10/14px | 6/8/12px | 改小会让现有密排表格变挤 |
| 卡片风格 | 扁平 + 1px 描边 | `shadow-sm` + 毛玻璃 | 现有口径明写「默认扁平」，反向改 |
| 核心资产 | 四通道一束色（无障碍实测达标） | 无对应概念 | **换掉即丢失识别性** |
| 文档契约 | `DESIGN.md` 声明 2026-10-05 终态 | — | 需同步改文档，多方成本 |

**一句话**：Helicone 值得抄的是它的**结构判断**，不是它的**皮肤颜色**。

---

## 3. 启动前置条件（必须全部满足）

| # | 条件 | 判定方式 |
|---|---|---|
| 1 | 功能改造完成，工作区落定 | `git status --short` 无 `M console/web/*` |
| 2 | 积分倍率补丁 `0011` 已提交或明确不做 | `patches/series` 确认 |
| 3 | AutoClaw 模块接入完成 | `console/autoclaw.go` 已提交 |
| 4 | 有可用的视觉回归手段 | 确认 `browser_preview_test.go` 可跑 |
| 5 | 备份当前 UI | 见 §6.1 |
| 6 | 本机 Go / Docker 可用 | 改前端须重建镜像 |

---

## 4. 推荐方案：吸收 5 个结构性做法（不动配色）

这 5 项都是 Helicone 的**工程判断**，与棱镜配色不冲突。

### ✅ P1 · 表格页加「翻页游标」（价值最高）

Helicone 的调用明细是分页的，本项目最近提交 `2aa3039` 也刚做了「调用明细改为翻页」——**方向已一致**。

待补的一环：**翻页游标要出现在 URL 里**（`?page=3`），这样：
- 刷新不丢位置
- 链接可分享给同事
- 浏览器前进后退可用

改动点：`console/web/app.js` 的明细翻页逻辑 + `console/route.go`（若需服务端配合）。
**注意**：`AGENTS.md` 要求改前端后同步更新 `console/web_test.cjs` 断言。

### ✅ P2 · KPI 卡加「语义化状态底色」

Helicone 的 `--confirmative` 是独立状态色体系，**不复用** `green-*`，避免和图表绿撞色。

棱镜现状：状态色 `--ok-ink/--ok-bg`、`--warn-ink/--warn-bg`、`--danger/--danger-soft` 已经是独立体系 ✅
**可补的一点**：KPI 卡按状态着色时，用**极浅底 + 深文字**（已是此做法），但要确认**色盲友好**——建议状态徽章补形状区分（●/▲/✕），不只靠颜色。这与最近提交 `9563cf0`「状态徽章按事实分五档」可衔接。

### ✅ P3 · 焦点环「反色」取值

Helicone：`--ring` 取文字反色而非主色（浅色下=深文字，深色下=浅色）。
棱镜现状：`--focus: #3a2bc4`（主色紫）、`--focus-dark: #8ee3ff`（暗底专用）——**已是双值设计，等价实现** ✅
**仅需核对**：所有可交互元素是否都覆盖了 `:focus-visible`，无遗漏。

### ✅ P4 · 侧栏宽度与快捷键

| 项 | 棱镜现状 | Helicone | 建议 |
|---|---|---|---|
| 展开宽度 | 需确认 | 12rem (192px) | 侧栏含「一束色」通道标记，**不建议压窄**，保持现状 |
| 折叠 | 有 | 3rem (48px) | 可加折叠态，收益中等 |
| 快捷键 | 无 | `Ctrl/Cmd+B` | **可加**，成本极低 |

⚠️ `AGENTS.md` 要求生产部署保持 `docker compose up -d` 一条命令，不新增宿主脚本。前端快捷键不违反此约束。

### ✅ P5 · 表格密度与横向滚动

Helicone 用 12 列栅格 + 固定侧栏。本项目表格多且列数不均。
建议：为宽表（调用明细、模型列表）加**列宽拖拽 + 横向滚动容器**，并在 `overflow-x` 时保持首列吸左。
改动点：`style.css` 第 307–353 行（区块标题与表格 / 明细分页）。

---

## 5. 不建议做的（明确排除，省得返工）

| 想法 | 排除原因 |
|---|---|
| 主色改天蓝 | 与通道色（靛 `#4f52d6`、紫 `#7a3fc4`）撞色，破坏「一束色」识别 |
| 引入毛玻璃 `.glass` | 控制台是长时间高频使用场景，毛玻璃降低可读性且吃性能 |
| 引入 Tremor 图表库 | 无框架项目引入 30KB+ 图表库不划算；现有手写 SVG 已处理过 y 轴裁剪问题 |
| 全面改深色模式 | 现为亮色主 + 暗色侧栏的「机架面」设计，是刻意选择 |
| 换圆角为更小值 | 现有密排表格会变挤，改动波及 29 个样式章节 |
| 重写为 React/Vue | `AGENTS.md` 明写控制台为无框架 embed，重写等于推翻架构 |

---

## 6. 执行方案（前置条件满足后）

### 6.1 备份（第一步，不可跳过）

```bash
cd D:/work/02toolsMy/workbuddy2api-ui-me
git branch backup/ui-before-helicone
# 或
cp console/web/style.css console/web/style.css.bak
cp console/web/index.html console/web/index.html.bak
```
⚠️ 遵循 `AGENTS.md`：**不重置工作区、不覆盖用户已有改动**。

### 6.2 分阶段改，每阶段独立验证

| 阶段 | 内容 | 改动文件 | 验证 |
|---|---|---|---|
| **S1** | URL 翻页游标 | `app.js` + `route.go`(?) | `node --check` + `node --test web_test.cjs` |
| **S2** | 焦点环覆盖核对 | `style.css` | 浏览器 Tab 遍历 |
| **S3** | 状态徽章形状区分 | `style.css` + `app.js` | 灰度图检查色盲友好 |
| **S4** | `Ctrl+B` 侧栏折叠 | `app.js` + `index.html` | 折叠态视觉检查 |
| **S5** | 宽表列宽拖拽 + 吸左 | `style.css` + `app.js` | 10 个页面逐页检查 |

**每阶段完成后必须重建镜像才能看到效果**（`//go:embed`）：
```bash
# 参考项目既有脚本
3-开机从零构建并启动.bat   或   2-重启全部服务.bat
```

### 6.3 每阶段验证清单

```bash
# 1. 语法检查（Node 可用）
node --check console/web/app.js

# 2. 前端测试
node --test console/web_test.cjs

# 3. 视觉回归（若有）
go -C console test -run TestBrowserPreview   # 须 Docker
```

⚠️ **本机无 Go 工具链**，Go 测试须走 Docker。`node --check` / `node --test` 可本机跑。

### 6.4 回滚

每个阶段独立提交（`feat(console): ...`），出问题 `git revert` 单个 commit 即可，不影响其他阶段。

---

## 7. 待确认事项（开工前需要你拍板）

1. **是否接受"不换配色、只吸结构"这个路线？** 如果你确实想要 Helicone 的天蓝皮肤，请明确——但会牺牲通道识别，且需同步改 `DESIGN.md`。
2. **6 个优先级里，哪几项现在就要？** 我建议至少做 S1（URL 游标）和 S5（宽表体验），这两项体感提升最直接。
3. **AutoClaw 模块是否已完工？** `console/autoclaw.go` 还在未跟踪状态，若还要加页面，UI 方案需把它算进去。
4. **是否需要深色模式？** 现在是亮色主 + 暗侧栏，若要做完整深色，工作量约等于重做一遍配色体系。

---

## 附：可复用的 Helicone 提取资产

位于 `C:/Users/Administrator/WorkBuddy/2026-10-07-09-28-39/token-gateway-ui/helicone-theme/`：

| 文件 | 内容 |
|---|---|
| `styles/helicone-tokens.css` | Helicone 全量令牌（深浅两套），**作为参考值，不建议直接套用** |
| `tailwind.config.ts` | Tailwind 配置 —— 本项目无 Tailwind，**仅供查色** |
| `components/ui/button.tsx` | Helicone 按钮变体定义（action/glass/ghostLinear） |
| `components/ui/sidebar.tsx` | 侧栏骨架（12rem / Ctrl+B / cookie 持久化） |
| `preview.html` | 实时验证页，可切换深浅色对比 |
| `README.md` | 速查表，含 7 个非标准设计说明 |

Helicone 主仓 **Apache 2.0**，样式层可自由取用。
