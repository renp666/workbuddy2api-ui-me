# 交接文档：GLM（zcode）通道多账号手工轮换

> 交接原因：功能已打通并上线，但**账号本身没有额度**，无法做端到端的成功验收。
> 本文档自包含，接手者不需要原会话任何上下文。
> 日期：2026-10-23 22:20 ｜ 仓：`D:\work\02toolsMy\workbuddy2api-ui-me`（棱镜网关）

---

## 0. 一句话定位

让 `zcode-proxy` 这个**单凭据**旁路通道支持「多个 GLM 账号、手工切换、A 开 B 停」。
不做自动轮换、不做并发多账号——用户明确要求「类似 trae 和 workbuddy 那样手工切」。

上游仓库是 **`TriDefender/zcode-api`**（458 star），镜像是 `ghcr.io/tridefender/zcode-proxy:4.8.0`。
⚠️ 按镜像名去 GitHub 找会 404，两者不同名。

---

## 1. 交接摘要（先读这段）

| 项 | 状态 |
| --- | --- |
| 代码 | **6 个提交，全部未 push**（用户明确要求不推） |
| 上线 | `9787be4` 已构建并部署到本机生产栈（console 镜像 `:local`） |
| 切换功能 | ✅ **双向切换已实测通过**（A→B、B→A 都成功，通道全程 running） |
| 8 条通道 | ✅ 全部 `enabled + reachable` |
| 真实调用 | ❌ 仍 503 `no_healthy_account`，sidecar 日志显示上游 **502**（额度耗尽，`code 1005 exceed quota limit`） |
| 账号档案 | **1 个**：「zai 主号」，`user_id=bc61b617…fddc`，`api_key_hint=8bb5f1…5c0a`，当前生效 |
| **未验收** | **没有任何一次成功的模型回答**——因为账号没额度 |

**接手第一件事**：确认你手上有没有一个**额度充足**的 GLM 账号。
没有的话这个任务无法验收，所有结论只能停在「切换机制可用」这一层。

---

## 2. 这次踩的三个致命坑（按发现顺序）

### 坑 A：整套登录流程是照着「未登录」写的

`logged_in` 在「A 已登录、正在登录 B」的**全程都是 true**。
凡是拿它判断「要不要显示登录入口」「要不要轮询」「是不是登录成功了」的地方，全部失效。

已修的两处：
- `canLogin = control && !loggedIn` → 已登录时登录页签整个隐藏（用户报「没有登录按钮」）
- `scheduleZcodePoll` 条件 `!logged_in` → 换账号时永不轮询（用户报「授权完了没反应」）

**还有没有第三处？接手时请全局搜索 `logged_in`，逐个确认。**

### 坑 B：console 提权后写出的文件 sidecar 读不到（最隐蔽）

- console 为了读 sidecar 写的 `0600` 凭据（uid 1000），必须以 root 运行（`user: "0:0"`）
- 但 console 以 root 写出的 `credentials.json` **属主也是 root**
- sidecar 以 **uid 1000（bun）** 运行，读不到 root 的 `0600` 文件
- `startProxy` 返回 **`not_logged_in`**，而文件内容**完好无损**

后果：切换失败 → 回滚也起不来 → 通道彻底停止，页面上只说「切换失败」，看不出真实原因。

**判据**：`docker exec <zcode> ls -ln /data/credentials.json` 必须是 `1000 1000`。
**修复**：`atomicWriteFile` 里 `tmp.Chown(1000, 1000)`（已提交 `9787be4`），并有回归测试守着。

> 这个坑只有在「真实 sidecar」上才会暴露。单元测试用的是 fake control，永远发现不了。

### 坑 C：回滚里的错误被丢弃

```go
// 修之前
if stopped { _, _ = h.zcodeControl(ctx, map[string]any{"cmd": "startProxy"}) }  // 错误被吞
```

回滚失败时用户只看到「切换失败」，实际通道已经停了。
已修：回滚失败会把 `「…；且回滚未能恢复通道（not_logged_in），请到「通道状态」重新启用」` 带出来。

### 坑 D：拿文件摘要当账号身份（「怎么变 3 个账号了」）

`credentials.json` 的内容**每次 token 刷新都会变**（sidecar 重新签发 JWT，`iat` 变），
文件摘要跟着变。早期用摘要判断「这个账号归档过没有」，于是一个账号刷两次就变成
2~3 条「自动备份」档案。线上实测：三条档案解密后 `userId` 和 `apiKey` **完全一致**，
只有 `jwt.iat` 不同（1791534714 / 1792764200 / 1792765178）。

