#!/usr/bin/env python3
"""autoclaw2api 模型目录的回归测试：命名空间、账号归属、Agent 分档。

只测纯逻辑，不发任何上游请求：Bridge 走 __new__ 构造，pools 与 up 都是内存桩。
httpx 缺失时注入最小桩，因此本机不装依赖也能跑。
"""
import os
import sys
import tempfile
import threading
import time
import types
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

if "httpx" not in sys.modules:
    try:
        import httpx  # noqa: F401
    except ImportError:
        _stub = types.ModuleType("httpx")
        _stub.Limits = lambda **kw: None
        _stub.Client = object
        sys.modules["httpx"] = _stub

import bridge  # noqa: E402


class FakePool:
    """只实现 model_list 用到的两个只读入口：status_list 与 auth_by_uid。"""

    def __init__(self, accounts, tmpdir):
        self.accounts = accounts
        self.tmpdir = tmpdir
        self.by_uid = {a["uid"]: a for a in accounts}

    def status_list(self):
        return [{"uid": a["uid"], "nickname": a.get("nickname", ""),
                 "region": a.get("region", "cn"),
                 "disabled": a.get("disabled", False),
                 "cooling": a.get("cooling", False)} for a in self.accounts]

    def auth_by_uid(self, uid):
        rec = self.by_uid.get(uid)
        if rec is None:
            return None
        return bridge.Auth(access_token="Bearer stub", refresh_token="r",
                           expires_at=int(time.time()) + 86400, user_id=uid,
                           user_name=rec.get("nickname", ""),
                           region=rec.get("region", "cn"),
                           file_path=os.path.join(self.tmpdir, f"{uid}.json"))


class FakeUpstream:
    def __init__(self, sandbox, config):
        self.sandbox = sandbox   # uid -> [model id]
        self.config = config     # uid -> [{"id","name","contextWindow"}]
        self.woken = []

    def refresh(self, a):
        return None

    def ensure_sandbox(self, a):
        if a.user_id not in self.sandbox:
            raise RuntimeError("no sandbox for " + a.user_id)
        a.sandbox_id = "sb-" + a.user_id
        a.sandbox_endpoint = "http://127.0.0.1:1/autoclaw-cloud/proxy/" + a.sandbox_id
        return a.sandbox_id, a.sandbox_endpoint

    def wake_sandbox(self, a, sandbox_id):
        self.woken.append(a.user_id)

    def fetch_sandbox_models(self, a):
        return list(self.sandbox.get(a.user_id, []))

    def fetch_model_config(self, a):
        return list(self.config.get(a.user_id, []))


def make_bridge(cn=(), global_accounts=(), sandbox=None, config=None):
    tmpdir = tempfile.mkdtemp(prefix="ac2a-test-")
    b = bridge.Bridge.__new__(bridge.Bridge)
    b.pools = {"cn": FakePool(list(cn), tmpdir), "global": FakePool(list(global_accounts), tmpdir)}
    b.up = FakeUpstream(sandbox or {}, config or {})
    b.models_cache = {"data": [], "fetched": 0.0, "last_fail": 0.0}
    b.models_lock = threading.Lock()
    b.refresh_skew = 600
    return b


class ModelNamespaceTests(unittest.TestCase):
    def test_prefix_helpers_accept_both_slash_and_colon(self):
        self.assertEqual(bridge._strip_route_prefix("global:zai_auto"), "zai_auto")
        self.assertEqual(bridge._strip_route_prefix("global/zai_auto"), "zai_auto")
        self.assertEqual(bridge._strip_route_prefix("cn:alpha"), "alpha")
        self.assertEqual(bridge._strip_route_prefix("alpha"), "alpha")

    def test_agent_target_is_decided_after_stripping_the_namespace(self):
        self.assertTrue(bridge._is_agent_target("cn:openclaw"))
        self.assertTrue(bridge._is_agent_target("global:openclaw/default"))
        self.assertTrue(bridge._is_agent_target("agent:foo"))
        self.assertFalse(bridge._is_agent_target("cn:alpha"))

    def test_normalize_model_never_leaks_the_namespace_downstream(self):
        self.assertEqual(bridge._normalize_model("cn:openclaw"), ("openclaw", ""))
        self.assertEqual(bridge._normalize_model("cn:alpha"), ("openclaw", "alpha"))
        self.assertEqual(bridge._normalize_model(""), ("openclaw", ""))


