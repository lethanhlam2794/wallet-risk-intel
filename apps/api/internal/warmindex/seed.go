package warmindex

// Warm seed (warm-seed-worker): a one-shot batch seeder that
// proactively fills warm_queue from three DB-resident signal sources —
// live OFAC listings (p20), risky-category labels (p10), and stale
// index coverage (p5) — so the risk graph around known-bad actors is
// warmed before any user query. Ops schedules `worker -mode warmseed`
// via cron; there is no loop. Priorities are pinned strictly below the
// interactive signal floor (PriorityRiskyLabel = 50): seed rows always
// yield to live investigation signals in the priority DESC, id claim
// order — a hard invariant guarded by TestSeedPriorities_BelowSignalFloor.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

const (
	// PrioritySeedOFAC seeds a live OFAC listing (seed:ofac_list).
	PrioritySeedOFAC = 20
	// PrioritySeedRiskyLabel seeds a risky-category label
	// (seed:risky_label) — the same category set tracing uses at p50.
	PrioritySeedRiskyLabel = 10
	// PrioritySeedRefresh seeds stale index coverage
	// (seed:refresh_stale) — cheapest signal, deepest queue position.
	PrioritySeedRefresh = 5

	reasonSeedOFAC    = "seed:ofac_list"
	reasonSeedLabel   = "seed:risky_label"
	reasonSeedRefresh = "seed:refresh_stale"

	// seedBatchTimeout bounds each keyset batch: the SELECT plus that
	// batch's enqueue iteration (A5). The run itself is intentionally
	// unbounded — a cron one-shot wrapped by ops timeouts.
	seedBatchTimeout = 30 * time.Second
)

// Seeder streams the three sources in bounded keyset batches,
// classifies each row, and enqueues eligible ones through the existing
// Queue (upsert dedupe is the ONLY dedup — no run-tracking state, so a
// crash mid-scan replays cleanly). Disabled flag semantics (D1): the
// scan still runs when WARM_INDEX_ENABLED=0 — Queue.Enqueue nil-no-ops
// and the counters stay truthful; the exit-0 gate lives in cmd/worker.
type Seeder struct {
	q         *db.Queries
	queue     *Queue
	batchSize int
	log       *slog.Logger
}

// NewSeeder wires the seeder; nil log discards (NewQueue/NewDrainer
// convention).
func NewSeeder(q *db.Queries, queue *Queue, batchSize int,
	log *slog.Logger) *Seeder {

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Seeder{q: q, queue: queue, batchSize: batchSize, log: log}
}

// seedStats carries the six contract counters for one source (or the
// run total). enqueued counts accepted upsert ATTEMPTS — a deduped
// re-enqueue or a disabled-queue no-op still counts (Enqueue's frozen
// signature returns only error), so a replayed run legitimately logs
// enqueued=N while writing zero new rows. skipped_fresh is only nonzero
// for refresh_stale; every per-source log line emits all six keys.
type seedStats struct {
	scanned            int64
	enqueued           int64
	skippedNonEVM      int64
	skippedInactiveNet int64
	skippedFresh       int64
	errors             int64
}

func (st *seedStats) add(o seedStats) {
	st.scanned += o.scanned
	st.enqueued += o.enqueued
	st.skippedNonEVM += o.skippedNonEVM
	st.skippedInactiveNet += o.skippedInactiveNet
	st.skippedFresh += o.skippedFresh
	st.errors += o.errors
}

func (st seedStats) attrs() []any {
	return []any{
		"scanned", st.scanned,
		"enqueued", st.enqueued,
		"skipped_non_evm", st.skippedNonEVM,
		"skipped_inactive_net", st.skippedInactiveNet,
		"skipped_fresh", st.skippedFresh,
		"errors", st.errors,
	}
}

