package warmindex

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/testutil"
)

// --- seed fixtures -------------------------------------------------------
// Package helpers reused from queue_test.go/drain_test.go: testDSN,
// netByCode, warmRow, warmRowCount, insertWarmItem, addrW*.

// seedAddr renders the canonical lowercase EVM form of a small integer —
// deterministic, unique per n, and always pre-normalized.
func seedAddr(n int64) string { return fmt.Sprintf("0x%040x", n) }

func seedWallet(t *testing.T, pool *pgxpool.Pool, netID int64,
	addr, addrType string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO wallets (network_id, address, normalized_address, address_type)
		 VALUES ($1, $2, $2, $3) RETURNING id`, netID, addr, addrType).Scan(&id))
	return id
}

func seedSanctionEntity(t *testing.T, pool *pgxpool.Pool, extID string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO sanctions_entities (source_id, external_entity_id, primary_name)
		 VALUES ((SELECT id FROM data_sources WHERE code='ofac_sdn'), $1, 'Seed Entity')
		 RETURNING id`, extID).Scan(&id))
	return id
}

// seedSanctionWallet inserts one sanctions_wallets row; netID nil → NULL
// (unmapped listing). identifier_type keeps the uq tuple realistic.
func seedSanctionWallet(t *testing.T, pool *pgxpool.Pool, entityID int64,
	listed, norm string, netID *int64) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO sanctions_wallets
		 (sanctions_entity_id, listed_address, normalized_address,
		  identifier_type, network_id)
		 VALUES ($1, $2, $3, 'Digital Currency Address - ETH', $4)
		 RETURNING id`, entityID, listed, norm, netID).Scan(&id))
	return id
}

func seedLabel(t *testing.T, pool *pgxpool.Pool, walletID int64,
	catCode, fp string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO wallet_labels
		 (wallet_id, category_id, source_id, fingerprint, label)
		 VALUES ($1, (SELECT id FROM risk_categories WHERE code=$2),
		         (SELECT id FROM data_sources WHERE code='wallet_attribution'),
		         $3, $4)
		 RETURNING id`, walletID, catCode, fp, catCode+" fixture").Scan(&id))
	return id
}

