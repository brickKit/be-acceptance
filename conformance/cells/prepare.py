"""Prepares a cell for `brickkit up`, as a deployment's provisioning would: the probes' databases and the shells'
login roles (fresh schemas every time), the deployment's certificates, the object storage bucket, a clean bus, and
every component's config file (the chain wired with $endpoint: references). Secrets go to the cell's .secrets/ and
are referenced from config; the object storage keys come from the environment (BE_ACC_S3_ACCESS_KEY, _SECRET_KEY)."""
from __future__ import annotations

import io
import json
import os
import pathlib
import secrets as secrets_mod
import shutil
import subprocess
import uuid
from urllib.parse import urlsplit

import asyncpg
import yaml

from besdk import testing
from cells import homes
from cells.spec import INSTANCES, SHELL_ROLES, Cell, file_of, schema_of, stream_of

# The security members' own resource types and permission keys (their component definitions), for the registry the
# authorization member checks submissions against.
INFRA_KEYS = {
    "infra/authz": {"resource_type": ["infra.authz.security_record", "infra.authz.security_disposition", "infra.authz.system"],
                    "permission": ["infra.authz.security_record.read", "infra.authz.security_record.change",
                                   "infra.authz.security_record.tighten", "infra.authz.security_record.relinquish",
                                   "infra.authz.security_disposition.read", "infra.authz.security_disposition.dispose",
                                   "infra.authz.security_disposition.release",
                                   "infra.authz.system.read", "infra.authz.system.verify", "infra.authz.system.submit",
                                   "infra.authz.system.claim", "infra.authz.system.account_check", "infra.authz.system.replay",
                                   "infra.authz.system.recover"]},
    "infra/iam-casdoor": {"resource_type": ["infra.iam.session", "infra.iam.account_status", "infra.iam.account"],
                          "permission": ["infra.iam.session.exchange", "infra.iam.session.ended",
                                         "infra.iam.account_status.pull", "infra.iam.account_status.read", "infra.iam.account_status.resend",
                                         "infra.iam.account_status.replay", "infra.iam.account_status.recover",
                                         "infra.iam.account.read", "infra.iam.account.disable", "infra.iam.account.freeze",
                                         "infra.iam.account.enable", "infra.iam.account.logout",
                                         "infra.iam.account.reset_second_factor"]},
    "infra/audit": {"resource_type": ["infra.audit.record", "infra.audit.intake", "infra.audit.evidence", "infra.audit.system"],
                    "permission": ["infra.audit.record.read", "infra.audit.record.list", "infra.audit.intake.read", "infra.audit.intake.submit",
                                   "infra.audit.intake.watermark", "infra.audit.evidence.append",
                                   "infra.audit.evidence.list", "infra.audit.system.read", "infra.audit.system.recover",
                                   # the records as a data class (3.0.30): the legal hold, the destruction's approval, the
                                   # replay of the destructions
                                   "infra.audit.record.hold", "infra.audit.record.approve_destruction",
                                   "infra.audit.system.replay"]},
}
# The master data components' resource types and operations (their definition.go), for the registry and the grants.
MDM_KEYS = {
    "mdm/org": {"resource_type": ["mdm.legal_entity", "mdm.org_unit", "mdm.org_position", "mdm.org.system"],
                "permission": ["mdm.legal_entity.read", "mdm.legal_entity.manage", "mdm.org_unit.read",
                               "mdm.org_unit.manage", "mdm.org_position.read", "mdm.org_position.manage", "mdm.org.system.read", "mdm.org.system.resend", "mdm.org.system.recover",
                               "mdm.org.system.replay"]},
    "mdm/currency": {"resource_type": ["mdm.currency", "mdm.exchange_rate", "mdm.currency.system"],
                     "permission": ["mdm.currency.read", "mdm.currency.manage", "mdm.exchange_rate.read",
                                    "mdm.exchange_rate.maintain", "mdm.currency.system.read",
                                    "mdm.currency.system.resend", "mdm.currency.system.recover"]},
    "mdm/partner": {"resource_type": ["mdm.partner", "mdm.partner_role", "mdm.partner.system"],
                    "permission": ["mdm.partner.read", "mdm.partner.create", "mdm.partner.edit", "mdm.partner.merge",
                                   "mdm.partner_role.read", "mdm.partner_role.manage", "mdm.partner.system.read",
                                   "mdm.partner.system.resend", "mdm.partner.system.recover"]},
    "mdm/product": {"resource_type": ["mdm.product", "mdm.product_category", "mdm.product.system"],
                    "permission": ["mdm.product.read", "mdm.product.edit", "mdm.product_category.read",
                                   "mdm.product_category.manage", "mdm.product.system.read", "mdm.product.system.resend", "mdm.product.system.recover"]},
    # C1 (3.0.1): employees, appointments and account links; its stream a watermark source the authorization member
    # pulls
    "mdm/employee": {"resource_type": ["mdm.employee", "mdm.employee.appointment", "mdm.employee.account_link",
                                       "mdm.employee.system"],
                     "permission": ["mdm.employee.read", "mdm.employee.manage", "mdm.employee.appointment.read",
                                    "mdm.employee.appointment.manage", "mdm.employee.account_link.read",
                                    "mdm.employee.account_link.manage", "mdm.employee.system.read",
                                    "mdm.employee.system.pull", "mdm.employee.system.resend", "mdm.employee.system.recover",
                                    "mdm.employee.system.replay"]},
}
# The business components' and the print component's resource types, operations and each resource type's dimensions
# (their definition.go and infra_print/__init__.py): for the registry, the grants and the segments' atoms.
ERP_KEYS = {
    "erp/inventory": {"resource_type": ["erp.inventory.warehouse", "erp.inventory.stock", "erp.inventory.movement",
                                        "erp.inventory.system"],
                      "permission": ["erp.inventory.warehouse.read", "erp.inventory.warehouse.manage",
                                     "erp.inventory.stock.read", "erp.inventory.movement.read",
                                     "erp.inventory.movement.create", "erp.inventory.adjustment.create",
                                     "erp.inventory.system.read", "erp.inventory.system.reserve",
                                     "erp.inventory.system.issue", "erp.inventory.system.resend", "erp.inventory.system.recover",
                                     # 3.0.37: the purchase component's receipts and returns
                                     "erp.inventory.system.receive"]},
    "erp/credit": {"resource_type": ["erp.credit.limit", "erp.credit.policy", "erp.credit.system"],
                   "permission": ["erp.credit.limit.read", "erp.credit.limit.manage", "erp.credit.limit.request",
                                  "erp.credit.policy.read", "erp.credit.policy.manage", "erp.credit.system.read",
                                  "erp.credit.system.reserve", "erp.credit.system.resend", "erp.credit.system.recover"]},
    "erp/sales": {"resource_type": ["erp.sales.quotation", "erp.sales.order", "erp.sales.delivery", "erp.sales.price_list",
                                    "erp.sales.return", "erp.sales.system"],
                  "permission": ["erp.sales.quotation.read", "erp.sales.quotation.edit", "erp.sales.order.read",
                                 "erp.sales.order.edit", "erp.sales.order.confirm", "erp.sales.order.cancel",
                                 "erp.sales.delivery.read", "erp.sales.delivery.create", "erp.sales.price_list.read",
                                 "erp.sales.price_list.manage", "erp.sales.return.read", "erp.sales.return.edit",
                                 "erp.sales.system.read", "erp.sales.system.billing", "erp.sales.system.resend", "erp.sales.system.recover",
                                 # the ended orders as a data class (3.0.35): the hold, the destruction's approval, the replay
                                 "erp.sales.system.order_hold", "erp.sales.system.order_destruction", "erp.sales.system.replay"]},
    "erp/billing": {"resource_type": ["erp.billing.invoice", "erp.billing.system"],
                    "permission": ["erp.billing.invoice.read", "erp.billing.invoice.draft", "erp.billing.invoice.issue",
                                   "erp.billing.invoice.print", "erp.billing.system.read",
                                   "erp.billing.system.resend", "erp.billing.system.recover"]},
    "erp/purchase": {"resource_type": ["erp.purchase.requisition", "erp.purchase.order", "erp.purchase.receipt",
                                       "erp.purchase.return", "erp.purchase.policy", "erp.purchase.system"],
                     "permission": ["erp.purchase.requisition.read", "erp.purchase.requisition.edit",
                                    "erp.purchase.requisition.approve", "erp.purchase.order.read",
                                    "erp.purchase.order.edit", "erp.purchase.order.approve", "erp.purchase.receipt.read",
                                    "erp.purchase.receipt.create", "erp.purchase.return.read", "erp.purchase.return.edit",
                                    "erp.purchase.policy.read", "erp.purchase.policy.manage", "erp.purchase.system.read",
                                    "erp.purchase.system.resend", "erp.purchase.system.recover"]},
    # C4 (3.0.0): the ledger, the open items and the supplier invoices; the system surface's claims are erp/payment's
    "erp/finance": {"resource_type": ["erp.finance.account", "erp.finance.voucher", "erp.finance.period",
                                      "erp.finance.open_item", "erp.finance.supplier_invoice", "erp.finance.system"],
                    "permission": ["erp.finance.account.read", "erp.finance.account.manage", "erp.finance.voucher.read",
                                   "erp.finance.voucher.edit", "erp.finance.voucher.approve",
                                   "erp.finance.period.read", "erp.finance.period.close",
                                   "erp.finance.open_item.read", "erp.finance.open_item.edit",
                                   "erp.finance.open_item.approve", "erp.finance.supplier_invoice.read",
                                   "erp.finance.supplier_invoice.edit", "erp.finance.supplier_invoice.approve",
                                   "erp.finance.system.read", "erp.finance.system.claim", "erp.finance.system.resend",
                                   "erp.finance.system.recover"]},
    # C3 (3.0.0): receipts, payment requests that claim erp/finance's open items, transfers and bank entries, statements
    "erp/payment": {"resource_type": ["erp.payment.bank_account", "erp.payment.receipt", "erp.payment.request",
                                      "erp.payment.entry", "erp.payment.statement", "erp.payment.system"],
                    "permission": ["erp.payment.bank_account.read", "erp.payment.bank_account.manage",
                                   "erp.payment.receipt.read", "erp.payment.receipt.register",
                                   "erp.payment.receipt.reverse", "erp.payment.request.read", "erp.payment.request.edit",
                                   "erp.payment.request.approve", "erp.payment.request.execute",
                                   "erp.payment.entry.read", "erp.payment.entry.edit", "erp.payment.entry.approve",
                                   "erp.payment.statement.read", "erp.payment.statement.reconcile",
                                   "erp.payment.system.read", "erp.payment.system.resend", "erp.payment.system.recover"]},
    # C5 (3.0.0): the value of stock by the valuation sequence; the cost facts for erp/finance
    "erp/costing": {"resource_type": ["erp.costing.policy", "erp.costing.valuation", "erp.costing.system"],
                    "permission": ["erp.costing.policy.read", "erp.costing.policy.manage", "erp.costing.valuation.read",
                                   "erp.costing.system.read", "erp.costing.system.resend", "erp.costing.system.recover"]},
    # C6 (3.0.0): opportunities, their stages and quote requests to erp/sales; the funnel
    "crm/opportunity": {"resource_type": ["crm.opportunity", "crm.opportunity.system"],
                        "permission": ["crm.opportunity.read", "crm.opportunity.edit", "crm.opportunity.close",
                                       "crm.opportunity.assign", "crm.opportunity.system.resend",
                                       "crm.opportunity.system.recover"]},
    "infra/print": {"resource_type": ["infra.print.template", "infra.print.system"],
                    "permission": ["infra.print.template.read", "infra.print.template.preview",
                                   "infra.print.template.edit", "infra.print.system.read", "infra.print.system.render",
                                   "infra.print.system.recover"]},
}
SALES_DOCUMENT = ["legal_entity", "department", "owner"]
DIMENSIONS = {"erp.inventory.warehouse": ["legal_entity", "warehouse"], "erp.inventory.stock": ["legal_entity", "warehouse"],
              "erp.inventory.movement": ["legal_entity", "warehouse"], "erp.credit.limit": ["legal_entity"],
              "erp.credit.policy": ["legal_entity"], "erp.sales.quotation": SALES_DOCUMENT,
              "erp.sales.order": SALES_DOCUMENT, "erp.sales.delivery": SALES_DOCUMENT, "erp.sales.return": SALES_DOCUMENT,
              "erp.sales.price_list": ["legal_entity"], "erp.billing.invoice": ["legal_entity"],
              "mdm.employee": ["legal_entity"], "mdm.employee.appointment": ["legal_entity", "department"],
              "mdm.employee.account_link": ["legal_entity"], "erp.purchase.requisition": SALES_DOCUMENT,
              "erp.purchase.order": SALES_DOCUMENT, "erp.purchase.receipt": SALES_DOCUMENT,
              "erp.purchase.return": SALES_DOCUMENT, "erp.purchase.policy": ["legal_entity"],
              "erp.finance.account": ["legal_entity"], "erp.finance.voucher": ["legal_entity"],
              "erp.finance.period": ["legal_entity"], "erp.finance.open_item": ["legal_entity", "department"],
              "erp.finance.supplier_invoice": ["legal_entity", "department"],
              "erp.payment.bank_account": ["legal_entity"], "erp.payment.receipt": ["legal_entity"],
              "erp.payment.request": ["legal_entity", "department"], "erp.payment.entry": ["legal_entity"],
              "erp.payment.statement": ["legal_entity"], "erp.costing.policy": ["legal_entity"],
              "erp.costing.valuation": ["legal_entity"], "crm.opportunity": ["legal_entity", "owner"]}
