#!/usr/bin/env python3
"""Reference model for protocol 05: confirming a claim before numbering a formal invoice.

Drafts claim billable quantity at the source; unconfirmed claims expire. The billing component
confirms the claim, numbers the formal invoice in a local transaction, then tells the source to
formalize. Confirm requests can be delayed past the claim's expiry and their replies can be
lost (the billing component retries with the same claim id). In the billing database a confirmed
draft ends either numbered or voided, decided by one atomic state change; the recovery work item
voids stale confirmed drafts that way and only then releases their claims. The source can be
unreachable for long periods. Checks:

  NO_OVERLAP  the quantity covered by formal invoices never exceeds the billable quantity
  LIVENESS    once the source is reachable, every confirmed claim ends billed or released

Bad variants (each must be caught): number the formal invoice first and formalize afterwards
(the claim can expire in between and be re-claimed by another draft); confirmed claims still
expire automatically; abandoned confirmed claims are never released by recovery; a lost confirm
reply is retried with a new claim id (the first claim is orphaned); recovery releases a stale
confirmed claim without first voiding the draft locally (numbering races with the release).
"""
import random
import sys

SEED = 20261013
RUNS = 3000
EXPIRY = 7
STALE = 12          # recovery treats a confirmed, un-numbered draft as abandoned after this


def run(rng, variant, fails):
    billable = 10
    claims = {}      # claim id -> dict(qty, state: held / confirmed / billed / released, at)
    drafts = {}      # draft id -> dict(cid, state: draft / confirming / confirmed / numbered / voided, at)
    net = []         # confirm requests in flight: (arrive time, draft id, claim id)
    formalize = []   # claim ids whose invoice is numbered, waiting for formalization at the source
    release = []     # claim ids of voided drafts, waiting to be released at the source
    numbered = []    # (draft id, qty)
    nxt = [0]

    def new_claim(t):
        used = sum(c["qty"] for c in claims.values() if c["state"] in ("held", "confirmed", "billed"))
        if used >= billable:
            return None
        cid = f"c{nxt[0]}"; nxt[0] += 1
        claims[cid] = dict(qty=rng.randint(1, billable - used), state="held", at=t)
        return cid

    def number(did):                         # local transaction: confirmed -> numbered, atomically
        d = drafts[did]
        if d["state"] != "confirmed" and not (variant == "number_first" and d["state"] == "draft"):
            return
        d["state"] = "numbered"
        numbered.append((did, claims[d["cid"]]["qty"]))
        formalize.append(d["cid"])

    def void(did):                           # local transaction: confirmed -> voided, atomically
        d = drafts[did]
        if d["state"] in ("confirmed", "confirming"):
            d["state"] = "voided"
            release.append(d["cid"])

    def recovery(t, source_up):
        for did, d in drafts.items():
            if d["state"] == "confirming" and t - d["at"] > 3 and variant != "retry_new_id":
                net.append((t + 1, did, d["cid"]))               # re-send the same claim id
                d["at"] = t
            if d["state"] == "confirmed" and t - d["at"] > STALE:
                if variant == "release_without_void":
                    release.append(d["cid"])                    # the draft can still be numbered
                    d["at"] = t
                else:
                    void(did)
        if variant == "no_recovery" or not source_up:
            return
        for cid in list(release):
            release.remove(cid)
            if claims[cid]["state"] in ("held", "confirmed"):
                claims[cid]["state"] = "released"

    for t in range(1, 81):
        source_up = rng.random() < 0.6
        for c in claims.values():                               # expiry at the source
            expires = c["state"] == "held" or (variant == "confirmed_expires" and c["state"] == "confirmed")
            if expires and t - c["at"] > EXPIRY:
                c["state"] = "released"
        if source_up:                                           # confirm requests arriving
            for item in [x for x in net if x[0] <= t]:
                net.remove(item)
                _, did, cid = item
                c = claims[cid]
                if c["state"] == "held":
                    c["state"] = "confirmed"                    # atomic on the claim's row
                ok = c["state"] in ("confirmed", "billed")      # idempotent for the same id
                if rng.random() < 0.2:
                    continue                                    # the reply is lost
                d = drafts[did]
                if d["cid"] != cid or d["state"] != "confirming":
                    continue
                d["state"], d["at"] = ("confirmed" if ok else "voided"), t
        r = rng.random()
        if r < 0.25 and source_up:                              # a new draft claims what is left
            cid = new_claim(t)
            if cid:
                drafts[f"d{t}"] = dict(cid=cid, state="draft", at=t)
        elif r < 0.45:                                          # send confirm (may arrive late)
            ds = [k for k, d in drafts.items() if d["state"] == "draft"]
            if ds:
                did = rng.choice(ds)
                if variant == "number_first":
                    number(did)
                else:
                    drafts[did]["state"], drafts[did]["at"] = "confirming", t
                    net.append((t + rng.choice([1, 1, 2, 12]), did, drafts[did]["cid"]))
        elif r < 0.55 and variant == "retry_new_id":            # lost reply retried with a new id
            ds = [k for k, d in drafts.items() if d["state"] == "confirming" and t - d["at"] > 3]
            if ds and source_up:
                did = rng.choice(ds)
                cid = new_claim(t)
                if cid:
                    drafts[did]["cid"], drafts[did]["at"] = cid, t
                    net.append((t + 1, did, cid))
        elif r < 0.75:                                          # number a confirmed draft
            ds = [k for k, d in drafts.items() if d["state"] == "confirmed"]
            if ds and rng.random() < 0.85:                      # some confirmed drafts are abandoned
                number(rng.choice(ds))
        elif r < 0.8:                                           # a user voids a draft
            ds = [k for k, d in drafts.items() if d["state"] in ("draft", "confirmed")]
            if ds:
                did = rng.choice(ds)
                if drafts[did]["state"] == "draft":
                    drafts[did]["state"] = "voided"; release.append(drafts[did]["cid"])
                else:
                    void(did)
        elif source_up and formalize:                           # formalize at the source (retries)
            cid = formalize.pop(0)
            if claims[cid]["state"] in ("held", "confirmed"):
                claims[cid]["state"] = "billed"
            # a released claim is rejected: the numbered invoice becomes "basis invalid"
        recovery(t, source_up)
        if sum(q for _, q in numbered) > billable:
            fails.append("NO_OVERLAP"); return
    # liveness: the source stays reachable; recovery and retries run to the end
    for t in range(81, 81 + 3 * STALE):
        for item in [x for x in net if x[0] <= t]:
            net.remove(item)
            _, did, cid = item
            if claims[cid]["state"] == "held":
                claims[cid]["state"] = "confirmed"
            d = drafts[did]
            if d["cid"] == cid and d["state"] == "confirming":
                d["state"], d["at"] = ("confirmed" if claims[cid]["state"] in ("confirmed", "billed") else "voided"), t
        while formalize:
            cid = formalize.pop(0)
            if claims[cid]["state"] in ("held", "confirmed"):
                claims[cid]["state"] = "billed"
        recovery(t, True)
    if any(c["state"] == "confirmed" for c in claims.values()):
        fails.append("LIVENESS")


def main():
    variants = ["good", "number_first", "confirmed_expires", "no_recovery", "retry_new_id", "release_without_void"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (NO_OVERLAP, LIVENESS over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    if res["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
