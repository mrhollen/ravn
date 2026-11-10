package sqlite

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
)

// The migration under test and the newest file a pre-0011 binary would have
// known about — the "old world" boundary for upgrade tests.
const (
	migration0011  = "migrations/0011_multiuser_identity.sql"
	lastLegacyFile = "migrations/0010_collect_task_metrics_indexes.sql"
)

// openLegacyDB opens a fresh DB file and applies every embedded migration up
// to and including upTo (lexical order) — exactly the on-disk state a binary
// predating 0011 would have produced, so tests can prove 0011 is a clean
// upgrade path for existing single-tenant databases.
func openLegacyDB(t *testing.T, upTo string) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// applyMigration assumes the runner has already ensured schema_migrations.
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("ensure schema_migrations: %v", err)
	}

	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	found := false
	for _, f := range files {
		if err := applyMigration(ctx, db, f); err != nil {
			t.Fatalf("apply legacy migration %s: %v", f, err)
		}
		if f == upTo {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("migration %q not in embedded set", upTo)
	}
	return db, path
}

// --- seeded legacy world -------------------------------------------------------
//
// Realistic single-tenant data spread across ALL six free-form actor-name
// columns: comments, artifacts, events, acceptance-criterion completers,
// task assignees and claim holders.
const (
	legacyTS  = "2026-01-05T10:00:00Z"
	legacyTS2 = "2026-01-05T11:30:00Z"
)

func seedLegacyWorld(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`INSERT INTO project (id, name, description, created_at, updated_at)
		 VALUES ('p-leg-1', 'Legacy Project', 'legacy desc', '2026-01-05T10:00:00Z', '2026-01-05T11:30:00Z')`,
		`INSERT INTO project (id, name, description, created_at, updated_at)
		 VALUES ('p-leg-2x', 'Legacy Project B', '', '2026-01-05T10:00:00Z', '2026-01-05T10:00:00Z')`,
		`INSERT INTO task (id, project_id, status, title, description, priority, due_at, notes,
		                  assignee_type, assignee_name, claimed_by_type, claimed_by_name,
		                  claimed_at, claim_expires_at, claim_ttl_sec, version, archived, created_at, updated_at, completed_at)
		 VALUES ('t-leg-1', 'p-leg-1', 'doing', 'Legacy task one', 'legacy desc', 3,
		         '2026-01-09T00:00:00Z', 'notes here', 'human', 'hollen', 'agent', 'devin',
		         '2026-01-05T11:00:00Z', '2026-01-05T12:30:00Z', 900, 4, 0,
		         '2026-01-05T10:00:00Z', '2026-01-05T11:30:00Z', NULL)`,
		`INSERT INTO task (id, project_id, status, title, description, priority, due_at, notes,
		                  assignee_type, assignee_name, claimed_by_type, claimed_by_name,
		                  claimed_at, claim_expires_at, claim_ttl_sec, version, archived, created_at, updated_at, completed_at)
		 VALUES ('t-leg-2', 'p-leg-2x', 'todo', 'Legacy task two', '', 0, NULL, '',
		         'agent', 'unicode-æøå-🚀', NULL, NULL, NULL, NULL, NULL, 0, 0,
		         '2026-01-05T10:00:00Z', '2026-01-05T11:30:00Z', NULL)`,
		`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		 VALUES ('c-leg-1', 't-leg-1', 'progress', 'first note', 'human', 'hollen', '2026-01-05T10:00:00Z')`,
		`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		 VALUES ('c-leg-2', 't-leg-1', 'question', 'needs input', 'agent', 'devin', '2026-01-05T11:30:00Z')`,
		`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		 VALUES ('c-leg-3', 't-leg-2', 'note', 'm1', 'human', 'mixed-bob', '2026-01-05T10:00:00Z')`,
		`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		 VALUES ('c-leg-4', 't-leg-2', 'progress', 'm2', 'agent', 'mixed-bob', '2026-01-05T11:30:00Z')`,
		`INSERT INTO comment (id, task_id, kind, body, actor_type, actor_name, created_at)
		 VALUES ('c-leg-5', 't-leg-2', 'note', 'u', 'agent', 'unicode-æøå-🚀', '2026-01-05T11:30:00Z')`,
		`INSERT INTO artifact (id, task_id, kind, ref, summary, actor_type, actor_name, created_at)
		 VALUES ('a-leg-1', 't-leg-1', 'url', 'https://example.com/legacy', 'legacy link', 'agent', 'devin', '2026-01-05T11:30:00Z')`,
		`INSERT INTO acceptance_criterion (id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at)
		 VALUES ('ac-leg-1', 't-leg-1', 1, 'ship it', 1, 'done', 'human', 'hollen', '2026-01-05T11:30:00Z')`,
		`INSERT INTO acceptance_criterion (id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at)
		 VALUES ('ac-leg-2', 't-leg-1', 2, 'not yet', 0, '', NULL, NULL, NULL)`,
		`INSERT INTO acceptance_criterion (id, task_id, seq, text, done, note, completed_by_type, completed_by_name, completed_at)
		 VALUES ('ac-leg-3', 't-leg-2', 1, 'u ac', 1, '', 'agent', 'unicode-æøå-🚀', '2026-01-05T11:30:00Z')`,
		`INSERT INTO relation (id, task_id, other_task_id, kind, created_at)
		 VALUES ('r-leg-1', 't-leg-1', 't-leg-2', 'related', '2026-01-05T10:00:00Z')`,
		`INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts)
		 VALUES ('e-leg-1', 'p-leg-1', 't-leg-1', 'agent', 'devin', 'claimed', NULL, NULL, '2026-01-05T11:30:00Z')`,
		`INSERT INTO event (id, project_id, task_id, actor_type, actor_name, kind, before_json, after_json, ts)
		 VALUES ('e-leg-2', 'p-leg-1', NULL, 'human', 'hollen', 'project-created', NULL, NULL, '2026-01-05T10:00:00Z')`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed legacy world: %v", err)
		}
	}
}

