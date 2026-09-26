package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Health checks database liveness with SELECT 1 and returns the value for the
// /healthz "db" field: exactly "ok" on success, or "error: <reason>" when the
// database is unreachable (ARCHITECTURE §8). Callers treat any non-"ok" value
// as degraded.
func Health(ctx context.Context, db *sql.DB) string {
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}