# the components whose events go through the bus (infra/print publishes none and declares no bus)
PRODUCERS = {"erp/inventory", "erp/credit", "erp/sales", "erp/billing", "erp/purchase", "erp/finance", "erp/payment",
             "erp/costing", "crm/opportunity"}
# the security members' streams (their definition.go; not named after the component)
INFRA_STREAMS = {"infra/iam-casdoor": ["be.session.ended", "infra.iam.account_status"]}
# each operation's resource type: the operation key less its last segment (mdm.partner_role.manage -> mdm.partner_role)
PROBE_OPS = ["read", "echo", "work", "write", "call"]  # the operations the load user is granted (and pay, secure.py)
# every key a probe defines (probe/defs.go, probe_py): the registry must own them all, or the authorization member
# refuses the probe's whole submission (DEFINITION_OWNER_MISMATCH); note since 3.2.22, pull since 3.2.31 (Go), the
# entries of the authorization profile since 3.2.38
PROBE_KEYS = PROBE_OPS + ["note", "pull", "resend", "pay", "recover"]  # resend: the redelivery since 3.2.39 (Go), P6.38; pay: the payment since 3.2.41 (Go); recover: a full recovery's checks since 3.2.42 (Go)
PROBE_ENTRY_OPS = ["read", "create", "move", "approve", "export"]