// --- identity read helpers ------------------------------------------------------

type identityRow struct {
	Name       string
	Kind       string
	AuthMethod string
	CredHash   sql.NullString
	LastActive sql.NullString
	CreatedAt  sql.NullString
	DisabledAt sql.NullString
	ID         string
}

func identitiesByName(t *testing.T, db *sql.DB) map[string]identityRow {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, kind, auth_method, credential_hash, disabled_at, created_at, last_active_at
		   FROM identity ORDER BY name`)
	if err != nil {
		t.Fatalf("query identities: %v", err)
	}
	defer rows.Close()

	out := map[string]identityRow{}
	for rows.Next() {
		var r identityRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.AuthMethod, &r.CredHash, &r.DisabledAt, &r.CreatedAt, &r.LastActive); err != nil {
			t.Fatalf("scan identity: %v", err)
		}
		out[r.Name] = r
	}
	return out
}

func tableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// --- AC1: schema is created correctly -------------------------------------------

func TestMigration0011_CreatesMultiUserSchema(t *testing.T) {
	t.Parallel()
	db := openTestDB(t) // fresh DB → full migration set incl. 0011
	ctx := context.Background()

	cases := []struct {
		table string
		cols  []string
	}{
		{"identity", []string{"id", "name", "kind", "auth_method", "credential_hash", "disabled_at", "created_at", "last_active_at"}},
		{"session", []string{"id", "identity_id", "token_hash", "created_at", "expires_at", "revoked_at", "meta"}},
		{"api_token", []string{"id", "identity_id", "token_hash", "label", "created_at", "last_used_at", "disabled_at"}},
		{"project_member", []string{"project_id", "identity_id", "role"}},
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			var n int
			if err := db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tc.table).Scan(&n); err != nil || n != 1 {
				t.Fatalf("table %s missing (count=%d, err=%v)", tc.table, n, err)
			}
			got, err := tableColumns(t, db, tc.table)
			if err != nil {
				t.Fatalf("table_info %s: %v", tc.table, err)
			}
			assertEqualStrings(t, got, tc.cols, "columns of "+tc.table)
		})
	}

	// Nullable ownership columns added to the legacy tables.
	projectCols, err := tableColumns(t, db, "project")
	if err != nil {
		t.Fatalf("table_info project: %v", err)
	}
	assertContains(t, projectCols, "created_by", "project columns")
	taskCols, err := tableColumns(t, db, "task")
	if err != nil {
		t.Fatalf("table_info task: %v", err)
	}
	assertContains(t, taskCols, "created_by", "task columns")

	// created_by must be NULLABLE (no NOT NULL flag, no default).
	for _, tc := range []struct{ table, col string }{
		{"project", "created_by"},
		{"task", "created_by"},
	} {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+tc.table+`)`)
		if err != nil {
			t.Fatalf("table_info %s: %v", tc.table, err)
		}
		found, bad := false, false
		for rows.Next() {
			var (
				cid, notnull, pk int
				name, typ        string
				dflt             sql.NullString
			)
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan table_info %s: %v", tc.table, err)
			}
			if name == tc.col {
				found = true
				if notnull != 0 || dflt.Valid {
					bad = true
				}
			}
		}
		rows.Close()
		if !found {
			t.Errorf("%s.%s column missing", tc.table, tc.col)
		} else if bad {
			t.Errorf("%s.%s has NOT NULL or a default — must be plain nullable", tc.table, tc.col)
		}
	}

	// Legacy tables keep their columns (nothing destructive).
	assertContains(t, taskCols, "claimed_by_name", "task columns")
	assertContains(t, taskCols, "assignee_name", "task columns")
	var statusCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM status`).Scan(&statusCount); err != nil || statusCount != 3 {
		t.Errorf("status seed rows = %d (err=%v), want 3 — legacy data must be intact", statusCount, err)
	}

	// Indexes.
	for _, idx := range []string{"idx_session_identity", "idx_api_token_identity"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing (count=%d, err=%v)", idx, n, err)
		}
	}

	// Exactly one recorded application of the migration.
	var rec int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, migration0011).Scan(&rec); err != nil || rec != 1 {
		t.Errorf("schema_migrations rows for %s = %d (err=%v), want 1", migration0011, rec, err)
	}

	// A fresh (actor-less) DB gets zero identities.
	if n := tableCount(t, db, "identity"); n != 0 {
		t.Errorf("identity rows on empty DB = %d, want 0", n)
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func assertEqualStrings(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q (full: %v)", what, i, got[i], want[i], got)
		}
	}
}