func seedIndexState(t *testing.T, pool *pgxpool.Pool, netID, walletID int64,
	sourceCode, status string, truncated bool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO wallet_index_state
		 (network_id, wallet_id, source_id, status, history_truncated)
		 VALUES ($1, $2, (SELECT id FROM data_sources WHERE code=$3), $4, $5)`,
		netID, walletID, sourceCode, status, truncated)
	require.NoError(t, err)
}

// --- counter capture -----------------------------------------------------

// logCapture is a slog.Handler that records every emitted record's
// attributes so tests can assert the per-source counter contract (all six
// keys on every source line).
type logCapture struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[string]any{"_msg": r.Message}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	h.recs = append(h.recs, m)
	return nil
}

func (h *logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logCapture) WithGroup(string) slog.Handler      { return h }

// sourceCounters returns the attribute map of the per-source summary line
// ("warm seed source complete", keyed by source); fails the test when the
// seeder never emitted it. Per-row Warn records also carry a source key —
// the message is the discriminator.
func sourceCounters(t *testing.T, h *logCapture, source string) map[string]any {
	t.Helper()
	for _, m := range h.recs {
		if m["_msg"] == "warm seed source complete" && m["source"] == source {
			return m
		}
	}
	t.Fatalf("no log line for source %q (records: %v)", source, h.recs)
	return nil
}

func counter(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	v, ok := m[key]
	require.True(t, ok, "missing counter key %q in %v", key, m)
	n, ok := v.(int64)
	require.True(t, ok, "counter %q is %T, want int64", key, v)
	return n
}

// assertSixCounters enforces the all-six-keys contract on a source line.
func assertSixCounters(t *testing.T, m map[string]any) {
	t.Helper()
	for _, k := range []string{"scanned", "enqueued", "skipped_non_evm",
		"skipped_inactive_net", "skipped_fresh", "errors"} {
		counter(t, m, k)
	}
}

func newSeedSeeder(pool *pgxpool.Pool, h *logCapture, batchSize int) *Seeder {
	return NewSeeder(db.New(pool), NewQueue(db.New(pool), true, nil),
		batchSize, slog.New(h))
}

// --- tests ---------------------------------------------------------------

// Seed priorities are a hard invariant: every seed tier stays strictly
// below the interactive signal floor so warmseed can never outrank a live
// investigation in the priority DESC, id claim order.
func TestSeedPriorities_BelowSignalFloor(t *testing.T) {
	for _, p := range []int{PrioritySeedOFAC, PrioritySeedRiskyLabel,
		PrioritySeedRefresh} {
		assert.Less(t, p, PriorityRiskyLabel,
			"seed priority %d must stay below the signal floor", p)
	}
	assert.Equal(t, 20, PrioritySeedOFAC)
	assert.Equal(t, 10, PrioritySeedRiskyLabel)
	assert.Equal(t, 5, PrioritySeedRefresh)
}

// Source 1: eligible OFAC rows enqueue at p20/seed:ofac_list; revoked
// rows are SQL-excluded; NULL/inactive/non-EVM-net rows count
// skipped_inactive_net; a non-EVM address on an ACTIVE EVM net counts
// skipped_non_evm; an EVM-shaped but non-canonical address counts errors
// and never aborts the batch.
func TestSeeder_SeedSanctions(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	tron := netByCode(t, pool, "tron")
	ent := seedSanctionEntity(t, pool, "seed-ent-1")

	seedSanctionWallet(t, pool, ent, addrW1, addrW1, &eth.ID)        // eligible
	seedSanctionWallet(t, pool, ent, addrW2, addrW2, &eth.ID)        // eligible
	rev := seedSanctionWallet(t, pool, ent, addrW3, addrW3, &eth.ID) // revoked
	seedSanctionWallet(t, pool, ent, addrW4, addrW4, nil)            // NULL net
	seedSanctionWallet(t, pool, ent, "TX7nBvMkP8YhQ2sW3dF4gH5jK6lZ9xC1vB",
		"TX7nBvMkP8YhQ2sW3dF4gH5jK6lZ9xC1vB", &tron.ID) // inactive non-EVM net
	seedSanctionWallet(t, pool, ent, "not-an-evm-address",
		"not-an-evm-address", &eth.ID) // non-EVM on active EVM net
	seedSanctionWallet(t, pool, ent,
		"0x0000000000000000000000000000000000000A01",
		"0x0000000000000000000000000000000000000A01", &eth.ID) // non-canonical
	_, err := pool.Exec(context.Background(),
		`UPDATE sanctions_wallets SET revoked_at=NOW() WHERE id=$1`, rev)
	require.NoError(t, err)

	h := &logCapture{}
	require.NoError(t, newSeedSeeder(pool, h, 500).Run(context.Background()))

	// Two eligible rows → two queue rows at p20/seed:ofac_list.
	var pri int32
	var reason string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT priority, reason FROM warm_queue
		 WHERE network_id=$1 AND address=$2`, eth.ID, addrW1).
		Scan(&pri, &reason))
	assert.Equal(t, int32(PrioritySeedOFAC), pri)
	assert.Equal(t, "seed:ofac_list", reason)
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT priority FROM warm_queue
		 WHERE network_id=$1 AND address=$2`, eth.ID, addrW2).Scan(&pri))
	assert.Equal(t, 2, warmRowCount(t, pool),
		"only the two eligible rows enqueue")

	m := sourceCounters(t, h, "ofac_list")
	assertSixCounters(t, m)
	assert.Equal(t, int64(6), counter(t, m, "scanned"),
		"revoked row is SQL-excluded — never scanned")
	assert.Equal(t, int64(2), counter(t, m, "enqueued"))
	assert.Equal(t, int64(2), counter(t, m, "skipped_inactive_net"),
		"NULL net + inactive tron net")
	assert.Equal(t, int64(1), counter(t, m, "skipped_non_evm"))
	assert.Equal(t, int64(1), counter(t, m, "errors"),
		"uppercase EVM address fails the canonical pre-check")
	assert.Equal(t, int64(0), counter(t, m, "skipped_fresh"))
}

// Source 2: risky-category labels enqueue at p10/seed:risky_label;
// EXCHANGE/GAMBLING are SQL-filtered (classifications, not crimes);
// revoked labels never scan; net/address gates mirror source 1.
func TestSeeder_SeedLabels(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	tron := netByCode(t, pool, "tron")

	w1 := seedWallet(t, pool, eth.ID, addrW1, "evm")
	w2 := seedWallet(t, pool, eth.ID, addrW2, "evm")
	w3 := seedWallet(t, pool, eth.ID, addrW3, "evm")
	w4 := seedWallet(t, pool, eth.ID, "TronBase58NotEvmFixture", "unknown")
	w5 := seedWallet(t, pool, tron.ID, seedAddr(50), "tron")
	w6 := seedWallet(t, pool, eth.ID, addrW4, "evm")

	seedLabel(t, pool, w1, "SCAM", "fp-scam-1")
	seedLabel(t, pool, w2, "MIXER", "fp-mixer-1")
	seedLabel(t, pool, w3, "EXCHANGE", "fp-exchange-1")
	seedLabel(t, pool, w3, "GAMBLING", "fp-gambling-1")
	seedLabel(t, pool, w4, "PHISHING", "fp-phish-nonevm")
	seedLabel(t, pool, w5, "HACK", "fp-hack-tron")
	rev := seedLabel(t, pool, w6, "RANSOMWARE", "fp-ransom-rev")
	_, err := pool.Exec(context.Background(),
		`UPDATE wallet_labels SET revoked_at=NOW() WHERE id=$1`, rev)
	require.NoError(t, err)

	h := &logCapture{}
	require.NoError(t, newSeedSeeder(pool, h, 500).Run(context.Background()))

	var pri int32
	var reason string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT priority, reason FROM warm_queue
		 WHERE network_id=$1 AND address=$2`, eth.ID, addrW1).
		Scan(&pri, &reason))
	assert.Equal(t, int32(PrioritySeedRiskyLabel), pri)
	assert.Equal(t, "seed:risky_label", reason)
	assert.Equal(t, 2, warmRowCount(t, pool),
		"EXCHANGE/GAMBLING/revoked/ineligible rows enqueue nothing")

	m := sourceCounters(t, h, "risky_label")
	assertSixCounters(t, m)
	assert.Equal(t, int64(4), counter(t, m, "scanned"),
		"EXCHANGE/GAMBLING/revoked labels are SQL-excluded")
	assert.Equal(t, int64(2), counter(t, m, "enqueued"))
	assert.Equal(t, int64(1), counter(t, m, "skipped_inactive_net"),
		"tron-net wallet")
	assert.Equal(t, int64(1), counter(t, m, "skipped_non_evm"))
	assert.Equal(t, int64(0), counter(t, m, "errors"))
}

