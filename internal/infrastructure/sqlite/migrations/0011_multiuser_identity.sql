-- 0011_multiuser_identity: multi-user data model — additive only.
--
-- New tables identity / session / api_token / project_member plus the
-- nullable ownership columns project.created_by and task.created_by, in
-- preparation for per-identity authentication (multi-user epic, ticket T1).
-- Nothing here is destructive and no existing row is mutated:
--   * Existing name-keyed actor data (comment, artifact, event,
--     acceptance_criterion.completed_by, task.assignee / task.claim) stays
--     exactly as it is; the new identity rows are pure additions.
--   * Every backfilled identity gets auth_method='shared_token' and a NULL
--     credential_hash — the legacy shared RAVN_TOKEN flow keeps working
--     unchanged, and this migration gates nothing.
--   * created_by columns are nullable, stay NULL on all legacy rows, and
--     every existing INSERT lists its columns explicitly, so no statement
--     changes shape.
--
-- Backfill: exactly one identity row per distinct actor name already stored
-- anywhere (the six free-form name columns above). kind is inferred from the
-- *_type value stored next to each occurrence: 'agent' only when every
-- observed type says agent, else 'human'. last_active_at is the newest
-- timestamp recorded next to the name (NULL where not recoverable).
-- Backfilled ids are random 26-char Crockford-base32 strings (ULID-shaped;
-- identities created later by Go code use time-sortable ULIDs from idgen).
-- The id is deliberately NOT derived from the name, so a renamed actor can
-- never collide with or inherit an old identity's future credentials. The
-- UNIQUE constraint on name plus ON CONFLICT(name) DO NOTHING make even a
-- manual re-execution of this statement safe: no duplicate identities are
-- ever possible (the runner itself applies each file exactly once — see
-- schema_migrations in the runner doc).
--
-- REVERSE (manual only; the runner has no down-migration mechanism):
--   ALTER TABLE task DROP COLUMN created_by;
--   ALTER TABLE project DROP COLUMN created_by;
--   DROP TABLE IF EXISTS project_member, api_token, session, identity;
-- (SQLite >= 3.35 supports DROP COLUMN.)

CREATE TABLE identity (
  id              TEXT PRIMARY KEY,             -- ULID
  name            TEXT NOT NULL UNIQUE,         -- stable identity key (was the free-form actor name)
  kind            TEXT NOT NULL DEFAULT 'human' CHECK (kind IN ('human','agent')),
  auth_method     TEXT NOT NULL DEFAULT 'none'
                  CHECK (auth_method IN ('password','api_token','shared_token','none')),
  credential_hash TEXT,                         -- argon2id PHC string; NULL until a credential is set
  disabled_at     TEXT,                         -- RFC3339; NULL = enabled
  created_at      TEXT NOT NULL,                -- RFC3339
  last_active_at  TEXT                           -- RFC3339; refreshed on successful auth
);

CREATE TABLE session (
  id          TEXT PRIMARY KEY,                 -- ULID
  identity_id TEXT NOT NULL REFERENCES identity(id) ON DELETE CASCADE,
  token_hash  TEXT NOT NULL UNIQUE,             -- SHA-256 hex of the opaque session token
  created_at  TEXT NOT NULL,                    -- RFC3339
  expires_at  TEXT NOT NULL,                    -- RFC3339
  revoked_at  TEXT,                             -- RFC3339; NULL = active
  meta        TEXT NOT NULL DEFAULT ''          -- JSON: user agent, ip, etc.
);
CREATE INDEX idx_session_identity ON session(identity_id) WHERE revoked_at IS NULL;

CREATE TABLE api_token (
  id           TEXT PRIMARY KEY,                -- ULID
  identity_id  TEXT NOT NULL REFERENCES identity(id) ON DELETE CASCADE,
  token_hash   TEXT NOT NULL UNIQUE,            -- SHA-256 hex of the Bearer secret
  label        TEXT NOT NULL DEFAULT '',        -- human description ("hollen's laptop")
  created_at   TEXT NOT NULL,                   -- RFC3339
  last_used_at TEXT,                            -- RFC3339; refreshed lazily on auth
  disabled_at  TEXT                             -- RFC3339; NULL = enabled
);
CREATE INDEX idx_api_token_identity ON api_token(identity_id) WHERE disabled_at IS NULL;

-- Optional per-project membership. Additive: no rows exist yet and no legacy
-- query references the table, so a project with zero members behaves exactly
-- as before (open to whoever holds the shared token).
CREATE TABLE project_member (
  project_id  TEXT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  identity_id TEXT NOT NULL REFERENCES identity(id) ON DELETE CASCADE,
  role        TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer','editor','admin')),
  PRIMARY KEY (project_id, identity_id)
);

