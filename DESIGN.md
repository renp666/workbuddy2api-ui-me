---
name: 棱镜工作台 Prism Gateway Console
description: 自托管模型网关的运维控制台视觉系统（2026-10-05 改造后终态，与 console/web/style.css 逐令牌对齐）
colors:
  prism-violet: "#5b4bf0"
  prism-violet-strong: "#4a3ad8"
  focus-ring: "#3a2bc4"
  focus-ring-dark: "#8ee3ff"
  prism-indigo: "#5b4bf0"
  prism-purple: "#7c4bec"
  prism-cyan: "#0a7f96"
  opencode-magenta: "#ec4899"
  accent-wash: "#eceafd"
  accent-hairline: "#d8d4f8"
  ink: "#171a2b"
  ink-muted: "#5b6076"
  canvas: "#f4f5fa"
  canvas-soft: "#f9fafd"
  table-head: "#f7f8fc"
  group-band: "#eef0f8"
  surface: "#ffffff"
  hairline: "#dfe3ee"
  hairline-soft: "#edf0f7"
  vault: "#0f1120"
  vault-raised: "#262b50"
  vault-ink: "#d3d6ee"
  vault-muted: "#959bc4"
  vault-dim: "#767ca6"
  danger: "#c22f3c"
  danger-wash: "#fbecee"
  ok-ink: "#176b4c"
  ok-wash: "#e6f5ee"
  live-green: "#3ddc97"
  warn-ink: "#7a5210"
  warn-wash: "#fdf1dc"
  code-ink: "#33385c"
  code-wash: "#f2f4fd"
typography:
  display:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "23px"
    fontWeight: 700
    lineHeight: 1.35
    letterSpacing: "-0.3px"
  headline:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "17px"
    fontWeight: 700
    letterSpacing: "-0.2px"
  section-title:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "16px"
    fontWeight: 700
  title:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "14px"
    fontWeight: 700
  body:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.75
  label:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "11px"
    fontWeight: 600
  metric:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "24px"
    fontWeight: 650
    letterSpacing: "-0.6px"
  control:
    fontFamily: "Inter, -apple-system, 'PingFang SC', sans-serif"
    fontSize: "13px"
    fontWeight: 600
  mono:
    fontFamily: "ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace"
    fontSize: "12px"
    lineHeight: 1.8
rounded:
  sm: "8px"
  md: "10px"
  lg: "14px"
  pill: "999px"
spacing:
  xxs: "4px"
  xs: "6px"
  sm: "8px"
  md: "14px"
  lg: "20px"
  xl: "28px"
  page: "42px"
components:
  button-primary:
    backgroundColor: "{colors.prism-violet}"
    textColor: "{colors.surface}"
    rounded: "{rounded.md}"
    padding: "10px 16px"
    hoverBackgroundColor: "{colors.prism-violet-strong}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    borderColor: "{colors.hairline}"
    rounded: "{rounded.md}"
    padding: "10px 16px"
  button-quiet:
    backgroundColor: "transparent"
    textColor: "{colors.prism-violet-strong}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  badge-neutral:
    backgroundColor: "{colors.accent-wash}"
    textColor: "{colors.prism-violet-strong}"
    rounded: "{rounded.pill}"
    padding: "4px 11px"
  badge-warn:
    backgroundColor: "{colors.warn-wash}"
    textColor: "{colors.warn-ink}"
    rounded: "{rounded.pill}"
    padding: "4px 11px"
  panel:
    backgroundColor: "{colors.surface}"
    borderColor: "{colors.hairline}"
    rounded: "{rounded.lg}"
    padding: "22px"
  nav-item:
    backgroundColor: "transparent"
    textColor: "{colors.vault-muted}"
    rounded: "{rounded.md}"
    padding: "11px 12px"
  nav-item-active:
    backgroundColor: "{colors.vault-raised}"
    textColor: "{colors.surface}"
    rounded: "{rounded.md}"
    padding: "11px 12px"
  text-input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.ink}"
    borderColor: "{colors.hairline}"
    rounded: "{rounded.md}"
    padding: "11px 13px"
  focus-ring:
    outlineColor: "{colors.focus-ring}"
    outlineWidth: "2px"
    outlineOffset: "2px"
    darkSurfaceOutlineColor: "{colors.focus-ring-dark}"
