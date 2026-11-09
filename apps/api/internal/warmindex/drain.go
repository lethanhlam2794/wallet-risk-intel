package warmindex

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/transaction"
)

// Drainer is the consumer side: one bounded cycle claims a single pending
// item, picks the first eligible lane, and spends upstream calls under a
// per-item budget and the shared daily ledger.
type Drainer struct {
	q        *db.Queries
	resolver *network.Resolver
	lanes    []ProviderLane // allowlist order — selection iterates it
	checker  transaction.StatusChecker
	cfg      Config
	log      *slog.Logger
	now      func() time.Time
}

// NewDrainer wires the consumer. checker is the shared killswitch read
// (providerconfig.StatusCache); nil means every lane active (env parity).
func NewDrainer(q *db.Queries, resolver *network.Resolver,
	lanes []ProviderLane, checker transaction.StatusChecker,
	cfg Config, log *slog.Logger) *Drainer {

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Drainer{q: q, resolver: resolver, lanes: lanes,
		checker: checker, cfg: cfg, log: log, now: time.Now}
}

// Run starts cfg.Workers concurrent claim loops (one item per worker
// per cycle at PollInterval). Disabled → log + immediate return
// (contract §7). Cycle errors are logged and loops keep ticking —
// transient DB blips must not kill a worker.
func (d *Drainer) Run(ctx context.Context) {
	if !d.cfg.Enabled {
		d.log.Info("warm index disabled; drain loop not started")
		return
	}
	workers := max(d.cfg.Workers, 1)
	d.log.Info("warm drain loop started", "poll_interval", d.cfg.PollInterval,
		"lanes", len(d.lanes), "daily_cap", d.cfg.DailyCap,
		"workers", workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.worker(ctx, i)
		}()
	}
	wg.Wait()
}

// worker is one claim loop. workerIdx feeds lane rotation so different
// workers prefer different providers.
func (d *Drainer) worker(ctx context.Context, workerIdx int) {
	interval := d.cfg.PollInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := d.runOnce(ctx, workerIdx); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.log.Error("warm drain cycle failed", "error", err,
				"worker", workerIdx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce runs one drain cycle: lease sweep → claim one item → lane
// eligibility → EnsureIndexed → outcome settle. workDone reports whether
// an item was claimed this cycle (any terminal outcome — released, done,
// failed, or dead). It is the serial worker-0 path; concurrent drains
// call runOnce with their own worker index.
func (d *Drainer) RunOnce(ctx context.Context) (bool, error) {
	return d.runOnce(ctx, 0)
}

// runOnce is RunOnce parameterized by worker index: only worker 0 runs
// the lease sweep, and workerIdx rotates the preferred lane.
func (d *Drainer) runOnce(ctx context.Context, workerIdx int) (bool, error) {
	// todayUTC is computed once per cycle (D10) — the cap check and the
	// spend write can never straddle midnight.
	today := d.todayUTC()
	if workerIdx == 0 {
		cutoff := pgtype.Timestamptz{
			Time: d.now().Add(-LeaseDuration), Valid: true}
		if n, err := d.q.ResetExpiredWarmClaims(ctx, db.ResetExpiredWarmClaimsParams{
			ClaimedAt: cutoff, Attempts: MaxAttempts}); err != nil {
			return false, err
		} else if n > 0 {
			d.log.Info("warm lease sweep reset crashed claims", "reset", n)
		}
	}

	item, err := d.q.ClaimNextWarmItem(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	net, ok := d.networkFor(item.NetworkID)
	if !ok {
		// Permanently unprocessable — no provider could ever serve it.
		if err := d.q.KillWarmItem(ctx, item.ID); err != nil {
			return true, err
		}
		d.log.Error("warm item network unresolvable; marked dead",
			"network_id", item.NetworkID, "addr", MaskAddr(item.Address))
		return true, nil
	}

	lane, err := d.selectLane(ctx, net, today, workerIdx)
	if err != nil {
		// Best-effort release — a stranded claim would wait out the
		// lease and the sweep would burn an unearned attempt (D3: an
		// item that was never attempted must never die).
		rctx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), 5*time.Second)
		_ = d.q.ReleaseWarmClaim(rctx, item.ID)
		cancel()
		return true, err
	}
	if lane == nil {
		if err := d.q.ReleaseWarmClaim(ctx, item.ID); err != nil {
			return true, err
		}
		d.log.Info("no warm lane under cap/active",
			"network", net.Code, "addr", MaskAddr(item.Address),
			"attempts", item.Attempts)
		return true, nil
	}

	// Refresh is mandatory (D3a): the input is by construction a
	// just-indexed partial+fresh row — without it the freshness gate
	// serves the row and nothing deepens. The FAILED cooldown still holds.
	dctx, cancel := context.WithTimeout(ctx, DrainTimeout)
	out, err := lane.Tx.EnsureIndexed(dctx, net, item.Address,
		transaction.EnsureOptions{
			Refresh:  true,
			Budget:   transaction.NewBudget(int64(d.cfg.ItemBudget)),
			MaxPages: d.cfg.MaxPages,
		})
	cancel()
	return d.settle(ctx, item, lane, net, today, out, err)
}

// settle applies the D3 outcome decision table. pc = ProviderCalls is the
// honest "attempted upstream call" discriminator — a pc=0 outcome was
// served/cooldowned/disabled/budget-blocked, never attempted, so it
// releases without an attempt bump and without ledger spend.
func (d *Drainer) settle(ctx context.Context, item db.WarmQueue,
	lane *ProviderLane, net network.Network, today pgtype.Date,
	out *transaction.IndexOutcome, err error) (bool, error) {

	switch {
	case err != nil:
		if ctx.Err() != nil {
			// Shutdown mid-drain: release the claim under a detached
			// ctx — a release under the dead ctx would strand it for
			// a whole lease. Never an attempt bump.
			rctx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), 5*time.Second)
			_ = d.q.ReleaseWarmClaim(rctx, item.ID)
			cancel()
			return true, err
		}
		return d.failAttempt(ctx, item, lane, net, "infra", err)
	case out == nil || out.ProviderCalls == 0:
		// Served fresh/partial, cooldown-served failed, disabled, live
		// 'indexing' claim, BudgetExhausted, not_indexed — never attempted.
		if rerr := d.q.ReleaseWarmClaim(ctx, item.ID); rerr != nil {
			return true, rerr
		}
		return true, nil
	default:
		// Calls were spent — ledger first (they're gone regardless of
		// the outcome write; an under-count on crash between the two
		// writes is bounded by the item budget — accepted v1).
		if lerr := d.addLedger(ctx, lane.Code, today, out.ProviderCalls); lerr != nil {
			return true, lerr
		}
		switch out.IndexStatus {
		case transaction.StatusFresh, transaction.StatusPartial:
			if cerr := d.q.CompleteWarmItem(ctx, item.ID); cerr != nil {
				return true, cerr
			}
			return true, nil
		case transaction.StatusFailed:
			return d.failAttempt(ctx, item, lane, net, "fetch",
				out.FetchErr)
		default:
			// Call spent, no terminal coverage (e.g. node-lag
			// head<fetchFrom release) — counts as a failed attempt.
			return d.failAttempt(ctx, item, lane, net, "fetch",
				out.FetchErr, slog.String("index_status", out.IndexStatus))
		}
	}
}

