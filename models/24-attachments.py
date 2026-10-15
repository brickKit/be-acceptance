#!/usr/bin/env python3
"""Reference model for protocol 12: attachments and object storage.

Clients upload through a pre-signed URL to a staging key; the attachment component finalizes
(copies to an immutable final key and records the digest), a scanner scans, and downloads are
served from the final key. Concurrently: a client keeps writing to the staging key after
finalizing (the pre-signed URL is still valid), orphan cleanup runs, legal holds are placed,
attachments are detached and purged, and the parent record's permission is revoked.
Checks:

  SCAN_BOUND   bytes served for download are always the bytes that were scanned clean
  NO_DANGLING  an available attachment always has its object
  HOLD         an attachment under hold is never purged
  DIGEST_MATCH bytes served always match the digest, size and type recorded at confirmation,
               also in deployments with scanning disabled
  REVOKE_BOUND after a revocation at time t, no download happens later than t + link TTL

Grant sub-model (D68: the attachment component never calls the parent's owner). Two owner
components each own parent records; a user asks an owner for access, the owner checks its own
permission and asks the attachment component for a grant over the system plane; the attachment
component checks that the calling service owns the parent's resource type and mints an opaque grant
bound to user, attachment, operation and expiry. Users replay grants, use them on other
attachments, forge them, and a foreign component asks for grants on parents it does not own.

  GRANT_SCOPE  every access the attachment component serves was decided by the parent's real
               owner for that user, that attachment and that operation, and is before the expiry

Bad variants (each must be caught): scan and serve the staging key; replace the file of an
attachment in place under the same key and keep the earlier scan result; read the staging
object for the checks and copy it to the final key afterwards;
orphan cleanup decides by object age instead of checking metadata references; purge ignores holds; download links without an expiry;
grant not bound to the attachment; grant expiry not checked; grant issued to any calling component
without checking that it owns the parent's resource type; grant accepted from a user other than the
one it was issued for.
"""
import random
import sys

SEED = 20261013
RUNS = 3000
TTL = 3


def run(rng, variant, fails):
    store = {}                 # key -> bytes
    att = None                 # dict(state, staging, final, digest, scanned_digest, held, session_open)
    links = []                 # (issued_at, expires_at)
    revoked_at = None
    now = 0
    scanning = rng.random() < 0.7          # some deployments disable scanning (marked unscanned)
    for step in range(60):
        now += 1
        a = rng.choice(["create", "put", "put", "finalize", "scan", "download", "download", "download",
                        "link", "link", "revoke", "cleanup", "hold", "detach", "purge", "replace"])
        if a == "create" and att is None:
            att = dict(state="uploading", staging=f"stg/{step}", final=None, digest=None,
                       scanned=None, held=False, session_open=True)
        elif a == "put" and att and att["session_open"]:
            # the pre-signed PUT stays usable until the session expires, even after finalize
            store[att["staging"]] = rng.choice(["clean", "clean", "virus"])
            if rng.random() < 0.1:
                att["session_open"] = False
        elif a == "finalize" and att and att["state"] in ("uploading", "pending_scan") and att["staging"] in store:
            content = store[att["staging"]]
            if variant == "staging_served":
                att["final"] = att["staging"]
            elif variant == "read_then_copy":
                att["digest"] = content                   # checks run on the staging object ...
                if rng.random() < 0.3:
                    store[att["staging"]] = rng.choice(["clean", "virus"])   # ... which is overwritten
                key = f"fin/{step}"
                store[key] = store[att["staging"]]       # before the copy
                att["final"] = key
            else:
                key = f"fin/{step}"
                store[key] = content                      # copy first (conditional write) ...
                att["final"] = key
                content = store[key]                      # ... then read the final object
            if variant != "read_then_copy":
                att["digest"] = content
            att["state"] = "pending_scan" if scanning else "available"
        elif a == "replace" and att and att["state"] == "available" and att["session_open"] and att["staging"] in store:
            # the user replaces the file of an existing attachment
            content = store[att["staging"]]
            if variant == "digest_unbound":
                store[att["final"]] = content            # same key, scan status kept
            else:
                key = f"fin/r{step}"
                store[key] = content
                att.update(final=key, digest=content, scanned=None, state="pending_scan")
        elif a == "scan" and att and att["state"] == "pending_scan" and att["final"] in store:
            content = store[att["final"]]
            if variant != "digest_unbound" and content != att["digest"]:
                att["state"] = "quarantined"; continue
            if content == "clean":
                att["state"] = "available"; att["scanned"] = content
            else:
                att["state"] = "quarantined"
        elif a == "link" and att and att["state"] == "available":
            if revoked_at is None:
                links.append((now, None if variant == "no_expiry" else now + TTL))
        elif a == "revoke" and revoked_at is None and rng.random() < 0.15:
            revoked_at = now
        elif a == "download" and links and att and att["state"] == "available":
            issued, exp = rng.choice(links)
            if exp is not None and now > exp:
                continue
            if revoked_at is not None and now > revoked_at + TTL:
                fails.append("REVOKE_BOUND"); return
            obj = store.get(att["final"])
            if obj is None:
                fails.append("NO_DANGLING"); return
            if obj != att["digest"]:
                fails.append("DIGEST_MATCH"); return
            if scanning and (obj != att["scanned"] or obj != "clean"):
                fails.append("SCAN_BOUND"); return
        elif a == "cleanup":
            referenced = {att["final"]} if att and att["final"] else set()
            for k in list(store):
                if k.startswith("fin/") and k not in referenced:
                    del store[k]
                if k.startswith("stg/") and att and k == att["staging"] and not att["session_open"] and att["state"] != "uploading":
                    del store[k]
            if variant == "cleanup_by_age" and att and att["state"] == "available" and rng.random() < 0.3:
                store.pop(att["final"], None)            # treated as orphan by an age rule
        elif a == "hold" and att:
            att["held"] = rng.random() < 0.5
        elif a == "detach" and att and att["state"] == "available":
            att["state"] = "detached"
        elif a == "purge" and att and att["state"] == "detached":
            if att["held"] and variant != "purge_ignores_hold":
                continue
            if att["held"]:
                fails.append("HOLD"); return
            store.pop(att["final"], None); att = None
        if att and att["state"] == "available" and att["final"] not in store:
            fails.append("NO_DANGLING"); return


