package ingestion

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/stablefreeze"
	"checkwallet/internal/wallet"
)

// freezePipeline stages issuer blacklist events and publishes them as
// FROZEN labels with DELTA semantics — never PublishRevokeAbsentWalletLabels:
// absence from a scan window is not a revocation signal. Only explicit
// unfreeze events revoke; destroy events append evidence but keep the
// label active (destroyBlackFunds preserves isBlackListed).
type freezePipeline struct {
	networks map[string]int64 // code → id
	byID     map[int64]string // id → code
	now      func() time.Time
}

func newFreezePipeline(networks map[string]int64, now func() time.Time) *freezePipeline {
	inv := make(map[int64]string, len(networks))
	for code, id := range networks {
		inv[id] = code
	}
	return &freezePipeline{networks: networks, byID: inv, now: now}
}

// freezeRunMeta carries window bounds + counters from Import through
// Validate into Publish — the publish step needs the windows to advance
// cursors and the head to stamp the snapshot version.
type freezeRunMeta struct {
	windows  []stablefreeze.WindowMeta
	head     int64
	checksum string
	parsed   int64 // well-formed event lines (staged or rejected-on-collision)
	rejected int64 // malformed lines + PK collisions
	staged   int64
}

// freezeHeader mirrors the artifact's JSONL line 1.
type freezeHeader struct {
	Version   int                       `json:"version"`
	Contracts []stablefreeze.WindowMeta `json:"contracts"`
}

func (p *freezePipeline) Import(ctx context.Context, qc *db.Queries,
	artifact provider.Snapshot, runID pgtype.UUID) (provider.ImportResult, Meta, error) {
	if len(artifact.Files) != 1 {
		return provider.ImportResult{}, nil,
			fmt.Errorf("freeze artifact must be exactly one file, got %d", len(artifact.Files))
	}
	f, err := os.Open(artifact.Files[0].Path)
	if err != nil {
		return provider.ImportResult{}, nil, fmt.Errorf("freeze import: open: %w", err)
	}
	defer f.Close()

	// Contract → its network code (fingerprint ingredient) via the cursor
	// rows — keeps the fingerprint honest to the real network id.
	states, err := qc.GetFreezeScanStates(ctx)
	if err != nil {
		return provider.ImportResult{}, nil, fmt.Errorf("freeze import: cursors: %w", err)
	}
	netByContract := map[string]string{}
	for _, st := range states {
		if code, ok := p.byID[st.NetworkID]; ok {
			netByContract[strings.ToLower(st.ContractAddress)] = code
		}
	}
	reg := stablefreeze.RegistryByAddress()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	if !sc.Scan() {
		return provider.ImportResult{}, nil, errors.New("freeze import: empty artifact")
	}
	var hdr freezeHeader
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
		return provider.ImportResult{}, nil, fmt.Errorf("freeze import: header: %w", err)
	}
	if hdr.Version != 1 {
		return provider.ImportResult{}, nil,
			fmt.Errorf("freeze import: artifact version %d unsupported", hdr.Version)
	}
	var head int64
	for _, w := range hdr.Contracts {
		if w.Head > head {
			head = w.Head
		}
	}

	var parsed, staged, rejected int64
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		parsed++
		var ev stablefreeze.EventLine
		if err := json.Unmarshal(line, &ev); err != nil {
			rejected++
			continue
		}
		params, ok := p.mapEvent(ev, reg, netByContract, runID)
		if !ok {
			rejected++
			continue
		}
		n, err := qc.StgInsertFreezeEvent(ctx, params)
		if err != nil {
			return provider.ImportResult{Labels: staged, Rejected: rejected}, nil,
				fmt.Errorf("freeze import: stage: %w", err)
		}
		if n == 0 {
			// (run, contract, tx, log_index) collision inside this file —
			// the same on-chain event seen twice is a malformed input.
			rejected++
			continue
		}
		staged++
	}
	if err := sc.Err(); err != nil {
		return provider.ImportResult{Labels: staged, Rejected: rejected}, nil,
			fmt.Errorf("freeze import: read: %w", err)
	}
	res := provider.ImportResult{Labels: staged, Rejected: rejected}
	meta := &freezeRunMeta{
		windows:  hdr.Contracts,
		head:     head,
		checksum: artifact.Checksum,
		parsed:   parsed,
		rejected: rejected,
		staged:   staged,
	}
	return res, meta, nil
}

