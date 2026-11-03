package tests

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/ingestion"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/walletattribution"
	"checkwallet/internal/testutil"
)

// stubSource is a SnapshotSource that always fails — an interrupted
// download must land a FAILED run and leave live data untouched.
type stubSource struct{ err error }

func (s stubSource) Download(ctx context.Context) (provider.Snapshot, error) {
	return provider.Snapshot{}, s.err
}

func newLabelsSvc(t *testing.T, fetcher provider.SnapshotSource) *ingestion.Service {
	t.Helper()
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	fetchers := map[string]provider.SnapshotSource{
		walletattribution.SourceCode: fetcher,
	}
	return ingestion.NewService(pool, testResolver(t, q), fetchers,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestLabelsFail_DownloadLeavesLiveAlone: after a successful publish, a run
// whose download fails must leave all labels live (interrupted fetch).
func TestLabelsFail_DownloadLeavesLiveAlone(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), map[string]provider.SnapshotSource{
		walletattribution.SourceCode: stubSource{errors.New("connection reset mid-body")},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: "testdata/labels_eth.csv,testdata/labels_bsc.csv",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", runStatus(t, q, runID).Status)
	live := count(t, pool, "SELECT count(*) FROM wallet_labels WHERE revoked_at IS NULL")
	require.Equal(t, int64(7), live)

	// Now a run that fails at download: no snapshot, no revocations.
	runID2, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
	})
	require.NoError(t, err)
	run2 := runStatus(t, q, runID2)
	require.Equal(t, "FAILED", run2.Status)
	assert.Contains(t, run2.ErrorMessage.String, "connection reset mid-body")
	assert.Equal(t, live, count(t, pool,
		"SELECT count(*) FROM wallet_labels WHERE revoked_at IS NULL"))
}

// TestLabelsFail_TruncatedCSV: a file cut mid-row fails the run — either at
// parse time (unterminated quote) or at validation (0 staged rows). Live
// data is never touched.
func TestLabelsFail_TruncatedCSV(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	data, _ := os.ReadFile("testdata/labels_eth.csv")

	// (a) Cut inside the second data row → ragged tail → the file produces
	// 0 staged rows → FAILED validation, nothing published.
	trunc := filepath.Join(t.TempDir(), "trunc_a.csv")
	require.NoError(t, os.WriteFile(trunc, data[:100], 0o600))
	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: trunc + ",testdata/labels_bsc.csv",
	})
	require.NoError(t, err)
	run := runStatus(t, q, runID)
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "0 staged rows")

	// (b) Unterminated quoted field → malformed CSV → FAILED at stage time.
	truncQ := filepath.Join(t.TempDir(), "trunc_b.csv")
	require.NoError(t, os.WriteFile(truncQ, []byte(
		"address,network,entity,label,category,source,source_url,confidence,last_updated\n"+
			"\"0x00000000000000000000000000000000000000a1,ethereum,E,L,entity,s,u,high,2026-10-01\n"), 0o600))
	runID2, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: truncQ,
	})
	require.NoError(t, err)
	run2 := runStatus(t, q, runID2)
	require.Equal(t, "FAILED", run2.Status)

	assert.Equal(t, int64(0), count(t, pool, "SELECT count(*) FROM wallet_labels"))
}

// TestLabelsFail_HeaderDriftRun: schema drift upstream fails the whole run.
func TestLabelsFail_HeaderDriftRun(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	runID, err := svc.RunSync(context.Background(), ingestion.RunRequest{
		SourceCode: walletattribution.SourceCode, TriggeredBy: "test",
		FilePath: "testdata/labels_eth.csv,testdata/labels_badheader.csv",
	})
	require.NoError(t, err)
	run := runStatus(t, q, runID)
	require.Equal(t, "FAILED", run.Status)
	assert.Contains(t, run.ErrorMessage.String, "header drift")
	assert.Equal(t, int64(0), count(t, pool, "SELECT count(*) FROM wallet_labels"))
}

// TestLabelsFail_StaleRunSweep is source-agnostic: a RUNNING labels row
// older than the sweep horizon is reaped like any other.
func TestLabelsFail_StaleRunSweep(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	svc := ingestion.NewService(pool, testResolver(t, q), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	_, err := pool.Exec(context.Background(),
		`INSERT INTO ingestion_runs (id, source_id, status, started_at)
		 SELECT gen_random_uuid(), id, 'RUNNING', NOW() - interval '2 hours'
		 FROM data_sources WHERE code='wallet_attribution'`)
	require.NoError(t, err)
	svc.SweepStaleRuns(context.Background())
	assert.Equal(t, int64(0), count(t, pool,
		`SELECT count(*) FROM ingestion_runs r JOIN data_sources s ON s.id=r.source_id
		 WHERE s.code='wallet_attribution' AND r.status='RUNNING'`))
}
