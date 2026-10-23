#!/usr/bin/env python3
# -*- coding: utf-8 -*-
r"""棱镜网关 · 小浣熊账号管理 CLI

解决「只能手工换 json 文件」的问题：一次导入全部账号，之后用一条命令切换。

用法
----
    # 看当前账号池
    python scripts/raccoon-ctl.py list

    # 切换到某个账号（停用其他所有，只留它）
    python scripts/raccoon-ctl.py switch 主号

    # 恢复「全部启用，由容器自动挑积分最高的」
    python scripts/raccoon-ctl.py all-on

    # 单个开关
    python scripts/raccoon-ctl.py off 小号
    python scripts/raccoon-ctl.py on 小号

    # 加一个账号（不覆盖已有的）
    python scripts/raccoon-ctl.py add --name 主号 --token eyJ... --refresh eyJ...

    # 批量导入（Raccoon-Daily 登录输出，一行一个：备注|token|refresh_token）
    python scripts/raccoon-ctl.py import tokens.txt

    # 扫描桌面端登录态目录，自动识别、去重、批量导入
    python scripts/raccoon-ctl.py scan --dry-run          # 先预览
    python scripts/raccoon-ctl.py scan                     # 扫 %USERPROFILE%\.box-agent\config
    python scripts/raccoon-ctl.py scan D:\raccoon-backups  # 或扫自己存放导出的目录

    # 导出全部账号做备份（目录结构与 scan 兼容，可原样导回）
    python scripts/raccoon-ctl.py export
    python scripts/raccoon-ctl.py export D:\raccoon-backups

    # 删除
    python scripts/raccoon-ctl.py rm 小号

为什么不用换文件
----------------
棱镜的小浣熊 sidecar 本来就支持多账号池（deploy/raccoon2api/raccoon/pool），
只是必须满足两个条件才会并存：

    1. 每个账号的 uid 必须不同 —— uid 为空时全部落到固定值 "raccoon-desktop"，
       后导入的会把先导入的覆盖掉。这是「只能有一个账号」的真正原因。
    2. 凭据文件名必须是 raccoon-<uid>.json。

导入走控制台接口是热生效的，不需要重启容器。

关于「切换」
------------
sidecar 选号的规则是「健康账号里挑积分最高的」，本身不支持指定账号。
所以「切换」= 停用其他账号，只留目标启用 —— 等价于强制指定。本脚本的
switch 子命令就是把这个动作一次做完。

凭据来源
--------
控制台管理员密钥依次从这些地方找：--key 参数 → 环境变量 WB2A_ADMIN_KEY
→ <项目>/runtime/wb2api/keys/keys.json 的 admin_key 字段。
"""

import argparse
import base64
import glob
import http.cookiejar
import json
import os
import re
import sys
import urllib.error
import urllib.request

DEFAULT_BASE = "http://127.0.0.1:7863"
ACCOUNT_SEP = "&"
FIELD_SEP = "|"
# 脚本位于 <项目>/scripts/，默认路径一律相对项目根解析。
PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


# ---------------------------------------------------------------- 控制台客户端

class Console:
    """带会话与 CSRF 的控制台客户端。

    鉴权是 session cookie + X-CSRF-Token，不是 Bearer：POST /admin/login 换
    cookie，之后每个写操作都要回带 csrf。Origin 必须与 Host 一致，否则
    服务端 sameOrigin 直接 403。
    """

    def __init__(self, base, key):
        self.base = base.rstrip("/")
        self.key = key
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar)
        )
        self.csrf = None

    def _request(self, method, path, payload=None, raw=None):
        data = raw if raw is not None else (
            json.dumps(payload).encode("utf-8") if payload is not None else None
        )
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Origin", self.base)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if method != "GET" and self.csrf:
            req.add_header("X-CSRF-Token", self.csrf)
        with self.opener.open(req, timeout=30) as resp:
            body = resp.read().decode("utf-8", "replace")
        return json.loads(body) if body.strip() else {}

    def login(self):
        out = self._request("POST", "/admin/login", {"key": self.key})
        self.csrf = out.get("csrf")
        if not self.csrf:
            raise RuntimeError("登录响应里没有 csrf，管理员密钥可能不对")
        return out

    def accounts(self):
        out = self._request("GET", "/admin/raccoon")
        return out.get("accounts") or []

    def patch(self, uid, enabled):
        return self._request(
            "POST", "/admin/raccoon/accounts/patch", {"uid": uid, "enabled": enabled}
        )

    def import_raw(self, doc):
        raw = json.dumps(doc, ensure_ascii=False).encode("utf-8")
        return self._request("POST", "/admin/raccoon/accounts/import", raw=raw)

    def delete(self, uid):
        return self._request("POST", "/admin/raccoon/accounts/delete", {"uid": uid})

    def refresh(self, uid):
        return self._request("POST", "/admin/raccoon/accounts/refresh", {"uid": uid})

    def post(self, path, payload=None):
        """通用 POST，path 相对 /admin/raccoon/。"""
        return self._request("POST", "/admin/raccoon/" + path.lstrip("/"), payload or {})


