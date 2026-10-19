# 交接班文档：请求内重试+冷却准入批次收尾（2026-10-19 收工 → 次日续接）

- **文档编号**：SHIFT-2026-10-19-RETRY-CIRCUIT
- **撰写时间**：2026-10-19
- **续接对象**：次日（新会话）的自己 / 任何接手者
- **一句话状态**：**「合并批次」（请求内重试 + 冷却准入升级）已全部落地并三层验证，生产与 lab 双环境均已发版且全通道在线，工作区干净、无未提交改动。无阻塞事项，等待用户下一个指令（后续增量见 §4）。**

> **给新会话的自己**：先读 §1（盘上事实）与 §3（双环境现状），不要重新实施本批次——
> §2 已把改动钉死，细节看验收记录。lab core 容器跑的是无标签旧镜像（§3.3），不影响运行，别当故障处理。

---

## 1. 现在停在哪（盘上事实，已核对）

### 1.1 分支与远端

```text
## main...origin/main [ahead 104]
```

- 领先 `origin/main` **104 个提交**，从未推送。推送需单独授权（AGENTS.md）。
- 工作区 `git status --short` 为**空**（干净，无未提交改动）。

### 1.2 本轮相关提交（已提交、未推送）

```text
6a8fa53 docs(verification): 请求内重试与冷却准入验收记录，设计文档状态更新
b8e3f5a feat(gateway): 请求内重试（首字节前换跳）+ 冷却准入与 Retry-After 感知
dbdfb98 feat(health): M2 批B 别名健康视图与Top榜及时延429展示
7c40aa7 docs(verification): M2 批A2 429 计数+时延分位验收通过（2f20756 一次通过）
```

- `b8e3f5a` = 合并批次主体（console/retry.go、circuit.go、route.go、probe.go、errors.go、proxy.go、zcode.go + retry_test.go 8 个回归测试）；提交时连带带入了此前已 staged 的 lab 方案A基建文件（AGENTS.md、compose.lab.yml、local-release.sh --tag、设计文档、M2批B验收记录）——已在当时向用户披露。

### 1.3 容器现状（实测）

```text
生产（project: workbuddy2api-ui-me，宿主端口 7863）—— 10 只全 Up 22min：
  console: workbuddy2api-console:local   f6248249ee5e（本批次新）
  core:    workbuddy2api-core:local      807e736bc75b（5 天前，本批次未改 core，正常）
  旁路 8 只: zcode / qoder / opencode / autoclaw / trae / traeai / raccoon / freellmapi(healthy)

lab（project: prism-lab，宿主端口 7864）—— 2 只：
  prism-lab-console-1: workbuddy2api-console:lab  97fc6a785bb5（本批次新）
  prism-lab-core-1:    运行镜像 ID 5c61e08d2cd6（见 §3.3 说明）
```

### 1.4 镜像标签清单（与回退有关的部分）

```text
workbuddy2api-console:local       f6248249ee5e   29 分钟前
workbuddy2api-console:local-prev  ff686f1bff51   2 天前     ← console 回退备胎
workbuddy2api-core:local          807e736bc75b   5 天前
workbuddy2api-core:local-prev     a214e074daa4   5 天前
workbuddy2api-console:lab         97fc6a785bb5   29 分钟前
workbuddy2api-core:lab            1ddb0a7f92e5   5 天前
```

---

## 2. 本轮完成了什么（合并批次，已闭环）

依据设计文档 §七/§八/§九 9.2/9.3（`docs/superpowers/specs/2026-10-18-生产运维规范与容错切换方案-v1.0.md`）：

1. **请求内重试**（`console/retry.go` 新建）：别名候选链 ≥2 跳时，在首字节前逐跳重试——429/5xx/超时可换跳（响应头阶段暂扣 + `retryWriter`），4xx 原样透传，任何 body 写出后绝不回退；全链失败兜底透传最后一跳真实响应。切链留痕：`X-Prism-Attempts`（仅多跳下发）、`X-Prism-Reason`（「…，重试切第 N 跳」）、失败跳落错误账本（含 RetryAfterSec）。
2. **冷却准入唯一判据 `cooldownCategory`**（`console/probe.go` + `console/circuit.go`）：`rate_limit`/`upstream_error`/`timeout` 可冷却可重试；`auth`/`model_error`/`error`/`pin` 只告警不熔断不切换。pin 排除被该判据自然覆盖。
3. **Retry-After 感知冷却**：`circuitState.CooldownMs = min(30min, Retry-After)`（`parseRetryAfter` 支持 seconds/HTTP-date，封顶 24h）；旧状态无该字段回落 30min 固定窗口。**固定窗口起步，未做指数冷却**（拍板明确不做，见 §4；——2026-10-19 勘误：此描述已被同日 m2c 批 6179560 有意演进覆盖，现为阶梯退避 30s→2m→10m→30m + 半开试探收紧，Retry-After 头值仍优先且封顶 30min）。
4. **候选链重构**（`console/route.go`）：`resolveEntryChain` 返回完整链 + 原链下标；单跳别名 / auto / 非别名请求走原路径零变化。
5. **验证**：go vet 零输出 → go test 全绿 3.9s → go test -race 全绿 6.2s（本机无 Go，Docker golang:1.23 内执行）；lab 真容器验证通过；生产发版后 healthz/livez 双 200、`channel-probe` 全通道在线（合并模型 total=42）。