GRANT_TTL = 2


def grant_run(rng, variant, fails):
    owner_of_type = {"order": "sales", "invoice": "billing"}
    parents = {"P1": "order", "P2": "invoice"}
    attachments = {"A1": "P1", "A2": "P1", "A3": "P2"}
    allowed = {("u1", "P1"), ("u2", "P2")}           # owner-side permission decisions
    grants = {}                                      # token -> (user, attachment, op, expires)
    decided = set()                                  # (user, attachment, op) decided by the real owner
    now = 0
    for step in range(40):
        now += 1
        a = rng.choice(["ask", "ask", "foreign", "use", "use", "use", "forge"])
        if a == "ask":
            user, att, op = rng.choice(["u1", "u2"]), rng.choice(list(attachments)), rng.choice(["read", "write"])
            parent = attachments[att]
            if (user, parent) not in allowed:
                continue                             # the owner refuses; no grant is requested
            tok = f"t{step}"
            grants[tok] = (user, att, op, now + GRANT_TTL)
            decided.add((user, att, op))
        elif a == "foreign":
            # a component asks for a grant on a parent whose resource type it does not own
            user, att = rng.choice(["u1", "u2"]), rng.choice(list(attachments))
            caller = rng.choice(["sales", "billing"])
            if caller == owner_of_type[parents[attachments[att]]]:
                continue                             # the real owner goes through "ask"
            if variant != "any_caller":
                continue                             # the attachment component refuses the caller
            grants[f"f{step}"] = (user, att, "read", now + GRANT_TTL)
        elif a == "forge":
            grants.setdefault("forged", None)
        elif a == "use" and grants:
            tok = rng.choice(list(grants))
            g = grants[tok]
            if g is None:
                continue                             # an unknown token is not found
            user, att, op, exp = g
            presenter = user if rng.random() < 0.7 else rng.choice(["u1", "u2"])   # tokens get passed around
            if presenter != user and variant != "no_user_check":
                continue                             # the grant is bound to the user it was issued for
            user = presenter
            target = att if variant != "unbound" else rng.choice(list(attachments))
            if variant != "no_expiry_check" and now > exp:
                continue
            if (user, target, op) not in decided or now > exp:
                fails.append("GRANT_SCOPE"); return


def main():
    variants = ["good", "staging_served", "digest_unbound", "cleanup_by_age", "purge_ignores_hold", "no_expiry", "read_then_copy"]
    res = {}
    for v in variants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            run(rng, v, fails)
        res[v] = sorted(set(fails))
    print(f"good: {'ok' if not res['good'] else 'FAIL ' + str(res['good'])} (SCAN_BOUND, DIGEST_MATCH, NO_DANGLING, HOLD, REVOKE_BOUND over {RUNS} runs)")
    caught = True
    for v in variants[1:]:
        print(f"bad variant {v}: {'caught ' + str(res[v]) if res[v] else 'NOT CAUGHT'}")
        caught &= bool(res[v])
    gvariants = ["good", "unbound", "no_expiry_check", "any_caller", "no_user_check"]
    gres = {}
    for v in gvariants:
        rng = random.Random(SEED)
        fails = []
        for _ in range(RUNS):
            grant_run(rng, v, fails)
        gres[v] = sorted(set(fails))
    print(f"grants good: {'ok' if not gres['good'] else 'FAIL ' + str(gres['good'])} (GRANT_SCOPE over {RUNS} runs)")
    for v in gvariants[1:]:
        print(f"grants bad variant {v}: {'caught ' + str(gres[v]) if gres[v] else 'NOT CAUGHT'}")
        caught &= bool(gres[v])
    if res["good"] or gres["good"] or not caught:
        sys.exit(1)


if __name__ == "__main__":
    main()
