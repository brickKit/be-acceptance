-- 3.2.47 (Python; the Go probe's 0007_watermark_records since 3.2.35): a watermark source declares its batch read and
-- snapshot (P9.30): which writes went to the watermark stream, and the one field of their event the table did not
-- keep, so the records are the events' full payloads.
ALTER TABLE probe_writes ADD COLUMN watermark boolean NOT NULL DEFAULT false;
ALTER TABLE probe_writes ADD COLUMN relay boolean NOT NULL DEFAULT false;
