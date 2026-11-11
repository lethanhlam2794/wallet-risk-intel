package screening

import (
	"context"
	"sort"
	"strings"
	"time"

	"checkwallet/internal/db"
	"checkwallet/internal/scoring"
)

// exposureStore is the analyzer's bounded-IO seam — *db.Queries
// satisfies it; tests fake it. Round-trip budget per analyze(): ≤6
// (candidate inbound scan, outbound scan, index-state read,
// receiver-check scan, merged label check, merged sanctions check).
type exposureStore interface {
	ListConfirmedCounterpartyTransfers(ctx context.Context,
		arg db.ListConfirmedCounterpartyTransfersParams) (
		[]db.ListConfirmedCounterpartyTransfersRow, error)
	ListConfirmedInboundToWallets(ctx context.Context,
		arg db.ListConfirmedInboundToWalletsParams) (
		[]db.ListConfirmedInboundToWalletsRow, error)
	BulkMatchLabels(ctx context.Context,
		arg db.BulkMatchLabelsParams) ([]db.BulkMatchLabelsRow, error)
	BulkMatchSanctionedAddresses(ctx context.Context,
		arg db.BulkMatchSanctionedAddressesParams) (
		[]db.BulkMatchSanctionedAddressesRow, error)
	ListWalletIndexStates(ctx context.Context,
		arg db.ListWalletIndexStatesParams) ([]db.WalletIndexState, error)
}

type exposureAnalyzer struct {
	store    exposureStore
	prices   scoring.PriceTableV1_1
	freshTTL time.Duration
	now      func() time.Time
}

// onchainExposure is the analyzer output the mapper folds.
type onchainExposure struct {
	evaluated                bool
	findings                 []scoring.Finding
	items                    []ExposureItem
	evidence                 []EvidenceRef
	onchainCoverage          string // "complete" | "partial" | "absent"
	serviceClassified        bool
	serviceClassUnknown      bool // candidate-label check failed
	sourcesEvaluated         int
	sourcesReceiverUnchecked int
	degraded                 bool // any query failed → PARTIAL status
}

type sourceGroup struct {
	addr        string
	walletID    int64
	hasWalletID bool
	meaningful  int
	belowFloor  int
	unpriced    int
	malformed   int
	txHashes    []string
}

// assetKey resolves a transfer row to a pricing-table key. ok=false
// marks an unassessable row (non-NATIVE with NULL asset_identifier —
// a token row must carry its contract id; treating it as the chain
// coin would overprice it).
func assetKey(transferType string, assetID string) (string, bool) {
	if transferType == "NATIVE" {
		return "native", true
	}
	if assetID == "" {
		return "", false
	}
	return strings.ToLower(assetID), true
}

// usableIndex reports whether one wallet's index-state set contains a
// row proving complete coverage: fresh, non-stale, non-truncated.
func (a *exposureAnalyzer) usableIndex(rows []db.WalletIndexState) bool {
	for _, r := range rows {
		if r.Status != "fresh" || r.HistoryTruncated {
			continue
		}
		// NULL indexed_at cannot prove freshness — never usable.
		if !r.IndexedAt.Valid {
			continue
		}
		if a.freshTTL > 0 && a.now().Sub(r.IndexedAt.Time) > a.freshTTL {
			continue // stale
		}
		return true
	}
	return false
}