def probe_prefix(component: str) -> str:
    """The probes' resource type and operation prefix: test/probe-go2 -> test.probe_go2."""
    return component.replace("/", ".").replace("-", "_")
from harness import empty_prefixes, ensure_bucket, need

HEADER = "# Written by be-acceptance/conformance/cells/prepare.py for cell {cell}; regenerate instead of editing.\n"


async def databases(c: Cell) -> None:
    """Every database the cell's schemas live in (cells/homes.py), the shells' login roles on every instance used, and
    each component's schema and roles in its own home; a moved component's schema is dropped from the cell's database
    (an earlier cell's, or the checks' foreign tables)."""
    secrets = c.dir / ".secrets" / "db"
    secrets.mkdir(parents=True, exist_ok=True)
    for instance in dict.fromkeys(i for i, _ in c.homes):
        conn = await asyncpg.connect(homes.admin(instance))
        try:
            for _, database in (h for h in c.homes if h[0] == instance):
                if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", database):
                    await conn.execute(f'CREATE DATABASE "{database}"')
            for shell in c.shells:
                role = SHELL_ROLES[shell]
                if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role):
                    await conn.execute(f"CREATE ROLE {role} LOGIN")
                await conn.execute(f"ALTER ROLE {role} LOGIN PASSWORD '{role}-pw'")
                (secrets / role).write_text(f"{role}-pw")
        finally:
            await conn.close()
    hosted_by = {m: shell for shell, ms in c.shells.items() for m in ms}
    for probe in c.placed:
        s = schema_of(probe)
        shell_role = SHELL_ROLES[hosted_by[probe]] if probe in hosted_by else ""
        instance, database = c.home_of(probe)
        await testing.provision(homes.admin(instance), database, s, s + "_owner", s + "_rw", shell_role)
        for role in (s + "_owner", s + "_rw"):
            (secrets / role).write_text(f"{role}-pw")
    if moved := [schema_of(p) for p in c.placed if p in c.databases]:
        conn = await asyncpg.connect(homes.admin("main", c.database))
        try:
            for s in moved:
                await conn.execute(f"DROP SCHEMA IF EXISTS {s} CASCADE")
        finally:
            await conn.close()


def target(c: Cell, comp: str) -> list[str]:
    """A component's PG_HOST, PG_PORT and PG_DATABASE: the cell's shared values, or its own home (databases:)."""
    if comp not in c.databases:
        return ["PG_HOST: $var:PG_HOST", "PG_PORT: $var:PG_PORT", "PG_DATABASE: $var:PG_DATABASE"]
    instance, database = c.home_of(comp)
    return [f"PG_HOST: {c.base_host}", f'PG_PORT: "{INSTANCES[instance][0]}"', f"PG_DATABASE: {database}"]


