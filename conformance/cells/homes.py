"""Where each component's schema lives (suite E: E5 a second instance, E6 a database per component, E7 the audit
component's own database), and the connections the tools open to it.

A cell's `databases:` moves components out of its `database` on the first instance. The checks (invariants, digest,
the participants of a fault) join schemas across components, so they keep one connection to the cell's database: a
moved schema appears there as foreign tables of the same name (postgres_fdw, IMPORT FOREIGN SCHEMA), mapped again on
every connection so that tables made since (partitions, a later migration) are there too. Whatever must act on a
schema itself (a fault's trigger, pg_stat_activity of its database) connects to the component's home."""
from __future__ import annotations

from urllib.parse import urlsplit

import asyncpg

from cells.spec import INSTANCES, Cell, schema_of
from harness import need


def admin(instance: str, database: str = "postgres") -> str:
    """The admin DSN of an instance and database, from the host (BE_ACC_PG_ADMIN names the first instance; the
    others take the same credentials on their own host port)."""
    u = urlsplit(need("BE_ACC_PG_ADMIN"))
    userinfo = u.netloc.rpartition("@")[0]
    netloc = (userinfo + "@" if userinfo else "") + f"{u.hostname}:{INSTANCES[instance][0]}"
    return u._replace(netloc=netloc, path="/" + database).geturl()


def server_of(instance: str, database: str) -> str:
    return f"cell_{instance}_{database}"


def mapping_sql(c: Cell, user: str, password: str) -> list[str]:
    """The statements that show every moved schema in the cell's database: a foreign server per home (reached from
    inside the first instance), the admin's user mapping, and the schema imported afresh."""
    moved = {comp: c.home_of(comp) for comp in c.placed if comp in c.databases}
    if not moved:
        return []
    out = ["CREATE EXTENSION IF NOT EXISTS postgres_fdw"]
    for instance, database in sorted(set(moved.values())):
        s = server_of(instance, database)
        out += [f"DROP SERVER IF EXISTS {s} CASCADE",
                f"CREATE SERVER {s} FOREIGN DATA WRAPPER postgres_fdw OPTIONS "
                f"(host '{INSTANCES[instance][1]}', port '5432', dbname '{database}')",
                f"CREATE USER MAPPING FOR CURRENT_USER SERVER {s} OPTIONS (user '{user}', password '{password}')"]
    for comp, (instance, database) in sorted(moved.items()):
        s = schema_of(comp)
        out += [f"DROP SCHEMA IF EXISTS {s} CASCADE", f"CREATE SCHEMA {s}",
                f"IMPORT FOREIGN SCHEMA {s} FROM SERVER {server_of(instance, database)} INTO {s}"]
    return out


async def checks(c: Cell) -> asyncpg.Connection:
    """A connection to the cell's database with every component's schema in it (the moved ones as foreign tables)."""
    conn = await asyncpg.connect(admin("main", c.database))
    u = urlsplit(need("BE_ACC_PG_ADMIN"))
    if sql := mapping_sql(c, u.username or "postgres", u.password or ""):
        try:
            async with conn.transaction():  # two tools connecting at once map one after the other
                await conn.execute("SELECT pg_advisory_xact_lock(hashtext('cells.homes'))")
                for s in sql:
                    await conn.execute(s)
        except BaseException:
            await conn.close()
            raise
    return conn


async def home(c: Cell, schema: str) -> tuple[asyncpg.Connection, str]:
    """A connection to the database that holds a schema itself, and that database's name."""
    comp = next((p for p in c.placed if schema_of(p) == schema), "")
    instance, database = c.home_of(comp) if comp else ("main", c.database)
    return await asyncpg.connect(admin(instance, database)), database
