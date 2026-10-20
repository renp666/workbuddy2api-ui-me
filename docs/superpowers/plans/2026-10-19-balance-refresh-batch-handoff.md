# 交接班文档：P1 #2 余额后台刷新批次收尾（2026-10-19 收工 → 续接）

- **文档编号**：SHIFT-2026-10-19-BALANCE-REFRESH
- **撰写时间**：2026-10-19
- **续接对象**：新会话的自己 / 任何接手者
- **一句话状态**：**P1 #2「余额后台刷新+402硬冷却自动解冻」已实现、测试、commit（`7154a11`），但未发版——生产仍跑 DSML 批镜像。无阻塞事项。下一步默认是 P1 #3「调用来源 IP·UA 归因」对标调研；生产发版（core 镜像）可单独授权随时插入。**

> **给新会话的自己**：先读 §1（盘上事实）与 §4（待办与触发条件）。不要重读本批实现——
> §2 已钉死落点，通道统一性结论与 panel 偏差在 `docs/superpowers/plans/2026-10-19-workbuddy通道对标调研报告.md` §六。

---

## 1. 现在停在哪（盘上事实，已核对）

### 1.1 分支与远端

```text
## main...origin/main [ahead 109]
```

- 领先 `origin/main` **109 个提交**，从未推送。推送需单独授权（AGENTS.md）。
- 工作区 `git status --short` 为**空**（干净）。

### 1.2 本批次提交（已提交、未推送）

```text
7154a11 feat(core): 余额后台刷新与402硬冷却自动解冻        ← 本批主体（6 文件 +384 行）
8519cf2 feat(core): 补丁 0016 出站 DSML 工具调用标记还原     ← 上一批（P1 #1）
987195d docs(plans): m2c 交接文档入库与验收文档勘误
6179560 feat(console): 熔断冷却阶梯退避 + 半开恢复收紧 + 摘链保护
fd6f068 fix(console): 熔断自产 503 不再喂回熔断器
```

### 1.3 生产容器现状（实测核对过镜像 ID 一致性）

```text
生产（project: workbuddy2api-ui-me，宿主端口 7863）—— 10 只全 Up（core/console + 八旁路）：
  console: workbuddy2api-console:local  fadccdd0ed22（= :local 标签，DSML 批）
  core:    workbuddy2api-core:local     49932bfbd565（= :local 标签，DSML 批）
healthz=200 livez=200
```

- **注意**：本批 `7154a11` 只在本地提交，core 镜像**未重建**——生产 core 不含余额刷新能力。发版后该能力自动生效（默认 5 分钟周期，无配置项需要设置）。
- 回退备胎就位：`workbuddy2api-{core,console}:local-prev`。

### 1.4 P1 #2 改动落点（不再重读代码）

| 文件 | 职责 |
| --- | --- |
| `extensions/internal/pool/balance.go` | `HardCooledUIDs`（枚举有效硬冷却号）、`ReviveHardCooledIfCredits`（余额>0 且硬冷却→解冻；软冷却/熔断不解冻） |
| `extensions/internal/scheduler/balance.go` | 周期循环：默认 5 分钟、号间 800ms 限速、`WB2A_BALANCE_REFRESH_MINUTES` 覆盖（<=0 关）、env 解析 `BalanceRefreshInterval` |
| `extensions/cmd/server/extension.go` | wrapCore 一行接线：`go sch.RunBalanceRefresh(ctx, sch.BalanceRefreshInterval())` |
| 调研报告 §六 | 通道统一性书面结论 + 与 panel 两个有意偏差（只刷硬冷却号不刷全池；解冻判据比签到解冻严） |

**零补丁**——全部 extensions 新文件，补丁序列仍止于 0016。console/前端零改动（解冻后状态自然透出）。

---

## 2. 验证状态（本批已跑完，可直接引用）

| 项 | 结果 |
| --- | --- |
| go vet（pool/scheduler/cmd/server） | ✅ |
| 定点回归 10/10（pool 5 + scheduler 5） | ✅ |
| Docker Linux race（golang:1.23，挂宿主 GOMODCACHE） | ✅ 两包 ok |
| 全量测试 | pool/scheduler/cmd/server 各有既有平台失败（合计 6 个），已做基线对照（临时移走改动物化 HEAD 复跑）确认**全部既有**，与本次无关 |
| 通道统一性结论 | 已书面化（调研报告 §六） |

基线对照的操作模式有存档价值：`git show HEAD:<file>` 还原被改文件 → 移走未提交新文件 → `overlay.py prepare` 物化基线树 → 复跑失败测试 → 恢复。用于定界「既有失败 vs 新引入失败」。

## 3. 环境与工具备忘（本批实测有效）

- Go 1.26.2：`C:\Users\Administrator\sdk\go1.26.2\bin\go.exe`；bash 命令一律走 .sh wrapper（`Start-Process bash -ArgumentList '/d/tmp/xxx.sh'`），PowerShell 5.1 不支持 `&&` 与 heredoc。
- Docker race：`docker run --rm -v "D:/...:/src" -v "C:/Users/Administrator/go/pkg/mod:/go/pkg/mod" -w /src -e GOCACHE=/tmp/gocache golang:1.23 go test -race <包>`；wrapper 内需 `export MSYS_NO_PATHCONV=1`（仅当行无 git 操作时安全）。
- Windows 既有失败名单（基线复现，勿当新故障）：pool `TestRateLimitedModelsInStatus`（reset_at 时间断言）、scheduler `TestScriptUsesPrivateCNReadonlySnapshots`（文件 mode）、cmd/server 4 个 InitializeKeys/TaskSchedule（`sync ... Access is denied` 权限类）。
- panel 源码拉取：`raw.githubusercontent.com` 可直连（WebFetch 有 18KB 截断，小文件 OK）；GitHub API 不通。

## 4. 待办与触发条件

1. **生产发版（待授权）**：`bash scripts/local-release.sh`。本批只动 core，console 镜像内容不变但会一起滚标重建。发版后必须核对 `docker inspect` 镜像 ID 与 `docker images` 一致（compose v2 不自动重建，DSML 批已踩），不符则 `docker compose up -d --force-recreate` 全量重建（连旁路）。验证：healthz/livez 200 + `bash scripts/channel-probe.sh` + core 日志确认无 `balance refresh` 报错。
2. **push（待授权，可与发版分开）**：ahead 109。
3. **下一批默认任务：P1 #3「调用来源 IP·UA 归因」**（对标调研报告 §三 #4 前一项）：console usagelog 每条账本记录补 client_ip / UA 摘要，落点 console usagelog + 出口链路；脱敏口径沿用 error_snippet 惯例（200 字截断+脱敏）。注意账本行由 core 写（usagelog 在 extensions/internal/usagelog），来源信息要从公共 Handler 传递——先考证 core 侧能拿到什么（RemoteAddr 经 console 代理后是回环，需 console 传 X-Forwarded-For 类内部头或 bridge 扩展），这是本项的第一个考证点。
4. **用户既定流程**：按功能划分逐个对标→总结拍板→落地；可仿照功能若适用多通道须统一各通道处理并逐一考证（无则留书面结论）。

## 5. 风险与红线提醒

- 生产发版不等于部署授权；push 不等于发布授权——三者各自单独拍板。
- 生产改码期间保通道可用：本批只动 core 扩展层（无既有文件修改），风险面小；但发版重启 core 会瞬时断 WorkBuddy 通道（旁路不受影响），照旧安排即可。
- 不要为「修复」上述 6 个 Windows 既有测试失败而改产品代码。