// failAttempt records one attempted-but-failed drain: attempts+1 (dead at
// MaxAttempts) with the failure logged — never a 'done'.
func (d *Drainer) failAttempt(ctx context.Context, item db.WarmQueue,
	lane *ProviderLane, net network.Network, kind string, cause error,
	extra ...slog.Attr) (bool, error) {

	if err := d.q.FailWarmItem(ctx, db.FailWarmItemParams{
		ID: item.ID, Attempts: MaxAttempts}); err != nil {
		return true, err
	}
	args := []any{
		"provider_code", lane.Code,
		"network", net.Code,
		"addr", MaskAddr(item.Address),
		"attempts", item.Attempts + 1,
		"kind", kind,
	}
	for _, a := range extra {
		args = append(args, a.Key, a.Value.Any())
	}
	if cause != nil {
		args = append(args, "error", cause)
	}
	d.log.Warn("warm drain failed", args...)
	return true, nil
}

// selectLane picks the first eligible provider that is enabled for the
// network, killswitch-active, and under today's cap. Iteration starts at
// lanes[start%len] so concurrent workers prefer different providers —
// worker k's order is lanes[k], lanes[k+1], … wrapping — while a single
// worker keeps the historical allowlist order.
func (d *Drainer) selectLane(ctx context.Context, net network.Network,
	today pgtype.Date, start int) (*ProviderLane, error) {

	for j := range d.lanes {
		lane := &d.lanes[(start+j)%len(d.lanes)]
		if !lane.Adapter.Enabled(net) {
			continue
		}
		if d.checker != nil && !d.checker.Active(ctx, lane.Code) {
			continue
		}
		calls, err := d.ledgerCalls(ctx, lane.Code, today)
		if err != nil {
			return nil, err
		}
		if calls >= int64(d.cfg.DailyCap) {
			continue
		}
		return lane, nil
	}
	return nil, nil
}

func (d *Drainer) ledgerCalls(ctx context.Context, code string,
	today pgtype.Date) (int64, error) {
	calls, err := d.q.GetWarmQuotaCalls(ctx, db.GetWarmQuotaCallsParams{
		ProviderCode: code, PeriodStart: today})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int64(calls), nil
}

func (d *Drainer) addLedger(ctx context.Context, code string,
	today pgtype.Date, calls int64) error {
	if calls <= 0 {
		return nil
	}
	return d.q.AddWarmQuotaCalls(ctx, db.AddWarmQuotaCallsParams{
		ProviderCode: code, PeriodStart: today, Calls: int32(calls)})
}

func (d *Drainer) networkFor(id int64) (network.Network, bool) {
	for _, n := range d.resolver.List() {
		if n.ID == id {
			return n, true
		}
	}
	return network.Network{}, false
}

// todayUTC is the ledger period key — the current UTC calendar day.
func (d *Drainer) todayUTC() pgtype.Date {
	n := d.now().UTC()
	return pgtype.Date{Time: time.Date(n.Year(), n.Month(), n.Day(),
		0, 0, 0, 0, time.UTC), Valid: true}
}
