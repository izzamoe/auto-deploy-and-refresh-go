#!/usr/bin/env python3
"""Aggregate `next analyze export --route R` JSON Lines: client-side parts grouped by
package (node_modules/<pkg>) or by src directory, sync vs async (group kind).
Usage: python3 -I scripts/perf/analyze-parts.py <export.jsonl>
Moved from reference/spike/analyze_parts.py (T002); logic unchanged.
"""
import collections
import json
import re
import sys


def bucket(src):
    m = re.search(r"node_modules/((?:@[^/]+/)?[^/]+)", src)
    if m:
        return "npm:" + m.group(1)
    m = re.search(r"web/admin/src/(.+)$", src)
    if m:
        parts = m.group(1).split("/")
        return "src:" + "/".join(parts[:2]) if len(parts) > 2 else "src:" + m.group(1)
    return "other:" + src[-50:]


def main():
    if len(sys.argv) != 2 or sys.argv[1] in ("-h", "--help"):
        print(__doc__)
        return 0 if len(sys.argv) == 2 else 2
    async_outputs = set()
    parts = []
    for line in open(sys.argv[1], encoding="utf-8"):
        line = line.strip()
        if not line.startswith("{"):
            continue
        r = json.loads(line)
        if r["type"] == "group" and r.get("kind") == "async":
            async_outputs.update(r.get("outputs", []))
        elif r["type"] == "part":
            parts.append(r)
    agg = collections.defaultdict(lambda: [0, 0])
    tot = {"sync": 0, "async": 0}
    for p in parts:
        fn = p["filename"]
        if "/static/chunks/" not in fn:  # client only
            continue
        kind = "async" if fn in async_outputs else "sync"
        key = (kind, bucket(p["source_path"]))
        agg[key][0] += int(p["size"])
        agg[key][1] += int(p["compressed_size"])
        tot[kind] += int(p["compressed_size"])
    print("totals compressed:", tot)
    for (kind, b), (raw, gz) in sorted(agg.items(), key=lambda x: -x[1][1])[:45]:
        print(f"{kind:5} {b:60} raw={raw:>8} cmp={gz:>7}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
