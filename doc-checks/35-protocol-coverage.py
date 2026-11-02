#!/usr/bin/env python3
r"""Which wire-protocol rules a test guards, per implementation.

Every rule row of be-protocol's spec (`| P6.16 | 必须 | ... | CK-BUS-01 |`) names how it is checked in its last
column: check IDs (`CK-BUS-01`, a range `CK-AU-01 … 05`), `SDK 测试`, `向量 \`name\``, or something not run by a
test (`装配检查`, `SDK 静态检查`, `—`). A rule counts as guarded by an implementation when one of its test files names
the rule (`P6.16`, or a range `P6.16-P6.24`) or one of the rule's check IDs (`CK-BUS-03`, or `CK-BUS-01..08`); a
vector rule when a test file of that implementation names the vector. Implementations: go (be-sdk-go's *_test.go
and its test-only packages, whose names end in "test": the transport suite is events/transporttest),
py (be-sdk-python's tests/), suite (be-acceptance's black-box conformance suite, conformance/profile/).

Naming is a proxy: a test that names a rule may not check it. It finds the rules nobody even claims to test.

Usage: 35-protocol-coverage.py [PROJECT_ROOT] [--chapters 05,06] [--require go,suite] [--markdown]
  --chapters   spec chapters to look at (default: all)
  --require    the rules in scope must be guarded by at least one of these (default: go); exit 1 when any is not
  --markdown   print the table of every rule instead of the summary
"""
import argparse
import glob
import os
import re
import sys

RULE = re.compile(r"^\| (P\d+\.\d+) \| ([^|]*) \|(.*)\| ([^|]*) \|\s*$")
CK = re.compile(r"CK-([A-Z]+)-(\d+)(?:\s*(?:…|\.\.|-|–)\s*(\d+))?")
P = re.compile(r"\bP(\d+)\.(\d+)(?:\s*(?:…|\.\.|-|–)\s*P?(\d+)\.(\d+))?")
VECTOR = re.compile(r"向量 `([a-z0-9-]+)`")
NOT_RUN = ("装配检查", "SDK 静态检查", "—", "")


def expand_ck(text):
    out = set()
    for kind, a, b in CK.findall(text):
        for n in range(int(a), int(b or a) + 1):
            out.add(f"CK-{kind}-{n:02d}")
    return out


def expand_p(text):
    out = set()
    for c1, n1, c2, n2 in P.findall(text):
        if c2 and c2 == c1:
            for n in range(int(n1), int(n2) + 1):
                out.add(f"P{c1}.{n}")
        else:
            out.add(f"P{c1}.{n1}")
            if c2:
                out.add(f"P{c2}.{n2}")
    return out


def named(files):
    """Every rule and check ID the files name, and the files' text (for vector names)."""
    ids, text = set(), []
    for f in files:
        t = open(f, encoding="utf-8", errors="replace").read()
        ids |= expand_ck(t) | expand_p(t)
        text.append(t)
    return ids, "\n".join(text)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("root", nargs="?", default=os.getcwd())
    ap.add_argument("--chapters", default="")
    ap.add_argument("--require", default="go")
    ap.add_argument("--markdown", action="store_true")
    a = ap.parse_args()
    root = os.path.abspath(a.root)
    spec = os.path.join(root, "tools/be-protocol/spec")
    impls = {
        "go": glob.glob(os.path.join(root, "tools/be-sdk-go/**/*_test.go"), recursive=True)
        + [f for f in glob.glob(os.path.join(root, "tools/be-sdk-go/**/*.go"), recursive=True)
           if os.path.basename(os.path.dirname(f)).endswith("test")],
        "py": glob.glob(os.path.join(root, "tools/be-sdk-python/tests/**/*.py"), recursive=True),
        "suite": glob.glob(os.path.join(root, "tools/be-acceptance/conformance/profile/**/*.py"), recursive=True),
    }
    seen = {k: named(v) for k, v in impls.items()}
    chapters = {c.strip() for c in a.chapters.split(",") if c.strip()}
    require = [r.strip() for r in a.require.split(",") if r.strip()]
    rows = []
    for path in sorted(glob.glob(os.path.join(spec, "[0-9][0-9]-*.md"))):
        ch = os.path.basename(path)[:2]
        if chapters and ch not in chapters:
            continue
        for line in open(path, encoding="utf-8"):
            m = RULE.match(line)
            if not m:
                continue
            rule, level, check = m.group(1), m.group(2).strip(), m.group(4).strip()
            cks, vectors = expand_ck(check), VECTOR.findall(check)
            if not cks and not vectors and (check in NOT_RUN or check not in ("SDK 测试",)):
                kind = "not run by a test"
            else:
                kind = "test"
            guard = {}
            for k, (ids, text) in seen.items():
                guard[k] = kind == "test" and (rule in ids or bool(cks & ids) or any(f'"{v}' in text or f"{v}.json" in text or f"'{v}" in text for v in vectors))
            rows.append((ch, rule, level, check, kind, guard))
    tested = [r for r in rows if r[4] == "test"]
    missing = [r for r in tested if not any(r[5][k] for k in require)]
    if a.markdown:
        print("| 章 | 规则 | 层级 | 检验 | go | py | suite |\n| --- | --- | --- | --- | --- | --- | --- |")
        for ch, rule, level, check, kind, g in rows:
            mark = (lambda k: "—" if kind != "test" else ("✓" if g[k] else "✗"))
            print(f"| {ch} | {rule} | {level} | {check} | {mark('go')} | {mark('py')} | {mark('suite')} |")
    else:
        for k in impls:
            print(f"{k:5}: {sum(1 for r in tested if r[5][k])} of {len(tested)} tested rules named by a test")
        print(f"not run by a test (assembly or static checks, or none): {len(rows) - len(tested)}")
        for ch, rule, level, check, kind, g in missing:
            print(f"UNGUARDED ({'/'.join(require)}): {rule} [{level}] {check}")
    sys.exit(1 if missing else 0)


if __name__ == "__main__":
    main()
