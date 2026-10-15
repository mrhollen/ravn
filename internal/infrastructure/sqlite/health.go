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
	if db == nil {
		return "error: database handle is nil"
	}
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// HealthPool checks database liveness using the pool's reader connection, ensuring
// health checks are never serialized behind active write transactions.
func HealthPool(ctx context.Context, pool *DBPool) string {
	if pool == nil {
		return "error: database pool is nil"
	}
	if pool.Reader != nil {
		return Health(ctx, pool.Reader)
	}
	return Health(ctx, pool.Writer)
}
