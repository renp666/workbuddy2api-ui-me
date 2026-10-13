# 交接班文档：通道开源说明与 Qoder 排查收尾（2026-10-13 收工 → 次日续接）

- **文档编号**：SHIFT-2026-10-13-CHANNEL-DOCS
- **撰写时间**：2026-10-13
- **续接对象**：次日（新会话）的自己 / 任何接手者
- **一句话状态**：**「通道开源说明」与「Qoder 通道排查报告」两份文档已写完并核对，均未提交；`watch.py` 有一处未提交的 repin 加固改动；`main` 领先 `origin/main` 65 个提交从未推送。次日第一件事 = 确认这批未跟踪文档与 watch.py 改动的取舍（提交 / 进 .gitignore）。**

> **给新会话的自己**：先读 §1（盘上事实）与 §2（待决策），不要重新调研 Qoder 排查——
> §3 已把结论钉死。本机 8 只容器全在跑，无需重启。

---

## 1. 现在停在哪（盘上事实，已核对）

### 1.1 分支与远端

```text
* main [origin/main: ahead 65]
```

- 领先 `origin/main` **65 个提交**，从未推送。推送需单独授权（AGENTS.md）。

### 1.2 最近已提交（本轮相关）

```text
8d3058d fix(watchdog): read_trae_model 截断行内注释，还原 pinned 别名（1-daily-v1）
5838b76 feat(watchdog): prism-model-watch 一劳永逸版（别名基线diff+最小真探针…）
3b13724 docs(token-gateway-ui): 收录 UI 改造规划、项目审查报告与配色/主题研究素材
546bff4 fix(console-web): 概览通道状态不再画假绿
01bc129 fix(console): /admin/health 不再假绿
```

### 1.3 未提交的工作区（次日决策点）

```text
 M scripts/prism-model-watch/watch.py          ← repin 加固，语法已过、功能未实测
?? docs/通道开源说明.md                          ← 本轮新建，已核对，未提交
?? docs/token-gateway-ui/2026-10-13-Qoder通道排查报告.md ← 本轮新建，未提交
```

1. **`docs/通道开源说明.md`**（新建，约 190 行）：7 条通道的源码来源 / License / 改造入口 / 更新手册，
   供后续 agent 追踪上游或找改造思路。**本轮已做一次纠错**：qoder 小节初稿沿用了 `AGENTS.md` 第 34 行
   的旧描述（「无控制面/OAuth/生命周期」），核对代码后发现 commit `bae3a44` 之后已有完整设备码
   OAuth 登录链路（`console/qodercontrol.go` + `deploy/qoder-login-ctl.cjs` + Compose
   `CONTROL_PORT: 3001`），已按代码改对并在文中标注 AGENTS.md 的滞后。
   其余 6 条通道的来源描述均已对照 `upstream.lock`、Dockerfile、overlay YAML、源码头注释逐条核实。
2. **`docs/token-gateway-ui/2026-10-13-Qoder通道排查报告.md`**（新建，约 395 行）：Qoder「模型无法使用」
   排查全过程，P0（扩白名单）已执行并有前后实测对照，P1/P2 留待决策。
3. **`watch.py` 未提交改动**（+22/-7，语法 `ast.parse` 通过）：
   - `repin()` 保留行内原有注释并追加 `# repin 旧 -> 新` 历史，写前自动备份 `*.bak-repin-<时间戳>`。
   - 自动重钉触发条件从「探针失败」放宽为「探针失败 **或** 别名从 `/v1/models` 消失」
     （最常见的上游轮换形态是别名被直接摘掉，旧条件抓不到）。
   - **未做实跑验证**（`--help` 正常，但没跑过真实 `--auto-repin` dry-run）；提交前建议先
     `python scripts/prism-model-watch/watch.py --auto-repin --verbose`（默认 dry-run，不写文件）过一遍。

### 1.4 硬约束核验（已过）

```text
git diff -- upstream upstream.lock   → 空
```

### 1.5 运行时状态（2026-10-13 实测）

| 项 | 状态 |
| --- | --- |
| 容器 | 8 只全部 Up 40 分钟（core、console、6 旁路），无重启循环 |
| `runtime/wb2api/console/routes.json` | `enabled_models` 30 条 = 26 qoder + autoclaw-cn:glm-5.3-flash + global:deepseek-v4.1-flash + raccoon-sn-glm-5-3-flash + opencode-OC · Nemotron 3.5 Lightning Free |
| 备份 | `runtime/wb2api/console/routes.bak-20261013-095727.json`（Qoder 扩白名单前） |
| Python / Node | 可用 |

