-- conformance/rawstub: the facts data scopes filter by, and the archive command's state.
ALTER TABLE notes ADD COLUMN IF NOT EXISTS dept_path   text        NOT NULL DEFAULT '';
ALTER TABLE notes ADD COLUMN IF NOT EXISTS kind        text        NOT NULL DEFAULT 'plain';
ALTER TABLE notes ADD COLUMN IF NOT EXISTS archived_at timestamptz;
ALTER TABLE notes ADD COLUMN IF NOT EXISTS version     bigint      NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS notes_created ON notes (created_at DESC, id DESC);
