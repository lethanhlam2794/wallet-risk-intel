package warmindex

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/testutil"
)

const testDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_warmindex_test?sslmode=disable"

var (
	addrW1 = "0x0000000000000000000000000000000000000a01"
	addrW2 = "0x0000000000000000000000000000000000000a02"
	addrW3 = "0x0000000000000000000000000000000000000a03"
	addrW4 = "0x0000000000000000000000000000000000000a04"
)

func netByCode(t *testing.T, pool *pgxpool.Pool, code string) network.Network {
	t.Helper()
	var net network.Network
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id, code, name, chain_reference, namespace FROM blockchain_networks
		 WHERE code=$1`, code).Scan(
		&net.ID, &net.Code, &net.Name, &net.ChainReference, &net.Namespace))
	return net
}

func warmRow(t *testing.T, pool *pgxpool.Pool, id int64) db.WarmQueue {
	t.Helper()
	var r db.WarmQueue
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id, network_id, address, priority, reason, source_screening_id,
		        status, attempts, claimed_at, created_at, updated_at
		 FROM warm_queue WHERE id=$1`, id).Scan(
		&r.ID, &r.NetworkID, &r.Address, &r.Priority, &r.Reason,
		&r.SourceScreeningID, &r.Status, &r.Attempts, &r.ClaimedAt,
		&r.CreatedAt, &r.UpdatedAt))
	return r
}

func warmRowCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM warm_queue`).Scan(&n))
	return n
}

func ledgerCalls(t *testing.T, pool *pgxpool.Pool, code string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(calls),0) FROM warm_quota_ledger
		 WHERE provider_code=$1`, code).Scan(&n))
	return n
}

func insertWarmItem(t *testing.T, pool *pgxpool.Pool, netID int64,
	addr string, priority int) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO warm_queue (network_id, address, priority, reason)
		 VALUES ($1, $2, $3, 'test') RETURNING id`,
		netID, addr, priority).Scan(&id))
	return id
}

// Dedupe (contract): a second enqueue of the same (net,addr) produces one
// row and only ever RAISES priority — reason follows the highest signal.
func TestQueueEnqueue_DedupeMonotone(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := netByCode(t, pool, "ethereum")
	queue := NewQueue(q, true, nil)
	ctx := context.Background()

	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PriorityRiskyLabel, "label:SCAM", nil))
	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PriorityRiskyLabel, "label:PHISHING", nil))
	assert.Equal(t, 1, warmRowCount(t, pool), "same (net,addr) → one row")

	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PrioritySanctionsMatch, "sanctions_direct_match", nil))
	var id int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM warm_queue WHERE network_id=$1 AND address=$2`,
		net.ID, addrW1).Scan(&id))
	r := warmRow(t, pool, id)
	assert.Equal(t, int32(PrioritySanctionsMatch), r.Priority,
		"higher signal raises priority")
	assert.Equal(t, "sanctions_direct_match", r.Reason,
		"reason follows the winning signal")

	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PriorityRiskyLabel, "label:HACK", nil))
	r = warmRow(t, pool, id)
	assert.Equal(t, int32(PrioritySanctionsMatch), r.Priority,
		"priority is monotone — lower signal never demotes")
	assert.Equal(t, "sanctions_direct_match", r.Reason)
	assert.Equal(t, 1, warmRowCount(t, pool))
}

// A done/dead row is history — a fresh signal re-enqueues cleanly (D4).
func TestQueueEnqueue_ReenqueueAfterTerminal(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := netByCode(t, pool, "ethereum")
	queue := NewQueue(q, true, nil)
	ctx := context.Background()

	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PriorityRiskyLabel, "label:SCAM", nil))
	_, err := pool.Exec(ctx, `UPDATE warm_queue SET status='done' WHERE address=$1`, addrW1)
	require.NoError(t, err)
	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PriorityRiskyLabel, "label:HACK", nil))
	assert.Equal(t, 2, warmRowCount(t, pool),
		"done row must not block re-enqueue")

	_, err = pool.Exec(ctx,
		`UPDATE warm_queue SET status='dead' WHERE status='pending' AND address=$1`, addrW1)
	require.NoError(t, err)
	require.NoError(t, queue.Enqueue(ctx, net.ID, addrW1,
		PrioritySanctionsMatch, "sanctions_direct_match", nil))
	assert.Equal(t, 3, warmRowCount(t, pool),
		"dead row must not block re-enqueue")
}

// screening_id is carried verbatim when provided.
func TestQueueEnqueue_ScreeningID(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := netByCode(t, pool, "ethereum")
	queue := NewQueue(q, true, nil)
	sid := uuid.New()

	require.NoError(t, queue.Enqueue(context.Background(), net.ID, addrW1,
		PriorityRiskyLabel, "label:SCAM", &sid))
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id FROM warm_queue WHERE address=$1`, addrW1).Scan(&id))
	r := warmRow(t, pool, id)
	require.True(t, r.SourceScreeningID.Valid)
	assert.Equal(t, sid, uuid.UUID(r.SourceScreeningID.Bytes))
}

// Only the canonical normalized form enters the queue (D8) — uppercase or
// un-prefixed input is a caller bug, not a stored row.
func TestQueueEnqueue_InvalidAddress(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := netByCode(t, pool, "ethereum")
	queue := NewQueue(q, true, nil)
	ctx := context.Background()

	for _, bad := range []string{
		"0x0000000000000000000000000000000000000A01", // uppercase ≠ normalized
		"0000000000000000000000000000000000000a01",   // missing 0x ≠ canonical
		"garbage",
		"0x123",
		"",
	} {
		require.Error(t, queue.Enqueue(ctx, net.ID, bad,
			PriorityRiskyLabel, "label:SCAM", nil), "input %q", bad)
	}
	assert.Equal(t, 0, warmRowCount(t, pool))
}

// Feature-flag off → silent no-op (contract §7).
func TestQueueEnqueue_DisabledNoop(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	net := netByCode(t, pool, "ethereum")
	queue := NewQueue(q, false, nil)

	require.NoError(t, queue.Enqueue(context.Background(), net.ID, addrW1,
		PrioritySanctionsMatch, "sanctions_direct_match", nil))
	assert.Equal(t, 0, warmRowCount(t, pool))
}

func TestMaskAddr(t *testing.T) {
	assert.Equal(t, "0x0000…dead",
		MaskAddr("0x000000000000000000000000000000000000dead"))
	assert.Equal(t, "short", MaskAddr("short"))
}
