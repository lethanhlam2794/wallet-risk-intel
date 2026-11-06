package stablefreeze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
	"checkwallet/internal/wallet"
)

// ScanCursor is the package's view of one freeze_scan_state row — kept
// db-type-free so tests fake the store without a pool.
type ScanCursor struct {
	NetworkID        int64
	ContractAddress  string
	GenesisBlock     int64
	LastScannedBlock int64
	LastHeadBlock    int64
}

// CursorStore reads freeze_scan_state. dbCursorStore adapts sqlc.
type CursorStore interface {
	GetFreezeScanStates(ctx context.Context) ([]ScanCursor, error)
}

// NewCursorStore adapts *db.Queries for production wiring.
func NewCursorStore(q *db.Queries) CursorStore { return dbCursorStore{q} }

type dbCursorStore struct{ q *db.Queries }

func (d dbCursorStore) GetFreezeScanStates(ctx context.Context) ([]ScanCursor, error) {
	rows, err := d.q.GetFreezeScanStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ScanCursor, 0, len(rows))
	for _, r := range rows {
		out = append(out, ScanCursor{
			NetworkID:        r.NetworkID,
			ContractAddress:  r.ContractAddress,
			GenesisBlock:     r.GenesisBlock,
			LastScannedBlock: r.LastScannedBlock,
			LastHeadBlock:    r.LastHeadBlock,
		})
	}
	return out, nil
}

// Config bounds the scan. Defaults mirror the FREEZE_SCAN_* env defaults.
type Config struct {
	BlockRange      int64  // max blocks per eth_getLogs call
	MaxBlocksPerRun int64  // per-contract span cap per run
	Confirmations   int64  // scan floor = head - confirmations (reorg margin)
	OutputDir       string // artifact dir; "" = os temp dir
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{BlockRange: 50000, MaxBlocksPerRun: 2000000, Confirmations: 12}
}

// WindowMeta is the per-contract scan window the header records — the
// pipeline validates and the publish step advances cursors by it.
type WindowMeta struct {
	Address   string `json:"address"`
	FromBlock int64  `json:"from_block"`
	ToBlock   int64  `json:"to_block"`
	Head      int64  `json:"head"`
	Empty     bool   `json:"empty"`
	Rejected  int64  `json:"rejected"`
}

// EventLine is one staged-on-disk event record (JSONL line ≥2).
type EventLine struct {
	Contract    string `json:"contract"`
	Event       string `json:"event"`
	Type        string `json:"type"`
	Wallet      string `json:"wallet"`
	TxHash      string `json:"tx_hash"`
	LogIndex    int64  `json:"log_index"`
	BlockNumber int64  `json:"block_number"`
	BlockTS     int64  `json:"block_ts"` // unix seconds; eth_getLogs has no ts
}

// fileHeader is JSONL line 1.
type fileHeader struct {
	Version   int          `json:"version"`
	Contracts []WindowMeta `json:"contracts"`
}

// Scanner implements provider.SnapshotSource: reads cursors, scans
// bounded eth_getLogs windows, resolves block timestamps, and writes a
// versioned JSONL artifact. RPC failures fail the whole download —
// cursors move only when a complete artifact commits at publish.
type Scanner struct {
	cursors CursorStore
	rpc     *RPCClient
	cfg     Config
	now     func() time.Time
}

// ScannerOption tweaks the scanner (tests only).
type ScannerOption func(*Scanner)

// WithScannerClock overrides the timestamp clock (tests).
func WithScannerClock(now func() time.Time) ScannerOption {
	return func(s *Scanner) { s.now = now }
}

