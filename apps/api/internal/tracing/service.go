package tracing

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/transaction"
)

// Config carries the tracing bounds.
type Config struct {
	MaxNodes         int           // legacy depth-2 cap — fallback for MaxNodesPerDepth[0] (50)
	MaxNodesPerDepth [4]int        // per-frontier caps, index d−2 (50/25/12/6)
	MaxEdges         int           // request-global edge-scan cap (5000)
	Deadline         time.Duration // request deadline (60s)
	ResponseCap      int           // response bytes cap (256 KiB)
	Concurrency      int           // expansion concurrency, shared across depth loops (4)
	MaxPathsPair     int           // paths kept per counterparty pair (3)
}

func (c *Config) defaults() {
	if c.MaxNodesPerDepth[0] <= 0 {
		c.MaxNodesPerDepth[0] = c.MaxNodes
	}
	if c.MaxNodesPerDepth[0] <= 0 {
		c.MaxNodesPerDepth[0] = 50
	}
	if c.MaxNodesPerDepth[1] <= 0 {
		c.MaxNodesPerDepth[1] = 25
	}
	if c.MaxNodesPerDepth[2] <= 0 {
		c.MaxNodesPerDepth[2] = 12
	}
	if c.MaxNodesPerDepth[3] <= 0 {
		c.MaxNodesPerDepth[3] = 6
	}
	if c.MaxEdges <= 0 {
		c.MaxEdges = 5000
	}
	if c.Deadline <= 0 {
		c.Deadline = 60 * time.Second
	}
	if c.ResponseCap <= 0 {
		c.ResponseCap = 256 << 10
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.MaxPathsPair <= 0 {
		c.MaxPathsPair = 3
	}
}

// sentinel addrs never expand but still emit matched items.
var sentinels = map[string]bool{
	"0x0000000000000000000000000000000000000000": true,
	"0x000000000000000000000000000000000000dead": true,
}

// Service owns bounded BFS exposure tracing.
type Service struct {
	q         *db.Queries
	tx        *transaction.Service
	sanctions *sanctions.Service
	resolver  *network.Resolver
	cfg       Config
	metrics   interface {
		IncTracingNodesExamined(int64)
	}
}

// New builds the tracing service; resolver scopes cross-network label
// correlation to active networks.
func New(q *db.Queries, tx *transaction.Service, sanctions *sanctions.Service,
	resolver *network.Resolver, cfg Config) *Service {
	cfg.defaults()
	return &Service{q: q, tx: tx, sanctions: sanctions, resolver: resolver, cfg: cfg}
}

// SetMetrics attaches the metrics collector (optional).
func (s *Service) SetMetrics(m interface {
	IncTracingNodesExamined(int64)
}) {
	s.metrics = m
}

// candidate is one addr in a level's candidate book (cpAgg in plan
// §2.2): the item + path-evidence store. Sentinel far endpoints stay
// candidates (they may emit items) but never consume a position.
type candidate struct {
	addr     string
	sentinel bool
	rank     int // index in the level's ordered candidate list
	parents  map[string][]db.ListWalletEdgesRow
}

// candSet is one level's candidate book — cpAgg_d. `order` holds every
// candidate (incl. sentinels) in §2.3 ordering-key order; `positions`
// is the expansion subset — frontier_d.
type candSet struct {
	cands     map[string]*candidate
	order     []string
	positions []string
	posIdx    map[string]int
}

// nodeResult is one scheduled position's expansion outcome.
type nodeResult struct {
	pos    int
	addr   string
	out    *transaction.IndexOutcome
	edges  []db.ListWalletEdgesRow
	failed bool
	done   bool
}

// levelState records a scheduled frontier's expansion state.
type levelState struct {
	results  []nodeResult
	emitted  int // contiguous resolved prefix length
	examined int // attempted positions
}

// Exposures runs the bounded BFS — generalized per-depth pipeline
// (phase-7 §2): hop-0 index → hop-1 edge extraction → per-frontier
// ordered-position expansion (d = 2..max_hops) → contiguous-prefix
// assembly → category filter → order → cuts. The deadline bounds the
// BFS work only; evidence lookups and assembly run under the caller
// ctx so a bounded trace still returns an honest truncated result
// (caller cancellation still propagates as an error).
func (s *Service) Exposures(ctx context.Context, net network.Network,
	root string, req Request) (*Result, error) {

	bctx, cancel := context.WithTimeout(ctx, s.cfg.Deadline)
	defer cancel()

	budget := transaction.NewBudget(int64(s.tx.BudgetFor(net)))
	rootOutcome, err := s.tx.EnsureIndexed(bctx, net, root, transaction.EnsureOptions{
		Refresh: req.Refresh, Budget: budget,
	})
	if err != nil {
		return nil, err
	}

	var edgesLeft atomic.Int64
	edgesLeft.Store(int64(s.cfg.MaxEdges))
	var edgesClipped atomic.Bool

	// --- level-1 edge scan (root is always "resolved") ---
	rootEdges, err := s.scanEdges(bctx, net, rootOutcome.WalletID, &edgesLeft, &edgesClipped)
	if err != nil {
		return nil, err
	}
	var edgesExamined atomic.Int64
	edgesExamined.Store(int64(len(rootEdges)))

	maxHops := req.MaxHops

	// cpAgg_2 — the hop-1 candidate book, built exactly as Phase-6:
	// direction filters the hop-1 edge set only, before derivation;
	// self-edges and root-returning loops drop.
	rootSet := &candSet{cands: map[string]*candidate{}}
	for _, e := range rootEdges {
		cp := counterpartyOf(e, root)
		if cp == "" || cp == root {
			continue // self-edge
		}
		if req.Direction == "in" && e.ToAddress != root {
			continue
		}
		if req.Direction == "out" && e.FromAddress != root {
			continue
		}
		c := rootSet.cands[cp]
		if c == nil {
			c = &candidate{addr: cp, sentinel: sentinels[cp],
				parents: map[string][]db.ListWalletEdgesRow{}}
			rootSet.cands[cp] = c
		}
		c.parents[root] = append(c.parents[root], e)
	}
	sortCandidates(rootSet)

	// Unified dedup (plan §2.4): root ∪ frontier positions ≤ d ∪
	// counterparty sets of emitted-prefix scans ≤ d−1 — snapshotted
	// before each level build. The post-filter level-1 counterparty set
	// joins immediately; producing scans join only after they emit.
	dedup := map[string]bool{root: true}
	for cp := range rootSet.cands {
		dedup[cp] = true
	}

	var (
		examined    atomic.Int64
		failed      atomic.Int64
		deadlineHit atomic.Bool
		nodeUntil   sync.Map // addr → *int64 for attempted positions
	)
	sets := map[int]*candSet{2: rootSet} // cpAgg_d per level
	levels := map[int]*levelState{}      // scheduled frontier state per d
	posDepth := map[string]int{}         // position addr → frontier index
	children := map[string][]string{}    // position addr → sorted next-level cands

	for d := 2; d <= maxHops; d++ {
		cs := sets[d]
		st := &levelState{}
		st.results = make([]nodeResult, len(cs.positions))
		for i, p := range cs.positions {
			st.results[i] = nodeResult{pos: i, addr: p}
			posDepth[p] = d
			dedup[p] = true // frontier positions ≤ d dedup'd at the d+1 build
		}
		levels[d] = st

		// Ordered-position pull under the shared concurrency/pool bounds —
		// the Phase-6 worker loop parameterized by depth.
		if len(st.results) > 0 {
			var (
				nextPos atomic.Int64
				ex      atomic.Int64
			)
			maxNodes := s.cfg.MaxNodesPerDepth[d-2]
			maxPages := s.tx.MaxPagesFor(d)
			g, gctx := errgroup.WithContext(bctx)
			for w := 0; w < s.cfg.Concurrency; w++ {
				g.Go(func() error {
					for {
						i := int(nextPos.Add(1) - 1)
						if i >= len(st.results) || i >= maxNodes {
							return nil
						}
						if gctx.Err() != nil {
							deadlineHit.Store(true)
							return nil
						}
						examined.Add(1)
						ex.Add(1)
						r := &st.results[i]
						out, err := s.tx.EnsureIndexed(gctx, net, r.addr,
							transaction.EnsureOptions{
								Refresh:  req.Refresh,
								Budget:   budget,
								MaxPages: maxPages,
							})
						if err != nil {
							// Context cancellation mid-flight is a
							// deadline outcome; other errors are
							// infrastructure — propagate, never faked
							// as a deadline (M2).
							if gctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) ||
								errors.Is(err, context.Canceled) {
								deadlineHit.Store(true)
								return nil
							}
							return err
						}
						if out.BudgetExhausted {
							// Raced: a sibling drained the pool between
							// the gate and the head call — the node was
							// never attempted; it joins nodes_remaining.
							examined.Add(-1)
							ex.Add(-1)
							return nil
						}
						r.out = out
						var wm *int64
						if out.IndexedUntilBlock != nil {
							v := *out.IndexedUntilBlock
							wm = &v
						}
						nodeUntil.Store(r.addr, wm)
						// failed/disabled indexes resolve with empty
						// contribution + nodes_failed (plan §3.3.2).
						noEdges := out.IndexStatus == transaction.StatusFailed ||
							out.IndexStatus == transaction.StatusDisabled
						if noEdges {
							failed.Add(1)
							r.failed = true
						} else {
							rows, err := s.scanEdges(gctx, net, out.WalletID,
								&edgesLeft, &edgesClipped)
							if err != nil {
								if gctx.Err() != nil {
									deadlineHit.Store(true)
									return nil
								}
								return err
							}
							edgesExamined.Add(int64(len(rows)))
							r.edges = rows
						}
						r.done = true
						if budget.Exhausted() {
							return nil
						}
					}
				})
			}
			if err := g.Wait(); err != nil {
				return nil, err
			}
			st.examined = int(ex.Load())
		}
		if bctx.Err() != nil {
			deadlineHit.Store(true)
		}

		// Contiguous resolved prefix → emitted level-d nodes.
		for st.emitted < len(st.results) && st.results[st.emitted].done {
			st.emitted++
		}

		// cpAgg_{d+1} — built from ALL scanned edges of the emitted
		// prefix (Reading A): clean intermediaries seed deeper
		// frontiers; dedup-set far endpoints are excluded from
		// candidacy entirely. Terminal (d = maxHops) candidate set is
		// built identically — dedup is uniform.
		next := &candSet{cands: map[string]*candidate{}}
		childOf := map[string][]string{}
		for i := 0; i < st.emitted; i++ {
			r := st.results[i]
			for _, e := range r.edges {
				cp := counterpartyOf(e, r.addr)
				if cp == "" || cp == r.addr || dedup[cp] {
					continue
				}
				c := next.cands[cp]
				if c == nil {
					c = &candidate{addr: cp, sentinel: sentinels[cp],
						parents: map[string][]db.ListWalletEdgesRow{}}
					next.cands[cp] = c
				}
				if _, seen := c.parents[r.addr]; !seen {
					childOf[r.addr] = append(childOf[r.addr], cp)
				}
				c.parents[r.addr] = append(c.parents[r.addr], e)
			}
		}
		sortCandidates(next)
		sets[d+1] = next
		for i := 0; i < st.emitted; i++ {
			r := st.results[i]
			if kids := childOf[r.addr]; len(kids) > 0 {
				sort.Strings(kids)
				children[r.addr] = kids
			}
			// The emitted level-d scan's counterparty set joins dedup
			// for deeper builds (scan levels ≤ d′−2).
			for _, e := range r.edges {
				if cp := counterpartyOf(e, r.addr); cp != "" {
					dedup[cp] = true
				}
			}
		}
	}

	// --- evidence for every candidate set (caller ctx — see Exposures) ---
	labelIdx := map[string][]labelEvidence{}
	foreignNetIDs := []int64{}
	for _, n := range s.resolver.List() {
		if n.ID != net.ID {
			foreignNetIDs = append(foreignNetIDs, n.ID)
		}
	}
	for d := 2; d <= maxHops+1; d++ {
		cs := sets[d]
		if cs == nil || len(cs.order) == 0 {
			continue
		}
		labels, err := s.q.BulkMatchLabels(ctx, db.BulkMatchLabelsParams{
			NetworkID: net.ID, Column2: cs.order})
		if err != nil {
			return nil, err
		}
		for _, r := range labels {
			labelIdx[r.NormalizedAddress] = append(labelIdx[r.NormalizedAddress],
				localEvidence(r))
		}
		xlabels, err := s.q.BulkMatchLabelsCrossNet(ctx, db.BulkMatchLabelsCrossNetParams{
			Addrs: cs.order, NetworkIds: foreignNetIDs})
		if err != nil {
			return nil, err
		}
		for _, r := range xlabels {
			labelIdx[r.NormalizedAddress] = append(labelIdx[r.NormalizedAddress],
				foreignEvidence(r))
		}
	}

	// Sanctions lookups — per-node LookupWallet over every candidate.
	lookups := map[string]*sanctions.WalletLookup{}
	for d := 2; d <= maxHops+1; d++ {
		cs := sets[d]
		if cs == nil {
			continue
		}
		for _, cp := range cs.order {
			l, err := s.lookupSanctions(ctx, net, cp)
			if err != nil {
				return nil, err
			}
			lookups[cp] = l
		}
	}

	items := []Item{} // [] on empty — never null on the wire
	// hop-1 items (positions + sentinels — all are counterparties).
	for _, cp := range rootSet.order {
		sigs, cat := s.signals(cp, labelIdx[cp], lookups[cp])
		if len(sigs) == 0 {
			continue
		}
		c := rootSet.cands[cp]
		it := Item{
			Counterparty:   cp,
			Category:       cat,
			MatchedSignals: sigs,
			Relationship:   RelDirect,
			Hops:           1,
			ExposureScore:  exposureScore(1, labelIdx[cp], lookups[cp]),
			minPos:         c.rank,
			Paths: []Path{{
				HopCount:           1,
				PathVerified:       false,
				TemporalOrderValid: true,
				Edges:              []Edge{toEdge(pickEdge(c.parents[root]), net.Code)},
			}},
		}
		it.TransactionEvidence = dedupEvidence(it.Paths)
		items = append(items, it)
	}
	// hop-d items (d ≥ 2) from the emitted-prefix candidate sets —
	// paths enumerate node sequences via forward lexicographic DFS
	// over the parents DAG (plan §2.9).
	for d := 2; d <= maxHops; d++ {
		cs := sets[d+1]
		for _, cp := range cs.order {
			sigs, cat := s.signals(cp, labelIdx[cp], lookups[cp])
			if len(sigs) == 0 {
				continue
			}
			ps, minPos := s.pathsFor(cp, d, cs, sets, children, posDepth,
				root, net.Code)
			if len(ps) == 0 {
				continue
			}
			items = append(items, Item{
				Counterparty:        cp,
				Category:            cat,
				MatchedSignals:      sigs,
				Relationship:        RelIndirect,
				Hops:                d,
				ExposureScore:       exposureScore(d, labelIdx[cp], lookups[cp]),
				minPos:              minPos,
				Paths:               ps,
				TransactionEvidence: dedupEvidence(ps),
			})
		}
	}

	// category filter over all matched_signals (plan §3.3.4).
	if req.Category != "" {
		keep := []Item{}
		for _, it := range items {
			for _, sig := range it.MatchedSignals {
				if sig.Category == req.Category || it.Category == req.Category {
					keep = append(keep, it)
					break
				}
			}
		}
		items = keep
	}

	// deterministic total order: hops asc, min contributing position
	// index, normalized counterparty asc.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.minPos != b.minPos {
			return a.minPos < b.minPos
		}
		return a.Counterparty < b.Counterparty
	})

	// --- truncation cuts (precedence pinned, evaluated post-assembly) ---
	var reason *string
	set := func(r string) {
		if reason == nil {
			reason = &r
		}
	}
	// counterparties_total counts MATERIALIZED frontier position-books —
	// frontier_2 is materialized even at max_hops=1 (it drives dedup +
	// coverage), so the counting domain is {2..max(max_hops,2)} while
	// the terminal candidate book never counts. The max_nodes check
	// covers SCHEDULED frontiers only ({2..max_hops}) — a materialized-
	// but-unscheduled frontier_2 cannot fire it at max_hops=1.
	mapDomain := maxHops
	if mapDomain < 2 {
		mapDomain = 2
	}
	counterpartiesTotal := 0
	for d := 2; d <= mapDomain; d++ {
		counterpartiesTotal += len(sets[d].positions)
	}
	maxNodesHit := false
	for d := 2; d <= maxHops; d++ {
		if len(sets[d].positions) > s.cfg.MaxNodesPerDepth[d-2] {
			maxNodesHit = true
		}
	}
	nodesRemaining := counterpartiesTotal - int(examined.Load())
	if nodesRemaining < 0 {
		nodesRemaining = 0
	}

	// limit cut — items beyond limit are dropped with truncated=limit.
	limit := req.Limit
	limitCut := limit > 0 && len(items) > limit
	if limitCut {
		items = items[:limit]
	}

	// response-cap cut at item boundary.
	capCut := false
	if s.cfg.ResponseCap > 0 && len(items) > 0 {
		budgetB := s.cfg.ResponseCap - 4096 // envelope headroom
		if budgetB < 0 {
			budgetB = 0
		}
		total := 0
		cut := len(items)
		for i, it := range items {
			b, _ := json.Marshal(it)
			total += len(b) + 1
			if total > budgetB {
				cut = i
				capCut = true
				break
			}
		}
		if capCut {
			items = items[:cut]
		}
	}

	switch {
	case deadlineHit.Load():
		set(TruncDeadline)
	case budget.Exhausted() && nodesRemaining > 0:
		set(TruncMaxReqs)
	case maxNodesHit:
		set(TruncMaxNodes)
	case edgesLeft.Load() <= 0 && (edgesClipped.Load() || nodesRemaining > 0):
		set(TruncMaxEdges)
	case capCut:
		set(TruncRespCap)
	case limitCut:
		set(TruncLimit)
	}

	// --- envelope ---
	res := &Result{
		Items:                 items,
		Truncated:             reason,
		TracingMethod:         "bounded_bfs",
		ExposurePolicyVersion: ExposurePolicyV1,
	}
	res.Coverage.CounterpartiesTotal = counterpartiesTotal
	res.Coverage.NodesExamined = int(examined.Load())
	res.Coverage.NodesRemaining = nodesRemaining
	res.Coverage.NodesFailed = int(failed.Load())
	res.Coverage.EdgesExamined = edgesExamined.Load()
	res.Coverage.ProviderCalls = budget.Used()
	if s.metrics != nil {
		s.metrics.IncTracingNodesExamined(examined.Load())
	}
	res.Coverage.ProviderStatus = rootOutcome.ProviderStatus
	res.Coverage.HeadBlock = rootOutcome.HeadBlock
	res.Coverage.Root = RootCoverage{
		IndexStatus:       rootOutcome.IndexStatus,
		Stale:             rootOutcome.Stale,
		ProviderStatus:    rootOutcome.ProviderStatus,
		IndexedUntilBlock: rootOutcome.IndexedUntilBlock,
		IndexedAt:         rootOutcome.IndexedAt,
		HistoryTruncated:  rootOutcome.HistoryTruncated,
		IndexScope:        rootOutcome.IndexScope,
		RowsIndexed:       rootOutcome.RowsIndexed,
		RowsSkipped:       rootOutcome.RowsSkipped,
	}
	res.Coverage.FrontierByHop = map[int]int{}
	res.Coverage.NodesExaminedByHop = map[int]int{}
	res.Coverage.FrontierEmittedByHop = map[int]int{}
	res.Coverage.NodesRemainingByHop = map[int]int{}
	for d := 2; d <= mapDomain; d++ {
		res.Coverage.FrontierByHop[d] = len(sets[d].positions)
		var ex, em int
		if st := levels[d]; st != nil {
			ex, em = st.examined, st.emitted
			res.Coverage.PositionsEmitted += em
			if ex > 0 {
				res.Coverage.MaxDepthReached = d
			}
		}
		res.Coverage.NodesExaminedByHop[d] = ex
		res.Coverage.FrontierEmittedByHop[d] = em
		rem := len(sets[d].positions) - ex
		if rem < 0 {
			rem = 0
		}
		res.Coverage.NodesRemainingByHop[d] = rem
	}
	nm := map[string]*int64{}
	nodeUntil.Range(func(k, v any) bool {
		nm[k.(string)] = v.(*int64)
		return true
	})
	res.Coverage.NodeIndexedUntil = nm
	if v, err := s.sanctionsVersion(ctx); err == nil {
		res.Coverage.SanctionsSnapshotVersion = v
	}
	if v, err := s.labelsVersion(ctx); err == nil {
		res.Coverage.LabelsSnapshotVersion = v
	}
	return res, nil
}

