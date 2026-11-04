package transaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/wallet"
)

// Config carries the indexing knobs (cmd/api wiring).
type Config struct {
	FreshTTL     time.Duration    // IndexingFreshTTL — fresh-serve horizon
	StaleAfter   time.Duration    // IndexingStaleAfter — stale-claim horizon
	NegativeTTL  time.Duration    // FAILED cooldown (EnrichmentNegativeTTL)
	PageSize     int              // rows per stream page
	MaxPagesHop0 int              // stream-pair steps, queried wallet
	MaxPagesHopN int              // stream-pair steps, frontier-2 nodes
	MaxPagesHop3 int              // stream-pair steps, frontier ≥3 nodes
	ReorgTail    map[string]int64 // net.Code → tail overlap blocks
	WindowCap    map[string]int64 // net.Code → max span (0 = unbounded)
	Budget       map[string]int   // adapter name → max_provider_requests
}

func (c *Config) defaults() {
	if c.FreshTTL <= 0 {
		c.FreshTTL = 10 * time.Minute
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = 15 * time.Minute
	}
	if c.NegativeTTL <= 0 {
		c.NegativeTTL = time.Minute
	}
	if c.PageSize <= 0 {
		c.PageSize = 1000
	}
	if c.MaxPagesHop0 <= 0 {
		c.MaxPagesHop0 = 5
	}
	if c.MaxPagesHopN <= 0 {
		c.MaxPagesHopN = 2
	}
	if c.MaxPagesHop3 <= 0 {
		c.MaxPagesHop3 = 1
	}
}

// failoverKinds is the closed set of provider failure kinds that justify
// walking to the next chain member (phase-12 D3/D4). Everything else —
// empty page, client_4xx, malformed, timeout, internal — is terminal for
// the request; disabled members are skipped before failing.
var failoverKinds = map[provider.ErrKind]bool{
	provider.ErrRateLimited: true,
	provider.ErrUpstream5xx: true,
	provider.ErrDegraded:    true,
	provider.ErrTransport:   true,
	provider.ErrCircuitOpen: true,
}

func isFailover(err error) bool {
	var le *provider.LookupError
	if !errors.As(err, &le) {
		return false
	}
	return failoverKinds[le.Kind]
}

// Service owns lazy indexing and indexed-read for one network registry.
type Service struct {
	q        *db.Queries
	pool     *pgxpool.Pool
	wallets  *wallet.Service
	adapters map[string][]provider.TxProvider // net.Code → ordered failover chain
	cfg      Config
	sf       singleflight.Group
	log      *slog.Logger
	now      func() time.Time

	mu        sync.Mutex
	sourceIDs map[string]int64 // adapter Name() → data_sources.id (lazy)
}

// New builds the service; adapters maps network code → the ordered
// failover chain registered for it (a one-entry chain is the pre-phase-12
// single-adapter shape; conflicts are a wiring error caught in main).
func New(pool *pgxpool.Pool, q *db.Queries, adapters map[string][]provider.TxProvider,
	wallets *wallet.Service, cfg Config, log *slog.Logger) *Service {

	cfg.defaults()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		pool: pool, q: q, wallets: wallets,
		adapters: adapters, cfg: cfg, log: log,
		now: time.Now, sourceIDs: map[string]int64{},
	}
}

// BudgetFor returns the request-global provider pool limit for the
// network — the max over the resolved chain's configured budgets
// (phase-12 D2); 0 = unlimited.
func (s *Service) BudgetFor(net network.Network) int {
	max := 0
	for _, a := range s.adapters[net.Code] {
		if b := s.cfg.Budget[a.Name()]; b > max {
			max = b
		}
	}
	return max
}

// MaxPagesFor returns the stream-pair step bound for a frontier index:
// 0/root → the hop-0 tier (and the MaxPages≤0 fallback), 2 → HopN,
// ≥3 → the shared deeper tier Hop3.
func (s *Service) MaxPagesFor(frontierIdx int) int {
	switch {
	case frontierIdx == 2:
		return s.cfg.MaxPagesHopN
	case frontierIdx >= 3:
		return s.cfg.MaxPagesHop3
	default:
		return s.cfg.MaxPagesHop0
	}
}

