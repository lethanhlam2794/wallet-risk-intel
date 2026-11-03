// Package ingestion orchestrates snapshot-source runs end to end:
// single-flight gating (advisory lock + RUNNING unique index), download,
// staging import, validation, atomic publish, and cache-pointer updates.
package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/ofac"
)

// ErrIngestionRunning is returned when a run for the source is already
// in flight (advisory lock or RUNNING-row gate).
var ErrIngestionRunning = errors.New("ingestion already running for source")

// failedStagingKeep is how many recent runs keep their staging rows for
// diagnosis; older staging is purged at run start.
const failedStagingKeep = 5

// staleRunAfter is how long a run's heartbeat may go silent before the
// sweep reaps the row as a crashed worker.
const staleRunAfter = time.Hour

// defaultRunTimeout bounds a detached async run (Enqueue). Long enough for
// 3 download attempts + parse + publish of the ~128MB SDN file; short
// enough that a wedged run cannot hold the single-flight gate forever.
const defaultRunTimeout = 45 * time.Minute

// heartbeatInterval is how often a live run touches heartbeat_at so the
// stale-run sweep can distinguish "alive but slow" from "crashed".
const heartbeatInterval = 30 * time.Second

// RunRequest describes one ingestion execution.
type RunRequest struct {
	SourceCode  string // data_sources.code, e.g. "ofac_sdn"
	TriggeredBy string // api client name or "worker"
	FilePath    string // when set, use this local file instead of downloading
}

// Service runs snapshot ingestions against the pinned pool.
type Service struct {
	pool       *pgxpool.Pool
	log        *slog.Logger
	networks   map[string]int64
	downloader provider.SnapshotSource
	metrics    *metrics.Metrics
	runTimeout time.Duration
	now        func() time.Time // test hook
}

// SetMetrics attaches Prometheus collectors (nil-safe no-op when unset).
func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

// SetRunTimeout overrides the detached-run deadline (tests shrink it).
func (s *Service) SetRunTimeout(d time.Duration) { s.runTimeout = d }

// SweepStaleRuns marks RUNNING rows older than the run timeout as FAILED —
// crashed workers leave no zombie runs (call on process boot).
func (s *Service) SweepStaleRuns(ctx context.Context) {
	if err := s.q().MarkStaleRunsFailed(ctx); err != nil {
		s.log.Warn("stale run sweep failed", "error", err)
	}
}

// NewService wires the ingestion orchestrator. downloader may be nil when
// only FilePath runs are used.
func NewService(pool *pgxpool.Pool, resolver *network.Resolver,
	downloader provider.SnapshotSource, log *slog.Logger) *Service {
	nets := map[string]int64{}
	for _, n := range resolver.List() {
		nets[n.Code] = n.ID
	}
	return &Service{
		pool: pool, log: log, networks: nets,
		downloader: downloader, runTimeout: defaultRunTimeout, now: time.Now,
	}
}

// RunSync executes an ingestion run in the caller's context (worker mode).
// Returns the run id; run outcome is recorded on the ingestion_runs row.
func (s *Service) RunSync(ctx context.Context, req RunRequest) (uuid.UUID, error) {
	conn, qc, runID, sourceID, err := s.begin(ctx, req)
	if err != nil {
		return uuid.Nil, err
	}
	defer conn.Release()
	s.execute(ctx, conn, qc, runID, sourceID, req)
	return runID, nil
}

// Enqueue starts an asynchronous ingestion run and returns its id once the
// RUNNING row is visible (so 202 responses carry a real, queryable id).
// The goroutine runs on a detached context with panic recovery.
func (s *Service) Enqueue(ctx context.Context, req RunRequest) (uuid.UUID, error) {
	conn, qc, runID, sourceID, err := s.begin(ctx, req)
	if err != nil {
		return uuid.Nil, err
	}
	go func() {
		defer conn.Release()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("ingestion panic", "run_id", runID, "panic", r)
				s.finish(qc, runID, "FAILED", provider.ImportResult{}, publishStats{},
					"", "", fmt.Sprintf("panic: %v", r))
				advisoryUnlock(context.Background(), conn, sourceID)
			}
		}()
		// Detached but bounded: client disconnects must not kill the run,
		// and a wedged run must not hold the single-flight gate forever.
		runCtx, cancel := context.WithTimeout(context.Background(), s.runTimeout)
		defer cancel()
		s.execute(runCtx, conn, qc, runID, sourceID, req)
	}()
	return runID, nil
}

