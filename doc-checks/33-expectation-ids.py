#!/usr/bin/env python3
"""Check the independent-expectation IDs used across the project documents.

Every expectation table row starts with an ID such as `| AP01 |`. The check scans
<project>/docs/zh/**/*.md and verifies:

  ONE_OWNER   each ID prefix is defined in exactly one document
  UNIQUE      no ID is defined twice
  CONTIGUOUS  within a prefix the numbers run 01..n without gaps
  MOVED       every ID in moved-ids.txt is gone from its old prefix and exists under the new ID in
              the named document; a moved ID still counts for CONTIGUOUS in its old prefix

It also prints the prefix registry (prefix, document, count) used by protocol 21.
Usage: expectation-ids.py [PROJECT_ROOT]  (default: the current directory).
Exit status is non-zero when a check fails.
"""
import glob
import os
import re
import sys

PROJECT = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.getcwd())
SCOPES = ["docs/zh/**/*.md"]
MOVED = os.path.join(os.path.dirname(os.path.abspath(__file__)), "moved-ids.txt")
ROW = re.compile(r"^\| ([A-Z]{2})(\d{2,3}) \|", re.M)


def moved():
    if not os.path.exists(MOVED):
        return {}
    out = {}
    for l in open(MOVED, encoding="utf-8").read().splitlines():
        if l.strip() and not l.startswith("#"):
            old, new, doc = l.split()
            out[old] = (new, doc)
    return out


def main():
    moves = moved()
    owners = {}
    seen = {}
    failures = []
    for pattern in SCOPES:
        for path in sorted(glob.glob(os.path.join(PROJECT, pattern), recursive=True)):
            rel = os.path.relpath(path, PROJECT)
            text = open(path, encoding="utf-8").read()
            for prefix, num in ROW.findall(text):
                key = f"{prefix}{num}"
                owners.setdefault(prefix, set()).add(rel)
                if key in seen:
                    failures.append(f"UNIQUE: {key} in {seen[key]} and {rel}")
                seen[key] = rel
    if not seen:
        failures.append(f"EMPTY: no expectation rows under {PROJECT}/docs/zh")
    for old, (new, doc) in sorted(moves.items()):
        if old in seen:
            failures.append(f"MOVED: {old} still defined in {seen[old]}")
        if seen.get(new) != doc:
            failures.append(f"MOVED: {old} -> {new} not found in {doc}")
    for prefix, files in sorted(owners.items()):
        if len(files) > 1:
            failures.append(f"ONE_OWNER: {prefix} defined in {sorted(files)}")
        nums = sorted(int(k[2:]) for k in list(seen) + list(moves) if k.startswith(prefix))
        if nums != list(range(1, len(nums) + 1)):
            failures.append(f"CONTIGUOUS: {prefix} numbers {nums[0]}..{nums[-1]} with gaps or not starting at 01")
    print(f"{'prefix':<7} {'count':>5}  document")
    for prefix, files in sorted(owners.items()):
        count = sum(1 for k in seen if k.startswith(prefix))
        print(f"{prefix:<7} {count:>5}  {', '.join(sorted(files))}")
    print(f"{len(owners)} prefixes, {len(seen)} expectations")
    for f in failures:
        print(f)
    if failures:
        sys.exit(1)
    print("ONE_OWNER, UNIQUE, CONTIGUOUS, MOVED: ok")


if __name__ == "__main__":
    main()