---

# Design System: 棱镜工作台 Prism Gateway Console

<!-- FINAL: 本文件描述 2026-10-05 改造完成后的视觉系统，逐令牌取自 console/web/style.css 的 `:root` 与组件规则；改动记录与实测见 docs/superpowers/verification/2026-10-05-ui-console-redesign.md。 -->

## 1. Overview

**Creative North Star: "The Prism Workbench（棱镜工作台）"**

这套系统是一台精密仪器，不是一张宣传页。四条通道（WorkBuddy、GLM/Zcode、Qoder、OpenCode）是入射光，控制台是读数面：用户带着一个具体问题进来——某个账号还能不能用、某个模型现在能不能调、客户端该填什么——页面要在不滚动的情况下回答他。密度、对齐、可扫读优先于气势。

品牌资产是「棱镜」这个名字本身：光从多处来、从一个口出去。它落在品牌标记、通道配色和图表色板上，不落在铺满屏幕的渐变光晕上。深色侧边栏是仪器的机壳，浅色工作区是读数面，两者的明暗分界就是系统的结构线。

改造已落地为单一口径，不再分「现状 / 目标」两端：

- 默认扁平、状态才抬：静态卡片只有一层 1px 描边，全站零 `translateY` 装饰动效，外投影只剩 `--hair`（`0 1px 0`）一条。
- 主标题承载事实不承载情绪：每个视图的 `h1` 就是任务名（`运行概览` / `模型别名` / `API 接入`），英文 eyebrow 层已从 HTML 与 CSS 中整体删除。
- 说明文字只有一个灰阶 `--muted #5b6076`，它在工作区底、白卡底、表头底上的实测对比度都 ≥4.5:1；原先不达标的 `--faint #9aa0bb` 已删除，不留「只给装饰用」的例外。

**Key Characteristics:**

- 明暗双区：`#0f1120` 机壳 + `#f4f5fa` 读数面，工作区内靠 `#ffffff` 面板与 `#dfe3ee` 描边分层。
- 单一强调色族（棱镜紫 `#5b4bf0`，悬停态 `#4a3ad8`）+ 四条通道色，色相不越过紫—青轴。
- 汉字优先：正文行高 1.75，最小可读字号 12px（11px 只允许出现在表头、徽章、指标标签与图表刻度）。
- 表格是一等公民：`min-width:720px` + 横向滚动容器，行 hover 只换底色不换高度。
- 状态三重表达：颜色 + 文字 + 位置，禁止纯色块表示可用/不可用。
- 键盘可达是被量过的：`:focus-visible` 为 `2px solid var(--focus)` + 2px 偏移，深色机壳换亮青 `--focus-dark`；Tab 走查 101 个停留点，最弱的可见环 8.3:1。

## 2. Colors

冷紫—青一族，浅色工作区配深色机壳；除通道标识外不出现第二个高饱和色相。

### Primary

- **Prism Violet**（`#5b4bf0`）：唯一强调色。主按钮实底色（渐变按钮已删）、激活态、趋势图首条线。占比应 ≤10%，超出即偏离系统。
- **Prism Violet Strong**（`#4a3ad8`）：主按钮 hover，同时是链接与 `quiet` 按钮的文字色——即「可点」在浅色面上的统一色。
- **Accent Wash**（`#eceafd`）：强调色的低饱和底。徽章底、用户气泡、`quiet` 按钮 hover。
- **Accent Hairline**（`#d8d4f8`）：强调色描边版本，用于用户气泡边框与激活页签边框。
- **Focus Ring**（`#3a2bc4`）/ **Focus Ring Dark**（`#8ee3ff`）：键盘焦点环两色，浅色面用深紫、深色机壳用亮青，不参与任何其他用途。

### Secondary（通道标识，非装饰）

