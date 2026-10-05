# 棱镜网关控制台 · UI/UX 改造验证记录

- 日期：2026-10-05
- 范围：仅 `console/web/`（`index.html` / `style.css` / `app.js`）与 `console/web_test.cjs`。core、`upstream/`、`patches/`、`extensions/`、`deploy/` 一笔未动。
- 交付边界：**未发版、未部署、未推送 Git、未重启服务**。用户指令「改造后不要急着发版，怕影响其他项目调用网关 llm」在本记录关闭前有效；发版与部署各自需要单独授权。
- 取证环境：隔离模拟夹具 `WB2A_BROWSER_PREVIEW=1`，端口 `127.0.0.1:17864`，管理密钥 `browser-preview-key-mock-only-12345`（mock only）。不接触真实账号、凭据、容器与上游服务；截图里的数据全是夹具素材。

## 0. 结论先行

| 判据 | 改造前 | 改造后 |
| --- | --- | --- |
| 对比度不合格文本节点 | **216** / 1439 | **0** / 2094（36 个视图×视口组合） |
| 键盘焦点环最弱值 | **1.94:1**，52 处 weakRing | **8.3:1**，weakRing/noRing/offscreen 各 **0**（12 视图 101 个 Tab 停留点） |
| `liveRegions=0` 的视图 | **12** | **0**（36 个组合最少 1 个） |
| 横向溢出（`scrollWidth>clientWidth`） | 1 处（390px 下 `input#api-endpoint` 334>302） | **0** 处，`documentOverflowX` 全 false |
| 未命名控件 / 未标注输入 / 无表头表 | 未逐项目测 | 全 **0**（含 21 个带表视图） |
| 页面测试 `node --test console/web_test.cjs` | 89 pass | **91 pass / 0 fail**（新增 2 条，均经变异验证） |
| `go -C console test ./...` | ok | **ok** 1.174s |
| `git diff --check` | — | **rc=0** |

审查基线：`.impeccable/critique/2026-10-04T15-48-23Z__console-web-index-html.md`，总分 25/40，P0 0 · P1 4 · P2 7 · P3 3。

## 1. 判据与复跑命令

```bash
# 1) 起隔离预览（40 分钟超时，用完关进程）
WB2A_BROWSER_PREVIEW=1 go -C console test -run '^TestAdminBrowserPreview$' -v -timeout 40m
# 2) 量一遍：对比度 / 溢出 / 语义 / 键盘走查，含分类器自检
python scripts/ui_probe.py --tag after --viewports desktop,laptop,phone --out .build/ui-probe
# 3) 页面回归
node --test console/web_test.cjs
go -C console test ./...
```

`scripts/ui_probe.py` 是本轮新建的 Playwright（`channel="msedge"`）判据脚本，未纳入 `scripts/check.sh`，因为本机才具备 Edge；它自带自检样本，自检不通过会打印 `DEAD` 并非零退出，读数不可用。

## 2. 改了什么（按审查条目对账）

### 令牌层（P1-1 / P1-2 / P1-3 的根因都在这里）

- `--faint #9aa0bb` **删除**，说明灰只剩 `--muted`，由 `#6c7189` 压深到 `#5b6076`（工作区底、白卡底、表头底三处渲染后实测均 ≥4.5:1）。
- 主按钮 `--prism-btn` 渐变（白字在 `#8b5cf6` 最坏端点只有 4.23:1）**删除**，改 `--accent #5b4bf0` 实底 + hover `--accent-strong #4a3ad8`。
- 焦点环从「关掉 outline 后画 12% alpha 光环」（合成到白底 ≈1.17:1）改为 `:focus-visible{outline:2px solid var(--focus) #3a2bc4; outline-offset:2px}`，深色机壳与 topbar 内换 `--focus-dark #8ee3ff`。
- `--prism` 品牌渐变收深为 `#5b4bf0 → #7c4bec → #0a7f96`，且只留给品牌标记 SVG；danger `#d64550→#c22f3c`、heat 后两档 `#d97a1f→#8a5410`、`#cfa62a→#6d6318`，都是为了让 11–13px 文字过 AA。
- 圆角档位从 `22/18/14/10` 收到 `14/10/8`（面板上限 14px，不出现「异常圆」）。
- 阴影令牌 `--shadow-sm/--shadow/--shadow-lg` 删除，只留 `--hair: 0 1px 0 var(--line)`。

