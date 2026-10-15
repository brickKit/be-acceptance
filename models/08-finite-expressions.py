#!/usr/bin/env python3
"""Reference model for the 7th security candidate: finite grant expressions.

Checks, on randomly generated cases, the properties the candidate relies on:

  CONTAIN    containment proof is sound: cand <= ceil  =>  every record cand matches, ceil matches
  SPLIT      one-dimension split cover is sound
  INTERSECT  atom-wise intersection matches exactly the records both segments match
  FIELDS     a restricted field may be sorted/filtered only when every view segment is contained
             in a segment that reads it; then every visible row has the field readable
  SQL        the compiled SQL predicate, matched-segment bitmap and field masks equal the
             in-memory evaluation (run on SQLite; dialect differs from PostgreSQL)
  PAGE       keyset pages over the deduplicated OR of segments equal the full ordered set
  FLATTEN    counterexample: per-dimension flattening (current authz/2 rule branch) widens access
  PERDIM     counterexample: per-dimension "grant only what you own" check accepts a cross pair

Bad variants (each must make at least one check fail):
  exact_covers_subtree     an EXACT ceiling covers a SUBTREE candidate on the same path
  subtree_meet_shallow     intersecting two nested subtrees keeps the shallower one
  ignore_currency          interval containment ignores the currency
  lower_bound_closedness   containment treats equal lower bounds as covered whatever the closedness
  upper_bound_closedness   the same for upper bounds
  meet_closedness          intersecting intervals with an equal endpoint keeps the closed end
  sql_raw_prefix           SUBTREE compiles to a raw string prefix (LIKE 'p%')
  mask_ignores_hits        a field is unmasked on every row when any segment reads it
  sort_proof_ignores_scope a field is sortable when some segment reads it, ignoring scope
  empty_dept_matches_tree  a record without a department matches any tree atom

This is a model of the algorithms, not of be-sdk-*, PostgreSQL or any provider.
"""
import itertools
import random
import sqlite3
import sys
from decimal import Decimal

ALL = "ALL"
BAD = set()
BAD_VARIANTS = ["exact_covers_subtree", "subtree_meet_shallow", "ignore_currency", "lower_bound_closedness",
                "upper_bound_closedness", "meet_closedness", "sql_raw_prefix", "mask_ignores_hits",
                "sort_proof_ignores_scope", "empty_dept_matches_tree"]
SEED = 20261013
CASES = 4000

# ---- the resource definition used by the model -------------------------------------------------

LEGALS = ["L1", "L2", "L3"]
WAREHOUSES = ["W1", "W2", "W3", "W4"]
PATHS = ["/a", "/a/x", "/a/x/1", "/a/y", "/ab", "/b", "/b/z"]  # "/ab" tests segment boundaries
SUBJECTS = ["u1", "u2", "u3"]
STATUSES = ["draft", "open", "closed"]
CURRENCIES = ["CNY", "USD"]
FIELDS = ["name", "price", "cost"]
RESTRICTED = ["price", "cost"]


def under(p, q):
    """p is q or a descendant of q, by path segments (never by raw string prefix)."""
    return p == q or p.startswith(q + "/")


# ---- atoms: one per dimension, each kind closed under intersection -----------------------------

# id-set:     ALL | frozenset (empty = matches nothing)
# tree:       ALL | frozenset of (path, mode) with mode in {"exact", "subtree"}
# principal:  ALL | frozenset of subjects (SELF is instantiated before use)
# interval:   ALL | (lo, lo_closed, hi, hi_closed, currency) on a (amount, currency) pair
# enum:       ALL | frozenset


def m_set(a, v):
    return a == ALL or (v is not None and v in a)


def m_tree(a, p):
    if a == ALL:
        return True
    if p is None:
        return "empty_dept_matches_tree" in BAD  # no department never means the whole tree
    return any(p == q if mode == "exact" else under(p, q) for q, mode in a)


def m_interval(a, amount, cur):
    if a == ALL:
        return True
    if amount is None or cur is None:
        return False  # known null fails a positive comparison
    lo, lc, hi, hc, c = a
    if cur != c:
        return False  # no implicit currency conversion
    if lo is not None and (amount < lo or (amount == lo and not lc)):
        return False
    if hi is not None and (amount > hi or (amount == hi and not hc)):
        return False
    return True


def c_set(c, k):
    if k == ALL:
        return True
    if c == ALL:
        return False
    return c <= k


