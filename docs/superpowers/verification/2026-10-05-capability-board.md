# 模型能力 TOP · 验证记录

- 日期：2026-10-05
- 需求：在 Agent 接入页新增「模型能力 TOP」，按编程 / 逻辑 / 设计 / 视频等维度出榜单，数据**采集网上现有权威榜单**并带更新日期，用于辅助设置模型别名供不同 Agent 环境使用。追加确认的口径：两条轴「能干活 → 能省钱」，采纳三档别名（my-coder / my-coder-pro / my-coder-ultra），并额外要一张**全模型榜单**供人工判断。
- 范围：`console/`（`capability.go` 新增、`capability_test.go` 新增、`heat.go` `server.go` `route.go` `credits.go` `browser_preview_test.go` 改）与 `console/web/`（`index.html` `app.js` `style.css`）、`console/web_test.cjs`、`scripts/ui_probe.py`、`DESIGN.md`。core、`upstream/`、`patches/`、`extensions/`、`deploy/` 一笔未动。
- 交付边界：**未提交、未发版、未部署、未推送 Git、未重启服务**。用户指令「改造后不要急着发版，怕影响其他项目调用网关 llm」在本记录关闭前有效；提交、发版、部署各自需要单独授权。

## 0. 结论先行

| 判据 | 结果 |
| --- | --- |
| `go -C console test ./...` | **ok** 1.132s（含 5 条新增 capability 解析测试） |
| `node --test console/web_test.cjs` | **97 pass / 0 fail**（新增 5 条，含全量视图截窗测试） |
| 界面探测（28 个视图×视口报告） | 对比度不合格 **0**、裁切元素 **0**、横向溢出 **0**、未命名控件 / 未标注输入 **0** |
| 键盘走查（桌面 14 视图） | Agent 接入页 19 个 Tab 停留点，最弱焦点环 **8.3:1**，weakRing / noRing / offscreen 各 **0** |
| 探测分类器自检 | 11 个正反样本 × 2 视口全 `ok`，`live=True`（判据本身可信） |
| 渲染时序 | 28 个报告 `settled` 与 `boardReady` 全 `True`，无 `UNSETTLED` / `BOARD-NOT-READY` |
| `docker compose ... config --quiet` | **rc=0** |
| `git diff --check` | **rc=0** |
| `python3 -m unittest discover -s scripts` / `-s deploy` | 30 条 + 2 条红，**与 HEAD 的临时 worktree 按失败测试名逐条比对完全一致**，属本机环境性红（symlink `WinError 1314`、`os.chmod` 不产出 0o700/0o777、依赖真实 bash/docker 入口），非本次回归 |
| `bash scripts/check.sh` | **rc=1**，但 11 条 `--- FAIL:` 测试名与 6 个 FAIL 包（`cmd/server` `internal/bridge` `internal/pin` `internal/pool` `internal/scheduler` `internal/taskrun`）与改造前基线日志 `.build/check-final.log`（00:49，本功能开工前）**逐条同名，仅耗时不同**；`internal/taskrun` 两次都是 ~600 s 测试超时。脚本 `set -eu` 在 core `go test ./...` 处截断，其后的 console / node / python 步骤从未执行，故这三步以上表的单独命令为准 |

## 1. 数据面：一份快照，两个出口

`console/capability.go` 把外部目录收敛成**进程内单份快照**，`/admin/heat` 与 `/admin/capabilities` 都从同一份 `[]json.RawMessage` 派生，因此页面上两处热度不可能互相打架。

- 目标地址是**写死的常量**，不做成可配置项：可配置目标会把管理面板变成 SSRF 跳板。
- TTL 24 h、抓取预算 30 s（本机实测吞吐 3 KB/s–155 KB/s，旧的 8 s 会稳定失败）、失败时保留旧快照、从未取到则 `{"available":false}`。
- 未知量一律不伪造：积分未公布显示「未公布」而不是 0；缺指数的行显示 `—`；快照时间戳标注为**采集时间**，因为源站不发布各项评测的日期，这一点在页面脚注里原样写明。

载荷实测（非估算）：源站目录 `763,948` 字节 / `466` 行；保留 `pricing` 的投影约 `102` KB。全量视图只渲染前 100 行并在页面注明命中总数，避免手机远程操作时首屏卡死，筛选仍作用于全表。

## 2. 界面