// sortCandidates orders a candidate book by the §2.3 key — firstEdge
// (earliest ts, then tx_hash, then transfer_index) over the union of
// all incoming parent edges, compared as (ts, tx_hash, addr) — then
// derives the frontier (non-sentinel subset) and per-candidate rank.
func sortCandidates(cs *candSet) {
	cs.order = cs.order[:0]
	for addr := range cs.cands {
		cs.order = append(cs.order, addr)
	}
	sort.SliceStable(cs.order, func(i, j int) bool {
		a, b := cs.cands[cs.order[i]], cs.cands[cs.order[j]]
		ea, eb := unionFirstEdge(a), unionFirstEdge(b)
		if !ea.BlockTimestamp.Time.Equal(eb.BlockTimestamp.Time) {
			return ea.BlockTimestamp.Time.Before(eb.BlockTimestamp.Time)
		}
		if ea.TxHash != eb.TxHash {
			return ea.TxHash < eb.TxHash
		}
		return a.addr < b.addr
	})
	cs.positions = cs.positions[:0]
	cs.posIdx = map[string]int{}
	for i, cp := range cs.order {
		cs.cands[cp].rank = i
		if !cs.cands[cp].sentinel {
			cs.posIdx[cp] = len(cs.positions)
			cs.positions = append(cs.positions, cp)
		}
	}
}