### 结构与文案（P2）

- 每个视图的英文 eyebrow 层（`OVERVIEW` / `GLM CHANNEL` …，10px/750/2.4px，实测 3.73:1）从 HTML 与 CSS 中整体删除，`grep -c eyebrow console/web/*` = 0。
- 标语式 `h1` 换成中文任务名：`一切，从连接开始。` → `运行概览`；`给模型起个名字，Agent 只填一次` → `模型别名`；`接入你喜欢的客户端` → `API 接入`；登录页 → `输入管理密钥进入控制台`。
- 概览底部两张纯说明卡（`你的数据，在你的服务器` / `状态不是一次真实调用`）删除；概览 section 说明行与页面副标题去重。
- 侧栏导航图标包进 `<span aria-hidden="true">`，读屏不再把 `◫ ◉ ◈` 念成文字；激活项的 3px 渐变左指示条删除，改纯色底 `--sidebar-hi`。
- topbar 的玻璃材质（`#ffffffcc` + `blur(12px)`）删除，改实底 `--surface` + 1px 下描边；`backdrop-filter` 现在全站 0 处。

### 违规样式清除（PRODUCT.md 红线 / detect 判据）

- `.model-group-head td` 的 `border-left:3px solid #8b5cf6` → 整行 `--group-band` 底色阶。
- `.compat-note` 的 `border-left:3px solid #a9a2ff` → 整圈 1px 描边 + `--code-bg`。
- `background-clip:text` 0 处；`repeating-linear-gradient` 0 处；描边 + ≥16px 模糊同挂的 ghost-card 0 处；hover `translateY` 装饰 0 处。
- 现存 3 条 `transition` 全部 `.14s`，并有一条全局 `@media(prefers-reduced-motion:reduce)` 归零。

### 组件行为

- 子页签从「胶囊卡片」改为下划线式（`border-bottom:2px`，active 换 `--accent`），因为页签下面直接是内容区。
- `.metrics` 从四张独立卡改成一个外框内分隔条（`auto-fit minmax(190px,1fr)` + 1px 左分隔线），指标数字 36px → 24px `tabular-nums`。
- 新增 `.sr-only` + `#announce`（`role=status aria-live=polite`），`notice()` 同步写入；可见的 `#notice` 横幅不再挂 `role="status"`，避免读屏播报两次。
- 别名删除按钮加内联二次确认（第一次点击只把标签改成「再次点击确认删除」）。
- 只读端点框补 `.access-grid input[readonly]{font-family:var(--font-mono);font-size:12px}`（原先继承 16px 默认字号），P3-1 的 390px 溢出消失。

## 3. 数字判据看不出来、靠肉眼发现的三个缺陷

这三条都是**改造前就存在**的产品缺陷，数值闸门全绿时它们照样在。

1. **对话面板被清空成一片空白**。`$('messages').replaceChildren()` 会把 HTML 里的 `.chat-empty` 首屏引导一起抹掉，清空后是一块无说明的空面板，读起来像面板坏了。修法：`clearMessages(container)` + `WeakMap` 记住被移走的引导节点，清空即放回；措辞在 HTML 里仍只有一份，未引入 `innerHTML`（该文件保持 0 处 `innerHTML`）。5 个调用点全部收敛（WorkBuddy / Zcode / Qoder / OpenCode / 退出登录）。回归：`clearing a transcript restores the pane invitation instead of a blank box`。
2. **通道卡中间一个洞**。`.channel-card-actions{margin-top:auto}` 把按钮组钉在卡片底部，而旁路通道只报 1 个指标、WorkBuddy 报 5 个，短卡就在统计与按钮之间裂开一块空白。修法：去掉 `margin-top:auto`，动作区紧跟统计。复验：`2026-10-05-ui-01-overview.png`。
3. **切换视图不回到顶部**。`showPage()` 只切 `hidden`，新页面停在上一页的滚动深度上，短页 `API 接入` 会开在表单中段、标题在折线以上看不见。修法：`showPage()` 里 `window.scrollTo(0, 0)`；已核对该函数三个调用点全是主动导航（侧栏按钮、通道卡「前往测试」、`前往测试`），没有轮询路径会把页面拽回顶部。回归：`changing page starts the new view at its own top`。

