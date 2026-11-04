-- 3.2.35 (Go): a watermark source declares its batch read and snapshot (P9.30, be-sdk-go alpha.57): which writes
-- went to the watermark stream, and the one field of their event the table did not keep, so the records are the
-- events' full payloads.
ALTER TABLE probe_writes ADD COLUMN watermark boolean NOT NULL DEFAULT false;
ALTER TABLE probe_writes ADD COLUMN relay boolean NOT NULL DEFAULT false;