def c_tree(c, k):
    if k == ALL:
        return True
    if c == ALL:
        return False
    for p, mode in c:
        ok = False
        for q, kmode in k:
            if kmode == "subtree" and under(p, q):
                ok = True  # a subtree covers any node or subtree below it
            elif kmode == "exact" and (mode == "exact" or "exact_covers_subtree" in BAD) and p == q:
                ok = True  # an exact node never covers a subtree: the tree can grow
        if not ok:
            return False
    return True


def lo_ge(a_lo, a_c, b_lo, b_c):
    """lower bound a is at least as tight as b."""
    if b_lo is None:
        return True
    if a_lo is None:
        return False
    if "lower_bound_closedness" in BAD and a_lo == b_lo:
        return True
    return a_lo > b_lo or (a_lo == b_lo and (b_c or not a_c))


def hi_le(a_hi, a_c, b_hi, b_c):
    if b_hi is None:
        return True
    if a_hi is None:
        return False
    if "upper_bound_closedness" in BAD and a_hi == b_hi:
        return True
    return a_hi < b_hi or (a_hi == b_hi and (b_c or not a_c))


def c_interval(c, k):
    if k == ALL:
        return True
    if c == ALL:
        return False
    if c[4] != k[4] and "ignore_currency" not in BAD:
        return False
    return lo_ge(c[0], c[1], k[0], k[1]) and hi_le(c[2], c[3], k[2], k[3])


def i_set(a, b):
    if a == ALL:
        return b
    if b == ALL:
        return a
    return a & b


def i_tree(a, b):
    if a == ALL:
        return b
    if b == ALL:
        return a
    out = set()
    for p, pm in a:
        for q, qm in b:
            if pm == "subtree" and qm == "subtree":
                shallow = "subtree_meet_shallow" in BAD
                if under(p, q):
                    out.add((q if shallow else p, "subtree"))
                elif under(q, p):
                    out.add((p if shallow else q, "subtree"))
            elif pm == "exact" and qm == "exact":
                if p == q:
                    out.add((p, "exact"))
            else:
                e, s = (p, q) if pm == "exact" else (q, p)
                if under(e, s):
                    out.add((e, "exact"))
    return frozenset(out)


def i_interval(a, b):
    if a == ALL:
        return b
    if b == ALL:
        return a
    if a[4] != b[4]:
        return (Decimal(1), True, Decimal(0), True, a[4])  # empty interval
    lo, lc = (a[0], a[1]) if lo_ge(a[0], a[1], b[0], b[1]) else (b[0], b[1])
    hi, hc = (a[2], a[3]) if hi_le(a[2], a[3], b[2], b[3]) else (b[2], b[3])
    if "meet_closedness" in BAD:
        if a[0] is not None and a[0] == b[0]:
            lc = a[1] or b[1]
        if a[2] is not None and a[2] == b[2]:
            hc = a[3] or b[3]
    return (lo, lc, hi, hc, a[4])


DIMS = {  # dimension -> (kind, record attribute)
    "legal": ("set", "legal"),
    "warehouse": ("set", "warehouse"),
    "org": ("tree", "dept"),
    "owner": ("set", "owner"),
    "amount": ("interval", None),
    "status": ("set", "status"),
}
MATCH = {"set": m_set, "tree": m_tree}
CONTAIN = {"set": c_set, "tree": c_tree, "interval": c_interval}
INTERSECT = {"set": i_set, "tree": i_tree, "interval": i_interval}


def match_atom(dim, atom, r):
    kind, attr = DIMS[dim]
    if kind == "interval":
        return m_interval(atom, r["amount"], r["currency"])
    return MATCH[kind](atom, r[attr])


class Seg:
    """A complete segment: every declared dimension explicit, plus read fields."""

    def __init__(self, atoms, read):
        assert set(atoms) == set(DIMS), "a missing dimension is never an implicit ALL"
        self.atoms = atoms
        self.read = frozenset(read)

    def match(self, r):
        return all(match_atom(d, a, r) for d, a in self.atoms.items())

    def __repr__(self):
        return f"Seg({self.atoms}, read={sorted(self.read)})"


def contains(cand, ceil, check_fields=True):
    """Product-set containment: per dimension, valid because a segment is a conjunction of
    independent atoms. Returns (ok, first failing dimension)."""
    for d, (kind, _) in DIMS.items():
        if not CONTAIN[kind](cand.atoms[d], ceil.atoms[d]):
            return False, d
    if check_fields and not cand.read <= ceil.read:
        return False, "fields"
    return True, None