**摘要只能回答「这份文件有没有被换过」，不能回答「这是哪个账号」。**
账号身份必须读凭据里的 `userId`——那是 OAuth 的用户标识，跨 token 刷新稳定。

已修（`f81c875`、`aed6734`）：
- console 解密凭据只读身份（`crypto/aes` + `cipher.NewGCM`，种子 `sha256("{home}-linux-x64")`）
- 判重改成 `userId + provider`
- 打开页面时先回填旧档案的 `user_id`，再合并重复项（内容取最新 token，保留用户起的名字）
- 解不开凭据时退回摘要判重，不让上游换 KDF 直接把功能打死

---

## 3. 事实基础（不要重新论证）

### 3.1 zcode-proxy 是单凭据架构（四层证据）

| 层 | 证据 |
| --- | --- |
| 存储 | `src/auth/store.ts`：`credentials.json` 是单个加密 blob `{encrypted:"..."}`；`saveCredential` 用 `JSON.stringify(cred)` **整体覆盖**；`oauthCredentialsPath` 注释 `parsed but currently not honored` |
| 内存 | `src/auth/manager.ts`：`AuthManager.oauthCred` 是 `Credential \| null` 单值，无池、无冷却 |
| 控制面 | `src/android/control.ts` 只有 10 个命令：`status / startOAuth / deliverOAuthCode / logout / setConfig / startProxy / stopProxy / getLogs / quota / shutdown`，**无账号维度** |
| 旁证 | 上游 README 无多账号；issues 搜 `account` 为 0 条 |

→ 多账号只能在 console 侧做**文件级留档 + 换文件**。

### 3.2 加密密钥是容器常量

密钥 = `sha256(homedir + platform + arch)`（可被 `ZCODE_PROXY_CREDENTIAL_SECRET` 覆盖）。
同一容器内恒定 → **凭据文件可整文件搬运**。

⚠️ **绝对不要改 zcode-proxy 的运行 uid**：`homedir` 一变密钥就变，已有凭据全部解不开。
⚠️ **不要设置 `ZCODE_PROXY_CREDENTIAL_SECRET`**：一旦设置必须永远保持一致。

### 3.2.1 console 会解密凭据——只读身份，不重新加密

要区分账号就必须读 `userId`，而它在加密 blob 里。console 复刻同一个种子：

```
seed = "{homedir}-{platform}-{arch}"      // sidecar 是 /home/bun-linux-x64
key  = sha256(seed)
blob = base64(iv[12] || AES-GCM(key, plaintext))
```

- `platform`/`arch` **写死 `linux`/`x64`**：Node 的 `process.arch` 是 `x64` 不是 `amd64`，
  写错一个字符就解不开（这条踩过）
- `homedir` 默认 `/home/bun`，可用 `WB2A_ZCODE_HOME` 覆盖
- **写回文件时仍然是整份拷贝**，加密格式、密钥一律不碰
- 解不开时 `zcodeReadCredentialIdentity` 返回 nil，全部判据退回文件摘要——
  上游哪天换 KDF，功能会降级但不会崩

凭据解密后的字段：`apiKey`、`secret`、`provider`、`userId`、`jwt`。
`jwt` 的 payload 是 `{user_id, token_version, sub, iat}`，`iat` 就是「最近一次签发」。

### 3.3 换凭据必须 `stopProxy → 换文件 → startProxy`

android 入口**没有凭据热加载**：`syncAuthWithDisk` 只存在于 `startServePanel`（`index.ts:247`）；
android 入口只在 `startProxy()` 内部 `loadCredential() + setOAuthCredential`。

只换文件不重启 = 内存里还是旧 token，`/v1` 继续花旧账号额度。

### 3.4 provider 变更更严格

`setConfig` 在 proxy 运行时返回 `stop_proxy_first`。
切换跨 provider（`zai` ↔ `bigmodel`）必须先停。**这条分支从未被真实触发过**（见待办 2）。

---

## 4. 提交清单（均未 push）

