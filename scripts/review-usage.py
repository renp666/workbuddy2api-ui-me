#!/usr/bin/env python3
# review-usage.py — 定期观察 API 在各 Agent 上的使用健康度。
#
# 数据源：
#   1. core 容器日志的调用表格行（含 502/503/超时/无产出 tok=-，成功与失败都在）；
#   2. runtime/wb2api/data/usage/usage-YYYYMMDD.jsonl 用量账本（仅成功调用，含 token 量）。
#
# 用法：
#   python3 scripts/review-usage.py            # 默认最近 24h
#   python3 scripts/review-usage.py --since 72h
#   python3 scripts/review-usage.py --since 7d --project <compose项目名>
#
# 输出：按模型的调用量、错误率、错误码分布、高延迟（TTFB/total）、无产出流、
# token 消耗 Top，以及需要人工关注的异常摘要。只读，不改动任何服务。
import argparse
import json
import re
import statistics
import subprocess
import sys
from collections import Counter, defaultdict
from datetime import datetime, timedelta
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
USAGE_DIR = REPO / "runtime" / "wb2api" / "data" / "usage"

# 表格行示例：
# core-1  | | #027 | 15:49:48 | cn:hy4-prev | stream | 200 | uid=a7433fa8 | TTFB=2748ms | tok=17947 | 35.0tok/s | total=513.2s |
ROW = re.compile(
    r"\|\s*#(\d+)\s*\|\s*(\d{2}:\d{2}:\d{2})\s*\|\s*(\S+)\s*\|\s*(\w+)\s*\|\s*(\d{3})\s*\|"
    r"\s*uid=(\S*)\s*\|\s*TTFB=([\d-]+)(?:ms)?\s*\|\s*tok=([\d-]+)\s*\|\s*([\d.-]+)tok/s\s*\|\s*total=([\d.]+)s"
)
# ERR 行示例：2026-09-28 11:29:07 ERR: [upstream] chat_stream uid=xxx: transport error: ...
ERR = re.compile(r"(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) ERR: (.*)")


def parse_since(s: str) -> timedelta:
    m = re.fullmatch(r"(\d+)([hd])", s)
    if not m:
        raise argparse.ArgumentTypeError("--since 形如 24h / 7d")
    n, unit = int(m.group(1)), m.group(2)
    return timedelta(hours=n) if unit == "h" else timedelta(days=n)


def find_core_container(project: str | None) -> str:
    cmd = ["docker", "ps", "--filter", "name=core", "--format", "{{.Names}}"]
    out = subprocess.run(cmd, capture_output=True, text=True, check=True).stdout.split()
    if project:
        out = [n for n in out if n.startswith(project + "-")]
    if not out:
        sys.exit("找不到运行中的 core 容器（docker ps 无匹配）；确认栈已启动或用 --project 指定")
    return out[0]


def fetch_logs(container: str, since: timedelta) -> list[str]:
    dur = f"{int(since.total_seconds())}s"
    out = subprocess.run(
        ["docker", "logs", "--since", dur, container],
        capture_output=True, text=True, check=True,
    )
    return (out.stdout + out.stderr).splitlines()


def pct(xs, p):
    if not xs:
        return 0.0
    xs = sorted(xs)
    i = min(len(xs) - 1, int(round((p / 100) * (len(xs) - 1))))
    return xs[i]


def normalize_model(name: str, known: set[str]) -> str:
    """日志把模型名截断到 12 字符且带 realm 前缀（cn:hy4-prev），账本无前缀全名
    （hy4-preview）。去 realm 前缀后与账本名做前缀匹配归一；匹配不到保持原样。"""
    core = re.sub(r"^(cn|global):", "", name)
    if core in known:
        return core
    for k in known:
        if k.startswith(core):
            return k
    return core


def analyze_rows(lines, known_models):
    rows = []
    for line in lines:
        m = ROW.search(line)
        if not m:
            continue
        idx, clock, model, mode, status, uid, ttfb, tok, rate, total = m.groups()
        rows.append({
            "clock": clock, "model": normalize_model(model, known_models), "mode": mode, "status": int(status),
            "uid": uid[:8], "ttfb": None if ttfb == "-" else int(ttfb),
            "tok": None if tok == "-" else int(tok), "total": float(total),
        })
    return rows


def analyze_errors(lines):
    errs = []
    for line in lines:
        m = ERR.search(line)
        if m:
            errs.append((m.group(1), m.group(2)[:160]))
    return errs


def load_usage(since: timedelta):
    """账本按天文件；读取覆盖 since 窗口的文件，返回按模型聚合。"""
    cutoff = datetime.now() - since
    by_model = defaultdict(lambda: {"calls": 0, "prompt": 0, "completion": 0, "uid": Counter()})
    if not USAGE_DIR.is_dir():
        return by_model
    for f in sorted(USAGE_DIR.glob("usage-*.jsonl")):
        try:
            day = datetime.strptime(f.stem.split("-", 1)[1], "%Y%m%d")
        except (IndexError, ValueError):
            continue
        if day < cutoff.replace(hour=0, minute=0, second=0):
            continue
        for line in f.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except json.JSONDecodeError:
                continue
            if datetime.fromtimestamp(e.get("ts", 0)) < cutoff:
                continue
            m = by_model[e.get("model", "?")]
            m["calls"] += 1
            if e.get("prompt_tokens", -1) >= 0:
                m["prompt"] += e["prompt_tokens"]
            if e.get("completion_tokens", -1) >= 0:
                m["completion"] += e["completion_tokens"]
            m["uid"][e.get("uid", "?")[:8]] += 1
    return by_model


