package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CollectTaskMetrics returns task counts partitioned by status (excluding archived tasks)
// and the count of active claims (where claimed_by_name is non-null and claim has not expired).
func CollectTaskMetrics(ctx context.Context, db *sql.DB, now time.Time) (map[string]int64, int64, error) {
	byStatus := make(map[string]int64)
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM task WHERE archived = 0 GROUP BY status`)
	if err != nil {
		return nil, 0, fmt.Errorf("count tasks by status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, 0, fmt.Errorf("scan task status count: %w", err)
		}
		byStatus[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows error: %w", err)
	}

	var activeClaims int64
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task WHERE claimed_by_name IS NOT NULL AND (claim_expires_at IS NULL OR claim_expires_at >= ?)`,
		formatTS(now),
	).Scan(&activeClaims)
	if err != nil {
		return nil, 0, fmt.Errorf("count active claims: %w", err)
	}

	return byStatus, activeClaims, nil
}