def owner_key(c: Cell) -> None:
    """The deployment owner's confirmation key pair (security 08, the assembly project's A124): the public key for the
    authorization member's AUTHZ_OWNER_KEY, the private key for the seeds (SEED_OWNER_KEY) — kept offline by a real
    owner; a cell has no owner but the suite."""
    from cryptography.hazmat.primitives import serialization
    from cryptography.hazmat.primitives.asymmetric import ed25519
    key = ed25519.Ed25519PrivateKey.generate()
    secrets = c.dir / ".secrets"
    secrets.mkdir(exist_ok=True)
    (secrets / "owner.key.pem").write_bytes(key.private_bytes(serialization.Encoding.PEM,
                                                               serialization.PrivateFormat.PKCS8,
                                                               serialization.NoEncryption()))
    (secrets / "owner.key.pem").chmod(0o600)
    pub = secrets / "owner.pub.pem"
    pub.write_bytes(key.public_key().public_bytes(serialization.Encoding.PEM,
                                                   serialization.PublicFormat.SubjectPublicKeyInfo))
    pub.chmod(0o644)  # mounted into the member's container, which runs as another user


def install_claim(c: Cell) -> None:
    """The install claim (infra/authz 3.0.20, A130): a one-time code whose digest is the member's AUTHZ_INSTALL_CLAIM;
    secure.py presents the code as the administrator, once."""
    import hashlib
    code = secrets_mod.token_hex(16)
    sec = c.dir / ".secrets"
    sec.mkdir(exist_ok=True)
    (sec / "install-claim.code").write_text(code)
    (sec / "install-claim.code").chmod(0o600)
    claim = sec / "install-claim"
    claim.write_text("code:sha256:" + hashlib.sha256(code.encode()).hexdigest())
    claim.chmod(0o644)  # mounted into the member's container, which runs as another user


def be_ops() -> str:
    """The be-ops binary: BE_OPS, or be-ops on the PATH (tools/be-ops of the assembly project: go build ./cmd/be-ops)."""
    path = os.environ.get("BE_OPS") or shutil.which("be-ops")
    if not path:
        raise RuntimeError("a secured cell's certificates come from its issuer: set BE_OPS to a built be-ops")
    return path


def issuer(c: Cell, *args: str) -> None:
    """One be-ops trust command for the cell: the deployment's issuer (be-protocol P12.24-P12.27), its CA's key in
    .secrets/trust (never mounted into a component), its records in the run profile."""
    sec = c.dir / ".secrets"
    env = dict(os.environ, RUN_PROFILE_URL=need("BE_ACC_S3_URL"), RUN_PROFILE_BUCKET="be-run-profile",
               RUN_PROFILE_ACCESS_KEY=need("BE_ACC_S3_ACCESS_KEY"), RUN_PROFILE_SECRET_KEY=need("BE_ACC_S3_SECRET_KEY"))
    subprocess.run([be_ops(), "trust", *args, "--project", str(c.dir), "--deployment", c.deployment,
                    "--dir", str(sec / "trust"), "--bundle", str(sec / "tls" / "ca.pem")],
                   env=env, check=True, stdout=subprocess.DEVNULL)


def certificates(c: Cell) -> None:
    """Every placed component's service credential and be-ops recover's own (ops/recover signs the run profile). A
    secured cell's come from its issuer, a new CA per preparation, as a deployment's would; the others' from the SDK's
    test world (no run profile)."""
    sec = c.dir / ".secrets"
    tls = sec / "tls"
    for d in (tls, sec / "trust"):
        if d.exists():
            shutil.rmtree(d)
    tls.mkdir(parents=True)
    names = [*c.placed, "ops/recover"]
    if c.secured:
        issuer(c, "init")
        for comp in names:
            issuer(c, "issue", "--component", comp, "--name", file_of(comp), "--out", str(tls))
    else:
        world = testing.World(tls, c.deployment)
        for comp in names:
            cert, key, _ = world.certs(comp)
            shutil.move(cert, tls / f"{file_of(comp)}.cert.pem")
            shutil.move(key, tls / f"{file_of(comp)}.key.pem")
    # the files are mounted into containers that run as other users: readable, as a secret store would deliver them
    # (the CA's key stays in .secrets/trust, which no component mounts)
    for f in tls.iterdir():
        f.chmod(0o644)


async def clean_bus(c: Cell) -> None:
    if c.bus == "pgqueue":
        await pg_bus(c)
        return
    from besdk.events import jetstream

    tr = await jetstream.Transport.connect(need("BE_ACC_NATS_URL"), "cell-prepare")
    try:
        streams = [stream_of(p) for p in [*c.probes, *c.mdm, *(e for e in c.erp if e in PRODUCERS)]]
        # the security members' streams too: every run is a new deployment with a new implementation instance, and an
        # earlier run's events left in the shared NATS make the authorization member judge the account source foreign
        # (P9.11, blocked: the view closes and the install claim waits in vain; F3-S, items 3 and 4)
        streams += [s for p in c.infra for s in INFRA_STREAMS.get(p, [])]
        for stream in streams:
            try:
                await tr.delete_stream(stream)
            except Exception:  # noqa: BLE001 - not there
                pass
    finally:
        await tr.close()


async def pg_bus(c: Cell) -> None:
    """The PostgreSQL queue in the experiment database's own schema be_bus, owned by the bus role be_bus (fresh every
    time); every component reaches it with that role, as they share one NATS."""
    from besdk import obs
    from besdk.events import pgqueue

    admin = urlsplit(need("BE_ACC_PG_ADMIN"))
    conn = await asyncpg.connect(admin._replace(path="/" + c.database).geturl())
    try:
        if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'be_bus')"):
            await conn.execute("CREATE ROLE be_bus LOGIN")
        await conn.execute("ALTER ROLE be_bus LOGIN PASSWORD 'be_bus-pw'")
        await conn.execute("DROP SCHEMA IF EXISTS be_bus CASCADE")
        await conn.execute("CREATE SCHEMA be_bus AUTHORIZATION be_bus")
        await conn.execute(f'GRANT CONNECT ON DATABASE "{c.database}" TO be_bus')
    finally:
        await conn.close()
    host = f"be_bus:be_bus-pw@{admin.hostname}:{admin.port or 5432}"
    url = f"pgqueue://{host}/{c.database}?schema=be_bus&sslmode=disable"
    await pgqueue.migrate_bus(url, obs.new_logger(obs.Member("bus", "0"), "error", io.StringIO()))
    (c.dir / ".secrets").mkdir(exist_ok=True)
    (c.dir / ".secrets" / "bus").write_text(
        f"pgqueue://be_bus:be_bus-pw@{c.base_host}:5432/{c.database}?schema=be_bus&sslmode=disable")