// unionFirstEdge returns the §2.3 ordering key: firstEdge over the
// union of all incoming parent edges.
func unionFirstEdge(c *candidate) db.ListWalletEdgesRow {
	var all []db.ListWalletEdgesRow
	for _, es := range c.parents {
		all = append(all, es...)
	}
	return firstEdge(all)
}

// pathsFor enumerates one hop-d item's node sequences (plan §2.9):
// reverse-BFS reachability mask over the parents DAG, then forward
// lexicographic DFS from frontier_2 positions in ascending ADDRESS
// order — via_2-most-significant tuple order — following sorted
// children lists, cut at MaxPathsPair. Edges are picked per step with
// pickEdge; temporal_order_valid requires every adjacent pair
// non-decreasing. Returns the paths + the min frontier_2 position
// index over emitted sequences (item.minPos domain).
func (s *Service) pathsFor(cp string, d int, term *candSet,
	sets map[int]*candSet, children map[string][]string,
	posDepth map[string]int, root, netCode string) ([]Path, int) {

	// Reverse-reachability mask: positions that can reach cp.
	mask := map[string]bool{}
	var queue []string
	for p := range term.cands[cp].parents {
		if !mask[p] {
			mask[p] = true
			queue = append(queue, p)
		}
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		lv := posDepth[p]
		cs := sets[lv]
		if cs == nil {
			continue
		}
		c := cs.cands[p]
		if c == nil {
			continue
		}
		for pp := range c.parents {
			if !mask[pp] {
				mask[pp] = true
				queue = append(queue, pp)
			}
		}
	}

	// Frontier_2 seeds in ascending ADDRESS order (the tuple key —
	// frontier order is the scheduling key, never the tuple key).
	var vias2 []string
	for p := range mask {
		if posDepth[p] == 2 {
			vias2 = append(vias2, p)
		}
	}
	sort.Strings(vias2)

	var tuples [][]string
	var walk func(node string, path []string)
	walk = func(node string, path []string) {
		if len(tuples) >= s.cfg.MaxPathsPair {
			return
		}
		path = append(path, node) // path[k−1] = via_{k+1}; node = via_{len(path)}
		if len(path) == d-1 {
			// node = via_d — emit iff it reaches the item cp.
			for _, ch := range children[node] {
				if ch == cp {
					tuples = append(tuples, append([]string(nil), path...))
					return
				}
			}
			return
		}
		for _, ch := range children[node] {
			if mask[ch] {
				walk(ch, path)
				if len(tuples) >= s.cfg.MaxPathsPair {
					return
				}
			}
		}
	}
	for _, v2 := range vias2 {
		walk(v2, nil)
		if len(tuples) >= s.cfg.MaxPathsPair {
			break
		}
	}

	minPos := int(^uint(0) >> 1)
	var ps []Path
	for _, tuple := range tuples {
		rows := make([]db.ListWalletEdgesRow, 0, d)
		prev := root
		for j, via := range tuple {
			c := sets[j+2].cands[via]
			rows = append(rows, pickEdge(c.parents[prev]))
			prev = via
		}
		rows = append(rows, pickEdge(term.cands[cp].parents[prev]))
		if pos := sets[2].posIdx[tuple[0]]; pos < minPos {
			minPos = pos
		}
		path := Path{
			HopCount:           d,
			PathVerified:       false,
			TemporalOrderValid: true,
			Edges:              make([]Edge, 0, d),
		}
		for i, r := range rows {
			if i > 0 && !temporalOK(rows[i-1], r) {
				path.TemporalOrderValid = false
			}
			path.Edges = append(path.Edges, toEdge(r, netCode))
		}
		ps = append(ps, path)
	}
	return ps, minPos
}