# ---------------------------------------------------------------- 工具函数

def find_admin_key(explicit, start_dir):
    if explicit:
        return explicit
    env = os.environ.get("WB2A_ADMIN_KEY", "").strip()
    if env:
        return env
    cur = os.path.abspath(start_dir)
    for _ in range(5):
        candidate = os.path.join(cur, "runtime", "wb2api", "keys", "keys.json")
        if os.path.isfile(candidate):
            with open(candidate, "r", encoding="utf-8") as fh:
                doc = json.load(fh)
            key = (doc.get("admin_key") or "").strip()
            if key:
                return key
        parent = os.path.dirname(cur)
        if parent == cur:
            break
        cur = parent
    return ""


def is_off(account):
    return bool(account.get("disabled")) or account.get("enabled") is False


def slugify(text):
    if not text:
        return ""
    cleaned = re.sub(r"[^0-9A-Za-z_-]+", "-", text).strip("-")
    return cleaned[:40]


def pick_account(accounts, target):
    """按 uid 精确匹配，其次按昵称匹配，再其次按昵称前缀。"""
    target = target.strip()
    for acc in accounts:
        if acc.get("uid") == target:
            return acc
    for acc in accounts:
        if (acc.get("nickname") or "") == target:
            return acc
    hits = [a for a in accounts if target and target in (a.get("nickname") or "")]
    if len(hits) == 1:
        return hits[0]
    if len(hits) > 1:
        names = "、".join("%s(%s)" % (a.get("nickname"), a.get("uid")) for a in hits)
        raise SystemExit("「%s」匹配到多个账号：%s\n请用 uid 精确指定。" % (target, names))
    raise SystemExit("找不到账号「%s」。先跑 list 看看有哪些。" % target)


def describe(acc):
    state = "已停用" if is_off(acc) else ("冷却中" if acc.get("cooling") else "可用")
    points = acc.get("points")
    parts = [
        "%-16s" % acc.get("uid", ""),
        "%-12s" % (acc.get("nickname") or ""),
        "%-6s" % state,
        "积分 %-8s" % (points if points is not None else "—"),
    ]
    if acc.get("reason"):
        parts.append("说明 %s" % acc["reason"])
    return "  ".join(parts)


def split_entries(text):
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        for chunk in line.split(ACCOUNT_SEP):
            chunk = chunk.strip()
            if chunk:
                out.append(chunk)
    return out


def parse_entry(entry, index):
    parts = [p.strip() for p in entry.split(FIELD_SEP)]
    if len(parts) >= 3:
        note, access, refresh = parts[0], parts[1], parts[2]
    elif len(parts) == 2:
        if parts[0].count(".") == 2:          # 第一段是 JWT，说明没写备注
            note, access, refresh = "", parts[0], parts[1]
        else:
            note, access, refresh = parts[0], parts[1], ""
    else:
        note, access, refresh = "", parts[0], ""
    if not access:
        raise ValueError("第 %d 条缺少 access_token" % index)
    return note, access, refresh


def unique_uid(note, index, taken):
    base = slugify(note) or ("%02d" % index)
    uid = base
    n = 1
    while uid in taken:
        n += 1
        uid = "%s-%d" % (base, n)
    taken.add(uid)
    return uid


# ---------------------------------------------------------------- 桌面端登录态

DESKTOP_UID = "raccoon-desktop"


