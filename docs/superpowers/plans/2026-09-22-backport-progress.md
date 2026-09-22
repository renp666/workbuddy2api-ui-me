# 回移进度与交付状态（2026-09-22）

任务书：`docs/superpowers/plans/2026-09-22-backport-handoff.md`
实施方：Qoder（三创会话）｜ 状态：**代码与文档均已完成，未推送**
验证记录：`docs/superpowers/verification/2026-09-22-backport-verification.md`
T0 分析：`docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md`

## 1. 任务完成度（按任务书判据逐条）

| 任务 | 状态 | 判据落点 | 证据 |
| --- | --- | --- | --- |
| T0 独立复析 | **降级完成** | T0-a/b/c/e 满足；**T0-d（盲析自证）不满足** | 分析文档 §0 声明 + §1 的 A1–A10 命令输出 |
| T1 WAF IP 级 fail-fast | 完成 | T1-a…T1-h 全绿 | 验证记录 §3 RED-A、§4 绿态（含 T1-d 调用数=2） |
| T2 auth 竞争仅验证 + 竞态测试 | 完成 | 生产代码零改动；新增竞态回归；查漏有函数级证据 | 验证记录 §5（含 §3 死锁陷阱复验） |
| T3 usage 缺失哨兵 | 完成 | T3-a/b/c | 验证记录 §3 RED-B（`tok=0` 行为红）+ §4 |
| T4 文档同步 | 完成 | `patches/README.md` 表行 + 移除条件 + 验证记录 + 本进度文件 | C3 提交 |

## 2. 交付物清单

```text
C1  0466671 security: backport WAF IP-level fail-fast breaker and usage sentinel
    patches/0006-waf-ip-failfast-and-usage-sentinel.patch  (121 行，只碰 client.go + handler.go)
    patches/series                                          (+1 行，0001–0005 顺序未动)
    extensions/internal/server/wafip.go                     (77 行，wafIPGate 状态机)
    extensions/internal/server/wafip_test.go                (203 行)
    extensions/internal/server/logging_usage_sentinel_test.go (44 行)
    extensions/internal/upstream/credential_race_test.go    (93 行)
    extensions/internal/upstream/wafip_classify_test.go     (61 行)

C2  6e5a18a docs(backport): 任务书三份规划文档入仓，登记本轮实测修订
    （§12.3 sed 配方修正、§12.3b 改动后实测与新坑 A/B、spec 324→360 与 Q5 注记）
C3  7c94f61 docs(backport): 补 0006 补丁说明、T0 复析、验证记录与进度文件
C3+ （本次）交付尖点复跑记录：同一配方在 7c94f61 全绿，identity 不变
```

`upstream/`、`upstream.lock` 零改动；既有 0001–0005 语义未动。

## 3. 与任务书的差异（C2 的实质内容，供验收方直接判）

| # | 差异 | 影响 |
| --- | --- | --- |
| Δ1 | §7.1 的可选方案落地为**合并补丁**：T3 并入 0006，文件名 `0006-waf-ip-failfast-and-usage-sentinel.patch` | `series` 6 行而非 7 行；README 表已写清 |
| Δ2 | §3 的"14 个出站函数开头 `a = a.Snapshot()`"按物化源码更正为**实际插入点**（`ChatStreamContext`/`billingJSONContext`/`userResourceDetailed`/`globalModelsOnce`/`growthJSON` …），公开 wrapper 是委托；`RefreshToken` 用 `snapshot := a.Snapshot()` | 照旧名单 grep 会落空（分析文档 D1） |
| Δ3 | 两个函数确实无自身快照（`ReportChatActivity` 读 `UID`、`ClaimTrialContext` 读 `Realm()`），复核为**安全**而非缺口 | 验收时别按"漏一个 Snapshot"判（分析文档 D2） |
| Δ4 | §12.3 容器配方的 `sed` 不可照抄：busybox 下必须 `s#https\?://…#…#g`（`|` 分隔报 `unmatched '|'`，漏 `\?` 则静默不匹配） | 新会话照抄会以为"镜像源已换"（D3） |
| Δ5 | `overlay.py identity` 在 Windows 上有三个值，只有 Linux clone 路径可引用；本轮权威值 `4f6b50a4…`（对照组：摘掉 0006 = `faba20a3…`；基线 5 补丁 = `d8c08ffe…`） | 拿宿主值比对必判错（D4） |
| Δ6 | 本机 `python3` 是 Microsoft Store 占位符，真解释器是 `python`（3.12.10） | §8.3 归因需按 Windows 平台语义补全（WinError 1314 / 无 `os.mkfifo` / temp ACL / 无 bash），非仅"占位符" |
| Δ7 | T2 **没有红态阶段**（设计如此）：竞争面已由补丁 0001/0002 关闭，只能证明"关掉后不回归 + 对抗测试在场" | 别按 T1/T3 的"红→绿"格式验收 T2 |
| Δ8 | §6.1 M3b 仍为不做，替代方案与边界已按用户裁定登记，等 pi 定论 | 未实现，不得记为已覆盖 |
| Δ9 | 执行顺序偏离：任务书要求 T0 先于一切代码改动；实际因会话压缩与 T1/T3 优先排期，T0 落笔在实现之后 | T0 独立性打折，已在分析文档 §0 声明（不隐去） |

## 4. 验收方复跑入口

```bash
# 基线（HEAD 未含 0006 时应为 faba20a3/d8c08ffe 族，绿）与本轮：容器内 clone + prepare
docker run --rm -v <本仓>:/host:ro -v wb2a-gomod:/go/pkg/mod -w /work \
  -e GOPROXY=https://goproxy.cn,direct docker.m.daocloud.io/library/golang:1.23-alpine \
  sh -c 'sed -i "s#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g" /etc/apk/repositories \
    && apk add -q git python3 bash gcc musl-dev && git config --global --add safe.directory "*" \
    && git clone -q --no-hardlinks /host /work/repo && cd /work/repo \
    && python3 scripts/overlay.py prepare --output /work/core \
    && cd /work/core && go build ./... && go vet ./... && go test ./... \
    && CGO_ENABLED=1 go test -race -count=1 ./internal/auth ./internal/pool ./internal/upstream ./internal/server ./internal/scheduler'
# 红态复现：把 patches/series 的 0006 行删掉再 prepare（编译期红）；
# 再把 extensions/internal/server/wafip*.go 与 logging_usage_sentinel_test.go 之外的那份 T3 测试留下（行为红）。
```

宿主侧仍需单独跑：`node --test console/web_test.cjs`（30/30）、
`docker compose --env-file /dev/null -f docker-compose.yml config --quiet`（rc=0）、`git diff --check`（干净）。

## 5. 遗留与后续（不在本轮范围）

1. §12.5 登记不修的四项（update 传干净 env、回退无文档、任务无超时、`task_events.py` 白名单口径）另立工单。
2. A4 表里 8 条"未核验"的上游安全提交：下次回移前逐条比对 diff。
3. M3b 若将来要做，需要独立设计（不是 `/status` 已解决）。
4. 推送 / 发布镜像 / 部署 / 重启：各需单独授权，本轮一律未做。
