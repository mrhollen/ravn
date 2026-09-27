package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mrhollen/crowban/internal/infrastructure/sqlite"
)

func TestCollectTaskMetrics(t *testing.T) {
	ctx := context.Background()
	db, closeDB, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "metrics-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer closeDB()

	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	nowStr := now.Format(time.RFC3339Nano)
	futureStr := now.Add(1 * time.Hour).Format(time.RFC3339Nano)
	pastStr := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)

	// 1. Initial state: 0 tasks, 0 active claims
	byStatus, activeClaims, err := sqlite.CollectTaskMetrics(ctx, db, now)
	if err != nil {
		t.Fatalf("collect task metrics on empty db: %v", err)
	}
	if len(byStatus) != 0 {
		t.Errorf("expected 0 task statuses, got %v", byStatus)
	}
	if activeClaims != 0 {
		t.Errorf("expected 0 active claims, got %d", activeClaims)
	}

	// 2. Insert test project
	_, err = db.ExecContext(ctx,
		`INSERT INTO project (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"01M3J000000000000000000001", "Metrics Test Project", "desc", nowStr, nowStr,
	)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// 3. Insert test tasks:
	// Task 1: todo, no claim, not archived
	// Task 2: doing, active claim (future expiry), not archived
	// Task 3: doing, expired claim (past expiry), not archived
	// Task 4: done, no claim, not archived
	// Task 5: todo, archived (archived = 1, should be excluded from byStatus)
	tasks := []struct {
		id       string
		status   string
		claimed  bool
		expiry   *string
		archived int
	}{
		{"01M3J000000000000000000002", "todo", false, nil, 0},
		{"01M3J000000000000000000003", "doing", true, &futureStr, 0},
		{"01M3J000000000000000000004", "doing", true, &pastStr, 0},
		{"01M3J000000000000000000005", "done", false, nil, 0},
		{"01M3J000000000000000000006", "todo", false, nil, 1},
	}

	for _, task := range tasks {
		var claimedType, claimedName any
		if task.claimed {
			claimedType = "agent"
			claimedName = "agent-1"
		}
		_, err = db.ExecContext(ctx,
			`INSERT INTO task (id, project_id, status, title, description, priority, claimed_by_type, claimed_by_name, claim_expires_at, archived, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.id, "01M3J000000000000000000001", task.status, "Title", "", 1, claimedType, claimedName, task.expiry, task.archived, nowStr, nowStr,
		)
		if err != nil {
			t.Fatalf("insert task %s: %v", task.id, err)
		}
	}

	byStatus, activeClaims, err = sqlite.CollectTaskMetrics(ctx, db, now)
	if err != nil {
		t.Fatalf("collect task metrics: %v", err)
	}

	if byStatus["todo"] != 1 {
		t.Errorf("expected 1 todo (excluding archived), got %d", byStatus["todo"])
	}
	if byStatus["doing"] != 2 {
		t.Errorf("expected 2 doing, got %d", byStatus["doing"])
	}
	if byStatus["done"] != 1 {
		t.Errorf("expected 1 done, got %d", byStatus["done"])
	}
	// Only task 2 has an active (unexpired) claim at `now`
	if activeClaims != 1 {
		t.Errorf("expected 1 active claim, got %d", activeClaims)
	}
}