| 提交 | 内容 |
| --- | --- |
| `84d6c46` | fix(trae): 国际版凭据刷新按客户端类别选 client_id（同期修复，与本任务无关） |
| `0cccaa0` | feat(zcode): 账号档案与手工轮换（A 开 B 停）——主体实现 |
| `6e2cf40` | fix(zcode): console 在 zcode overlay 下提权，否则读不到凭据文件 |
| `5b2b576` | fix(zcode): 已登录状态下也能登录其它账号（拆坑 A 第一处） |
| `31d51f2` | fix(zcode): 换账号时持续轮询，用凭据指纹判断登录完成（拆坑 A 第二处） |
| `9787be4` | fix(zcode): 落盘把属主交还 sidecar（修坑 B、坑 C） |
| `f81c875` | fix(zcode): 用凭据里的 userId 认账号（修坑 D） |
| `aed6734` | fix(zcode): 旧档案先回填账号身份再合并 |

---

## 5. 数据与文件布局

宿主 `runtime/zcode/` 同时挂给两个容器——**必须是同一份文件，否则切换是假的**：

```
runtime/zcode/
├── config.yaml           # zcode-proxy 配置（binds 127.0.0.1）
├── credentials.json      # sidecar 唯一读取的凭据；属主必须 1000:1000，权限 0600
├── accounts.json         # 账号索引（console 写）
└── auths/
    └── <id>.json         # 档案：整份凭据文件的副本
```

| 容器 | 挂载 | 身份 | 环境变量 |
| --- | --- | --- | --- |
| `zcode-proxy` | `./runtime/zcode:/data` | uid **1000 (bun)** | `ZCODE_PROXY_STORE_DIR=/data` |
| `console` | `./runtime/zcode:/app/zcode-data` | uid **0 (root)** | `WB2A_ZCODE_DATA_DIR=/app/zcode-data` |

### accounts.json

```json
{
  "active": "20261023-214452-98563e",
  "accounts": [
    {"id":"20261023-214452-98563e","label":"zai 主号","provider":"zai","plan":"start-plan",
     "saved_at":"2026-10-23T22:20:14+08:00","digest":"c3d4efb1b060aec9",
     "user_id":"bc61b617-6b0f-4bfb-a0cf-e74f0ed0fddc",
     "api_key_hint":"8bb5f1…5c0a","jwt_iat":1792765178}
  ]
}
```

- `id` = 时间戳串（`20060102-150405-随机6位`），**不是 digest**
- `digest` = 凭据文件内容 sha256 前 16 位。**只表示「这份文件」**，token 一刷新就变，
  **绝不能当账号身份用**
- `user_id` = 凭据里的 Z.AI 用户标识，**唯一稳定的账号身份**
- `api_key_hint` = API key 掩码（前 6 后 4），给人认人用
- `jwt_iat` = start-plan token 签发时间（Unix 秒），近似「最近一次登录」
- `active` 只是缓存，视图层用 `user_id` 现算

### 常量（`console/zcodeaccounts.go`）

```go
zcodeCredentialsFile = "credentials.json"
zcodeAccountsFile    = "accounts.json"
zcodeAuthsDir        = "auths"
zcodeAutoLabelPrefix = "自动备份 "
zcodeProxyUID/GID    = 1000   // sidecar 的 bun 用户，落盘后必须 chown 回去
```

---

## 6. 端点契约

全部 `withAdmin` 保护（`POST /admin/login` 拿 cookie + CSRF）。

| 方法 | 路径 | 请求体 |
| --- | --- | --- |
| GET | `/admin/zcode/accounts` | — |
| POST | `/admin/zcode/accounts/save` | `{"label":"名字"}` |
| POST | `/admin/zcode/accounts/switch` | `{"id":"..."}` |
| POST | `/admin/zcode/accounts/delete` | `{"id":"..."}`（当前生效的返回 409） |
| POST | `/admin/zcode/accounts/rename` | `{"id":"...","label":"新名"}` |

### GET 响应

```json
{
  "available": true,
  "accounts": [{"id":"...","label":"zai 主号","provider":"zai","plan":"start-plan",
                "saved_at":"...","active":true,
                "user_id":"bc61b617…fddc","api_key_hint":"8bb5f1…5c0a","jwt_iat":1792765178}],
  "active": "20261023-214452-98563e",
  "unfiled": false,
  "count": 1,
  "current_digest": "2468e9b06fba1235",
  "current_user_id": "bc61b617…fddc"
}
```