def registry(c: Cell) -> str:
    """The registry's owners of every resource type and permission key the cell's components define (TSV)."""
    rows = ["kind\tkey\towner_component"]
    for comp, keys in {**INFRA_KEYS, **MDM_KEYS, **ERP_KEYS}.items():
        if comp in c.placed:
            rows += [f"{kind}\t{k}\t{comp}" for kind, ks in keys.items() for k in ks]
    for probe in c.probes:
        prefix = probe_prefix(probe)
        rows += [f"resource_type\t{prefix}\t{probe}", f"resource_type\t{prefix}.entry\t{probe}"]
        rows += [f"permission\t{prefix}.{op}\t{probe}" for op in PROBE_KEYS]
        rows += [f"permission\t{prefix}.entry.{op}\t{probe}" for op in PROBE_ENTRY_OPS]
    return "\n".join(rows) + "\n"


def signing_key(path: pathlib.Path) -> None:
    """The identity member's Ed25519 signing key (PKCS #8 PEM), new for every preparation."""
    from cryptography.hazmat.primitives import serialization
    from cryptography.hazmat.primitives.asymmetric import ed25519

    k = ed25519.Ed25519PrivateKey.generate()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                     serialization.NoEncryption()))
    path.chmod(0o644)  # read by a container user, as the TLS files


def declares(c: Cell, comp: str, key: str) -> bool:
    """Whether the version of comp the cell runs declares a configuration key. Unknown: no."""
    return key in schema_props(c, comp)


def schema_props(c: Cell, comp: str) -> dict:
    """The configuration keys the version of comp the cell runs declares: its manifest in BrickKit's cache (the create
    stage fetched it), a shell member's version read from its shell's manifest. Unknown: none."""
    versions = dict(c.versions)
    cache = c.dir / ".brickkit" / "manifests"
    for shell, version in list(versions.items()):
        m = cache / shell / version / "component.yaml"
        if m.exists():
            for ref in (yaml.safe_load(m.read_text()).get("shell") or {}).get("members") or []:
                versions.setdefault(ref.split("@")[0], ref.split("@")[1])
    m = cache / comp / versions.get(comp, "") / "component.yaml"
    if not versions.get(comp) or not m.exists():
        return {}
    return (yaml.safe_load(m.read_text()).get("configSchema") or {}).get("properties") or {}


# The keys a cell leaves unset on purpose, with why. Any other key a member declares without a default and the cell
# does not set fails the preparation (unset_keys): the run profile's keys went unset from T3 to T11 of foundations item
# 4, so no cell ever had the alert channel, and nobody noticed.
UNSET_ON_PURPOSE = {
    "OTEL_URL": "no tracing backend in a cell",
    "ALERT_WEBHOOK_URL": "no notifier in a cell; the alert channel's machine-readable copy is the run profile",
    "AUTHZ_RECOVERY_KEY": "the deployment owner's administrator recovery is tested by infra/authz, not in cells",
    "PG_MIGRATION_HOST": "set only behind a pooler (PG_HOST otherwise)",
    "PG_MIGRATION_PORT": "set only behind a pooler (PG_PORT otherwise)",
    "BUS_SUBSCRIPTION_CONCURRENCY": "the SDK's default unless a cell sets its concurrency",
    "PROBE_NEXT": "a probe without a next one in its chain",
    "PROBE_NEXT_URL": "a probe without a next one in its chain",
    "PROBE_NEXT_GRPC_URL": "a probe without a next one in its chain",
    "PROBE_PEER_STREAM": "a probe without a previous one in its chain",
    "PROBE_EXTERNAL_URL": "a probe that pays no external system: only the recovery drills' cells set it",
    "CLAIM_PERIOD": "erp/finance's payment claims last 7 days when unset",
    "MATCH_TOLERANCES": "erp/finance's tolerances are 0 in every legal entity when unset (the L3 mismatch and the "
                        "seed's price difference rely on it)",
    "FINANCE_FACT_SOURCES": "set when the cell places erp/payment or erp/costing (configs); without it erp/finance "
                            "subscribes to none",
    "VALUATION_GAP_WAIT": "erp/costing waits 5 minutes for a gap in a valuation sequence when unset (protocol 05b)",
    "VALUATION_BASIS_WAIT": "erp/costing waits 5 minutes for a purchase receipt's valuation basis when unset (protocol 05b; "
                            "erp/purchase publishes the basis before it receives, so a cell does not wait)",
}
# The same for keys named after a data class (be-protocol chapter 15), by their prefix.
UNSET_PREFIXES_ON_PURPOSE = {
    "TIERING_RESIDENCE_": "the class's declared residence",
    "TIERING_RETENTION_": "the retention is the customer's (TR24): pending in a cell, so nothing is ever destroyed",
    "TIERING_ROUTE_": "the class's first route unless the cell's settings pick one",
}


def unset_keys(c: Cell) -> list[str]:
    """Every "member: KEY" the cell's configuration leaves unset although the member's version declares it without a
    default (its description names one when the SDK has it) and it is not unset on purpose."""
    problems = []
    for comp in [*c.placed, *c.shells]:
        f = c.dir / "config" / f"{file_of(comp)}.yaml"
        if not f.exists():
            continue
        conf = yaml.safe_load(f.read_text()) or {}
        for key, prop in schema_props(c, comp).items():
            if key in conf or key in UNSET_ON_PURPOSE or key.startswith(tuple(UNSET_PREFIXES_ON_PURPOSE)) or \
                    "default" in prop or "default" in (prop.get("description") or "").lower():
                continue
            problems.append(f"{comp}: {key}")
    return problems