// begin performs the synchronous gate: pinned conn → advisory lock →
// stale-run reap → staging purge → RUNNING row insert.
func (s *Service) begin(ctx context.Context, req RunRequest) (*pgxpool.Conn, *db.Queries, uuid.UUID, int64, error) {
	source, err := s.q().GetSourceByCode(ctx, req.SourceCode)
	if err != nil {
		return nil, nil, uuid.Nil, 0, fmt.Errorf("source %q: %w", req.SourceCode, err)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, uuid.Nil, 0, fmt.Errorf("acquire conn: %w", err)
	}
	got, err := tryAdvisoryLock(ctx, conn, source.ID)
	if err != nil || !got {
		conn.Release()
		if err == nil {
			return nil, nil, uuid.Nil, 0, ErrIngestionRunning
		}
		return nil, nil, uuid.Nil, 0, err
	}
	qc := db.New(conn)

	// Reap crashed workers' RUNNING rows, then purge staging older than
	// the most recent runs (kept for diagnosis).
	_ = qc.MarkStaleRunsFailed(ctx)
	keep, err := qc.ListRecentRunIDs(ctx, failedStagingKeep)
	if err == nil && len(keep) > 0 {
		uuids := make([]pgtype.UUID, 0, len(keep))
		for _, id := range keep {
			uuids = append(uuids, pgtype.UUID{Bytes: id.Bytes, Valid: true})
		}
		s.purgeStagingExcept(ctx, qc, uuids)
	}

	runID := uuid.New()
	if _, err := qc.InsertIngestionRun(ctx, db.InsertIngestionRunParams{
		ID:          pgtype.UUID{Bytes: runID, Valid: true},
		SourceID:    source.ID,
		TriggeredBy: pgtype.Text{String: req.TriggeredBy, Valid: req.TriggeredBy != ""},
	}); err != nil {
		advisoryUnlock(ctx, conn, source.ID)
		conn.Release()
		if isUniqueViolation(err) {
			return nil, nil, uuid.Nil, 0, ErrIngestionRunning
		}
		return nil, nil, uuid.Nil, 0, fmt.Errorf("insert run: %w", err)
	}
	s.log.Info("ingestion run started", "run_id", runID, "source", req.SourceCode,
		"triggered_by", req.TriggeredBy, "file_mode", req.FilePath != "")
	return conn, qc, runID, source.ID, nil
}

// execute runs the full pipeline and finishes the run row. All failures
// land on ingestion_runs; nothing here panics.
func (s *Service) execute(ctx context.Context, conn *pgxpool.Conn, qc *db.Queries,
	runID uuid.UUID, sourceID int64, req RunRequest) {
	defer advisoryUnlock(context.Background(), conn, sourceID)
	started := s.now()
	result := "FAILED"
	defer func() {
		if s.metrics != nil {
			s.metrics.ObserveIngestion(req.SourceCode, result, s.now().Sub(started))
		}
	}()

	// Liveness heartbeat: the stale-run sweep keys off heartbeat_at, so a
	// slow-but-alive run (large download, long publish) is never reaped.
	hbStop := make(chan struct{})
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				_ = qc.TouchRunHeartbeat(context.Background(),
					pgtype.UUID{Bytes: runID, Valid: true})
			}
		}
	}()
	defer close(hbStop)

	// 1. Obtain the snapshot (download or file mode).
	var snap provider.Snapshot
	var err error
	if req.FilePath != "" {
		snap, err = checksumFile(req.FilePath)
	} else if s.downloader == nil {
		err = errors.New("no downloader configured")
	} else {
		snap, err = s.downloader.Download(ctx)
		if err == nil {
			defer os.Remove(snap.Path)
		}
	}
	if err != nil {
		s.finish(qc, runID, "FAILED", provider.ImportResult{}, publishStats{}, "", "", fmt.Sprintf("fetch: %s", err))
		return
	}
	if snap.FinalURL != "" {
		s.log.Info("snapshot fetched", "run_id", runID, "final_url", snap.FinalURL,
			"checksum", snap.Checksum[:12], "size_bytes", snap.SizeBytes)
	}

	// 2. Checksum-identical no-op: skip stage/publish/invalidate entirely.
	prev, prevErr := qc.GetLatestSnapshot(ctx, sourceID)
	if prevErr == nil && prev.Checksum == snap.Checksum {
		s.log.Info("identical checksum; skipping publish", "run_id", runID, "checksum", snap.Checksum[:12])
		s.finish(qc, runID, "COMPLETED", provider.ImportResult{}, publishStats{},
			snap.Checksum, prev.Version, "")
		result = "COMPLETED"
		return
	}

	// 3. Stage.
	imp := ofac.NewImporter(s.networks)
	res, meta, err := imp.Import(ctx, qc, snap.Path, runID)
	if err != nil {
		s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("stage: %s", err))
		return
	}

	// 4. Validate staged snapshot.
	var prevSnap *db.SanctionsSnapshot
	if prevErr == nil {
		prevSnap = &prev
	}
	if err := validateStaged(ctx, qc, runID, meta, prevSnap); err != nil {
		s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("validate: %s", err))
		return
	}

	// 5. Publish atomically. Readers resolve the current version from the
	// snapshots table itself — no separate cache pointer to go stale.
	stats, version, err := s.publish(ctx, conn, sourceID, runID, snap.Checksum, meta, res)
	if err != nil {
		s.finish(qc, runID, "FAILED", res, stats, snap.Checksum, "", fmt.Sprintf("publish: %s", err))
		return
	}

	// 6. Successful runs don't keep staging around.
	s.deleteStaging(context.Background(), qc, runID)
	s.finish(qc, runID, "COMPLETED", res, stats, snap.Checksum, version, "")
	result = "COMPLETED"
	if s.metrics != nil {
		s.metrics.IncIngestionRecords(req.SourceCode, "fetched",
			res.Entities+res.Wallets+res.Programs+res.EntityPrograms+res.References)
		s.metrics.IncIngestionRecords(req.SourceCode, "updated", stats.upserted)
		s.metrics.IncIngestionRecords(req.SourceCode, "rejected", res.Rejected+stats.revoked)
	}
	s.log.Info("ingestion run completed", "run_id", runID, "version", version,
		"entities", res.Entities, "wallets", res.Wallets, "rejected", res.Rejected)
}

