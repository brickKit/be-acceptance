-- The tiering profile's archive (since 3.2.47, Go; assembly protocol 11): a root with a column of every type the
-- canonical row knows, its parts, and their pieces (a leaf with a key of two columns), closed or open.
CREATE TABLE probe_archive (
    id         uuid PRIMARY KEY,
    batch      text NOT NULL,
    closed_at  timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    t          text,
    i2         smallint,
    i4         integer,
    i8         bigint,
    n          numeric(19,4),
    b          boolean,
    d          date,
    j          jsonb,
    bin        bytea,
    tags       text[]
);
CREATE TABLE probe_archive_part (
    id         uuid PRIMARY KEY,
    archive_id uuid NOT NULL REFERENCES probe_archive (id),
    label      text NOT NULL
);
CREATE TABLE probe_archive_piece (
    part_id uuid NOT NULL REFERENCES probe_archive_part (id),
    seq     integer NOT NULL,
    note    text,
    PRIMARY KEY (part_id, seq)
);
