# Helicone 样式提取包 · 速查

> 提取自 `github.com/Helicone/helicone`（Apache 2.0），2026-10-07
> 全部色值为**原文照抄**，未做任何主观调整。

## 文件说明

| 文件 | 作用 | 怎么用 |
|---|---|---|
| `styles/helicone-tokens.css` | 全部设计令牌（颜色/圆角/工具类/滚动条） | 合并进你的 `globals.css`，或替换 `@layer base` |
| `tailwind.config.ts` | Tailwind 配置（含 Tremor 色板、safelist、插件） | 覆盖项目根目录同名文件 |
| `components/ui/button.tsx` | Helicone 版按钮（含 3 个私有 variant） | 替换 shadcn 同名组件 |
| `components/ui/sidebar.tsx` | 侧栏骨架（12rem / Ctrl+B / cookie 持久化） | 替换 shadcn 同名组件 |
| `preview.html` | 实时验证页，深浅色可切换 | 双击直接打开 |

---

## 一、核心色值（十六进制速查）

### 深色模式（默认）

| 令牌 | HSL | ≈ HEX | 用途 |
|---|---|---|---|
| `--background` | `0 0% 0%` | `#000000` | 页面最底层，**纯黑** |
| `--card` | `229 84% 5%` | `#050816` | 卡片，**比页面亮一档** |
| `--foreground` | `210 40% 98%` | `#f8fafc` | 主文字 |
| `--primary` | `200 98% 39%` | `#0ea5e9` | 主色天蓝，**比浅色更饱和** |
| `--muted-foreground` | `215 20% 65%` | `#94a3b8` | 次级文字 |
| `--subdued-foreground` | `215 25% 80%` | `#cbd5e1` | 三级文字（比 muted 亮） |
| `--secondary` / `--muted` / `--accent` / `--border` | `217 33% 17%` | `#1c2536` | 四者同值 |
| `--confirmative` | `144 61% 20%` | `#14532d` | 成功态，**深色下很暗** |
| `--destructive` | `0 63% 31%` | `#991b1b` | 危险态，**深色下很暗** |
| `--ring` | `213 27% 84%` | `#d6d3d1` | 焦点环，**深色下变浅** |
| `--sidebar-background` | `oklch(14.5% 0 0)` | `#212121` | 侧栏 |

### 浅色模式

| 令牌 | HSL | ≈ HEX |
|---|---|---|
| `--background` | `210 40% 98%` | `#f8fafc` |
| `--card` | `0 0% 100%` | `#ffffff` |
| `--foreground` | `222 47% 11%` | `#020817` |
| `--primary` | `199 89% 48%` | `#0ea5e9` |
| `--muted-foreground` | `215 16% 47%` | `#64748b` |
| `--subdued-foreground` | `220 20% 35%` | `#475569` |
| `--border` / `--input` | `214 32% 91%` | `#e2e8f0` |
| `--confirmative` | `142 76% 36%` | `#16a34a` |
| `--destructive` | `0 72% 51%` | `#dc2626` |
| `--ring` | `222 47% 11%` | `#020817`（= 主文字色） |
| `--sidebar-background` | `oklch(96.8% 0.007 247.896)` | `#f1f5f9` |

### 品牌蓝（Helicone 独有）

```
heliblue:        #0DA5E8
heliblue-light:  #E6F6FD
heliblue-dark:   #0369A1
```

---

## 二、7 个非标准设计（这是 Helicone 风格的灵魂）

### 1. `--card` 比 `--background` 亮一档
深色下背景纯黑 `#000`，卡片 `#050816`。**纯黑上阴影不可见**，所以靠亮度差做层次。这条不做，卡片就"贴"在背景上。

### 2. `--subdued`：shadcn 没有的第 4 层文字层级
```
foreground            #f8fafc   标题
muted-foreground      #94a3b8   说明文字
subdued-foreground    #cbd5e1   极次要（深色下反而更亮）
```
Helicone 用 `subdued` 承载三级信息。抄这个能给你的界面多一层呼吸空间。

### 3. `--confirmative`：独立的状态色体系
不复用 Tailwind 的 `green-*`，避免和图表绿撞色。深浅两套亮度差 3 倍以上。

### 4. `--ring` 取"反色"而非主色
浅色下焦点环 = 深色文字色；深色下 = 浅色文字色。焦点环要可见但不抢戏。

### 5. `--radius: 0.5rem`（8px）
比 shadcn 默认小一号。小圆角 → 紧凑 → 像工程工具而非营销站。

### 6. 侧栏 `12rem`（192px），不是 240px
因为 Helicone 侧栏还有搜索框 + 大写分组标签，192px 刚好容纳而不浪费横向空间。

### 7. `chart-1..10` 用 oklch，深色模式整体降亮度 ~0.2
```
浅色 chart-1: 0.852 0.199 91.936   （亮橙）
深色 chart-1: 0.652 0.199 91.936   （暗橙）
```
只改第一个数字（亮度），色相 chroma 不变。这样同一组色在两种背景下都可辨识。

---

## 三、按钮变体（Helicone 比 shadcn 多 3 个）

```tsx
// 标准 shadcn 那 5 个
variant="default" | "destructive" | "outline" | "secondary" | "ghost" | "link"

// Helicone 私有 3 个
variant="action"        // 主操作：primary + 边框 + interactive 缩放
variant="glass"         // 毛玻璃：backdrop-blur + 半透明白
variant="ghostLinear"   // 幽灵按钮弱化版

// 尺寸：标准 5 个 + Helicone 4 个
size="sm_sleek"         // h-6 超紧凑，表格行内
size="md_sleek"         // h-8 紧凑，工具栏
size="square_icon"      // 27×27 方形图标，表格操作列
size="none"

// 额外 prop
asPill={true}           // 胶囊圆角
```

