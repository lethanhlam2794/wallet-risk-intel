package ingestion

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tryAdvisoryLock attempts a session-level advisory lock keyed by source_id
// on the pinned connection. The lock lives for the life of the connection,
// so it must be released (or the conn closed) when the run ends. Returns
// false when another session already holds it.
func tryAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, key int64) (bool, error) {
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		return false, fmt.Errorf("advisory lock: %w", err)
	}
	return ok, nil
}

func advisoryUnlock(ctx context.Context, conn *pgxpool.Conn, key int64) {
	// Best-effort: if the conn died the lock is gone anyway.
	var ok bool
	_ = conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", key).Scan(&ok)
}