// sourceID resolves (and caches) data_sources.id for an adapter name.
func (s *Service) sourceID(ctx context.Context, name string) (int64, error) {
	s.mu.Lock()
	id, ok := s.sourceIDs[name]
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	src, err := s.q.GetSourceByCode(ctx, name)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.sourceIDs[name] = src.ID
	s.mu.Unlock()
	return src.ID, nil
}

// EnsureIndexed brings the wallet's index forward under the
// singleflight/freshness/cooldown/disabled gates, then reports the
// terminal index state — plan §3.2.
func (s *Service) EnsureIndexed(ctx context.Context, net network.Network,
	normalized string, opts EnsureOptions) (*IndexOutcome, error) {

	w, err := s.wallets.GetOrCreate(ctx, net.ID, normalized, normalized)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%d:%d", net.ID, w.ID)
	v, err, _ := s.sf.Do(key, func() (any, error) {
		return s.ensure(ctx, net, normalized, w.ID, opts)
	})
	if err != nil {
		return nil, err
	}
	return v.(*IndexOutcome), nil
}

type ixState struct {
	exists bool
	row    db.WalletIndexState
}

func (s *Service) loadState(ctx context.Context, netID, walletID int64,
	srcID int64) (*ixState, error) {
	row, err := s.q.GetWalletIndexState(ctx, db.GetWalletIndexStateParams{
		NetworkID: netID, WalletID: walletID, SourceID: srcID})
	if errors.Is(err, pgx.ErrNoRows) {
		return &ixState{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &ixState{exists: true, row: row}, nil
}

func (s *Service) ensure(ctx context.Context, net network.Network,
	normalized string, walletID int64, opts EnsureOptions) (*IndexOutcome, error) {

	now := s.now()
	chain := s.adapters[net.Code]

	firstState := &ixState{}
	var firstSrc int64
	disabledSkips := 0
	var lastOutcome *IndexOutcome
	var totalCalls int64

	for i, adapter := range chain {
		var srcID int64
		if adapter != nil {
			id, err := s.sourceID(ctx, adapter.Name())
			if err != nil {
				// Resolution failure is infrastructure error, not a
				// skippable source — silent skip would mask a DB outage
				// behind a stale verdict.
				return nil, fmt.Errorf("source lookup %q: %w", adapter.Name(), err)
			}
			srcID = id
		}
		st := &ixState{}
		if srcID != 0 {
			loaded, err := s.loadState(ctx, net.ID, walletID, srcID)
			if err != nil {
				return nil, err
			}
			st = loaded
		}
		if i == 0 {
			firstState, firstSrc = st, srcID
		}
		status := StatusNotIndexed
		if st.exists {
			status = st.row.Status
		}
		fresh := st.exists && st.row.IndexedAt.Valid &&
			now.Sub(st.row.IndexedAt.Time) <= s.cfg.FreshTTL
		cooldown := st.exists && st.row.IndexedAt.Valid &&
			now.Sub(st.row.IndexedAt.Time) <= s.cfg.NegativeTTL
		serve := func() *IndexOutcome {
			out := outcomeFromState(st, s.cfg.FreshTTL, now)
			out.WalletID = walletID
			return out
		}
		// Per-source gate order preserved (design D3): freshness serves,
		// disabled/cooldown skip without traffic, live claims serve.
		switch {
		case (status == StatusFresh || status == StatusPartial) && fresh && !opts.Refresh:
			return serve(), nil
		case adapter == nil || !adapter.Enabled(net):
			disabledSkips++
			continue
		case status == StatusFailed && cooldown:
			continue
		case status == StatusIndexing && st.row.UpdatedAt.Valid &&
			now.Sub(st.row.UpdatedAt.Time) <= s.cfg.StaleAfter:
			return serve(), nil
		case srcID == 0:
			continue // no provenance row to persist under — skip the attempt
		}

		out, failover, err := s.attemptFetch(ctx, net, normalized, walletID,
			adapter, srcID, st, now, opts, i < len(chain)-1, totalCalls)
		if err != nil {
			return nil, err
		}
		totalCalls = out.ProviderCalls
		if !failover {
			return out, nil
		}
		lastOutcome = out
	}

	if lastOutcome != nil {
		// Every attempted adapter failed — the last FAILED row is the
		// freshest verdict (D3); per-attempt failures persist per source.
		return lastOutcome, nil
	}
	if len(chain) == 0 || disabledSkips == len(chain) {
		// All-disabled terminal: written under chain[0]'s configured
		// source (D5) — the selector's verdict, not a phantom row.
		return s.writeDisabled(ctx, net.ID, walletID, firstSrc, firstState, now)
	}
	// No adapter attempted and none all-disabled — chain[0]'s persisted
	// row carries the verdict (primary cooling down, rest unusable).
	out := outcomeFromState(firstState, s.cfg.FreshTTL, now)
	out.WalletID = walletID
	return out, nil
}

// attemptFetch runs one chain member's full fetch cycle against its own
// per-source state row: head → window → index-claim → stream-pair
// pagination → batch upsert → terminal write. It returns failover=true
// exactly when the attempt ended on a failover-eligible ErrKind AND a
// later chain member exists (moreMembers): that attempt's accumulated
// transfers are DISCARDED (never mixed into the next attempt's batch —
// D3) and its row records FAILED with its prior watermark kept. On any
// other outcome the returned *IndexOutcome is terminal for the request.
// callsBase accumulates prior attempts' upstream calls so ProviderCalls
// always reports request-total real outbound calls.
func (s *Service) attemptFetch(ctx context.Context, net network.Network,
	normalized string, walletID int64, adapter provider.TxProvider,
	srcID int64, st *ixState, now time.Time, opts EnsureOptions,
	moreMembers bool, callsBase int64) (*IndexOutcome, bool, error) {

	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = s.cfg.MaxPagesHop0
	}
	out := &IndexOutcome{WalletID: walletID, IndexScope: IndexScope,
		ProviderCalls: callsBase}
	// Per-provider cap (D2): this adapter may spend at most its own
	// configured budget within the request, on top of the shared pool.
	providerCap := int64(s.cfg.Budget[adapter.Name()])
	var attemptCalls int64
	prepay := func() bool {
		if providerCap > 0 && attemptCalls >= int64(providerCap) {
			return false
		}
		return opts.Budget.tryTake(1)
	}
	settle := func(calls int64) {
		attemptCalls += calls
		if calls > 1 {
			opts.Budget.charge(calls - 1)
		}
	}
	if opts.Budget.Exhausted() {
		// No request left for even the head call — serve persisted facts;
		// the budget flag lets BFS count this as never-attempted. The
		// read-only check runs BEFORE the index claim so a drained pool
		// never leaves an 'indexing' row behind.
		fillFromState(out, st, s.cfg.FreshTTL, now)
		out.WalletID = walletID
		out.BudgetExhausted = true
		return out, false, nil
	}
	// Claim the index work before spending: the 'indexing' row is the
	// cross-process guard for the whole attempt (phase-12 D3).
	if err := s.q.MarkWalletIndexing(ctx, db.MarkWalletIndexingParams{
		NetworkID: net.ID, WalletID: walletID, SourceID: srcID}); err != nil {
		return nil, false, err
	}
	if !prepay() {
		// Raced drain between the exhausted check and the prepay —
		// release the claim (revert to pre-claim content) then report
		// never-attempted.
		if uerr := s.releaseClaim(ctx, net.ID, walletID, srcID, st); uerr != nil {
			return nil, false, uerr
		}
		fillFromState(out, st, s.cfg.FreshTTL, now)
		out.WalletID = walletID
		out.BudgetExhausted = true
		return out, false, nil
	}
	head, err := adapter.HeadBlock(ctx, net)
	settle(1)
	out.ProviderCalls++
	if err != nil {
		kind := errKind(err)
		out.FetchErr = err
		res, werr := s.writeTerminal(ctx, net.ID, walletID, srcID, st, now,
			StatusFailed, kind, nil, 0, RowsSkipped{}, out)
		if werr != nil {
			return nil, false, werr
		}
		return res, moreMembers && isFailover(err), nil
	}
	out.HeadBlock = &head
	out.Fetched = true

	var watermark int64 = -1
	if st.exists && st.row.IndexedUntilBlock.Valid {
		watermark = st.row.IndexedUntilBlock.Int64
	}
	tail := s.cfg.ReorgTail[net.Code]
	fetchFrom := watermark - tail + 1
	if fetchFrom < 0 {
		fetchFrom = 0
	}
	if head < fetchFrom {
		// Node lag or >tail reorg: no transfer call is honest — serve
		// persisted facts; head_block on the wire evidences the
		// condition. The index claim is released — nothing was indexed.
		if uerr := s.releaseClaim(ctx, net.ID, walletID, srcID, st); uerr != nil {
			return nil, false, uerr
		}
		fillFromState(out, st, s.cfg.FreshTTL, now)
		out.ProviderCalls = callsBase + 1
		return out, false, nil
	}
	fetchTo := head
	if cap := s.cfg.WindowCap[net.Code]; cap > 0 && fetchFrom+cap < fetchTo {
		fetchTo = fetchFrom + cap
	}

	// Paginate stream-pair steps until done, bound, or typed failure.
	var transfers []provider.Transfer
	finishedStreams := map[string]bool{}
	streamSeen := map[string]bool{}
	streamMaxBlock := map[string]int64{}
	var maxBlock int64 = -1
	token := ""
	steps := 0
	var fetchErr error
	budgetStop := false
	for steps < maxPages {
		if !prepay() {
			budgetStop = true
			break
		}
		page, err := adapter.LookupTransfers(ctx, net, normalized,
			provider.TransferLookupOpts{
				FromBlock: fetchFrom, ToBlock: fetchTo,
				Kinds:     []string{provider.TransferKindNative, provider.TransferKindToken},
				PageToken: token, PageSize: s.cfg.PageSize,
				HeadBlock: head,
			})
		if err != nil {
			fetchErr = err
			break
		}
		steps++
		settle(int64(page.UpstreamCalls))
		out.ProviderCalls += int64(page.UpstreamCalls) // real upstream calls
		transfers = append(transfers, page.Transfers...)
		for _, ss := range page.Streams {
			streamSeen[ss.Name] = true
			if ss.Finished {
				finishedStreams[ss.Name] = true
			}
		}
		for _, t := range page.Transfers {
			if t.BlockNumber > maxBlock {
				maxBlock = t.BlockNumber
			}
			if t.Stream != "" && t.BlockNumber > streamMaxBlock[t.Stream] {
				streamMaxBlock[t.Stream] = t.BlockNumber
			}
		}
		token = page.NextPageToken
		if token == "" {
			break
		}
	}

	// Failover-eligible mid-flight failure with a later member waiting:
	// discard this attempt's accumulated batch entirely (never upserted,
	// never mixed — D3) and write FAILED with the prior watermark kept.
	if fetchErr != nil && moreMembers && isFailover(fetchErr) {
		out.FetchErr = fetchErr
		res, werr := s.writeTerminal(ctx, net.ID, walletID, srcID, st, now,
			StatusFailed, errKind(fetchErr), nil, 0, RowsSkipped{}, out)
		if werr != nil {
			return nil, false, werr
		}
		return res, true, nil
	}

	// Upsert the assembled batch first — the watermark applies only after
	// commit (crash-safe ordering).
	rowsJSON, skipped, rowCount, err := s.buildRows(ctx, net, transfers)
	if err != nil {
		return nil, false, err
	}
	if len(rowsJSON) > 2 { // "[]"
		if err := s.q.UpsertTransactions(ctx, db.UpsertTransactionsParams{
			NetworkID: net.ID, Rows: rowsJSON}); err != nil {
			return nil, false, err
		}
	}

	// Min over per-stream contiguous coverage (plan §3.2): a finished
	// stream is complete-to-fetch_to; a truncated stream covers through
	// boundary_block−1; an untouched stream contributes fetch_from−1.
	var newWatermark int64
	switch {
	case len(streamSeen) == 0:
		// Stub/implicit stream: a done token after real steps ⇒ complete
		// window. A zero-page stop (budget before page 1) proves nothing —
		// coverage falls back to the old watermark (review r2-N1).
		if token == "" && fetchErr == nil && !budgetStop {
			newWatermark = fetchTo
		} else if maxBlock > 0 {
			newWatermark = maxBlock - 1
		} else {
			newWatermark = fetchFrom - 1
		}
	default:
		// Per-stream contiguous coverage: an unfinished stream only
		// proves coverage through ITS OWN max block − 1 — never the
		// global max, or a fast stream's boundary would over-claim a
		// slow stream's (review B1).
		newWatermark = fetchTo
		for name := range streamSeen {
			if finishedStreams[name] {
				continue
			}
			cov := fetchFrom - 1
			if mb, ok := streamMaxBlock[name]; ok && mb > 0 {
				cov = mb - 1
			}
			if cov < newWatermark {
				newWatermark = cov
			}
		}
	}
	if newWatermark < watermark {
		newWatermark = watermark // never regress on a bounded refetch
	}

	var wmPtr *int64
	if newWatermark >= 0 {
		v := newWatermark
		wmPtr = &v
	}

	// A window ending with a non-empty token or an unfinished stream is
	// not complete — 'fresh' is earned only when every declared stream
	// finished (a capped stream drops its cursor yet stays unfinished).
	allFinished := true
	for name := range streamSeen {
		if !finishedStreams[name] {
			allFinished = false
		}
	}
	var res *IndexOutcome
	switch {
	case fetchErr != nil:
		out.FetchErr = fetchErr
		res, err = s.writeTerminal(ctx, net.ID, walletID, srcID, st, now,
			StatusFailed, errKind(fetchErr), wmPtr,
			rowCount, skipped, out)
	case token == "" && fetchTo == head && !budgetStop && allFinished:
		res, err = s.writeTerminal(ctx, net.ID, walletID, srcID, st, now,
			StatusFresh, "", wmPtr, rowCount, skipped, out)
	default:
		// Stopped on a bound (max_pages, budget, window cap) — partial and
		// history_truncated are written together (plan §3.4).
		res, err = s.writeTerminal(ctx, net.ID, walletID, srcID, st, now,
			StatusPartial, "", wmPtr, rowCount, skipped, out)
	}
	if err != nil {
		return nil, false, err
	}
	// Non-failover-kind fetch failures are terminal for the request even
	// when later members exist; so are failures on the last member.
	return res, fetchErr != nil && moreMembers && isFailover(fetchErr), nil
}

// errKind folds an adapter error to its persisted ErrKind vocabulary.
func errKind(err error) string {
	var le *provider.LookupError
	if errors.As(err, &le) {
		return string(le.Kind)
	}
	return string(provider.ErrInternal)
}

// releaseClaim reverts an 'indexing' claim to the row's pre-claim
// content. MarkWalletIndexing only touches status/error/updated_at, but
// the generic upsert assigns every column unconditionally — so a plain
// not_indexed write would wipe indexed_until_block/history_truncated,
// the per-source coverage this re-keyed row exists to keep. The revert
// carries st.row verbatim; a claim-created row (!st.exists) reverts to
// an honest empty not_indexed row, and a stale 'indexing' predecessor
// releases as not_indexed (a claim is not coverage).
func (s *Service) releaseClaim(ctx context.Context, netID, walletID,
	srcID int64, st *ixState) error {

	p := db.UpsertWalletIndexStateParams{
		NetworkID: netID, WalletID: walletID, SourceID: srcID,
		Status: StatusNotIndexed,
	}
	if st.exists {
		p.IndexedUntilBlock = st.row.IndexedUntilBlock
		p.IndexedAt = st.row.IndexedAt
		p.Error = st.row.Error
		p.HistoryTruncated = st.row.HistoryTruncated
		p.Status = st.row.Status
		if p.Status == StatusIndexing {
			p.Status = StatusNotIndexed
		}
	}
	_, err := s.q.UpsertWalletIndexState(ctx, p)
	return err
}

// writeDisabled records the disabled terminal (error='disabled' — an
// ErrKind, never an unearned FAILED cooldown).
func (s *Service) writeDisabled(ctx context.Context, netID, walletID int64,
	srcID int64, st *ixState, now time.Time) (*IndexOutcome, error) {

	if srcID == 0 {
		// No resolvable source — post-rekey rows require provenance;
		// report the disabled verdict without persisting a row.
		out := outcomeFromState(st, s.cfg.FreshTTL, now)
		out.WalletID = walletID
		out.IndexStatus = StatusDisabled
		ps := string(provider.ErrDisabled)
		out.ProviderStatus = &ps
		return out, nil
	}
	var wm pgtype.Int8
	if st.exists && st.row.IndexedUntilBlock.Valid {
		wm = st.row.IndexedUntilBlock
	}
	row, err := s.q.UpsertWalletIndexState(ctx, db.UpsertWalletIndexStateParams{
		NetworkID: netID, WalletID: walletID, SourceID: srcID,
		IndexedUntilBlock: wm,
		IndexedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		Status:            StatusDisabled,
		Error:             pgtype.Text{String: string(provider.ErrDisabled), Valid: true},
		HistoryTruncated:  st.exists && st.row.HistoryTruncated,
	})
	if err != nil {
		return nil, err
	}
	out := outcomeFromState(&ixState{exists: true, row: row}, s.cfg.FreshTTL, now)
	out.WalletID = walletID
	return out, nil
}

// writeTerminal persists one terminal state and reports it.
func (s *Service) writeTerminal(ctx context.Context, netID, walletID int64,
	srcID int64, st *ixState, now time.Time,
	status, errKind string, watermark *int64,
	rowsIndexed int64, skipped RowsSkipped, out *IndexOutcome) (*IndexOutcome, error) {

	var wm pgtype.Int8
	if watermark != nil {
		wm = pgtype.Int8{Int64: *watermark, Valid: true}
	} else if st.exists && st.row.IndexedUntilBlock.Valid {
		wm = st.row.IndexedUntilBlock
	}
	var et pgtype.Text
	if errKind != "" {
		et = pgtype.Text{String: errKind, Valid: true}
	}
	truncated := status == StatusPartial
	if status == StatusFailed && st.exists {
		truncated = st.row.HistoryTruncated // preserve last-known bound truth
	}
	row, err := s.q.UpsertWalletIndexState(ctx, db.UpsertWalletIndexStateParams{
		NetworkID: netID, WalletID: walletID, SourceID: srcID,
		IndexedUntilBlock: wm,
		IndexedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		Status:            status, Error: et,
		RowsIndexed:                rowsIndexed,
		RowsSkippedMissingContract: skipped.MissingContract,
		RowsSkippedInvalidAddress:  skipped.InvalidAddress,
		HistoryTruncated:           truncated,
	})
	if err != nil {
		return nil, err
	}
	res := outcomeFromState(&ixState{exists: true, row: row}, s.cfg.FreshTTL, now)
	res.WalletID = walletID
	res.IndexScope = out.IndexScope
	res.Fetched = out.Fetched
	res.ProviderCalls = out.ProviderCalls
	res.HeadBlock = out.HeadBlock
	res.FetchErr = out.FetchErr
	return res, nil
}

// outcomeFromState maps a persisted state row to the wire outcome.
func outcomeFromState(st *ixState, freshTTL time.Duration, now time.Time) *IndexOutcome {
	out := &IndexOutcome{IndexScope: IndexScope}
	fillFromState(out, st, freshTTL, now)
	return out
}

func fillFromState(out *IndexOutcome, st *ixState, freshTTL time.Duration, now time.Time) {
	if !st.exists {
		out.IndexStatus = StatusNotIndexed
		out.Stale = true // NULL indexed_at ⇒ stale
		return
	}
	r := st.row
	out.IndexStatus = r.Status
	if r.IndexedUntilBlock.Valid {
		v := r.IndexedUntilBlock.Int64
		out.IndexedUntilBlock = &v
	}
	if r.IndexedAt.Valid {
		t := r.IndexedAt.Time.UTC()
		out.IndexedAt = &t
		out.Stale = now.Sub(t) > freshTTL
	} else {
		out.Stale = true
	}
	out.HistoryTruncated = r.HistoryTruncated
	out.RowsIndexed = r.RowsIndexed
	out.RowsSkipped = RowsSkipped{
		MissingContract: r.RowsSkippedMissingContract,
		InvalidAddress:  r.RowsSkippedInvalidAddress,
	}
	out.ProviderStatus = providerStatus(r)
}

// providerStatus implements the total wire rule (plan §3.4): persisted
// error when non-empty (its ErrKind), "ok" only after a succeeded attempt
// (fresh|partial, empty error), null otherwise — never a false ok.
func providerStatus(r db.WalletIndexState) *string {
	if r.Error.Valid && r.Error.String != "" {
		s := r.Error.String
		return &s
	}
	if r.Status == StatusFresh || r.Status == StatusPartial {
		s := "ok"
		return &s
	}
	return nil
}
