# L2 核验任务单：A4 表未核验的 11 个上游安全提交

- **编号**：TASK-2026-09-22-L2
- **来源**：`docs/superpowers/plans/2026-09-22-backport-shift-handoff.md` §3.1 的 **L2**
- **上溯**：`docs/superpowers/verification/2026-09-22-v0-vs-me-analysis.md` §1.A4（该表把以下提交标为"未核验"）
- **目的**：逐条判定「本仓快照是否已覆盖该修复」，产出「该回移 / 不需回移」的可追溯结论
- **状态**：**需求方已完成前置分级（见 §2），实施方只需做绿格部分**

---

## 1. 边界约束（不可越界）

- **只读分析**：本任务**不改任何生产代码、不写补丁**。产出物是**结论文档**，不是代码。
- 三个 clone 都在本地，**离线**完成，不要 `git fetch`（GitHub 不可达）。
- 不修改 `upstream/`、`upstream.lock`。
- 不推送、不发布镜像、不部署。

> **为什么只读**：L2 的目的是**判断**，不是修复。判完再由需求方决定哪些进下一轮回移
> （那会是新的、独立授权的任务）。**不要**顺手写补丁——会让"判断"与"实现"耦合，
> 失去独立复核的价值。

---

## 2. 需求方前置分级（实施方直接用，不要重做）

已用「该提交触及的非测试文件是否存在本仓快照」做机械分级：

| # | commit | 日期 | 主题 | 触及快照内文件 | 分级 | 原因 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `7fad570` | 09-17 | [Audit] 竞态/死代码/泄漏/逻辑漏洞系统扫描 | 部分（**merge**，15 文件） | **需核验** | 见 §3.1 拆分 |
| 2 | `af51945` | 09-16 | session 粘性路由 GC goroutine 关停竞态与泄漏 | 是 | **合并** | 它是 `2b8dba0` 的 merge，只核 `2b8dba0` 即可 |
| 3 | `2b8dba0` | 09-16 | 同上（实体提交） | `internal/session/session.go` | **需核验** | — |
| 4 | `5f10a9c` | 09-16 | redisstore Close 排空竞态 | `internal/redisstore/redisstore.go` | **需核验** | — |
| 5 | `855e5b9` | 09-17 | scheduler 守卫锁外直读 RefreshToken | `auth.go` `scheduler.go` `travel.go` | **预计已覆盖** | 补丁 0002 已把 scheduler 四处凭据读改 `a.Snapshot().X`；**需确认等价** |
| 6 | `a767465` | 09-18 | session content 内容签名 | `handler.go` `session/ids.go` `session/session.go` | **需核验** | 字段名疑拼写错误（`content 内容签名`）；本仓 handler.go 已被补丁改过 |
| 7 | `8058019` | 09-18 | 粘性键补 `prompt_cache_key` + 首条 user 兜底 | `handler.go` `session/session.go` | **需核验** | — |
| 8 | `10eefa8` | 09-18 | 兜底键抑制带 user_id 的请求 | `session/session.go` | **需核验** | — |
| 9 | `231a076` | 09-17 | sanitize 指纹字面量字节快照护栏 | **仅 `_test.go`** | **不需核验** | 纯测试护栏，不涉生产行为 |
| 10 | `145220d` | 09-16 | Classify 429 前移 | `internal/upstream/client.go` | **需核验** | 与补丁 0006 同文件，可能交互 |
| 11 | `4ac68b7` | 09-18 | global chat 固定走 `/v2` 绕 WAF | `internal/upstream/client.go` | **需核验** | 与 T1 的 WAF 止损**相邻但机制不同**（路由策略 vs 请求量止损） |

**请求方已完成的机械结论**（可直接引用）：

- 快照内**不存在**的文件：`internal/upstream/modelsdev.go`、`internal/upstream/growth_bonus.go`
  → `7fad570` 涉及这两个文件的部分**天然不适用**。
- `af51945` 与 `2b8dba0` 内容相同（前者是后者的 merge），**只做一次**。

---

## 3. 逐条核验方法（统一口径）

对每条「需核验」提交，按以下三步：

### 第 1 步：该修复针对什么问题