- **`--prism` 三段**（`#5b4bf0` → `#7c4bec` → `#0a7f96`）：只出现在品牌标记 SVG 内。通道之间靠位置与文字区分，不靠这三段配色。
- **OpenCode Magenta**（`#ec4899`）：OpenCode 通道。
- **Live Green**（`#3ddc97`）：侧边栏自托管状态点。**只在状态确认为「活」时使用**。
- **Ok Ink**（`#176b4c`）on **Ok Wash**（`#e6f5ee`）：成功徽标。深绿到 `#176b4c` 才让 11px 汉字过 AA。
- **Danger Red**（`#c22f3c`）：错误文案；**Danger Wash**（`#fbecee`）是错误底。原 `#d64550` 在浅底上不足 4.5:1，故加深。**禁止**用于普通文字强调。
- **Heat 阶梯**（`#c5372b` / `#d97a1f` / `#cfa62a`）：模型热度 1–3 星，仅此用途。

### Neutral

- **Ink**（`#171a2b`）：正文与数字。
- **Ink Muted**（`#5b6076`）：次级说明、表头、`td small`、指标标签、未激活导航。工作区唯一的说明灰。
- **Canvas**（`#f4f5fa`）/ **Canvas Soft**（`#f9fafd`）：页面底与行 hover 底。**Table Head**（`#f7f8fc`）/ **Group Band**（`#eef0f8`）：表头底与模型分组条。页面底不再叠紫雾渐变，分层只靠描边与留白。
- **Surface**（`#ffffff`）：面板、卡片、输入。
- **Hairline**（`#dfe3ee`）/ **Hairline Soft**（`#edf0f7`）：主描边与行分隔。
- **Vault**（`#0f1120`）/ **Vault Raised**（`#262b50`）/ **Vault Ink**（`#d3d6ee`）/ **Vault Muted**（`#959bc4`）/ **Vault Dim**（`#767ca6`）：侧边栏机壳五色。`Dim` 专用于「通道未启用」的弱化态，不是装饰灰。
- **Warn Ink**（`#7a5210`）on **Warn Wash**（`#fdf1dc`）：需注意但未失败。
- **Code Ink**（`#33385c`）on **Code Wash**（`#f2f4fd`）：代码示例与配置块。

### Named Rules

**The One Beam Rule.** 一个屏幕内，Prism Violet 只允许有一个主人：主按钮，或激活页签，或强调链接。两处同时抢注意力时，把其中一处降为描边。

**The Channel-Is-Data Rule.** 通道色与 `--prism` 的三段是数据编码与品牌标记，不是配色素材。不得拿 `--prism` 铺背景，不得把 `#ec4899` 当第二个 CTA。

**The Honest Green Rule.** 绿点、绿徽章、成功动效只能来自上游确认的结果。未探测 = 中性灰 + 文字「未探测」，不得用绿色占位。

**The Contrast Floor Rule.** 任何承担信息的文字都要过实测 4.5:1，判据取渲染后的背景（含渐变最坏端点与半透明叠加），不是令牌名义色。要新增灰阶之前先问：`--ink` / `--muted` 能不能解决。

## 3. Typography

**Display Font:** Inter（回落 -apple-system / BlinkMacSystemFont / Segoe UI / PingFang SC / sans-serif）
**Body Font:** 同族；汉字由 PingFang SC / Microsoft YaHei 兜底
**Label/Mono Font:** ui-monospace 栈（SFMono-Regular, JetBrains Mono, Menlo, Consolas）

**Character:** 单一无衬线族承担全部层级，靠字号与字重对比拉开，不引入第二款显示字体。等宽只用于 token、模型 ID、日志与图表刻度——它是「这是原始数据」的信号。仓库不打包字体，实际落到 Segoe UI / 微软雅黑，所以个性由配色与信息密度承担，不由字形承担。

### Hierarchy

- **Display**（700, 23px, lh 1.35, ls -0.3px, `text-wrap: balance`）：每个视图唯一 `h1`，写中文任务名；登录页同尺寸。
- **Headline**（700, 17px, ls -0.2px, `text-wrap: balance`）：区块标题 `h2`；`.section-title h2` 收到 16px。
- **Title**（700, 14px）：面板内小标题 `h3`、任务卡标题。
- **Metric**（650, 24px, ls -0.6px, `tabular-nums`）：指标数字。原 36px 巨号已收——数字要占住重心，但不能压过表格。
- **Body**（400, 14px, lh 1.75）：对话气泡、说明段落。
- **Control**（600, 13px）：按钮、`label`、输入控件。
- **Label**（600, 11px）：表头、指标小标签、徽章。大写英文 eyebrow 层已整体删除。
- **Mono**（12px, lh 1.8）：代码示例、任务日志、token 数值。