// NewScanner builds a scanner; cfg zero values fall back to defaults.
func NewScanner(cursors CursorStore, rpc *RPCClient, cfg Config, opts ...ScannerOption) (*Scanner, error) {
	if cursors == nil {
		return nil, fmt.Errorf("stablefreeze: cursor store required")
	}
	if rpc == nil {
		return nil, fmt.Errorf("stablefreeze: rpc client required")
	}
	def := DefaultConfig()
	if cfg.BlockRange <= 0 {
		cfg.BlockRange = def.BlockRange
	}
	if cfg.MaxBlocksPerRun <= 0 {
		cfg.MaxBlocksPerRun = def.MaxBlocksPerRun
	}
	if cfg.Confirmations < 0 {
		cfg.Confirmations = def.Confirmations
	}
	s := &Scanner{cursors: cursors, rpc: rpc, cfg: cfg, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Download scans all registered contracts and returns a single-file
// snapshot whose artifact is a JSONL: header line then event lines.
// FinalURL stays empty — there is no HTTP resource URL behind a
// chain scan.
func (s *Scanner) Download(ctx context.Context) (provider.Snapshot, error) {
	states, err := s.cursors.GetFreezeScanStates(ctx)
	if err != nil {
		return provider.Snapshot{}, fmt.Errorf("stablefreeze: cursor read: %w", err)
	}
	if len(states) == 0 {
		return provider.Snapshot{}, fmt.Errorf("stablefreeze: no freeze_scan_state rows — run migrations")
	}
	byContract := make(map[string]ScanCursor, len(states))
	for _, st := range states {
		byContract[strings.ToLower(st.ContractAddress)] = st
	}
	// Fail closed: every registered contract MUST have a cursor row —
	// scanning without one would mint unbounded labels from genesis.
	reg := Registry()
	for _, c := range reg {
		if _, ok := byContract[c.Address]; !ok {
			return provider.Snapshot{}, fmt.Errorf(
				"stablefreeze: missing cursor row for %s", c.Address)
		}
	}

	head, err := s.rpc.BlockNumber(ctx)
	if err != nil {
		return provider.Snapshot{}, fmt.Errorf("stablefreeze: head: %w", err)
	}
	floor := head - s.cfg.Confirmations

	windows := make([]WindowMeta, 0, len(reg))
	var events []EventLine
	for _, c := range reg {
		cur := byContract[c.Address]
		from := cur.LastScannedBlock + 1
		scanTo := min(floor, cur.LastScannedBlock+s.cfg.MaxBlocksPerRun)
		if scanTo <= cur.LastScannedBlock {
			// Degenerate window (head−conf ≤ cursor): honest no-op window —
			// header-only section, nothing staged, cursor untouched.
			windows = append(windows, WindowMeta{
				Address: c.Address, FromBlock: from, ToBlock: from - 1,
				Head: head, Empty: true,
			})
			continue
		}
		win := WindowMeta{Address: c.Address, FromBlock: from, ToBlock: scanTo, Head: head}
		evs, rejected, err := s.scanWindow(ctx, c, from, scanTo)
		if err != nil {
			return provider.Snapshot{}, err
		}
		win.Rejected = rejected
		windows = append(windows, win)
		events = append(events, evs...)
	}

	// eth_getLogs carries no timestamp — resolve distinct blocks with one
	// sparse batched eth_getBlockByNumber pass.
	if len(events) > 0 {
		blocks := map[int64]bool{}
		for _, e := range events {
			blocks[e.BlockNumber] = true
		}
		nums := make([]int64, 0, len(blocks))
		for n := range blocks {
			nums = append(nums, n)
		}
		sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
		ts, err := s.rpc.BlockTimestamps(ctx, nums)
		if err != nil {
			return provider.Snapshot{}, fmt.Errorf("stablefreeze: block ts: %w", err)
		}
		for i := range events {
			events[i].BlockTS = ts[events[i].BlockNumber].Unix()
		}
	}

	f, err := s.writeArtifact(windows, events)
	if err != nil {
		return provider.Snapshot{}, err
	}
	return provider.Snapshot{
		Files:     []provider.File{f},
		Checksum:  provider.CombinedChecksum([]provider.File{f}),
		SizeBytes: f.SizeBytes,
	}, nil
}

// scanWindow pages eth_getLogs over [from, to] in BlockRange chunks and
// converts well-formed logs into event lines. Malformed logs are skipped
// and counted — they never abort the scan.
func (s *Scanner) scanWindow(ctx context.Context, c Contract,
	from, to int64) ([]EventLine, int64, error) {

	byTopic := c.EventByTopic()
	var out []EventLine
	var rejected int64
	for lo := from; lo <= to; lo += s.cfg.BlockRange {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		hi := min(lo+s.cfg.BlockRange-1, to)
		logs, err := s.rpc.GetLogs(ctx, c.Address, c.TopicList(), lo, hi)
		if err != nil {
			return nil, 0, fmt.Errorf("stablefreeze %s getLogs [%d,%d]: %w",
				c.Symbol, lo, hi, err)
		}
		for _, lg := range logs {
			e, ok := parseLog(lg, c, byTopic)
			if !ok {
				rejected++
				continue
			}
			out = append(out, e)
		}
	}
	return out, rejected, nil
}

// parseLog converts one raw log into an event line; ok=false marks the
// entry malformed (skipped, counted).
func parseLog(lg RawLog, c Contract, byTopic map[string]EventSpec) (EventLine, bool) {
	if lg.Removed {
		return EventLine{}, false
	}
	if len(lg.Topics) == 0 {
		return EventLine{}, false
	}
	spec, ok := byTopic[strings.ToLower(lg.Topics[0])]
	if !ok {
		return EventLine{}, false
	}
	if !strings.EqualFold(lg.Address, c.Address) || lg.TransactionHash == "" {
		return EventLine{}, false
	}
	addr, ok := subjectAddress(lg, spec)
	if !ok {
		return EventLine{}, false
	}
	walletAddr, err := wallet.NormalizeEVM(addr)
	if err != nil {
		return EventLine{}, false
	}
	bn, ok1 := hexInt(lg.BlockNumber)
	li, ok2 := hexInt(lg.LogIndex)
	if !ok1 || !ok2 {
		return EventLine{}, false
	}
	return EventLine{
		Contract:    c.Address,
		Event:       spec.Name,
		Type:        spec.Type,
		Wallet:      walletAddr,
		TxHash:      strings.ToLower(lg.TransactionHash),
		LogIndex:    li,
		BlockNumber: bn,
	}, true
}

// subjectAddress extracts the watched wallet from topics[1] (indexed,
// USDC) or the data tail (non-indexed, USDT).
func subjectAddress(lg RawLog, spec EventSpec) (string, bool) {
	if spec.Indexed {
		if len(lg.Topics) < 2 {
			return "", false
		}
		return tail20(lg.Topics[1])
	}
	return headWordTail20(lg.Data)
}

// tail20 takes the last 20 bytes of a 0x-hex blob — the address sits in
// the low bytes of a padded topic.
func tail20(hexstr string) (string, bool) {
	s := strings.TrimPrefix(strings.ToLower(hexstr), "0x")
	if len(s) < 40 || len(s)%2 != 0 {
		return "", false
	}
	return "0x" + s[len(s)-40:], true
}

// headWordTail20 reads the low 20 bytes of the FIRST 32-byte ABI word —
// the subject address for non-indexed event args. DestroyedBlackFunds
// carries (address,uint256) so the payload is two words and the address
// is word 0's tail, not the blob tail (that would be the amount).
func headWordTail20(hexstr string) (string, bool) {
	s := strings.TrimPrefix(strings.ToLower(hexstr), "0x")
	if len(s) < 64 || len(s)%2 != 0 {
		return "", false
	}
	return "0x" + s[24:64], true
}

// hexInt decodes a 0x-quantity to int64.
func hexInt(s string) (int64, bool) {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if s == "" {
		return 0, false
	}
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return 0, false
	}
	return n.Int64(), true
}

// writeArtifact materializes the JSONL file and hashes it.
func (s *Scanner) writeArtifact(windows []WindowMeta, events []EventLine) (provider.File, error) {
	f, err := os.CreateTemp(s.cfg.OutputDir, "stablefreeze-*.jsonl")
	if err != nil {
		return provider.File{}, fmt.Errorf("stablefreeze: temp file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	var written int64
	write := func(v any) error {
		var buf []byte
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf = append(buf, '\n')
		if _, err := f.Write(buf); err != nil {
			return err
		}
		h.Write(buf)
		written += int64(len(buf))
		return nil
	}
	if err := write(fileHeader{Version: 1, Contracts: windows}); err != nil {
		os.Remove(f.Name())
		return provider.File{}, fmt.Errorf("stablefreeze: header: %w", err)
	}
	for _, e := range events {
		if err := write(e); err != nil {
			os.Remove(f.Name())
			return provider.File{}, fmt.Errorf("stablefreeze: event: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		os.Remove(f.Name())
		return provider.File{}, fmt.Errorf("stablefreeze: sync: %w", err)
	}
	return provider.File{
		Path:      f.Name(),
		Name:      "freeze_events.jsonl",
		Checksum:  hex.EncodeToString(h.Sum(nil)),
		SizeBytes: written,
	}, nil
}