// mapEvent validates a parsed event line and builds staging params.
// Evidence carries the six provenance keys the spec pins; block_ts is the
// unix timestamp the scanner resolved off-band (eth_getLogs has none).
func (p *freezePipeline) mapEvent(ev stablefreeze.EventLine,
	reg map[string]stablefreeze.Contract, netByContract map[string]string,
	runID pgtype.UUID) (db.StgInsertFreezeEventParams, bool) {

	c, ok := reg[strings.ToLower(ev.Contract)]
	if !ok {
		return db.StgInsertFreezeEventParams{}, false
	}
	byName := map[string]stablefreeze.EventSpec{}
	for _, e := range c.Events {
		byName[e.Name] = e
	}
	spec, ok := byName[ev.Event]
	if !ok || spec.Type != ev.Type {
		return db.StgInsertFreezeEventParams{}, false
	}
	net, ok := netByContract[c.Address]
	if !ok {
		return db.StgInsertFreezeEventParams{}, false
	}
	w, err := wallet.NormalizeEVM(ev.Wallet)
	if err != nil {
		return db.StgInsertFreezeEventParams{}, false
	}
	if !isTxHash(ev.TxHash) || ev.LogIndex < 0 || ev.BlockNumber <= 0 || ev.BlockTS <= 0 {
		return db.StgInsertFreezeEventParams{}, false
	}
	evidence, err := json.Marshal(map[string]any{
		"contract":     c.Address,
		"event":        spec.Name,
		"tx_hash":      strings.ToLower(ev.TxHash),
		"block_number": ev.BlockNumber,
		"block_ts":     ev.BlockTS,
		"topic":        spec.Topic,
	})
	if err != nil {
		return db.StgInsertFreezeEventParams{}, false
	}
	return db.StgInsertFreezeEventParams{
		RunID:           runID,
		ContractAddress: c.Address,
		EventType:       spec.Type,
		WalletAddress:   w,
		TxHash:          strings.ToLower(ev.TxHash),
		LogIndex:        int32(ev.LogIndex),
		BlockNumber:     ev.BlockNumber,
		BlockTs:         pgtype.Timestamptz{Time: time.Unix(ev.BlockTS, 0).UTC(), Valid: true},
		Fingerprint:     stablefreeze.Fingerprint(net, w, "FROZEN", c.Label),
		Label:           c.Label,
		Evidence:        evidence,
	}, true
}

