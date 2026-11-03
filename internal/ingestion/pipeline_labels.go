package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/walletattribution"
)

// labelNetworkFloor is the per-network sanity band: a republish must stage
// at least this fraction of the currently-live label count for each staged
// network (skipped on first publish — no live rows to compare against).
const labelNetworkFloor = 0.2

// labelsPipeline stages the wallet-attribution CSVs and publishes labels in
// one transaction. Provider specifics (CSV shape, fingerprint formula,
// evidence shape) live inside internal/provider/walletattribution.
type labelsPipeline struct {
	networks map[string]int64
	now      func() time.Time
	log      *slog.Logger
	metrics  *metrics.Metrics
}

func newLabelsPipeline(networks map[string]int64, now func() time.Time, log *slog.Logger) *labelsPipeline {
	return &labelsPipeline{networks: networks, now: now, log: log}
}

// SetMetrics propagates the collector (SetMetrics lands after NewService).
func (p *labelsPipeline) SetMetrics(m *metrics.Metrics) { p.metrics = m }

// labelsRunMeta carries the importer's per-file stats plus the artifact
// checksum for the publish version string.
type labelsRunMeta struct {
	stats    *walletattribution.ImportMeta
	checksum string
	labels   int64 // staged label count → snapshot row_count
}

func (p *labelsPipeline) Import(ctx context.Context, qc *db.Queries,
	artifact provider.Snapshot, runID pgtype.UUID) (provider.ImportResult, Meta, error) {
	if len(artifact.Files) == 0 {
		return provider.ImportResult{}, nil, errors.New("wallet-attribution artifact has no files")
	}
	imp := walletattribution.NewImporter(p.networks)
	res, meta, err := imp.Import(ctx, qc, artifact, runID)
	return res, &labelsRunMeta{stats: meta, checksum: artifact.Checksum, labels: res.Labels}, err
}

// Validate gates publish: each provided file must have produced staged rows;
// parsed-vs-staged reconciliation must hold exactly (a fingerprint
// collision is silent data loss → hard-fail); staged networks must exist in
// the registry; and each staged network must clear the sanity floor against
// its live count. The last_updated max regression is warn-only — upstream's
// stamp is a uniform rebuild marker, not a per-record observation.
func (p *labelsPipeline) Validate(ctx context.Context, qc *db.Queries,
	runID pgtype.UUID, meta Meta) error {
	rm, ok := meta.(*labelsRunMeta)
	if !ok {
		return fmt.Errorf("labels validate: unexpected meta type %T", meta)
	}
	for _, fs := range rm.stats.Files {
		if fs.Staged == 0 {
			return fmt.Errorf("file %q produced 0 staged rows", fs.Name)
		}
		if coll := fs.Attempted - fs.Staged; coll != 0 {
			return fmt.Errorf("file %q: %d fingerprint collisions (attempted %d, staged %d)",
				fs.Name, coll, fs.Attempted, fs.Staged)
		}
		if fs.Parsed != fs.Attempted+fs.Rejected {
			return fmt.Errorf("file %q: reconciliation failed (parsed %d != attempted %d + rejected %d)",
				fs.Name, fs.Parsed, fs.Attempted, fs.Rejected)
		}
	}

	run, err := qc.GetIngestionRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("labels validate: run row: %w", err)
	}
	if orphans, err := qc.StgLabelOrphanNetworks(ctx, runID); err != nil {
		return fmt.Errorf("labels validate: orphan networks: %w", err)
	} else if orphans > 0 {
		return fmt.Errorf("%d staged rows reference unknown network codes", orphans)
	}

	live, err := qc.CountLiveLabelsForStagedNetworks(ctx,
		db.CountLiveLabelsForStagedNetworksParams{SourceID: run.SourceID, RunID: runID})
	if err != nil {
		return fmt.Errorf("labels validate: live counts: %w", err)
	}
	staged, err := qc.StgLabelCountsByNetwork(ctx, runID)
	if err != nil {
		return fmt.Errorf("labels validate: staged counts: %w", err)
	}
	liveByNet := map[string]int64{}
	for _, r := range live {
		liveByNet[r.NetworkCode] = r.N
	}
	for _, r := range staged {
		prev := liveByNet[r.NetworkCode]
		if prev > 0 && float64(r.N) < labelNetworkFloor*float64(prev) {
			return fmt.Errorf("network %s staged %d below %.0f%% of live %d",
				r.NetworkCode, r.N, labelNetworkFloor*100, prev)
		}
	}

	if mx, err := qc.StgLabelMaxLastUpdated(ctx, runID); err == nil && mx != "" {
		if prevMx, err := qc.MaxLabelObservedAt(ctx, run.SourceID); err == nil && prevMx.Valid {
			if t, perr := time.Parse("2006-01-02", mx); perr == nil && t.Before(prevMx.Time) {
				p.log.Warn("last_updated max regression (uniform rebuild stamp — continuing)",
					"run_id", runID, "staged_max", mx, "live_max", prevMx.Time.Format("2006-01-02"))
			}
		}
	}
	return nil
}