class ModelListTests(unittest.TestCase):
    def test_cn_models_use_colon_namespace_and_per_account_ownership(self):
        b = make_bridge(cn=[{"uid": "u1", "nickname": "一号"}, {"uid": "u2", "nickname": "二号"}],
                        sandbox={"u1": ["alpha", "beta"], "u2": ["beta", "openclaw"]})
        got = {m["id"]: m for m in b.model_list()}
        self.assertEqual(sorted(got), ["cn:alpha", "cn:beta", "cn:openclaw"])
        self.assertEqual([a["uid"] for a in got["cn:alpha"]["accounts"]], ["u1"])
        self.assertEqual([a["uid"] for a in got["cn:beta"]["accounts"]], ["u1", "u2"])
        self.assertEqual(got["cn:alpha"]["accounts"][0]["nickname"], "一号")

    def test_agent_tier_is_marked_only_for_agent_targets(self):
        b = make_bridge(cn=[{"uid": "u1"}], sandbox={"u1": ["alpha", "openclaw"]})
        got = {m["id"]: m for m in b.model_list()}
        self.assertEqual(got["cn:openclaw"].get("tier"), "agent")
        self.assertNotIn("tier", got["cn:alpha"])

    def test_global_models_are_owned_by_the_cn_accounts_that_serve_them(self):
        b = make_bridge(cn=[{"uid": "u1", "nickname": "一号"}],
                        global_accounts=[{"uid": "g1", "region": "global"}],
                        sandbox={"g1": ["zai_auto"]},
                        config={"g1": [{"id": "zai_auto", "name": "ZAI Auto", "contextWindow": 262144}]})
        got = {m["id"]: m for m in b.model_list()}
        self.assertIn("global:zai_auto", got)
        self.assertEqual([a["uid"] for a in got["global:zai_auto"]["accounts"]], ["u1"])
        self.assertEqual(got["global:zai_auto"]["context_length"], 262144)
        self.assertEqual(got["global:zai_auto"]["description"], "ZAI Auto")

    def test_disabled_and_cooling_accounts_are_not_owners(self):
        b = make_bridge(cn=[{"uid": "u1"}, {"uid": "u2", "disabled": True}, {"uid": "u3", "cooling": True}],
                        sandbox={"u1": ["alpha"]})
        got = b.model_list()
        self.assertEqual([m["id"] for m in got], ["cn:alpha"])
        self.assertEqual([a["uid"] for a in got[0]["accounts"]], ["u1"])

    def test_sandbox_is_woken_for_the_first_account_only(self):
        b = make_bridge(cn=[{"uid": "u1"}, {"uid": "u2"}], sandbox={"u1": ["alpha"], "u2": ["beta"]})
        b.model_list()
        self.assertEqual(b.up.woken, ["u1"])

    def test_static_fallback_keeps_the_same_namespace_and_tier(self):
        ids = [m["id"] for m in bridge._static_models([{"uid": "u1", "nickname": "一号"}])]
        self.assertIn("cn:openclaw", ids)
        self.assertIn("global:zai_auto", ids)
        # 命名空间分隔符只能是冒号：模型名自身允许含斜杠（cn:openclaw/default），
        # 这里锁的是不再回落到 cn/ 与 global/ 那套旧写法。
        self.assertFalse([i for i in ids if i.startswith(("cn/", "global/"))], ids)
        by_id = {m["id"]: m for m in bridge._static_models([])}
        self.assertEqual(by_id["cn:openclaw"].get("tier"), "agent")
        self.assertEqual(by_id["global:zai_auto"]["accounts"], [])

    def test_no_readable_account_falls_back_to_static_models(self):
        b = make_bridge(cn=[{"uid": "u1"}], sandbox={})
        ids = [m["id"] for m in b.model_list()]
        self.assertIn("cn:openclaw", ids)
        self.assertIn("global:zai_auto", ids)


if __name__ == "__main__":
    unittest.main()