`.interactive` 工具类：`hover:shadow-md active:scale-95`，Helicone 所有可点元素都有。

---

## 四、侧栏参数

```ts
SIDEBAR_WIDTH          = "12rem"   // 192px 展开
SIDEBAR_WIDTH_MOBILE   = "16rem"   // 256px
SIDEBAR_WIDTH_ICON     = "3rem"    // 48px 折叠
SIDEBAR_KEYBOARD_SHORTCUT = "b"    // Ctrl/Cmd + B
cookie: sidebar_state，7 天过期
```

分组标签样式：`text-[11px] font-semibold uppercase tracking-wider` + `--muted-foreground`。

---

## 五、工具类

```css
.glass        /* backdrop-blur-md bg-white/[.3333] dark:bg-slate-950/[.3333] */
.interactive  /* transition-transform hover:shadow-md active:scale-95 */

.text-primary    /* text-black dark:text-white */
.text-secondary  /* text-slate-700 dark:text-slate-300 */
.text-tertiary   /* text-slate-400 dark:text-slate-600 */
```

---

## 六、需要的依赖

```bash
npm i tailwind-scrollbar tailwindcss-animate @tailwindcss/typography \
      class-variance-authority lucide-react clsx tailwind-merge
```

Tremor 图表模块（可选，Helicone 用它）：
```bash
npm i @tremor/react @tremor/react-components @tremor/react-charts
```

---

## 七、集成到你的项目（3 分钟）

1. **令牌**：把 `helicone-tokens.css` 内容粘进 `app/globals.css` 的 `@layer base`
2. **配置**：覆盖 `tailwind.config.ts`（关键：`darkMode: ["class"]` 和 `colors`）
3. **组件**：换 `button.tsx` / `sidebar.tsx`，其余 shadcn 组件不用动（variant 名已对齐）
4. **主题切换**：用 `next-themes`，Helicone 就是 `class` 策略

```bash
npm i next-themes
```

```tsx
// app/layout.tsx
import { ThemeProvider } from "next-themes";
// ...
<ThemeProvider attribute="class" defaultTheme="dark" enableSystem>
  {children}
</ThemeProvider>
```

---

## 八、仪表盘栅格（真实布局，直接抄）

来自 `components/templates/dashboard/gridLayouts.tsx`，**12 列 react-grid-layout**：

```
Row 0  (y=0, h=1)  4 个 KPI 卡，各占 3 列
                     cost-req(0-2) prompt-tokens(3-5)
                     completion-tokens(6-8) total-tokens(9-11)
                     ↑ static: true，固定不可拖动

Row 1  (y=1, h=3)  requests(0-5,w6) errors(6-8,w3) top-providers(9-11,w3)

Row 2  (y=4, h=3)  costs(0-3,w4) users(4-7,w4) models(8-11,w4)
```

**这个布局的逻辑**：KPI 横条固定在顶部（4 等分），下面两行用 **6+3+3** 和 **4+4+4** 两种分栏。宽窄有别，不是呆板的等分——这正是它"看起来专业"的原因。

关键属性：所有初始面板都是 `static: true`，即**初始状态不允许拖动**，避免用户一进来布局就乱。你可以保留这个设计。

配套文件值得一起看：
```
dashboard/gridLayouts.tsx          栅格定义
dashboard/dashboardPage.tsx        页面组装
dashboard/filters.tsx              筛选器（时间范围/模型/提供商）
dashboard/panels/                  各面板实现
dashboard/DashboardEmptyState.tsx  空状态 ← 别跳过这个
dashboard/styledAreaChart.tsx      面积图样式
dashboard/DashboardChartTooltipContent.tsx  Tooltip
```

---

## 九、注意事项

- **Licensing**：Helicone 主仓是 **Apache 2.0**（网关部分另有商业许可，UI 样式层可放心用）。抄 `globals.css` 里的色值没问题。
- **`cssVariables: false`**：Helicone 的 `components.json` 里设的是 `false`，但它实际用了 CSS 变量。这是从 shadcn 迁移留下的痕迹，照抄变量方案即可。
- **Tremor 是可选的**：图表色板已内置进 `tailwind.config.ts`，不想装 Tremor 也能用 `bg-chart-1` 这类类名。
- **safelist 必须保留**：Helicone 动态拼接 class（如 `bg-${color}-500`），生产构建会 tree-shake 掉，务必保留 safelist 配置。

---

## 十、Helicone 组件清单（可按需去仓库拿）

```
components/ui/
  accordion  alert-dialog  avatar  badge  button  calendar  card
  chart  checkbox  collapsible  command  context-menu  date-picker
  dialog  drawer  dropdown-menu  hoverCard  input  label  menubar
  popover  progress  radio-group  resizable  scroll-area  select
  separator  sheet  sidebar  skeleton  slider  sonner  switch
  table  tabs  textarea  tooltip

components/layout/
  admin/adminLayout  admin/adminSidebar
  auth/authLayout  auth/DesktopSidebar  auth/Sidebar  auth/NavItem
  navbar/navBarV2  SidebarHelpDropdown  SidebarQuickstartCard

components/templates/
  dashboard/gridLayouts   ← 仪表盘栅格，看这个
  sessions/gridLayouts    ← 请求列表布局
  settings/settingsLayout
  users/gridLayouts
```

`components/templates/dashboard/gridLayouts.tsx` 决定 Helicone 仪表盘怎么排格子，想复刻布局直接看这个文件。