// Source 3: stale coverage (partial/failed) or history_truncated seeds a
// p5 refresh; adequate coverage counts skipped_fresh. The OR-logic is
// literal: indexing/disabled + history_truncated still seeds.
func TestSeeder_SeedIndexState(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	tron := netByCode(t, pool, "tron")

	type fixture struct {
		status    string
		truncated bool
		source    string
		wantQueue bool
	}
	cases := []fixture{
		{"partial", false, "blockscout", true},
		{"failed", false, "blockscout", true},
		{"fresh", true, "blockscout", true},    // truncated overrides fresh
		{"disabled", true, "blockscout", true}, // literal OR: still seeds
		{"fresh", false, "blockscout", false},
		{"not_indexed", false, "blockscout", false},
		{"indexing", false, "blockscout", false}, // live claim → fresh bucket
		{"disabled", false, "blockscout", false},
	}
	var wantEnqueued int64
	for i, c := range cases {
		w := seedWallet(t, pool, eth.ID, seedAddr(int64(100+i)), "evm")
		seedIndexState(t, pool, eth.ID, w, c.source, c.status, c.truncated)
		if c.wantQueue {
			wantEnqueued++
		}
	}
	// A stale row on the inactive tron net: net gate precedes enqueue.
	wt := seedWallet(t, pool, tron.ID, seedAddr(200), "tron")
	seedIndexState(t, pool, tron.ID, wt, "blockscout", "partial", false)

	h := &logCapture{}
	require.NoError(t, newSeedSeeder(pool, h, 500).Run(context.Background()))

	assert.Equal(t, int(wantEnqueued), warmRowCount(t, pool))
	var pri int32
	var reason string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT priority, reason FROM warm_queue LIMIT 1`).Scan(&pri, &reason))
	assert.Equal(t, int32(PrioritySeedRefresh), pri)
	assert.Equal(t, "seed:refresh_stale", reason)

	m := sourceCounters(t, h, "refresh_stale")
	assertSixCounters(t, m)
	assert.Equal(t, int64(len(cases)+1), counter(t, m, "scanned"),
		"all coverage rows are read — fresh rows must be observable")
	assert.Equal(t, wantEnqueued, counter(t, m, "enqueued"))
	assert.Equal(t, int64(1), counter(t, m, "skipped_inactive_net"))
	assert.Equal(t, int64(4), counter(t, m, "skipped_fresh"),
		"fresh/not_indexed/indexing/disabled without truncation")
	assert.Equal(t, int64(0), counter(t, m, "errors"))
}

// Idempotent replay (contract): the upsert dedupe is the ONLY dedup — a
// second run enqueues again (attempts, honestly counted) yet produces
// zero duplicate ACTIVE rows.
func TestSeeder_IdempotentReplay(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	ent := seedSanctionEntity(t, pool, "seed-ent-replay")
	seedSanctionWallet(t, pool, ent, addrW1, addrW1, &eth.ID)
	w := seedWallet(t, pool, eth.ID, addrW2, "evm")
	seedLabel(t, pool, w, "HACK", "fp-replay-hack")

	s := newSeedSeeder(pool, &logCapture{}, 500)
	require.NoError(t, s.Run(context.Background()))
	first := warmRowCount(t, pool)
	assert.Equal(t, 2, first)

	h2 := &logCapture{}
	require.NoError(t, newSeedSeeder(pool, h2, 500).Run(context.Background()))
	var active int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM warm_queue WHERE status IN ('pending','claimed')`).
		Scan(&active))
	assert.Equal(t, first, active, "replay must not duplicate active rows")

	m := sourceCounters(t, h2, "ofac_list")
	assert.Equal(t, int64(1), counter(t, m, "enqueued"),
		"re-enqueue attempts are still counted honestly")
}