### Named Rules

**The Hanzi Floor Rule.** 汉字最小可读字号 12px。11px 只允许出现在：表头、徽章、指标标签、图表刻度，且深度不低于 `--muted`。正文性汉字不得降灰。

**The Mono Means Raw Rule.** 等宽字体只表示「这是来自上游/服务的原始字符串」——模型 ID、token 数、日志。不得为排版好看而把中文标签写成等宽。

**The One Scale Rule.** 层级差 ≥1.25 倍（14→17→23 / 12→14→17）。同一屏内不出现两个只差 1–2px 的字号。

**The No-Eyebrow Rule.** 视图上方不挂大写英文小标。层级由 `h1` 的中文任务名与面包屑承担；需要英文时写进 `code` 或模型 ID，不做装饰性字距展示。

## 4. Elevation

默认扁平。浅色面板的分层只靠 1px 描边 + 底色差（`#ffffff` on `#f4f5fa`）；深色机壳靠色阶（`#0f1120` → `#262b50`）分层，不靠外投影。全站静态卡片零投影、零 `translateY`，唯一保留的高度语言是一条 1px 发丝线。

### Shadow Vocabulary

- **Hair**（`--hair: 0 1px 0 var(--line)`）：全站唯一阴影令牌。用在区块分隔与协议切换的选中态上——它是「压平后还剩一点接缝」，不是抬升。
- **Focus Ring**（`outline: 2px solid var(--focus); outline-offset: 2px`，深色区 `--focus-dark`）：键盘焦点。它是状态，不是装饰，且必须落在渲染后的背景上量过 ≥3:1。
- 已删除的旧令牌：`--shadow-sm`、`--shadow`（`0 14px 34px`）、`--shadow-lg`（`0 40px 80px`，登录卡的 ghost-card）、`--prism-btn` 渐变与主按钮的强调色发光投影。登录卡与 `.panel` 现在都是 1px 描边 + `--radius-lg`，无投影。

### Named Rules

**The Flat-By-Default Rule.** 静止态的面板只有一层描边。判据：把某个组件的 `box-shadow` 删掉，如果它反而更清楚了，那它原本就不该有阴影——现在的 CSS 里已经没有可删的了。

**The No Ghost-Card Rule.** 禁止 1px 描边 + ≥16px 模糊投影同挂一个元素（Codex 典型异味）。二选一：描边，或 ≤8px 模糊的定义性投影。本系统统一选描边。

## 5. Buttons

**Shape:** `--radius` 10px，全站按钮统一（`button` 基本规则里就给）；徽章 999px 全药丸。
**Padding:** `10px 16px`，字重 600，字号 13px，无边框；表格内与卡片内按钮收窄为 `7px 12px` / 12px。

- **Primary**：`--accent` 实底 + 白字，hover 转 `--accent-strong`。渐变按钮（`--prism-btn`）与强调色发光投影已删除，因为渐变最坏端点上白字只有 4.23:1。文案是动词 + 对象（`＋ 浏览器授权`、`保存别名`、`发送 ↑`）。
- **Secondary**：白底 + 1px `--line` 描边 + ink 字。hover 转 `--bg-soft` 并把描边加深到 `#c3c9dd`，不新增投影。
- **Quiet**：无底无框，文字色 `--accent-strong`，hover 给 `--accent-soft` 底。用于行内动作（「删除」）。
- **Disabled**：`opacity:.5` + `cursor:not-allowed`。
- **Focus**：`:focus-visible` 统一 `2px solid var(--focus)` + 2px offset；侧栏与 topbar 内换 `--focus-dark`。

**The Verb-First Rule.** 按钮标签必须以动词开头且说明点击后果。「更多」「确定」这类无对象标签禁用。