func isTxHash(s string) bool {
	if len(s) != 66 || !strings.HasPrefix(strings.ToLower(s), "0x") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

// Validate gates publish on the header windows and staged counts. Rules:
// header contracts ⊆ registry; parsed == staged + rejected exactly;
// no staged event may sit above its contract's scanned bound; normal
// windows require toBlock ≥ fromBlock; empty windows allow toBlock=from-1.
func (p *freezePipeline) Validate(ctx context.Context, qc *db.Queries,
	runID pgtype.UUID, meta Meta) error {
	rm, ok := meta.(*freezeRunMeta)
	if !ok {
		return fmt.Errorf("freeze validate: unexpected meta type %T", meta)
	}
	reg := stablefreeze.RegistryByAddress()
	winByContract := map[string]stablefreeze.WindowMeta{}
	for _, w := range rm.windows {
		if _, ok := reg[strings.ToLower(w.Address)]; !ok {
			return fmt.Errorf("freeze validate: unknown contract %s in header", w.Address)
		}
		if w.Empty {
			if w.ToBlock != w.FromBlock-1 {
				return fmt.Errorf("freeze validate: empty window %s must satisfy toBlock=fromBlock-1 (%d != %d)",
					w.Address, w.ToBlock, w.FromBlock-1)
			}
		} else if w.ToBlock < w.FromBlock {
			return fmt.Errorf("freeze validate: window %s toBlock %d < fromBlock %d",
				w.Address, w.ToBlock, w.FromBlock)
		}
		if w.ToBlock > w.Head {
			return fmt.Errorf("freeze validate: window %s toBlock %d above head %d",
				w.Address, w.ToBlock, w.Head)
		}
		winByContract[strings.ToLower(w.Address)] = w
	}

	total, err := qc.StgFreezeEventTotal(ctx, runID)
	if err != nil {
		return fmt.Errorf("freeze validate: staged count: %w", err)
	}
	if total+rm.rejected != rm.parsed {
		return fmt.Errorf("freeze validate: reconciliation failed (staged %d + rejected %d != parsed %d)",
			total, rm.rejected, rm.parsed)
	}
	maxes, err := qc.StgFreezeEventMaxBlocks(ctx, runID)
	if err != nil {
		return fmt.Errorf("freeze validate: max blocks: %w", err)
	}
	for _, m := range maxes {
		w, ok := winByContract[strings.ToLower(m.ContractAddress)]
		if !ok {
			return fmt.Errorf("freeze validate: staged events for contract %s missing from header",
				m.ContractAddress)
		}
		if m.Mx > w.ToBlock {
			return fmt.Errorf("freeze validate: staged event block %d above scanned toBlock %d for %s",
				m.Mx, w.ToBlock, m.ContractAddress)
		}
		if m.Mx > w.Head {
			return fmt.Errorf("freeze validate: staged event block %d above head %d for %s",
				m.Mx, w.Head, m.ContractAddress)
		}
	}
	return nil
}

// Publish: staged → live inside the service-managed tx. Order: ensure
// wallets → freeze/destroy upsert (latest-per-fingerprint wins top-level
// evidence) → destroy append → unfreeze revoke → orphan unfreeze labels →
// cursor advance (GREATEST — never regresses) → snapshot row → run cursor
// summary. Absence is never a revoke signal.
func (p *freezePipeline) Publish(ctx context.Context, qtx *db.Queries,
	runID pgtype.UUID, sourceID int64, meta Meta) (string, publishStats, error) {
	rm, ok := meta.(*freezeRunMeta)
	if !ok {
		return "", publishStats{}, fmt.Errorf("freeze publish: unexpected meta type %T", meta)
	}
	stats := publishStats{}

	// network_id per contract for the cursor-advance step.
	states, err := qtx.GetFreezeScanStates(ctx)
	if err != nil {
		return "", stats, fmt.Errorf("freeze publish: cursors: %w", err)
	}
	netByContract := map[string]int64{}
	for _, st := range states {
		netByContract[strings.ToLower(st.ContractAddress)] = st.NetworkID
	}

	if _, err := qtx.PublishFreezeEnsureWallets(ctx, runID); err != nil {
		return "", stats, fmt.Errorf("freeze publish wallets: %w", err)
	}
	n, err := qtx.PublishFreezeUpsertLabels(ctx,
		db.PublishFreezeUpsertLabelsParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("freeze publish labels: %w", err)
	}
	stats.upserted += n
	n, err = qtx.PublishFreezeAppendDestroys(ctx,
		db.PublishFreezeAppendDestroysParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("freeze publish destroys: %w", err)
	}
	stats.upserted += n
	n, err = qtx.PublishFreezeRevoke(ctx,
		db.PublishFreezeRevokeParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("freeze publish revocations: %w", err)
	}
	stats.revoked += n
	n, err = qtx.PublishFreezeOrphanUnfreeze(ctx,
		db.PublishFreezeOrphanUnfreezeParams{SourceID: sourceID, RunID: runID})
	if err != nil {
		return "", stats, fmt.Errorf("freeze publish orphan unfreeze: %w", err)
	}
	stats.upserted += n

	// Cursors advance inside the same tx — a crash before commit leaves
	// both labels and cursors untouched (re-scan is idempotent).
	type kv struct {
		c string
		v int64
	}
	summary := make([]kv, 0, len(rm.windows))
	for _, w := range rm.windows {
		summary = append(summary, kv{strings.ToLower(w.Address), w.ToBlock})
	}
	sort.Slice(summary, func(i, j int) bool { return summary[i].c < summary[j].c })
	parts := make([]string, 0, len(summary))
	for _, e := range summary {
		nid, ok := netByContract[e.c]
		if !ok {
			return "", stats, fmt.Errorf("freeze publish: no cursor row for %s", e.c)
		}
		if err := qtx.AdvanceFreezeScanState(ctx, db.AdvanceFreezeScanStateParams{
			NetworkID:        nid,
			ContractAddress:  e.c,
			LastScannedBlock: e.v,
			LastHeadBlock:    rm.head,
		}); err != nil {
			return "", stats, fmt.Errorf("freeze publish cursor %s: %w", e.c, err)
		}
		parts = append(parts, fmt.Sprintf("%s:%d", e.c, e.v))
	}

	version := fmt.Sprintf("freeze-%d-%d-%s", rm.head, p.now().Unix(),
		hex.EncodeToString(runID.Bytes[:4]))
	if _, err := qtx.InsertDataSourceSnapshot(ctx, db.InsertDataSourceSnapshotParams{
		SourceID: sourceID,
		Version:  version,
		Checksum: rm.checksum,
		RowCount: rm.parsed,
	}); err != nil {
		return "", stats, fmt.Errorf("freeze snapshot row: %w", err)
	}
	if err := qtx.UpdateRunCursorValue(ctx, db.UpdateRunCursorValueParams{
		ID:          runID,
		CursorValue: pgtype.Text{String: strings.Join(parts, ","), Valid: true},
	}); err != nil {
		return "", stats, fmt.Errorf("freeze run cursor: %w", err)
	}
	return version, stats, nil
}

func (p *freezePipeline) LatestVersion(ctx context.Context, qc *db.Queries,
	sourceID int64) (string, string, error) {
	row, err := qc.GetLatestDataSourceSnapshot(ctx, sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return row.Version, row.Checksum, nil
}

func (p *freezePipeline) StagingNames() []string { return []string{"freeze_events"} }
