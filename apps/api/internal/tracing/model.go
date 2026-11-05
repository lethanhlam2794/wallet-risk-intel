// Package tracing implements bounded 1–5 hop exposure tracing
// (plan §3.3, phase 7): ordered-position pull, contiguous-prefix
// assembly, per-kind matched_signals, cross-depth counterparty dedup,
// and a coverage envelope that reports bounds honestly — a truncated
// trace is never a clean one.
package tracing

import (
	"time"

	"checkwallet/internal/transaction"
)

// Truncated-reason enum (closed; plan §3.3.4 precedence order).
const (
	TruncDeadline = "deadline"
	TruncMaxReqs  = "max_provider_requests"
	TruncMaxNodes = "max_nodes"
	TruncMaxEdges = "max_edges"
	TruncRespCap  = "response_cap"
	TruncLimit    = "limit"
)

// Item categories.
const (
	CatSanctioned = "SANCTIONED"
	CatCrossChain = "cross_chain_listing"
)

// Signal kinds.
const (
	SigSanctionsDirect = "sanctions_direct"
	SigLabel           = "label"
	SigListing         = "listing"
)

// Relationships — never ownership language (invariant).
const (
	RelDirect   = "direct_counterparty"
	RelIndirect = "indirect_hop"
)

// Request is the validated /exposures parameter set.
type Request struct {
	Direction string // "", "in", "out"
	MaxHops   int    // validated {1..5}
	Category  string // post-assembly matched_signals filter
	Limit     int
	Refresh   bool
}

// EntityRef is a matched-signal entity pointer.
type EntityRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ProgramRef is a matched-signal program pointer.
type ProgramRef struct {
	Code string  `json:"code"`
	Name *string `json:"name"`
}

// MatchedSignal is one entry of matched_signals[] — per-kind fields.
type MatchedSignal struct {
	Kind               string       `json:"kind"`
	Category           string       `json:"category"`
	Label              string       `json:"label,omitempty"`
	Entity             *EntityRef   `json:"entity,omitempty"`
	Programs           []ProgramRef `json:"programs,omitempty"`
	Source             string       `json:"source"`
	VerificationStatus string       `json:"verification_status,omitempty"`
	Network            *string      `json:"network,omitempty"`
	ListedAddress      string       `json:"listed_address,omitempty"`
}

// Edge is one §11.5 evidence edge (+ transfer_index, declared).
type Edge struct {
	FromAddress        string     `json:"from_address"`
	ToAddress          string     `json:"to_address"`
	Network            string     `json:"network"`
	TxHash             string     `json:"tx_hash"`
	TransferIndex      string     `json:"transfer_index"`
	TransferType       string     `json:"transfer_type"`
	Asset              string     `json:"asset"`
	AmountRaw          string     `json:"amount_raw"`
	BlockNumber        *int64     `json:"block_number"`
	BlockTimestamp     *time.Time `json:"block_timestamp"`
	ConfirmationStatus string     `json:"confirmation_status"`
}

// Path is one evidence path — hop edges along a node sequence.
type Path struct {
	HopCount           int    `json:"hop_count"`
	PathVerified       bool   `json:"path_verified"` // constant false in MVP
	TemporalOrderValid bool   `json:"temporal_order_valid"`
	Edges              []Edge `json:"edges"`
}

// Item is one matched counterparty. ExposureScore is the exposure-v1
// per-item evidence weight — null when every signal is zero-weight
// (informational-only); it never feeds screening_results.risk_score.
type Item struct {
	Counterparty        string          `json:"counterparty"`
	Category            string          `json:"category"`
	MatchedSignals      []MatchedSignal `json:"matched_signals"`
	Relationship        string          `json:"relationship"`
	Hops                int             `json:"hops"`
	ExposureScore       *int            `json:"exposure_score"`
	Paths               []Path          `json:"paths"`
	TransactionEvidence []Edge          `json:"transaction_evidence"`

	minPos int // ordering: min contributing position index (not on wire)
}

// RootCoverage mirrors the index-state envelope for the root wallet.
type RootCoverage struct {
	IndexStatus       string                  `json:"index_status"`
	Stale             bool                    `json:"stale"`
	ProviderStatus    *string                 `json:"provider_status"`
	IndexedUntilBlock *int64                  `json:"indexed_until_block"`
	IndexedAt         *time.Time              `json:"indexed_at"`
	HistoryTruncated  bool                    `json:"history_truncated"`
	IndexScope        string                  `json:"index_scope"`
	RowsIndexed       int64                   `json:"rows_indexed"`
	RowsSkipped       transaction.RowsSkipped `json:"rows_skipped"`
}

// Coverage is the exposures envelope (plan §3.3.8, phase-7 §2.7).
// The *_by_hop maps are keyed by materialized frontier index {2..max_hops}.
type Coverage struct {
	CounterpartiesTotal      int               `json:"counterparties_total"`
	NodesExamined            int               `json:"nodes_examined"`
	PositionsEmitted         int               `json:"positions_emitted"`
	NodesRemaining           int               `json:"nodes_remaining"`
	NodesFailed              int               `json:"nodes_failed"`
	EdgesExamined            int64             `json:"edges_examined"`
	ProviderCalls            int64             `json:"provider_calls"`
	ProviderStatus           *string           `json:"provider_status"`
	SanctionsSnapshotVersion *string           `json:"sanctions_snapshot_version"`
	LabelsSnapshotVersion    *string           `json:"labels_snapshot_version"`
	NodeIndexedUntil         map[string]*int64 `json:"node_indexed_until"`
	HeadBlock                *int64            `json:"head_block"`
	FrontierByHop            map[int]int       `json:"frontier_by_hop"`
	NodesExaminedByHop       map[int]int       `json:"nodes_examined_by_hop"`
	FrontierEmittedByHop     map[int]int       `json:"frontier_emitted_by_hop"`
	NodesRemainingByHop      map[int]int       `json:"nodes_remaining_by_hop"`
	MaxDepthReached          int               `json:"max_depth_reached"`
	Root                     RootCoverage      `json:"root"`
}

// Result is the /exposures response payload. ExposurePolicyVersion is
// always emitted — the per-item exposure_score semantics are
// reproducible by version, never folded into risk_score.
type Result struct {
	Items                 []Item   `json:"items"`
	Coverage              Coverage `json:"coverage"`
	Truncated             *string  `json:"truncated"`
	TracingMethod         string   `json:"tracing_method"` // "bounded_bfs"
	ExposurePolicyVersion string   `json:"exposure_policy_version"`
}
