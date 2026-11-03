// Package ingestion orchestrates snapshot-source runs end to end:
// single-flight gating (advisory lock + RUNNING unique index), download,
// staging import, validation, atomic publish, and cache-pointer updates.
// Per-source work dispatches through the Pipeline registry — no source-
// keyed if-chains (F1).
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
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/walletattribution"
)

// ErrIngestionRunning is returned when a run for the source is already
// in flight (advisory lock or RUNNING-row gate).
var ErrIngestionRunning = errors.New("ingestion already running for source")

// failedStagingKeep is how many recent runs keep their staging rows for
// diagnosis; older staging is purged at run start.
const failedStagingKeep = 5

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
	FilePath    string // comma-separated local files; skips download
}

// Service runs snapshot ingestions against the pinned pool.
type Service struct {
	pool       *pgxpool.Pool
	log        *slog.Logger
	networks   map[string]int64
	pipelines  map[string]Pipeline
	fetchers   map[string]provider.SnapshotSource
	metrics    *metrics.Metrics
	runTimeout time.Duration
	now        func() time.Time // test hook
}

// SetMetrics attaches Prometheus collectors (nil-safe no-op when unset).
func (s *Service) SetMetrics(m *metrics.Metrics) {
	s.metrics = m
	for _, p := range s.pipelines {
		if pm, ok := p.(interface{ SetMetrics(*metrics.Metrics) }); ok {
			pm.SetMetrics(m)
		}
	}
}

// SetRunTimeout overrides the detached-run deadline (tests shrink it).
func (s *Service) SetRunTimeout(d time.Duration) { s.runTimeout = d }

// SweepStaleRuns marks RUNNING rows older than the run timeout as FAILED —
// crashed workers leave no zombie runs (call on process boot).
func (s *Service) SweepStaleRuns(ctx context.Context) {
	if err := s.q().MarkStaleRunsFailed(ctx); err != nil {
		s.log.Warn("stale run sweep failed", "error", err)
	}
}

// HasPipeline reports whether a source code maps to a registered pipeline —
// the admin trigger validates against this set.
func (s *Service) HasPipeline(code string) bool {
	_, ok := s.pipelines[code]
	return ok
}