ALTER TABLE project ADD COLUMN created_by TEXT REFERENCES identity(id);   -- nullable; legacy rows stay NULL
ALTER TABLE task    ADD COLUMN created_by TEXT REFERENCES identity(id);   -- nullable; legacy rows stay NULL

-- Backfill identities from every free-form actor-name column (single statement:
-- CTE chain + INSERT, so the whole thing applies atomically with the DDL above).
WITH RECURSIVE
actors(name, typ, last_at) AS (
  SELECT actor_name, actor_type, created_at FROM comment
  UNION ALL
  SELECT actor_name, actor_type, created_at FROM artifact
  UNION ALL
  SELECT actor_name, actor_type, ts             FROM event
  UNION ALL
  SELECT completed_by_name, completed_by_type, completed_at
    FROM acceptance_criterion WHERE completed_by_name IS NOT NULL
  UNION ALL
  SELECT assignee_name, assignee_type, NULL     FROM task WHERE assignee_name IS NOT NULL
  UNION ALL
  SELECT claimed_by_name, claimed_by_type, claimed_at
    FROM task WHERE claimed_by_name IS NOT NULL
),
distinct_names(name, kind, last_active_at) AS (
  SELECT name,
         CASE WHEN SUM(CASE WHEN typ = 'agent' THEN 1 ELSE 0 END) > 0
                   AND SUM(CASE WHEN typ = 'human' THEN 1 ELSE 0 END) = 0
              THEN 'agent' ELSE 'human' END,
         MAX(last_at)
    FROM actors
   WHERE TRIM(name) <> ''
   GROUP BY name
),
-- One random 26-char Crockford-base32 id per name (ULID shape). The recursion
-- streams the hex of a fresh randomblob(16) left to right; whenever five or
-- more bits have accumulated it emits one base-32 digit and carries the
-- remainder forward (invariant: bits stays in 0..4). randomblob() in the
-- select list is evaluated per row, so every identity gets its own seed.
-- After all 32 hex nibbles are consumed exactly 25 digits have been emitted
-- with 3 bits left over; ids below appends the final digit from those.
enc(name, hexs, pos, acc, bits, digits) AS (
  SELECT dn.name, hex(randomblob(16)), 1, 0, 0, ''
    FROM distinct_names dn
  UNION ALL
  SELECT e.name, e.hexs, e.pos + 1,
         -- next value c = (acc << 4) | nibble; remainder after emitting a digit
         CASE e.bits
           WHEN 0 THEN e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)
           WHEN 1 THEN 0
           WHEN 2 THEN (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) % 2
           WHEN 3 THEN (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) % 4
           ELSE        (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) % 8
         END,
         CASE WHEN e.bits = 0 THEN 4 ELSE e.bits - 1 END,
         -- digit emitted this step (none while bits = 0)
         CASE e.bits
           WHEN 0 THEN e.digits
           WHEN 1 THEN e.digits || SUBSTR('0123456789ABCDEFGHJKMNPQRSTVWXYZ',
                        e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1) + 1, 1)
           WHEN 2 THEN e.digits || SUBSTR('0123456789ABCDEFGHJKMNPQRSTVWXYZ',
                        (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) / 2 + 1, 1)
           WHEN 3 THEN e.digits || SUBSTR('0123456789ABCDEFGHJKMNPQRSTVWXYZ',
                        (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) / 4 + 1, 1)
           ELSE        e.digits || SUBSTR('0123456789ABCDEFGHJKMNPQRSTVWXYZ',
                        (e.acc * 16 + (INSTR('0123456789ABCDEF', SUBSTR(e.hexs, e.pos, 1)) - 1)) / 8 + 1, 1)
         END
    FROM enc e
   WHERE e.pos <= 32
),
ids(id, name) AS (
  SELECT e.digits || SUBSTR('0123456789ABCDEFGHJKMNPQRSTVWXYZ',
             (e.acc * CASE e.bits WHEN 0 THEN 32 WHEN 1 THEN 16 WHEN 2 THEN 8 ELSE 4 END) + 1, 1),
         e.name
    FROM enc e
   WHERE e.pos = 33
)
INSERT INTO identity(id, name, kind, auth_method, credential_hash, disabled_at, created_at, last_active_at)
SELECT i.id, d.name, d.kind, 'shared_token', NULL, NULL,
       strftime('%Y-%m-%dT%H:%M:%SZ','now'), d.last_active_at
  FROM distinct_names d
  JOIN ids i ON i.name = d.name
ON CONFLICT(name) DO NOTHING;
