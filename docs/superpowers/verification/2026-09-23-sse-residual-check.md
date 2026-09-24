# W2 核验：`7fad570` 对 `internal/upstream/sse.go` 的残留项

- 日期：2026-09-23
- 任务来源：`docs/superpowers/plans/2026-09-23-trae-task-ticket.md` §3
- 性质：**只读分析**，未改任何生产代码（本目录新增一份文档除外）

## 1. 上游 `7fad570` 对 `sse.go` 的改动（逐 hunk）

`7fad570` 是 merge commit（`Merge: 64064ce b1f7bc8`），用 `--first-parent` 取净改动。

**实际命令**：

```powershell
git -C D:\work\02toolsMy\workbuddy2api_v0 show --first-parent 7fad570 -- internal/upstream/sse.go
```

**实际输出（完整）**：

```text
commit 7fad570b733b4aee52752b9b51b021a3709f9a5a
Merge: 64064ce b1f7bc8
    [Audit] 竞态/死代码/泄漏/逻辑漏洞系统扫描与修复(#134)

 internal/upstream/sse.go | 5 ++++-
 1 file changed, 4 insertions(+), 1 deletion(-)

@@ -98,10 +98,13 @@ func Aggregate(r io.Reader) (map[string]any, error) {
-                                                       // 有的上游把完整消息放在 message 里（非 delta）
+                                                       // 有的上游把完整消息放在 message 里（非 delta）。守卫 once 语义：
+                                                       // 采过一次即 latch gotAnyContent，否则「每帧都带完整 message」
+                                                       // 的上游会让正文被逐帧重复追加（N 帧 → N 遍）。
                                                        if msg, ok := c["message"].(map[string]any); ok && !gotAnyContent {
                                                                if txt, ok := msg["content"].(string); ok {
                                                                        content.WriteString(txt)
+                                                                       gotAnyContent = true
                                                                }
                                                        }
```

**hunk 摘要（共 1 个 hunk）**：`Aggregate()` 的非流式 message 分支——上游若"每帧都带完整
`message.content`"（非 delta 形态），`gotAnyContent` 只在 delta.content 路径被置位，
message 路径采完后不 latch，导致下一帧仍满足 `!gotAnyContent`，正文被逐帧重复追加（N 帧 → N 遍）。
修复为写入一次后置 `gotAnyContent = true`（once 语义）。

## 2. 本仓现状

文件：`upstream/internal/upstream/sse.go`（快照基点 `c576b48`，只读）。

**实际命令**：

```powershell
Select-String -Path upstream\internal\upstream\sse.go -Pattern 'gotAnyContent'
```

**实际输出**：

```text
LineNumber Line
---------- ----
        28                      gotAnyContent bool
        76                                      gotAnyContent = true
       102                              if msg, ok := c["message"].(map[string]any); ok && !gotAnyContent {
```

对照 `upstream/internal/upstream/sse.go` 第 101–106 行：

```go
// 有的上游把完整消息放在 message 里（非 delta）
if msg, ok := c["message"].(map[string]any); ok && !gotAnyContent {
    if txt, ok := msg["content"].(string); ok {
        content.WriteString(txt)
    }
}
```

message 分支内**没有** `gotAnyContent = true`——与上游修复前形态逐字一致，同一缺陷原样存在。

补丁覆盖情况：

```powershell
Select-String -Path patches\*.patch -Pattern 'sse\.go'
# 实际输出：（空，无任何补丁触及 sse.go）
```

`patches/series` 现有 0001–0007 均不改 `internal/upstream/sse.go`，物化后该文件仍保留此缺陷。

## 3. 判定

**未覆盖 · 建议回移**（单行生产改动 + 注释）。

理由：
- 触发条件是上游以"每帧完整 message"形态返回（`Aggregate` 的非 delta 兜底分支），
  该形态在快照注释里被明确承认存在（"有的上游把完整消息放在 message 里"），非假想路径；
- 后果是聚合响应正文重复 N 遍，属正确性缺陷，且修复仅 1 行、语义保守（once latch 不影响
  delta 路径与只出现一次 message 的正常流）；
- 补丁可独立新增（如 `0008-sse-aggregate-message-once.patch`），与本仓任何既有补丁无文件交集。

本轮任务书 W2 授权范围是**只读核验**，回移实施未授权，故仅登记结论，不动代码。

## 4. 与 M3a（usage 哨兵）的交互

- M3a 落点在 `internal/server/handler.go`（补丁 0006 的 `st.toks, hasUsage := stats.Tokens()`
  哨兵改造）与 `sse.go` 的 **`Stream()`** 无关——`Stream()` 是逐帧透传路径，
  usage 由 `stats` 读取；本 hunk 在 **`Aggregate()`**，是非流式聚合路径。
- 两条路径无共享状态、无相邻改动；上游 `7fad570` 在 `sse.go` 仅此 1 个 hunk，
  不涉及 usage/帧规范化逻辑。
- 补丁 0006/0007 均不触及 `sse.go`（见 §2 检索为空），未来回移不会与既有补丁产生上下文耦合。

**结论：与 M3a 无重叠、无冲突。**

## 5. 证据链汇总

| 证据 | 命令 | 结果 |
| --- | --- | --- |
| 上游净改动 | `git -C D:\work\02toolsMy\workbuddy2api_v0 show --first-parent 7fad570 -- internal/upstream/sse.go` | 1 hunk：`Aggregate` message 分支补 `gotAnyContent = true`（4+/1-，其中 3 行为注释） |
| 本仓现状 | `Select-String upstream\internal\upstream\sse.go -Pattern 'gotAnyContent'` | 仅 28/76/102 三处，message 分支无 latch |
| 补丁交集 | `Select-String patches\*.patch -Pattern 'sse\.go'` | 空 |
| 生产代码改动 | `git status --short`（写本文档前） | 无 sse.go / client.go 等生产文件改动 |
