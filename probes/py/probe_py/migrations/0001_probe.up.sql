-- What the probe writes (Write) and what it received from its peer (its event stream), with the relay's progress.
CREATE TABLE probe_writes (
    id         uuid PRIMARY KEY,
    size       integer NOT NULL,
    written_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE probe_seen (
    event_id    uuid PRIMARY KEY,
    source      text NOT NULL,
    size        integer NOT NULL,
    relay       boolean NOT NULL,
    relayed_at  timestamptz,
    received_at timestamptz NOT NULL DEFAULT now()
);

-- The relay's queue: the Python SDK has no background work items yet, so the probe keeps its own (the same claim
-- rules: FOR UPDATE SKIP LOCKED, a lease that expires, done only after the call).
CREATE TABLE probe_relay (
    event_id   uuid PRIMARY KEY REFERENCES probe_seen (event_id),
    leased_to  timestamptz,
    attempts   integer NOT NULL DEFAULT 0
);