## 4. 判据自身的三处假阳性（先修尺子，再谈通过）

改造后首跑还剩 3 条“缺陷”，逐条追下去都是**量错了**，不是改错了。每条都补了能判别的自检样本，避免尺子静默失效。

- **`no-live-region` 误报登录页**：谓词要求元素有非零盒子，而空的 `role="alert"` 盒子高度为 0，可文字一落进去就会被播报。改成 `el.checkVisibility({visibilityProperty:true})`——仍会走祖先链，所以登录时隐藏的控制台壳子还是被正确排除。
- **`weakRing / minFocusRing=1.6` 误报**：把 `outline-offset:2px` 的环按**元素自己的填充色**算对比度，而环实际落在容器底上。抽出 `ringScore()`，`offset>0` 时从父元素解背景，并加一对**判别样本**把两种情形劈开：同一枚 `#3a2bc4` 环，`offset:2px` 读 9.04（合格）、`offset:0px` 读 1.6（不合格）；另加 12% alpha 的 `box-shadow` 环与 `outline:auto` UA 环两条。
- **`offscreen` 误报**：Chrome 把新聚焦元素滚到视口顶端时带设备像素取整，完全可见的按钮会读出 `top=-0.00003`。视口判定放宽 1px 容差。

自检终态：11 条样本 × 3 视口 = 33 行全 `ok`，`live=true`，无 `DEAD` 行。

## 5. 未做 / 在册（不是新发现的缺陷，是明确没做的部分）

| 项 | 状态 | 说明 |
| --- | --- | --- |
| P2「破坏性动作加确认」 | **只做了别名删除** | 停用通道、顶栏退出登录、协议切换清空对话仍是单击即生效。Zcode/Qoder 页签内退出早已有二次确认（本轮未动）。 |
| P2「表格补 `th scope` / `<caption>`」 | **未做** | 判据只测「有没有表头行」（21 个带表视图全 0 缺表头），测不出 `scope` 缺失；`grep -o 'scope="col"' console/web/*` = 0。 |
| P2「四通道模型表列集统一」 | **未做，且判断不该做** | core/zcode/qoder 三张表在改造前就已同列（10 列）；opencode 少「积分消耗」「操作」是数据面真缺（无积分、无单模型操作），强行补表头会造出永远为 `—` 的列。审查文档把这行写成「四通道列集不同」，实测差异只在 opencode 一处。 |
| P3「字体栈写 Inter 但不打包字体」 | **未做** | 保持现状，个性已由配色与信息密度承担，见 DESIGN.md §3。 |
| `live` 变体模式 | **未开** | `.impeccable/live/config.json` 里 `cspChecked:false`：console 有 CSP，要在浏览器里做变体实时替换需要改 Go 侧 CSP，属另一条授权面。 |
| DESIGN.md | 已刷到终态 | `DESIGN.md` 现逐令牌对齐 `console/web/style.css`；frontmatter 的 colors/typography/rounded/components 全部按实测值重写。 |

## 6. 未验证项（如实登记，附本机根因）