func (a *exposureAnalyzer) analyze(ctx context.Context, networkID int64,
	networkCode string, walletID int64, candAddr string) onchainExposure {

	out := onchainExposure{evaluated: true, onchainCoverage: "partial"}
	partial := false
	markErr := func() { out.degraded, partial = true, true }

	// 1-2. Candidate scans.
	inRows, err := a.store.ListConfirmedCounterpartyTransfers(ctx,
		db.ListConfirmedCounterpartyTransfersParams{
			NetworkID: networkID, WalletID: walletID,
			Direction: "in", Lim: scoring.ExposureScanLimitV1_1})
	if err != nil {
		out.evaluated = false // leg failed — not usable evidence
		out.degraded = true
		out.onchainCoverage = "partial"
		return out
	}
	if len(inRows) >= scoring.ExposureScanLimitV1_1 {
		partial = true
	}
	outRows, err := a.store.ListConfirmedCounterpartyTransfers(ctx,
		db.ListConfirmedCounterpartyTransfersParams{
			NetworkID: networkID, WalletID: walletID,
			Direction: "out", Lim: scoring.ExposureScanLimitV1_1})
	if err != nil {
		markErr()
	} else if len(outRows) >= scoring.ExposureScanLimitV1_1 {
		partial = true
	}

	// 3. Group inbound by source; bound distinct sources (scan order).
	var order []string
	sources := map[string]*sourceGroup{}
	for _, r := range inRows {
		addr := strings.ToLower(r.CounterpartyAddress)
		if addr == "" {
			continue // NULL-from row: sourceless, never a source
		}
		g, ok := sources[addr]
		if !ok {
			if len(order) >= scoring.ExposureMaxSourcesV1_1 {
				partial = true
				continue
			}
			g = &sourceGroup{addr: addr}
			if id, ok := r.CounterpartyWalletID.(int64); ok {
				g.walletID, g.hasWalletID = id, true
			}
			sources[addr], order = g, append(order, addr)
		}
		key, keyOK := assetKey(r.TransferType, r.AssetIdentifier.String)
		if !keyOK {
			g.unpriced++ // assetless token row: evidence-only
			continue
		}
		m, priced, err := a.prices.IsMeaningful(networkCode,
			key, r.AmountRaw)
		switch {
		case err != nil:
			g.malformed++
		case m:
			g.meaningful++
			if len(g.txHashes) < scoring.ExposureEvidenceTxsV1_1 {
				g.txHashes = append(g.txHashes, r.TxHash)
			}
		case priced:
			g.belowFloor++
		default:
			g.unpriced++
		}
	}
	out.sourcesEvaluated = len(order)

	// Outbound counterparty set (for the merged taint check + victim
	// contact classification). Wallet IDs are kept so non-source cps
	// can take the same receiver-check as sources (chain-tainted
	// victim contacts, spec §3.1 "tainted" incl. chain).
	outCPs := map[string]bool{}
	cpWallets := map[string]int64{}
	for _, r := range outRows {
		if a := strings.ToLower(r.CounterpartyAddress); a != "" {
			outCPs[a] = true
			if id, ok := r.CounterpartyWalletID.(int64); ok && sources[a] == nil {
				cpWallets[a] = id
			}
		}
	}

	// 4. Bulk index-state read over candidate + source + cp wallets.
	stateIDs := []int64{walletID}
	for _, a := range order {
		if sources[a].hasWalletID {
			stateIDs = append(stateIDs, sources[a].walletID)
		}
	}
	for _, wid := range cpWallets {
		stateIDs = append(stateIDs, wid)
	}
	states, err := a.store.ListWalletIndexStates(ctx,
		db.ListWalletIndexStatesParams{NetworkID: networkID, WalletIds: stateIDs})
	statesErr := err != nil
	if statesErr {
		markErr()
	}
	byWallet := map[int64][]db.WalletIndexState{}
	candRows := 0
	for _, s := range states {
		byWallet[s.WalletID] = append(byWallet[s.WalletID], s)
		if s.WalletID == walletID {
			candRows++
		}
	}
	candUsable := a.usableIndex(byWallet[walletID])
	if !statesErr && candRows == 0 {
		// "absent" asserts a proven no-index condition — only a
		// successful probe can prove it (cannot-determine ≈ partial).
		out.onchainCoverage = "absent"
	} else if !candUsable {
		partial = true
	}

	// Sources without usable index state: receiver-check unverifiable.
	var checkableIDs []int64
	for _, addr := range order {
		g := sources[addr]
		if !g.hasWalletID || !a.usableIndex(byWallet[g.walletID]) {
			out.sourcesReceiverUnchecked++
			partial = true
			continue
		}
		checkableIDs = append(checkableIDs, g.walletID)
	}

	// 5. Receiver-check scan → depth-2 sender set.
	senderAddrs := map[string]bool{}
	recvOf := map[int64]*sourceGroup{}
	for _, a := range order {
		if g := sources[a]; g.hasWalletID {
			recvOf[g.walletID] = g
		}
	}
	// Non-source outbound counterparties join the same receiver check
	// (their chain taint feeds possible_victim_contact). A cp whose
	// index state is unusable simply cannot be chain-verified — its
	// contact is never asserted, never silently upgraded.
	cpRecvOf := map[int64]string{}
	for addr, wid := range cpWallets {
		if a.usableIndex(byWallet[wid]) {
			cpRecvOf[wid] = addr
			checkableIDs = append(checkableIDs, wid)
		}
	}
	sendTo := map[int64]map[string]bool{} // receiver → sender addrs
	if len(checkableIDs) > 0 {
		rows, err := a.store.ListConfirmedInboundToWallets(ctx,
			db.ListConfirmedInboundToWalletsParams{
				NetworkID: networkID, WalletIds: checkableIDs,
				Lim: scoring.ExposureScanLimitV1_1})
		if err != nil {
			markErr()
		} else {
			if len(rows) >= scoring.ExposureScanLimitV1_1 {
				partial = true
			}
			overflow := false
			for _, r := range rows {
				sa := strings.ToLower(r.CounterpartyAddress)
				if sa == "" || !r.ReceiverWalletID.Valid {
					continue
				}
				key, keyOK := assetKey(r.TransferType, r.AssetIdentifier.String)
				if !keyOK {
					continue
				}
				m, _, err := a.prices.IsMeaningful(networkCode,
					key, r.AmountRaw)
				if err != nil || !m {
					continue
				}
				if sendTo[r.ReceiverWalletID.Int64] == nil {
					sendTo[r.ReceiverWalletID.Int64] = map[string]bool{}
				}
				sendTo[r.ReceiverWalletID.Int64][sa] = true
				if !senderAddrs[sa] &&
					len(senderAddrs) >= scoring.ExposureMaxDepth2SendersV1_1 {
					overflow = true
					continue
				}
				senderAddrs[sa] = true
			}
			if overflow {
				partial = true
			}
		}
	}

	// 6. Merged taint check: sources ∪ outbound cps ∪ candidate ∪
	// depth-2 senders — ONE bulk label call + ONE bulk sanctions call.
	taintSet := map[string]bool{}
	for a := range sources {
		taintSet[a] = true
	}
	for a := range outCPs {
		taintSet[a] = true
	}
	for a := range senderAddrs {
		taintSet[a] = true
	}
	taintSet[strings.ToLower(candAddr)] = true
	addrs := make([]string, 0, len(taintSet))
	for a := range taintSet {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)

	labels, lerr := a.store.BulkMatchLabels(ctx,
		db.BulkMatchLabelsParams{NetworkID: networkID, Column2: addrs})
	if lerr != nil {
		markErr()
		out.serviceClassUnknown = true
	}
	sanc, serr := a.store.BulkMatchSanctionedAddresses(ctx,
		db.BulkMatchSanctionedAddressesParams{
			NetworkID: networkID, Addresses: addrs})
	if serr != nil {
		markErr()
	}
	// Deterministic processing order — the queries have no ORDER BY,
	// and equal-weight category picks must not drift with row order.
	sort.Slice(labels, func(i, j int) bool {
		if labels[i].NormalizedAddress != labels[j].NormalizedAddress {
			return labels[i].NormalizedAddress < labels[j].NormalizedAddress
		}
		return labels[i].CategoryCode < labels[j].CategoryCode
	})
	sort.Slice(sanc, func(i, j int) bool {
		return sanc[i].NormalizedAddress < sanc[j].NormalizedAddress
	})

	// Resolve per-address taint.
	type taint struct {
		direct   bool
		sanction bool
		status   string
		category string
		service  bool
	}
	tinfo := map[string]*taint{}
	get := func(a string) *taint {
		if tinfo[a] == nil {
			tinfo[a] = &taint{}
		}
		return tinfo[a]
	}
	bestWeight := map[string]int{}
	for _, r := range labels {
		a := strings.ToLower(r.NormalizedAddress)
		t := get(a)
		cat := strings.ToUpper(r.CategoryCode)
		if scoring.ServiceClassificationV1_1(cat) && r.VerificationStatus != "disputed" {
			t.service = true
		}
		if !scoring.DirectTaintCategoryV1_1(cat) || r.VerificationStatus == "disputed" {
			continue
		}
		t.direct = true
		if w := scoring.TaintWeightV1_1(cat); w >= bestWeight[a] {
			bestWeight[a] = w
			t.category, t.status = cat, r.VerificationStatus
		}
	}
	for _, r := range sanc {
		a := strings.ToLower(r.NormalizedAddress)
		t := get(a)
		t.direct, t.sanction = true, true
		// A13: category is the max-weight taint evidence — SANCTIONED
		// (85) competes on the same scale as labels.
		if w := scoring.TaintWeightV1_1("SANCTIONED"); w >= bestWeight[a] {
			bestWeight[a] = w
			t.category = "SANCTIONED"
		}
		t.status = "verified" // sanctions listing is verified evidence
	}

	// Candidate service gate — independent of include_labels (A4).
	if t := tinfo[strings.ToLower(candAddr)]; t != nil && t.service {
		out.serviceClassified = true
	}

	// Chain taint: source has ≥1 meaningful inbound from a DIRECT-
	// tainted sender (depth-2 senders evaluated direct-only). The
	// sender's taint evidence carries through to the receiver finding.
	type senderTaint struct {
		category string
		status   string
		weight   int
	}
	chainTainted := map[string]senderTaint{}
	chainTaintedCP := map[string]senderTaint{}
	for recvID, senders := range sendTo {
		// Service-classified receivers can never become chain-tainted
		// sources — a service label does not confer taint, and chain
		// taint through Binance must not mass-flag its recipients.
		var dst map[string]senderTaint
		var key string
		if g := recvOf[recvID]; g != nil {
			if tg := tinfo[g.addr]; tg != nil && tg.service {
				continue
			}
			dst, key = chainTainted, g.addr
		} else if cp, ok := cpRecvOf[recvID]; ok {
			dst, key = chainTaintedCP, cp
		} else {
			continue
		}
		sortedSenders := make([]string, 0, len(senders))
		for sa := range senders {
			sortedSenders = append(sortedSenders, sa)
		}
		sort.Strings(sortedSenders)
		for _, sa := range sortedSenders {
			t := tinfo[sa]
			if t == nil || !t.direct {
				continue
			}
			w := scoring.TaintWeightV1_1(t.category)
			if cur, ok := dst[key]; !ok || w >= cur.weight {
				dst[key] = senderTaint{
					category: t.category, status: t.status, weight: w}
			}
		}
	}

	// 7. Emit findings per tainted source; collect evidence items.
	// Service labels never CONFER taint — but they never erase it
	// either (proposal §3.1): a sanctioned or taint-labeled
	// mixer/exchange is still a source.
	for _, a := range order {
		g := sources[a]
		t := tinfo[a]
		direct := t != nil && t.direct
		ct, chain := chainTainted[a]
		if !direct && !chain {
			continue
		}
		kind := "chain"
		cat, status := ct.category, ct.status
		if direct {
			kind, cat, status = "direct", t.category, t.status
		}
		cov := "complete"
		if partial || out.onchainCoverage == "absent" {
			cov = "partial"
		}
		reason := ""
		if g.meaningful == 0 {
			switch {
			case g.belowFloor > 0:
				reason = "below_materiality_floor"
			case g.malformed > 0:
				reason = "unassessable_amount"
			default:
				reason = "unpriced_asset"
			}
		}
		f := scoring.Finding{
			Kind:               scoring.KindExposure,
			Category:           cat,
			Source:             "onchain",
			Ref:                "onchain:" + a,
			Basis:              scoring.BasisOnchainComputed,
			Hops:               1,
			VerificationStatus: status,
			SourceAddress:      a,
			Direction:          "INBOUND",
			MeaningfulCount:    g.meaningful,
			TxHashes:           g.txHashes,
			Coverage:           cov,
			Reason:             reason,
			TaintKind:          kind,
		}
		out.findings = append(out.findings, f)
		out.items = append(out.items, ExposureItem{
			Counterparty:    a,
			Category:        cat,
			Hops:            1,
			Direction:       "INBOUND",
			Source:          "onchain",
			Basis:           scoring.BasisOnchainComputed,
			Scored:          g.meaningful >= 1,
			SourceAddress:   a,
			MeaningfulCount: g.meaningful,
			TxHashes:        g.txHashes,
			Coverage:        cov,
			Reason:          reason,
			TaintKind:       kind,
		})
		out.evidence = append(out.evidence, EvidenceRef{
			Kind: "exposure", Ref: map[string]any{
				"source": a, "basis": "onchain_computed",
				"tx_hashes": g.txHashes},
		})
		if direct && g.meaningful >= 1 {
			out.findings = append(out.findings, scoring.Finding{
				Kind:               scoring.KindProceedsReceiver,
				Category:           cat,
				Source:             "onchain",
				Ref:                "onchain:proceeds:" + a,
				VerificationStatus: status,
				SourceAddress:      a,
				Direction:          "INBOUND",
				MeaningfulCount:    g.meaningful,
				TxHashes:           g.txHashes,
				TaintKind:          "direct",
			})
		}
	}

	// Outbound → possible_victim_contact (not already an inbound
	// source). Sorted for deterministic persisted ordering.
	sortedCPs := make([]string, 0, len(outCPs))
	for cp := range outCPs {
		sortedCPs = append(sortedCPs, cp)
	}
	sort.Strings(sortedCPs)
	for _, cp := range sortedCPs {
		if sources[cp] != nil {
			continue
		}
		t := tinfo[cp]
		ct, chain := chainTaintedCP[cp]
		// §3.1 "tainted" incl. chain — but a service-only cp still
		// needs direct taint (same rule as sources: service labels
		// never confer taint).
		direct := t != nil && t.direct
		service := t != nil && t.service
		if !direct && (service || !chain) {
			continue
		}
		cat, status, tkind := ct.category, ct.status, "chain"
		if direct {
			cat, status, tkind = t.category, t.status, "direct"
		}
		out.findings = append(out.findings, scoring.Finding{
			Kind:               scoring.KindPossibleVictimContact,
			Category:           cat,
			Source:             "onchain",
			Ref:                "onchain:victim:" + cp,
			VerificationStatus: status,
			SourceAddress:      cp,
			Direction:          "OUTBOUND",
			TaintKind:          tkind,
		})
	}

	// Final coverage: absent stays absent; else partial when marked.
	if out.onchainCoverage != "absent" {
		if partial || !candUsable {
			out.onchainCoverage = "partial"
		} else {
			out.onchainCoverage = "complete"
		}
	}
	return out
}