func assertContains(t *testing.T, hay []string, needle, what string) {
	t.Helper()
	for _, s := range hay {
		if s == needle {
			return
		}
	}
	t.Errorf("%s = %v, want it to contain %q", what, hay, needle)
}

// --- AC2: backfill — one identity per distinct actor name ------------------------

func TestMigration0011_BackfillsDistinctActorNames(t *testing.T) {
	t.Parallel()
	db, _ := openLegacyDB(t, lastLegacyFile)
	seedLegacyWorld(t, db)

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	got := identitiesByName(t, db)
	if len(got) != 4 {
		t.Fatalf("identity rows = %d (%v), want 4 distinct actors", len(got), namesOf(got))
	}

	cases := []struct {
		name       string
		wantKind   string // inferred from the *_type values stored next to it
		wantActive sql.NullString
	}{
		{"hollen", "human", nullTS("2026-01-05T11:30:00Z")},        // comments + AC check + project event, all human
		{"devin", "agent", nullTS("2026-01-05T11:30:00Z")},         // comment + artifact + claim event, all agent
		{"mixed-bob", "human", nullTS("2026-01-05T11:30:00Z")},     // human AND agent → default 'human'
		{"unicode-æøå-🚀", "agent", nullTS("2026-01-05T11:30:00Z")}, // assignee + comment + AC, all agent
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := got[tc.name]
			if !ok {
				t.Fatalf("identity %q missing from backfill", tc.name)
			}
			if r.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", r.Kind, tc.wantKind)
			}
			if r.AuthMethod != "shared_token" {
				t.Errorf("auth_method = %q, want shared_token", r.AuthMethod)
			}
			if r.CredHash.Valid {
				t.Errorf("credential_hash = %q, want NULL for backfilled identities", r.CredHash.String)
			}
			if r.DisabledAt.Valid {
				t.Errorf("disabled_at = %q, want NULL", r.DisabledAt.String)
			}
			if !r.CreatedAt.Valid || r.CreatedAt.String == "" {
				t.Errorf("created_at not set")
			}
			if r.LastActive.Valid != tc.wantActive.Valid {
				t.Errorf("last_active_at valid=%v (%q), want valid=%v", r.LastActive.Valid, r.LastActive.String, tc.wantActive.Valid)
			} else if tc.wantActive.Valid && r.LastActive.String != tc.wantActive.String {
				t.Errorf("last_active_at = %q, want %q", r.LastActive.String, tc.wantActive.String)
			}
			if len(r.ID) != 26 || !isCrockfordBase32(t, r.ID) {
				t.Errorf("id %q is not a 26-char Crockford-base32 string (ULID shape)", r.ID)
			}
		})
	}

	// Ownership columns stay NULL on all legacy rows.
	for _, tc := range []struct{ table, col string }{
		{"project", "created_by"},
		{"task", "created_by"},
	} {
		var n int
		if err := db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+tc.table+` WHERE `+tc.col+` IS NOT NULL`).Scan(&n); err != nil {
			t.Fatalf("check %s.%s: %v", tc.table, tc.col, err)
		}
		if n != 0 {
			t.Errorf("%d %s rows have %s set; backfill must leave it NULL", n, tc.table, tc.col)
		}
	}
}

