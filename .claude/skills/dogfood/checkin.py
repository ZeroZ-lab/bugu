"""Dogfood 巡检:只读地查看一段时间内的运行记录,找出值得追问的异常,输出 JSON。

用法:python3 -I checkin.py [--since 2026-11-13T09:00] [--bugu bugu]
不给 --since 时看最近 3 天。只通过 `bugu ... --json` 读数据,不改任何东西。
"""
import argparse
import json
import statistics
import subprocess
import sys
from datetime import datetime, timedelta, timezone


def bugu(cmd, *args):
    out = subprocess.run([cmd, *args, "--json"], capture_output=True, text=True)
    if out.returncode != 0:
        try:
            err = json.loads(out.stderr)["error"]
        except (ValueError, KeyError):
            err = {"code": "error", "message": out.stderr.strip()}
        sys.exit(json.dumps({"error": err}, ensure_ascii=False))
    return json.loads(out.stdout)


def ts(s):
    if not s or s.startswith("0001-"):
        return None
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--since", help="ISO 时间,默认 3 天前")
    p.add_argument("--bugu", default="bugu", help="bugu 程序的路径")
    a = p.parse_args()
    now = datetime.now(timezone.utc)
    since = datetime.fromisoformat(a.since).astimezone(timezone.utc) if a.since else now - timedelta(days=3)

    tasks = bugu(a.bugu, "list")
    runs = bugu(a.bugu, "runs", "-n", "2000")
    inbox = bugu(a.bugu, "inbox")
    anomalies = []

    def flag(kind, task, run, detail):
        anomalies.append({"kind": kind, "task": task, "run": run, "detail": detail})

    in_window = [r for r in runs if (ts(r.get("started_at")) or ts(r.get("created_at")) or now) >= since]
    by_task = {}
    for r in in_window:
        by_task.setdefault(r["task"], []).append(r)

    # 1. 结果不好的运行。
    for r in in_window:
        s = r["status"]
        if s in ("failed", "timed_out", "interrupted"):
            flag(s, r["task"], r["id"], r.get("error") or r.get("note") or "")
        elif s in ("missed", "skipped"):
            flag(s, r["task"], r["id"], r.get("note") or "")

    # 2. 同一个原定时间触发了两次(重复跑)。
    seen = {}
    for r in in_window:
        key = (r["task"], r.get("scheduled_for"))
        if r.get("trigger") == "schedule" and key[1]:
            if key in seen:
                flag("duplicate", r["task"], r["id"], f"和 {seen[key]} 的原定时间相同:{key[1]}")
            seen[key] = r["id"]

    # 3. 失败了,但设置过的通知一条也没送达。
    for r in in_window:
        if r["status"] in ("failed", "timed_out"):
            deliveries = bugu(a.bugu, "logs", r["id"])["deliveries"]
            if deliveries and not any(d["status"] == "delivered" for d in deliveries):
                flag("unnotified", r["task"], r["id"], "; ".join(f'{d["channel"]}:{d["status"]} {d.get("error", "")}' for d in deliveries))

    # 4. 可能漏跑:下次运行时间已经过去,或者距上次定时运行远超平时的间隔。
    all_by_task = {}
    for r in runs:
        if r.get("trigger") == "schedule" and ts(r.get("scheduled_for")):
            all_by_task.setdefault(r["task"], []).append(ts(r["scheduled_for"]))
    for t in tasks:
        if t.get("paused"):
            continue
        nxt = ts(t.get("next_run"))
        if nxt and nxt < now - timedelta(minutes=2):
            flag("overdue", t["name"], None, f"下次运行时间 {t['next_run']} 已经过去")
        times = sorted(all_by_task.get(t["name"], []))
        if len(times) >= 3:
            gaps = [(b - a_).total_seconds() for a_, b in zip(times, times[1:])]
            typical = statistics.median(gaps)
            if typical > 0 and (now - times[-1]).total_seconds() > 2.5 * typical:
                flag("gap", t["name"], None, f"上次定时运行在 {times[-1].isoformat()},平时约每 {typical / 3600:.1f} 小时一次")

    summary = []
    for t in tasks:
        rs = by_task.get(t["name"], [])
        counts = {}
        for r in rs:
            counts[r["status"]] = counts.get(r["status"], 0) + 1
        summary.append({"task": t["name"], "paused": bool(t.get("paused")), "runs": len(rs), "statuses": counts, "next_run": t.get("next_run")})

    print(json.dumps({
        "window": {"since": since.isoformat(), "until": now.isoformat()},
        "tasks": summary,
        "unread": inbox.get("unread", 0),
        "anomalies": anomalies,
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