def intersect(a, b):
    return Seg({d: INTERSECT[DIMS[d][0]](a.atoms[d], b.atoms[d]) for d in DIMS}, a.read & b.read)


def split_cover(cand, ceilings, dim):
    """Cover cand by splitting one finite id-set dimension: each value must be contained,
    with that dimension narrowed to it, in a single ceiling. Returns witness list or None."""
    vals = cand.atoms[dim]
    if vals == ALL:
        return None
    witness = []
    for v in sorted(vals):
        piece = Seg({**cand.atoms, dim: frozenset([v])}, cand.read)
        hit = next((i for i, k in enumerate(ceilings) if contains(piece, k)[0]), None)
        if hit is None:
            return None
        witness.append((v, hit))
    return witness


# ---- random generation -------------------------------------------------------------------------

def rnd_set(rng, universe, p_all=0.3):
    if rng.random() < p_all:
        return ALL
    return frozenset(rng.sample(universe, rng.randint(0, min(3, len(universe)))))


def rnd_tree(rng):
    if rng.random() < 0.25:
        return ALL
    return frozenset((rng.choice(PATHS), rng.choice(["exact", "subtree"])) for _ in range(rng.randint(0, 2)))


def rnd_interval(rng):
    if rng.random() < 0.4:
        return ALL
    a, b = sorted(Decimal(rng.randint(0, 20)) * 10 for _ in range(2))
    lo = a if rng.random() < 0.6 else None
    hi = b if rng.random() < 0.8 else None
    return (lo, rng.random() < 0.5, hi, rng.random() < 0.5, rng.choice(CURRENCIES))


def rnd_seg(rng):
    return Seg({
        "legal": rnd_set(rng, LEGALS),
        "warehouse": rnd_set(rng, WAREHOUSES),
        "org": rnd_tree(rng),
        "owner": rnd_set(rng, SUBJECTS, 0.5),
        "amount": rnd_interval(rng),
        "status": rnd_set(rng, STATUSES, 0.6),
    }, rng.sample(FIELDS, rng.randint(1, 3)))


def narrow(rng, k):
    """A segment likely (not certainly) contained in k, to exercise the positive path."""
    s = rnd_seg(rng)
    atoms = dict(k.atoms)
    d = rng.choice(list(DIMS))
    atoms[d] = s.atoms[d] if rng.random() < 0.5 else atoms[d]
    if atoms["amount"] != ALL and rng.random() < 0.3:  # same bounds, other closedness
        lo, lc, hi, hc, cur = atoms["amount"]
        atoms["amount"] = (lo, not lc, hi, not hc, cur)
    return Seg(atoms, rng.sample(sorted(k.read), rng.randint(1, len(k.read))))


def rnd_record(rng, i):
    return {
        "id": i,
        "legal": rng.choice(LEGALS),
        "warehouse": rng.choice(WAREHOUSES),
        "dept": rng.choice(PATHS + ["/a/x/new", "/b/new", None]),  # nodes created after the grant
        "owner": rng.choice(SUBJECTS),
        "amount": None if rng.random() < 0.1 else Decimal(rng.randint(0, 22) * 10 if rng.random() < 0.5 else rng.randint(0, 220)),
        "currency": rng.choice(CURRENCIES),
        "status": rng.choice(STATUSES),
    }


# ---- SQL compilation (parameterised; values never concatenated) --------------------------------

COLUMN = {"legal": "legal", "warehouse": "warehouse", "owner": "owner", "status": "status"}


def like_escape(s):
    return s.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")


def atom_sql(d, a, args):
    kind = DIMS[d][0]
    if a == ALL:
        return "1"
    if kind == "set":
        if not a:
            return "0"
        args.extend(sorted(a))
        return f"{COLUMN[d]} IN ({','.join('?' * len(a))})"
    if kind == "tree":
        parts = []
        for q, mode in sorted(a):
            args.append(q)
            if mode == "exact":
                parts.append("dept = ?")
            elif "sql_raw_prefix" in BAD:
                args.append(like_escape(q) + "%")
                parts.append("(dept = ? OR dept LIKE ? ESCAPE '\\')")
            else:
                args.append(like_escape(q) + "/%")
                parts.append("(dept = ? OR dept LIKE ? ESCAPE '\\')")
        return "(" + " OR ".join(parts) + ")" if parts else "0"
    lo, lc, hi, hc, cur = a
    parts = ["amount IS NOT NULL", "currency = ?"]
    args.append(cur)
    if lo is not None:
        parts.append(f"amount {'>=' if lc else '>'} ?")
        args.append(int(lo))
    if hi is not None:
        parts.append(f"amount {'<=' if hc else '<'} ?")
        args.append(int(hi))
    return "(" + " AND ".join(parts) + ")"


