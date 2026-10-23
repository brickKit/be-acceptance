#!/usr/bin/env python3
"""Check local Markdown links and anchors in the current project documents.

Every relative link to a .md file must point at an existing file, and its #anchor (if any)
must match a heading of that file (heading → anchor: strip punctuation, spaces to '-',
lower-case). Links inside fenced code blocks are ignored. Exit status 1 when any link is
broken. Scans every current Markdown file of the project repository: the files Git tracks
(outside Git, the directory tree) except archive/ and dev/, without descending into submodules,
which check their own documents (brickkit lint). Links into archive/ or dev/ are reported
(current documents must not depend on them, protocol 21).
Usage: links.py [PROJECT_ROOT]  (default: the current directory). Known false positive:
URL-encoded paths such as %20.
"""
import os
import re
import subprocess
import sys

ROOT = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.getcwd())
FENCE = re.compile(r"^```.*?^```", re.M | re.S)


def slug(h):
    h = h.strip().lower()
    h = re.sub(r"[^\w一-鿿\- ]", "", h)
    return h.replace(" ", "-")


_anchors = {}


def heads(p):
    if p not in _anchors:
        text = open(p, encoding="utf-8").read()
        _anchors[p] = {slug(m) for m in re.findall(r"^#+ (.*)$", FENCE.sub("", text), re.M)}
    return _anchors[p]


SKIP = ("archive", "dev")


def current_files():
    try:
        out = subprocess.run(["git", "-c", "core.quotepath=off", "ls-files", "-z", "--", "*.md"], cwd=ROOT,
                             capture_output=True, check=True).stdout.decode()
        rels = [f for f in out.split("\0") if f]
    except (OSError, subprocess.CalledProcessError):
        rels = []
        for d, ds, fs in os.walk(ROOT):
            ds[:] = [x for x in ds if not x.startswith(".") or x == ".claude"]
            rels += [os.path.relpath(os.path.join(d, f), ROOT) for f in fs if f.endswith(".md")]
    return [os.path.join(ROOT, f) for f in rels if f.split("/")[0] not in SKIP]


def main():
    files = current_files()
    errs = n = 0
    for p in sorted(files):
        text = FENCE.sub("", open(p, encoding="utf-8").read())
        for m in re.finditer(r"\]\(([^)\s]+)\)", text):
            t = m.group(1)
            if t.startswith(("http:", "https:", "mailto:")):
                continue
            path, _, anchor = t.partition("#")
            target = os.path.normpath(os.path.join(os.path.dirname(p), path)) if path else p
            rel_target = os.path.relpath(target, ROOT)
            if rel_target.split(os.sep)[0] in ("archive", "dev"):
                print("BOUNDARY", os.path.relpath(p, ROOT), t); errs += 1
                continue
            if not target.endswith(".md"):
                if not os.path.exists(target):
                    print("MISSING", os.path.relpath(p, ROOT), t); errs += 1
                continue
            n += 1
            rel = os.path.relpath(p, ROOT)
            if not os.path.exists(target):
                print("MISSING", rel, t); errs += 1
            elif anchor and anchor not in heads(target):
                print("ANCHOR", rel, t); errs += 1
    print(f"{len(files)} files, {n} local links, {errs} errors")
    sys.exit(1 if errs else 0)


if __name__ == "__main__":
    main()