// The composite keyset cursor must advance across (network_id,
// wallet_id, source_id) — a wallet with multiple per-source rows loses
// none even at batchSize=1.
func TestSeeder_CompositeKeysetPagination(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	bsc := netByCode(t, pool, "bsc")

	w1 := seedWallet(t, pool, eth.ID, addrW1, "evm")
	w2 := seedWallet(t, pool, bsc.ID, addrW2, "evm")
	// Two sources for one (net,wallet) — same net+wallet prefix.
	seedIndexState(t, pool, bsc.ID, w2, "blockscout", "partial", false)
	seedIndexState(t, pool, bsc.ID, w2, "chainscan", "fresh", false)
	seedIndexState(t, pool, eth.ID, w1, "blockscout", "failed", false)
	seedIndexState(t, pool, eth.ID, w1, "chainscan", "partial", false)
	// Extra sanctions rows force source-1 pagination too (batchSize=1).
	ent := seedSanctionEntity(t, pool, "seed-ent-pages")
	for i := int64(0); i < 3; i++ {
		a := seedAddr(300 + i)
		seedSanctionWallet(t, pool, ent, a, a, &eth.ID)
	}

	h := &logCapture{}
	require.NoError(t, newSeedSeeder(pool, h, 1).Run(context.Background()))

	// Index-state: stale rows for w1 (2 sources) + w2 (1 source) seed;
	// w2's 'fresh' row counts skipped_fresh.
	m := sourceCounters(t, h, "refresh_stale")
	assert.Equal(t, int64(4), counter(t, m, "scanned"),
		"composite cursor must advance across shared (net,wallet) prefixes")
	assert.Equal(t, int64(3), counter(t, m, "enqueued"))
	assert.Equal(t, int64(1), counter(t, m, "skipped_fresh"))
	assert.Equal(t, 5, warmRowCount(t, pool),
		"3 enqueued index-state attempts dedupe to 2 rows + 3 sanctions rows")

	// w1 enqueued once despite two stale source rows (upsert dedupe).
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM warm_queue WHERE address=$1`, addrW1).Scan(&n))
	assert.Equal(t, 1, n)
}

// A disabled queue is a truthful no-op: the scan still runs end-to-end,
// counters record accepted attempts, and zero rows are written.
func TestSeeder_DisabledQueue(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	ent := seedSanctionEntity(t, pool, "seed-ent-off")
	seedSanctionWallet(t, pool, ent, addrW1, addrW1, &eth.ID)

	h := &logCapture{}
	s := NewSeeder(db.New(pool), NewQueue(db.New(pool), false, nil),
		500, slog.New(h))
	require.NoError(t, s.Run(context.Background()))
	assert.Equal(t, 0, warmRowCount(t, pool),
		"disabled queue writes nothing")
	m := sourceCounters(t, h, "ofac_list")
	assert.Equal(t, int64(1), counter(t, m, "enqueued"),
		"attempts are counted even when the queue no-ops them")
}

// batchSize < 1 is rejected by Run (A7): a zero/negative knob can never
// silently no-op the whole scan.
func TestSeeder_BatchSizeValidation(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	for _, bs := range []int{0, -5} {
		s := NewSeeder(db.New(pool), NewQueue(db.New(pool), true, nil),
			bs, nil)
		assert.Error(t, s.Run(context.Background()), "batchSize %d", bs)
	}
}

// A post-check Enqueue failure is infra by construction (D2): the queue
// sits on a dead pool while the scan pool stays live, so the first
// eligible row's upsert errors and the run aborts — never errors++,
// never a partial "success" report.
func TestSeeder_EnqueueInfraAbort(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	ent := seedSanctionEntity(t, pool, "seed-ent-abort")
	seedSanctionWallet(t, pool, ent, addrW1, addrW1, &eth.ID)

	deadPool, err := pgxpool.New(context.Background(), testDSN)
	require.NoError(t, err)
	deadPool.Close()

	h := &logCapture{}
	s := NewSeeder(db.New(pool), NewQueue(db.New(deadPool), true, nil),
		500, slog.New(h))
	require.Error(t, s.Run(context.Background()),
		"Enqueue on a dead pool must abort the run")
	assert.Equal(t, 0, warmRowCount(t, pool))
	for _, m := range h.recs {
		if m["source"] == "ofac_list" {
			assert.Equal(t, "warm seed source aborted", m["_msg"],
				"abort path must not log 'complete'")
		}
	}
}

// ctx death aborts the run — never a row-level error, never a hang.
func TestSeeder_CtxCancelAborts(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	eth := netByCode(t, pool, "ethereum")
	ent := seedSanctionEntity(t, pool, "seed-ent-ctx")
	seedSanctionWallet(t, pool, ent, addrW1, addrW1, &eth.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newSeedSeeder(pool, &logCapture{}, 500)
	assert.Error(t, s.Run(ctx), "dead ctx must abort")
}
