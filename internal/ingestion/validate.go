package ingestion

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider/ofac"
)

// Sanity band: a new snapshot must not shrink below 50% or grow above
// 300% of the previously published entity count — catches truncated
// downloads and parser duplication bugs before they can revoke live data.
const (
	entityShrinkFloor = 0.5
	entityGrowCeil    = 3.0
)

// validateStaged gates publish: staged row counts, referential integrity,
// and regression checks against the currently published snapshot. Any
// failure aborts the run with staging preserved for diagnosis.
func validateStaged(ctx context.Context, qc *db.Queries, runID uuid.UUID,
	meta *ofac.DocMeta, prev *db.SanctionsSnapshot) error {

	u := pgtype.UUID{Bytes: runID, Valid: true}
	counts, err := qc.StgCountByRun(ctx, u)
	if err != nil {
		return fmt.Errorf("staging counts: %w", err)
	}
	n := map[string]int64{}
	for _, row := range counts {
		n[row.TableName] = row.N
	}
	if n["entities"] == 0 || n["wallets"] == 0 {
		return fmt.Errorf("staging empty (entities=%d wallets=%d) — refusing to publish",
			n["entities"], n["wallets"])
	}

	for name, fn := range map[string]func(context.Context, pgtype.UUID) (int64, error){
		"wallets":         qc.StgOrphanWallets,
		"entity_programs": qc.StgOrphanEntityPrograms,
		"references":      qc.StgOrphanReferences,
	} {
		orphans, err := fn(ctx, u)
		if err != nil {
			return fmt.Errorf("orphan check %s: %w", name, err)
		}
		if orphans > 0 {
			return fmt.Errorf("%d orphan %s rows reference missing entities", orphans, name)
		}
	}

	if prev != nil {
		// DateOfIssue regression: refuse to republish an older document.
		if meta.DateOfIssue != nil && prev.DateOfIssue.Valid {
			if meta.DateOfIssue.Before(prev.DateOfIssue.Time) {
				return fmt.Errorf("DateOfIssue regression: new %s < published %s",
					meta.DateOfIssue.Format("2006-01-02"),
					prev.DateOfIssue.Time.Format("2006-01-02"))
			}
		}
		// Entity-count sanity band.
		if prev.EntityCount > 0 {
			ratio := float64(n["entities"]) / float64(prev.EntityCount)
			if ratio < entityShrinkFloor {
				return fmt.Errorf("entity count %d below %.0f%% of previous %d",
					n["entities"], entityShrinkFloor*100, prev.EntityCount)
			}
			if ratio > entityGrowCeil {
				return fmt.Errorf("entity count %d above %.0f%% of previous %d",
					n["entities"], entityGrowCeil*100, prev.EntityCount)
			}
		}
	}
	return nil
}