def report(rows, errs, usage, since):
    print(f"=== API 使用健康度报告（窗口：最近 {since}，生成于 {datetime.now():%Y-%m-%d %H:%M}）===\n")
    if not rows:
        print("窗口内没有解析到调用表格行（日志可能被轮转或窗口太短）。")
    models = sorted({r["model"] for r in rows} | set(usage))
    print(f"{'模型':<28}{'调用':>6}{'错误':>6}{'错误率':>8}  {'中位total':>9}{'p95 total':>10}  {'高TTFB':>7}{'无产出':>7}")
    for model in models:
        mr = [r for r in rows if r["model"] == model]
        if not mr:
            print(f"{model:<28}{usage[model]['calls']:>6}     -       -          -         -        -      -")
            continue
        errs_ = [r for r in mr if r["status"] >= 400]
        totals = [r["total"] for r in mr if r["status"] == 200]
        slow_ttfb = sum(1 for r in mr if r["ttfb"] and r["ttfb"] > 15000)
        no_out = sum(1 for r in mr if r["status"] == 200 and r["tok"] is None)
        rate = f"{len(errs_)/len(mr)*100:.1f}%" if mr else "-"
        print(f"{model:<28}{len(mr):>6}{len(errs_):>6}{rate:>8}  "
              f"{(statistics.median(totals) if totals else 0):>8.1f}s{pct(totals, 95):>9.1f}s  "
              f"{slow_ttfb:>7}{no_out:>7}")

    # 错误码分布
    code_by_model = defaultdict(Counter)
    for r in rows:
        if r["status"] >= 400:
            code_by_model[r["model"]][r["status"]] += 1
    if code_by_model:
        print("\n--- 错误码分布（非 200）---")
        for model, c in sorted(code_by_model.items()):
            print(f"  {model}: " + ", ".join(f"{code}×{n}" for code, n in sorted(c.items())))

    # 账号维度错误集中度
    uid_err = Counter()
    for r in rows:
        if r["status"] >= 400 and r["uid"]:
            uid_err[r["uid"]] += 1
    if uid_err:
        print("\n--- 错误按账号（uid 前8位）---")
        for uid, n in uid_err.most_common():
            print(f"  {uid}: {n}")

    # 上游传输错误
    if errs:
        print(f"\n--- 上游 ERR 行（{len(errs)} 条，最近 10 条）---")
        for ts, msg in errs[-10:]:
            print(f"  {ts} {msg}")

    # token 消耗
    if usage:
        print("\n--- 成功调用 token 消耗（账本）---")
        for model, m in sorted(usage.items(), key=lambda kv: -kv[1]["prompt"] - kv[1]["completion"]):
            who = ", ".join(f"{u}×{n}" for u, n in m["uid"].most_common(3))
            print(f"  {model}: calls={m['calls']} prompt={m['prompt']} completion={m['completion']} 账号[{who}]")

    # 摘要判定
    print("\n--- 摘要 ---")
    total_calls = len(rows)
    total_err = sum(1 for r in rows if r["status"] >= 400)
    no_out = sum(1 for r in rows if r["status"] == 200 and r["tok"] is None)
    slow = sum(1 for r in rows if r["ttfb"] and r["ttfb"] > 15000)
    verdict = []
    if total_calls and total_err / total_calls > 0.1:
        verdict.append(f"错误率 {total_err}/{total_calls} 偏高，优先查账号池可用性与上游状态")
    if no_out > 3:
        verdict.append(f"{no_out} 笔流式无产出（tok=-），疑似上游挂死/掐流，对应客户端『Stream ended without finish_reason』")
    if slow > 3:
        verdict.append(f"{slow} 笔 TTFB>15s，上游排队或劣化，长对话易触发客户端超时与压缩失败")
    if not verdict:
        verdict.append("窗口内未见系统性异常")
    for v in verdict:
        print("  * " + v)


def main():
    ap = argparse.ArgumentParser(description="网关 API 使用健康度观察（只读）")
    ap.add_argument("--since", type=parse_since, default=parse_since("24h"), help="观察窗口，如 24h / 7d（默认 24h）")
    ap.add_argument("--project", default=None, help="compose 项目名前缀（多栈并存时定位 core 容器）")
    args = ap.parse_args()
    container = find_core_container(args.project)
    lines = fetch_logs(container, args.since)
    usage = load_usage(args.since)
    rows = analyze_rows(lines, set(usage))
    errs = analyze_errors(lines)
    report(rows, errs, usage, args.since)


if __name__ == "__main__":
    main()
