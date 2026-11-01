-- 3.2.26 (Go) and 3.2.25 (Python), an expand migration for suite D's V1 again on SDKs with P5.32: an older probe on
-- this schema keeps running, and its migration, run again, takes the newer database as done.
ALTER TABLE probe_seen ADD COLUMN received_by text;