**The Destructive-Needs-Arm Rule.** 写库且不可逆的行内动作（删除别名、旁路通道退出登录）第一次点击只改标签为「再次点击确认…」，第二次才发请求；不做模态弹窗。互斥的启用/停用用同一档按钮样式，不做强弱差。

## 6. Chips / Badges

全药丸（`border-radius:999px`），`4px 11px`，11px/600，`white-space:nowrap`。

- 中性：`--accent-soft` 底 + `--accent-strong` 字。用于状态、能力标签、`badge-row` 多标签堆叠（gap 6px + wrap）。
- 警示：`--warn-bg` 底 + `--warn-ink` 字（`.badge.warn`）。
- 通道锁定标签 `.pin-tag`：`--accent-soft` 底 + `--accent-strong` 字，与徽章同族。
- **必须自带文字**：徽章从不单独用颜色表达状态。

## 7. Cards / Containers（面板）

**Corner Style:** 容器 `--radius-lg` 14px，内部控件 10px / 8px。旧的 18px、22px 档位已删——没有品牌想要「异常圆」的面板。
**Background:** `--surface` 纯白 + 1px `--line` 描边，零投影。
**Internal Padding:** `22px`（`.panel`）、`16px 18px`（通道卡）、`18px`（趋势面板）。
**表格外壳:** `.table-wrap` 是 `padding:0; overflow:auto` 的面板，内部 `table{min-width:720px}`。
**分组条:** `.model-group-head td` 用 `--group-band` 整行底色表达分组，不用彩色侧条。
**通道卡动作区:** `.channel-card-actions` 紧跟统计行排布，不再 `margin-top:auto`——旁路通道只有 1 个指标而 WorkBuddy 有 5 个，钉底会在短卡中间留一个洞。

## 8. Inputs / Fields

`padding:11px 13px`、1px `--line` 描边、`--radius` 10px、白底、`color:inherit`、`min-width:0`。
**Focus:** 描边转 `--accent-strong`，焦点可见性由 `:focus-visible` 的实色 outline 负责（旧的 12% alpha 光环已删——合成到白底只有约 1.17:1，键盘用户看不出走到哪）。
`input{width:100%}` 是全局默认，因此在 flex 表单里靠 `flex:1` 与 `min-width` 反控。
数字输入（max tokens）宽 120px。只读密钥框配「显示密钥 / 复制」两个 secondary 按钮，默认 `type=password` 掩码；离开接入页时清空并复位为 password 型。

## 9. Navigation

**深色机壳侧栏**（fixed，224px，`@media 1050px` 缩至 190px）：品牌行 → 竖排 `nav`（gap 4px）→ `sidebar-bottom`（状态点 + 退出，`margin-top:auto`）。

- 默认：`--sidebar-muted` 字、透明底、`11px 12px`、`--radius` 圆角；图标是独立 `<span aria-hidden="true">`，读屏不再把 `◫ ◉ ◈` 念成文字。
- Hover：`#191c33` 底 + `--sidebar-ink` 字。
- Active：`--sidebar-hi` 纯色底 + 白字 + 650 字重。**旧的 3px 棱镜渐变左指示条与 `inset` 内描边已删除**（侧条是本系统禁用项，且激活与非激活已有底色差）。
- 未启用通道：`--sidebar-dim`，配合 hover 回落到 `--sidebar-muted`，与「可用但没数据」的 `--sidebar-muted` 分档。
- Topbar：64px、`--surface` 实底 + 1px 下描边、sticky、z-index 20，内容是面包屑 + 服务状态徽章。**玻璃材质已删**（原 `#ffffffcc` + `blur(12px)`），因为 sticky 条上的半透明会让下面的表格文字从它背后穿过来。
- **窄屏 700px**：侧栏转 static、`nav` 变 flex-wrap 药丸阵（`flex:1 1 30%`、居中、12px 字、`9px 8px`）；topbar 转 static 并降到 56px；`h1` 降到 21px。图标与文字都保留，靠缩字号与内边距压缩。

## 10. Tabs / Data Views / Signature Components

