package ingestion

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
)

// publishStats accumulates execrow counters across the publish steps.
type publishStats struct {
	upserted int64
	revoked  int64
}

// Meta is a pipeline's per-run parse artifact (e.g. *ofac.DocMeta,
// *labelsRunMeta). The service carries it between Import/Validate/Publish
// untouched — only the owning pipeline interprets it.
type Meta any

// Pipeline is the per-source stage→validate→publish contract (F1: the
// ingestion orchestrator dispatches through this interface instead of
// source-keyed if-chains).
//
// Import streams the artifact into run-scoped staging and returns counters
// plus the pipeline's Meta. Validate gates publish on staged data. Publish
// runs inside a service-managed transaction (qtx is already bound to it)
// and returns the published version. LatestVersion reports the last
// published (version, checksum) for the checksum-identical no-op check —
// ("","",nil) when nothing has been published. StagingNames enumerates the
// pipeline's staging query set for purge/delete housekeeping.
type Pipeline interface {
	Import(ctx context.Context, qc *db.Queries, artifact provider.Snapshot,
		runID pgtype.UUID) (provider.ImportResult, Meta, error)
	Validate(ctx context.Context, qc *db.Queries, runID pgtype.UUID, meta Meta) error
	Publish(ctx context.Context, qtx *db.Queries, runID pgtype.UUID,
		sourceID int64, meta Meta) (version string, stats publishStats, err error)
	LatestVersion(ctx context.Context, qc *db.Queries, sourceID int64) (version, checksum string, err error)
	StagingNames() []string
}

// stagingPurge maps a logical staging name to its "purge everything except
// the kept run ids" query. Housekeeping is global — an OFAC run purges the
// labels staging table too — so cross-source dead-run rows cannot leak.
var stagingPurge = map[string]func(context.Context, *db.Queries, []pgtype.UUID) error{
	"sanctions_entities": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeEntitiesExceptRuns(ctx, k)
	},
	"sanctions_wallets": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeWalletsExceptRuns(ctx, k)
	},
	"sanctions_programs": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeProgramsExceptRuns(ctx, k)
	},
	"sanctions_entity_programs": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeEntityProgramsExceptRuns(ctx, k)
	},
	"sanctions_references": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeReferencesExceptRuns(ctx, k)
	},
	"wallet_labels": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeWalletLabelsExceptRuns(ctx, k)
	},
	"freeze_events": func(ctx context.Context, q *db.Queries, k []pgtype.UUID) error {
		return q.StgPurgeFreezeEventsExceptRuns(ctx, k)
	},
}

// stagingDelete maps a logical staging name to its "delete this run's
// rows" query. Runs delete only their OWN staging after a successful
// publish.
var stagingDelete = map[string]func(context.Context, *db.Queries, pgtype.UUID) error{
	"sanctions_entities": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error { return q.StgDeleteRunEntities(ctx, u) },
	"sanctions_wallets":  func(ctx context.Context, q *db.Queries, u pgtype.UUID) error { return q.StgDeleteRunWallets(ctx, u) },
	"sanctions_programs": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error { return q.StgDeleteRunPrograms(ctx, u) },
	"sanctions_entity_programs": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error {
		return q.StgDeleteRunEntityPrograms(ctx, u)
	},
	"sanctions_references": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error { return q.StgDeleteRunReferences(ctx, u) },
	"wallet_labels": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error {
		return q.StgDeleteRunWalletLabels(ctx, u)
	},
	"freeze_events": func(ctx context.Context, q *db.Queries, u pgtype.UUID) error {
		return q.StgDeleteRunFreezeEvents(ctx, u)
	},
}
