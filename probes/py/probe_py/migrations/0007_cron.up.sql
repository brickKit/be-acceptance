-- 3.2.44 (Python; the Go probe since 3.2.32), an expand migration: what the cron job of the background work profile
-- did, one row a run, without a unique constraint so a slot run twice shows (CK-WK-05).
CREATE TABLE probe_cron (
    slot     text NOT NULL,
    instance text NOT NULL,
    at       timestamptz NOT NULL DEFAULT now()
);