| 字段 | 含义 |
| --- | --- |
| `available=false` | `WB2A_ZCODE_DATA_DIR` 未配置/不可写，前端整块隐藏 |
| `unfiled=true` | 磁盘凭据不对应任何档案（按 `user_id` 判，不是按摘要） |
| `current_digest` | 磁盘凭据的文件指纹，**换账号轮询用它判断「文件变了」** |
| `current_user_id` | 磁盘凭据的账号身份（掩码），用来回答「现在生效的是哪个账号」 |

⚠️ 两个字段分工不同，别混：`current_digest` 答「文件有没有变」，`current_user_id` 答「是哪个账号」。
token 刷新只改前者，不改后者。

### switch 响应（成功）

```json
{"switched":true,"account":{...},"provider":"zai","plan":"start-plan","proxy_running":true}
```

幂等切换返回 `{"switched":false,"already_active":true}`。

### save 响应

```json
{"saved":true,"created":true,"applied":true,"apply_error":""}
```

`created=false` = 同 digest 已归档（幂等）；`applied/apply_error` = 新凭据是否已生效。

---

## 7. 切换时序（`zcodeSwitchAccount`）

```
1. autoSnapshotUnfiled     # 先给未归档的当前凭据留档（否则切走就永久丢）
2. 同 digest 判定          # 已经是目标账号 → 直接返回 already_active
3. 记录 wasRunning / needProvider
4. 备份 prevCred 到内存    # 回滚用
5. stopProxy               # 必须停：android 入口不热加载凭据
6. setConfig（仅跨 provider 时）
7. copyFile 档案 → credentials.json
8. startProxy              # 重读凭据
9. 回读 logged_in 确认
10. 写索引
任一步失败 → rollback：恢复 prevCred + 尽力 startProxy（失败会把原因带出来）
```

---

## 8. 前端状态机（`console/web/app.js`，搜索 `zcode`）

### 状态变量

```js
zcodeStatus           // /admin/zcode 响应
zcodeAccounts         // /admin/zcode/accounts 响应
zcodeAuthURL          // 授权链接（已登录时也要保留，别清）
zcodeLoginPending     // 是否正在等这次授权完成
zcodeLoginBaseDigest  // 发起登录前的 current_digest
zcodeAccountMessage   // 操作回执（单独存，防止异步刷新冲掉）
```

### 三条铁律

```js
// 1) 入口可见性只看控制面，不看登录状态
const canLogin = control;                 // 不是 control && !loggedIn
const loginFirst = control && !loggedIn;  // 只决定首次落哪一页

// 2) 轮询：未登录 或 正在换账号
if (zcodePageVisible() && zcodeStatus?.control && (!zcodeStatus.logged_in || zcodeLoginPending))

// 3) 完成判据是磁盘指纹变化，不是 logged_in
if (zcodeLoginPending && cur && cur !== zcodeLoginBaseDigest) { /* 登录完成 */ }
```

### 用户流程

```
Zcode 页签 → ⇆ 换账号 → 选服务商 → 开始登录
  → 新标签页打开 chat.z.ai 授权 → 用 B 完成授权
  → 页面每 3 秒轮询，发现 current_digest 变化
  → 提示「新账号已登录完成，点保存当前账号起名」
  → 保存 → 后端 stop→start 让 B 生效（applied=true）
  → 列表里 A 变「备用」，可一键切回
```

---

## 9. 踩坑全表

| # | 症状 | 根因 | 修法 |
| --- | --- | --- | --- |
| 1 | 「没有登录按钮」 | `canLogin = control && !loggedIn` | 只看 `control`（`5b2b576`） |
| 2 | 授权链接一闪就消失 | 轮询回调清空 `zcodeAuthURL` | 不清，已登录再登录是正常操作 |
| 3 | 已登录显示「未登录」 | badge 硬编码 | 按 `loggedIn` 渲染 |
| 4 | 切了账号但高亮错人 | 用索引 `active` 字段 | 用 `byDigest(digestFile(credPath))` 现算 |
| 5 | 换了凭据 `/v1` 还是旧额度 | 只换文件没重启 | stopProxy→copy→startProxy |
| 6 | 保存后新账号不生效 | save 只落盘 | 新建档案且 running 时 `zcodeRestartProxy` |
| 7 | **授权完页面毫无反应** | 轮询条件 `!logged_in` | 加 `\|\| zcodeLoginPending` + digest 判据（`31d51f2`） |
| 8 | console 读不到 credentials.json | uid 10001 vs 0600(uid 1000) | console `user: "0:0"`（`6e2cf40`） |
| 9 | **切换/回滚后通道再也起不来** | console 以 root 落盘，sidecar 读不到 | `atomicWriteFile` 里 `Chown(1000,1000)`（`9787be4`） |
| 10 | 回滚失败但只报「切换失败」 | `_, _ =` 吞掉 startProxy 错误 | 把失败原因拼进错误（`9787be4`） |
| 11 | `git add console/server.go` 后 HEAD 不可编译 | 该文件混有他人未提交的 raccoon 端点 | 改共享文件前先 `git diff <file>`；用 `git hash-object` + `update-index` 精确暂存 |
| 12 | 发版后 `/admin/health` 顶层 `ok:false` | glm 无手工测速缓存，`probe_state` 为空 | 既有设计，不是故障（见 10.2） |
| 13 | **一个账号显示成 3 个** | 用文件摘要当账号身份，token 刷新让摘要漂移 | 改读凭据里的 `userId`（`f81c875`） |
| 14 | 合并逻辑对旧档案无效 | 旧档案索引里没有 `user_id`，dedupe 全部跳过 | 先回填再合并（`aed6734`） |

