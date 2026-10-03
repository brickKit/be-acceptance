-- conformance/rawstub: conformance/peer's owners, kept from conformance.owner.updated.v1 (P15).
CREATE TABLE IF NOT EXISTS owner_snapshots (
    owner_id     text          PRIMARY KEY,
    display_name text          NOT NULL,
    credit_limit numeric(19,4) NOT NULL,
    currency     char(3)       NOT NULL,
    version      bigint        NOT NULL
);
