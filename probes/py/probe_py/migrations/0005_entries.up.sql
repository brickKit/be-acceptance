-- The authorization profile's entries (since 3.2.38): one column per dimension, NULL a known null. The Go probe's
-- 0008_entries (the Python probe has no jobs, cron or watermark migrations yet: its numbers run on their own).
CREATE TABLE probe_entries (
    id           uuid PRIMARY KEY,
    number       text NOT NULL,
    legal_entity text,
    dept_path    text,
    owner_id     uuid,
    amount       numeric,
    currency     text,
    cost         numeric NOT NULL,
    note         text NOT NULL,
    approved     boolean NOT NULL DEFAULT false
);