// Run executes the one-shot scan: OFAC → labels → index-state, a six-key
// summary line per source, then a totals line. batchSize < 1 is
// rejected here (A7) so a bad knob can never silently no-op the run.
func (s *Seeder) Run(ctx context.Context) error {
	if s.batchSize < 1 {
		return fmt.Errorf("warmindex: seed batch size %d < 1", s.batchSize)
	}
	var total seedStats
	for _, src := range []struct {
		name string
		scan func(context.Context) (seedStats, error)
	}{
		{"ofac_list", s.seedSanctions},
		{"risky_label", s.seedLabels},
		{"refresh_stale", s.seedIndexState},
	} {
		st, err := src.scan(ctx)
		total.add(st)
		if err != nil {
			s.log.Warn("warm seed source aborted",
				append([]any{"source", src.name, "error", err},
					st.attrs()...)...)
			return fmt.Errorf("warmindex: seed %s: %w", src.name, err)
		}
		s.log.Info("warm seed source complete",
			append([]any{"source", src.name}, st.attrs()...)...)
	}
	s.log.Info("warm seed complete", total.attrs()...)
	return nil
}

// scanBatches is the shared keyset loop: each batch's SELECT and its
// enqueue iteration run under one bounded seedBatchTimeout child ctx
// (A5). fetch re-reads the caller-held cursor; visit advances it and
// classifies the row. A non-nil visit return or a dead bctx aborts the
// run (D2) — checked BEFORE cancel, never after.
func (s *Seeder) scanBatches(ctx context.Context,
	fetch func(context.Context) ([]seedRow, error),
	visit func(context.Context, seedRow) error) error {

	for {
		bctx, cancel := context.WithTimeout(ctx, seedBatchTimeout)
		batch, err := fetch(bctx)
		var rerr error
		if err != nil {
			rerr = err
		} else {
			for _, row := range batch {
				if rerr = visit(bctx, row); rerr != nil {
					break
				}
			}
		}
		if rerr == nil {
			rerr = bctx.Err()
		}
		cancel()
		if rerr != nil {
			return rerr
		}
		if len(batch) < s.batchSize {
			return nil
		}
	}
}

// seedRow is the uniform shape the gate chain needs across all three
// sqlc row types.
type seedRow struct {
	netID    pgtype.Int8 // NULL → skipped_inactive_net
	active   pgtype.Bool // LEFT JOIN is_active
	ns       pgtype.Text // LEFT JOIN namespace
	addr     string
	stale    bool // source 3 only: coverage gate verdict (unread by S1/S2)
	priority int
	reason   string
}

// enqueueGated applies the shared gate chain — network eligibility →
// EVM shape → canonical pre-check → Queue.Enqueue — folding the outcome
// into st. Classification order is contractual: the net gate runs FIRST
// (a Base58 address on the inactive TRON net counts skipped_inactive_net,
// never skipped_non_evm). A non-nil return is an infra/DB failure that
// aborts the run; row-level rejects only move counters.
func (s *Seeder) enqueueGated(ctx context.Context, st *seedStats,
	source string, r seedRow) error {

	// NULL network_id is NOT "inactive" — an unmapped sanctions listing
	// stays a deliberate gap under the anti-cross-listing invariant; do
	// not "fix" it by guessing a network.
	if !r.netID.Valid || !r.active.Valid || !r.active.Bool ||
		!r.ns.Valid ||
		network.AddressFormatForNamespace(r.ns.String) != network.AddressFormatEVM {
		st.skippedInactiveNet++
		return nil
	}
	if !wallet.IsEVMAddress(r.addr) {
		st.skippedNonEVM++
		return nil
	}
	// Canonical pre-check (D2): the ONLY row-level failure domain.
	// NormalizeEVM is invoked solely on syntactically-EVM input — the
	// IsEVMAddress gate above filtered everything else, so non-EVM
	// strings (TRON Base58) can never reach it.
	if n, err := wallet.NormalizeEVM(r.addr); err != nil || n != r.addr {
		st.errors++
		s.log.Warn("warm seed row rejected (non-canonical address)",
			"source", source, "addr", MaskAddr(r.addr))
		return nil
	}
	// Post-check Enqueue failure is by construction infra/DB (the
	// address is canonical, network_id came from a live JOIN row) →
	// abort the run, never errors++.
	if err := s.queue.Enqueue(ctx, r.netID.Int64, r.addr,
		r.priority, r.reason, nil); err != nil {
		return err
	}
	st.enqueued++
	return nil
}