// scanEdges pages one node's edges under the request-global edge count.
// Fetch is remain+1 to detect the boundary; deduction is a CAS loop so
// concurrent workers can never overshoot MaxEdges, and any clip —
// boundary row or concurrent budget shrink — sets clipped so the
// truncation reason is reported, never dropped (review M1).
func (s *Service) scanEdges(ctx context.Context, net network.Network,
	walletID int64, left *atomic.Int64, clipped *atomic.Bool) (
	[]db.ListWalletEdgesRow, error) {

	remain := left.Load()
	if remain <= 0 {
		clipped.Store(true) // another worker exhausted the budget
		return nil, nil
	}
	rows, err := s.q.ListWalletEdges(ctx, db.ListWalletEdgesParams{
		NetworkID:    net.ID,
		FromWalletID: pgtype.Int8{Int64: walletID, Valid: true},
		Limit:        int32(remain + 1),
	})
	if err != nil {
		return nil, err
	}
	for {
		avail := left.Load()
		use := int64(len(rows))
		if use > avail {
			use = avail
		}
		if left.CompareAndSwap(avail, avail-use) {
			if int64(len(rows)) > use {
				rows = rows[:use]
				clipped.Store(true)
			}
			return rows, nil
		}
	}
}

// counterpartyOf returns the far endpoint of an edge relative to node.
func counterpartyOf(e db.ListWalletEdgesRow, node string) string {
	switch {
	case e.FromAddress == node && e.ToAddress == node:
		return node // self edge — caller drops
	case e.FromAddress == node:
		return e.ToAddress
	default:
		return e.FromAddress
	}
}