func (s *Service) q() *db.Queries { return db.New(s.pool) }

func (s *Service) finish(qc *db.Queries, runID uuid.UUID, status string,
	res provider.ImportResult, stats publishStats, checksum, version, errMsg string) {
	fetched := res.Entities + res.Wallets + res.Programs + res.EntityPrograms + res.References
	params := db.FinishIngestionRunParams{
		ID:              pgtype.UUID{Bytes: runID, Valid: true},
		Status:          status,
		RecordsFetched:  fetched,
		RecordsInserted: res.Entities + res.Wallets + res.Programs + res.EntityPrograms + res.References,
		RecordsUpdated:  stats.upserted,
		RecordsRejected: res.Rejected + stats.revoked,
		SnapshotChecksum: pgtype.Text{
			String: checksum, Valid: checksum != "",
		},
		SnapshotVersion: pgtype.Text{
			String: version, Valid: version != "",
		},
		ErrorMessage: pgtype.Text{
			String: errMsg, Valid: errMsg != "",
		},
	}
	// Detached ctx: finish must land even if caller ctx is done.
	if err := qc.FinishIngestionRun(context.Background(), params); err != nil {
		s.log.Error("failed to finish run row", "run_id", runID, "error", err)
	}
}

func (s *Service) purgeStagingExcept(ctx context.Context, qc *db.Queries, keep []pgtype.UUID) {
	for _, fn := range []func(context.Context, []pgtype.UUID) error{
		qc.StgPurgeEntitiesExceptRuns, qc.StgPurgeWalletsExceptRuns,
		qc.StgPurgeProgramsExceptRuns, qc.StgPurgeEntityProgramsExceptRuns,
		qc.StgPurgeReferencesExceptRuns,
	} {
		if err := fn(ctx, keep); err != nil {
			s.log.Warn("staging purge failed", "error", err)
		}
	}
}

func (s *Service) deleteStaging(ctx context.Context, qc *db.Queries, runID uuid.UUID) {
	u := pgtype.UUID{Bytes: runID, Valid: true}
	for _, fn := range []func(context.Context, pgtype.UUID) error{
		qc.StgDeleteRunEntities, qc.StgDeleteRunWallets, qc.StgDeleteRunPrograms,
		qc.StgDeleteRunEntityPrograms, qc.StgDeleteRunReferences,
	} {
		_ = fn(ctx, u)
	}
}

// checksumFile hashes a local snapshot for the -file worker path.
func checksumFile(path string) (provider.Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return provider.Snapshot{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return provider.Snapshot{}, err
	}
	return provider.Snapshot{Path: path, Checksum: hex.EncodeToString(h.Sum(nil)), SizeBytes: n}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
