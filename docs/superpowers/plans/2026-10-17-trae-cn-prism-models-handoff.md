# 交接文档：把棱镜网关模型并入 TRAE CN 自定义模型列表

面向**接手执行的另一个 agent**。任务规格（逐条字段、硬约束、依据）在 `trae-cn-prism-models-prompt.md`；
本文档补充的是：**为什么要换执行通道、通道怎么打开、现场实测状态、已存在条目核对、对账与回报**。
写于 2026-10-17（现场数据取自 2026-10-16 夜）。

---

## 1. 任务

在 TRAE CN 的「设置 → 模型 → 添加模型」表单里**逐条**添加棱镜网关（`http://localhost:7863`）的模型，
每条保存成功后做下一条。目标是让 16 条全量在列表里齐备。

## 2. 谁可以执行（前置条件）

- 执行者必须是**独立于 Trae CN 的进程**：Claude Code CLI、Codex CLI、另一个终端里的 agent 都行。
- **不能是跑在 Trae CN 里的会话**（比如 Trae CN 自带的 agent）：第 4 节要先杀掉 Trae CN，跑在里面的会话会把自己一起杀掉。
- 如果只能在 Trae CN 内运行 → 先让用户手动执行第 4 节的重启命令，再从**新会话**接手。

## 3. 现场实测事实（决定通道选择）

1. 网关可用：`http://localhost:7863/healthz` → 200。密钥在
   `D:\work\02toolsMy\workbuddy2api-ui-me\runtime\wb2api\keys\keys.json` 的 `api_key`（**不得打印**）。
2. **Trae CN 渲染进程没有开 CDP**（进程命令行里没有 `--remote-debugging-port`）→
   任何 agent 都无法驱动它的设置页 DOM。`trae-cn-prism-models-prompt.md` 里
   "TRAE 的 agent 能点自己的设置页"这个前提**不成立**，必须换通道。
3. 唯一开着的调试口是**扩展宿主 Node inspector**：`http://127.0.0.1:1949/json/version` 返回
   `node.js/v22.21.1`，`/json/list` 只有一个 `type=node` target（`electron/js2c/utility_init`）。
   **不是渲染进程**，驱动不了 UI，别在上面浪费时间。
4. 主进程另有端口 17788：`/`、`/api`、`/mcp`、`/health`、`/tools`、`/json` 全部 404，无可用接口。
5. 自定义模型列表是**云端下发**的：打开「添加模型」弹窗会打 `providers`、
   `get_custom_model_type_config` 两个云端 RPC（见 `%APPDATA%\Trae CN\logs\20261016T190927\window2\renderer.log`
   的 `TransportManager executeRequest` 行）。2026-10-10 直接写库塞进去的 16 条
   （`custom_model_id` `7100000001~7100000016`）至今只躺在库里、UI 一条都不显示 →
   所以**禁止走本地库**，只能走表单。

## 4. 打开执行通道（唯一可行路径）

把 Trae CN 以带 CDP 的方式重启，在 **Trae CN 之外的 PowerShell 窗口**里执行：

```powershell
# 先保存其它窗口的编辑内容（热退出一般能恢复未保存缓冲，但别赌）
$exe = (Get-Process -Name 'Trae CN' | Where-Object Path | Select-Object -First 1).Path
Get-Process -Name 'Trae CN' | Stop-Process -Force
Start-Sleep 3
Start-Process -FilePath $exe -ArgumentList '--remote-debugging-port=9222','--remote-allow-origins=*'
# 轮询确认通道已开
for ($i=1; $i -le 45; $i++) {
  Start-Sleep 2
  try { (Invoke-WebRequest http://127.0.0.1:9222/json/version -UseBasicParsing -TimeoutSec 3).Content; break } catch {}
}
```

- **现成脚本**（更稳，内含"检测到自己跑在 Trae 内就拒绝执行"的父进程链守卫）：
  `.build/trae-cn-models/restart-trae-cn-with-cdp.ps1`
- 判定：打印出 `{"Browser":"Chrome/..."}` 才算通道打开；90s 内没起来 = 这个构建忽略该参数，
  转而退回"人工点 + 对账"（备选路径）。