// firstEdge returns the earliest edge in scan order (ts, tx_hash,
// transfer_index) — the position-ordering key, distinct from pickEdge.
func firstEdge(edges []db.ListWalletEdgesRow) db.ListWalletEdgesRow {
	best := edges[0]
	for _, e := range edges[1:] {
		if e.BlockTimestamp.Time.Before(best.BlockTimestamp.Time) ||
			(e.BlockTimestamp.Time.Equal(best.BlockTimestamp.Time) &&
				(e.TxHash < best.TxHash ||
					(e.TxHash == best.TxHash &&
						e.TransferIndex < best.TransferIndex))) {
			best = e
		}
	}
	return best
}

// pickEdge selects the single evidence edge for a hop position:
// earliest block_timestamp → smallest amount_raw → (tx_hash,
// transfer_index).
func pickEdge(edges []db.ListWalletEdgesRow) db.ListWalletEdgesRow {
	best := edges[0]
	for _, e := range edges[1:] {
		if edgeLess(e, best) {
			best = e
		}
	}
	return best
}

func edgeLess(a, b db.ListWalletEdgesRow) bool {
	at, bt := a.BlockTimestamp.Time, b.BlockTimestamp.Time
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	if a.AmountRaw != b.AmountRaw {
		return amountLess(a.AmountRaw, b.AmountRaw)
	}
	if a.TxHash != b.TxHash {
		return a.TxHash < b.TxHash
	}
	return a.TransferIndex < b.TransferIndex
}