- **子页签**（`.workbuddy-tab` / `.zcode-tab` / `.qoder-tab` / `.opencode-tab`）：容器 `border-bottom:1px solid var(--line)`；页签本身透明底、`--muted` 字、`11px 16px`、上半圆角 8px + `margin-bottom:-1px` 压住容器线；active 转 `--accent-strong` 字 + 650 + `border-bottom-color:var(--accent)`。**这是下划线式页签，不是胶囊卡片**，因为页签下面就是内容区，线比盒子更便宜。`role="tablist"` / `aria-selected` 语义已具备。
- **协议切换**（`.protocol-switch`）：外框 `--bg-soft` 底 + 1px `--line` + 10px 圆角 + 3px 内衬，内部按钮 8px 圆角、`9px 20px`，选中态白底 + `--accent-strong` 字 + 650 + `--hair`。`aria-pressed` 驱动。
- **指标条**（`.metrics`）：`auto-fit minmax(190px,1fr)` 的单块外框（1px 描边 + `--radius-lg` + `overflow:hidden`），单元之间靠 1px 左分隔线而不是独立卡片；标签 11px `--muted` → 数字 24px/650 `tabular-nums` → 脚注 11px `--muted`。无 hover 抬升。
- **档位候选条**（`.metrics#cap-tiers`）：借用指标条的外框与分隔线，但单元内是「`档位名 · 规则` → 16px/650 模型名 → 得分·倍率·通道脚注 → `quiet` 的「填入表单」」，不放 24px 大数字——这里并列的是三个候选而不是三个计数。切到「全量榜单」维度时整条退化为一格说明，因为全量表供人工判断，三档只在能力维度下有意义的规则里算。
- **能力维度切换**（`.protocol-switch#cap-dims`）：协议切换的胶囊外观（`--bg-soft` 底 + 3px 内衬 + 选中白底）不变，但 `#cap-dims{flex-wrap:wrap}` 且按钮 `flex:0 0 auto;white-space:nowrap`——维度有 6 项，比协议多一倍，沿用 `@media 440px` 段的 `.protocol-switch button{flex:1}` 会把每项压成竖排单字，窄屏只收紧内边距让它落在两行内。
- **通道卡**（`.channel-card`）：`auto-fit minmax(240px,1fr)` + gap 12px，`16px 18px`，头部名称 + 徽章，统计行，操作按钮组紧跟统计（不钉底）。异常态 `.channel-card.warn` 换 `#e6c48f` 描边 + `#fffcf6` 底。
- **趋势图**（`.trend-svg`）：`width:100%;height:auto`，网格 `--line-soft` 虚线 `4 4`，轴标签 10px 等宽 `--muted`，图例 12×3px 色条 + 12px `--muted` 文字。
- **对话气泡**（`.message`）：`max-width:95%`，14px / lh 1.8，`overflow-wrap:anywhere` + `white-space:pre-wrap`，1px `--line-soft` 描边 + `--bg-soft` 底；user 右对齐转 `--accent-soft` + `--accent-line` 描边；上方 11px/650 `--muted` 角色标签。`.messages` 是 `min-height:160px / max-height:340px` 的滚动列。
- **空态两件套**：表格尾部 `.empty`（`padding:30px`，13px `--muted`）；对话首屏 `.chat-empty`（居中 14px + 12px 副句）。**清空对话会把 `.chat-empty` 放回去**，不留一块空白面板——`clearMessages()` 用 WeakMap 记住被移走的节点，措辞在 HTML 里只有一份。
- **模型热度**（`.heat-stars`）：13px、1px 字距的星号 + `heat-1/2/3` 三色阶（`--danger` / `#8a5410` / `#6d6318`，后两档由橙/黄加深而来以过 AA）。
- **说明块**（`.compat-note`、`.model-group-head td`）：整圈 1px 描边 + 底色阶，不用彩色侧条。
- **全局播报**（`#announce`）：`.sr-only` + `role="status"` + `aria-live="polite"`，由 `notice()` 同步写入；可见的 `#notice` 横幅本身不再挂 `role="status"`，避免读屏重复一次。每条通道页还有各自的 `role="status"` 容器，所以定时刷新的状态与用量对读屏可见。

