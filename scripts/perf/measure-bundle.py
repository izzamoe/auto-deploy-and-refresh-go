#!/usr/bin/env python3
"""Measure initial JS per route of the Next static export (002 baseline / NFR-005 bundle gate).

Usage:
  python3 -I scripts/perf/measure-bundle.py <out-dir> [--json]
  python3 -I scripts/perf/measure-bundle.py <out-dir> --write-baseline <baseline.json>
  python3 -I scripts/perf/measure-bundle.py <out-dir> --check <baseline.json>
                                            [--target-module <B>] [--target-all <B>]

For every */index.html under <out-dir> it collects <script src> (and modulepreload /
<link rel=preload as=script>) URLs, resolves them under <out-dir> (basePath /admin is
stripped), and sums raw, gzip -9 and brotli -q 11 sizes. Also prints inline <script>
count (CSP hash list) and the largest chunks overall.

Columns: js_gz = gzip -9 of every <script src> (SC-007 b, "all tags"); gz_mod = the same
without the `noModule` scripts a modern browser never downloads (SC-007 a, "effective").

--write-baseline writes {route: {scripts, js_gzip, js_gzip_module, ...}} (route = directory
of the HTML file, e.g. "/apps/edit/").
--check exits 1 when any route's js_gzip or js_gzip_module is larger than in the baseline
(NFR-005: no route may grow), or when the worst route exceeds --target-module (js_gzip_module)
or --target-all (js_gzip). Every violation is printed with the route and the byte values.
Moved from reference/spike/measure_bundle.py (T002); the measurement itself is unchanged.
"""
import argparse
import gzip
import json
import os
import re
import sys

try:
    import brotli  # type: ignore
except Exception:  # pragma: no cover
    brotli = None

BASE = "/admin"


def sizes(path, cache={}):
    if path in cache:
        return cache[path]
    with open(path, "rb") as f:
        data = f.read()
    gz = len(gzip.compress(data, 9))
    br = len(brotli.compress(data, quality=11)) if brotli else -1
    cache[path] = (len(data), gz, br)
    return cache[path]


def measure(out):
    script_re = re.compile(r'<script[^>]*\bsrc="([^"]+)"', re.I)
    nomodule_re = re.compile(r'<script[^>]*\bsrc="([^"]+)"[^>]*nomodule', re.I)
    preload_re = re.compile(r'<link[^>]*rel="(?:modulepreload|preload)"[^>]*as="script"[^>]*href="([^"]+)"|<link[^>]*href="([^"]+)"[^>]*as="script"', re.I)
    inline_re = re.compile(r'<script(?![^>]*\bsrc=)[^>]*>', re.I)
    css_re = re.compile(r'<link[^>]*rel="stylesheet"[^>]*href="([^"]+)"', re.I)
    routes = {}
    for root, _dirs, files in os.walk(out):
        for fn in files:
            if fn != "index.html" and not (fn.endswith(".html") and root == out):
                continue
            p = os.path.join(root, fn)
            rel = "/" + os.path.relpath(p, out)
            html = open(p, encoding="utf-8").read()
            urls = []
            for m in script_re.finditer(html):
                urls.append(m.group(1))
            for m in preload_re.finditer(html):
                u = m.group(1) or m.group(2)
                if u and u not in urls:
                    urls.append(u)
            nomod = {m.group(1) for m in nomodule_re.finditer(html)}
            css = [m.group(1) for m in css_re.finditer(html)]

            def resolve(u):
                u = u.split("?")[0]
                if u.startswith(BASE):
                    u = u[len(BASE):]
                return os.path.join(out, u.lstrip("/"))
            tot = [0, 0, 0]
            mod_gz = 0
            files_ = []
            for u in urls:
                fp = resolve(u)
                if not os.path.exists(fp):
                    continue
                s = sizes(fp)
                files_.append((u, s))
                for i in range(3):
                    tot[i] += s[i]
                if u not in nomod:
                    mod_gz += s[1]
            ctot = [0, 0, 0]
            for u in css:
                fp = resolve(u)
                if os.path.exists(fp):
                    s = sizes(fp)
                    for i in range(3):
                        ctot[i] += s[i]
            routes[rel] = {
                "scripts": len(files_),
                "js_raw": tot[0], "js_gzip": tot[1], "js_br": tot[2],
                "js_gzip_module": mod_gz, "nomodule": sorted(nomod),
                "css_raw": ctot[0], "css_gzip": ctot[1],
                "inline_scripts": len(inline_re.findall(html)),
                "html_raw": len(html.encode()),
                "chunks": [[u, s[0], s[1]] for u, s in files_],
            }
    return routes