// amountLess compares raw-integer strings without floats.
func amountLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b) // canonical: no leading zeros
	}
	return a < b
}

func temporalOK(e1, e2 db.ListWalletEdgesRow) bool {
	return !e2.BlockTimestamp.Time.Before(e1.BlockTimestamp.Time)
}

// toEdge maps a stored row to the §11.5 edge evidence; NATIVE rows
// surface asset="native" (filter-vocabulary alignment).
func toEdge(r db.ListWalletEdgesRow, netCode string) Edge {
	asset := "native"
	if r.AssetIdentifier.Valid && r.AssetIdentifier.String != "" {
		asset = r.AssetIdentifier.String
	}
	e := Edge{
		FromAddress:        r.FromAddress,
		ToAddress:          r.ToAddress,
		Network:            netCode,
		TxHash:             r.TxHash,
		TransferIndex:      r.TransferIndex,
		TransferType:       r.TransferType,
		Asset:              asset,
		AmountRaw:          r.AmountRaw,
		ConfirmationStatus: r.ConfirmationStatus,
	}
	if r.BlockNumber.Valid {
		n := r.BlockNumber.Int64
		e.BlockNumber = &n
	}
	if r.BlockTimestamp.Valid {
		t := r.BlockTimestamp.Time.UTC()
		e.BlockTimestamp = &t
	}
	return e
}

