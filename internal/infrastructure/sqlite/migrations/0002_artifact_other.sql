-- 0002_artifact_other: extend the artifact.kind CHECK with the 'other' escape
-- hatch (ADR, 2026-09-25) so domain.ArtifactKind and the DDL agree on:
--   log | screenshot | command-output | trace | url | other
-- SQLite cannot ALTER a table's CHECK constraint, so the table is rebuilt the
-- standard way. Safe to run once (runner guarantees that); the dance is also
-- harmless if replayed against an already-migrated schema: it produces the
-- same DDL and copies the same rows back. Runs inside one transaction; if any
-- step fails the whole migration rolls back, including the bookkeeping row.

CREATE TABLE artifact_new (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('log','screenshot','command-output','trace','url','other')),
  ref        TEXT NOT NULL,                    -- typed reference: URL or filesystem path (no uploads in MVP)
  summary    TEXT NOT NULL DEFAULT '',         -- one line
  actor_type TEXT NOT NULL, actor_name TEXT NOT NULL,
  created_at TEXT NOT NULL
);

INSERT INTO artifact_new (id, task_id, kind, ref, summary, actor_type, actor_name, created_at)
SELECT id, task_id, kind, ref, summary, actor_type, actor_name, created_at FROM artifact;

-- No other table references artifact (only outbound FK to task), so the drop
-- is legal with foreign_keys=ON and without the 12-step pragma dance. Dropping
-- the table also drops idx_artifact_task; recreate it after the rename.
DROP TABLE artifact;
ALTER TABLE artifact_new RENAME TO artifact;

CREATE INDEX idx_artifact_task ON artifact(task_id, created_at);