// Publish: staged → live in the service-managed transaction. Order matters:
// wallets → categories → entities → labels upsert → network-scoped
// soft-revoke → snapshot row.
func (p *labelsPipeline) Publish(ctx context.Context, qtx *db.Queries,
	runID pgtype.UUID, sourceID int64, meta Meta) (string, publishStats, error) {
	rm, ok := meta.(*labelsRunMeta)
	if !ok {
		return "", publishStats{}, fmt.Errorf("labels publish: unexpected meta type %T", meta)
	}
	stats := publishStats{}

	if _, err := qtx.PublishEnsureLabelWallets(ctx, runID); err != nil {
		return "", stats, fmt.Errorf("publish step wallets: %w", err)
	}

	newCats, err := qtx.StgLabelNewCategories(ctx, runID)
	if err != nil {
		return "", stats, fmt.Errorf("publish step new categories: %w", err)
	}
	for _, code := range newCats {
		p.log.Warn("dynamic risk category created", "code", code, "run_id", runID)
	}
	if p.metrics != nil && len(newCats) > 0 {
		p.metrics.IncDynamicLabelCategories(int64(len(newCats)))
	}
	if _, err := qtx.PublishEnsureRiskCategories(ctx, runID); err != nil {
		return "", stats, fmt.Errorf("publish step categories: %w", err)
	}
	if _, err := qtx.PublishEnsureLabelEntities(ctx, runID); err != nil {
		return "", stats, fmt.Errorf("publish step entities: %w", err)
	}
	n, err := qtx.PublishUpsertWalletLabels(ctx,
		db.PublishUpsertWalletLabelsParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("publish step labels: %w", err)
	}
	stats.upserted += n

	n, err = qtx.PublishRevokeAbsentWalletLabels(ctx,
		db.PublishRevokeAbsentWalletLabelsParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("publish step label_revocations: %w", err)
	}
	stats.revoked += n

	version := fmt.Sprintf("wattr-%s-%d", rm.checksum[:12], p.now().Unix())
	if _, err := qtx.InsertDataSourceSnapshot(ctx, db.InsertDataSourceSnapshotParams{
		SourceID: sourceID,
		Version:  version,
		Checksum: rm.checksum,
		RowCount: rm.labels,
	}); err != nil {
		return "", stats, fmt.Errorf("snapshot row: %w", err)
	}
	return version, stats, nil
}

func (p *labelsPipeline) LatestVersion(ctx context.Context, qc *db.Queries,
	sourceID int64) (string, string, error) {
	row, err := qc.GetLatestDataSourceSnapshot(ctx, sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return row.Version, row.Checksum, nil
}

func (p *labelsPipeline) StagingNames() []string {
	return []string{"wallet_labels"}
}