**完整验收记录**：`docs/superpowers/verification/2026-10-19-请求内重试与冷却准入验收.md`（含改动文件清单、测试证据、lab 验证、如实说明）。
**已批准计划**：`.trae/documents/2026-10-19-请求内重试与冷却准入批次.md`（已全部执行完毕）。

---

## 3. 双环境现状与密钥归属

### 3.1 生产 7863

- 入口不变：`docker compose up -d`（本机由不入库的 `docker-compose.override.yml` 钉 `:local` 标签）。
- 数据/密钥：`runtime/wb2api/`；密钥三把（admin/api/bridge）在 `runtime/wb2api/keys/keys.json`。
- 回退方法：`docker tag workbuddy2api-console:local-prev workbuddy2api-console:local` → `docker compose up -d`（本批次只改 console，core 无需动）。

### 3.2 lab 演练环境 7864（prism-lab）

- 启停：`docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml up -d / down`；**`-p prism-lab` 不能省**。
- 隔离四件套：独立 project / 端口 7864 / 数据 `runtime/lab/` / 镜像 `:lab` + `pull_policy: never`；默认只含 core+console，验证某通道时临时叠加该通道 overlay。
- `:lab` 标签产出：`bash scripts/local-release.sh --allow-dirty --tag lab`（不滚动 `:local`、不重启生产）。

### 3.3 已知现象：lab core 运行旧镜像（不是故障）

`docker ps` 显示 `prism-lab-core-1` 运行镜像 ID `5c61e08d2cd6`，该 ID 已不在本地标签清单中（当前 `core:lab` 标签指向 `1ddb0a7f92e5`）。容器照常服务；下次对 lab 执行 `up -d`（或重建）时会自然对齐到 `core:lab` 当前标签。无需处理，记录在此防误判。

### 3.4 密钥归属（两套完全独立）

- lab 三把密钥（admin/api/bridge）与生产**字段级全部不同**，各 43 字符，独立自动生成于 `runtime/lab/keys/keys.json`（明文在该文件；console 启动日志也会打印管理密钥一行：`docker compose -p prism-lab logs console | Select-String "管理密钥"`）。
- 修改方式（已向用户交付）：**删 `runtime/lab/keys/keys.json` 重启重新生成**，或建不入库 override（如 `runtime/lab/compose.keys.yml`）用 `WB2A_ADMIN_KEY`/`WB2A_API_KEY` 指定固定值——两服务同名覆盖必须一致、管理密钥 ≥32 字节且 ≠ API Key、bridge 不接受 env 指定。取消覆盖即恢复基础文件。

---

## 4. 待办与后续增量（均需用户拍板，勿自行开工）

1. **本批次拍板明确不做的三个增量**（设计文档在案）：
   - 指数冷却 + 半开探测（到期前 30s 轻量 probe，Envoy 式 base×次数封顶）；——已由同日 m2c 批 6179560 实现（阶梯退避 + 半开试探收紧），本条待办完成
   - 独立会话亲和表；
   - gateway-auto 链上重试。
2. **在册未修项**：`deploy/compose.trae.yml` 注入的是 `WB2A_TRAE_KEY`，而 trae2api 二进制读 `TRAE_API_KEY`，该通道 sidecar 实际处于免鉴权状态（回环内不致命，AGENTS.md 已在册）。
3. **Git 未推送**：main ahead 104，推送需用户单独授权。
4. 其余长期任务体系（新增通道 / 单通道排错 / 健康检查 / 实用性 / 统计）照旧，改生产码前先读设计文档 §二保活铁律。

---

## 5. 操作速查（接手者常用）

```bash
# 全通道体检（一次读 8 旁路 + core）
bash scripts/channel-probe.sh

# 生产健康
curl -s http://127.0.0.1:7863/healthz ; curl -s http://127.0.0.1:7863/livez
# lab 健康（7864）

# lab 启停（-p 不能省）
docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml up -d
docker compose -p prism-lab -f docker-compose.yml -f deploy/compose.lab.yml down

# 生产发版（会滚动 :local/:local-prev 并连带重建全部旁路）
bash scripts/local-release.sh            # 脏工作区默认拒绝

# 用量健康度观察
python scripts/review-usage.py --since 72h
```

**验证命令（本机无 Go，Docker 内跑）**：vet/test 用 `golang:1.23-alpine` 挂 console 目录 + `wb2a-gomod` 卷；race 用 Debian 版 golang:1.23。
