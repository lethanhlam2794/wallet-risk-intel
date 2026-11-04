// Package transaction owns lazy on-demand transaction indexing (plan §3.2)
// and the /transactions read path (plan §3.5): per-wallet ascending
// watermark windows, singleflight per (network,wallet), content-derived
// transfer identity, and coverage-honest envelopes — a provider failure or
// bounded fetch is always visible, never a clean-looking empty index.
package transaction

import (
	"errors"
	"sync/atomic"
	"time"

	"checkwallet/internal/provider"
)

// ErrInvalidInput marks bad filter input → HTTP 400.
var ErrInvalidInput = errors.New("invalid input")

// ErrInvalidCursor marks a malformed page cursor → HTTP 400 INVALID_CURSOR.
var ErrInvalidCursor = errors.New("invalid cursor")

// Index statuses (wallet_index_state.status vocabulary, lowercase).
const (
	StatusNotIndexed = "not_indexed"
	StatusIndexing   = "indexing"
	StatusFresh      = "fresh"
	StatusPartial    = "partial"
	StatusFailed     = "failed"
	StatusDisabled   = "disabled"
)

// IndexScope is the declared transfer scope in MVP (plan §6): INTERNAL and
// NFT classes are excluded — coverage says so explicitly.
const IndexScope = "native+token"

// RowsSkipped carries the persisted rows_skipped_* counters on the wire.
type RowsSkipped struct {
	MissingContract int64 `json:"missing_contract"`
	InvalidAddress  int64 `json:"invalid_address"`
}

// EnsureOptions constrains one EnsureIndexed call.
type EnsureOptions struct {
	Refresh  bool    // bypasses freshness only — the FAILED cooldown holds
	Budget   *Budget // request-global provider-call pool; nil = unlimited
	MaxPages int     // stream-pair step bound; 0 → service default (hop-0)
}

// IndexOutcome is EnsureIndexed's result: the terminal index state plus
// per-call facts (head observed, provider calls spent, whether upstream
// was touched at all).
type IndexOutcome struct {
	WalletID          int64       `json:"-"`
	IndexStatus       string      `json:"index_status"`
	Stale             bool        `json:"stale"`
	ProviderStatus    *string     `json:"provider_status"` // ErrKind | "ok" | null
	IndexedUntilBlock *int64      `json:"indexed_until_block"`
	IndexedAt         *time.Time  `json:"indexed_at"`
	HeadBlock         *int64      `json:"head_block"`
	HistoryTruncated  bool        `json:"history_truncated"`
	IndexScope        string      `json:"index_scope"`
	RowsIndexed       int64       `json:"rows_indexed"`
	RowsSkipped       RowsSkipped `json:"rows_skipped"`
	Fetched           bool        `json:"-"`
	ProviderCalls     int64       `json:"-"`
	BudgetExhausted   bool        `json:"-"`
	FetchErr          error       `json:"-"`
}

// Budget is the request-global provider-request pool shared by hop-0 and
// every hop-2 index (plan §3.3). One seam call (HeadBlock/LookupTransfers)
// prepays 1; the page's real UpstreamCalls settles the difference.
type Budget struct {
	limit int64
	used  atomic.Int64
}

// NewBudget builds a pool of n requests; n ≤ 0 means unlimited.
func NewBudget(n int64) *Budget { return &Budget{limit: n} }

// tryTake prepays n requests; false when the pool cannot cover them.
func (b *Budget) tryTake(n int64) bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	for {
		u := b.used.Load()
		if u+n > b.limit {
			return false
		}
		if b.used.CompareAndSwap(u, u+n) {
			return true
		}
	}
}

// charge settles requests beyond the prepaid one (may drive past limit —
// the next tryTake fails).
func (b *Budget) charge(n int64) {
	if b != nil && n > 0 {
		b.used.Add(n)
	}
}

// Used reports total requests charged.
func (b *Budget) Used() int64 {
	if b == nil {
		return 0
	}
	return b.used.Load()
}

// Exhausted reports whether the pool can cover another request.
func (b *Budget) Exhausted() bool {
	return b != nil && b.limit > 0 && b.used.Load() >= b.limit
}

// TxFilters constrains the /transactions listing.
type TxFilters struct {
	Direction string // "", "in", "out", "self"
	Asset     string // "", "native", or normalized contract address
	FromTs    *time.Time
	ToTs      *time.Time
	Cursor    string
	Limit     int
	Refresh   bool
}

// TxItem is one indexed transfer on the wire.
type TxItem struct {
	TxHash             string     `json:"tx_hash"`
	Direction          string     `json:"direction"` // in|out|self — the row's fact
	TransferType       string     `json:"transfer_type"`
	AssetIdentifier    *string    `json:"asset_identifier"`
	AmountRaw          string     `json:"amount_raw"`
	Counterparty       string     `json:"counterparty"`
	BlockNumber        *int64     `json:"block_number"`
	BlockTimestamp     *time.Time `json:"block_timestamp"`
	ConfirmationStatus string     `json:"confirmation_status"`
	IsError            bool       `json:"is_error"`
	TransferIndex      string     `json:"transfer_index"`
}

// ListResult is the /transactions envelope.
type ListResult struct {
	Items      []TxItem `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
	*IndexOutcome
}

// AdapterEntry binds a TxProvider to the network it may serve.
type AdapterEntry struct {
	Provider provider.TxProvider
}