def seg_sql(s, args):
    return "(" + " AND ".join(atom_sql(d, s.atoms[d], args) for d in DIMS) + ")"


def plan(segs):
    """Visible set = OR of segments, plus a per-segment match bit and per-field readability,
    all computed by the same compiled predicates. Returns (select list, where, args)."""
    sel_args, where_args = [], []
    bits = [seg_sql(s, sel_args) for s in segs]
    masks = []
    for f in RESTRICTED:
        idx = [i for i, s in enumerate(segs) if f in s.read]
        if "mask_ignores_hits" in BAD:
            masks.append("1" if idx else "0")
        else:
            masks.append(" OR ".join(f"b{i}" for i in idx) or "0")
    where = " OR ".join(seg_sql(s, where_args) for s in segs) or "0"
    return bits, masks, where, sel_args, where_args


def run_plan(db, segs, after_id=None, limit=None):
    bits, masks, where, sel_args, where_args = plan(segs)
    inner = ", ".join(f"{b} AS b{i}" for i, b in enumerate(bits)) or "0 AS b0"
    page = " AND id > ?" if after_id is not None else ""
    sql = (f"SELECT id, {', '.join(f'({m}) AS r_{f}' for m, f in zip(masks, RESTRICTED))} "
           f"FROM (SELECT id, {inner} FROM orders WHERE ({where}){page}) ORDER BY id")
    args = sel_args + where_args + ([after_id] if after_id is not None else [])
    if limit:
        sql += " LIMIT ?"
        args.append(limit)
    return [(row[0], tuple(bool(x) for x in row[1:])) for row in db.execute(sql, args)]


def mem_eval(rows, segs):
    out = []
    for r in rows:
        hit = [s for s in segs if s.match(r)]
        if hit:
            readable = set().union(*(s.read for s in hit))
            out.append((r["id"], tuple(f in readable for f in RESTRICTED)))
    return out


# ---- checks ------------------------------------------------------------------------------------