func namesOf(m map[string]identityRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func nullTS(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func isCrockfordBase32(t *testing.T, s string) bool {
	t.Helper()
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i := 0; i < len(s); i++ {
		found := false
		for j := 0; j < len(alphabet); j++ {
			if s[i] == alphabet[j] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// --- AC2 (idempotency): re-running the migration is safe -------------------------

func TestMigration0011_ReRunIsIdempotent(t *testing.T) {
	t.Parallel()
	db, path := openLegacyDB(t, lastLegacyFile)
	seedLegacyWorld(t, db)

	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	first := identitiesByName(t, db)
	if len(first) != 4 {
		t.Fatalf("identity rows after first migrate = %d, want 4", len(first))
	}

	// Second pass on the same handle: full no-op, no error.
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	second := identitiesByName(t, db)
	assertIdentitySetsEqual(t, second, first)

	// A second PROCESS booting against the same file (the runner's concurrency
	// contract): still exactly one identity per actor.
	other, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer other.Close()
	if err := Migrate(ctx, other); err != nil {
		t.Fatalf("Migrate from second process: %v", err)
	}
	third := identitiesByName(t, db)
	assertIdentitySetsEqual(t, third, first)

	var rec int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, migration0011).Scan(&rec); err != nil {
		t.Fatalf("count recorded migrations: %v", err)
	}
	if rec != 1 {
		t.Errorf("schema_migrations rows for %s = %d, want exactly 1", migration0011, rec)
	}

	var dups int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT name FROM identity GROUP BY name HAVING COUNT(*) > 1)`).Scan(&dups); err != nil {
		t.Fatalf("check duplicate names: %v", err)
	}
	if dups != 0 {
		t.Errorf("%d duplicated identity names after re-runs", dups)
	}
}

func assertIdentitySetsEqual(t *testing.T, got, want map[string]identityRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("identity count = %d (%v), want %d (%v)", len(got), namesOf(got), len(want), namesOf(want))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("identity %q missing after re-run", name)
			continue
		}
		if g.Kind != w.Kind || g.AuthMethod != w.AuthMethod || g.ID != w.ID {
			t.Errorf("identity %q changed across re-runs: kind=%q/%q auth=%q/%q id=%q/%q",
				name, g.Kind, w.Kind, g.AuthMethod, w.AuthMethod, g.ID, w.ID)
		}
	}
}

// --- AC3 (data side): upgrading a populated single-tenant DB loses nothing -------

func TestMigration0011_LegacySingleTenantUpgradeIntact(t *testing.T) {
	t.Parallel()
	db, path := openLegacyDB(t, lastLegacyFile)
	seedLegacyWorld(t, db)

	ctx := context.Background()
	tables := []string{"project", "task", "comment", "artifact", "acceptance_criterion", "relation", "event"}
	before := map[string]int{}
	for _, tbl := range tables {
		before[tbl] = tableCount(t, db, tbl)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, tbl := range tables {
		if after := tableCount(t, db, tbl); after != before[tbl] {
			t.Errorf("%s rows = %d after migrate, want %d (all prior data intact)", tbl, after, before[tbl])
		}
	}

	// Spot-check exact field values on the trickiest legacy row (claim + assignee).
	var (
		title, status, notes  string
		priority              int
		version               int
		ttl                   sql.NullInt64
		assigneeName, claimer string
		dueAt                 sql.NullString
	)
	err := db.QueryRowContext(ctx, `SELECT title, status, priority, due_at, notes, assignee_name, claimed_by_name, claim_ttl_sec, version
		FROM task WHERE id = 't-leg-1'`).
		Scan(&title, &status, &priority, &dueAt, &notes, &assigneeName, &claimer, &ttl, &version)
	if err != nil {
		t.Fatalf("read t-leg-1: %v", err)
	}
	if title != "Legacy task one" || status != "doing" || priority != 3 || notes != "notes here" ||
		assigneeName != "hollen" || claimer != "devin" || version != 4 || !dueAt.Valid {
		t.Errorf("t-leg-1 drifted after migrate: %+v", struct {
			T, S, N string
			P       int
		}{title, status, notes, priority})
	}
	if ttl.Int64 != 900 || !ttl.Valid {
		t.Errorf("claim_ttl_sec = %v, want 900", ttl)
	}

	var body, actor string
	if err := db.QueryRowContext(ctx, `SELECT body, actor_name FROM comment WHERE id = 'c-leg-2'`).Scan(&body, &actor); err != nil {
		t.Fatalf("read c-leg-2: %v", err)
	}
	if body != "needs input" || actor != "devin" {
		t.Errorf("comment drifted: %q by %q", body, actor)
	}

	var done int
	if err := db.QueryRowContext(ctx, `SELECT done FROM acceptance_criterion WHERE id = 'ac-leg-1'`).Scan(&done); err != nil || done != 1 {
		t.Errorf("AC check state drifted (done=%d, err=%v)", done, err)
	}

	var kind string
	if err := db.QueryRowContext(ctx, `SELECT kind FROM relation WHERE id = 'r-leg-1'`).Scan(&kind); err != nil || kind != "related" {
		t.Errorf("relation drifted (kind=%q, err=%v)", kind, err)
	}

	// Production boot path: OpenPool re-runs Migrate on the migrated file and
	// everything must still read back intact.
	pool, closePool, err := OpenPool(ctx, path)
	if err != nil {
		t.Fatalf("re-open migrated DB via OpenPool: %v", err)
	}
	t.Cleanup(func() { _ = closePool() })

	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Errorf("integrity_check = %q (err=%v), want ok", integrity, err)
	}
	for _, tbl := range tables {
		if after := tableCount(t, pool.Writer, tbl); after != before[tbl] {
			t.Errorf("%s rows via re-opened pool = %d, want %d", tbl, after, before[tbl])
		}
	}
	if n := tableCount(t, pool.Writer, "identity"); n != 4 {
		t.Errorf("identity rows via re-opened pool = %d, want 4", n)
	}

	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var rec int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rec); err != nil || rec != len(files) {
		t.Errorf("schema_migrations rows = %d (err=%v), want %d (every file recorded once)", rec, err, len(files))
	}
}