def default_auth_root():
    """小浣熊桌面端登录态目录。

    Windows 为 %USERPROFILE%\\.box-agent\\config，auth.json 就在这里。
    只扫 config 而不是整个 .box-agent：后者还有 skills / memory / browsers
    等大量无关 JSON，扫进来全是噪音。
    """
    home = os.path.join(os.path.expanduser("~"), ".box-agent")
    config = os.path.join(home, "config")
    return config if os.path.isdir(config) else home


def first_text(*vals):
    for value in vals:
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def parse_auth_doc(raw):
    """按棱镜 auth.Parse 的口径解析一份 auth.json。

    兼容 {"auth": {...}} 嵌套形；access_token / token / auth_token 三选一。
    返回 (doc, None) 或 (None, 失败原因)。
    """
    if not raw.strip():
        return None, "空文件"
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        return None, "不是合法 JSON（%s）" % exc.msg
    if not isinstance(data, dict):
        return None, "顶层不是对象"

    inner = data.get("auth")
    if isinstance(inner, dict):
        merged = dict(inner)
        if not first_text(merged.get("uid")):
            merged["uid"] = data.get("uid")
        if not first_text(merged.get("nickname")):
            merged["nickname"] = first_text(data.get("nickname"), data.get("name"))
        data = merged

    access = first_text(data.get("access_token"), data.get("token"), data.get("auth_token"))
    if not access:
        return None, "缺少 access_token"

    doc = {
        "uid": first_text(data.get("uid")),
        "nickname": first_text(data.get("nickname"), data.get("name")),
        "access_token": access,
        "refresh_token": first_text(data.get("refresh_token")),
    }
    # expires_at 原样透传：棱镜的 normalizeExpiresAt 同时吃秒、毫秒与字符串，
    # 带上它导入时才能正确判断「新凭证是否比池里的旧」，避免被 409 挡掉。
    expires = data.get("expires_at")
    if expires not in (None, "", 0):
        doc["expires_at"] = expires
    return doc, None


def collect_json_files(root, max_depth):
    out = []
    root = os.path.abspath(root)
    if not os.path.isdir(root):
        return out
    base_depth = root.rstrip(os.sep).count(os.sep)
    for cur, dirs, files in os.walk(root):
        if cur.rstrip(os.sep).count(os.sep) - base_depth >= max_depth:
            dirs[:] = []
        dirs.sort()
        for name in sorted(files):
            if not name.lower().endswith(".json"):
                continue
            path = os.path.join(cur, name)
            try:
                if os.path.getsize(path) > 1024 * 1024:
                    continue
            except OSError:
                continue
            out.append(path)
    # auth.json 优先处理：多份导出里它才是正本，备份/副本排后面，
    # 这样去重时留下的是正本所在的目录名，而不是「备份」这种误导性名字。
    out.sort(key=lambda p: (0 if os.path.basename(p).lower() == "auth.json" else 1, p))
    return out


def uid_from_token(token):
    """从 access_token 的 JWT payload 取 sub 当 uid —— 与棱镜 auth.Parse 同一口径。

    桌面端 auth.json 只有 access_token / refresh_token / office_identity，不含 uid。
    两边推导口径必须一致：同一账号若用网页导入一次、再用 scan 导一次，口径不同就会
    落成两份文件，它们共用 refreshToken，轮换时互相踢下线。

    只读不验签：取的是身份标签，凭证有效性由上游判定。
    """
    parts = (token or "").split(".")
    if len(parts) != 3:
        return ""
    seg = parts[1]
    seg += "=" * (-len(seg) % 4)
    try:
        payload = json.loads(base64.urlsafe_b64decode(seg))
    except (ValueError, TypeError):
        return ""
    sub = payload.get("sub") if isinstance(payload, dict) else None
    if not isinstance(sub, str):
        return ""
    return "".join(ch for ch in sub.strip() if ch.isalnum() or ch in "-_").strip("-_")


def short_uid(uid):
    """展示名用的截短 uid —— 与棱镜 shortUID 同口径。

    sub 有 42 字符，直接当昵称会把账号表撑开。取**尾部**：sub 是 UUIDv7
    （pu-<时间戳>-<随机位>），同期账号前缀相同，按前缀截会让两个账号同名。
    """
    if len(uid) <= 12:
        return uid
    tail = uid.rsplit("-", 1)[-1]
    return tail if len(tail) >= 6 else uid[-12:]


