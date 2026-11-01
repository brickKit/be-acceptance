-- 3.2.23 (Go) and 3.2.22 (Python), an expand migration (suite D, V1): a write's optional label, and the type of each event received. An
-- older probe still running on this schema writes and reads as before.
ALTER TABLE probe_writes ADD COLUMN label text;
ALTER TABLE probe_seen ADD COLUMN type text;