- 找不到 exe 路径时兜底：搜 `D:\`、`%LOCALAPPDATA%\Programs`、`%ProgramFiles%` 深度 3。
- **收尾时把 Trae CN 正常重启一次**（不带调试口），别让它长期停在调试状态。

## 5. 驱动设置页

1. 先列 target，确认渲染进程在：
   `(Invoke-WebRequest http://127.0.0.1:9222/json/list -UseBasicParsing).Content`
   → 找 `type=page` 的条目（`title`/`url` 形如 workbench / `trae-file://…`）。
2. 连接方式（按可用性择优）：
   - 该 agent 若装有 **`electron` skill**（agent-browser 经 CDP 驱动 VS Code 类 Electron 应用）→ 优先用它。
   - 兜底用 Python Playwright（本机已验证可用：解释器 `D:\gogogo\python\python.exe`，`import playwright` 通过）：

```python
from playwright.sync_api import sync_playwright
with sync_playwright() as p:
    br = p.chromium.connect_over_cdp("http://127.0.0.1:9222")
    pages = [pg for c in br.contexts for pg in c.pages]
    for pg in pages:
        print(pg.url, "|", (pg.title() or "")[:60])
    # 选中承载设置页的那个 page，再往下做定位与点击
```

> 上面只是连接骨架；具体选择器要在通道打开后按真实 DOM 现场调，别照抄臆造的选择器。
3. 操作路径：「设置 → 模型 → 添加模型」（`Ctrl+,` 可直接开设置）。
4. 每条：填字段 → 选 API 格式 → 点「添加」→ 等连通性测试（会真实打网关，慢的条目 8s 上下）→
   保存成功后再下一条。**不要打开「完整 URL」开关**。
5. 若设置页是 webview 而不是 workbench DOM，Playwright 的 target 列表里会单独出现该 webview，
   连它再做同样的操作。

## 6. 每条固定字段

| 字段 | 值 |
| --- | --- |
| API 格式 | OpenAI Chat Completions |
| 自定义请求地址 | `http://localhost:7863/v1`（不打开「完整 URL」开关） |
| API 密钥 | 读 `runtime/wb2api/keys/keys.json` 的 `api_key`，**不要打印到任何输出/日志/回复** |
| 模型 ID | 照抄第 7 节清单，逐字，不加前后缀 |
| 模型展示名称 | 留空 |
| 高级配置（上下文窗口 / 最大输出 / 温度等） | 全部留空 |

参考：本地缓存里已存在的条目显示 provider = `custom_openai_compatible`、
`base_url` = `http://localhost:7863/v1/chat/completions`，且**没有**上下文/输出长度字段 ——
与"字段照上表填、高级配置留空"一致。

**为什么高级配置必须留空**：网关只对 2 条模型上报权威额度
（`global:deepseek-v4.1-flash`=131072、`autoclaw-cn:glm-5.3-flash`=1000000），
其余在 `/v1/models` 里是 virtual 条目、不带 `context_length`。OpenRouter 目录里的
1048576/1000000 是官方纸面值，不是免费通道实际给的额度；填大了 TRAE 就不压缩上下文，
真打到上游被截断时报错点离原因更远。

## 7. 清单：**先核对，再添加**

⚠️ **本节与 `trae-cn-prism-models-prompt.md` 有一处出入，以此处为准。**
提示词文件假设"只有 `2-fast-v1`/`2-fast-v2` 已添加"，但本地缓存（`state.vscdb`，2026-10-16 夜只读读取）
显示**云 id 条目已有 6 条**（另有一份 scope 只有 2 条）：

| 已存在（scope `2021341456505097`，云 id） | 是否在本次要求添加的清单里 |
| --- | --- |
| `2-fast-v1`（2900616322） | 否，本来就跳过 |
| `2-fast-v2`（2900163458） | 否，本来就跳过 |
| `5-free-v1`（2897981826） | **是 —— 已在列表里，不要重复添加** |
| `qoder-qwen3.8-flash`（2901805954） | **是 —— 已在列表里，不要重复添加** |
| `global:deepseek-v4.1-flash`（2824966786） | **是 —— 已在列表里，不要重复添加** |
| `cn:deepseek-v4.1-flash`（2843806210） | 不在清单内（早期加的，别动） |

另一份 scope（`4245681982938427`）只有 `2-fast-v1`、`2-fast-v2`。
**以打开设置页后实际显示的列表为准**，已在列表里的条目跳过，不要重复添加；有疑问就停下来问用户。

剔除已存在的 3 条后，**实际待添加 11 条**：

```
1-daily-v1
1-daily-v2
3-strong-v1
3-strong-v2
4-max-v1
4-max-v2
5-free-v2
6-try-v1
6-try-v2
gateway-auto
autoclaw-cn:glm-5.3-flash
```

（若设置页里没看到 `5-free-v1`/`qoder-qwen3.8-flash`/`global:deepseek-v4.1-flash`，
说明缓存与实际不同步，按实际显示为准照原 14 条清单添加，并在回报里说明。）

## 8. 预期结果（2026-10-16 夜复检：真实 `/v1/chat/completions`，`max_tokens=8`）

| 结果 | 条目 |
| --- | --- |
| 预期通过（13 条） | 除 `6-try-v2` 外全部 |
| **预期必然失败** | `6-try-v2` → HTTP 400 `model_not_found`（`Select an available free model from /v1/models`）。保存会被拒 —— 照硬约束记「未入库 + 报错原文」，不要改地址/名字/格式 |
| 可能被判失败 | `1-daily-v1`、`1-daily-v2`、`global:deepseek-v4.1-flash`：8 token 下 `content` 为空（思考型模型把额度全花在推理上），给到 128 token 就正常。若 TRAE 连通性测试额度极小，可能判失败 → 如实回报，不硬填 |
| 可能超时 | `4-max-v1`（8.3s）、`4-max-v2`（7.8s）、`autoclaw-cn:glm-5.3-flash`（8.8s）、`qoder-qwen3.8-flash`（8.4s）；同一条最多重试 1 次 |
| 数据修正 | `3-strong-v1` 在提示词文件里记为 502（更早一次实测，上游 code 1005 超配额），本次复检已恢复 200 —— 按 200 处理 |

## 9. 对账（跑完必须做，只读，不写库）

库：`%APPDATA%\Trae CN\User\globalStorage\state.vscdb`

结构（本次实测判定规则）：
- 键名里含 `model_list_map` 的条目里才有自定义模型；
- **冒号键**（`…5097:AI.agent.model.model_list_map`）装的是 2026-10-10 伪造遗留（160 条 `7100000*`），UI 不显示；
- **下划线键**（`…5097_AI.agent.model.model_list_map`、`…8427_AI.agent.model.model_list_map`）
  里的才是云分配 id，即"真走 UI 添加"的条目。

```python
import sqlite3, os, json
p = os.path.expandvars(r"%APPDATA%\Trae CN\User\globalStorage\state.vscdb")
c = sqlite3.connect("file:" + p.replace("\\", "/") + "?mode=ro", uri=True)   # 只读，绝不写
def walk(n, out):
    if isinstance(n, dict):
        if "custom_model_id" in n: out.append(n)
        for v in n.values(): walk(v, out)
    elif isinstance(n, list):
        for v in n: walk(v, out)
for (k,) in c.execute("select key from ItemTable where key like '%model_list_map%'"):
    (v,) = c.execute("select value from ItemTable where key=?", (k,)).fetchone()
    s = v if isinstance(v, str) else v.decode("utf-8", "ignore")
    out = []; walk(json.loads(s), out)
    real = [o for o in out if o.get("custom_model_id") not in (None, "null")]
    ui = sorted({(str(o["custom_model_id"]), o.get("display_name")) for o in real
                 if not str(o["custom_model_id"]).startswith("7100000")})
    print(k, "| 伪造", len([o for o in real if str(o['custom_model_id']).startswith('7100000')]),
          "| 走 UI 添加的去重条目", len(ui))
    for i in ui: print("   ", i)
```

**基线（执行前应先记一次）**：scope `2021341456505097` = 6 条
（`2-fast-v1`、`2-fast-v2`、`5-free-v1`、`qoder-qwen3.8-flash`、`global:deepseek-v4.1-flash`、`cn:deepseek-v4.1-flash`）；
scope `4245681982938427` = 2 条（`2-fast-v1`、`2-fast-v2`）。
执行后再记一次，**增量就是真正入库的**。

## 10. 回报格式

1. 通道是否打开（`/json/version` 的原文）；
2. 设置页里原本已有的条目（用来核对第 7 节的出入）；
3. 成功添加 N 条 + 模型名；
4. 失败 M 条 + **每条的报错原文照抄**（不要改写、不要润色）；
5. 实际填写的字段值（API 密钥写成 `***`）；
6. 对账读数：两次的 scope 条目数与新增条目名。

## 11. 绝对不要做

- 不要改 `state.vscdb`、`storage.json`、`settings.json` 等本地文件来伪造列表（已两次实测证伪：写库 UI 不显示）。
- 不要为了通过连通性测试而改请求地址、模型名、API 格式。
- 不要删、停用、重命名任何预置模型或已有自定义条目。
- 不要把 API 密钥打印到任何输出、日志、回复里。
- 不要去改本仓别的服务（`.env`、`docker-compose.yml`、网关本身）；网关侧的问题只回报、不顺手改。
- 失败条目不要重试超过 1 次，不要换别的模型顶替。

---

## 12. 执行记录（2026-10-17 21:20–21:40，WorkBuddy agent 实跑）

### 12.1 第 3、4 节的判断要修正

第 3 节说"渲染进程没开 CDP → 只能换通道"，**这个结论是错的**，错因是启动环境：

1. 本机 agent 的 shell 注入了 `ELECTRON_RUN_AS_NODE=1` 和
   `NODE_OPTIONS=--require=...cli/vendor/shim/node-language-shim.cjs`。
   子进程继承后，`Trae CN.exe` 以**纯 Node** 运行：`--help` 打印 node 用法、
   `--disable-gpu`/`--no-sandbox`/`--remote-debugging-port` 一律报
   `bad option`（node 的 CLI 解析器），窗口起来 2s 后自行退出。
   之前看到的"没开 CDP"就是这么来的。
2. **清洗环境变量后该构建完全接受 `--remote-debugging-port=9222`**，
   `/json/version` 返回 `Chrome/142.0.7444.235`（TraeCN/1.107.1、Electron/39.2.7）。
3. 但从 agent 的作业对象里启动时，渲染进程会在 web-contents-created 后 0.07s 崩溃，
   `code: -2147483645`（0x80000003）。**这不是反调试，是 Chromium 沙箱在受限作业对象里
   初始化失败**——加 `--no-sandbox` 后一切正常（同一进程连开 10 分钟无崩溃）。
4. 可用通道（本次实跑验证）：
   ```python
   env.pop("ELECTRON_RUN_AS_NODE"); env.pop("NODE_OPTIONS"); env.pop("NODE_EXTRA_CA_CERTS"); env.pop("NODE_PATH")
   Popen([exe, "--no-sandbox", "--remote-debugging-port=9222", "--remote-allow-origins=*"], env=env)
   ```
   然后用裸 CDP（`websocket-client`）连 `type=page` 的 target，`Runtime.evaluate` 直接操作 DOM；
   **Playwright 的 `connect_over_cdp` 在 Electron 上会在 browser 级握手失败（socket hang up），别用。**
   另外主进程 `--inspect=9229` 会让渲染进程崩溃，不要碰。
5. 注意：agent 的每次工具调用结束后会回收其子进程树，所以 Trae 必须由一个常驻 keeper 进程托管
   （本次脚本在 `%TEMP%\trae-prism\launch_flags.py`），否则跨调用就没了。

### 12.2 结果（14 条清单全跑，每条最多 2 次）

成功 5 条：`1-daily-v1`、`1-daily-v2`、`3-strong-v2`、`gateway-auto`、`global:deepseek-v4.1-flash`

失败 9 条，**全部是网关侧 503，报错原文照抄**：

| 模型 | TRAE 弹窗原文 | 直连网关复核 |
| --- | --- | --- |
| `3-strong-v1` | `请求模型供应商失败…{"error":"Trae 国际版 服务不可达，请稍后重试"} (HTTP Status: 503)` | 同 503 |
| `4-max-v1` | `…{"error":"Qoder 服务不可达，请稍后重试"} (HTTP Status: 503)` | 同 503 |
| `qoder-qwen3.8-flash` | `…{"error":"Qoder 服务不可达，请稍后重试"} (HTTP Status: 503)` | 同 503 |
| `4-max-v2`、`5-free-v1`、`5-free-v2`、`6-try-v1`、`6-try-v2` | `…{"error":"OpenCode 服务不可达，请稍后重试"} (HTTP Status: 503)` | 同 503 |
| `autoclaw-cn:glm-5.3-flash` | `…{"error":"AutoClaw 服务不可达，请稍后重试"} (HTTP Status: 503)` | 同 503 |

即：`curl -H "Authorization: Bearer <key>" -d '{"model":"5-free-v1",...}' http://localhost:7863/v1/chat/completions`
直接返回 `503 {"error":"OpenCode 服务不可达，请稍后重试"}` —— 与第 8 节"13 条预期通过"不符，
是**上游通道当前不可用**，不是 TRAE 侧问题。`6-try-v2` 现在报的是 503 而不是 400 `model_not_found`。

### 12.3 其他实测细节

- 设置页「自定义」区**实际只有 `2-fast-v1`/`2-fast-v2`**（`5-free-v1`/`qoder-qwen3.8-flash`/
  `global:deepseek-v4.1-flash` 都不显示），故按第 7 节末段走了 14 条全量清单。
- 对账：scope `4245681982938427` 由 2 条 → **7 条**，新增即上面 5 条；
  scope `2021341456505097` 仍是 6 条（未动）。
  新条目字段与预置的 `2-fast-v2` 完全一致：`provider=custom_openai_compatible`、
  `base_url=http://localhost:7863/v1/chat/completions`、`is_custom_base_url=true`、
  `context_window_size={max:null,default:null}`、`max_tokens/temperature/top_p=null`。
- **坑**：连通性测试失败后弹窗会"表单内容已暂存"，下次打开会带出上一次的值
  （包括**「完整 URL」开关被打开**、展示名称被填成模型名）。所以每次打开表单必须
  先点「重置」，再核对开关为关、四个输入框为空，才可填值。失败后关闭弹窗要点 X，
  **不要点「直接保存」**（那是绕过测试入库）。

### 12.4 后续：按用户要求补齐到 16 条（2026-10-17 21:55）

21:53 复查，9 条失败模型直连网关仍是同样的 503，上游通道未恢复。
用户明确要求 16 条全量入库，于是改用弹窗在测试失败后提供的 **「直接保存」**
（按钮出现在「重置 / 直接保存 / 测试并添加」这一排；它只跳过连通性测试，
**不改任何字段**，保存结果与测试通过时完全一致）。

- 21:55–21:56 补入 9 条：`3-strong-v1`、`4-max-v1`、`4-max-v2`、`5-free-v1`、`5-free-v2`、
  `6-try-v1`、`6-try-v2`、`autoclaw-cn:glm-5.3-flash`、`qoder-qwen3.8-flash`。
- 界面「自定义」区 **16 条齐备**，DB 对账 scope `4245681982938427` **2 → 16**。
- 抽查 `3-strong-v1` / `6-try-v2` / `qoder-qwen3.8-flash` / `autoclaw-cn:glm-5.3-flash`：
  `provider=custom_openai_compatible`、`base_url=http://localhost:7863/v1/chat/completions`、
  `is_custom_base_url=true`、`context_window_size={max:null,default:null}`、
  `max_tokens/temperature/top_p=null` —— 与测试通过入库的条目、与预置 `2-fast-v2` 完全一致。
- **注意**：这 9 条是"已入列表但上游仍不可用"，在网关通道恢复前选中它们发消息会拿到 503。
  通道恢复后无需重新添加，直接用即可。
- 脚本：`%TEMP%\trae-prism\add_force_all.py`（流程 = 打开弹窗 → 点「重置」→ 填三个字段 →
  「测试并添加」→ 若出现「直接保存」则点它 → 校验行是否出现在表格）。