def uid_hint(path, doc):
    """uid 的候选值：原始 uid → JWT 的 sub → 父目录名 → 文件名 → 桌面端固定值。

    桌面端导出的 auth.json 通常不带 uid（或统一写死 raccoon-desktop），
    直接沿用会让多个账号在池子里塌成一个。**先按 JWT 的 sub 推导**，与棱镜
    同口径；推不出来（老 token、非 JWT）才退到目录名/文件名兜底。
    两者都是 config/auth 这种通用名时，退回 raccoon-desktop —— 与棱镜
    对桌面端凭证的默认口径一致，重复导入才会命中「更新已有」而不是新建。
    """
    raw = doc.get("uid") or ""
    if raw and raw != DESKTOP_UID:
        return raw
    derived = uid_from_token(doc.get("access_token") or "")
    if derived:
        return derived
    parent = os.path.basename(os.path.dirname(os.path.abspath(path)))
    stem = os.path.splitext(os.path.basename(path))[0]
    for candidate in (parent, stem):
        if candidate and candidate.lower() not in ("config", "auth", ".box-agent"):
            return candidate
    return raw or DESKTOP_UID


# ---------------------------------------------------------------- 子命令

def cmd_list(console, _args):
    accounts = console.accounts()
    if not accounts:
        print("账号池是空的。用 add 或 import 导入。")
        return 0
    usable = sum(1 for a in accounts if not is_off(a) and not a.get("cooling"))
    stopped = sum(1 for a in accounts if is_off(a))
    print("共 %d 个账号：可用 %d / 停用 %d" % (len(accounts), usable, stopped))
    print()
    for acc in accounts:
        print(describe(acc))
    if usable > 1:
        print("\n当前有多个可用账号，容器会挑积分最高的那个。要指定用哪个，跑 switch <账号>。")
    return 0


def cmd_switch(console, args):
    accounts = console.accounts()
    if not accounts:
        raise SystemExit("账号池是空的，没什么可切换。")
    target = pick_account(accounts, args.target)
    changed = 0
    for acc in accounts:
        if acc.get("uid") == target.get("uid"):
            continue
        if not is_off(acc):
            console.patch(acc.get("uid"), False)
            changed += 1
            print("  停用 %s（%s）" % (acc.get("nickname") or acc.get("uid"), acc.get("uid")))
    if is_off(target):
        console.patch(target.get("uid"), True)
        print("  启用 %s（%s）" % (target.get("nickname") or target.get("uid"), target.get("uid")))
    print("\n已独占：%s（%s）" % (target.get("nickname") or target.get("uid"), target.get("uid")))
    if changed == 0 and not is_off(target):
        print("（本来就是独占状态，没有改动）")
    return 0


def cmd_all_on(console, _args):
    accounts = console.accounts()
    if not accounts:
        raise SystemExit("账号池是空的。")
    n = 0
    for acc in accounts:
        if is_off(acc):
            console.patch(acc.get("uid"), True)
            n += 1
            print("  启用 %s" % (acc.get("nickname") or acc.get("uid")))
    print("\n已恢复自动选号（全部启用）。" if n else "所有账号本来就是启用的。")
    return 0


def cmd_toggle(console, args, enabled):
    accounts = console.accounts()
    target = pick_account(accounts, args.target)
    if is_off(target) == (not enabled):
        state = "启用" if enabled else "停用"
        print("%s 已经是「%s」状态，无需改动。" % (target.get("nickname"), state))
        return 0
    console.patch(target.get("uid"), enabled)
    print("已%s %s（%s）" % ("启用" if enabled else "停用",
                          target.get("nickname") or target.get("uid"), target.get("uid")))
    return 0


def cmd_on(console, args):
    return cmd_toggle(console, args, True)


def cmd_off(console, args):
    return cmd_toggle(console, args, False)


def cmd_rm(console, args):
    accounts = console.accounts()
    target = pick_account(accounts, args.target)
    if not args.yes:
        answer = input("确定删除 %s（%s）？凭据会被移除，需要重新导入。[y/N] "
                       % (target.get("nickname") or target.get("uid"), target.get("uid")))
        if answer.strip().lower() not in ("y", "yes"):
            print("已取消。")
            return 1
    console.delete(target.get("uid"))
    print("已删除 %s" % target.get("uid"))
    return 0


