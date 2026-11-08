package sqlite

import (
	"context"
	"strings"
	"testing"
)

// Adversarial/hostile-input cases for the 0011 multi-user backfill and DDL:
// blank and SQL-ish actor names, oversized names, one name present in all six
// storage columns at once, constraint violations, and late-arriving actors.

func TestMigration0011_BackfillHostileActorNames(t *testing.T) {
	t.Parallel()
	db, _ := openLegacyDB(t, lastLegacyFile)
	ctx := context.Background()

	longName := "hostile-" + strings.Repeat("x", 496) // 500 chars total
	sqlishName := "Robert'); DROP TABLE identity;--"

	seed := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO project (id, name, description, created_at, updated_at)
		   VALUES ('p-hostile', 'Hostile Project', '', '2026-01-05T10:00:00Z', '2026-01-05T10:00:00Z')`, nil},
		{`INSERT INTO task (id, project_id, status, title, description, priority, due_at, notes,
		                    assignee_type, assignee_name, claimed_by_type, claimed_by_name,
		                    claimed_at, claim_expires_at, claim_ttl_sec, version, archived, created_at, updated_at, completed_at)
		   VALUES ('t-hostile', 'p-hostile', 'todo', 'Hostile task', '', 0, NULL, '',
		           'agent', ?, 'agent', ?, NULL, NULL, NULL, 0, 0,
		           '2026-01-05T10:00:00Z', '2026-01-05T10:00:00Z', NULL)`, []any{"everywhere-actor", "everywhere-actor"}},
		{`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, 't-hostile', 'note', '', 'human', ?, '2026-01-05T10:00:00Z')`, []any{"c-h1", "   "}},
		{`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, 't-hostile', 'note', '', 'human', ?, '2026-01-05T10:00:00Z')`, []any{"c-h2", ""}},
		{`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, 't-hostile', 'note', '', 'human', ?, '2026-01-05T10:00:00Z')`, []any{"c-h3", sqlishName}},
		{`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, 't-hostile', 'note', '', 'human', ?, '2026-01-05T10:00:00Z')`, []any{"c-h4", " pad"}},
		{`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at) VALUES (?, 't-hostile', 'note', '', 'agent', ?, '2026-01-05T10:00:00Z')`, []any{"c-h5", "everywhere-actor"}},
		{`INSERT INTO artifact (id, task_id, kind, ref, summary, actor_type, actor_name, created_at) VALUES ('a-h1', 't-hostile', 'url', 'x', '', 'agent', ?, '2026-01-05T10:00:00Z')`, []any{"everywhere-actor"}},
		{`INSERT INTO acceptance_criterion (id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at) VALUES ('ac-h1', 't-hostile', 1, 't', 1, '', 'agent', ?, '2026-01-05T10:00:00Z')`, []any{"everywhere-actor"}},
		{`INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts) VALUES ('e-h1', 'p-hostile', 't-hostile', 'agent', ?, 'noted', NULL, NULL, '2026-01-05T10:00:00Z')`, []any{"everywhere-actor"}},
		{`INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts) VALUES ('e-h2', 'p-hostile', NULL, 'agent', ?, 'project-updated', NULL, NULL, '2026-01-05T10:00:00Z')`, []any{longName}},
	}
	for _, s := range seed {
		if _, err := db.ExecContext(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("seed hostile world: %v", err)
		}
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	got := identitiesByName(t, db)
	cases := []struct {
		name     string
		wantKind string
	}{
		{sqlishName, "human"},
		{" pad", "human"},
		{"everywhere-actor", "agent"}, // present in ALL six columns at once → exactly one row
		{longName, "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := got[tc.name]
			if !ok {
				t.Fatalf("identity %q missing from backfill (have %v)", tc.name, namesOf(got))
			}
			if r.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", r.Kind, tc.wantKind)
			}
			if len(r.ID) != 26 || !isCrockfordBase32(t, r.ID) {
				t.Errorf("id %q is not ULID-shaped", r.ID)
			}
		})
	}
	// Blank and empty names are excluded; everything else survives verbatim.
	if len(got) != 4 {
		t.Fatalf("identity rows = %d (%v), want 4 (blank/empty names excluded)", len(got), namesOf(got))
	}
	if r, ok := got[longName]; ok && r.Name != longName {
		t.Errorf("long name stored as %q, want it verbatim", r.Name)
	}

	// The tables must still exist and be readable (the SQL-ish name is DATA, not a statement).
	if n := tableCount(t, db, "identity"); n != 4 {
		t.Fatalf("re-count identity = %d, want 4", n)
	}
}

func TestMigration0011_ConstraintsRejectInvalidRows(t *testing.T) {
	t.Parallel()
	db := openTestDB(t) // fresh → already migrated
	ctx := context.Background()

	expectConstraintError := func(what, stmt string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, stmt, args...)
		if err == nil {
			t.Errorf("%s: expected a constraint error, got none", what)
			return
		}
		if !strings.Contains(strings.ToLower(err.Error()), "constraint") {
			t.Errorf("%s: unexpected error %q, want a constraint violation", what, err)
		}
	}

	// Valid baseline rows so each hostile case violates exactly one thing.
	baseline := []string{
		`INSERT INTO project (id, name, description, created_at, updated_at)
		   VALUES ('p-c', 'C Project', '', '2026-01-05T10:00:00Z', '2026-01-05T10:00:00Z')`,
		`INSERT INTO identity (id, name, created_at) VALUES ('i-real', 'real-name', '2026-01-05T10:00:00Z')`,
		`INSERT INTO identity (id, name, created_at) VALUES ('i-dup', 'dup-name', '2026-01-05T10:00:00Z')`,
		`INSERT INTO session (id, identity_id, token_hash, created_at, expires_at)
		   VALUES ('s-base', 'i-real', 'shared-hash', '2026-01-05T10:00:00Z', '2026-01-05T11:00:00Z')`,
	}
	for _, s := range baseline {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed baseline row: %v", err)
		}
	}

	expectConstraintError("identity kind CHECK",
		`INSERT INTO identity (id, name, kind, created_at) VALUES ('i-x1', 'x1', 'robot', '2026-01-05T10:00:00Z')`)
	expectConstraintError("identity auth_method CHECK",
		`INSERT INTO identity (id, name, auth_method, created_at) VALUES ('i-x2', 'x2', 'magic', '2026-01-05T10:00:00Z')`)
	expectConstraintError("identity name UNIQUE (duplicate)",
		`INSERT INTO identity (id, name, created_at) VALUES ('i-x3', 'dup-name', '2026-01-05T10:00:00Z')`)
	expectConstraintError("identity created_at NOT NULL",
		`INSERT INTO identity (id, name) VALUES ('i-x4', 'x4')`)
	expectConstraintError("session FK to identity",
		`INSERT INTO session (id, identity_id, token_hash, created_at, expires_at)
		   VALUES ('s-x1', 'no-such-identity', 'abc123', '2026-01-05T10:00:00Z', '2026-01-05T11:00:00Z')`)
	expectConstraintError("session token_hash UNIQUE (duplicate)",
		`INSERT INTO session (id, identity_id, token_hash, created_at, expires_at)
		   VALUES ('s-x2', 'i-real', 'shared-hash', '2026-01-05T10:00:00Z', '2026-01-05T11:00:00Z')`)
	expectConstraintError("api_token FK to identity",
		`INSERT INTO api_token (id, identity_id, token_hash, created_at)
		   VALUES ('t-x1', 'no-such-identity', 'def456', '2026-01-05T10:00:00Z')`)
	expectConstraintError("project_member role CHECK",
		`INSERT INTO project_member (project_id, identity_id, role) VALUES ('p-c', 'i-real', 'superuser')`)

	// A project with no members is still fine (project_member is optional and additive).
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_member`).Scan(&n); err != nil || n != 0 {
		t.Errorf("project_member rows = %d (err=%v), want 0", n, err)
	}
}