| 项 | 状态 | 根因 / 边界 |
| --- | --- | --- |
| `bash scripts/check.sh` 全量 | **rc=1，未通过** | 失败全在 core 包：`cmd/server`、`internal/bridge`、`internal/pin`、`internal/pool`、`internal/scheduler`、`internal/taskrun`。console 是独立 Go 模块、不参与 core 物化，本次改动面（`console/web/*` + `console/web_test.cjs`）无法触达这些包；核心阶段失败后脚本即中止，所以其中嵌套的 console 竞态测试与页面测试**不由这次运行覆盖**，已改为单跑（见 §0）。 |
| └ 其中的 Windows 归因 | **实测** | `sync <temp dir>: Access is denied.`（Go 对目录 `fsync`；Python 侧同调用报 `PermissionError [Errno 13]`）；`pin_test.go:83: pin.json perm = 666, want 0600`（NTFS 上 `chmod` 是建议性的）。这两条解释了 `cmd/server`/`bridge`/`pin`/`pool`/`taskrun` 的红。 |
| └ 一条**不是**环境原因的失败 | **在册，待你裁** | `internal/pool TestRateLimitedModelsInStatus` 是纯内存用例（无文件、无网络）：`pool_test.go:1099` 期望未截断时 `row.ResetAt≈reset`，而 `state.go:463` 只在 `ResetAt != Until` 时才写 `row.ResetAt`。两侧证据：当前工作树（series 无 0014）与 `.build/core-0014`（含 0014）里同一条都以同样读数失败（`reset_at=0001-01-01 00:00:00 +0000 UTC`），而 0014 只碰 `internal/upstream/global_models.go` 与 `internal/server/handler.go`，不碰 pool。结论：与本次 console 改动无关、与你未提交的 0014 回退也无关，属既有的 core 层测试/实现口径分歧。未修，也不该由我在这一笔里顺手修。 |
| Go `-race` | **不可跑** | 本机 `cgo: C compiler "gcc" not found`，竞态测试需要 CGO。 |
| `python3 -m unittest discover -s scripts` / `-s deploy` | **未跑** | 同一 Windows 语义（symlink 需特权、`os.mkfifo` 缺失、POSIX 权限位）；本轮不涉及 overlay 物化与 Compose，AGENTS 的验收触发条件未命中。 |
| `docker compose config --quiet` | **未跑** | 本轮不改任何 YAML、镜像、密钥或持久化路径。 |
| 真实上游调用 | **未验证** | 夹具环境不证明真实上游授权、模型可达或用量返回；对话测试面板里的响应全是模拟素材。 |

## 7. 发版耦合点（为什么"UI 改动"不能顺手发版）

`scripts/release.sh` 从**当前工作树**构建两个镜像。工作树里还有一笔不属于本次的未提交改动：`patches/0014-global-realtime-catalog.patch` 被删、`patches/series` 去掉 0014、`patches/README.md` 相应改写——这会改变 core 物化结果，也就改变其他项目从 `/v1/models` 看到的模型目录。**现在跑发版，会把这笔 core 回退一起打进镜像。** 要发 UI 版本，先把 core 那笔单独定案（提交或还原），再走发版授权。

## 8. 产物清单

| 文件 | 变更 |
| --- | --- |
| `console/web/style.css` | 令牌与组件重写（362 行改动） |
| `console/web/index.html` | eyebrow/标语删除、`#announce` 全局播报、导航图标 `aria-hidden`、概览说明卡去掉（30 行） |
| `console/web/app.js` | `clearMessages()`、`showPage()` 滚回顶部、`notice()` 同步播报、别名删除二次确认（33 行） |
| `console/web_test.cjs` | 夹具补 `querySelector`/`scrollTo`/`remove`，新增 2 条回归（52 行，共 91 条用例） |
| `scripts/ui_probe.py` | 新增：对比度/溢出/语义/键盘走查判据，自带 11 条自检样本（未接进 `check.sh`） |
| `PRODUCT.md` / `DESIGN.md` | 新增 / 已刷到终态 |
| `docs/superpowers/verification/2026-10-05-ui-*.png` | 17 张：12 张改造后桌面全视图、3 对改造前后（登录、运行概览、WorkBuddy 对话）、2 张 390px 手机（概览、Agent 接入） |
| `.build/ui-probe/`（gitignore） | `before-probe.json` / `after-probe.json` 与逐视口截图 |

README 尚未引用这批截图（需要的话另说一笔）；图片均为隔离模拟夹具环境，不含真实密钥与账号信息。