def cmd_add(console, args):
    accounts = console.accounts()
    taken = {a.get("uid") for a in accounts}
    # 有 sub 就用它当 uid（与网页导入、scan 同一口径），--name 只决定展示名。
    # 没有 sub 才退回按名字编号，避免两个账号都落到 raccoon-desktop 互相覆盖。
    uid = uid_from_token(args.token) or unique_uid(args.name or "", len(taken) + 1, taken)
    doc = {
        "uid": uid,
        "nickname": args.name or uid,
        "access_token": args.token,
    }
    if args.refresh:
        doc["refresh_token"] = args.refresh
    result = console.import_raw(doc)
    action = result.get("action", "ok")
    if action == "skipped_existing_newer":
        print("跳过：容器里已有该账号更新的凭证（uid=%s）。" % result.get("uid", uid))
    else:
        print("已导入 %s（uid=%s，%s）" % (doc["nickname"], result.get("uid", uid), action))
    if not args.refresh:
        print("注意：没给 refresh_token，这个账号无法自动续期。")
    return 0


def cmd_import(console, args):
    if args.file:
        with open(args.file, "r", encoding="utf-8") as fh:
            text = fh.read()
    else:
        text = sys.stdin.read()
    entries = split_entries(text)
    if not entries:
        raise SystemExit("没读到任何账号。")
    accounts = console.accounts()
    taken = {a.get("uid") for a in accounts}
    ok = skip = fail = 0
    for i, entry in enumerate(entries, start=1):
        try:
            note, access, refresh = parse_entry(entry, i)
        except ValueError as exc:
            print("  [跳过] %s" % exc)
            fail += 1
            continue
        uid = unique_uid(note, i, taken)
        doc = {"uid": uid, "nickname": note or uid, "access_token": access}
        if refresh:
            doc["refresh_token"] = refresh
        try:
            result = console.import_raw(doc)
        except urllib.error.HTTPError as exc:
            print("  [失败] %s：HTTP %s %s" % (note or uid, exc.code,
                                              exc.read().decode("utf-8", "replace")[:120]))
            fail += 1
            continue
        if result.get("action") == "skipped_existing_newer":
            print("  [跳过] %s：容器里已有更新的凭证" % (note or uid))
            skip += 1
        else:
            print("  [导入] %s（uid=%s）" % (note or uid, result.get("uid", uid)))
            ok += 1
    print("\n完成：导入 %d / 跳过 %d / 失败 %d" % (ok, skip, fail))
    return 0


