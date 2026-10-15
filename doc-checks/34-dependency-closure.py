#!/usr/bin/env python3
"""Check the boundary cards' required dependencies and the customer combinations.

Reads every `## <component>` section in <project>/docs/zh/07-components/*.md, takes the component IDs that
appear in its "必需依赖" row (plus 身份 -> the identity family, 授权 -> infra/authz), and checks:

  ACYCLIC   the required-dependency graph has no cycle (BrickKit rejects a required cycle); the
            identity family stands for every infra/iam-* card, since any of them can be chosen
  KNOWN     every required dependency names a component that has a card
  CLOSURE   every combination in the selection page's table lists the transitive closure of the
            required dependencies of its own listed components
  BASELINE  every combination contains the deployment-level components every formal
            deployment needs: exactly one identity member (any infra/iam-*), authorization, audit
            (protocol 03)
  RULES     the selection page's admission table: a combination meeting a component condition lists
            the required components (or one of them)
  ONE-OF    a card's "可选依赖与缺件" row may name a group that "装配准入要求至少有一个" (BrickKit
            has no either-or dependency; assembly admission checks it, security 09): every
            combination listing that component lists at least one member of the group

Self-tests run first: adding "授权" as a required dependency of an identity member must be
reported as a cycle (authorization requires identity); a combination of `erp/billing` alone must
fail ONE-OF (billing needs a source).

Exit status is non-zero when a check fails.
"""
import glob
import os
import re
import sys

# Usage: dependency-closure.py [PROJECT_ROOT]  (default: the current directory)
PROJECT = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else os.getcwd())
ROOT = os.path.join(PROJECT, "docs", "zh", "07-components")
COMBOS = os.path.join(PROJECT, "docs", "zh", "00-overview", "04-selection.md")
ID = re.compile(r"\b((?:mdm|erp|crm|hrm|prj|ana|infra|integration|frontend)/[a-z0-9<>\-]+)")
FAMILY = "infra/iam-*"   # 身份 names the identity family: any one infra/iam-* member satisfies it
ALIASES = {"身份": FAMILY, "授权": "infra/authz"}
BASELINE = {"infra/authz", "infra/audit"}
IDENTITY = re.compile(r"^infra/iam-")


ONE_OF_MARK = "装配准入要求至少有一个"


def cards():
    """Returns the required dependencies and the at-least-one groups of every card."""
    deps, oneof = {}, {}
    for path in sorted(glob.glob(os.path.join(ROOT, "*.md"))):
        name = os.path.basename(path)
        if name in ("README.md", "catalog.md", "selection-map.md"):
            continue
        text = open(path, encoding="utf-8").read()
        for sec in re.split(r"^## ", text, flags=re.M)[1:]:
            head = sec.split("\n", 1)[0]
            ids = ID.findall(head)
            if not ids:
                continue
            m = re.search(r"^\| 必需依赖 \| (.*) \|$", sec, re.M)
            req = set()
            if m:
                cell = m.group(1)
                req |= set(ID.findall(cell))
                for word, cid in ALIASES.items():
                    if re.search(r"(^|[、，；（ ])" + word + r"($|[、，；（）(])", cell):
                        req.add(cid)
            groups = []
            m = re.search(r"^\| 可选依赖与缺件 \| (.*) \|$", sec, re.M)
            if m:
                for seg in m.group(1).split("；"):
                    if ONE_OF_MARK in seg:
                        groups.append(set(ID.findall(seg.split("：", 1)[0])))
            for cid in ids:
                deps[cid] = req - {cid}
                oneof[cid] = groups
    return deps, oneof


def one_of_fails(oneof, name, listed):
    out = []
    for n in sorted(listed):
        for g in oneof.get(n, ()):
            if not g & listed:
                out.append(f"ONE-OF: {name} lists {n} but none of {sorted(g)}")
    return out


def expand_family(deps):
    """For cycle detection the family placeholder has the edges of every identity member."""
    members = sorted(n for n in deps if IDENTITY.match(n) and n != FAMILY)
    out = {n: set(ds) for n, ds in deps.items()}
    out[FAMILY] = set(members)
    return out