```bash
git -C <v0> log -1 <commit>            # 读提交信息全文（这些提交的信息写得很详细，含根因）
git -C <v0> show <commit> -- <file>    # 读实际 diff
```

### 第 2 步：本仓快照当前是什么状态

在**物化树**里查（不是 `upstream/` 原样——补丁会改它）：

```bash
# 物化（容器内，宿主的 prepare 会因 digest 失败）
git clone /host /work/repo && cd /work/repo && python3 scripts/overlay.py prepare --output /work/core
```

然后再看 `/work/core/<file>` 的对应位置。

### 第 3 步：判定并给证据

四选一，**必须附行号或命令输出**：

| 判定 | 含义 |
| --- | --- |
| **已覆盖** | 本仓（快照或补丁）已有等价行为 → 给等价代码位置 |
| **不适用** | 涉及的文件/机制在本仓不存在 → 给"文件不存在"证据 |
| **未覆盖·建议回移** | 缺口真实存在且影响部署安全面 → 说明攻击面与代价 |
| **未覆盖·不建议回移** | 缺口存在但收益低/成本高 → 说明理由 |

---

## 4. 逐条验收条件

| 编号 | 条件 |
| --- | --- |
| L2-a | 11 个提交**全部**有判定，无遗漏（可合并的注明合并理由） |
| L2-b | 每条判定附**实际命令 + 实际输出**，不接受"应该/大概" |
| L2-c | 「已覆盖」项必须指出本仓的**等价代码位置**（文件:行） |
| L2-d | 「未覆盖」项必须说明**攻击面**（谁能利用、在什么条件下）与回移代价估计 |
| L2-e | 与 T1/T3 已回移内容的**交互**要写明（尤其 `145220d`/`4ac68b7` 与补丁 0006 同文件） |
| L2-f | 明确列出**无法判定**的项及原因（不得隐去） |
| L2-g | **未改任何生产代码**（`git status` 应为干净，或仅有本文档） |

---

## 5. 交付物

`docs/superpowers/verification/2026-09-22-l2-security-commit-triage.md`

固定结构：

```text
1. 方法与环境声明（物化路径、容器/工具版本；离线声明）
2. 前置分级复核（对 §2 表格的确认或异议——若有异议必须给证据）
3. 逐条判定（11 条，每条：提交摘要 / 根因 / 本仓现状 / 判定 / 证据命令与输出）
4. 汇总表（commit | 判定 | 影响面 | 建议）
5. 与已回移内容（0006 的 WAF、M3a 的哨兵）的交互说明
6. 无法判定项
7. 未做事项（如实列出）
```

---

## 6. 需求方将如何验收

1. **独立复跑**抽查：对「已覆盖」与「建议回移」两类各抽 2 条，自己跑命令核对。
2. **对抗**：重点质疑"已覆盖"的判定——这类结论最容易草率。
   尤其 `855e5b9`（我预期"已覆盖"）与 `2b8dba0`（GC 竞态），我会自己看代码确认。
3. 检查是否有"未改代码"以外的越界（`git status`）。
4. 逐条判 L2-a…L2-g。

---

## 附录：核验用命令速查

```bash
# 提交信息与 diff（v0 副本）
git -C D:/work/02toolsMy/workbuddy2api_v0 log -1 <commit>
git -C D:/work/02toolsMy/workbuddy2api_v0 show <commit>
git -C D:/work/02toolsMy/workbuddy2api_v0 show <commit> --stat

# 该提交触及的文件是否存在于快照
git -C D:/work/02toolsMy/workbuddy2api_v0 cat-file -e c576b48:<path>

# 本仓是否已有某符号（在物化树里查，补丁会改）
grep -rn "<symbol>" /work/core/internal/

# 容器物化（宿主 prepare 会因 digest 失败）
docker run --rm -v "<本仓>:/host:ro" -v wb2a-gomod:/go/pkg/mod -w /work \
  -e GOPROXY=https://goproxy.cn,direct docker.m.daocloud.io/library/golang:1.23-alpine \
  sh -c 'sed -i "s#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g" /etc/apk/repositories \
    && apk add -q git python3 && git clone -q /host /work/repo && cd /work/repo \
    && python3 scripts/overlay.py prepare --output /work/core'
```
