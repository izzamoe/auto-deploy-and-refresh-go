#!/usr/bin/env python3
"""Gate on a transfer-harness.mjs result (002 T074, RV-20, SC-009).

transfer-harness.mjs always exits 0; this script turns its JSON into a pass/fail:

  python3 -I scripts/perf/assert-transfer.py <transfer.json> --route '/apps/edit/?id=x' --max-js 215000
                                             [--baseline <transfer-before.json>]

`load:js` bytes are the response body sizes on the wire (Playwright `sizes().responseBodySize`,
i.e. gzip when the server compressed). Exit 1 when the route's `load:js` exceeds --max-js, or
(with --baseline) when any route's `load:js` is larger than in the baseline measured with the
same tool. Every value is printed.
"""
import argparse
import json
import sys


def load_js(routes, route):
    entry = routes.get(route)
    if entry is None:
        return None
    return entry.get("load:js", {}).get("bytes", 0)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("result", help="transfer-harness.mjs output JSON")
    ap.add_argument("--route", required=True, help="route key as in the harness, e.g. '/apps/edit/?id=x'")
    ap.add_argument("--max-js", type=int, required=True, help="max load:js bytes (wire) for --route")
    ap.add_argument("--baseline", help="transfer JSON of the baseline (same tool): no route may grow")
    args = ap.parse_args()

    routes = json.load(open(args.result, encoding="utf-8"))["routes"]
    base = json.load(open(args.baseline, encoding="utf-8"))["routes"] if args.baseline else {}
    failures = []

    print(f"{'route':24s} {'load:js (B)':>12s} {'baseline (B)':>13s}")
    for r in routes:
        cur, old = load_js(routes, r), load_js(base, r)
        print(f"{r:24s} {cur:12d} {'' if old is None else old:>13}")
        if old is not None and cur > old:
            failures.append(f"{r} load:js grew: {cur} B > baseline {old} B")

    got = load_js(routes, args.route)
    if got is None:
        failures.append(f"route {args.route} not in {args.result}")
    elif got > args.max_js:
        failures.append(f"{args.route} load:js {got} B > {args.max_js} B (SC-009)")

    if failures:
        print(f"FAIL: {len(failures)} violation(s)")
        for f in failures:
            print("  " + f)
        return 1
    print(f"OK: {args.route} load:js {got} B <= {args.max_js} B")
    return 0


if __name__ == "__main__":
    sys.exit(main())