def cycle(deps):
    deps = expand_family(deps)
    state, stack = {}, []
    def visit(n):
        state[n] = 1; stack.append(n)
        for d in deps.get(n, ()):
            if state.get(d) == 1:
                return stack[stack.index(d):] + [d]
            if d not in state:
                c = visit(d)
                if c:
                    return c
        state[n] = 2; stack.pop()
        return None
    for n in list(deps):
        if n not in state:
            c = visit(n)
            if c:
                return c
    return None


def closure(deps, roots):
    """The family placeholder is replaced by the identity members the combination lists itself;
    with none listed it stays and is reported as missing."""
    members = [r for r in roots if IDENTITY.match(r) and r != FAMILY]
    out, todo = set(), list(roots)
    while todo:
        n = todo.pop()
        if n == FAMILY and members:
            todo += members
            continue
        if n in out:
            continue
        out.add(n)
        todo += list(deps.get(n, ()))
    return out


def combos():
    text = open(COMBOS, encoding="utf-8").read()
    sec = text.split("## 客户组合与闭包", 1)[1].split("\n## ", 1)[0].split("\n### ", 1)[0]
    rows = {}
    for line in sec.splitlines():
        if not line.startswith("| ") or line.startswith("| 组合") or line.startswith("| ---"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        name, req = cells[0], cells[1]
        listed = set(ID.findall(req))
        for other, ids in list(rows.items()):
            if req.startswith(other + "的全部"):
                listed |= ids
        rows[name] = listed
    return rows


def rules():
    """Component rules of the selection page's admission table: (name, condition set, required set, one-of)."""
    text = open(COMBOS, encoding="utf-8").read()
    if "### 组合准入规则" not in text:
        return []
    sec = text.split("### 组合准入规则", 1)[1].split("\n## ", 1)[0]
    out = []
    for line in sec.splitlines():
        if not line.startswith("| ") or line.startswith("| 规则") or line.startswith("| ---"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        cond = set(ID.findall(cells[1]))
        if not cond or not cells[1].startswith("含"):
            continue   # configuration rules are checked at assembly time
        need = set(ID.findall(cells[2]))
        out.append((cells[0], cond, need, cells[2].startswith("其中之一")))
    return out


def rule_fails(name, listed, rs):
    out = []
    for rname, cond, need, one in rs:
        if cond <= listed:
            ok = bool(need & listed) if one else need <= listed
            if not ok:
                out.append(f"RULES: {name} breaks '{rname}': needs {'one of ' if one else ''}{sorted(need)}")
    return out


def main():
    deps, oneof = cards()
    fails = []
    rs = rules()
    if rs and not rule_fails("probe", {"erp/inventory", "erp/finance"}, rs):
        fails.append("SELF-TEST: inventory and finance without costing was not reported")
    if not one_of_fails(oneof, "probe", {"erp/billing"}):
        fails.append("SELF-TEST: erp/billing without a source was not reported")
    probe = {n: set(ds) for n, ds in deps.items()}
    member = next(n for n in sorted(probe) if IDENTITY.match(n))
    probe[member].add("infra/authz")
    if not cycle(probe):
        fails.append(f"SELF-TEST: a cycle through {member} was not detected")
    c = cycle(deps)
    if c:
        fails.append("ACYCLIC: " + " -> ".join(c))
    for n, ds in sorted(deps.items()):
        for d in ds:
            if d not in deps and "<" not in d and d != FAMILY:
                fails.append(f"KNOWN: {n} requires {d}, which has no card")
    for name, listed in combos().items():
        missing = closure(deps, listed) - listed
        if missing:
            fails.append(f"CLOSURE: {name} misses {sorted(missing)}")
        if BASELINE - listed:
            fails.append(f"BASELINE: {name} misses {sorted(BASELINE - listed)}")
        ids = [c for c in listed if IDENTITY.match(c)]
        if len(ids) != 1:
            fails.append(f"BASELINE: {name} needs exactly one identity member (infra/iam-*), has {ids}")
        fails += one_of_fails(oneof, name, listed)
        fails += rule_fails(name, listed, rs)
    print(f"{len(deps)} components with cards; {sum(len(v) for v in deps.values())} required edges; {len(combos())} combinations")
    for f in fails:
        print(f)
    if fails:
        sys.exit(1)
    print("ACYCLIC, KNOWN, CLOSURE, BASELINE, ONE-OF, RULES: ok")


if __name__ == "__main__":
    main()