func TestMigration0011_LateActorsNotRetroBackfilled(t *testing.T) {
	t.Parallel()
	db, _ := openLegacyDB(t, lastLegacyFile)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO project (id, name, description, created_at, updated_at)
		VALUES ('p-late', 'Late Project', '', '2026-01-05T10:00:00Z', '2026-01-05T10:00:00Z')`); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts)
		VALUES ('e-early', 'p-late', NULL, 'human', 'early-actor', 'project-created', NULL, NULL, '2026-01-05T10:00:00Z')`); err != nil {
		t.Fatalf("seed early event: %v", err)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	got := identitiesByName(t, db)
	if _, ok := got["early-actor"]; !ok {
		t.Fatalf("early-actor not backfilled: %v", namesOf(got))
	}

	// An actor that only appears AFTER the migration must NOT be retro-created
	// by a later (no-op) pass; runtime identity creation belongs to follow-up
	// auth tickets, not to the one-time backfill.
	if _, err := db.ExecContext(ctx, `INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts)
		VALUES ('e-late', 'p-late', NULL, 'agent', 'latecomer', 'project-updated', NULL, NULL, '2026-01-05T11:00:00Z')`); err != nil {
		t.Fatalf("seed late event: %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	got = identitiesByName(t, db)
	if _, ok := got["latecomer"]; ok {
		t.Errorf("latecomer was retro-backfilled; the backfill must be a one-time snapshot")
	}
	if len(got) != 1 {
		t.Errorf("identity rows = %d (%v), want 1", len(got), namesOf(got))
	}
}
