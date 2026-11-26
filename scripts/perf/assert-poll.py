#!/usr/bin/env python3
"""Assert a poll-harness result (scripts/perf/poll-harness.mjs) against the 002 expectations.

Usage: python3 -I scripts/perf/assert-poll.py <poll.json> --expect baseline|target

baseline (research.md section 2, STOP-1):
  idle REST/minute per route = 2.0 (dashboard "/" = 4.0), tolerance +-0.5;
  "/apps/" initial requests = N + 3 (N = apps in the run).
target (SC-001, SC-002, SC-003, SC-005):
  idle = 0, focus = 0, reconnect = 0 on every route; "/apps/" initial <= 3.
  restart (only when the run used --restart, RV-20; SC-003 "server restart"): recovery
  requests per route <= the page's active queries without the session and without the
  per-app service status (= distinct endpoints of the route's initial load minus those two),
  and "/apps/" <= 2.

Exit 0 when every route matches; exit 1 and print every violating route otherwise.
"""
import argparse
import json
import sys

TOL = 0.5
BASELINE_IDLE = {"/": 4.0}
DEFAULT_IDLE = 2.0
TARGET_APPS_INITIAL = 3
TARGET_APPS_RESTART = 2
EXCLUDED_FROM_ACTIVE = ("GET /admin/api/v1/session", "GET /admin/api/v1/apps/:id/service/status")


def restart_limit(route, r):
    """Active queries of the page without session and per-app service status (SC-003)."""
    active = [k for k in r.get("initialBreakdown", {}) if not k.startswith(EXCLUDED_FROM_ACTIVE)]
    limit = len(active)
    if route == "/apps/":
        limit = min(limit, TARGET_APPS_RESTART)
    return limit


def check_baseline(data):
    problems = []
    n = data["n"]
    for route, r in sorted(data["results"].items()):
        want = BASELINE_IDLE.get(route, DEFAULT_IDLE)
        got = r["idlePerMin"]
        if abs(got - want) > TOL:
            problems.append(f"{route}: idlePerMin {got} != {want} +-{TOL} ({r.get('idleBreakdown')})")
    apps = data["results"].get("/apps/")
    if apps is None:
        problems.append("/apps/: missing from the result")
    elif apps["initial"] != n + 3:
        problems.append(f"/apps/: initial {apps['initial']} != N+3 = {n + 3} ({apps.get('initialBreakdown')})")
    return problems


def check_target(data):
    problems = []
    for route, r in sorted(data["results"].items()):
        for key in ("idle", "focus", "reconnect"):
            if r[key] != 0:
                problems.append(f"{route}: {key} {r[key]} != 0 ({r.get(key + 'Breakdown')})")
        if "restart" in r:
            limit = restart_limit(route, r)
            if r["restart"] > limit:
                problems.append(f"{route}: restart {r['restart']} > {limit} ({r.get('restartBreakdown')})")
    apps = data["results"].get("/apps/")
    if apps is None:
        problems.append("/apps/: missing from the result")
    elif apps["initial"] > TARGET_APPS_INITIAL:
        problems.append(f"/apps/: initial {apps['initial']} > {TARGET_APPS_INITIAL} ({apps.get('initialBreakdown')})")
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("poll_json", help="output of scripts/perf/poll-harness.mjs")
    ap.add_argument("--expect", choices=("baseline", "target"), required=True)
    args = ap.parse_args()
    with open(args.poll_json, encoding="utf-8") as f:
        data = json.load(f)
    problems = check_baseline(data) if args.expect == "baseline" else check_target(data)
    print(f"{args.poll_json}: N={data['n']} window={data['windowSeconds']} s base={data.get('base')} expect={args.expect}")
    for route, r in sorted(data["results"].items()):
        extra = f" restart={r['restart']:>3} (<= {restart_limit(route, r)})" if "restart" in r else ""
        print(f"  {route:22} initial={r['initial']:>3} idlePerMin={r['idlePerMin']:>5} focus={r['focus']:>3} reconnect={r['reconnect']:>3}{extra}")
    if problems:
        print(f"FAIL: {len(problems)} violation(s)")
        for p in problems:
            print(f"  {p}")
        return 1
    print("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
