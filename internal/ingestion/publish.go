package ingestion

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/ofac"
)

// publishStats accumulates execrow counters across the publish steps.
type publishStats struct {
	upserted int64
	revoked  int64
}

// publish runs the staged→live transition in ONE transaction on the pinned
// conn: upserts, entity/program/reference swaps, source-scoped revocation
// of absent records, and the snapshot registry row. Any error rolls the
// whole thing back — the live snapshot is never half-published.
func (s *Service) publish(ctx context.Context, conn *pgxpool.Conn, sourceID int64,
	runID uuid.UUID, checksum string, meta *ofac.DocMeta, res provider.ImportResult,
) (publishStats, string, error) {

	tx, err := conn.Begin(ctx)
	if err != nil {
		return publishStats{}, "", fmt.Errorf("begin publish tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Bound the tx: no waiting on locks, no unbounded statements.
	for _, stmt := range []string{
		"SET LOCAL lock_timeout = '10s'",
		"SET LOCAL statement_timeout = '120s'",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return publishStats{}, "", fmt.Errorf("tx tuning: %w", err)
		}
	}

	qtx := db.New(tx)
	u := pgtype.UUID{Bytes: runID, Valid: true}
	src := db.PublishUpsertEntitiesParams{SourceID: sourceID, RunID: u}
	stats := publishStats{}

	steps := []struct {
		name string
		run  func() (int64, error)
		out  *int64
	}{
		{"entities", func() (int64, error) {
			return qtx.PublishUpsertEntities(ctx, src)
		}, &stats.upserted},
		{"programs", func() (int64, error) {
			return qtx.PublishUpsertPrograms(ctx, db.PublishUpsertProgramsParams(src))
		}, &stats.upserted},
		{"entity_programs_clear", func() (int64, error) {
			err := qtx.PublishDeleteEntityProgramsForSource(ctx, sourceID)
			return 0, err
		}, nil},
		{"entity_programs", func() (int64, error) {
			return qtx.PublishInsertEntityPrograms(ctx, db.PublishInsertEntityProgramsParams(src))
		}, &stats.upserted},
		{"wallets_resolve", func() (int64, error) {
			return qtx.PublishEnsureWallets(ctx, u)
		}, &stats.upserted},
		{"sanctions_wallets", func() (int64, error) {
			return qtx.PublishUpsertSanctionsWallets(ctx, db.PublishUpsertSanctionsWalletsParams(src))
		}, &stats.upserted},
		{"wallet_revocations", func() (int64, error) {
			return qtx.PublishRevokeAbsentWallets(ctx, db.PublishRevokeAbsentWalletsParams(src))
		}, &stats.revoked},
		{"entity_revocations", func() (int64, error) {
			return qtx.PublishRevokeAbsentEntities(ctx, db.PublishRevokeAbsentEntitiesParams(src))
		}, &stats.revoked},
		{"references_clear", func() (int64, error) {
			err := qtx.PublishDeleteReferencesForSource(ctx, sourceID)
			return 0, err
		}, nil},
		{"references", func() (int64, error) {
			return qtx.PublishInsertReferences(ctx, db.PublishInsertReferencesParams(src))
		}, &stats.upserted},
	}
	for _, st := range steps {
		rows, err := st.run()
		if err != nil {
			return publishStats{}, "", fmt.Errorf("publish step %s: %w", st.name, err)
		}
		if st.out != nil {
			*st.out += rows
		}
	}

	version := fmt.Sprintf("ofac-%s-%d", checksum[:12], s.now().Unix())
	var doi pgtype.Date
	if meta.DateOfIssue != nil {
		doi = pgtype.Date{Time: meta.DateOfIssue.UTC(), Valid: true}
	}
	if _, err := qtx.InsertSnapshot(ctx, db.InsertSnapshotParams{
		SourceID:     sourceID,
		Version:      version,
		Checksum:     checksum,
		EntityCount:  res.Entities,
		WalletCount:  res.Wallets,
		ProgramCount: res.Programs,
		DateOfIssue:  doi,
	}); err != nil {
		return publishStats{}, "", fmt.Errorf("snapshot row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return publishStats{}, "", fmt.Errorf("commit: %w", err)
	}
	return stats, version, nil
}
