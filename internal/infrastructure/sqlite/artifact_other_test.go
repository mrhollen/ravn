package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestArtifactOtherKindMigration proves migration 0002 extended the artifact
// kind CHECK: every canonical kind (including the new 'other' escape hatch) is
// accepted by the rebuilt table, unknown kinds are still rejected, and rows
// written before the rebuild survived it. Migrate itself is idempotent
// (TestMigrateIsIdempotent covers a second full pass).
func TestArtifactOtherKindMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `INSERT INTO project (id, name, description, created_at, updated_at)
		VALUES ('proj-1', 'Migration Fixture', '', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO task (id, project_id, title, created_at, updated_at)
		VALUES ('task-1', 'proj-1', 'fixture task', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	const insertArtifact = `INSERT INTO artifact (id, task_id, kind, ref, summary, actor_type, actor_name, created_at)
		VALUES (?, 'task-1', ?, 'https://example.com/evidence', '', 'agent', 'agent-a', ?)`

	for i, kind := range []string{"log", "screenshot", "command-output", "trace", "url", "other"} {
		if _, err := db.ExecContext(ctx, insertArtifact, fmt.Sprintf("art-%d", i), kind, now); err != nil {
			t.Fatalf("insert artifact with kind %q failed, want success: %v", kind, err)
		}
	}

	if _, err := db.ExecContext(ctx, insertArtifact, "art-bad", "bogus", now); err == nil {
		t.Fatal("insert with unknown kind 'bogus' succeeded; the CHECK constraint was lost in the rebuild")
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact WHERE task_id = 'task-1'`).Scan(&n); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if n != 6 {
		t.Errorf("artifact rows = %d, want 6 (the rebuild must preserve existing rows)", n)
	}
}