---

## 10. 排查手册

### 10.1 通用入口（已有轮子，别手搓）

```bash
cd /d/work/02toolsMy/workbuddy2api-ui-me
python -c "
import sys, json; sys.path.insert(0,'.build')
from wb2a_admin import Client
c=Client(); c.login()
print(json.dumps(c._call('GET','/admin/zcode'), ensure_ascii=False))
print(json.dumps(c._call('GET','/admin/zcode/accounts'), ensure_ascii=False))
"
```

### 10.2 `/admin/health` 的 `ok` 与单通道 `healthy()` 口径不同

- `channels` = **实时快照**，只算 `reachable`
- `history` = **后台轮次**，判据 `Reachable && ProbeState=="ok"`
- glm 没有手工测速缓存 → `probe_state` 缺失 → history 恒 false → 顶层 `ok:false`

**这是既有设计。** 判断 glm 有没有问题，看 `reachable` + 真实调用。

### 10.3 决定性判据：console 内部实连（不要接管道，会得假阳性）

```bash
for p in 8080 3000 41980 7865 7866 7867 7868 7869; do
  docker exec workbuddy2api-ui-me-console-1 sh -c "wget -q -O /dev/null -T 3 http://127.0.0.1:$p/healthz; echo \"$p=\$?\""
done
```

`0`=通；`4`=不可达；`8`=服务器返回错误（多为 404，正常）。
**`8080=8` 是 zcode 的正常值**（sidecar 没有 `/healthz` 路由）。

### 10.4 属主检查（坑 B 的专用判据）

```bash
docker exec workbuddy2api-ui-me-zcode-proxy-1 ls -ln /data/credentials.json
# 必须 1000 1000。若是 0 0，sidecar 一定 not_logged_in
```

急救（把属主改回去，然后重启 sidecar）：

```bash
docker exec workbuddy2api-ui-me-console-1 sh -c 'chown -R 1000:1000 /app/zcode-data'
docker restart workbuddy2api-ui-me-zcode-proxy-1
```

### 10.5 真实调用

```bash
python -c "
import json, urllib.request, urllib.error
k=json.load(open('runtime/wb2api/keys/keys.json'))
body=json.dumps({'model':'zcode/glm-5.3-flash','messages':[{'role':'user','content':'hi'}],'max_tokens':8}).encode()
req=urllib.request.Request('http://127.0.0.1:7863/v1/chat/completions',data=body,
  headers={'Content-Type':'application/json','Authorization':'Bearer '+k['api_key']},method='POST')
try: print(urllib.request.urlopen(req,timeout=90).read().decode()[:300])
except urllib.error.HTTPError as e: print(e.code, e.read().decode()[:400])
"
```

- `503 no_healthy_account` → 额度耗尽（上游 `1005`）
- `401/403` → 凭据问题，不是额度

### 10.6 sidecar 侧的真实失败

```bash
docker logs --tail 20 workbuddy2api-ui-me-zcode-proxy-1
# 会打印请求表：| #001 | 22:15:54 | OAI | glm-5.3-flash | batch | 502 | 7674ms |
```

### 10.7 拿授权链接

```bash
python -c "
import sys; sys.path.insert(0,'.build')
from wb2a_admin import Client
c=Client(); c.login()
print(c._call('POST','/admin/zcode/login',{'provider':'zai'},need_csrf=True)['authorize_url'])
"
```

