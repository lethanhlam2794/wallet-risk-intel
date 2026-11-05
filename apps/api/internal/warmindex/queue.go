package warmindex

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/wallet"
)

// Queue is the producer side of the warm index queue — the tracing
// hook's Enqueuer implementation. When the feature flag is off it is a
// silent no-op, so the interactive path stays byte-identical.
type Queue struct {
	q       *db.Queries
	enabled bool
	log     *slog.Logger
}

// NewQueue builds the producer; enabled comes from WARM_INDEX_ENABLED.
func NewQueue(q *db.Queries, enabled bool, log *slog.Logger) *Queue {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Queue{q: q, enabled: enabled, log: log}
}

// Enqueue upserts one signal-driven deep-index request (D4): a second
// signal on the same active row only raises its priority — never a
// duplicate, never a demotion. done/dead rows are history and do not
// block re-enqueue. Disabled → nil no-op.
//
// Callers log returned errors (fail-open contract — Enqueue never
// panics); a non-nil return means nothing was queued.
func (q *Queue) Enqueue(ctx context.Context, netID int64, addr string,
	priority int, reason string, screeningID *uuid.UUID) error {

	if q == nil || !q.enabled {
		return nil
	}
	// Only canonical normalized form enters the queue (D8) — anything
	// NormalizeEVM would change is a caller bug, not a stored row.
	if n, err := wallet.NormalizeEVM(addr); err != nil || n != addr {
		return fmt.Errorf("warmindex: %s is not normalized EVM form",
			MaskAddr(addr))
	}
	var sid pgtype.UUID
	if screeningID != nil {
		sid = pgtype.UUID{Bytes: *screeningID, Valid: true}
	}
	if _, err := q.q.EnqueueWarmItem(ctx, db.EnqueueWarmItemParams{
		NetworkID: netID, Address: addr,
		Priority: int32(priority), Reason: reason,
		SourceScreeningID: sid,
	}); err != nil {
		return fmt.Errorf("warmindex: enqueue %s: %w", MaskAddr(addr), err)
	}
	return nil
}