// seedSanctions scans live OFAC-listed wallets at p20/seed:ofac_list;
// keyset cursor on sanctions_wallets.id.
func (s *Seeder) seedSanctions(ctx context.Context) (seedStats, error) {
	var st seedStats
	var cursor int64
	err := s.scanBatches(ctx,
		func(bctx context.Context) ([]seedRow, error) {
			batch, err := s.q.ListWarmSeedSanctionsBatch(bctx,
				db.ListWarmSeedSanctionsBatchParams{
					CursorID: pgtype.Int8{Int64: cursor, Valid: true},
					Lim:      int64(s.batchSize)})
			if err != nil {
				return nil, err
			}
			rows := make([]seedRow, 0, len(batch))
			for _, b := range batch {
				cursor = b.ID
				rows = append(rows, seedRow{
					netID: b.NetworkID, active: b.NetActive,
					ns: b.NetNamespace, addr: b.NormalizedAddress,
					priority: PrioritySeedOFAC, reason: reasonSeedOFAC})
			}
			return rows, nil
		},
		func(bctx context.Context, r seedRow) error {
			st.scanned++
			return s.enqueueGated(bctx, &st, "ofac_list", r)
		})
	return st, err
}

// seedLabels scans risky-category wallet labels at
// p10/seed:risky_label; keyset cursor on wallet_labels.id. The category
// allowlist is enforced in SQL (EXCHANGE/GAMBLING never scan).
func (s *Seeder) seedLabels(ctx context.Context) (seedStats, error) {
	var st seedStats
	var cursor int64
	err := s.scanBatches(ctx,
		func(bctx context.Context) ([]seedRow, error) {
			batch, err := s.q.ListWarmSeedLabelBatch(bctx,
				db.ListWarmSeedLabelBatchParams{
					CursorID: pgtype.Int8{Int64: cursor, Valid: true},
					Lim:      int64(s.batchSize)})
			if err != nil {
				return nil, err
			}
			rows := make([]seedRow, 0, len(batch))
			for _, b := range batch {
				cursor = b.ID
				rows = append(rows, seedRow{
					netID:  pgtype.Int8{Int64: b.NetworkID, Valid: true},
					active: b.NetActive, ns: b.NetNamespace,
					addr:     b.NormalizedAddress,
					priority: PrioritySeedRiskyLabel,
					reason:   reasonSeedLabel})
			}
			return rows, nil
		},
		func(bctx context.Context, r seedRow) error {
			st.scanned++
			return s.enqueueGated(bctx, &st, "risky_label", r)
		})
	return st, err
}

// seedIndexState scans ALL per-source coverage rows at
// p5/seed:refresh_stale; composite keyset over the (network_id,
// wallet_id, source_id) PK. The coverage check runs FIRST: only stale
// coverage (status partial/failed, or history_truncated — the literal
// OR-logic keeps indexing/disabled+truncated seeding too, a harmless
// wasted row the drain releases at pc=0) may burn drain quota; adequate
// coverage counts skipped_fresh — honest because the SELECT returns
// every row, not just stale ones.
func (s *Seeder) seedIndexState(ctx context.Context) (seedStats, error) {
	var st seedStats
	var cNet, cWallet, cSource int64
	err := s.scanBatches(ctx,
		func(bctx context.Context) ([]seedRow, error) {
			batch, err := s.q.ListWarmSeedIndexStateBatch(bctx,
				db.ListWarmSeedIndexStateBatchParams{
					CursorNet:    pgtype.Int8{Int64: cNet, Valid: true},
					CursorWallet: pgtype.Int8{Int64: cWallet, Valid: true},
					CursorSource: pgtype.Int8{Int64: cSource, Valid: true},
					Lim:          int64(s.batchSize)})
			if err != nil {
				return nil, err
			}
			rows := make([]seedRow, 0, len(batch))
			for _, b := range batch {
				cNet, cWallet, cSource = b.NetworkID, b.WalletID, b.SourceID
				rows = append(rows, seedRow{
					netID:  pgtype.Int8{Int64: b.NetworkID, Valid: true},
					active: b.NetActive, ns: b.NetNamespace,
					addr: b.NormalizedAddress,
					stale: b.Status == "partial" ||
						b.Status == "failed" || b.HistoryTruncated,
					priority: PrioritySeedRefresh,
					reason:   reasonSeedRefresh})
			}
			return rows, nil
		},
		func(bctx context.Context, r seedRow) error {
			st.scanned++
			if !r.stale {
				st.skippedFresh++
				return nil
			}
			return s.enqueueGated(bctx, &st, "refresh_stale", r)
		})
	return st, err
}
