#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import random
import re
import socket
import threading
import time
import uuid
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Optional

import httpx

APP_ID = "100003"
APP_SECRET = "38d2391985e2369a5fb8227d8e6cd5e5"
CLIENT_VER = "1.18.5"
USERAPI_BASE = {
    "cn": "https://autoglm-acceleration-api.zhipuai.cn",
    "global": "https://autoglm-api.autoglm.ai",
}
PROXY_CHAT_PATH = "/autoclaw-proxy/proxy/autoclaw/v1/chat/completions"
PROXY_MODELS_PATH = "/autoclaw-proxy/proxy/autoclaw/v1/models"
CONFIG_MODELS_PATH = "/autoclaw-proxy/proxy/autoclaw-model-config"

GLOBAL_STATIC_MODELS = [
    "zai_auto",
    "zai_auto-fast",
    "zaicoding_glm-5.3",
    "tdpsk_deepseek-v4-flash-202605",
    "tdpsk_deepseek-v4-pro-202606",
    "zai_glm-5.3-flash",
]
CN_STATIC_MODELS = [
    "openclaw",
    "openclaw/default",
    "glm-5.3-flash",
    "glm-5.2",
    "zai_auto",
    "zai_auto-fast",
    "zai_glm-5.3-flash",
    "zaicoding_glm-5.3",
    "tdpsk_deepseek-v4-flash-202605",
    "tdpsk_deepseek-v4-pro-202606",
]

HARD_MARKERS = [
    "insufficient credit", "no credit", "credit exhausted", "out of credit",
    "quota exceeded", "quota exhaust", "payment required", "not enough credit",
    "insufficient balance", "balance not enough", "limit reached", "usage limit",
    "余额不足", "额度不足", "积分不足", "积分用完", "额度用尽", "没有积分", "配额不足", "可用额度",
]
DEAD_MARKERS = [
    "not logged in", "unauthorized", "invalid token", "token expired",
    "login required", "session expired", "offline user session", "auth.inject failed",
    "未登录", "登录已过期", "请先登录", "token 失效", "invalid token",
]


class UpstreamError(Exception):
    def __init__(self, kind: str, status: int = 0, code: int = 0, msg: str = ""):
        super().__init__(f"upstream {kind} (http {status} code {code}): {msg}")
        self.kind = kind
        self.status = status
        self.code = code
        self.msg = msg


def classify(status: int, code: int, body: str) -> str:
    if code == 410000:
        return "session_dead"
    lower = body.lower()
    for m in HARD_MARKERS:
        if m.lower() in lower or m in body:
            return "hard_credit"
    for m in DEAD_MARKERS:
        if m.lower() in lower:
            return "session_dead"
    if status == 402:
        return "hard_credit"
    if status == 429:
        return "soft_rate"
    if status == 404:
        return "not_found"
    if status >= 500:
        return "server"
    if status >= 400:
        return "client"
    return "none"


def sign(ts: str) -> str:
    return hashlib.md5(f"{APP_ID}&{ts}&{APP_SECRET}".encode()).hexdigest()


def bearer(token: str) -> str:
    t = token.strip()
    return t if t.lower().startswith("bearer ") else "Bearer " + t


def strip_bearer(token: str) -> str:
    t = token.strip()
    return t[7:] if t.lower().startswith("bearer ") else t


def new_trace_id() -> str:
    return uuid.uuid4().hex


def userapi_headers(token: str, region: str) -> dict:
    ts = str(int(time.time()))
    h = {
        "Content-Type": "application/json",
        "Accept": "*/*",
        "X-Version": "1.12.1",
        "X-Tm": "web",
        "X-Product": "autoclaw",
        "X-Client-Type": "web",
        "X-Channel": "official",
        "X-Auth-Appid": APP_ID,
        "X-Auth-TimeStamp": ts,
        "X-Auth-Sign": sign(ts),
        "X-Trace-Id": new_trace_id(),
        "X-Lang": "en",
    }
    if token:
        h["Authorization"] = bearer(token)
    return h


def proxy_chat_headers(token: str, model: str, region: str) -> dict:
    h = {
        "Accept": "application/json, text/plain, */*",
        "Authorization": bearer(token),
        "X-Authorization": bearer(token),
        "X-Request-Model": model,
        "X-Tm": "mac",
        "X-Version": CLIENT_VER,
        "X-Product": "autoclaw",
        "X-Channel": "official",
        "X-Lang": "zh-CN",
        "X-Client-Type": "pc",
        "X-Trace-Id": new_trace_id(),
        "x_trace_id": "autoclaw-bridge",
        "User-Agent": f"AutoClaw/{CLIENT_VER}",
    }
    return h


def relay_base(endpoint: str) -> str:
    t = endpoint.strip()
    low = t.lower()
    if low.startswith("ws://"):
        t = "http://" + t[5:]
    elif low.startswith("wss://"):
        t = "https://" + t[6:]
    suffix = "/autoclaw-cloud"
    if "://" not in t:
        t = t.rstrip("/")
        return t if t.endswith(suffix) else t + suffix
    try:
        from urllib.parse import urlsplit, urlunsplit
        sp = urlsplit(t)
        path = sp.path
        i = path.find(suffix)
        path = path[: i + len(suffix)] if i >= 0 else path.rstrip("/") + suffix
        return urlunsplit((sp.scheme, sp.netloc, path, "", "")).rstrip("/")
    except Exception:
        t = t.rstrip("/")
        return t if t.endswith(suffix) else t + suffix


def relay_proxy_base(endpoint: str, sandbox_id: str) -> str:
    base = relay_base(endpoint)
    return base if not sandbox_id else base.rstrip("/") + "/proxy/" + sandbox_id.strip()


def relay_base_from_proxy(proxy_base: str) -> str:
    p = proxy_base.rstrip("/")
    i = p.rfind("/proxy/")
    return p[:i] if i >= 0 else p


def to_ws_base(base: str) -> str:
    if base.startswith("https://"):
        return "wss://" + base[8:]
    if base.startswith("http://"):
        return "ws://" + base[7:]
    return base


@dataclass
class Auth:
    access_token: str
    refresh_token: str = ""
    expires_at: int = 0
    user_id: str = ""
    user_name: str = ""
    phone: str = ""
    device_id: str = ""
    region: str = "cn"
    sandbox_id: str = ""
    sandbox_endpoint: str = ""
    end_timestamp: int = 0
    file_path: str = ""
    lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    def needs_refresh(self, within: float) -> bool:
        if self.expires_at <= 0:
            return True
        return time.time() + within >= self.expires_at

    def save(self) -> None:
        with self.lock:
            if not self.access_token.strip():
                raise ValueError(f"save refused: empty accessToken (uid={self.user_id})")
            if not self.file_path:
                raise ValueError("no FilePath set")
            doc = {
                "accessToken": self.access_token,
                "refreshToken": self.refresh_token,
                "expiresAt": self.expires_at,
                "userId": self.user_id,
                "userName": self.user_name,
                "phone": self.phone,
                "deviceId": self.device_id,
                "region": self.region,
                "sandboxId": self.sandbox_id,
                "sandboxEndpoint": self.sandbox_endpoint,
                "endTimestamp": self.end_timestamp,
            }
            tmp = self.file_path + ".tmp"
            with open(tmp, "w") as f:
                json.dump(doc, f, indent=2, ensure_ascii=False)
            os.replace(tmp, self.file_path)


def parse_auth(raw: bytes) -> Auth:
    if not raw:
        raise ValueError("empty auth storage")
    f = json.loads(raw)
    a = Auth(
        access_token=f.get("accessToken", ""),
        refresh_token=f.get("refreshToken", ""),
        expires_at=int(f.get("expiresAt") or 0),
        user_id=str(f.get("userId") or ""),
        user_name=f.get("userName") or "",
        phone=f.get("phone") or "",
        device_id=f.get("deviceId") or "",
        region=f.get("region") or "cn",
        sandbox_id=f.get("sandboxId") or "",
        sandbox_endpoint=f.get("sandboxEndpoint") or "",
        end_timestamp=int(f.get("endTimestamp") or 0),
    )
    if not a.access_token.strip():
        raise ValueError("parse_error: missing accessToken")
    return a


def load_dir(directory: str, want_region: str) -> list:
    out = []
    if not os.path.isdir(directory):
        return out
    for name in sorted(os.listdir(directory)):
        if not (name.startswith("autoclaw-") and name.endswith(".json")):
            continue
        path = os.path.join(directory, name)
        try:
            with open(path, "rb") as f:
                a = parse_auth(f.read())
        except Exception:
            continue
        if a.region != want_region:
            continue
        a.file_path = path
        out.append(a)
    return out


@dataclass
class Entry:
    auth: Auth
    points: int = 0
    success_count: int = 0
    err_count: int = 0
    last_err: float = 0.0
    last_success: float = 0.0
    cool_kind: str = ""
    until: float = 0.0
    disabled: bool = False
    reason: str = ""
    last_used: float = 0.0

    def healthy(self, now: float) -> bool:
        if self.disabled:
            return False
        if self.until and now < self.until:
            return False
        return True


