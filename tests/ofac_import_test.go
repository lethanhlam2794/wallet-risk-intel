package tests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/provider/ofac"
	"checkwallet/internal/testutil"
)

const ofacFixture = "../internal/provider/ofac/testdata/valid_small.xml"

func pguuid(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func loadNetworkIDs(t *testing.T, ctx context.Context, q *db.Queries) map[string]int64 {
	t.Helper()
	rows, err := q.ListActiveNetworks(ctx)
	require.NoError(t, err)
	m := map[string]int64{}
	for _, n := range rows {
		m[n.Code] = n.ID
	}
	return m
}

func TestOFACImport_StagingCounts(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	ctx := context.Background()
	q := db.New(pool)
	imp := ofac.NewImporter(loadNetworkIDs(t, ctx, q))
	runID := uuid.New()

	res, _, err := imp.Import(ctx, q, ofacFixture, runID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Entities)
	assert.Equal(t, int64(5), res.Wallets)
	assert.Equal(t, int64(2), res.Programs)
	assert.Equal(t, int64(3), res.EntityPrograms)
	assert.Equal(t, int64(2), res.References)

	counts, err := q.StgCountByRun(ctx, pguuid(runID))
	require.NoError(t, err)
	byTable := map[string]int64{}
	for _, c := range counts {
		byTable[c.TableName] = c.N
	}
	assert.Equal(t, int64(2), byTable["entities"])
	assert.Equal(t, int64(5), byTable["wallets"])
	assert.Equal(t, int64(2), byTable["programs"])
	assert.Equal(t, int64(3), byTable["entity_programs"])
	assert.Equal(t, int64(2), byTable["references"])

	orphans, err := q.StgOrphanWallets(ctx, pguuid(runID))
	require.NoError(t, err)
	assert.Equal(t, int64(0), orphans)

	// network_id resolution: ETH→ethereum id, BNB/BSC→bsc id, USDT/XBT→NULL.
	rows, err := pool.Query(ctx,
		`SELECT currency_code, network_id, match_scope FROM stg_sanctions_wallets WHERE run_id=$1 ORDER BY currency_code`,
		runID)
	require.NoError(t, err)
	defer rows.Close()
	type row struct {
		cur   string
		net   *int64
		scope string
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.cur, &r.net, &r.scope))
		got = append(got, r)
	}
	require.Len(t, got, 5)
	ids := loadNetworkIDs(t, ctx, q)
	for _, r := range got {
		switch r.cur {
		case "ETH":
			require.NotNil(t, r.net)
			assert.Equal(t, ids["ethereum"], *r.net)
			assert.Equal(t, "CHAIN_SPECIFIC", r.scope)
		case "BNB", "BSC":
			require.NotNil(t, r.net)
			assert.Equal(t, ids["bsc"], *r.net)
			assert.Equal(t, "CHAIN_SPECIFIC", r.scope)
		case "USDT":
			assert.Nil(t, r.net)
			assert.Equal(t, "UNRESOLVED", r.scope)
		case "XBT":
			assert.Nil(t, r.net)
			assert.Equal(t, "CHAIN_SPECIFIC", r.scope)
		}
	}
}

func TestOFACImport_IdempotentPerRun(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	ctx := context.Background()
	q := db.New(pool)
	imp := ofac.NewImporter(loadNetworkIDs(t, ctx, q))
	runID := uuid.New()

	_, _, err := imp.Import(ctx, q, ofacFixture, runID)
	require.NoError(t, err)
	// Re-run the same fixture under the same run: identical cardinality.
	res, _, err := imp.Import(ctx, q, ofacFixture, runID)
	require.NoError(t, err)

	counts, err := q.StgCountByRun(ctx, pguuid(runID))
	require.NoError(t, err)
	byTable := map[string]int64{}
	for _, c := range counts {
		byTable[c.TableName] = c.N
	}
	assert.Equal(t, int64(2), byTable["entities"])
	assert.Equal(t, int64(5), byTable["wallets"])
	assert.Equal(t, int64(2), res.Entities, "importer still reports rows it attempted")
}

func TestOFACImport_FailureWritesNothing(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	ctx := context.Background()
	q := db.New(pool)
	imp := ofac.NewImporter(loadNetworkIDs(t, ctx, q))
	runID := uuid.New()

	_, _, err := imp.Import(ctx, q, "../internal/provider/ofac/testdata/truncated.xml", runID)
	require.Error(t, err)
	counts, err := q.StgCountByRun(ctx, pguuid(runID))
	require.NoError(t, err)
	for _, c := range counts {
		assert.Zero(t, c.N, "failed import must leave zero staging rows in %s", c.TableName)
	}
}
