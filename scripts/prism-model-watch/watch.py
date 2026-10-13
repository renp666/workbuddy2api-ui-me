#!/usr/bin/env python3
"""prism-model-watch: gateway alias rotation watchdog.

Alerts when the Prism gateway (workbuddy2api) model aliases silently change:
  - an alias disappears from /v1/models
  - an alias's routed_model changes
  - a minimal real probe against an alias fails

Exit codes:
  0  all green (silent by default, for cron --no-agent)
  1  alias set / routing baseline changed
  2  one or more alias probes failed
  3  gateway unreachable / config error

Baseline snapshot is a JSON file (default next to this script):
  scripts/prism-model-watch/baseline.json
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

DEFAULT_BASE_URL = "http://127.0.0.1:7863/v1"
DEFAULT_BASELINE = Path(__file__).resolve().parent / "baseline.json"
DEFAULT_CONFIG_ENV = r"D:\AgentData\Hermes\scripts\hermes-wf-gofish\config.env"
DEFAULT_TRAE_KEY = "WF_TRAE_MODEL"


def _req(url, key, payload=None, timeout=30):
    data = None
    headers = {"Authorization": "Bearer " + key, "Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method="POST" if data else "GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.status, json.loads(resp.read().decode("utf-8", "replace"))


def fetch_aliases(base_url, key, timeout):
    status, body = _req(base_url + "/models", key, timeout=timeout)
    items = body.get("data", body if isinstance(body, list) else [])
    aliases = {}
    for it in items:
        if it.get("alias") is not True:
            # Only gateway aliases (1-daily-v1 … 6-try-v2). Real channel ids and
            # the gateway-auto virtual model are out of scope: the latter would
            # trigger the auto router on every probe.
            continue
        mid = it.get("id")
        if not mid:
            continue
        aliases[mid] = {
            "realm": (it.get("realm") or it.get("owned_by") or ""),
            "routed_model": (it.get("routed_model") or it.get("root") or it.get("model") or ""),
        }
    return aliases


def probe_alias(base_url, key, alias, timeout):
    payload = {
        "model": alias,
        "messages": [{"role": "user", "content": "ping"}],
        # 32 而不是 1：部分模型族（如 gpt-5.x）对 max_tokens 有下限，给 1 会被上游
        # 以 400 integer_below_min_value 拒绝，别名本身没问题却被判成探针失败。
        # 探针只校验 HTTP 200，不校验内容，故 32 足够绕开下限又不拖长生成。
        "max_tokens": 32,
        "temperature": 0,
    }
    t0 = time.time()
    try:
        status, _ = _req(base_url + "/chat/completions", key, payload=payload, timeout=timeout)
        return status == 200, round(time.time() - t0, 2), None
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace")[:200]
        return False, round(time.time() - t0, 2), "HTTP %s: %s" % (e.code, detail)
    except Exception as e:  # noqa: BLE001
        return False, round(time.time() - t0, 2), "%s: %s" % (type(e).__name__, e)


def load_baseline(path):
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except Exception:
        return None


def save_baseline(path, aliases):
    Path(path).write_text(
        json.dumps(aliases, indent=2, sort_keys=True, ensure_ascii=False), encoding="utf-8"
    )


def read_trae_model(config_env):
    try:
        for line in Path(config_env).read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if line.startswith(DEFAULT_TRAE_KEY + "="):
                raw = line.split('=', 1)[1].split('#', 1)[0].strip()
                if len(raw) >= 2 and raw[0] == raw[-1] and raw[0] in (chr(34), chr(39)):
                    raw = raw[1:-1]
                return raw
    except Exception:
        return None
    return None


def repin(config_env, key_name, new_alias, apply, old_alias=""):
    import shutil

    path = Path(config_env)
    lines = path.read_text(encoding="utf-8").splitlines()
    changed = False
    old = old_alias
    for i, line in enumerate(lines):
        if line.strip().startswith(key_name + "="):
            stripped = line.strip()
            head, sep, comment = stripped.partition("#")
            if not old:
                old = head.split("=", 1)[1].strip().strip('"').strip("'")
            # Keep the original trailing comment (the change history) and record
            # this repin on the same line, so nothing is lost on a rewrite.
            tail = ("  " + sep + comment) if sep else ""
            lines[i] = '%s="%s"  # repin %s -> %s%s' % (key_name, new_alias, old, new_alias, tail)
            changed = True
    if not changed:
        return False, "%s not found in %s" % (key_name, config_env)
    if not apply:
        return True, "[dry-run] would repin %s: %s -> %s in %s" % (key_name, old, new_alias, config_env)
    backup = "%s.bak-repin-%s" % (config_env, time.strftime("%Y%m%d-%H%M%S"))
    shutil.copy2(config_env, backup)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return True, "repinned %s: %s -> %s in %s (backup %s)" % (key_name, old, new_alias, config_env, backup)


def main(argv=None):
    ap = argparse.ArgumentParser(description="Prism gateway alias rotation watchdog")
    ap.add_argument("--base-url", default=os.environ.get("PRISM_BASE_URL", DEFAULT_BASE_URL))
    ap.add_argument("--key-env", default="WB2A_API_KEY")
    ap.add_argument("--key-file", default=r"D:\AgentData\Hermes\.env")
    ap.add_argument("--baseline", default=str(DEFAULT_BASELINE))
    ap.add_argument("--update-baseline", action="store_true", help="record current state as new baseline and exit")
    ap.add_argument("--requests", type=int, default=1, help="probe attempts per alias (must all pass)")
    ap.add_argument("--timeout", type=float, default=30.0)
    ap.add_argument(
        "--qoder-timeout",
        type=float,
        default=60.0,
        help="probe timeout for qoder-realm aliases (they run a CLI subprocess; 30s is too tight)",
    )
    ap.add_argument("--config-env", default=DEFAULT_CONFIG_ENV)
    ap.add_argument("--auto-repin", action="store_true", help="GATED: rewrite WF_TRAE_MODEL when its own alias fails")
    ap.add_argument("--apply", action="store_true", help="with --auto-repin, actually write (default dry-run)")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args(argv)

    key = os.environ.get(args.key_env, "").strip()
    if not key and args.key_file:
        for line in Path(args.key_file).read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if line.startswith(args.key_env + "="):
                key = line.split("=", 1)[1].strip().strip('"').strip("'")
                break
    if not key:
        print("ALERT config: no API key (env %s empty, and not found in %s)" % (args.key_env, args.key_file), file=sys.stderr)
        return 3

    try:
        aliases = fetch_aliases(args.base_url, key, args.timeout)
    except Exception as e:  # noqa: BLE001
        print("ALERT gateway: %s unreachable: %s: %s" % (args.base_url, type(e).__name__, e), file=sys.stderr)
        return 3

    if not aliases:
        print("ALERT gateway: /v1/models returned no aliases", file=sys.stderr)
        return 3

    if args.update_baseline:
        save_baseline(args.baseline, aliases)
        print("baseline updated: %d aliases -> %s" % (len(aliases), args.baseline), file=sys.stderr)
        return 0

    problems = []
    disabled = []
    baseline = load_baseline(args.baseline)
    if baseline is None:
        # First run / fresh deploy: seed the baseline and carry on instead of
        # alerting every tick until someone remembers to run --update-baseline.
        save_baseline(args.baseline, aliases)
        print(
            "baseline initialized: %d aliases -> %s" % (len(aliases), args.baseline),
            file=sys.stderr,
        )
        baseline = aliases
    else:
        for name, meta in baseline.items():
            if name not in aliases:
                problems.append("alias disappeared: %s (was -> %s)" % (name, meta.get("routed_model")))
            elif (aliases[name].get("routed_model") or "") != (meta.get("routed_model") or ""):
                problems.append(
                    "routing changed: %s %r -> %r"
                    % (name, meta.get("routed_model"), aliases[name].get("routed_model"))
                )
        for name in aliases:
            if name not in baseline:
                problems.append("alias appeared: %s -> %s" % (name, aliases[name].get("routed_model")))

    # Billed tier: the user's standing rule forbids using the 3-strong group
    # without consent, so never liveness-probe it here (the membership/routing
    # diff above still covers it for free). Re-add a name here only if the user
    # agrees.
    costly = {"3-strong-v1", "3-strong-v2"}

    trae = read_trae_model(args.config_env)
    failed = {}
    skipped = []
    for name in sorted(aliases):
        if name in costly:
            skipped.append(name)
            continue
        ok = True
        detail = ""
        # qoder aliases route to a CLI-subprocess proxy whose real latency is
        # 12-43s; a 30s probe would report them as timeouts while they work.
        # Give the qoder realm its own (wider) timeout, keep 30s for the rest.
        probe_timeout = (
            args.qoder_timeout if (aliases[name].get("realm") or "") == "qoder" else args.timeout
        )
        for _ in range(max(1, args.requests)):
            ok, dt, err = probe_alias(args.base_url, key, name, probe_timeout)
            if not ok:
                detail = "%s (%ss)" % (err, dt)
                break
        if ok:
            if args.verbose:
                print("ok: %s" % name)
            continue
        # An alias switched off in the console answers 404 model_disabled. That
        # is an admin choice, not an upstream rotation failure, so don't cry wolf
        # on it -- unless it is the alias the dispatch chain is pinned to, which
        # would be a real breakage.
        if "model_disabled" in detail and name != trae:
            disabled.append(name)
            continue
        failed[name] = detail
        problems.append("probe failed: %s: %s" % (name, detail))
    if skipped and args.verbose:
        print("skipped (billed tier, not probed): %s" % ", ".join(skipped))
    if disabled and args.verbose:
        print("disabled (console switch, not alerted): %s" % ", ".join(sorted(disabled)))

    # Pinned alias is broken if its probe failed OR it vanished from /v1/models
    # entirely (the most common rotation shape: the alias is dropped upstream).
    pinned_broken = bool(trae) and (trae in failed or trae not in aliases)
    if args.auto_repin and pinned_broken:
        fallback = next(
            (n for n in sorted(aliases) if n not in failed and n != trae and n not in costly and n not in disabled),
            None,
        )
        if fallback:
            _ok, msg = repin(args.config_env, DEFAULT_TRAE_KEY, fallback, args.apply, old_alias=trae)
            problems.append("auto-repin: %s" % msg)
        else:
            problems.append("auto-repin: no healthy fallback alias available (pinned %s broken)" % trae)

    if problems:
        print("PRISM MODEL WATCH: anomaly detected")
        for p in problems:
            print(" - %s" % p)
        return 2 if failed else 1

    if args.verbose:
        print("all green: %d aliases healthy (trae=%s)" % (len(aliases), trae))
    return 0


if __name__ == "__main__":
    sys.exit(main())