def route_name(rel):
    """'/apps/edit/index.html' -> '/apps/edit/'; '/index.html' -> '/'; '/404.html' stays."""
    if rel.endswith("/index.html"):
        return rel[: -len("index.html")]
    return rel


def print_table(routes):
    print(f"{'route':34} {'n':>3} {'js_raw':>9} {'js_gz':>8} {'gz_mod':>8} {'css_gz':>7} {'inl':>4}")
    for r in sorted(routes):
        v = routes[r]
        print(f"{r:34} {v['scripts']:>3} {v['js_raw']:>9} {v['js_gzip']:>8} {v['js_gzip_module']:>8} {v['css_gzip']:>7} {v['inline_scripts']:>4}")
    allchunks = {}
    for v in routes.values():
        for u, raw, gz in v["chunks"]:
            allchunks.setdefault(u, [raw, gz, 0])[2] += 1
    print("\nlargest chunks (raw, gzip, #routes):")
    for u, (raw, gz, n) in sorted(allchunks.items(), key=lambda x: -x[1][1])[:20]:
        print(f"  {u:70} {raw:>8} {gz:>7} {n:>3}")


def summary(routes):
    return {
        route_name(r): {k: v[k] for k in ("scripts", "js_raw", "js_gzip", "js_gzip_module", "css_gzip", "inline_scripts")}
        for r, v in routes.items()
    }


def check(current, baseline, target_module, target_all):
    problems = []
    for r in sorted(current):
        cur = current[r]
        base = baseline.get(r)
        if base is None:
            print(f"note: {r} has no baseline entry (new route)")
            continue
        for key in ("js_gzip_module", "js_gzip"):
            if cur[key] > base[key]:
                problems.append(f"{r} {key} grew: {cur[key]} B > baseline {base[key]} B (+{cur[key] - base[key]} B)")
    for r in sorted(baseline):
        if r not in current:
            print(f"note: baseline route {r} is missing from the build")
    if target_module is not None:
        for r in sorted(current):
            if current[r]["js_gzip_module"] > target_module:
                problems.append(f"{r} js_gzip_module {current[r]['js_gzip_module']} B > target {target_module} B (SC-007a)")
    if target_all is not None:
        for r in sorted(current):
            if current[r]["js_gzip"] > target_all:
                problems.append(f"{r} js_gzip {current[r]['js_gzip']} B > target {target_all} B (SC-007b)")
    worst_mod = max(current.items(), key=lambda kv: kv[1]["js_gzip_module"])
    worst_all = max(current.items(), key=lambda kv: kv[1]["js_gzip"])
    print(f"worst js_gzip_module: {worst_mod[0]} {worst_mod[1]['js_gzip_module']} B; worst js_gzip: {worst_all[0]} {worst_all[1]['js_gzip']} B")
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("out", help="Next static export directory (web/admin/out)")
    ap.add_argument("--json", action="store_true", help="print the full per-HTML measurement as JSON")
    ap.add_argument("--write-baseline", metavar="JSON", help="write the per-route summary to JSON")
    ap.add_argument("--check", metavar="JSON", help="compare against a baseline written by --write-baseline")
    ap.add_argument("--target-module", type=int, metavar="B", help="max js_gzip_module per route (SC-007a)")
    ap.add_argument("--target-all", type=int, metavar="B", help="max js_gzip per route (SC-007b)")
    args = ap.parse_args()
    if (args.target_module is not None or args.target_all is not None) and not args.check:
        ap.error("--target-module/--target-all need --check")

    routes = measure(args.out)
    if not routes:
        print(f"error: no HTML found under {args.out}", file=sys.stderr)
        return 2
    if args.json:
        print(json.dumps(routes, indent=1, sort_keys=True))
        return 0
    print_table(routes)
    cur = summary(routes)
    if args.write_baseline:
        with open(args.write_baseline, "w", encoding="utf-8") as f:
            json.dump(cur, f, indent=1, sort_keys=True)
            f.write("\n")
        print(f"\nbaseline written: {args.write_baseline} ({len(cur)} routes)")
    if args.check:
        with open(args.check, encoding="utf-8") as f:
            baseline = json.load(f)
        print()
        problems = check(cur, baseline, args.target_module, args.target_all)
        if problems:
            print(f"\nFAIL: {len(problems)} violation(s)")
            for p in problems:
                print(f"  {p}")
            return 1
        print("\nOK: no route grew" + (" and every route is within the targets" if (args.target_module or args.target_all) else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