// dedupEvidence is the flat deduped union across an item's paths keyed
// by (tx_hash, transfer_index).
func dedupEvidence(paths []Path) []Edge {
	seen := map[string]bool{}
	var out []Edge
	for _, p := range paths {
		for _, e := range p.Edges {
			k := e.TxHash + "|" + e.TransferIndex
			if !seen[k] {
				seen[k] = true
				out = append(out, e)
			}
		}
	}
	if out == nil {
		out = []Edge{}
	}
	return out
}

// localEvidence adapts a same-network BulkMatchLabels row.
func localEvidence(r db.BulkMatchLabelsRow) labelEvidence {
	return labelEvidence{
		CategoryCode: r.CategoryCode, CategoryType: r.CategoryType,
		Label: r.Label, Verification: r.VerificationStatus,
		Confidence: r.Confidence, EntityID: r.EntityID,
		EntityName: r.EntityName, Source: r.SourceCode,
	}
}

// foreignEvidence adapts a cross-network label row — carries the
// foreign network code and the foreign wallets.address original
// representation for the wire signal.
func foreignEvidence(r db.BulkMatchLabelsCrossNetRow) labelEvidence {
	e := localEvidence(db.BulkMatchLabelsRow{
		NormalizedAddress: r.NormalizedAddress, Label: r.Label,
		VerificationStatus: r.VerificationStatus, Confidence: r.Confidence,
		CategoryCode: r.CategoryCode, CategoryType: r.CategoryType,
		EntityID: r.EntityID, EntityName: r.EntityName, SourceCode: r.SourceCode,
	})
	e.ForeignNet = r.NetworkCode
	e.ListedAddr = r.ListedAddress
	return e
}

func (s *Service) lookupSanctions(ctx context.Context, net network.Network,
	addr string) (*sanctions.WalletLookup, error) {
	l, _, _, err := s.sanctions.LookupWallet(ctx, net.ID, net.Code, addr)
	if err != nil {
		return nil, err
	}
	if l == nil {
		l = &sanctions.WalletLookup{}
	}
	return l, nil
}