class Pool:
    def __init__(self, region: str, state_file: str = ""):
        self.region = region
        self.state_file = state_file
        self.by_uid: dict = {}
        self.lock = threading.RLock()
        self.dirty = False
        self._load()

    def _load(self) -> None:
        if not self.state_file or not os.path.exists(self.state_file):
            return
        try:
            with open(self.state_file) as f:
                sf = json.load(f)
        except Exception:
            return
        for uid, s in (sf.get("accounts") or {}).items():
            if s.get("region") and s["region"] != self.region:
                continue
            until = s.get("until") or 0
            if isinstance(until, str):
                try:
                    until = time.mktime(time.strptime(until[:19], "%Y-%m-%dT%H:%M:%S")) if until[:4] != "0001" else 0
                except Exception:
                    until = 0
            self.by_uid[uid] = Entry(
                auth=Auth(access_token="placeholder", user_id=uid, region=self.region),
                points=int(s.get("points") or 0),
                disabled=bool(s.get("disabled")),
                reason=s.get("reason") or "",
                until=float(until),
                cool_kind=str(s.get("cool_kind") or ""),
                success_count=int(s.get("success_count") or 0),
                err_count=int(s.get("err_count") or 0),
            )

    def flush(self) -> None:
        with self.lock:
            if not self.dirty or not self.state_file:
                return
            self.dirty = False
            accounts = {}
            for uid, e in self.by_uid.items():
                accounts[uid] = {
                    "region": self.region,
                    "points": e.points,
                    "disabled": e.disabled,
                    "reason": e.reason,
                    "until": e.until,
                    "cool_kind": e.cool_kind,
                    "success_count": e.success_count,
                    "err_count": e.err_count,
                }
            os.makedirs(os.path.dirname(self.state_file) or ".", exist_ok=True)
            tmp = self.state_file + ".tmp"
            with open(tmp, "w") as f:
                json.dump({"accounts": accounts}, f, indent=2, ensure_ascii=False)
            os.replace(tmp, self.state_file)

    def sync(self, auths: list) -> None:
        with self.lock:
            seen = set()
            for a in auths:
                seen.add(a.user_id)
                if a.user_id in self.by_uid:
                    self.by_uid[a.user_id].auth = a
                else:
                    self.by_uid[a.user_id] = Entry(auth=a)
            for uid in list(self.by_uid):
                if uid not in seen:
                    del self.by_uid[uid]
            self.dirty = True
            self.flush()

    def pick(self, tried: dict = None) -> Optional[Auth]:
        with self.lock:
            now = time.time()
            cands = [e for uid, e in self.by_uid.items() if not (tried and uid in tried) and e.healthy(now)]
            if not cands:
                return None
            cands.sort(key=lambda e: (-e.points, e.auth.user_id))
            cands = cands[:5]
            eligible = [e for e in cands if now - e.last_used >= 0.1]
            if not eligible:
                e = min(cands, key=lambda x: x.last_used)
            else:
                total = sum(e.points for e in eligible)
                if total <= 0:
                    e = random.choice(eligible)
                else:
                    r = random.randrange(total)
                    acc = 0
                    e = eligible[-1]
                    for c in eligible:
                        acc += c.points
                        if r < acc:
                            e = c
                            break
            e.last_used = time.time()
            return e.auth

    def set_points(self, uid: str, points: int) -> None:
        with self.lock:
            if uid in self.by_uid:
                self.by_uid[uid].points = points
                self.dirty = True

    def cooldown(self, uid: str, kind: str, seconds: float, reason: str) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.until = time.time() + seconds
                e.cool_kind = kind
                e.reason = reason
                e.err_count = 0
                self.dirty = True

    def disable(self, uid: str, reason: str) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.disabled = True
                e.reason = reason
                self.dirty = True

    def reenable(self, uid: str) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.disabled = False
                e.until = 0.0
                e.cool_kind = ""
                e.reason = ""
                e.err_count = 0
                self.dirty = True

    def reenable_if_points(self, uid: str, points: int) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.points = points
                if not e.disabled:
                    e.until = 0.0
                    e.cool_kind = ""
                    e.reason = ""
                    e.err_count = 0
                self.dirty = True

    def note_error(self, uid: str, threshold: int, seconds: float) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.err_count += 1
                e.last_err = time.time()
                if e.err_count >= threshold:
                    e.until = time.time() + seconds
                    e.cool_kind = "error_threshold"
                    e.reason = "consecutive errors"
                    e.err_count = 0
                self.dirty = True

    def note_success(self, uid: str) -> None:
        with self.lock:
            if uid in self.by_uid:
                e = self.by_uid[uid]
                e.success_count += 1
                e.last_success = time.time()
                e.err_count = 0
                self.dirty = True

    def counts(self):
        with self.lock:
            now = time.time()
            total = len(self.by_uid)
            healthy = sum(1 for e in self.by_uid.values() if e.healthy(now))
            cooling = sum(1 for e in self.by_uid.values() if not e.disabled and e.until and now < e.until)
            disabled = sum(1 for e in self.by_uid.values() if e.disabled)
            return total, healthy, cooling, disabled

    def status_list(self) -> list:
        with self.lock:
            now = time.time()
            out = []
            for uid in sorted(self.by_uid):
                e = self.by_uid[uid]
                cooling = bool(e.until and now < e.until) and not e.disabled
                out.append({
                    "uid": uid,
                    "region": self.region,
                    "nickname": e.auth.user_name,
                    "points": e.points,
                    "cooling": cooling,
                    "cool_kind": e.cool_kind if cooling else "",
                    "cool_remaining_sec": max(0, int(e.until - now)) if cooling else 0,
                    "reason": e.reason,
                    "disabled": e.disabled,
                    "success_count": e.success_count,
                    "err_count": e.err_count,
                })
            return out

    def auth_by_uid(self, uid: str) -> Optional[Auth]:
        with self.lock:
            e = self.by_uid.get(uid)
            return e.auth if e else None


