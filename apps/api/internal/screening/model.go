// Package screening orchestrates one screening run (spec §9.6–9.7,
// plan §3): resolve wallet → gather sanctions/labels/provider inputs →
// map to scoring.FindingSet → score under policy v1 → compose the §9.7
// result → persist the terminal row. Direct attribution, direct labels,
// and provider-reported exposure stay strictly separated; absence of
// data is never presented as safety.
package screening

import (
	"time"

	"checkwallet/internal/enrichment"
	"checkwallet/internal/label"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/scoring"
)

// Screening statuses (CHECK constraint vocabulary — PENDING is a
// transient claim state, never persisted).
const (
	StatusCompleted        = "COMPLETED"
	StatusPartial          = "PARTIAL"
	StatusInsufficientData = "INSUFFICIENT_DATA"
	StatusFailed           = "FAILED"
)

// data_status vocabulary (sanctions/labels share it; NOT_REQUESTED
// marks a dimension excluded by request options).
const (
	DataAvailable    = "AVAILABLE"
	DataStale        = "STALE"
	DataUnavailable  = "UNAVAILABLE"
	DataNotRequested = "NOT_REQUESTED"
)

// Request is the §9.6 POST body.
type Request struct {
	Network string     `json:"network"`
	Address string     `json:"address"`
	Options *OptionsIn `json:"options,omitempty"`
}

// OptionsIn carries optional §9.6 options — pointers distinguish an
// explicit false from an omitted option (defaults never erase intent).
type OptionsIn struct {
	IncludeSanctions *bool   `json:"include_sanctions,omitempty"`
	IncludeLabels    *bool   `json:"include_labels,omitempty"`
	IncludeExposure  *bool   `json:"include_exposure,omitempty"`
	MaxHops          *int    `json:"max_hops,omitempty"`
	RefreshProviders *bool   `json:"refresh_providers,omitempty"`
	PolicyVersion    *string `json:"policy_version,omitempty"`
}

// Options is the defaulted option set (plan §3.6). PolicyVersion is
// ,omitempty: an absent option marshals byte-identically to pre-v1.1
// clients, so idempotency hashes never shift for them (A5).
type Options struct {
	IncludeSanctions bool   `json:"include_sanctions"`
	IncludeLabels    bool   `json:"include_labels"`
	IncludeExposure  bool   `json:"include_exposure"`
	MaxHops          int    `json:"max_hops"`
	RefreshProviders bool   `json:"refresh_providers"`
	PolicyVersion    string `json:"policy_version,omitempty"`
}

// DefaultOptions returns the §3.6 defaults: all includes on,
// max_hops 2, no refresh.
func DefaultOptions() Options {
	return Options{
		IncludeSanctions: true, IncludeLabels: true,
		IncludeExposure: true, MaxHops: 2,
	}
}

// Resolve applies defaults over the optional input — an explicit false
// or zero survives; only omitted fields take defaults.
func (o *OptionsIn) Resolve() Options {
	d := DefaultOptions()
	if o == nil {
		return d
	}
	if o.IncludeSanctions != nil {
		d.IncludeSanctions = *o.IncludeSanctions
	}
	if o.IncludeLabels != nil {
		d.IncludeLabels = *o.IncludeLabels
	}
	if o.IncludeExposure != nil {
		d.IncludeExposure = *o.IncludeExposure
	}
	if o.MaxHops != nil {
		d.MaxHops = *o.MaxHops
	}
	if o.RefreshProviders != nil {
		d.RefreshProviders = *o.RefreshProviders
	}
	if o.PolicyVersion != nil {
		d.PolicyVersion = *o.PolicyVersion
	}
	return d
}

// CanonicalRequest is the idempotency identity: resolved network,
// normalized address, defaulted options — never raw body bytes.
// RawAddress carries the caller's original representation for the
// wallet row's `address` column (original representation preserved per
// data rules); it is excluded from the request hash — two spellings of
// one address are one idempotent request.
type CanonicalRequest struct {
	Network    string  `json:"network"`
	Address    string  `json:"address"`
	RawAddress string  `json:"-"`
	Options    Options `json:"options"`
}

// ---- §9.7 result model ----

// Result is the synchronous §9.7 response body. It is serialized once
// and stored as type JSON — replays return it byte-identically.
type Result struct {
	ScreeningID      string           `json:"screening_id"`
	Network          string           `json:"network"`
	Address          string           `json:"address"`
	Status           string           `json:"status"`
	RiskScore        *int             `json:"risk_score"`
	RiskLevel        string           `json:"risk_level"`
	DirectSanctions  SanctionsBlock   `json:"direct_sanctions"`
	DirectLabels     LabelsBlock      `json:"direct_labels"`
	Exposures        ExposuresBlock   `json:"exposures"`
	Scoring          ScoringBlock     `json:"scoring"`
	Evidence         []EvidenceRef    `json:"evidence"`
	ProviderStatus   ProviderStatus   `json:"provider_status"`
	DataCompleteness DataCompleteness `json:"data_completeness"`
	ScreenedAt       time.Time        `json:"screened_at"`
}

// SanctionsBlock is the direct-sanctions dimension. Evaluated=false
// means the dimension was not requested — never a clean verdict.
type SanctionsBlock struct {
	Evaluated bool                      `json:"evaluated"`
	Matched   bool                      `json:"matched"`
	Entries   []sanctions.SanctionEntry `json:"entries"`
}

