package ingestion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/ofac"
)

// ofacPipeline wraps the pre-existing OFAC stage→validate→publish logic
// behind the Pipeline interface — byte-for-byte behavior preservation; the
// OFAC path must keep working unchanged.
type ofacPipeline struct {
	networks map[string]int64
	now      func() time.Time
}

func newOFACPipeline(networks map[string]int64, now func() time.Time) *ofacPipeline {
	return &ofacPipeline{networks: networks, now: now}
}

// ofacRunMeta carries the importer's DocMeta plus what Publish needs —
// the publish step reads meta.DateOfIssue, res counts, and the artifact
// checksum for the version string; none of it is re-derivable from staging.
type ofacRunMeta struct {
	doc      *ofac.DocMeta
	res      provider.ImportResult
	checksum string
}

func (p *ofacPipeline) Import(ctx context.Context, qc *db.Queries,
	artifact provider.Snapshot, runID pgtype.UUID) (provider.ImportResult, Meta, error) {
	if len(artifact.Files) != 1 {
		return provider.ImportResult{}, nil,
			fmt.Errorf("ofac artifact must be exactly one file, got %d", len(artifact.Files))
	}
	imp := ofac.NewImporter(p.networks)
	res, doc, err := imp.Import(ctx, qc, artifact.Files[0].Path, uuid.UUID(runID.Bytes))
	return res, &ofacRunMeta{doc: doc, res: res, checksum: artifact.Checksum}, err
}

func (p *ofacPipeline) Validate(ctx context.Context, qc *db.Queries,
	runID pgtype.UUID, meta Meta) error {
	rm, ok := meta.(*ofacRunMeta)
	if !ok {
		return fmt.Errorf("ofac validate: unexpected meta type %T", meta)
	}
	run, err := qc.GetIngestionRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("ofac validate: run row: %w", err)
	}
	var prev *db.SanctionsSnapshot
	row, err := qc.GetLatestSnapshot(ctx, run.SourceID)
	if err == nil {
		prev = &row
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ofac validate: latest snapshot: %w", err)
	}
	return validateStaged(ctx, qc, uuid.UUID(runID.Bytes), rm.doc, prev)
}

// Publish performs the staged→live transition steps inside the
// service-managed transaction — identical query order to the pre-refactor
// implementation.
func (p *ofacPipeline) Publish(ctx context.Context, qtx *db.Queries,
	runID pgtype.UUID, sourceID int64, meta Meta) (string, publishStats, error) {
	rm, ok := meta.(*ofacRunMeta)
	if !ok {
		return "", publishStats{}, fmt.Errorf("ofac publish: unexpected meta type %T", meta)
	}
	u := runID
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
		{"entity_programs", func() (int64, error) {
			return qtx.PublishUpsertEntityPrograms(ctx, db.PublishUpsertEntityProgramsParams(src))
		}, &stats.upserted},
		{"entity_program_revocations", func() (int64, error) {
			return qtx.PublishRevokeAbsentEntityPrograms(ctx, db.PublishRevokeAbsentEntityProgramsParams(src))
		}, &stats.revoked},
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
		{"references", func() (int64, error) {
			return qtx.PublishUpsertReferences(ctx, db.PublishUpsertReferencesParams(src))
		}, &stats.upserted},
		{"reference_revocations", func() (int64, error) {
			return qtx.PublishRevokeAbsentReferences(ctx, db.PublishRevokeAbsentReferencesParams(src))
		}, &stats.revoked},
	}
	for _, st := range steps {
		rows, err := st.run()
		if err != nil {
			return "", publishStats{}, fmt.Errorf("publish step %s: %w", st.name, err)
		}
		if st.out != nil {
			*st.out += rows
		}
	}

	version := fmt.Sprintf("ofac-%s-%d", rm.checksum[:12], p.now().Unix())
	var doi pgtype.Date
	if rm.doc.DateOfIssue != nil {
		doi = pgtype.Date{Time: rm.doc.DateOfIssue.UTC(), Valid: true}
	}
	if _, err := qtx.InsertSnapshot(ctx, db.InsertSnapshotParams{
		SourceID:     sourceID,
		Version:      version,
		Checksum:     rm.checksum,
		EntityCount:  rm.res.Entities,
		WalletCount:  rm.res.Wallets,
		ProgramCount: rm.res.Programs,
		DateOfIssue:  doi,
	}); err != nil {
		return "", publishStats{}, fmt.Errorf("snapshot row: %w", err)
	}
	return version, stats, nil
}

func (p *ofacPipeline) LatestVersion(ctx context.Context, qc *db.Queries,
	sourceID int64) (string, string, error) {
	row, err := qc.GetLatestSnapshot(ctx, sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return row.Version, row.Checksum, nil
}

func (p *ofacPipeline) StagingNames() []string {
	return []string{
		"sanctions_entities", "sanctions_wallets", "sanctions_programs",
		"sanctions_entity_programs", "sanctions_references",
	}
}
