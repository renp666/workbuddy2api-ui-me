#!/usr/bin/env python3
"""AutoClaw 每日签到工具（独立维护，仅标准库）。

签到端点（桌面端 1.18.5 bundle 逆向，2026-09-22）：
  POST {region基址}/autoclaw-proxy/proxy/autoclaw-task-complete
  body: {"task_id": "daily_signin"}
  幂等：data.already_completed=true 表示今日已签。

用法：
  python3 check.py                  # 全部账号签到
  python3 check.py --region cn      # 仅国内
  python3 check.py --region global  # 仅海外
  python3 check.py --status         # 只查任务列表，不签到
  python3 check.py --dry-run        # 只打印将要发生的请求，不发送
  python3 check.py --json           # 机器可读输出
"""
from __future__ import annotations

import argparse
import glob
import hashlib
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request
import uuid

APP_ID = "100003"
APP_SECRET = "38d2391985e2369a5fb8227d8e6cd5e5"
CLIENT_VER = "1.18.5"
TASK_ID = "daily_signin"
USERAPI_BASE = {
    "cn": "https://autoglm-acceleration-api.zhipuai.cn",
    "global": "https://autoglm-api.autoglm.ai",
}
PATH_TASK_COMPLETE = "/autoclaw-proxy/proxy/autoclaw-task-complete"
PATH_TASK_LIST = "/autoclaw-proxy/proxy/autoclaw-task-list"
DEFAULT_AUTH_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "auths")


def sign(ts: str) -> str:
    return hashlib.md5(f"{APP_ID}&{ts}&{APP_SECRET}".encode()).hexdigest()


def bearer(token: str) -> str:
    t = token.strip()
    return t if t.lower().startswith("bearer ") else "Bearer " + t


def build_headers(token: str) -> dict:
    ts = str(int(time.time()))
    return {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "X-Version": CLIENT_VER,
        "X-Tm": "mac",
        "X-Product": "autoclaw",
        "X-Client-Type": "pc",
        "X-Channel": "official",
        "X-Auth-Appid": APP_ID,
        "X-Auth-TimeStamp": ts,
        "X-Auth-Sign": sign(ts),
        "X-Trace-Id": str(uuid.uuid4()),
        "X-Lang": "en",
        "Authorization": bearer(token),
        "User-Agent": f"AutoClaw/{CLIENT_VER}",
    }


def load_auths(auth_dir: str, region: str) -> list:
    out = []
    for path in sorted(glob.glob(os.path.join(auth_dir, "autoclaw-*.json"))):
        try:
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
        except Exception:
            continue
        r = doc.get("region") or "cn"
        if region != "all" and r != region:
            continue
        token = (doc.get("accessToken") or "").strip()
        if not token:
            continue
        out.append({
            "path": path,
            "region": r,
            "user_id": str(doc.get("userId") or os.path.basename(path)),
            "user_name": doc.get("userName") or "",
            "token": token,
        })
    return out


def http_json(method: str, url: str, token: str, body: dict | None, timeout: float):
    data = None
    headers = build_headers(token)
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    ctx = ssl.create_default_context()
    try:
        with urllib.request.urlopen(req, timeout=timeout, context=ctx) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read()
        status = e.code
    except Exception as e:
        return None, 0, str(e)
    try:
        parsed = json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        return None, status, raw.decode("utf-8", "replace")[:300]
    return parsed, status, ""