class Upstream:
    ZAI_PROXY_CHAT_URL = "https://autoglm-api.autoglm.ai/autoclaw-proxy/proxy/autoclaw/chat/completions"

    def __init__(self, timeout: float = 120.0, user_api_override: str = ""):
        self.http = httpx.Client(timeout=timeout, limits=httpx.Limits(max_keepalive_connections=20),
                                 follow_redirects=True)  # 上游 userapi 偶发 307 重定向，默认不跟随会导致 parse failed
        self.user_api_override = user_api_override.rstrip("/")
        self._jwt_cache: dict = {}

    def _zai_jwt(self, region: str) -> str:
        """取该区账号池内最新 iat 的 JWT（X-Authorization 用），带缓存。"""
        import base64 as _b64
        cached = self._jwt_cache.get(region)
        if cached and time.time() < cached.get("exp", 0) - 600:
            return cached["jwt"]
        auths = load_dir(os.path.join(os.path.dirname(os.path.abspath(__file__)), "auths"), region)
        best_jwt, best_iat, best_exp = "", -1, 0
        for a in auths:
            tok = a.access_token[7:] if a.access_token.lower().startswith("bearer ") else a.access_token
            try:
                p = tok.split(".")[1]
                p += "=" * (-len(p) % 4)
                payload = json.loads(_b64.urlsafe_b64decode(p))
                if payload.get("iat", 0) > best_iat:
                    best_iat = payload.get("iat", 0)
                    best_exp = payload.get("exp", 0)
                    best_jwt = tok
            except Exception:
                continue
        if not best_jwt:
            raise UpstreamError("client", 0, 0, f"no {region} JWT for zai-proxy channel")
        self._jwt_cache[region] = {"jwt": best_jwt, "exp": best_exp}
        return best_jwt

    def chat_zai_direct(self, route_model: str, body: bytes, region: str = "global"):
        """直连 autoclaw-proxy 远端（桌面端同款头，X-Harness-Type=zcode 豁免白名单）。"""
        jwt = self._zai_jwt(region)
        body_model = re.sub(r"^[a-z]+_", "", route_model)
        obj = json.loads(body)
        obj["model"] = body_model
        headers = {
            "Content-Type": "application/json",
            "Accept": "*/*",
            "X-Authorization": bearer(jwt),
            "X-Request-Id": str(uuid.uuid4()),
            "X-Request-Model": route_model,
            "X-Client-Type": "pc",
            "X-Product": "autoclaw",
            "X-Harness-Type": "zcode",
            "X-Tm": "mac",
            "X-Version": CLIENT_VER,
            "X-Lang": "zh-CN",
            "x_trace_id": "autoclaw-desktop",
            "X-Channel": "official",
            "X-Session-Id": f"webchat:autoclaw2api:{uuid.uuid4().hex[:12]}",
            "X-Agent-Id": "main",
            "User-Agent": f"AutoClaw/{CLIENT_VER} Electron/37.2.0",
        }
        resp = self.http.send(
            self.http.build_request("POST", self.ZAI_PROXY_CHAT_URL, content=json.dumps(obj).encode(), headers=headers),
            stream=True,
        )
        if resp.status_code >= 400:
            raw = resp.read()
            text = raw.decode("utf-8", "replace")
            resp.close()
            raise UpstreamError(classify(resp.status_code, 0, text), resp.status_code, 0, text[:200])
        return resp

    def base(self, region: str) -> str:
        return self.user_api_override or USERAPI_BASE[region]

    def _userapi(self, a: Optional[Auth], region: str, method: str, path: str, body: Any = None, token: str = ""):
        tok = token or (a.access_token if a else "")
        headers = userapi_headers(tok, region)
        resp = self.http.request(method, self.base(region) + path, json=body, headers=headers)
        raw = resp.text
        if resp.status_code >= 400:
            raise UpstreamError(classify(resp.status_code, 0, raw), resp.status_code, 0, raw[:200])
        try:
            env = resp.json()
        except Exception:
            raise UpstreamError("client", resp.status_code, 0, f"parse failed: {raw[:120]}")
        if env.get("code", 0) != 0:
            kind = classify(resp.status_code, int(env.get("code") or 0), env.get("msg") or "")
            if kind == "none":
                kind = "client"
            raise UpstreamError(kind, resp.status_code, int(env.get("code") or 0), f"code={env.get('code')} msg={str(env.get('msg'))[:160]}")
        return env.get("data")

    def send_code(self, region: str, device_id: str, phone: str) -> None:
        self._userapi(None, region, "POST", "/userapi/v1/agent-send-code",
                      {"source_id": "autoclaw", "device_id": device_id, "phone": phone})

    def login(self, region: str, device_id: str, phone: str, code: str) -> dict:
        data = self._userapi(None, region, "POST", "/userapi/v1/agent-login/", {
            "source_id": "autoclaw", "device_id": device_id,
            "phone": phone, "code": int(str(code).strip()), "platform": "web",
        })
        if not data or not data.get("access_token"):
            raise UpstreamError("client", 0, 0, "login_failed: no access_token")
        return data

    def refresh(self, a: Auth) -> None:
        with a.lock:
            if not a.refresh_token.strip():
                raise ValueError("no refreshToken")
            data = self._userapi(a, a.region, "POST", "/userapi/v1/refresh", {
                "source_id": "autoclaw", "device_id": a.device_id, "refresh_token": a.refresh_token,
            }, token=a.access_token)
            if not data or not data.get("access_token"):
                raise UpstreamError("session_dead", 401, 0, "refresh_failed: re-login required")
            a.access_token = data["access_token"]
            if data.get("refresh_token"):
                a.refresh_token = data["refresh_token"]

    def check_points(self, a: Auth) -> int:
        data = self._userapi(a, a.region, "GET", "/agent-assetmgr/api/v2/wallets?biz_app_id=autoclaw")
        tb = (data or {}).get("total_balance")
        return _num(tb)

    def list_sandboxes(self, a: Auth) -> list:
        data = self._userapi(a, a.region, "GET", "/agentdr/v2/assistant/sandbox/list")
        if isinstance(data, list):
            return data
        return (data or {}).get("sandbox_list") or []

    def apply_sandbox(self, a: Auth) -> None:
        self._userapi(a, a.region, "POST", "/agentdr/v2/assistant/sandbox/apply", {"sandbox_name": "default"})

    def ensure_sandbox(self, a: Auth):
        now = time.time()
        if a.sandbox_id and a.sandbox_endpoint and (not a.end_timestamp or a.end_timestamp > now):
            a.sandbox_endpoint = relay_proxy_base(relay_base(a.sandbox_endpoint), a.sandbox_id)
            return a.sandbox_id, a.sandbox_endpoint
        lst = self.list_sandboxes(a)
        if not lst:
            self.apply_sandbox(a)
            lst = self.list_sandboxes(a)
        if not lst:
            raise UpstreamError("server", 0, 0, "no sandbox available after apply")
        pick = lst[0]
        for s in lst:
            exp = s.get("end_timestamp") or 0
            pexp = pick.get("end_timestamp") or 0
            if (not exp or exp > now) and (not pick.get("sandbox_id") or (pexp and pexp <= now)):
                pick = s
        a.sandbox_id = pick.get("sandbox_id") or ""
        a.sandbox_endpoint = relay_proxy_base(pick.get("sandbox_endpoint") or "", a.sandbox_id)
        a.end_timestamp = int(pick.get("end_timestamp") or 0)
        if not a.sandbox_id:
            raise UpstreamError("server", 0, 0, "sandbox response missing sandbox_id")
        return a.sandbox_id, a.sandbox_endpoint

    def wake_sandbox(self, a: Auth, sandbox_id: str) -> None:
        bare = relay_base_from_proxy(a.sandbox_endpoint)
        self._relay_json(a, bare, "POST", f"/v1/sandboxes/{sandbox_id}/wake", {
            "access_hold_ms": 600000,
            "reason": "autoclaw2api chat request",
            "apply_business_busy_cooldown": True,
        })

    def _relay_json(self, a: Auth, base: str, method: str, path: str, body: Any = None):
        headers = {"Accept": "application/json, text/plain, */*"}
        if a.access_token:
            headers["Authorization"] = bearer(a.access_token)
        resp = self.http.request(method, base.rstrip("/") + path, json=body, headers=headers)
        raw = resp.text
        if resp.status_code >= 400:
            raise UpstreamError(classify(resp.status_code, 0, raw), resp.status_code, 0, raw[:200])
        try:
            env = resp.json()
        except Exception:
            return raw
        ok = env.get("ok")
        code = env.get("code")
        msg = env.get("error") or env.get("msg") or ""
        if (ok is False) or (code not in (None, 0)):
            kind = classify(resp.status_code, int(code or 0), msg)
            raise UpstreamError(kind, resp.status_code, int(code or 0), msg)
        return env.get("data") if env.get("data") is not None else env

    def fetch_model_config(self, a: Auth) -> list:
        try:
            data = self._userapi(a, a.region, "GET", CONFIG_MODELS_PATH)
            return (data or {}).get("models") or []
        except Exception:
            return []

    def chat_proxy(self, a: Auth, body: bytes, model: str):
        url = self.base(a.region) + PROXY_CHAT_PATH
        headers = proxy_chat_headers(a.access_token, model, a.region)
        payload = _prepare_proxy_body(body, model)
        resp = self.http.send(
            self.http.build_request("POST", url, content=payload, headers=headers),
            stream=True,
        )
        if resp.status_code >= 400:
            raw = resp.read()
            text = raw.decode("utf-8", "replace")
            kind = classify(resp.status_code, 0, text)
            resp.close()
            raise UpstreamError(kind, resp.status_code, 0, text[:200])
        return resp

    def chat_openai(self, a: Auth, body: bytes):
        base = a.sandbox_endpoint
        if not base:
            raise UpstreamError("client", 0, 0, f"no sandbox endpoint for account {a.user_id}")
        headers = {"Accept": "application/json, text/plain, */*"}
        if a.access_token:
            headers["Authorization"] = bearer(a.access_token)
        headers["Content-Type"] = "application/json"
        m, xm = _normalize_model(_peek_model(body))
        payload = _prepare_model_body(body)
        if xm:
            headers["x-openclaw-model"] = xm
        resp = self.http.send(
            self.http.build_request("POST", base.rstrip("/") + "/v1/chat/completions", content=payload, headers=headers),
            stream=True,
        )
        if resp.status_code >= 400:
            raw = resp.read()
            text = raw.decode("utf-8", "replace")
            resp.close()
            return None, resp.status_code, text
        return resp, resp.status_code, ""

    def fetch_sandbox_models(self, a: Auth) -> list:
        base = a.sandbox_endpoint
        if not base:
            return []
        headers = {"Accept": "application/json, text/plain, */*"}
        if a.access_token:
            headers["Authorization"] = bearer(a.access_token)
        resp = self.http.get(base.rstrip("/") + "/v1/models", headers=headers)
        if resp.status_code != 200:
            return []
        data = resp.json()
        return [m.get("id") for m in (data.get("data") or []) if m.get("id")]

    def open_device_ws(self, a: Auth):
        import ssl as _ssl
        base = a.sandbox_endpoint
        if not base or "/proxy/" not in base:
            return None
        bare = relay_base_from_proxy(base)
        token = strip_bearer(a.access_token)
        ws_url = f"{to_ws_base(bare)}/v1/client/ws?device_id={a.sandbox_id}&access_token={token}"
        import websockets.sync.client as ws_client
        ctx = _ssl._create_unverified_context()
        conn = ws_client.connect(ws_url, open_timeout=30, close_timeout=5, ssl_context=ctx)
        frame = {"type": "auth.inject", "token": token, "clientMetadata": {"client_type": "web"}}
        if a.user_id:
            frame["userId"] = a.user_id
        if a.user_name:
            frame["userName"] = a.user_name
        conn.send(json.dumps(frame))
        deadline = time.time() + 25
        while time.time() < deadline:
            try:
                msg = conn.recv(timeout=25)
            except Exception as ex:
                conn.close()
                raise RuntimeError(f"auth wait: {ex}")
            try:
                m = json.loads(msg)
            except Exception:
                continue
            if m.get("type") == "auth.inject.ok":
                return conn
            if m.get("type") == "auth.inject.error":
                conn.close()
                raise RuntimeError(f"auth.inject failed: {msg}")
        conn.close()
        raise RuntimeError("auth.inject timeout")

    def open_relay_events(self, a: Auth, base: str):
        headers = {"Accept": "text/event-stream"}
        if a.access_token:
            headers["Authorization"] = bearer(a.access_token)
        req = self.http.build_request("GET", base.rstrip("/") + "/api/events", headers=headers)
        resp = self.http.send(req, stream=True)
        if resp.status_code >= 400:
            raw = resp.read()
            text = raw.decode("utf-8", "replace")
            resp.close()
            raise UpstreamError(classify(resp.status_code, 0, text), resp.status_code, 0, text[:200])
        return resp

    def chat_relay(self, a: Auth, body: bytes, out) -> None:
        base = a.sandbox_endpoint
        if not base:
            raise UpstreamError("client", 0, 0, f"no sandbox endpoint for account {a.user_id}")
        req = _build_relay_chat_req(body)
        ws = None
        try:
            ws = self.open_device_ws(a)
        except Exception as ex:
            print(f"warn: device online ws: {ex} (fallback to http bridge)")
        try:
            events = self.open_relay_events(a, base)
        except Exception:
            if ws:
                ws.close()
            raise
        try:
            send_body = {"args": [req]}
            headers = {"Accept": "application/json, text/plain, */*", "Content-Type": "application/json"}
            if a.access_token:
                headers["Authorization"] = bearer(a.access_token)
            xm = _request_model_header(body)
            if xm:
                headers["x-openclaw-model"] = _strip_route_prefix(xm)
            resp = self.http.post(base.rstrip("/") + "/api/electron/agent/send", json=send_body, headers=headers)
            raw = resp.text
            if resp.status_code >= 400:
                raise UpstreamError(classify(resp.status_code, 0, raw), resp.status_code, 0, raw[:200])
            env = resp.json()
            if env.get("ok") is False or env.get("code") not in (None, 0):
                msg = env.get("error") or env.get("msg") or ""
                raise UpstreamError(classify(resp.status_code, int(env.get("code") or 0), msg), resp.status_code, int(env.get("code") or 0), msg)
            data = env.get("data") if env.get("data") is not None else env
            run_id = _extract_run_id(data)
            cv = RelayConverter(run_id, req.get("sessionKey", "main"), body, out)
            cv.run(events.iter_lines())
        finally:
            events.close()
            if ws:
                try:
                    ws.close()
                except Exception:
                    pass


def _num(v) -> int:
    if v is None:
        return 0
    if isinstance(v, (int, float)):
        return int(v)
    try:
        return int(float(str(v)))
    except Exception:
        return 0


def _peek_model(body: bytes) -> str:
    try:
        return (json.loads(body).get("model") or "").strip()
    except Exception:
        return ""


def _is_agent_target(model: str) -> bool:
    # 目录里的模型带 cn:/global: 命名空间，先剥前缀再判档，否则 cn:openclaw 会被当成
    # 普通后端模型，绕开 Agent 目标分支（x-openclaw-model 头与 relay 分派都依赖这个判定）。
    m = _strip_route_prefix(model).strip().lower()
    if m in ("openclaw", "openclaw/default"):
        return True
    return m.startswith(("openclaw/", "openclaw:", "agent:"))


def _normalize_model(model: str):
    m = _strip_route_prefix((model or "").strip())
    if not m:
        return "openclaw", ""
    if _is_agent_target(m):
        return m, ""
    return "openclaw", m


def _prepare_model_body(body: bytes) -> bytes:
    try:
        obj = json.loads(body)
    except Exception:
        return body
    m, xm = _normalize_model(obj.get("model") or "")
    if not xm:
        return body
    obj["model"] = "openclaw"
    return json.dumps(obj).encode()


def _prepare_proxy_body(body: bytes, model: str) -> bytes:
    try:
        obj = json.loads(body)
    except Exception:
        return body
    if model:
        obj["model"] = model
    obj.pop("user", None)
    return json.dumps(obj).encode()


def _request_model_header(body: bytes) -> str:
    m = _peek_model(body)
    if not m or _is_agent_target(m):
        return ""
    return _strip_route_prefix(m)