---

## 2. 待决策（次日按优先级）

### P0 — 两份新文档 + watch.py 的取舍

- `docs/通道开源说明.md`、Qoder 排查报告：提交（docs 类，不触发重建镜像）或按用户偏好进 `.gitignore`。
  **按惯例等用户确认后再提交。**
- `watch.py`：先跑一次 dry-run 验证，再决定是否连同提交。

### P1 — AGENTS.md 第 34 行的 qoder 描述滞后（事实性，已确认）

AGENTS.md qoder 段仍写「qoder-proxy 无控制面/OAuth/生命周期，由 `WB2A_QODER_PAT` 提供 PAT，
console 只读状态与对话测试」，与 `bae3a44` 后的实际代码（页面设备码登录、
`/admin/qoder/login|logout`、`WB2A_QODER_CONTROL_URL`、overlay `CONTROL_PORT: 3001`）不符。
`docs/通道开源说明.md` §3.3 已按代码写对并标注了这处滞后。**是否回改 AGENTS.md 待用户确认**
（AGENTS.md 是 AI 开发指南，改动需谨慎）。

### P2 — Qoder 排查报告遗留项（报告 §11「未做」）

| 项 | 内容 | 涉及 |
| --- | --- | --- |
| P1-a | 裸模型名（如 `qwen3.8-flash`）不带 `qoder-` 前缀会落 core，撞 pin 报 `pinned_account_unavailable`——考虑加裸名归属推断 | `console/zcode.go` `publicRouter()` |
| P1-b | 或至少改进错误文案，提示该名字属于 qoder 通道 | `extensions/internal/pin/pin.go` |
| P2 | qoder 上游版本未 pin（`git clone --depth 1` 浮动）、旧 id 防呆、`/admin/probe` 结论未落盘核对、`4-max-v1` note 字段仍写「4 千问」 | 各处 |
| 观察 | watchdog 30s 超时对 qoder 偏紧（尾延迟可达 17s），建议放宽到 45–60s | `watch.py` / 调度 |

---

## 3. 已钉死的结论（不要重新调研）

- **Qoder 通道本体无故障**：qoder-proxy 1.6.0，26/26 模型实测全 OK，登录/流式/工具/并发全通过，
  本仓两个补丁在 1.6.0 上仍命中。详见排查报告 §2、§6。
- **「模型无法使用」两个根因**：① 本仓自加的发布白名单只放行 1 个 qoder 模型（**P0 已修**，
  扩到 26 个，`/v1/models` 从 16 条涨到 41 条）；② 裸名不带前缀落 core 撞 pin（P1 未动）。
  白名单机制来源 = `f8fed97` + `409997b`（双轨开关：调用放行 / 公布走白名单）。
- **7 条通道的源码来源**已全部写入 `docs/通道开源说明.md`：core=Sliverkiss 快照（MIT）、
  zcode/qoder/opencode=外部引用（License 未核实）、autoclaw=仓内 Python 自研、
  trae=移植 AgentPoints（MIT）、raccoon=参考 aimod-cc/agent2api（MIT+限制）。

---

## 4. 文档地图

| 要找什么 | 读哪里 |
| --- | --- |
| 通道源码来源 / 更新手册 | `docs/通道开源说明.md`（本轮新建） |
| Qoder「无法使用」排查全过程 + 修复记录 | `docs/token-gateway-ui/2026-10-13-Qoder通道排查报告.md` |
| 三层血缘、能改哪一层、验证命令 | `AGENTS.md` |
| 通道候选 / License 纪律 | `docs/superpowers/plans/2026-10-08-channel-expansion-plan.md` |
| watchdog 用法 | `scripts/prism-model-watch/README.md` |
| 上一班交接格式参考 | `docs/superpowers/plans/2026-10-02-prism-credit-handoff.md` |

---

## 5. 授权边界（AGENTS.md）

- 推送 Git、发布镜像、重启实际服务、更新上游——分别需对应授权；**发布 ≠ 部署**。
- 本机发版：`bash scripts/local-release.sh`（构建上下文是 `git archive HEAD`，
  **未提交改动不进镜像**——先提交再发版）。