def checkin(acct: dict, dry_run: bool, timeout: float) -> dict:
    base = USERAPI_BASE[acct["region"]]
    url = base + PATH_TASK_COMPLETE
    result = {
        "uid": acct["user_id"],
        "region": acct["region"],
        "url": url,
        "task_id": TASK_ID,
        "ok": False,
        "already_completed": False,
        "reward_points": 0,
        "code": None,
        "msg": "",
        "error": "",
    }
    if dry_run:
        result["msg"] = "dry-run"
        result["ok"] = True
        return result
    payload, status, err = http_json("POST", url, acct["token"], {"task_id": TASK_ID}, timeout)
    if err and payload is None:
        result["error"] = f"http {status}: {err}"
        return result
    if not isinstance(payload, dict):
        result["error"] = f"http {status}: non-json response"
        return result
    code = payload.get("code")
    result["code"] = code
    result["msg"] = str(payload.get("msg") or "")
    data = payload.get("data") if isinstance(payload.get("data"), dict) else {}
    if status >= 400 and code is None:
        result["error"] = f"http {status}: {result['msg'] or payload}"
        return result
    # code=0 成功；code=null 且有 data 也算成功（与桌面端 completeClientTask 一致）
    ok = code == 0 or (code is None and bool(data))
    if not ok:
        result["error"] = result["msg"] or f"code={code}"
        return result
    result["ok"] = True
    result["already_completed"] = bool(data.get("already_completed"))
    rp = data.get("reward_points")
    result["reward_points"] = int(rp) if isinstance(rp, (int, float)) else 0
    if data.get("task_id"):
        result["task_id"] = str(data.get("task_id"))
    return result


def task_list(acct: dict, dry_run: bool, timeout: float) -> dict:
    base = USERAPI_BASE[acct["region"]]
    url = base + PATH_TASK_LIST + "?lang=en"
    result = {
        "uid": acct["user_id"],
        "region": acct["region"],
        "url": url,
        "ok": False,
        "tasks": [],
        "error": "",
    }
    if dry_run:
        result["error"] = "dry-run"
        return result
    payload, status, err = http_json("GET", url, acct["token"], None, timeout)
    if err and payload is None:
        result["error"] = f"http {status}: {err}"
        return result
    if not isinstance(payload, dict):
        result["error"] = f"http {status}: non-json"
        return result
    code = payload.get("code")
    data = payload.get("data")
    if code not in (0, None):
        result["error"] = str(payload.get("msg") or f"code={code}")
        return result
    result["ok"] = True
    if isinstance(data, list):
        result["tasks"] = data
    elif isinstance(data, dict):
        result["tasks"] = data.get("tasks") or data.get("list") or [data]
    return result


def main() -> int:
    ap = argparse.ArgumentParser(description="AutoClaw daily check-in")
    ap.add_argument("--auths", default=DEFAULT_AUTH_DIR, help="凭证目录（默认 ./auths）")
    ap.add_argument("--region", choices=["cn", "global", "all"], default="all")
    ap.add_argument("--status", action="store_true", help="只查询任务列表，不签到")
    ap.add_argument("--dry-run", action="store_true", help="不发送请求，只打印计划")
    ap.add_argument("--json", action="store_true", help="JSON 输出")
    ap.add_argument("--timeout", type=float, default=20.0)
    args = ap.parse_args()

    auths = load_auths(args.auths, args.region)
    if not auths:
        print(f"no accounts in {args.auths} (region={args.region})", file=sys.stderr)
        return 1

    results = []
    for acct in auths:
        if args.status:
            r = task_list(acct, args.dry_run, args.timeout)
        else:
            r = checkin(acct, args.dry_run, args.timeout)
        results.append(r)

    if args.json:
        print(json.dumps({"results": results}, ensure_ascii=False, indent=2))
    else:
        for r in results:
            uid = r["uid"][:16]
            if args.status:
                if r["ok"]:
                    n = len(r.get("tasks") or [])
                    print(f"[{r['region']:6s}] {uid:16s} task-list ok ({n} items)  {r['url']}")
                else:
                    print(f"[{r['region']:6s}] {uid:16s} task-list FAIL: {r['error']}")
            else:
                if not r["ok"]:
                    print(f"[{r['region']:6s}] {uid:16s} FAIL: {r['error']}")
                elif r["already_completed"]:
                    print(f"[{r['region']:6s}] {uid:16s} already checked in today")
                elif r["msg"] == "dry-run":
                    print(f"[{r['region']:6s}] {uid:16s} DRY-RUN -> {r['url']} task_id={r['task_id']}")
                else:
                    print(f"[{r['region']:6s}] {uid:16s} checked in, +{r['reward_points']} points")
    return 0 if all(r["ok"] for r in results) else 2


if __name__ == "__main__":
    sys.exit(main())