def _strip_route_prefix(model: str) -> str:
    """global:zai_auto / global/zai_auto → zai_auto；cn 同理；其余原样。"""
    for p in ("global:", "global/", "cn:", "cn/"):
        if model.lower().startswith(p):
            return model[len(p):]
    return model


def _content_parts_text(parts) -> str:
    sb = []
    for p in parts or []:
        if isinstance(p, dict) and p.get("type") == "text" and isinstance(p.get("text"), str):
            sb.append(p["text"])
    return "".join(sb).strip()


def _build_relay_chat_req(body: bytes) -> dict:
    obj = json.loads(body)
    req = {"sessionKey": (obj.get("user") or "").strip() or "main"}
    message = ""
    for m in reversed(obj.get("messages") or []):
        if m.get("role") != "user":
            continue
        c = m.get("content")
        if isinstance(c, str):
            message = c.strip()
            break
        if isinstance(c, list):
            message = _content_parts_text(c)
            if message:
                break
    if not message:
        raise ValueError("no user text message found")
    req["message"] = message
    if (obj.get("reasoning_effort") or "").strip():
        req["thinking"] = obj["reasoning_effort"].strip()
    return req


def _extract_run_id(data) -> str:
    if data is None:
        return ""
    if isinstance(data, str):
        return data.strip()
    if isinstance(data, dict):
        for k in ("runId", "run_id"):
            v = data.get(k)
            if isinstance(v, str) and v.strip():
                return v.strip()
    return ""


class SSEWriter:
    def __init__(self, wfile):
        self.wfile = wfile
        self.lock = threading.Lock()

    def write_data(self, payload: str) -> None:
        with self.lock:
            self.wfile.write(f"data: {payload}\n\n".encode())
            self.wfile.flush()

    def write_done(self) -> None:
        with self.lock:
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()


