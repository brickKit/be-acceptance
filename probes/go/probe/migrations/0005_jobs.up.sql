-- 3.2.30 (Go; the Python probe follows in the foundations' item 6), an expand migration for the suite's background
-- work profile: what each attempt of a probe job did, as the suite reads it with the item's own row.
CREATE TABLE probe_job_requests (id text PRIMARY KEY); -- the business row each enqueue writes with its item
CREATE TABLE probe_job_attempts (
    item         text NOT NULL,
    attempt      integer NOT NULL, -- counted by the probe: an attempt given back on stop (P7.8) still counts here
    step         integer NOT NULL DEFAULT 0,
    operation_id text NOT NULL,
    instance     text NOT NULL, -- the probe process that ran it
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended        text, -- done, failed, cancelled; NULL when the process died in it
    ended_at     timestamptz
);
CREATE TABLE probe_job_effects (
    item         text NOT NULL,
    step         integer NOT NULL DEFAULT 0,
    operation_id text NOT NULL,
    instance     text NOT NULL,
    at           timestamptz NOT NULL DEFAULT now()
);
-- the far side of the external calls: an idempotent one takes each key once, a non-idempotent one every call
CREATE TABLE probe_job_calls (
    key        text NOT NULL,
    item       text NOT NULL,
    idempotent boolean NOT NULL,
    instance   text NOT NULL,
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX probe_job_calls_once ON probe_job_calls (key) WHERE idempotent;
CREATE TABLE probe_job_dead (item text PRIMARY KEY, cause text NOT NULL, at timestamptz NOT NULL DEFAULT now());
-- the steps singleton's one task (PROBE_STEPS_EVERY)
CREATE TABLE probe_steps (
    id        integer PRIMARY KEY CHECK (id = 1),
    n         integer NOT NULL,
    hold_step integer NOT NULL,
    held      boolean NOT NULL DEFAULT false
);