## 11. Do's and Don'ts

### Do:

- **Do** 保持静止态面板只有一层 `#dfe3ee` 描边，零外投影、零 `translateY`。
- **Do** 用 `#5b4bf0` 系强调色承担全部交互提示，占比 ≤10%；可点文字用 `#4a3ad8`。
- **Do** 正文与小标签对比度 ≥4.5:1，且按**渲染后**背景计算（渐变取最坏端点、半透明先合成）。工作区说明灰统一 `#5b6076`；`#9aa0bb` 这类名义色是本轮 2094 个文本节点里唯一的系统性失败源，已删除。
- **Do** 每个状态同时给颜色、文字、位置三重线索（徽章必带文字；图表折线除颜色外另带直读图例）。
- **Do** 汉字正文 14px / lh 1.75，说明文字最小 12px；11px 只用于表头、徽章、指标标签、图表刻度。
- **Do** 表格按可扫读设计：数字等宽对齐、列宽稳定、行高一致、`min-width:720px` + 横向滚动。
- **Do** 未探测 / 未知 / 未启用使用中性灰 + 明确文字，与「已确认」在视觉上可区分。
- **Do** 键盘完整可用：`:focus-visible` 的 2px 实色环 + 2px offset 不得移除，深色区用 `--focus-dark`；子页签、协议切换、表格内操作按钮全部 Tab 可达，且焦点元素必须留在视口内。
- **Do** 切换视图时 `window.scrollTo(0,0)`：入口都是主动导航，否则新页面会停在上一页的滚动深度上。
- **Do** 动效只表达状态变化，≤160ms `ease`，并由全局 `prefers-reduced-motion` 一键归零。

### Don't:

- **Don't** 做成 **SaaS 官网落地页**（PRODUCT.md 红线）：标语式主标题、四张等宽卡片阵、渐变光晕铺背景、每屏只说一件事。原概览页的 `一切，从连接开始。` 与两张纯说明卡属于此类，已删。
- **Don't** 做成**游戏化仪表盘**（PRODUCT.md 红线）：彩色徽章发光、动画堆叠、用装饰掩盖数据缺失。
- **Don't** 退化成**纯文本终端日志**（PRODUCT.md 红线）：全等宽、无层级、找状态靠肉眼扫关键字。
- **Don't** 做成**杂志式留白排版**（PRODUCT.md 红线）：超大字号、大量留白、每屏三元素，逼用户滚三屏才看到数据。
- **Don't** 在同一元素上同时使用 1px 描边和 ≥16px 模糊投影（ghost-card）。
- **Don't** 给卡片/区块/输入框超过 14px 的圆角；容器上限 `--radius-lg` 14px，内部控件 10px / 8px。
- **Don't** 使用 `border-left`/`border-right` 大于 1px 的彩色侧条做装饰。原两处违规（`.model-group-head td`、`.compat-note`）已改成底色阶 + 整圈描边；侧栏激活项的 3px 渐变指示条同样已删。
- **Don't** 使用渐变文字（`background-clip:text`）。
- **Don't** 把玻璃拟态当材质。`backdrop-filter` 现在全站 0 处，不要为了「好看」再加回来。
- **Don't** 使用 `.eyebrow` 全大写英文小标。这一层已经从 HTML/CSS 删除，`grep -c eyebrow console/web/` 必须是 0。
- **Don't** 用 `#3ddc97` 或任何绿色表示「还没试过」。「未探测」必须是中性色。
- **Don't** 伪造数据：未知用量显示 `—`，不显示 `0`；模拟结果的样式必须与真实结果可区分。
- **Don't** 在正文使用全大写英文段落或破折号（`—`）作为文案修辞。
- **Don't** 把下拉/浮层放在 `overflow:auto` 容器内用 `position:absolute`（`.table-wrap` 会裁切它）；改用 `<dialog>`/popover 或 `position:fixed`。
- **Don't** 为了页面测试通过而削弱产品代码：`console/web_test.cjs` 的 `vm` 夹具只模拟浏览器（`querySelector` / `scrollTo` / `remove`），缺什么补夹具，不改产品行为。