// NewService wires the ingestion orchestrator. fetchers maps source code →
// downloader; it may be nil/empty when only FilePath runs are used.
func NewService(pool *pgxpool.Pool, resolver *network.Resolver,
	fetchers map[string]provider.SnapshotSource, log *slog.Logger) *Service {
	nets := map[string]int64{}
	for _, n := range resolver.List() {
		nets[n.Code] = n.ID
	}
	s := &Service{
		pool: pool, log: log, networks: nets, fetchers: fetchers,
		runTimeout: defaultRunTimeout, now: time.Now,
	}
	s.pipelines = map[string]Pipeline{
		"ofac_sdn":                   newOFACPipeline(nets, func() time.Time { return s.now() }),
		walletattribution.SourceCode: newLabelsPipeline(nets, func() time.Time { return s.now() }, log),
	}
	return s
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
	// the most recent runs (kept for diagnosis). The keep-set must include
	// every currently-RUNNING run — an OFAC run's housekeeping must never
	// delete a labels run's in-flight staging (F13), and vice versa.
	_ = qc.MarkStaleRunsFailed(ctx)
	var keep []pgtype.UUID
	if recent, err := qc.ListRecentRunIDs(ctx, failedStagingKeep); err == nil {
		keep = append(keep, recent...)
	}
	if running, err := qc.ListRunningRunIDs(ctx); err == nil {
		keep = append(keep, running...)
	}
	if len(keep) > 0 {
		s.purgeStagingExcept(ctx, qc, keep)
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

	p, ok := s.pipelines[req.SourceCode]
	if !ok {
		s.finish(qc, runID, "FAILED", provider.ImportResult{}, publishStats{},
			"", "", fmt.Sprintf("no pipeline registered for source %q", req.SourceCode))
		return
	}

	// Liveness heartbeat: the stale-run sweep keys off heartbeat_at, so a
	// slow-but-alive run (large download, long publish) is never reaped.
	// Beats go through the pool (not the pinned conn — pgx conns forbid
	// concurrent use) and the goroutine is joined before execute returns
	// so it can never outlive the pinned conn's Release().
	hbStop := make(chan struct{})
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hbStop:
				return
			case <-t.C:
				_ = s.q().TouchRunHeartbeat(context.Background(),
					pgtype.UUID{Bytes: runID, Valid: true})
			}
		}
	}()
	defer func() { close(hbStop); <-hbDone }()

	// 1. Obtain the snapshot (download or file mode). File mode accepts
	// comma-separated paths; only downloader-produced temp files are
	// removed — never a caller-owned path.
	var snap provider.Snapshot
	var err error
	downloaded := false
	if req.FilePath != "" {
		snap, err = checksumFiles(req.FilePath)
	} else if fetcher := s.fetchers[req.SourceCode]; fetcher == nil {
		err = fmt.Errorf("no downloader configured for source %q", req.SourceCode)
	} else {
		snap, err = fetcher.Download(ctx)
		downloaded = err == nil
	}
	if downloaded {
		defer func() {
			for _, f := range snap.Files {
				os.Remove(f.Path)
			}
		}()
	}
	if err != nil {
		s.finish(qc, runID, "FAILED", provider.ImportResult{}, publishStats{}, "", "", fmt.Sprintf("fetch: %s", err))
		return
	}
	for _, f := range snap.Files {
		if f.FinalURL != "" {
			s.log.Info("snapshot file fetched", "run_id", runID, "name", f.Name,
				"final_url", f.FinalURL, "checksum", f.Checksum[:12], "size_bytes", f.SizeBytes)
		}
	}

	runUUID := pgtype.UUID{Bytes: runID, Valid: true}

	// 2. Checksum-identical no-op: skip stage/publish/invalidate entirely.
	prevVersion, prevChecksum, prevErr := p.LatestVersion(ctx, qc, sourceID)
	if prevErr == nil && prevChecksum != "" && prevChecksum == snap.Checksum {
		s.log.Info("identical checksum; skipping publish", "run_id", runID, "checksum", snap.Checksum[:12])
		s.finish(qc, runID, "COMPLETED", provider.ImportResult{}, publishStats{},
			snap.Checksum, prevVersion, "")
		result = "COMPLETED"
		return
	}

	// 3. Stage.
	res, meta, err := p.Import(ctx, qc, snap, runUUID)
	if err != nil {
		s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("stage: %s", err))
		return
	}

	// 4. Validate staged snapshot.
	if err := p.Validate(ctx, qc, runUUID, meta); err != nil {
		s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("validate: %s", err))
		return
	}

	// 5. Publish atomically in ONE transaction on the pinned conn. Readers
	// resolve the current version from the snapshots table itself — no
	// separate cache pointer to go stale.
	tx, err := conn.Begin(ctx)
	if err != nil {
		s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("publish: %s", err))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	for _, stmt := range []string{
		"SET LOCAL lock_timeout = '10s'",
		"SET LOCAL statement_timeout = '120s'",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			s.finish(qc, runID, "FAILED", res, publishStats{}, snap.Checksum, "", fmt.Sprintf("publish tx tuning: %s", err))
			return
		}
	}
	version, stats, err := p.Publish(ctx, db.New(tx), runUUID, sourceID, meta)
	if err != nil {
		// Roll back BEFORE finish: qc shares the pinned conn, so a finish
		// issued while tx is still open lands inside the aborted
		// transaction (25P02) and the run would stay RUNNING forever.
		_ = tx.Rollback(context.Background())
		s.finish(qc, runID, "FAILED", res, stats, snap.Checksum, "", fmt.Sprintf("publish: %s", err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(context.Background())
		s.finish(qc, runID, "FAILED", res, stats, snap.Checksum, "", fmt.Sprintf("publish commit: %s", err))
		return
	}
	committed = true

	// 6. Successful runs don't keep staging around.
	s.deleteStaging(context.Background(), qc, runID, p)
	s.finish(qc, runID, "COMPLETED", res, stats, snap.Checksum, version, "")
	result = "COMPLETED"
	if s.metrics != nil {
		s.metrics.IncIngestionRecords(req.SourceCode, "fetched",
			res.Entities+res.Wallets+res.Programs+res.EntityPrograms+res.References+res.Labels)
		s.metrics.IncIngestionRecords(req.SourceCode, "updated", stats.upserted)
		s.metrics.IncIngestionRecords(req.SourceCode, "rejected", res.Rejected+stats.revoked)
	}
	s.log.Info("ingestion run completed", "run_id", runID, "version", version,
		"entities", res.Entities, "wallets", res.Wallets, "labels", res.Labels,
		"rejected", res.Rejected)
}

func (s *Service) q() *db.Queries { return db.New(s.pool) }

func (s *Service) finish(qc *db.Queries, runID uuid.UUID, status string,
	res provider.ImportResult, stats publishStats, checksum, version, errMsg string) {
	fetched := res.Entities + res.Wallets + res.Programs + res.EntityPrograms + res.References + res.Labels
	params := db.FinishIngestionRunParams{
		ID:              pgtype.UUID{Bytes: runID, Valid: true},
		Status:          status,
		RecordsFetched:  fetched,
		RecordsInserted: fetched,
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

// purgeStagingExcept removes staging rows for runs outside the keep set —
// across EVERY registered pipeline's staging (housekeeping is global so a
// dead run's rows can't leak regardless of which source ran next).
func (s *Service) purgeStagingExcept(ctx context.Context, qc *db.Queries, keep []pgtype.UUID) {
	seen := map[string]bool{}
	for _, p := range s.pipelines {
		for _, name := range p.StagingNames() {
			if seen[name] {
				continue
			}
			seen[name] = true
			if err := stagingPurge[name](ctx, qc, keep); err != nil {
				s.log.Warn("staging purge failed", "table", name, "error", err)
			}
		}
	}
}

// deleteStaging removes this run's staging rows — only the running
// pipeline's own set.
func (s *Service) deleteStaging(ctx context.Context, qc *db.Queries, runID uuid.UUID, p Pipeline) {
	u := pgtype.UUID{Bytes: runID, Valid: true}
	for _, name := range p.StagingNames() {
		_ = stagingDelete[name](ctx, qc, u)
	}
}

// checksumFiles hashes comma-separated local artifact paths for the -file
// worker path. The combined checksum composes over the provided files only,
// in filename order — a single-CSV run is network-scoped by its content,
// not by which files were passed.
func checksumFiles(csvPaths string) (provider.Snapshot, error) {
	var files []provider.File
	for _, path := range strings.Split(csvPaths, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return provider.Snapshot{}, err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return provider.Snapshot{}, err
		}
		files = append(files, provider.File{
			Path: path, Name: filepath.Base(path),
			Checksum: hex.EncodeToString(h.Sum(nil)), SizeBytes: n,
		})
	}
	if len(files) == 0 {
		return provider.Snapshot{}, errors.New("no usable files in -file list")
	}
	return provider.Snapshot{
		Files:     files,
		Checksum:  provider.CombinedChecksum(files),
		SizeBytes: provider.TotalSize(files),
	}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