def cmd_scan(console, args):
    """扫描目录里的桌面端登录态，批量收进账号池。

    桌面端一次只保存一个登录态，所以「多账号」通常体现为多份手工导出的
    auth.json。这个命令把它们一次性识别出来，按 access_token 去重，
    再用目录名/文件名补 uid，避免全部塌成 raccoon-desktop。
    """
    root = args.dir or default_auth_root()
    if not os.path.isdir(root):
        raise SystemExit(
            "目录不存在：%s\n用 scan <目录> 指定登录态所在位置（默认 %s）。"
            % (root, default_auth_root())
        )

    files = collect_json_files(root, args.depth)
    if not files:
        raise SystemExit("在 %s 下没找到 .json 文件（深度 %d）。" % (root, args.depth))

    print("扫描 %s" % root)
    print("发现 %d 个 JSON 文件，深度上限 %d\n" % (len(files), args.depth))

    existing = console.accounts()
    existing_uids = {a.get("uid") for a in existing}

    found = []
    skipped = []
    seen_access = {}
    taken_in_scan = set()
    for index, path in enumerate(files, start=1):
        try:
            with open(path, "r", encoding="utf-8", errors="replace") as fh:
                raw = fh.read()
        except OSError as exc:
            skipped.append((path, "读不了：%s" % exc))
            continue

        doc, err = parse_auth_doc(raw)
        if err:
            skipped.append((path, err))
            continue

        fingerprint = doc["access_token"]
        if fingerprint in seen_access:
            skipped.append((path, "与 %s 是同一份凭证" % short(seen_access[fingerprint], root)))
            continue
        seen_access[fingerprint] = path

        hint = uid_hint(path, doc)
        base = slugify(hint) or ("%02d" % index)
        # 只在本次扫描内部去重：同一批里两个目录撞名才加序号。
        # 与池里已有 uid 相同不算冲突 —— 那是同一个账号，导入即覆盖更新，
        # 否则每扫一次就多出一份 A-2、A-3。
        uid = base
        n = 1
        while uid in taken_in_scan:
            n += 1
            uid = "%s-%d" % (base, n)
        taken_in_scan.add(uid)

        doc["uid"] = uid
        if not doc["nickname"] or doc["nickname"] == DESKTOP_UID:
            doc["nickname"] = short_uid(hint) if hint != DESKTOP_UID else uid
        doc["_source"] = path
        doc["_already"] = uid in existing_uids
        found.append(doc)

    if not found:
        print("没有可导入的凭证。")
        report_skipped(skipped, root)
        return 1

    print("可导入 %d 个：" % len(found))
    for doc in found:
        rt = "有 refresh" if doc.get("refresh_token") else "无 refresh（不会自动续期）"
        mark = "（更新已有）" if doc["_already"] else ""
        print("  %-14s %-12s %-24s %s" % (
            doc["uid"], doc["nickname"], rt, short(doc["_source"], root) + mark))

    if skipped:
        print()
        report_skipped(skipped, root)

    if args.dry_run:
        print("\n--dry-run：只预览，没有导入。确认无误后去掉 --dry-run 再跑一次。")
        return 0

    if not args.yes:
        answer = input("\n导入上面这 %d 个账号？[y/N] " % len(found))
        if answer.strip().lower() not in ("y", "yes"):
            print("已取消。")
            return 1

    print()
    ok = skip = fail = 0
    for doc in found:
        payload = {k: v for k, v in doc.items() if not k.startswith("_")}
        try:
            result = console.import_raw(payload)
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace")[:120]
            print("  [失败] %s：HTTP %s %s" % (doc["nickname"], exc.code, detail))
            fail += 1
            continue
        if result.get("action") == "skipped_existing_newer":
            print("  [跳过] %s：池里已有更新的凭证" % doc["nickname"])
            skip += 1
        else:
            label = "更新" if result.get("action") == "updated" else "新增"
            print("  [%s] %s（uid=%s）" % (label, doc["nickname"], result.get("uid", doc["uid"])))
            ok += 1

    print("\n完成：导入 %d / 跳过 %d / 失败 %d" % (ok, skip, fail))
    if ok:
        print("用 list 看账号池，switch <账号> 切换。")
    return 0


def short(path, root):
    """把绝对路径压成相对 root 的形式，输出更短。"""
    try:
        rel = os.path.relpath(path, root)
    except ValueError:
        return path
    return path if rel.startswith("..") else rel


def report_skipped(skipped, root):
    """跳过项分类输出。

    config 目录里混着 model-profiles.json / mcp.system.json 这类配置，
    它们同样「缺少 access_token」。逐个列出来会把真正的问题淹掉，
    所以这一整类只报数量，其余（JSON 损坏、读不了）才逐条列出。
    """
    plain = [(p, r) for p, r in skipped if r == "缺少 access_token"]
    others = [(p, r) for p, r in skipped if r != "缺少 access_token"]
    if plain:
        names = [short(p, root) for p, _ in plain]
        head = "、".join(names[:3])
        if len(names) > 3:
            head += " 等"
        print("另有 %d 个 JSON 不是凭证（缺少 access_token）：%s" % (len(plain), head))
    for path, reason in others:
        print("  %s —— %s" % (short(path, root), reason))


def safe_dir_name(text, fallback):
    """把昵称转成可用的目录名。

    中文要保留（用户就是要看「账号A」这种名字），只清掉 Windows 不允许的
    字符和结尾的空格/点。
    """
    name = re.sub(r'[<>:"/\\|?*\x00-\x1f]', "_", text or "").strip(" .")
    return name[:60] or fallback


