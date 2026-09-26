-- 0001_init: full crowban MVP schema (ARCHITECTURE.md §4) + seed statuses.
-- Plain versioned SQL, embedded and applied by the runner in filename order;
-- tracked in schema_migrations below. New changes go in new numbered files —
-- this one is never edited after application.

PRAGMA foreign_keys=ON;  -- also enforced per-connection by driver DSN pragma

CREATE TABLE status (                 -- workflow statuses are NAMED DATA, not an enum
  name         TEXT PRIMARY KEY,      -- seeded: 'todo','doing','done' (Tier-2 inserts more, no migration needed)
  display_name TEXT NOT NULL,
  sort_order   INTEGER NOT NULL UNIQUE,-- board column order
  is_terminal  INTEGER NOT NULL DEFAULT 0
);
INSERT INTO status(name,display_name,sort_order,is_terminal) VALUES
 ('todo','Todo',0,0),('doing','Doing',1,0),('done','Done',2,1);

CREATE TABLE project (
  id          TEXT PRIMARY KEY,        -- ULID
  name        TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',-- markdown; doubles as the agent onboarding doc
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE task (
  id                TEXT PRIMARY KEY,
  project_id        TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  status            TEXT NOT NULL DEFAULT 'todo' REFERENCES status(name),
  title             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',   -- markdown
  priority          INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 5),
  due_at            TEXT,                        -- nullable RFC3339
  notes             TEXT NOT NULL DEFAULT '',    -- durable resume context; replace-on-write
  assignee_type     TEXT CHECK (assignee_type IN ('human','agent')),   -- durable ownership, nullable
  assignee_name     TEXT,
  claimed_by_type   TEXT CHECK (claimed_by_type IN ('human','agent')), -- ephemeral lease holder, nullable
  claimed_by_name   TEXT,
  claimed_at        TEXT,
  claim_expires_at  TEXT,
  claim_ttl_sec     INTEGER,                   -- TTL stored at claim; heartbeat reuses it
  version           INTEGER NOT NULL DEFAULT 0,-- bumped ONLY by task-row mutations (see below)
  archived          INTEGER NOT NULL DEFAULT 0,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  completed_at      TEXT                        -- set when status→done
);
-- Canonical ordering (the "what's next" index). SQLite has no NULLS LAST keyword:
-- (due_at IS NULL) sorts 0 before 1 → nulls last. EXACT expression used in every ORDER BY:
CREATE INDEX idx_task_order ON task(project_id, priority DESC, due_at ASC, created_at ASC) WHERE archived = 0;
CREATE INDEX idx_task_lookup ON task(project_id, status);

CREATE TABLE acceptance_criterion (
  id                TEXT PRIMARY KEY,
  task_id           TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq               INTEGER NOT NULL,          -- 1-based display order; complete_ac targets seq
  text              TEXT NOT NULL,
  done              INTEGER NOT NULL DEFAULT 0,
  note              TEXT NOT NULL DEFAULT '',  -- optional note recorded on check
  completed_by_type TEXT, completed_by_name TEXT, completed_at TEXT,
  UNIQUE (task_id, seq)
);

CREATE TABLE comment (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('progress','question','decision','handoff','note')),
  body       TEXT NOT NULL,                    -- markdown
  actor_type TEXT NOT NULL CHECK (actor_type IN ('human','agent')),
  actor_name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_comment_task ON comment(task_id, created_at);

CREATE TABLE artifact (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('log','screenshot','command-output','trace','url')),
  ref        TEXT NOT NULL,                    -- typed reference: URL or filesystem path (no uploads in MVP)
  summary    TEXT NOT NULL DEFAULT '',         -- one line
  actor_type TEXT NOT NULL, actor_name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_artifact_task ON artifact(task_id, created_at);

CREATE TABLE relation (                        -- directed edges; inverses ("blocks","parent-of") are computed
  id            TEXT PRIMARY KEY,
  task_id       TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,   -- source
  other_task_id TEXT NOT NULL REFERENCES task(id) ON DELETE CASCADE,   -- target
  kind          TEXT NOT NULL CHECK (kind IN ('parent','child','blocked-by','duplicate-of','related')),
  created_at    TEXT NOT NULL,
  UNIQUE (task_id, other_task_id, kind),
  CHECK (task_id <> other_task_id)
);
CREATE UNIQUE INDEX idx_one_parent ON relation(task_id) WHERE kind = 'parent';  -- ≤1 parent per task
CREATE INDEX idx_relation_target   ON relation(other_task_id);

CREATE TABLE event (                            -- append-only activity log; every mutation appends here
  id           TEXT PRIMARY KEY,
  project_id   TEXT REFERENCES project(id) ON DELETE CASCADE,          -- null for task-level events? no — always set when derivable
  task_id      TEXT REFERENCES task(id)     ON DELETE CASCADE,         -- null only for pure project-level events
  actor_type   TEXT NOT NULL, actor_name TEXT NOT NULL,
  kind         TEXT NOT NULL,              -- created|updated|status-changed|claimed|released|heartbeat|noted|commented|ac-toggled|artifact-added|relation-added|relation-removed|project-created|project-updated
  before_json  TEXT,                        -- compact JSON of changed values before (nullable)
  after_json   TEXT,                        -- compact JSON after (nullable)
  ts           TEXT NOT NULL
);
CREATE INDEX idx_event_task    ON event(task_id, ts);
CREATE INDEX idx_event_project ON event(project_id, ts);

-- IF NOT EXISTS: the runner creates this table before checking for applied
-- files, so the definition must be idempotent (everything else above is not).
CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
