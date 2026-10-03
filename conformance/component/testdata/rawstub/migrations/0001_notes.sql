-- conformance/rawstub: one table. Unqualified names, no GRANT / OWNER / SET (be-protocol P11.2).
CREATE TABLE IF NOT EXISTS notes (
    id         uuid        PRIMARY KEY,
    title      text        NOT NULL,
    owner_id   text        NOT NULL,
    created_at timestamptz NOT NULL
);