def cmd_export(_console, args):
    """把池里的账号反向导出成 <目录>/<昵称>/auth.json。

    控制台接口是脱敏的，拿不到 token，所以这里直接读 sidecar 落盘的
    runtime/raccoon/auths/raccoon-<uid>.json。导出的形状与桌面端 auth.json
    一致，因此既能当备份，也能被 scan 原样再导入回来。

    注意：导出的是明文凭据，脚本本身不打印 token，但文件是明文。
    """
    auth_dir = args.auth_dir or os.path.join(
        PROJECT_ROOT, "runtime", "raccoon", "auths")
    if not os.path.isdir(auth_dir):
        raise SystemExit("凭据目录不存在：%s\n用 --auth-dir 指定。" % auth_dir)

    files = sorted(glob.glob(os.path.join(auth_dir, "raccoon-*.json")))
    if not files:
        raise SystemExit("%s 下没有 raccoon-*.json。" % auth_dir)

    out_root = args.dir or os.path.join(PROJECT_ROOT, "raccoon-export")

    print("从 %s 读取 %d 个凭据文件" % (auth_dir, len(files)))
    print("导出到 %s\n" % out_root)

    ok = skip = fail = 0
    used_names = set()
    for path in files:
        try:
            with open(path, "r", encoding="utf-8", errors="replace") as fh:
                raw = fh.read()
        except OSError as exc:
            print("  [失败] %s：读不了（%s）" % (os.path.basename(path), exc))
            fail += 1
            continue

        doc, err = parse_auth_doc(raw)
        if err:
            print("  [失败] %s：%s" % (os.path.basename(path), err))
            fail += 1
            continue

        uid = doc.get("uid") or os.path.splitext(
            os.path.basename(path))[0].replace("raccoon-", "", 1)
        name = safe_dir_name(doc.get("nickname"), uid)
        # 昵称重名时加序号，避免两个账号写进同一个目录互相覆盖
        base = name
        n = 1
        while name in used_names:
            n += 1
            name = "%s-%d" % (base, n)
        used_names.add(name)

        target_dir = os.path.join(out_root, name)
        target = os.path.join(target_dir, "auth.json")
        if os.path.exists(target) and not args.force:
            print("  [跳过] %s → 已存在（加 --force 覆盖）" % name)
            skip += 1
            continue

        payload = {
            "uid": uid,
            "nickname": doc.get("nickname") or uid,
            "access_token": doc["access_token"],
            "refresh_token": doc.get("refresh_token", ""),
        }
        if doc.get("expires_at") not in (None, "", 0):
            payload["expires_at"] = doc["expires_at"]

        try:
            os.makedirs(target_dir, mode=0o700, exist_ok=True)
            with open(target, "w", encoding="utf-8") as fh:
                json.dump(payload, fh, ensure_ascii=False, indent=2)
                fh.write("\n")
            try:
                os.chmod(target, 0o600)
            except OSError:
                pass
        except OSError as exc:
            print("  [失败] %s：写不了（%s）" % (name, exc))
            fail += 1
            continue

        rt = "含 refresh" if payload.get("refresh_token") else "无 refresh"
        print("  [导出] %-14s → %s\\auth.json（%s）" % (name, name, rt))
        ok += 1

    print("\n完成：导出 %d / 跳过 %d / 失败 %d" % (ok, skip, fail))
    if ok:
        print("这批文件可以直接用 scan 再导入回来：python scripts/raccoon-ctl.py scan %s" % out_root)
        print("注意：auth.json 里是明文凭据，别提交到仓库、别放网盘。")
    return 0


def cmd_refresh(console, args):
    accounts = console.accounts()
    target = pick_account(accounts, args.target)
    console.refresh(target.get("uid"))
    print("已刷新 %s 的令牌。" % (target.get("nickname") or target.get("uid")))
    return 0


def cmd_checkin(console, args):
    """立即跑一轮积分链路（或 token 预刷新）。

    小浣熊没有原生签到接口：sidecar 每天在固定时点（默认 9 点）跑
    「桌面登录奖励 → setting_info 触发 → 账单核对」。容器在那个小时没运行就
    会漏掉一整天，所以需要这个手动补跑的入口。逐账号访问上游，会慢一些。
    """
    path = "schedule/refresh" if args.refresh else "schedule/checkin"
    label = "token 预刷新" if args.refresh else "积分链路"
    print("正在跑一轮%s（逐账号访问上游，可能要几十秒）…" % label)
    result = console.post(path, {})
    processed = result.get("processed")
    if processed is None:
        processed = result.get("count")
    if processed is not None:
        print("完成：处理 %s 个账号。" % processed)
    else:
        print("完成。")
    if result.get("error"):
        print("有账号失败：%s" % result["error"])
    print("用 list 看积分，或到控制台点「刷新积分」核对。")
    return 0