`zai` → `chat.z.ai`（Z.AI 国际站）；`bigmodel` → 智谱国内站。

### 10.8 手工确认「这几个档案是不是同一个账号」

界面只显示掩码，要确认真身就在 sidecar 里解密（它有 homedir，密钥自然正确）：

```bash
cat > /tmp/dec.js <<'EOF'
const { readFileSync, readdirSync } = require("fs");
const { createHash } = require("crypto");
const os = require("os");
const seed = os.homedir() + "-" + process.platform + "-" + process.arch;
const key = new Uint8Array(createHash("sha256").update(seed, "utf-8").digest());
(async () => {
  const k = await crypto.subtle.importKey("raw", key, { name: "AES-GCM" }, false, ["decrypt"]);
  for (const f of readdirSync("/data/auths").filter(x => x.endsWith(".json"))) {
    const b = Buffer.from(JSON.parse(readFileSync("/data/auths/" + f, "utf-8")).encrypted, "base64");
    const c = JSON.parse(new TextDecoder().decode(
      await crypto.subtle.decrypt({ name: "AES-GCM", iv: b.subarray(0, 12) }, k, b.subarray(12))));
    const iat = JSON.parse(Buffer.from(c.jwt.split(".")[1], "base64url").toString()).iat;
    console.log(f, "userId=" + c.userId, "apiKey=" + c.apiKey.slice(0, 6) + "..." + c.apiKey.slice(-4), "iat=" + iat);
  }
})();
EOF
docker cp /tmp/dec.js workbuddy2api-ui-me-zcode-proxy-1:/tmp/dec.js
docker exec workbuddy2api-ui-me-zcode-proxy-1 sh -c 'cd /app && bun run /tmp/dec.js'
```

`userId` 相同 = 同一个账号（`iat` 不同只是 token 刷新）。

---

## 11. 发版与回退

本机栈 = 默认链 `docker-compose.yml` + `docker-compose.override.yml`
（override 里 include 了 8 个通道 overlay，镜像钉 `:local`）。

### 只改 console 时

```bash
cd /d/work/02toolsMy/workbuddy2api-ui-me
docker tag workbuddy2api-console:local workbuddy2api-console:local-prev
git archive --format=tar -o .build/release-ctx.tar HEAD
docker build -f deploy/console.Dockerfile -t workbuddy2api-console:local \
  --build-arg GOPROXY=https://goproxy.cn,direct --pull=false - < .build/release-ctx.tar
docker compose -f docker-compose.yml -f docker-compose.override.yml up -d --force-recreate --no-deps \
  console zcode-proxy qoder-proxy opencode-proxy autoclaw-proxy trae-proxy traeai-proxy raccoon-proxy freellmapi
```

**铁律**：console 与 8 个 sidecar 必须**同一条链、同一次调用**重建。
只重建 console 会让 sidecar 孤立成 `Exited(255)`，且新 console 缺 `WB2A_*_URL`。
`--no-deps` 是为了不连带重建 core（core 没改时纯浪费 8 分钟）。

### 回退

```bash
docker tag workbuddy2api-console:local-prev workbuddy2api-console:local
docker compose -f docker-compose.yml -f docker-compose.override.yml up -d --force-recreate --no-deps \
  console zcode-proxy qoder-proxy opencode-proxy autoclaw-proxy trae-proxy traeai-proxy raccoon-proxy freellmapi
```

### 发版后必跑

```bash
curl -s http://127.0.0.1:7863/app.js | grep -c "zcodeLoginPending"   # 应为 8
docker exec workbuddy2api-ui-me-zcode-proxy-1 ls -ln /data/credentials.json   # 必须 1000 1000
```

### 若停用 zcode overlay（让 console 回 app 用户）

```bash
docker run --rm -v "D:/work/02toolsMy/workbuddy2api-ui-me/runtime/wb2api/console:/d" alpine chown -R 10001:10001 /d
```

---

## 12. 待办（按优先级）

1. **【阻塞验收】用额度充足的账号走通一次完整对话**
   两个现有账号都没额度。走通标准：切换成功 + 真实调用 200 + 切回后原账号仍可用。

2. **provider 跨站切换从未验证**
   `zai` ↔ `bigmodel` 需要 `stopProxy → setConfig → startProxy`。
   `adminZcodeLogin` 里只在 provider 不同时才 `setConfig`，运行中会返回 `stop_proxy_first`，
   前端文案「切换服务商需先停用通道」。**这条分支一次都没被真实触发过**，很可能有 bug。