def security_configs(c: Cell) -> None:
    """The three security members' config files (step 5): the identity member in front of the local Casdoor (its
    development exchange on: a test deployment, A101), the authorization member with the cell's registry, the audit
    component. The install claim is a one-time code's digest (install_claim); secure.py presents the code."""
    cfg, sec = c.dir / "config", c.dir / ".secrets"
    signing_key(sec / "iam" / "signing-key.pem")
    signing_key(sec / "iam" / "signing-key-next.pem")  # the standby the next rotation (and a recovery's 6.8) moves to
    (sec / "authz-registry.tsv").write_text(registry(c))
    (sec / "authz-registry.tsv").chmod(0o644)
    common = ["DEPLOYMENT_ID: $var:DEPLOYMENT_ID", *migration(c)]

    def db(comp: str) -> list[str]:
        s = schema_of(comp)
        return [*target(c, comp), f"PG_USER: {s}_rw", f"PG_PASSWORD: file://.secrets/db/{s}_rw", f"PG_SCHEMA: {s}",
                f"PG_OWNER_USER: {s}_owner", f"PG_OWNER_PASSWORD: file://.secrets/db/{s}_owner"]

    def guarded(comp: str) -> list[str]:
        f = file_of(comp)
        return ["IAM_ISSUER: $var:IAM_ISSUER", "IAM_JWKS_URL: $var:IAM_JWKS_URL", "AUTHZ_GRPC_URL: $var:AUTHZ_GRPC_URL",
                "AUDIT_GRPC_URL: $var:AUDIT_GRPC_URL", f"SERVICE_TLS_CERT_FILE: file://.secrets/tls/{f}.cert.pem",
                f"SERVICE_TLS_KEY_FILE: file://.secrets/tls/{f}.key.pem", "SERVICE_TLS_CA_FILE: file://.secrets/tls/ca.pem"]

    files = {
        "infra/iam-casdoor": [*common, *db("infra/iam-casdoor"), "IAM_TOKEN_ISSUER: $var:IAM_ISSUER",
                              "IAM_SIGNING_KEY: file://.secrets/iam/signing-key.pem",
                              "IAM_SIGNING_KEY_NEXT: file://.secrets/iam/signing-key-next.pem",
                              "IAM_CALLBACK_URL: http://localhost:8200/api/iam/callback", "IAM_COOKIE_SECURE: \"false\"",
                              f"CASDOOR_URL: http://{c.base_host}:8000", "CASDOOR_PUBLIC_URL: http://localhost:8000",
                              "CASDOOR_CLIENT_ID: be-iam-client", "CASDOOR_CLIENT_SECRET: ${BE_ACC_IAM_CLIENT_SECRET}",
                              "IAM_DEV_EXCHANGE: \"true\"", "IAM_DEV_CLIENT_ID: be-iam-dev-client",
                              "IAM_DEV_CLIENT_SECRET: ${BE_ACC_IAM_DEV_CLIENT_SECRET}",
                              # 3.0.16 on: system methods over mTLS (sessions, the account status stream's pull and
                              # read for infra/authz), its definitions submitted, the stream on the bus
                              *guarded("infra/iam-casdoor"), "BUS_URL: file://.secrets/bus",
                              f"IMPLEMENTATION_INSTANCE: {uuid.uuid4()}"],
        "infra/authz": [*common, *db("infra/authz"), *guarded("infra/authz"), f"AUTHZ_INSTANCE: {uuid.uuid4()}",
                        "AUTHZ_REGISTRY: file://.secrets/authz-registry.tsv",
                        # 3.0.18 on: the deployment owner's confirmation key (A124), for protected changes
                        "AUTHZ_OWNER_KEY: file://.secrets/owner.pub.pem",
                        # 3.0.20 on: the install claim, a one-time code's digest (A130)
                        "AUTHZ_INSTALL_CLAIM: file://.secrets/install-claim",
                        # 3.0.16 on: the identity member's account status, a watermark source (security 03)
                        "IAM_GRPC_URL: $var:IAM_GRPC_URL", "IAM_COMPONENT: $var:IAM_COMPONENT",
                        "BUS_URL: file://.secrets/bus",
                        # 3.0.46 on: the employee member, the second internal source (links and appointments)
                        *(["EMPLOYEE_GRPC_URL: $endpoint:mdm/employee:grpc", "EMPLOYEE_COMPONENT: mdm/employee"]
                          if "mdm/employee" in c.placed else [])],
        "infra/audit": [*common, *db("infra/audit"), *guarded("infra/audit")],
    }
    for comp in c.mdm:  # the master data components: a database, the bus, a service identity, guarded routes
        files[comp] = [*common, *db(comp), *guarded(comp), "BUS_URL: file://.secrets/bus",
                       f"IMPLEMENTATION_INSTANCE: {uuid.uuid4()}"]
    for comp in c.erp:  # the business components and print: a database, a service identity, guarded routes
        files[comp] = [*common, *db(comp), *guarded(comp)]
        if comp in PRODUCERS:
            files[comp] += ["BUS_URL: file://.secrets/bus", f"IMPLEMENTATION_INSTANCE: {uuid.uuid4()}"]
    if "mdm/org" in files:
        files["mdm/org"].append("DEFAULT_LEGAL_ENTITY_TIMEZONE: Asia/Shanghai")
    sources = []  # the assembly's fact sources (erp/finance's ST01, AF01)
    if "erp/payment" in c.placed:
        sources += [{"stream": "erp.payment", "type": "erp.payment.fund_fact.v1"},
                    {"stream": "erp.payment", "type": "erp.payment.accounting_fact.v1"}]
    if "erp/costing" in c.placed:
        sources += [{"stream": "erp.costing", "type": "erp.costing.accounting_fact.v1"}]
    if "erp/finance" in files and sources:
        files["erp/finance"].append(f"FINANCE_FACT_SOURCES: {json.dumps(json.dumps(sources))}")
    for comp, lines in files.items():
        if comp in c.placed:
            if declares(c, comp, "RECOVERY_PHASE"):  # the recovery phase (be-protocol P1.22): a shared variable
                lines = [*lines, "RECOVERY_PHASE: $var:RECOVERY_PHASE"]
            if declares(c, comp, "OBJECT_STORE_URL"):  # a producer's large events travel by claim check (P6.14): the
                # same object store for every member, its own prefix (as the probes have)
                lines = [*lines, "OBJECT_STORE_URL: $var:OBJECT_STORE_URL", "OBJECT_STORE_BUCKET: $var:OBJECT_STORE_BUCKET",
                         f"OBJECT_STORE_PREFIX: {schema_of(comp)}/", "OBJECT_STORE_ACCESS_KEY: ${BE_ACC_S3_ACCESS_KEY}",
                         "OBJECT_STORE_SECRET_KEY: ${BE_ACC_S3_SECRET_KEY}"]
            lines = [*lines, *profile_and_tiering(c, comp), *settings_of(c, comp)]
            (cfg / f"{file_of(comp)}.yaml").write_text(HEADER.format(cell=c.name) + "\n".join(lines) + "\n")


