-- 3.2.24 (Go) and 3.2.23 (Python), a contract migration (suite D, V1's counterexample): probe_writes.size becomes
-- bytes. A probe before these versions still inserts into size and fails; nothing refuses it before the statement
-- runs (the assembly project's step 12 plan, F1).
ALTER TABLE probe_writes RENAME COLUMN size TO bytes;