class RelayConverter:
    def __init__(self, run_id: str, session: str, body: bytes, out: SSEWriter):
        self.run_id = run_id
        self.session = session
        self.out = out
        try:
            self.model = json.loads(body).get("model") or ""
        except Exception:
            self.model = ""
        self.id = f"chatcmpl-{time.time_ns()}"
        self.created = int(time.time())
        self.last_text = ""
        self.tool_seq = 0
        self.seen_tools = set()
        self.done = False

    def run(self, lines) -> None:
        buf = []
        for line in lines:
            if isinstance(line, bytes):
                line = line.decode("utf-8", "replace")
            line = line.rstrip("\r\n")
            if line == "":
                if buf:
                    self._handle_frame("\n".join(buf))
                    buf = []
                if self.done:
                    break
            else:
                buf.append(line)
        if not self.done:
            self.finish()

    def _handle_frame(self, raw: str) -> None:
        if self.done:
            return
        data = ""
        for line in raw.split("\n"):
            if line.startswith("data:"):
                data += line[5:]
        if not data:
            return
        try:
            ev = json.loads(data)
        except Exception:
            return
        payload = ev.get("payload") or {}
        if not isinstance(payload, dict):
            return
        self._handle(ev.get("event") or "", payload)

    def _handle(self, event: str, payload: dict) -> None:
        run_id = payload.get("runId") or ""
        sk = payload.get("sessionKey") or ""
        to = payload.get("to") or ""
        same_run = bool(run_id) and bool(self.run_id) and run_id == self.run_id
        same_session = (sk and sk == self.session) or (to and to == self.session)
        if not same_run and not (same_session and not run_id):
            return
        if event in ("agent:stream", "stream", "chat:message", "chat.delta"):
            self._handle_agent_stream(payload)
        elif event == "chat":
            self._handle_chat(payload)
        elif event == "agent":
            self._handle_agent(payload)

    def _handle_agent_stream(self, payload: dict) -> None:
        if self.done:
            return
        typ = payload.get("type") or ""
        if typ == "error":
            self._emit_error(_payload_msg(payload))
        elif typ == "done":
            self._emit_usage_done(_extract_usage(payload))
        elif typ == "text":
            delta = payload.get("delta") or payload.get("text") or ""
            self._emit_text(delta)
        elif typ == "thinking":
            self._emit_thinking(payload.get("text") or payload.get("delta") or "")
        elif typ in ("tool_call", "tool_use"):
            name = payload.get("name") or payload.get("tool") or ""
            if name:
                self._emit_tool_use(payload.get("id") or "", name, payload.get("input", payload.get("arguments")))
        msg = payload.get("message")
        if isinstance(msg, dict):
            c = msg.get("content")
            if isinstance(c, str):
                self._emit_text(c)
            elif isinstance(c, list):
                self._emit_parts(c)

    def _handle_chat(self, payload: dict) -> None:
        state = payload.get("state") or ""
        if state == "error":
            self._emit_error(_payload_msg(payload))
            return
        msg = payload.get("message")
        if not isinstance(msg, dict):
            if state == "final" and _extract_usage(payload):
                self._emit_usage_done(_extract_usage(payload))
            return
        c = msg.get("content")
        if isinstance(c, str):
            self._emit_text(c)
        elif isinstance(c, list):
            self._emit_parts(c)
        for tc in msg.get("toolCalls") or []:
            if not isinstance(tc, dict):
                continue
            name = tc.get("name") or ((tc.get("function") or {}).get("name") if isinstance(tc.get("function"), dict) else "")
            if name:
                self._emit_tool_use(tc.get("id") or "", name, tc.get("input", tc.get("arguments")))
        if state == "final":
            self._emit_usage_done(_extract_usage(payload.get("usage") or payload))

    def _handle_agent(self, payload: dict) -> None:
        stream = payload.get("stream") or ""
        data = payload.get("data") if isinstance(payload.get("data"), dict) else payload
        if stream == "assistant":
            parts = data.get("content") or data.get("blocks")
            if isinstance(parts, list):
                self._emit_parts(parts)
            else:
                self._emit_text(data.get("text") or data.get("delta") or "")
        elif stream == "tool":
            if data.get("phase") == "start" and data.get("name"):
                self._emit_tool_use(data.get("toolCallId") or "", data["name"], data.get("args", data.get("input")))
        elif stream == "lifecycle":
            if data.get("phase") == "end":
                self._emit_usage_done(_extract_usage(data.get("usage") or payload))
            elif data.get("phase") == "error":
                self._emit_error(_payload_msg(data))
        elif stream == "error":
            self._emit_error(_payload_msg(data))

    def _emit_text(self, text: str) -> None:
        if not text or text == self.last_text:
            return
        delta = text[len(self.last_text):] if text.startswith(self.last_text) else text
        self.last_text = text
        if delta:
            self._chunk({"choices": [{"index": 0, "delta": {"content": delta}, "finish_reason": None}]})

    def _emit_thinking(self, text: str) -> None:
        if text:
            self._chunk({"choices": [{"index": 0, "delta": {"reasoning_content": text}, "finish_reason": None}]})

    def _emit_tool_use(self, tid: str, name: str, inp) -> None:
        if not tid:
            tid = f"{name}-{self.tool_seq}"
        if tid in self.seen_tools:
            return
        self.seen_tools.add(tid)
        args = json.dumps(inp, ensure_ascii=False) if inp is not None else ""
        idx = self.tool_seq
        self.tool_seq += 1
        self._chunk({"choices": [{"index": 0, "delta": {"tool_calls": [
            {"index": idx, "id": tid, "type": "function", "function": {"name": name, "arguments": args}}
        ]}, "finish_reason": None}]})

    def _emit_usage_done(self, usage: dict) -> None:
        chunk = {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}
        if usage:
            chunk["usage"] = {
                "prompt_tokens": _num(usage.get("input", usage.get("prompt_tokens"))),
                "completion_tokens": _num(usage.get("output", usage.get("completion_tokens"))),
                "total_tokens": _num(usage.get("total", usage.get("total_tokens"))),
            }
        self._chunk(chunk)
        self.finish()

    def _emit_error(self, msg: str) -> None:
        if msg:
            self._emit_text(msg)
        self._chunk({"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
        self.finish()

    def _emit_parts(self, parts: list) -> None:
        for p in parts:
            if not isinstance(p, dict):
                continue
            typ = p.get("type")
            if typ == "text":
                self._emit_text(p.get("text") or "")
            elif typ == "thinking":
                self._emit_thinking(p.get("thinking") or "")
            elif typ in ("tool_use", "toolCall"):
                if p.get("name"):
                    self._emit_tool_use(p.get("id") or "", p["name"], p.get("input", p.get("arguments")))

    def _chunk(self, chunk: dict) -> None:
        chunk.update({"id": self.id, "object": "chat.completion.chunk", "created": self.created, "model": self.model})
        self.out.write_data(json.dumps(chunk, ensure_ascii=False))

    def finish(self) -> None:
        if self.done:
            return
        self.done = True
        self.out.write_done()


def _payload_msg(m: dict) -> str:
    if not m:
        return "agent request failed (upstream error event, no detail)"
    for k in ("message", "error", "msg", "detail", "text", "delta"):
        v = m.get(k)
        if isinstance(v, str) and v.strip():
            return v.strip()
    typ = m.get("type") or "error"
    parts = ["agent request failed"]
    for k in ("code", "phase", "runId"):
        if m.get(k):
            parts.append(f"{k}={m[k]}")
    return " ".join([parts[0], typ] + parts[1:])


def _extract_usage(m):
    if not isinstance(m, dict):
        return None
    u = m.get("usage")
    if isinstance(u, dict) and _has_usage(u):
        return u
    if _has_usage(m):
        return m
    r = m.get("result")
    if isinstance(r, dict):
        if _has_usage(r):
            return r
        if isinstance(r.get("usage"), dict):
            return r["usage"]
    return None


def _has_usage(m: dict) -> bool:
    return any(k in m for k in (
        "input", "output", "total", "inputTokens", "outputTokens", "totalTokens",
        "prompt_tokens", "completion_tokens", "total_tokens",
    ))


def _not_enabled(status: int, body: str) -> bool:
    if status in (404, 501):
        return True
    if status == 400:
        lower = body.lower()
        return any(k in lower for k in ("chatcompletions", "endpoint", "not enabled", "disabled"))
    return False


def _validate_chat_body(body: bytes) -> str:
    try:
        req = json.loads(body)
    except Exception:
        return "invalid JSON body"
    msgs = req.get("messages")
    if not isinstance(msgs, list) or not msgs:
        return "messages must be a non-empty array"
    if not any(isinstance(m, dict) and m.get("role") == "user" for m in msgs):
        return "messages must contain at least one user message"
    return ""


def _aggregate_sse(lines) -> dict:
    content: list = []
    reasoning: list = []
    role = "assistant"
    finish = "stop"
    usage = None
    ident = ""
    model = ""
    created = 0
    tool_calls: dict = {}
    tool_order: list = []
    for line in lines:
        if isinstance(line, bytes):
            line = line.decode("utf-8", "replace")
        line = line.rstrip("\r\n")
        if not line.startswith("data: "):
            continue
        payload = line[6:]
        if payload == "[DONE]":
            break
        try:
            chunk = json.loads(payload)
        except Exception:
            continue
        if not ident and chunk.get("id"):
            ident = chunk["id"]
        if not model and chunk.get("model"):
            model = chunk["model"]
        if not created and chunk.get("created"):
            created = int(chunk["created"])
        if isinstance(chunk.get("usage"), dict):
            usage = chunk["usage"]
        for ch in chunk.get("choices") or []:
            if ch.get("finish_reason"):
                finish = ch["finish_reason"]
            delta = ch.get("delta") or {}
            if delta.get("role"):
                role = delta["role"]
            if isinstance(delta.get("content"), str):
                content.append(delta["content"])
            if isinstance(delta.get("reasoning_content"), str):
                reasoning.append(delta["reasoning_content"])
            for tc in delta.get("tool_calls") or []:
                idx = int(tc.get("index") or 0)
                if idx not in tool_calls:
                    tool_calls[idx] = {"index": idx}
                    tool_order.append(idx)
                merged = tool_calls[idx]
                for k in ("id", "type"):
                    if tc.get(k):
                        merged[k] = tc[k]
                df = tc.get("function") or {}
                mf = merged.setdefault("function", {})
                if df.get("name"):
                    mf["name"] = df["name"]
                if df.get("arguments"):
                    mf["arguments"] = mf.get("arguments", "") + df["arguments"]
    message = {"role": role, "content": "".join(content)}
    if reasoning:
        message["reasoning_content"] = "".join(reasoning)
    if tool_order:
        message["tool_calls"] = [tool_calls[i] for i in sorted(tool_order)]
    resp = {
        "id": ident or f"chatcmpl-{time.time_ns()}",
        "object": "chat.completion",
        "created": created or int(time.time()),
        "model": model,
        "choices": [{"index": 0, "message": message, "finish_reason": finish}],
    }
    if usage:
        resp["usage"] = usage
    return resp


def _single_chunk_sse(full: dict, out: SSEWriter) -> None:
    chunk = {k: full[k] for k in ("id", "object", "created", "model") if k in full}
    chunk["object"] = "chat.completion.chunk"
    choices = full.get("choices") or []
    if choices and isinstance(choices[0].get("message"), dict):
        msg = choices[0]["message"]
        delta = {k: msg[k] for k in ("role", "content") if k in msg}
        if msg.get("tool_calls"):
            delta["tool_calls"] = msg["tool_calls"]
        chunk["choices"] = [{"index": 0, "delta": delta, "finish_reason": "stop"}]
    if full.get("usage"):
        chunk["usage"] = full["usage"]
    out.write_data(json.dumps(chunk, ensure_ascii=False))
    out.write_done()


class Bridge:
    def __init__(self, cfg: dict):
        self.cfg = cfg
        self.auth_dir = cfg.get("auth_dir") or "./auths"
        self.api_key = cfg.get("api_key") or ""
        self.mode = (cfg.get("mode") or "auto").lower()
        self.max_rotate = int(cfg.get("max_rotate") or 3)
        self.hard_cooldown = _dur(cfg.get("cooldown", {}).get("hard_credit"), 12 * 3600)
        self.soft_cooldown = _dur(cfg.get("cooldown", {}).get("soft_rate"), 60)
        self.err_threshold = int(cfg.get("cooldown", {}).get("err_threshold") or 3)
        self.err_cooldown = _dur(cfg.get("cooldown", {}).get("err_cooldown"), 600)
        self.refresh_skew = 600
        self.timeout = float((cfg.get("upstream") or {}).get("timeout_seconds") or 120)
        self.up = Upstream(self.timeout, (cfg.get("upstream") or {}).get("user_api_base_override") or "")
        self.pools = {
            "cn": Pool("cn", (cfg.get("state_file") or "./data/state.json").replace(".json", ".cn.json")),
            "global": Pool("global", (cfg.get("state_file") or "./data/state.json").replace(".json", ".global.json")),
        }
        self.models_cache = {"data": [], "fetched": 0.0, "last_fail": 0.0}
        self.models_lock = threading.Lock()
        self.rescan()
        self._stop = threading.Event()
        self._maint = threading.Thread(target=self._maintain_loop, daemon=True)
        self._maint.start()

    def rescan(self) -> None:
        for region in ("cn", "global"):
            auths = load_dir(self.auth_dir, region)
            self.pools[region].sync(auths)

    def _maintain_loop(self) -> None:
        interval = float((self.cfg.get("schedule") or {}).get("maintenance_minutes") or 30) * 60
        while not self._stop.is_set():
            try:
                self.maintain_now()
            except Exception as ex:
                print("maintain error:", ex)
            self._stop.wait(interval)

    def maintain_now(self) -> None:
        self.rescan()
        for region, pool in self.pools.items():
            for st in pool.status_list():
                if st["disabled"]:
                    # session dead 停用的账号自愈尝试：上游可能只是服务端注销了
                    # token，refreshToken 还能换新（只针对 session dead，人工
                    # "manual disable (panel)" 的绝不动）。失败保持停用。
                    if "session dead" in (st.get("reason") or ""):
                        a = pool.auth_by_uid(st["uid"])
                        if a and a.refresh_token:
                            try:
                                self.up.refresh(a)
                                a.save()
                                pts = self.up.check_points(a)
                                pool.set_points(st["uid"], pts)
                                pool.reenable(st["uid"])
                                print(f"maintain {st['uid'][:8]} recovered from session dead via refresh")
                            except Exception as ex:
                                print(f"maintain {st['uid'][:8]} recovery: {ex}")
                    continue
                a = pool.auth_by_uid(st["uid"])
                if not a:
                    continue
                if a.refresh_token and a.needs_refresh(self.refresh_skew):
                    try:
                        self.up.refresh(a)
                        a.save()
                    except UpstreamError as ex:
                        if ex.kind == "session_dead":
                            pool.disable(st["uid"], "refresh session dead")
                            continue
                    except Exception as ex:
                        print(f"maintain {st['uid']} refresh: {ex}")
                try:
                    pts = self.up.check_points(a)
                    pool.set_points(st["uid"], pts)
                    pool.reenable_if_points(st["uid"], pts)
                except Exception as ex:
                    print(f"maintain {st['uid']} points: {ex}")
    def credit_detail(self, region: str, page: int = 1, page_size: int = 20) -> dict:
        a = self.pools[region].pick()
        if not a:
            return {"error": f"no {region} account"}
        h = userapi_headers(a.access_token, region)
        base = self.up.base(region)
        out = {"region": region, "uid": a.user_id, "entries": [], "expiring": None}
        try:
            r = self.up.http.get(base + f"/agent-assetmgr/api/v1/ledgers_std?page={page}&page_size={page_size}", headers=h, timeout=20)
            env = r.json()
            if env.get("code") == 0:
                d = env.get("data") or {}
                entries = []
                for e in (d.get("entries") or []):
                    m = e.get("metadata") or {}
                    entries.append({
                        "time": e.get("occurred_at"),
                        "amount": e.get("amount"),
                        "model": m.get("model") or m.get("display_model") or "",
                        "actual_model": m.get("actual_model") or "",
                        "reason": m.get("reason") or e.get("mutation_type") or "",
                        "charged": m.get("charged_amount") or "",
                        "cap": m.get("request_point_cap") or "",
                        "token_usage": m.get("token_usage") or "",
                        "description": e.get("description") or "",
                    })
                out["entries"] = entries
                out["total"] = d.get("total") or d.get("total_count") or len(entries)
            else:
                out["error"] = f"ledgers code={env.get('code')} {str(env.get('msg'))[:100]}"
        except Exception as ex:
            out["error"] = str(ex)[:150]
        try:
            r = self.up.http.get(base + "/agent-assetmgr/api/v1/points/expiring", headers=h, timeout=20)
            env = r.json()
            if env.get("code") == 0:
                d = env.get("data") or {}
                out["expiring"] = {
                    "points": d.get("expiring_points"),
                    "total": d.get("total_points"),
                    "days": d.get("expire_time_value"),
                    "text": d.get("expiring_points_text"),
                }
        except Exception:
            pass
        return out

    def refresh_credits_now(self) -> dict:
        out = []
        for region, pool in self.pools.items():
            for st in pool.status_list():
                if st["disabled"]:
                    out.append({"uid": st["uid"], "region": region, "points": st["points"], "error": "disabled"})
                    continue
                a = pool.auth_by_uid(st["uid"])
                if not a:
                    continue
                try:
                    pts = self.up.check_points(a)
                    pool.set_points(st["uid"], pts)
                    pool.reenable_if_points(st["uid"], pts)
                    out.append({"uid": st["uid"], "region": region, "points": pts})
                except Exception as ex:
                    out.append({"uid": st["uid"], "region": region, "points": st["points"], "error": str(ex)[:120]})
        return {"accounts": out}

    def _device_path(self, phone: str) -> str:
        return os.path.join(self.cfg.get("auth_dir") or "./auths", f".device-{phone}")

    def _load_or_new_device(self, phone: str) -> str:
        fp = self._device_path(phone)
        if os.path.exists(fp):
            with open(fp) as f:
                s = f.read().strip()
            if s:
                return s
        d = uuid.uuid4().hex
        d = f"{d[0:8]}-{d[8:12]}-{d[12:16]}-{d[16:20]}-{d[20:32]}"
        os.makedirs(os.path.dirname(fp) or ".", exist_ok=True)
        with open(fp, "w") as f:
            f.write(d)
        return d

    def login_cn_sendcode(self, phone: str) -> str:
        device_id = self._load_or_new_device(phone)
        self.up.send_code("cn", device_id, phone)
        return device_id

    def login_cn(self, phone: str, code: str) -> dict:
        device_id = self._load_or_new_device(phone)
        data = self.up.login("cn", device_id, phone, str(code).strip())
        a = Auth(
            access_token=data.get("access_token") or "",
            refresh_token=data.get("refresh_token") or "",
            expires_at=int(time.time()) + 28 * 86400,
            user_id=str(data.get("user_id") or ""),
            user_name=data.get("user_name") or "",
            phone=phone,
            device_id=device_id,
            region="cn",
        )
        if not a.access_token or not a.user_id:
            return {"error": "login response missing access_token/user_id", "raw": {k: v for k, v in (data or {}).items() if "token" not in k}}
        a.file_path = os.path.join(self.cfg.get("auth_dir") or "./auths", f"autoclaw-{a.user_id}.json")
        a.save()
        self.rescan()
        return {"uid": a.user_id, "name": a.user_name, "region": "cn"}

    def login_global_import_desktop(self) -> dict:
        import base64
        home = os.path.expanduser("~")
        jwt = ""
        for fp in (
            os.path.join(home, ".openclaw-autoclaw", "request-headers.json"),
            os.path.join(home, ".openclaw-autoclaw", "openclaw.json"),
        ):
            if not os.path.exists(fp):
                continue
            try:
                doc = json.load(open(fp, encoding="utf-8"))
            except Exception:
                continue
            if "headers" in doc and isinstance(doc.get("headers"), dict):
                jwt = doc["headers"].get("X-Authorization") or ""
            else:
                models = ((doc.get("models") or {}).get("providers") or {}).get("zai", {}).get("models") or []
                best_iat, best_tok = -1, ""
                for m in models:
                    a_ = (m.get("headers") or {}).get("X-Authorization") or ""
                    if not a_:
                        continue
                    tok = a_[7:] if a_.lower().startswith("bearer ") else a_
                    try:
                        p = tok.split(".")[1]
                        p += "=" * (-len(p) % 4)
                        iat = json.loads(base64.urlsafe_b64decode(p)).get("iat", 0)
                        if iat > best_iat:
                            best_iat, best_tok = iat, tok
                    except Exception:
                        continue
                jwt = best_tok
            if jwt:
                break
        jwt = jwt[7:] if jwt.lower().startswith("bearer ") else jwt
        if not jwt:
            return {"error": "desktop JWT not found (~/.openclaw-autoclaw)"}
        p = jwt.split(".")[1]
        p += "=" * (-len(p) % 4)
        payload = json.loads(base64.urlsafe_b64decode(p))
        uid = str(payload.get("user_id") or "")
        if not uid:
            return {"error": "JWT missing user_id"}
        auth_dir = self.cfg.get("auth_dir") or "./auths"
        a = Auth(
            access_token="Bearer " + jwt,
            refresh_token="",
            expires_at=int(payload.get("exp") or 0) or int(time.time()) + 86400,
            user_id=uid,
            user_name="global-desktop",
            phone="",
            device_id=str(payload.get("device_id") or ""),
            region="global",
        )
        a.file_path = os.path.join(auth_dir, f"autoclaw-global-{uid}.json")
        a.save()
        self.rescan()
        return {"uid": uid, "region": "global", "exp": payload.get("exp")}

    def login_global_oauth_callback(self, callback_url: str) -> dict:
        from urllib.parse import urlsplit, parse_qs
        sp = urlsplit(callback_url.strip())
        qs = parse_qs(sp.query)
        code = (qs.get("code") or [""])[0]
        state = (qs.get("state") or [""])[0]
        if not code or not state:
            return {"error": "callback URL missing code/state"}
        device_id = self._load_or_new_device("global-oauth")
        headers = userapi_headers("", "global")
        try:
            resp = self.http.post(
                USERAPI_BASE["global"] + "/userapi/overseasv1/google-oauth-login",
                json={"source_id": "autoclaw", "device_id": device_id, "code": code, "state": state, "platform": "web"},
                headers=headers,
                timeout=30,
            )
        except Exception as ex:
            return {"error": f"oauth-login request failed: {ex}"}
        try:
            env = resp.json()
        except Exception:
            return {"error": f"oauth-login http {resp.status_code}: {resp.text[:200]}"}
        data = env.get("data") if isinstance(env.get("data"), dict) else env
        token = data.get("access_token") or env.get("access_token") or ""
        if not token:
            return {"error": f"oauth-login no access_token (http {resp.status_code})", "msg": str(env)[:300]}
        try:
            p = token.split(".")[1]
            p += "=" * (-len(p) % 4)
            payload = json.loads(base64.urlsafe_b64decode(p))
            uid = str(payload.get("user_id") or "")
        except Exception:
            uid = ""
        if not uid:
            return {"error": "token missing user_id", "token_prefix": token[:20]}
        auth_dir = self.cfg.get("auth_dir") or "./auths"
        a = Auth(
            access_token=token if token.lower().startswith("bearer ") else "Bearer " + token,
            refresh_token=data.get("refresh_token") or env.get("refresh_token") or "",
            expires_at=int(time.time()) + 28 * 86400,
            user_id=uid,
            user_name="global-oauth",
            phone="",
            device_id=device_id,
            region="global",
        )
        a.file_path = os.path.join(auth_dir, f"autoclaw-global-{uid}.json")
        a.save()
        self.rescan()
        return {"uid": uid, "region": "global"}


    def _handle_error(self, pool: Pool, uid: str, err: Exception) -> bool:
        """处理上游错误并记账。返回 True 表示已通过 refresh 自救成功，
        调用方可在同一请求内重试该账号（chat 轮转会把 uid 移出 tried）。"""
        if isinstance(err, UpstreamError):
            if err.kind == "session_dead":
                # 410000 / "access token expired" 多为上游**服务端**注销了 token
                # （本地 expiresAt 往往还远未到，maintain 的本地过期判断踩不中）。
                # 先用 refreshToken 抢救一次：成功则不停用（禁用只能等人工重登），
                # 失败才判死。
                a = pool.auth_by_uid(uid)
                if a and a.refresh_token:
                    try:
                        self.up.refresh(a)
                        a.save()
                        pool.reenable(uid)
                        print(f"{uid[:8]}: session_dead recovered via refresh")
                        return True
                    except Exception as ex:
                        print(f"{uid[:8]}: refresh after session_dead failed: {ex}")
                pool.disable(uid, "session dead")
            elif err.kind == "hard_credit":
                pool.cooldown(uid, "hard_credit", self.hard_cooldown, "余额不足")
            elif err.kind == "soft_rate":
                pool.cooldown(uid, "soft_rate", self.soft_cooldown, "429 rate limit")
            else:
                pool.note_error(uid, self.err_threshold, self.err_cooldown)
        else:
            pool.note_error(uid, self.err_threshold, self.err_cooldown)
        return False

    def _prepare(self, a: Auth, region: str = "cn") -> None:
        if region == "global":
            self._prepare_global(a)
            return
        if a.needs_refresh(self.refresh_skew):
            if not a.refresh_token:
                raise UpstreamError("session_dead", 401, 0, "no refreshToken — re-login required")
            self.up.refresh(a)
            a.save()
        self.up.ensure_sandbox(a)
        a.save()
        self.up.wake_sandbox(a, a.sandbox_id)

    def resolve_region(self, body_obj: dict, headers: dict, model: str) -> Optional[str]:
        for key in ("X-AutoClaw-Region", "x-autoclaw-region", "X-Region", "x-region"):
            v = (headers.get(key) or "").strip().lower()
            if v in ("cn", "global"):
                return v
        v = (body_obj.get("region") or "").strip().lower()
        if v in ("cn", "global"):
            return v
        m = model.strip()
        for p in ("global/", "cn/", "global:", "cn:"):
            if m.lower().startswith(p):
                return p.split("/")[0].split(":")[0]
        return None

    def strip_region_prefix(self, model: str) -> str:
        for p in ("global/", "cn/", "global:", "cn:"):
            if model.lower().startswith(p):
                return model[len(p):]
        return model

    def chat(self, wfile, body: bytes, headers: dict, want_stream: bool) -> int:
        verr = _validate_chat_body(body)
        if verr:
            _write_json(wfile, 400, {"error": {"message": verr, "type": "invalid_request", "code": "invalid_request"}})
            return 400
        body_obj = json.loads(body)
        raw_model = (body_obj.get("model") or "").strip()
        region = self.resolve_region(body_obj, headers, raw_model)
        model = self.strip_region_prefix(raw_model)
        # global/后端模型统一走 cn relay（zai-proxy 直连已被 WAF 406 拦截，2026-09-23）
        # 全部走 cn 池的沙箱 relay（x-openclaw-model 头指定后端模型）
        regions = ["cn"]
        tried = {r: set() for r in regions}
        last_err = None
        for _ in range(self.max_rotate * len(regions)):
            picked = None
            for r in regions:
                picked = self.pools[r].pick(tried[r])
                if picked:
                    region_used = r
                    break
            if not picked:
                break
            a = picked
            tried[region_used].add(a.user_id)
            pool = self.pools[region_used]
            try:
                self._prepare(a, region_used)
            except Exception as ex:
                last_err = ex
                if self._handle_error(pool, a.user_id, ex):
                    # refresh 自救成功：移出 tried，本轮就用新 token 重试该账号。
                    tried[region_used].discard(a.user_id)
                continue
            ok, status = self._dispatch(pool, a, wfile, body, model, region_used, want_stream)
            if ok:
                return status
            if status == -1:
                continue
        msg = "all accounts unavailable (cooling/disabled)"
        if last_err:
            msg += f": {last_err}"
        _write_json(wfile, 503, {"error": {"message": msg, "type": "api_error", "code": "no_healthy_account"}})
        return 503

    def _dispatch(self, pool: Pool, a: Auth, wfile, body: bytes, model: str, region: str, want_stream: bool):
        # zai-proxy 直连已被 WAF 406 拦截（2026-09-23），所有后端模型统一走 relay
        # （x-openclaw-model 头指定后端模型，tdpsk_*/zaicoding_*/zai_* 实测全通）

        if self.mode in ("auto", "proxy") and not _is_agent_target(model):
            try:
                resp = self.up.chat_proxy(a, body, model or "zai_auto")
            except Exception as ex:
                print(f"proxy chat failed (model={model}), fallback to relay: {ex}")
                if self.mode == "proxy":
                    self._handle_error(pool, a.user_id, ex)
                    return False, -1
            else:
                return True, self._stream_out(pool, a, wfile, resp, want_stream, model)
        if self.mode != "relay":
            resp, status, text = self.up.chat_openai(a, body)
            if resp is None:
                if self.mode == "auto" and _not_enabled(status, text):
                    pass
                else:
                    ex = UpstreamError(classify(status, 0, text), status, 0, text[:200])
                    self._handle_error(pool, a.user_id, ex)
                    return False, -1
            else:
                return True, self._stream_out(pool, a, wfile, resp, want_stream, model)
        try:
            if want_stream:
                _write_sse_headers(wfile)
                out = SSEWriter(wfile)
                self.up.chat_relay(a, body, out)
                pool.note_success(a.user_id)
                return True, 200
            buf_lines = []

            class ListOut:
                def write_data(self, payload: str) -> None:
                    buf_lines.append(f"data: {payload}\n")

                def write_done(self) -> None:
                    buf_lines.append("data: [DONE]\n")

            self.up.chat_relay(a, body, ListOut())
            agg = _aggregate_sse(buf_lines)
            pool.note_success(a.user_id)
            _write_json(wfile, 200, agg)
            return True, 200
        except Exception as ex:
            self._handle_error(pool, a.user_id, ex)
            return False, -1

    def _stream_out(self, pool: Pool, a: Auth, wfile, resp, want_stream: bool, model: str) -> int:
        ct = (resp.headers.get("content-type") or "").lower()
        if pool is not None and a is not None:
            pool.note_success(a.user_id)
        if "text/event-stream" in ct:
            if want_stream:
                _write_sse_headers(wfile)
                _pump_sse(resp, SSEWriter(wfile))
                return 200
            agg = _aggregate_sse(resp.iter_lines())
            _write_json(wfile, 200, agg)
            return 200
        raw = resp.read()
        try:
            data = json.loads(raw)
        except Exception:
            _write_json(wfile, 502, {"error": {"message": "invalid JSON from upstream", "type": "api_error", "code": "upstream_parse"}})
            return 502
        if not want_stream:
            _write_json(wfile, 200, data)
            return 200
        _write_sse_headers(wfile)
        _single_chunk_sse(data, SSEWriter(wfile))
        return 200

    def _prepare_global(self, a: Auth) -> None:
        if a.needs_refresh(self.refresh_skew):
            if not a.refresh_token:
                return
            try:
                self.up.refresh(a)
                a.save()
            except Exception:
                pass

    def _healthy_accounts(self, region: str) -> list:
        """模型目录用的只读账号快照：走 status_list，不 refresh、不唤醒、不改池状态。
        返回 [(auth, 归属引用)]，最多 MODEL_LIST_ACCOUNT_LIMIT 个——归属要按账号读目录，
        不设上限的话账号数增长会把只读请求也放大成上游风暴。"""
        pool = self.pools[region]
        out = []
        for st in pool.status_list():
            if st.get("disabled") or st.get("cooling"):
                continue
            a = pool.auth_by_uid(st["uid"])
            if a is not None:
                out.append((a, {"uid": st["uid"], "nickname": st.get("nickname") or ""}))
        return out[:MODEL_LIST_ACCOUNT_LIMIT]

    def _catalog_ready(self, a: Auth, wake: bool) -> bool:
        """把账号备到能读模型目录：按需 refresh + ensure_sandbox。
        wake 只给每区首个账号——目录本身是只读请求，沙箱唤醒留给真正的对话请求。"""
        if a.needs_refresh(self.refresh_skew):
            if not a.refresh_token.strip():
                return False
            try:
                self.up.refresh(a)
                a.save()
            except Exception:
                return False
        try:
            self.up.ensure_sandbox(a)
            if wake:
                self.up.wake_sandbox(a, a.sandbox_id)
            a.save()
        except Exception:
            return False
        return True

    def _serving_refs(self) -> list:
        return [ref for _, ref in self._healthy_accounts("cn")]

    def model_list(self) -> list:
        with self.models_lock:
            cached = self.models_cache["data"]
            if cached and time.time() - self.models_cache["fetched"] < 3600:
                return cached
            if self.models_cache["last_fail"] and time.time() - self.models_cache["last_fail"] < 300:
                return cached or _static_models(self._serving_refs())
        cn_accounts = self._healthy_accounts("cn")
        # global: 目录从 global 账号读一次，但对话统一走 cn 池 relay（zai-proxy 直连被
        # WAF 406 拦截，2026-09-23），所以 global: 模型的承接方是 cn 账号。
        serving = [ref for _, ref in cn_accounts]
        owners: dict = {}
        meta: dict = {}

        def note(model_id: str, refs: list, description: str = "", context_length: int = 0) -> None:
            bucket = owners.setdefault(model_id, [])
            for ref in refs:
                if ref not in bucket:
                    bucket.append(ref)
            if model_id not in meta:
                meta[model_id] = {"description": description, "context_length": context_length}

        # cn: 逐账号读沙箱目录，模型挂到它真正出现的账号下（多账号模型在各组重复出现）。
        for i, (a, ref) in enumerate(cn_accounts):
            if not self._catalog_ready(a, wake=(i == 0)):
                continue
            try:
                mids = self.up.fetch_sandbox_models(a) or []
            except Exception:
                continue
            for mid in mids:
                note(f"cn:{mid}", [ref])
        for i, (a, _ref) in enumerate(self._healthy_accounts("global")):
            if not self._catalog_ready(a, wake=(i == 0)):
                continue
            try:
                if i == 0:
                    for it in self.up.fetch_model_config(a) or []:
                        mid = it.get("id") or ""
                        if mid:
                            note(f"global:{mid}", serving, it.get("name") or mid, it.get("contextWindow") or 0)
                mids = self.up.fetch_sandbox_models(a) or []
            except Exception:
                continue
            for mid in mids:
                note(f"global:{mid}", serving)
        out = []
        for model_id, refs in owners.items():
            info = meta.get(model_id) or {}
            entry = {"id": model_id, "object": "model", "created": 1787000000,
                     "owned_by": "autoclaw",
                     "context_length": info.get("context_length") or 1000000,
                     "accounts": refs}
            if _is_agent_target(model_id):
                entry["tier"] = "agent"
            if info.get("description"):
                entry["description"] = info["description"]
            out.append(entry)
        if out:
            with self.models_lock:
                self.models_cache["data"] = out
                self.models_cache["fetched"] = time.time()
                self.models_cache["last_fail"] = 0.0
            return out
        with self.models_lock:
            self.models_cache["last_fail"] = time.time()
        return self.models_cache["data"] or _static_models(serving)


# MODEL_LIST_ACCOUNT_LIMIT 限制逐账号读目录的账号数：归属精度换来的是上游只读请求，
# 不设上限的话账号数增长会把它放大成风暴。
MODEL_LIST_ACCOUNT_LIMIT = 10


def _tier(model: str) -> dict:
    """Agent 分档复用 _is_agent_target 的口径：命中才标 tier，其余不标。"""
    return {"tier": "agent"} if _is_agent_target(model) else {}


def _static_models(accounts: Optional[list] = None) -> list:
    """上游全不可达时的兜底目录。命名空间与在线口径一致（cn:/global:），
    归属沿用 cn 账号——global: 模型实际也由国内版账号承接。"""
    refs = list(accounts or [])
    out = []
    for m in CN_STATIC_MODELS:
        out.append({"id": f"cn:{m}", "object": "model", "created": 1787000000,
                    "owned_by": "autoclaw", "context_length": 1000000,
                    "accounts": list(refs), **_tier(m)})
    for m in GLOBAL_STATIC_MODELS:
        out.append({"id": f"global:{m}", "object": "model", "created": 1787000000,
                    "owned_by": "autoclaw", "context_length": 1000000,
                    "accounts": list(refs), **_tier(m)})
    return out


def _dur(v, default: float) -> float:
    if not v:
        return default
    s = str(v).strip()
    m = re.fullmatch(r"(\d+)\s*([smhd]?)", s)
    if not m:
        return default
    n = int(m.group(1))
    unit = m.group(2) or "s"
    return float(n * {"s": 1, "m": 60, "h": 3600, "d": 86400}[unit])


def _write_json(wfile, status: int, obj: dict) -> None:
    raw = json.dumps(obj, ensure_ascii=False).encode()
    wfile.write(f"HTTP/1.1 {status} {'OK' if status < 400 else 'Error'}\r\n".encode())
    wfile.write(b"Content-Type: application/json\r\n")
    wfile.write(f"Content-Length: {len(raw)}\r\n\r\n".encode())
    wfile.write(raw)


def _write_sse_headers(wfile) -> None:
    wfile.write(b"HTTP/1.1 200 OK\r\n")
    wfile.write(b"Content-Type: text/event-stream\r\n")
    wfile.write(b"Cache-Control: no-cache\r\n")
    wfile.write(b"Connection: close\r\n")
    wfile.write(b"X-Accel-Buffering: no\r\n\r\n")
    wfile.flush()


def _pump_sse(resp, out: SSEWriter) -> None:
    saw_done = False
    finished = False
    try:
        for line in resp.iter_lines():
            if isinstance(line, bytes):
                line = line.decode("utf-8", "replace")
            if line.startswith("data: [DONE]"):
                saw_done = True
                out.wfile.write(b"data: [DONE]\n\n")
                out.wfile.flush()
                break
            if not finished and '"finish_reason"' in line and re.search(r'"finish_reason"\s*:\s*"(?:stop|length|tool_calls)"', line):
                finished = True
            out.wfile.write((line + "\n").encode())
            out.wfile.flush()
            if finished:
                out.wfile.write(b"\ndata: [DONE]\n\n")
                out.wfile.flush()
                saw_done = True
                break
    except Exception as ex:
        print("sse pump error:", ex)
    if not saw_done:
        out.wfile.write(b"\ndata: [DONE]\n\n")
        out.wfile.flush()
    try:
        resp.close()
    except Exception:
        pass


class Handler(BaseHTTPRequestHandler):
    bridge: Bridge = None
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _authed(self) -> bool:
        if not self.bridge.api_key:
            return True
        authz = self.headers.get("Authorization") or ""
        return authz.startswith("Bearer ") and authz[7:] == self.bridge.api_key

    def _deny(self):
        _write_json(self.wfile, 401, {"error": {"message": "missing or invalid API key", "type": "api_error", "code": "invalid_api_key"}})

    def _write_html(self, html: str, status: int = 200) -> None:
        raw = html.encode("utf-8")
        self.wfile.write(f"HTTP/1.1 {status} OK\r\n".encode())
        self.wfile.write(b"Content-Type: text/html; charset=utf-8\r\n")
        self.wfile.write(b"Cache-Control: no-cache\r\n")
        self.wfile.write(f"Content-Length: {len(raw)}\r\n\r\n".encode())
        self.wfile.write(raw)

    def _console_path(self) -> Optional[str]:
        base = os.path.dirname(os.path.abspath(__file__))
        return os.path.join(base, "console.html")

    def _run_checkin(self, region: str, dry_run: bool, mode: str, uid: str = "") -> dict:
        sys_path = os.path.dirname(os.path.abspath(__file__))
        if sys_path not in __import__("sys").path:
            __import__("sys").path.insert(0, sys_path)
        import check as ck
        auths = ck.load_auths(self.bridge.cfg.get("auth_dir") or "./auths", region)
        if uid:
            auths = [a for a in auths if a["user_id"] == uid]
        results = []
        for acct in auths:
            if mode == "tasks":
                results.append(ck.task_list(acct, dry_run, 20.0))
            else:
                results.append(ck.checkin(acct, dry_run, 20.0))
        return {"results": results}

    def do_GET(self):
        path = self.path.split("?")[0]
        if path in ("/", "/console", "/console.html"):
            cp = self._console_path()
            if cp and os.path.exists(cp):
                with open(cp, encoding="utf-8") as f:
                    self._write_html(f.read())
                return
            _write_json(self.wfile, 500, {"error": {"message": "console.html missing", "type": "api_error", "code": "internal"}})
            return
        if path == "/healthz":
            counts = [self.bridge.pools[r].counts() for r in ("cn", "global")]
            total = sum(c[0] for c in counts)
            healthy = sum(c[1] for c in counts)
            _write_json(self.wfile, 200 if healthy else 503, {"healthy": healthy, "total": total})
            return
        if not self._authed():
            self._deny()
            return
        if path == "/v1/models":
            _write_json(self.wfile, 200, {"object": "list", "data": self.bridge.model_list()})
            return
        if path == "/status":
            _write_json(self.wfile, 200, {
                "accounts": self.bridge.pools["cn"].status_list() + self.bridge.pools["global"].status_list(),
                "mode": self.bridge.mode,
            })
            return
        if path == "/api/credits/detail":
            if not self._authed():
                self._deny()
                return
            qs = dict(p.split("=", 1) for p in (self.path.split("?", 1)[1].split("&") if "?" in self.path else []) if "=" in p)
            region = qs.get("region", "cn")
            if region not in ("cn", "global"):
                region = "cn"
            try:
                data = self.bridge.credit_detail(region)
            except Exception as ex:
                data = {"error": str(ex)}
            _write_json(self.wfile, 200, data)
            return
        if path == "/api/tasks":
            qs = dict(p.split("=", 1) for p in (self.path.split("?", 1)[1].split("&") if "?" in self.path else []) if "=" in p)
            region = qs.get("region", "all")
            if region not in ("all", "cn", "global"):
                region = "all"
            try:
                data = self._run_checkin(region, True, "tasks")
            except Exception as ex:
                data = {"error": str(ex), "results": []}
            _write_json(self.wfile, 200, data)
            return
        _write_json(self.wfile, 404, {"error": {"message": "Not Found", "type": "api_error", "code": "not_found"}})

    def do_POST(self):
        path = self.path.split("?")[0]
        if path in ("/", "/console", "/console.html"):
            return self.do_GET()
        if path == "/api/credits/refresh":
            if not self._authed():
                self._deny()
                return
            n = int(self.headers.get("Content-Length") or 0)
            if n:
                self.rfile.read(min(n, 64 << 10))
            try:
                data = self.bridge.refresh_credits_now()
            except Exception as ex:
                data = {"error": str(ex), "accounts": []}
            _write_json(self.wfile, 200, data)
            return
        if path == "/api/credits/detail":
            if not self._authed():
                self._deny()
                return
            qs = dict(p.split("=", 1) for p in (self.path.split("?", 1)[1].split("&") if "?" in self.path else []) if "=" in p)
            region = qs.get("region", "cn")
            if region not in ("cn", "global"):
                region = "cn"
            try:
                data = self.bridge.credit_detail(region)
            except Exception as ex:
                data = {"error": str(ex)}
            _write_json(self.wfile, 200, data)
            return
        if path == "/api/maintain":
            # 手动触发一次积分刷新 + 会话维护（后台线程 30 分钟才跑一次）。
            if not self._authed():
                self._deny()
                return
            # 必须读掉请求体：keep-alive 下残留的 body 字节会被当成下一个
            # 请求行解析 → 连接上后续请求全部 501 Unsupported method。
            n = int(self.headers.get("Content-Length") or 0)
            if n:
                self.rfile.read(min(n, 64 << 10))
            try:
                self.bridge.maintain_now()
                data = {"ok": True}
            except Exception as ex:
                data = {"ok": False, "error": str(ex)}
            _write_json(self.wfile, 200, data)
            return
        if path == "/api/accounts/toggle":
            # 停用/启用账号（面板账号行按钮；等价于池内 disable/reenable，落 state 文件）。
            if not self._authed():
                self._deny()
                return
            n = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(min(n, 64 << 10)) or b"{}") if n else {}
            uid = str(body.get("uid") or "")
            region = body.get("region") if body.get("region") in ("cn", "global") else "cn"
            pool = self.bridge.pools[region]
            if not uid or uid not in pool.by_uid:
                _write_json(self.wfile, 200, {"error": "uid not found"})
                return
            if body.get("enable"):
                pool.reenable(uid)
            else:
                pool.disable(uid, "manual disable (panel)")
            pool.flush()
            _write_json(self.wfile, 200, {"ok": True, "uid": uid, "disabled": not body.get("enable")})
            return
        if path == "/api/accounts/delete":
            # 删除账号：删凭证文件 → rescan（sync 会剪掉不在目录里的 uid 并 flush）。
            if not self._authed():
                self._deny()
                return
            n = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(min(n, 64 << 10)) or b"{}") if n else {}
            uid = str(body.get("uid") or "")
            regions = [body.get("region")] if body.get("region") in ("cn", "global") else ["cn", "global"]
            removed = ""
            for r in regions:
                a = self.bridge.pools[r].auth_by_uid(uid)
                if a and a.file_path and os.path.isfile(a.file_path):
                    os.remove(a.file_path)
                    removed = a.file_path
            if removed:
                self.bridge.rescan()
                _write_json(self.wfile, 200, {"ok": True, "uid": uid, "deleted": True})
            else:
                _write_json(self.wfile, 200, {"error": "auth file not found for uid " + (uid or "(empty)")})
            return
        if path == "/api/checkin":
            if not self._authed():
                self._deny()
                return
            n = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(min(n, 64 << 10)) if n else b"{}"
            try:
                payload = json.loads(raw or b"{}")
            except Exception:
                payload = {}
            region = str(payload.get("region") or "all")
            if region not in ("all", "cn", "global"):
                region = "all"
            dry = bool(payload.get("dry_run"))
            uid = str(payload.get("uid") or "")
            try:
                data = self._run_checkin(region, dry, "checkin", uid)
            except Exception as ex:
                data = {"error": str(ex), "results": []}
            _write_json(self.wfile, 200, data)
            return
        if path in ("/api/login/cn/sendcode", "/api/login/cn", "/api/login/global/import-desktop", "/api/login/global/oauth"):
            if not self._authed():
                self._deny()
                return
            n = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(min(n, 64 << 10)) if n else b"{}"
            try:
                payload = json.loads(raw or b"{}")
            except Exception:
                payload = {}
            try:
                if path == "/api/login/cn/sendcode":
                    phone = str(payload.get("phone") or "").strip()
                    if not phone:
                        data = {"error": "phone required"}
                    else:
                        self.bridge.login_cn_sendcode(phone)
                        data = {"ok": True, "msg": "验证码已发送"}
                elif path == "/api/login/cn":
                    data = self.bridge.login_cn(str(payload.get("phone") or ""), str(payload.get("code") or ""))
                elif path == "/api/login/global/import-desktop":
                    data = self.bridge.login_global_import_desktop()
                else:
                    data = self.bridge.login_global_oauth_callback(str(payload.get("callback_url") or ""))
            except Exception as ex:
                data = {"error": str(ex)}
            _write_json(self.wfile, 200, data)
            return
        if path != "/v1/chat/completions":
            _write_json(self.wfile, 404, {"error": {"message": "Not Found", "type": "api_error", "code": "not_found"}})
            return
        if not self._authed():
            self._deny()
            return
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(min(n, 8 << 20)) if n else b""
        try:
            want_stream = bool(json.loads(body).get("stream"))
        except Exception:
            want_stream = False
        try:
            self.bridge.chat(self.wfile, body, dict(self.headers), want_stream)
        except BrokenPipeError:
            pass
        except Exception as ex:
            print("chat handler error:", ex)
            try:
                _write_json(self.wfile, 500, {"error": {"message": str(ex), "type": "api_error", "code": "internal"}}
                            )
            except Exception:
                pass
        if want_stream:
            self.close_connection = True
            try:
                self.wfile.flush()
                self.connection.shutdown(socket.SHUT_WR)
            except Exception:
                pass