# ---------------------------------------------------------------- 入口

def main():
    parser = argparse.ArgumentParser(
        description="棱镜网关小浣熊账号管理",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("--base", default=os.environ.get("WB2A_CONSOLE_URL", DEFAULT_BASE),
                        help="控制台地址，默认 %s" % DEFAULT_BASE)
    parser.add_argument("--key", default="", help="管理员密钥；不填则自动查找")
    sub = parser.add_subparsers(dest="cmd", required=True)

    sub.add_parser("list", help="列出账号池").set_defaults(func=cmd_list)
    sub.add_parser("all-on", help="全部启用，恢复自动选号").set_defaults(func=cmd_all_on)

    p = sub.add_parser("switch", help="独占切换：只留目标账号可用")
    p.add_argument("target")
    p.set_defaults(func=cmd_switch)

    p = sub.add_parser("on", help="启用某个账号")
    p.add_argument("target")
    p.set_defaults(func=cmd_on)

    p = sub.add_parser("off", help="停用某个账号")
    p.add_argument("target")
    p.set_defaults(func=cmd_off)

    p = sub.add_parser("rm", help="删除某个账号")
    p.add_argument("target")
    p.add_argument("-y", "--yes", action="store_true", help="不询问直接删")
    p.set_defaults(func=cmd_rm)

    p = sub.add_parser("refresh", help="强制刷新某个账号的令牌")
    p.add_argument("target")
    p.set_defaults(func=cmd_refresh)

    p = sub.add_parser("add", help="添加一个账号")
    p.add_argument("--name", default="", help="备注名，也用于生成 uid")
    p.add_argument("--token", required=True, help="access_token")
    p.add_argument("--refresh", default="", help="refresh_token（强烈建议提供）")
    p.set_defaults(func=cmd_add)

    p = sub.add_parser("import", help="批量导入（备注|token|refresh_token 每行一条）")
    p.add_argument("file", nargs="?", help="输入文件；省略则读 stdin")
    p.set_defaults(func=cmd_import)

    p = sub.add_parser("scan", help="扫描桌面端登录态目录，自动识别并批量导入")
    p.add_argument("dir", nargs="?", default="", help="目录；默认 %s" % default_auth_root())
    p.add_argument("--depth", type=int, default=4, help="递归深度，默认 4")
    p.add_argument("--dry-run", action="store_true", help="只预览，不导入")
    p.add_argument("-y", "--yes", action="store_true", help="不询问直接导入")
    p.set_defaults(func=cmd_scan)

    p = sub.add_parser("checkin", help="立即跑一轮积分链路（不用等到每日 9 点）")
    p.add_argument("--refresh", action="store_true", help="改跑 token 预刷新而不是积分")
    p.set_defaults(func=cmd_checkin)

    p = sub.add_parser("export", help="导出全部账号为 <目录>/<昵称>/auth.json 备份")
    p.add_argument("dir", nargs="?", default="", help="输出目录，默认 <项目>/raccoon-export")
    p.add_argument("--auth-dir", default="", help="凭据目录，默认 runtime/raccoon/auths")
    p.add_argument("--force", action="store_true", help="覆盖已存在的导出文件")
    p.set_defaults(func=cmd_export, offline=True)

    args = parser.parse_args()

    # export 只读磁盘上的凭据文件，不碰控制台，所以不需要登录。
    if getattr(args, "offline", False):
        return args.func(None, args)

    args = parser.parse_args()

    key = find_admin_key(args.key, PROJECT_ROOT)
    if not key:
        print("找不到管理员密钥。用 --key 指定，或设置 WB2A_ADMIN_KEY，\n"
              "或确认项目里有 runtime/wb2api/keys/keys.json。", file=sys.stderr)
        return 1

    console = Console(args.base, key)
    try:
        console.login()
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:160]
        print("登录失败（HTTP %s）：%s\n检查控制台地址与管理员密钥。" % (exc.code, detail),
              file=sys.stderr)
        return 1
    except urllib.error.URLError as exc:
        print("连不上控制台 %s：%s\n容器起来了吗？" % (args.base, exc.reason), file=sys.stderr)
        return 1

    return args.func(console, args)


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except AttributeError:
        pass
    sys.exit(main())