def run():
    rng = random.Random(SEED)
    records = [rnd_record(rng, i) for i in range(1, 400)]
    fails = []
    stats = {"contained": 0, "split": 0, "fields_ok": 0, "sql_rows": 0}

    for _ in range(CASES):  # CONTAIN, INTERSECT
        k = rnd_seg(rng)
        c = narrow(rng, k) if rng.random() < 0.6 else rnd_seg(rng)
        ok, _ = contains(c, k)
        if ok:
            stats["contained"] += 1
            bad = next((r for r in records if c.match(r) and not k.match(r)), None)
            if bad:
                fails.append(("CONTAIN", c, k, bad))
        x = intersect(c, k)
        bad = next((r for r in records if x.match(r) != (c.match(r) and k.match(r))), None)
        if bad:
            fails.append(("INTERSECT", c, k, bad))

    for _ in range(CASES // 4):  # SPLIT
        ceilings = [rnd_seg(rng) for _ in range(3)]
        base = narrow(rng, ceilings[0])
        cand = Seg({**base.atoms, "warehouse": frozenset(rng.sample(WAREHOUSES, 2))}, base.read)
        w = split_cover(cand, ceilings, "warehouse")
        if w:
            stats["split"] += 1
            bad = next((r for r in records if cand.match(r) and not any(k.match(r) for k in ceilings)), None)
            if bad:
                fails.append(("SPLIT", cand, ceilings, bad))

    for _ in range(CASES // 4):  # FIELDS
        segs = [rnd_seg(rng) for _ in range(rng.randint(1, 4))]
        for f in RESTRICTED:
            if "sort_proof_ignores_scope" in BAD:
                proven = any(f in t.read for t in segs)
            else:
                proven = all(any(contains(v, t, check_fields=False)[0] and f in t.read for t in segs) for v in segs)
            if proven:
                stats["fields_ok"] += 1
                for r in records:
                    hit = [s for s in segs if s.match(r)]
                    if hit and not any(f in s.read for s in hit):
                        fails.append(("FIELDS", segs, f, r))
                        break

    db = sqlite3.connect(":memory:")
    db.execute("CREATE TABLE orders (id INTEGER PRIMARY KEY, legal, warehouse, dept, owner, amount, currency, status)")
    db.executemany("INSERT INTO orders VALUES (?,?,?,?,?,?,?,?)", [
        (r["id"], r["legal"], r["warehouse"], r["dept"], r["owner"],
         None if r["amount"] is None else int(r["amount"]), r["currency"], r["status"]) for r in records])
    for _ in range(CASES // 8):  # SQL, PAGE
        segs = [rnd_seg(rng) for _ in range(rng.randint(1, 4))]
        expect = mem_eval(records, segs)
        got = run_plan(db, segs)
        stats["sql_rows"] += len(got)
        if got != expect:
            fails.append(("SQL", segs, len(expect), len(got)))
        pages, after = [], None
        while True:
            page = run_plan(db, segs, after, 7)
            if not page:
                break
            pages.extend(page)
            after = page[-1][0]
        if pages != expect:
            fails.append(("PAGE", segs, len(expect), len(pages)))

    # FLATTEN: g1 = L1 x W1, g2 = L2 x W2; flattened = {L1,L2} x {W1,W2}
    def pair(legal, wh):
        return Seg({"legal": frozenset([legal]), "warehouse": frozenset([wh]), "org": ALL,
                    "owner": ALL, "amount": ALL, "status": ALL}, ["name"])
    g = [pair("L1", "W1"), pair("L2", "W2")]
    flat = Seg({**g[0].atoms, "legal": frozenset(["L1", "L2"]), "warehouse": frozenset(["W1", "W2"])}, ["name"])
    leak = next((r for r in records if flat.match(r) and not any(s.match(r) for s in g)), None)
    flatten_ok = leak is not None and (leak["legal"], leak["warehouse"]) in {("L1", "W2"), ("L2", "W1")}

    # PERDIM: granter owns L1/W1/<=100 and L2/W2/<=50; candidate L1/W2/<=100
    def grant(legal, wh, hi):
        return Seg({**pair(legal, wh).atoms, "amount": (None, True, Decimal(hi), True, "CNY")}, ["name"])
    owned = [grant("L1", "W1", 100), grant("L2", "W2", 50)]
    cand = grant("L1", "W2", 100)
    per_dim_accepts = all(
        any(CONTAIN[DIMS[d][0]](cand.atoms[d], o.atoms[d]) for o in owned) for d in DIMS)
    segment_rejects = not any(contains(cand, o)[0] for o in owned) and split_cover(cand, owned, "warehouse") is None

    return fails, stats, records, flatten_ok, leak, per_dim_accepts and segment_rejects


def main():
    fails, stats, records, flatten_ok, leak, perdim_ok = run()
    print(f"CONTAIN: {stats['contained']} proven containments, every matched record also matched the ceiling")
    print(f"INTERSECT: {CASES} pairs, atom-wise intersection equals the conjunction on {len(records)} records")
    print(f"SPLIT: {stats['split']} one-dimension split covers, each covered record lies in a single ceiling")
    print(f"FIELDS: {stats['fields_ok']} proven sort/filter fields, readable on every visible row")
    print(f"SQL: {CASES // 8} plans on SQLite, {stats['sql_rows']} rows; set, segment bits and field masks equal memory")
    print(f"PAGE: keyset pages of 7 over the deduplicated OR equal the full ordered set")
    print(f"FLATTEN: per-dimension flattening admits {leak['legal']}/{leak['warehouse']}, outside both grants" if flatten_ok
          else "FLATTEN: counterexample not reproduced")
    print(f"PERDIM: per-dimension subset check accepts L1/W2/<=100; segment containment rejects it"
          if perdim_ok else "PERDIM: counterexample not reproduced")

    if fails or not flatten_ok or not perdim_ok:
        for f in fails[:5]:
            print("FAIL", f, file=sys.stderr)
        sys.exit(1)
    caught = 0
    for bad in BAD_VARIANTS:
        BAD.clear(); BAD.add(bad)
        bad_fails = run()[0]
        kinds = sorted({f[0] for f in bad_fails})
        print(f"bad variant {bad}: {'detected by ' + ', '.join(kinds) if bad_fails else 'NOT DETECTED'}")
        caught += bool(bad_fails)
    BAD.clear()
    if caught != len(BAD_VARIANTS):
        print("FAIL: some bad variants were not detected", file=sys.stderr)
        sys.exit(1)
    print(f"all {len(BAD_VARIANTS)} bad variants detected")


if __name__ == "__main__":
    main()