// categoryRank orders label tiers: LEGAL > MALICIOUS_ACTIVITY >
// SERVICE_TYPE > ENTITY_TYPE > OTHER, then category code ascending.
func categoryRank(t string) int {
	switch strings.ToUpper(t) {
	case "LEGAL":
		return 0
	case "MALICIOUS_ACTIVITY":
		return 1
	case "SERVICE_TYPE":
		return 2
	case "ENTITY_TYPE":
		return 3
	default:
		return 4
	}
}

// signals builds matched_signals[] + the winning category for one
// counterparty: SANCTIONED > best label category > cross_chain_listing.
// Foreign-network label rows emit kind:"label" signals carrying
// network + listed_address provenance but NEVER participate in
// consider() — an item whose only signals are foreign resolves to
// cross_chain_listing.
func (s *Service) signals(addr string, labels []labelEvidence,
	lookup *sanctions.WalletLookup) ([]MatchedSignal, string) {

	var sigs []MatchedSignal
	bestCat := ""
	bestRank := int(^uint(0) >> 1)
	bestCode := ""

	consider := func(cat string, rank int, code string) {
		if bestCat == "" || rank < bestRank ||
			(rank == bestRank && code < bestCode) {
			bestCat, bestRank, bestCode = cat, rank, code
		}
	}

	if lookup != nil {
		for _, e := range lookup.Sanctions {
			sig := MatchedSignal{
				Kind:     SigSanctionsDirect,
				Category: CatSanctioned,
				Source:   e.Source.Code,
				Entity:   &EntityRef{ID: e.EntityID, Name: e.EntityName},
			}
			for _, p := range e.Programs {
				sig.Programs = append(sig.Programs,
					ProgramRef{Code: p.Code, Name: p.Name})
			}
			sigs = append(sigs, sig)
		}
		if len(lookup.Sanctions) > 0 {
			consider(CatSanctioned, -1, "")
		}
	}
	for _, l := range labels {
		ent := (*EntityRef)(nil)
		if l.EntityID.Valid {
			ent = &EntityRef{ID: l.EntityID.Int64, Name: l.EntityName.String}
		}
		sig := MatchedSignal{
			Kind:               SigLabel,
			Category:           l.CategoryCode,
			Label:              l.Label,
			Entity:             ent,
			Source:             l.Source,
			VerificationStatus: l.Verification,
		}
		if l.ForeignNet != "" {
			netCode := l.ForeignNet
			sig.Network = &netCode
			sig.ListedAddress = l.ListedAddr
		} else {
			consider(l.CategoryCode, categoryRank(l.CategoryType), l.CategoryCode)
		}
		sigs = append(sigs, sig)
	}
	if lookup != nil {
		for _, o := range append(
			append([]sanctions.Observation{}, lookup.CrossChainObservations...),
			lookup.UnresolvedNetworkListings...) {
			sigs = append(sigs, MatchedSignal{
				Kind:          SigListing,
				Category:      CatCrossChain,
				Network:       o.NetworkCode,
				ListedAddress: o.ListedAddress,
				Source:        o.SourceCode,
			})
		}
		if len(lookup.CrossChainObservations)+
			len(lookup.UnresolvedNetworkListings) > 0 {
			consider(CatCrossChain, int(^uint(0)>>1)-1, "")
		}
	}
	if bestCat == "" && len(sigs) > 0 {
		bestCat = CatCrossChain // solely foreign-label item
	}
	return sigs, bestCat
}

// sanctionsVersion returns the published OFAC snapshot version once per
// request (nil when none published).
func (s *Service) sanctionsVersion(ctx context.Context) (*string, error) {
	snap, err := s.q.LatestSnapshotPublished(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := snap.Version
	return &v, nil
}

func (s *Service) labelsVersion(ctx context.Context) (*string, error) {
	snap, err := s.q.LatestLabelSnapshotPublished(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := snap.Version
	return &v, nil
}

// Sentinel reports whether an address is a never-expand sentinel.
func Sentinel(addr string) bool { return sentinels[addr] }

// ValidateRequest enforces the /exposures parameter contract.
func ValidateRequest(req *Request) error {
	if req.MaxHops < 1 || req.MaxHops > 5 {
		return transaction.ErrInvalidInput // max_hops ∈ {1..5} → 400
	}
	switch req.Direction {
	case "", "in", "out":
	default:
		return transaction.ErrInvalidInput
	}
	if req.Limit < 0 || req.Limit > 100 {
		return transaction.ErrInvalidInput
	}
	return nil
}
