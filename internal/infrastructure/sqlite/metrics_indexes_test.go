package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// The query texts below must stay in sync with CollectTaskMetrics in
// metrics.go — they are EXPLAINed verbatim, so any drift is what we want to
// catch.
const (
	statusQuery = `SELECT status, COUNT(*) FROM task WHERE archived = 0 GROUP BY status`
	claimsQuery = `SELECT COUNT(*) FROM task WHERE claimed_by_name IS NOT NULL AND (claim_expires_at IS NULL OR claim_expires_at >= ?)`
)

func TestTaskMetricsIndexesExist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	indexes := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'task'`)
	if err != nil {
		t.Fatalf("query task indexes: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		indexes[name] = true
	}
	rows.Close()
	for _, wantIndex := range []string{
		"idx_task_metrics_status", // 0010: status aggregation over non-archived tasks
		"idx_task_active_claims",  // 0010: active claim counts by expiry
	} {
		if !indexes[wantIndex] {
			t.Errorf("index %q missing from task schema", wantIndex)
		}
	}
}

// explainQueryPlan returns the "detail" column of every EXPLAIN QUERY PLAN row.
// Bind args are passed through (SQLite plans without executing them).
func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan %q: %v", query, err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan explain row for %q: %v", query, err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain rows for %q: %v", query, err)
	}
	return details
}

// TestCollectTaskMetricsQueriesUseIndexes pins the query plans of both /metrics
// scrape queries: after migration 0010 they must use their partial indexes and
// never fall back to a plain full scan of task.
func TestCollectTaskMetricsQueriesUseIndexes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestDB(t)

	// Seed a little data so the planner runs over a realistic table shape —
	// same mix as TestCollectTaskMetrics (statuses, claims with/without expiry,
	// one archived task).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO project (id, name, description, created_at, updated_at) VALUES ('p1', 'P', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	for _, spec := range []struct {
		id       string
		status   string
		name     any
		expiry   any
		archived int
	}{
		{"t1", "todo", nil, nil, 0},
		{"t2", "doing", "agent-1", "2026-09-27T13:00:00Z", 0},
		{"t3", "doing", "agent-1", "2026-01-01T00:00:00Z", 0},
		{"t4", "done", nil, nil, 0},
		{"t5", "todo", nil, nil, 1},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO task (id, project_id, status, title, description, priority, claimed_by_type, claimed_by_name, claim_expires_at, archived, created_at, updated_at)
			 VALUES (?, 'p1', ?, 'Title', '', 1, CASE WHEN ? IS NOT NULL THEN 'agent' END, ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			spec.id, spec.status, spec.name, spec.name, spec.expiry, spec.archived); err != nil {
			t.Fatalf("insert task %s: %v", spec.id, err)
		}
	}

	cases := []struct {
		name  string
		query string
		args  []any
		// wantIndex is the dedicated 0010 partial index this query must use.
		wantIndex string
	}{
		{"status aggregation", statusQuery, nil, "idx_task_metrics_status"},
		{"active claim count", claimsQuery, []any{"2026-09-27T12:00:00Z"}, "idx_task_active_claims"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := explainQueryPlan(t, db, tc.query, tc.args...)
			if len(details) == 0 {
				t.Fatalf("no query plan rows for %q", tc.query)
			}

			var sawIndex bool
			for _, detail := range details {
				upper := strings.ToUpper(detail)
				// A plain full scan of the task table is exactly "SCAN task";
				// any index use appends "USING [COVERING] INDEX ...".
				if upper == "SCAN TASK" || (strings.HasPrefix(upper, "SCAN TASK ") && !strings.Contains(upper, "INDEX")) {
					t.Errorf("plan uses a full table scan of task:\n  %s\nfull plan:\n%s", detail, strings.Join(details, "\n"))
					return
				}
				if strings.Contains(upper, strings.ToUpper(tc.wantIndex)) {
					sawIndex = true
				}
			}
			if !sawIndex {
				t.Errorf("plan does not use %q:\n%s", tc.wantIndex, strings.Join(details, "\n"))
			}
		})
	}
}
