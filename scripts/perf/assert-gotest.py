#!/usr/bin/env python3
"""Assert a `go test -v` log: every named test or subtest passed and nothing failed.

Usage: python3 -I scripts/perf/assert-gotest.py <go-test-v.log> <Name> [<Name> ...]

A name is matched against the `--- PASS: <name> (` lines of `go test -v`, so a subtest is
written as `Parent/sub` (the name go test prints, e.g. `TestEventsV3_Contract/hello`).
The parent passing does NOT count for a subtest (RV2-11).

Exit 1 (and print why) when:
  - the log has a `--- FAIL:` line or a `FAIL` package line;
  - the log says `no tests to run` (a -run pattern that matched nothing is a failure);
  - any named test has no `--- PASS: <name>` line.
Exit 0 otherwise; prints the number of `--- PASS` lines found per name.
"""
import argparse
import re
import sys


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("log", help="output of go test -v")
    ap.add_argument("names", nargs="+", help="tests/subtests that must pass")
    args = ap.parse_args()

    with open(args.log, encoding="utf-8", errors="replace") as f:
        text = f.read()

    problems = []
    fails = re.findall(r"^\s*--- FAIL: (\S+)", text, re.M)
    for name in fails:
        problems.append(f"--- FAIL: {name}")
    if re.search(r"^FAIL\b", text, re.M):
        problems.append("a FAIL package line is present")
    if "no tests to run" in text:
        problems.append("`no tests to run` (the -run pattern matched nothing)")

    passed = {}
    for name in re.findall(r"^\s*--- PASS: (\S+) \(", text, re.M):
        passed[name] = passed.get(name, 0) + 1
    for name in args.names:
        n = passed.get(name, 0)
        print(f"{name}: {n} --- PASS")
        if n == 0:
            problems.append(f"no --- PASS: {name}")

    print(f"total --- PASS lines: {sum(passed.values())}; --- FAIL lines: {len(fails)}")
    if problems:
        for p in problems:
            print("VIOLATION:", p)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