3. **`zcodeLoginPending` 没有超时兜底**
   OAuth flow 5 分钟超时（`LOGIN_TIMEOUT_MS = 300_000`），但前端 `zcodeLoginPending`
   不会自动复位。用户放弃授权后会一直卡在「正在等待授权」。
   建议加 5 分钟定时器复位并提示重试。

4. **自动备份档案可能重新累积（已大幅缓解，未彻底消除）**
   现在同一账号只会有一条档案（按 `userId` 去重），token 刷新不会再堆。
   但**不同账号**每次登录仍会生成一条「自动备份」——这是有意的（不丢凭据）。
   清理入口仍然只有逐个删。

5. **界面上只有 userId，没有邮箱**
   Z.AI 的 `/quota` 和 sidecar 的 `status` 都**不返回**邮箱/昵称，
   凭据里也只有 `userId`（UUID）和 `apiKey`。所以界面上能看到的「真实信息」上限就是
   `bc61b617…fddc` + `8bb5f1…5c0a` + 套餐 + token 签发时间。
   想显示邮箱只能去调 Z.AI 的用户信息接口（未找到公开端点），或改上游 sidecar。

6. **代码质量**
   `console/web/app.js` 的 zcode 部分是线性堆叠，未模块化；
   `zcodeAccountsView` 返回 `map[string]any`，类型不安全。重构时优先改 struct（前端契约不变）。

---

## 13. 红线

1. **不要改 zcode-proxy 的运行 uid** —— 会改 `sha256(homedir+platform+arch)`，已有凭据全部解不开。
2. **不要设 `ZCODE_PROXY_CREDENTIAL_SECRET`** —— 一旦设置必须永远一致。
3. **不要 push** —— 用户明确要求，6 个提交都在本地。
4. **不要在 `console/server.go` 上 `git add` 整个文件** —— 工作区有他人未提交的 raccoon 端点，
   会把不可编译的 HEAD 提交上去（踩过一次）。
5. **不要只重建 console** —— sidecar 会孤立。
6. **不要把 `logged_in` 当成「登录成功」** —— A 已登录时它恒为 true。
7. **不要把文件摘要当成账号身份** —— token 刷新就漂移，会凭空变出多个「账号」。
8. **不要用 `_ =` 吞掉 startProxy 的错误** —— 通道停了用户不会知道。

---

## 14. 文件索引

| 文件 | 作用 |
| --- | --- |
| `console/zcodeaccounts.go` | 档案/切换/保存/删除/改名 + 凭据解密读身份 + 去重合并 + `atomicWriteFile`（含 chown） |
| `console/zcodeaccounts_test.go` | 24 个测试：切换、幂等、自动留档、删除保护、回滚、属主、身份判重、回填合并 |
| `console/zcodecontrol.go` | 登录前自动留档（`adminZcodeLogin` 开头） |
| `console/server.go` | 5 个端点注册；`Config.ZCodeDataDir` ⚠️ 含他人改动 |
| `console/main.go` | 解析 `WB2A_ZCODE_DATA_DIR`（仅接受绝对路径） |
| `console/web/app.js` | 前端状态机（搜索 `zcode`） |
| `console/web/index.html` | `#zcode-accounts-panel` 等 |
| `console/web_test.cjs` | 前端用例（搜索 `zcode 换账号`） |
| `deploy/compose.zcode.yml` | 共享挂载 + console 提权 |
| `AGENTS.md` | 项目级约定（发版、通道架构） |

---

## 15. 给接手者的最后一句话

这个任务的代码路径已经验证过了（双向切换实测通过、账号身份判重生效、重复档案已合并），
**剩下的唯一障碍是账号额度**。
不要再在没有额度的情况下反复改代码——那只会把已经能用的东西改坏。
先解决「有一个能用的账号」，再谈验收。

如果用户再说「还是不行」，先问清楚是**哪一步不行**：
- 看不到账号 / 账号数不对 → 看 `/admin/zcode/accounts` 的 `count` 和 `user_id`
- 点了没反应 → 看 `zcodeLoginPending` 是否被置起、轮询有没有排上
- 切换失败 → 看错误里的 `not_logged_in`（属主）还是 `start_proxy_first`（provider）
- 切过去不能用 → 先跑 10.5，`503 no_healthy_account` 就是没额度，不是代码问题
