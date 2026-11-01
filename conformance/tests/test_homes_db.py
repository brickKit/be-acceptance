"""cells/homes.py against a real PostgreSQL (BE_ACC_PG_ADMIN, the base resources' be-postgres; not in `make check`):
a schema moved to another database of the same instance is provisioned there, and the checks' connection to the
cell's database reads and joins it as foreign tables, tables made after the first mapping included. The second
instance differs only in the server's host (tests/test_homes.py); the E5 cells exercise it."""
import asyncpg

from cells import homes, prepare
from cells.spec import Cell

DB, AUDIT_DB = "be_acc_homes", "be_acc_homes_audit"


async def drop_databases():
    conn = await asyncpg.connect(homes.admin("main"))
    try:
        for d in (DB, AUDIT_DB):
            await conn.execute(f'DROP DATABASE IF EXISTS "{d}" WITH (FORCE)')
    finally:
        await conn.close()


async def test_a_schema_in_another_database_is_read_and_joined_from_the_cells_database(tmp_path):
    (tmp_path / "cell.yaml").write_text(f"cell: T\ndatabase: {DB}\nalone: [infra/audit@3.0.11, erp/sales@3.0.18]\n"
                                        f"chain: []\ndatabases:\n  infra/audit: {AUDIT_DB}\n")
    c = Cell.load(tmp_path)
    await drop_databases()
    try:
        await prepare.databases(c)
        audit, where = await homes.home(c, "infra_audit")
        assert where == AUDIT_DB
        try:
            await audit.execute("CREATE TABLE infra_audit.audit_record (source text, source_seq bigint)")
            await audit.execute("INSERT INTO infra_audit.audit_record VALUES ('erp/sales', 1), ('erp/sales', 2)")
        finally:
            await audit.close()
        sales, where = await homes.home(c, "erp_sales")
        assert where == DB
        try:
            await sales.execute("CREATE TABLE erp_sales.be_audit_log (seq bigint)")
            await sales.execute("INSERT INTO erp_sales.be_audit_log VALUES (1), (2), (3)")
        finally:
            await sales.close()

        conn = await homes.checks(c)
        try:
            assert await conn.fetchval("SELECT count(*) FROM erp_sales.be_audit_log l JOIN infra_audit.audit_record r "
                                       "ON r.source = 'erp/sales' AND r.source_seq = l.seq") == 2
            assert await conn.fetchval("SELECT table_schema FROM information_schema.tables "
                                       "WHERE table_name = 'audit_record'") == "infra_audit", "the checks list it"
        finally:
            await conn.close()

        audit, _ = await homes.home(c, "infra_audit")
        try:
            await audit.execute("CREATE TABLE infra_audit.intake (n int)")
        finally:
            await audit.close()
        conn = await homes.checks(c)
        try:
            assert await conn.fetchval("SELECT count(*) FROM infra_audit.intake") == 0, "mapped again on connecting"
        finally:
            await conn.close()
    finally:
        await drop_databases()