def profile_and_tiering(c: Cell, comp: str) -> list[str]:
    """The run profile and the tiering's cold store, for a member (component or probe) that declares them."""
    lines = []
    if declares(c, comp, "RUN_PROFILE_URL"):  # an evidence writer's alert channel (P12.7, P12.16), and when the latest
        # backup finished (P15.21: a tiering member releases online rows only after one): the run profile outside the
        # recovery group; without it an intent the evidence store does not take is lost
        lines += ["RUN_PROFILE_URL: $var:RUN_PROFILE_URL", "RUN_PROFILE_BUCKET: $var:RUN_PROFILE_BUCKET",
                  "RUN_PROFILE_ACCESS_KEY: ${BE_ACC_S3_ACCESS_KEY}", "RUN_PROFILE_SECRET_KEY: ${BE_ACC_S3_SECRET_KEY}"]
    if declares(c, comp, "TIERING_STORE_URL"):  # the cold tier's object store (be-protocol chapter 15): one bucket for
        # the cell; the store's three identities (write, read, delete) are one credential here, separate in a deployment
        lines += ["TIERING_STORE_URL: $var:OBJECT_STORE_URL", "TIERING_BUCKET: $var:TIERING_BUCKET"]
        for role in ("WRITE", "READ", "DELETE"):
            lines += [f"TIERING_{role}_ACCESS_KEY: ${{BE_ACC_S3_ACCESS_KEY}}",
                      f"TIERING_{role}_SECRET_KEY: ${{BE_ACC_S3_SECRET_KEY}}"]
    return lines


def settings_of(c: Cell, comp: str) -> list[str]:
    """The cell's extra settings of this member (cell.yaml's settings)."""
    return [f"{key}: {json.dumps(value)}" for key, value in c.settings.get(comp, {}).items()]


def migration(c: Cell) -> list[str]:
    """Behind a pooler a component's migrations connect to the database itself (PG_MIGRATION_HOST, _PORT)."""
    return ["PG_MIGRATION_HOST: $var:PG_MIGRATION_HOST", "PG_MIGRATION_PORT: $var:PG_MIGRATION_PORT"] if c.pooler else []


def pgbouncer(c: Cell) -> None:
    """PgBouncer in transaction mode in front of the experiment database (step 10, B1), as its container reads it:
    every component logs in as its own role, which PgBouncer looks up with the admin (auth_query), one pool per role
    of 5 server connections (about what a shell member uses, step 9); protocol-level prepared statements on, as pgx
    needs (0-12)."""
    admin = urlsplit(need("BE_ACC_PG_ADMIN"))
    d = c.dir / ".secrets" / "pgbouncer"
    d.mkdir(parents=True, exist_ok=True)
    (d / "pgbouncer.ini").write_text(f"""[databases]
{c.database} = host=be-postgres port=5432 dbname={c.database}

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
auth_user = {admin.username}
pool_mode = transaction
default_pool_size = 5
max_client_conn = 1000
max_prepared_statements = 200
ignore_startup_parameters = extra_float_digits
""")
    (d / "userlist.txt").write_text(f'"{admin.username}" "{admin.password}"\n')
    for f in d.iterdir():
        f.chmod(0o644)  # read by the container's own user