6 个维度（编程 / 智能体 / 综合智能 / 设计 / 长上下文 / 全量榜单）；设计维度另有 25 项类目下拉，默认自动选中。三档候选固定为「免费兜底 · 倍率明示为 0 里分最高」「最省 · 倍率最低，同价取分高」「上限 · 不看花费，本维度分最高」，每档一个「填入表单」按钮，点击只回填别名表单、不写库。档位永远按整份候选算，筛选只影响表格。

## 3. 截图（隔离模拟夹具）

预览：`WB2A_BROWSER_PREVIEW=1 go -C console test -run '^TestAdminBrowserPreview$' -v -timeout 40m` → `127.0.0.1:17864`，管理密钥 `browser-preview-key-mock-only-12345`（mock only）。不接触真实账号、凭据、容器与上游服务；**别名表、通道模型名、倍率是夹具素材**，而榜单名次与指数来自真实外部快照。

| 文件 | 视口 | 内容 |
| --- | --- | --- |
| `2026-10-05-capability-routes-desktop-mock.png` | 1440×900 | 编程维度：三档候选 + 池内榜单 |
| `2026-10-05-capability-routes-phone-mock.png` | 390 全页 | 窄屏整页：维度条换行落在两行内、无溢出 |
| `2026-10-05-capability-design-desktop-mock.png` | 1440×900 | 设计维度 + 类目下拉，列头转「设计」，缺 AA 综合分的行显示 `—` |
| `2026-10-05-capability-fullboard-desktop-mock.png` | 1440×900 | 全量榜单：热度名次、池内/池外标记、仅池内行给「填入表单」 |

## 4. 取证边界

- 基线日志 `.build/check-final.log` 与本轮 `.build/check-cap-ship.log`、探测产物 `.build/ui-probe/` 都在 gitignore 目录内，不入库；上表引用的 11 条同名红要复核，按第 5 节命令在 HEAD 的临时 worktree 里重跑一次 `check.sh` 即可，别把"基线在本地"当成不可证。
- `go test -race` 本机跑不了：无 gcc，`CGO_ENABLED=0`，容器里的 `golang:1.23-alpine` 同样缺 gcc 且 `apk add` 卡死。因此 console 竞态测试这一步是**未执行**，不是通过。
- 隔离预览进程按设计服务 900 s 后自行结束，其 `-v` 日志在本机后台运行时落为空文件，所以预览的自报行不作为证据；服务成功的证据是 28 个视图的探测产物本身。

## 5. 复跑命令

```bash
WB2A_BROWSER_PREVIEW=1 go -C console test -run '^TestAdminBrowserPreview$' -v -timeout 40m
python3 scripts/ui_probe.py --out .build/ui-probe --tag cap-ship2 --viewports desktop,phone
python3 scripts/ui_probe.py --out .build/ui-probe --tag cap-design --only 10d   # 只重拍单视图
go -C console test ./...
node --test console/web_test.cjs

# 判"红是不是我改出来的"：在 HEAD 的临时 worktree 里跑同一套，按失败测试名 diff（不碰用户工作区）
git worktree add ../.wt-head-check HEAD
( cd ../.wt-head-check && python3 -m unittest discover -s scripts -p 'test_*.py' 2>&1 | grep -E '^(FAIL|ERROR):' | sed 's/ (.*//' | sort ) | diff - <(
  python3 -m unittest discover -s scripts -p 'test_*.py' 2>&1 | grep -E '^(FAIL|ERROR):' | sed 's/ (.*//' | sort)
git worktree remove ../.wt-head-check
```

`ui_probe.py` 本轮新增：`--only`（按 slug 子串重拍单视图，省一整轮 4 分钟）、`10d-routes-design` 视图、`settled` / `boardReady` 两个时序标志，并把所有等待改走 `page.evaluate` 轮询（`page.wait_for_function` 在本页 CSP `script-src 'self'` 下间歇 `EvalError`，旧代码的 `try/except: pass` 把每次等待静默降级成了固定 600 ms 睡眠）。

## 6. 未做（在册，非新发现）

- 别名实际写入调用账本属 core 层，本次未碰。
- 真实通道目录与外部快照的命中率要等部署后才有意义，模拟夹具不能证明。
- 停用通道 / 退出登录 / 协议切换清空对话的二次确认，是改造前既有缺口，本次未扩大范围。