// LabelsBlock is the direct-labels dimension (cursor-collected, never
// 200-truncated; the hard 1000 cap surfaces via Truncated).
type LabelsBlock struct {
	Evaluated bool         `json:"evaluated"`
	Items     []label.Item `json:"items"`
	Collected int          `json:"collected"`
	Truncated bool         `json:"truncated"`
}

// ExposureItem is one provider-reported indirect exposure — basis is
// always provider_reported; a graph path never implies ownership.
// Scored records whether hops satisfied max_hops ∧ ≤2.
type ExposureItem struct {
	Counterparty string   `json:"counterparty"`
	Category     string   `json:"category"`
	Hops         int      `json:"hops"`
	Direction    string   `json:"direction,omitempty"`
	Score        *float64 `json:"provider_score,omitempty"`
	Source       string   `json:"source"`
	Basis        string   `json:"basis"`
	Scored       bool     `json:"scored"`
	// v1.1 additive evidence fields — all omitempty (A5 wire parity).
	SourceAddress   string   `json:"source_address,omitempty"`
	MeaningfulCount int      `json:"meaningful_count,omitempty"`
	TxHashes        []string `json:"tx_hashes,omitempty"`
	Coverage        string   `json:"coverage,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	TaintKind       string   `json:"taint_kind,omitempty"`
}

// ExposuresBlock carries provider-reported exposures — never under
// transaction_evidence (own-graph tracing is phase 6).
type ExposuresBlock struct {
	Evaluated bool           `json:"evaluated"`
	Items     []ExposureItem `json:"items"`
	Coverage  Coverage       `json:"coverage"`
}

// Coverage documents tracing limits honestly.
type Coverage struct {
	Tracing           string `json:"tracing"` // provider_reported_only in P6
	MaxHopsRequested  int    `json:"max_hops_requested"`
	DisabledByRequest bool   `json:"disabled_by_request,omitempty"`
	// v1.1 coverage-honesty fields — all omitempty (A5).
	OnchainCoverage          string `json:"onchain_coverage,omitempty"`
	SourcesEvaluated         int    `json:"sources_evaluated,omitempty"`
	SourcesReceiverUnchecked int    `json:"sources_receiver_unchecked,omitempty"`
}

// ScoringBlock mirrors the engine verdict plus the policy version.
type ScoringBlock struct {
	PolicyVersion string           `json:"policy_version"`
	Score         *int             `json:"score"`
	RiskLevel     string           `json:"risk_level"`
	Flags         []string         `json:"flags"`
	Blocks        scoring.Blocks   `json:"blocks"`
	Reasons       []scoring.Reason `json:"reasons"`
}

// EvidenceRef joins reasons to proof: {kind, ref} where ref is
// wallet_labels.id for labels, {sanctions_entity_id} for sanctions
// rows, {source, fetched_at} for provider observations, and the same
// composite for cross-chain informational listings.
type EvidenceRef struct {
	Kind string `json:"kind"`
	Ref  any    `json:"ref"`
}

// SanctionsEvidence identifies a sanctions row behind a reason.
type SanctionsEvidence struct {
	EntityID    int64   `json:"sanctions_entity_id"`
	NetworkCode *string `json:"network_code,omitempty"`
}

// ProviderEvidence identifies a provider observation/score/exposure.
type ProviderEvidence struct {
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Identity  string     `json:"identity,omitempty"`
}

// ProviderStatusEntry is one source's outcome — OK, a typed failure,
// or DISABLED (configuration, never a failure in the fold).
type ProviderStatusEntry struct {
	Status        string                `json:"status"`
	Enabled       bool                  `json:"enabled"`
	Cached        bool                  `json:"cached"`
	FetchedAt     *time.Time            `json:"fetched_at,omitempty"`
	Error         *enrichment.ErrorInfo `json:"error,omitempty"`
	ProviderScore *float64              `json:"provider_score,omitempty"`
}

// SnapshotStatus is one input dimension's freshness.
type SnapshotStatus struct {
	DataStatus   string     `json:"data_status"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
}

// ProviderStatus is the §9.7 provider_status block: per-source statuses
// verbatim plus snapshot data_status for the local dimensions. Fanout
// reports a wholesale enrichment-pipeline failure — a named sibling
// field, not a pseudo-provider key, so it can never collide with a
// real source code.
type ProviderStatus struct {
	Providers map[string]ProviderStatusEntry `json:"providers"`
	Fanout    *ProviderStatusEntry           `json:"fanout,omitempty"`
	Sanctions SnapshotStatus                 `json:"sanctions"`
	Labels    SnapshotStatus                 `json:"labels"`
}

// DimensionStatus is one input's availability in the fold.
type DimensionStatus struct {
	Requested  bool   `json:"requested"`
	DataStatus string `json:"data_status"`
	Usable     bool   `json:"usable"`
	Stale      bool   `json:"stale,omitempty"`
}

// ProvidersCompleteness folds provider availability without DISABLED —
// a provider that made no call is configuration, not a failure.
type ProvidersCompleteness struct {
	Usable   []string `json:"usable"`
	Degraded []string `json:"degraded"`
	Disabled []string `json:"disabled"`
}

// DataCompleteness is the §9.7 data_completeness block: per-dimension
// availability, staleness, and label truncation. It always accompanies
// a LOW score — LOW never means "proven safe".
type DataCompleteness struct {
	Sanctions       DimensionStatus       `json:"sanctions"`
	Labels          DimensionStatus       `json:"labels"`
	LabelsCollected int                   `json:"labels_collected"`
	LabelsTruncated bool                  `json:"labels_truncated"`
	Providers       ProvidersCompleteness `json:"providers"`
	Exposure        DimensionStatus       `json:"exposure"`
}