def load_config(path: str) -> dict:
    cfg = {}
    if path and os.path.exists(path):
        with open(path) as f:
            cfg = json.load(f)
    env = os.environ
    for k, dest in (
        ("AC2A_LISTEN", "listen"), ("AC2A_API_KEY", "api_key"), ("AC2A_AUTH_DIR", "auth_dir"),
        ("AC2A_STATE_FILE", "state_file"), ("AC2A_MODE", "mode"),
    ):
        if env.get(k):
            cfg[dest] = env[k]
    return cfg


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("-config", default="config.json")
    ap.add_argument("-listen", default="")
    args = ap.parse_args()
    cfg = load_config(args.config)
    listen = args.listen or cfg.get("listen") or "127.0.0.1:7865"
    if listen.startswith(":"):
        listen = "127.0.0.1" + listen
    bridge = Bridge(cfg)
    Handler.bridge = bridge
    host, _, port = listen.rpartition(":")
    srv = ThreadingHTTPServer((host or "127.0.0.1", int(port)), Handler)
    n_cn = len(bridge.pools["cn"].by_uid)
    n_gl = len(bridge.pools["global"].by_uid)
    print(f"autoclaw2api(py) listening on {listen} mode={bridge.mode} cn={n_cn} global={n_gl}")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        bridge._stop.set()
        bridge.pools["cn"].flush()
        bridge.pools["global"].flush()


if __name__ == "__main__":
    main()