def configs(c: Cell) -> None:
    cfg = c.dir / "config"
    # a bus URL may carry credentials, so the component declares it secret: it is delivered from a file
    if c.bus == "jetstream":
        (c.dir / ".secrets" / "bus").write_text(f"nats://{c.base_host}:4222")
    if c.pooler:
        pgbouncer(c)
    (cfg / "vars.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_HOST: {c.base_host}
PG_PORT: "{6432 if c.pooler else 5432}"
PG_DATABASE: {c.database}
""" + (f"""PG_MIGRATION_HOST: {c.base_host}
PG_MIGRATION_PORT: "5432"
""" if c.pooler else "") + f"""OBJECT_STORE_URL: http://{c.base_host}:9000
OBJECT_STORE_BUCKET: be-acceptance
# the run profile (be-protocol chapter 12): an object lock bucket outside the recovery group
RUN_PROFILE_URL: http://{c.base_host}:9000
RUN_PROFILE_BUCKET: be-run-profile
# the cold tier's bucket (be-protocol chapter 15)
TIERING_BUCKET: be-tiering
DEPLOYMENT_ID: {c.deployment}
# the full recovery's phase (A140; be-ops recover sets it): empty while running
RECOVERY_PHASE: ""
""" + ("""# the tokens' iss is an identifier, the same string for every component wherever it runs: a $endpoint resolves
# per caller (a service name in a container, localhost:port on this machine), so LD and LP would refuse the tokens
IAM_ISSUER: https://iam.be-acceptance.invalid
IAM_JWKS_URL: $endpoint:infra/iam-casdoor/api/iam/jwks
AUTHZ_GRPC_URL: $endpoint:infra/authz:grpc
AUDIT_GRPC_URL: $endpoint:infra/audit:grpc
# the identity member as the authorization member reads it: changed together when the member is switched
IAM_GRPC_URL: $endpoint:infra/iam-casdoor:grpc
IAM_COMPONENT: infra/iam-casdoor
""" if c.secured else ""))
    if c.secured:
        security_configs(c)
    for shell in c.shells:
        role = SHELL_ROLES[shell]
        (cfg / f"{file_of(shell)}.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_SHELL_USER: {role}
PG_SHELL_PASSWORD: file://.secrets/db/{role}
""")
    for probe in c.probes:
        s, f = schema_of(probe), file_of(probe)
        lines = [*target(c, probe), *migration(c),
                 f"PG_USER: {s}_rw", f"PG_PASSWORD: file://.secrets/db/{s}_rw", f"PG_SCHEMA: {s}",
                 f"PG_OWNER_USER: {s}_owner", f"PG_OWNER_PASSWORD: file://.secrets/db/{s}_owner",
                 "BUS_URL: file://.secrets/bus", f"IMPLEMENTATION_INSTANCE: {uuid.uuid4()}",
                 "OBJECT_STORE_URL: $var:OBJECT_STORE_URL", "OBJECT_STORE_BUCKET: $var:OBJECT_STORE_BUCKET",
                 f"OBJECT_STORE_PREFIX: {s}/", "OBJECT_STORE_ACCESS_KEY: ${BE_ACC_S3_ACCESS_KEY}",
                 "OBJECT_STORE_SECRET_KEY: ${BE_ACC_S3_SECRET_KEY}", "DEPLOYMENT_ID: $var:DEPLOYMENT_ID",
                 f"SERVICE_TLS_CERT_FILE: file://.secrets/tls/{f}.cert.pem",
                 f"SERVICE_TLS_KEY_FILE: file://.secrets/tls/{f}.key.pem",
                 "SERVICE_TLS_CA_FILE: file://.secrets/tls/ca.pem"]
        if nxt := c.next_of(probe):
            lines += [f"PROBE_NEXT: {nxt}", f"PROBE_NEXT_URL: $endpoint:{nxt}", f"PROBE_NEXT_GRPC_URL: $endpoint:{nxt}:grpc"]
        if prev := c.previous_of(probe):
            lines.append(f"PROBE_PEER_STREAM: {stream_of(prev)}")
        if c.concurrency != 1:
            lines.append(f"BUS_SUBSCRIPTION_CONCURRENCY: {c.concurrency}")
        if declares(c, probe, "RECOVERY_PHASE"):  # the recovery phase (P1.22) since Go 3.2.50, Python 3.2.43: alone, a
            # probe reads it like any member (in a shell the shell's own does)
            lines.append("RECOVERY_PHASE: $var:RECOVERY_PHASE")
        lines += [*profile_and_tiering(c, probe), *settings_of(c, probe)]
        if c.secured:  # the routes need a token and their operation; the view and the audit records are real
            lines += ["PROBE_GUARD: \"true\"", "IAM_ISSUER: $var:IAM_ISSUER", "IAM_JWKS_URL: $var:IAM_JWKS_URL",
                      "AUTHZ_GRPC_URL: $var:AUTHZ_GRPC_URL", "AUDIT_GRPC_URL: $var:AUDIT_GRPC_URL"]
        (cfg / f"{f}.yaml").write_text(HEADER.format(cell=c.name) + "\n".join(lines) + "\n")
    for ref in c.also:  # an older version beside the default: the same targets, the same implementation instance
        cid, version = ref.split("@")
        (cfg / f"{file_of(cid)}@{version}.yaml").write_text((cfg / f"{file_of(cid)}.yaml").read_text())


async def join_shell(directory: str | pathlib.Path, shell: str, members: list[str]) -> str:
    """Suite C, R5: components running alone in a running cell move into a shell. The shell's login role must exist
    and hold each member's runtime role WITH INHERIT FALSE (switched per transaction), and the shell's config must name
    it: what prepare() does when a cell is made, without touching any schema (the cell's data stays)."""
    c = Cell.load(directory)
    role = SHELL_ROLES[shell]
    secrets = c.dir / ".secrets" / "db"
    secrets.mkdir(parents=True, exist_ok=True)
    for instance in dict.fromkeys(c.home_of(m)[0] for m in members):  # roles belong to an instance
        conn = await asyncpg.connect(homes.admin(instance))
        try:
            if not await conn.fetchval("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role):
                await conn.execute(f"CREATE ROLE {role} LOGIN")
            await conn.execute(f"ALTER ROLE {role} LOGIN PASSWORD '{role}-pw'")
            for m in members:
                if c.home_of(m)[0] == instance:
                    await conn.execute(f"GRANT {schema_of(m)}_rw TO {role} WITH INHERIT FALSE, SET TRUE")
        finally:
            await conn.close()
    (secrets / role).write_text(f"{role}-pw")
    (c.dir / "config" / f"{file_of(shell)}.yaml").write_text(HEADER.format(cell=c.name) + f"""PG_SHELL_USER: {role}
PG_SHELL_PASSWORD: file://.secrets/db/{role}
""")
    return role


async def prepare(directory: str | pathlib.Path) -> Cell:
    c = Cell.load(directory)
    await databases(c)
    await ensure_bucket()
    await ensure_bucket("be-tiering")  # the cold tier's (profile_and_tiering)
    if c.secured:
        await ensure_bucket("be-run-profile", object_lock=True)  # before the certificates: the issuer records in it
    certificates(c)
    if c.secured:
        owner_key(c)
        install_claim(c)
    await empty_prefixes([schema_of(p) + "/" for p in c.probes])  # the prefixes configs() gives the probes
    # the cold tier's prefixes (the SDK's: the component ID, "/" as "-"): a deployment's recovery check lists its
    # prefix, and objects another cell or the tiering profile left there are orphans to it (T10e)
    await empty_prefixes(sorted({comp.replace("/", "-") + "/" for comp in c.placed
                                 if declares(c, comp, "TIERING_STORE_URL")}), name="be-tiering")
    await clean_bus(c)
    configs(c)
    if problems := unset_keys(c):
        raise ValueError("declared without a default and left unset by the cell (set them in prepare.py, or add them "
                         "to UNSET_ON_PURPOSE with why): " + ", ".join(problems))
    return c
