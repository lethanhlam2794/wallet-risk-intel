// Package scoring implements the versioned, deterministic risk-scoring
// engine (spec §10, plan §3.5). Evaluate is a pure function:
// same FindingSet + policy ⇒ byte-identical Verdict. No IO, no clock,
// no RNG, no provider/db/HTTP types — the screening service maps its
// gathered evidence into FindingSet and consumes the Verdict.
package scoring

// Finding kinds — the closed set the screening mapper emits.
const (
	// KindSanctionsDirect is an official direct sanctions match on the
	// screened wallet (OFAC published snapshot). Scored flat at 100 and
	// raises the DIRECT_SANCTIONS flag independently of the score.
	KindSanctionsDirect = "sanctions_direct"
	// KindLabel is a direct wallet label from the labels store.
	KindLabel = "label"
	// KindProviderObservation is a provider-reported risk signal on the
	// screened wallet — always reported, never verified.
	KindProviderObservation = "provider_observation"
	// KindProviderScore is a provider-emitted numeric score; only the
	// maximum band contributes to the other block (max, never sum).
	KindProviderScore = "provider_score"
	// KindExposure is a provider-reported or onchain-computed
	// counterparty exposure. Basis identifies the evidence class.
	KindExposure = "exposure"
	// KindProceedsReceiver (v1.1) marks ≥1 meaningful inbound from a
	// direct-tainted source — informational evidence, weight 0.
	KindProceedsReceiver = "proceeds_receiver"
	// KindPossibleVictimContact (v1.1) marks an outbound contact to a
	// tainted counterparty — informational evidence, weight 0.
	KindPossibleVictimContact = "possible_victim_contact"
	// KindCrossChainListing is a cross-chain address observation —
	// informational evidence, never a direct listing (AGENTS.md §5).
	KindCrossChainListing = "cross_chain_listing"
	// KindSnapshotStale marks a degraded (STALE) sanctions snapshot —
	// usable but visible.
	KindSnapshotStale = "snapshot_stale"
	// KindProviderFailed marks a typed provider failure — visible,
	// never converted into a clean result.
	KindProviderFailed = "provider_failed"
	// KindNotEvaluated marks a dimension excluded by request options;
	// it signals absence of evaluation, not absence of risk.
	KindNotEvaluated = "not_evaluated"
)

// Basis values.
const (
	// BasisProviderReported is the only exposure basis in policy v1 —
	// provider-reported counterparties, not graph-derived proof.
	BasisProviderReported = "provider_reported"
	// BasisOnchainComputed (v1.1) is computed from the indexed
	// transactions table at screening time — never provider data.
	BasisOnchainComputed = "onchain_computed"
)

// Verification statuses (wallet_labels.verification_status parity).
const (
	VerificationVerified   = "verified"
	VerificationReported   = "reported"
	VerificationUnverified = "unverified"
	VerificationDisputed   = "disputed"
)

// Confidence levels (wallet_labels.confidence parity).
const (
	ConfidenceHigh    = "high"
	ConfidenceMedium  = "medium"
	ConfidenceLow     = "low"
	ConfidenceUnknown = "unknown"
)

// Reason codes — closed enum (plan §3.5).
const (
	ReasonDirectSanctionsMatch = "DIRECT_SANCTIONS_MATCH"
	ReasonDirectLabel          = "DIRECT_LABEL"
	ReasonLabelClassification  = "LABEL_CLASSIFICATION"
	ReasonProviderObservation  = "PROVIDER_OBSERVATION"
	ReasonProviderScore        = "PROVIDER_SCORE"
	ReasonExposureProvider     = "EXPOSURE_PROVIDER"
	// v1.1 reason codes.
	ReasonExposureOnchainSource      = "EXPOSURE_ONCHAIN_SOURCE"
	ReasonExposureProviderSource     = "EXPOSURE_PROVIDER_SOURCE"
	ReasonExposureInformational      = "EXPOSURE_INFORMATIONAL"
	ReasonProceedsReceiver           = "PROCEEDS_RECEIVER"
	ReasonPossibleVictimContact      = "POSSIBLE_VICTIM_CONTACT"
	ReasonServiceClassifiedCandidate = "SERVICE_CLASSIFIED_CANDIDATE"
	ReasonCrossChainListing          = "CROSS_CHAIN_LISTING"
	ReasonSnapshotStale              = "SNAPSHOT_STALE"
	ReasonProviderFailed             = "PROVIDER_FAILED"
	ReasonNotEvaluated               = "NOT_EVALUATED"
)

// Flags.
const (
	FlagDirectSanctions = "DIRECT_SANCTIONS"
)

// Risk levels.
const (
	RiskCritical = "CRITICAL"
	RiskHigh     = "HIGH"
	RiskMedium   = "MEDIUM"
	RiskLow      = "LOW"
	RiskUnknown  = "UNKNOWN"
)

// Finding is one normalized unit of evidence. Dedup identity is
// {Kind}:{Category}:{Source}:{Ref} — labels key by fingerprint,
// provider observations by source+identity; cross-source mirrors stay
// distinct by design (block caps bound residual stacking).
type Finding struct {
	Kind               string `json:"kind"`
	Category           string `json:"category,omitempty"`
	CategoryType       string `json:"category_type,omitempty"`
	Source             string `json:"source,omitempty"`
	Ref                string `json:"ref,omitempty"`
	VerificationStatus string `json:"verification_status,omitempty"`
	Confidence         string `json:"confidence,omitempty"`
	Basis              string `json:"basis,omitempty"`
	Hops               int    `json:"hops,omitempty"`
	ProviderScore      *int   `json:"provider_score,omitempty"`
	// v1.1 evidence fields — all additive omitempty so v1 payloads
	// stay byte-identical.
	SourceAddress   string   `json:"source_address,omitempty"`
	Direction       string   `json:"direction,omitempty"` // "INBOUND" | "OUTBOUND"
	MeaningfulCount int      `json:"meaningful_count,omitempty"`
	TxHashes        []string `json:"tx_hashes,omitempty"`
	Coverage        string   `json:"coverage,omitempty"` // "complete" | "partial"
	Reason          string   `json:"reason,omitempty"`
	TaintKind       string   `json:"taint_kind,omitempty"` // "direct" | "chain"
}

// DedupKey is the plan §3.5 dedup identity.
func (f Finding) DedupKey() string {
	return f.Kind + ":" + f.Category + ":" + f.Source + ":" + f.Ref
}

// FindingSet is the engine input: normalized findings plus the
// usability signal computed by the availability fold.
type FindingSet struct {
	// HasUsableData is false when no evaluated dimension produced
	// usable evidence — score is then NULL, never 0 (unknown ≠ safe).
	HasUsableData bool `json:"has_usable_data"`
	// MaxHops is the request's exposure hop bound (0–2); exposures
	// beyond it stay listed but unscored.
	MaxHops int `json:"max_hops"`
	// CandidateServiceClassified (v1.1) is a dedicated engine input:
	// the screening analyzer's verdict that the candidate itself
	// carries a non-disputed service classification. Engine input
	// only — never serialized into the findings JSONB column.
	CandidateServiceClassified bool      `json:"-"`
	Findings                   []Finding `json:"findings"`
}

// Reason is one explainable contribution (or explicit non-contribution)
// to the verdict.
type Reason struct {
	Code               string `json:"code"`
	Category           string `json:"category,omitempty"`
	VerificationStatus string `json:"verification_status,omitempty"`
	Weight             int    `json:"weight"`
	EvidenceRef        string `json:"evidence_ref,omitempty"`
	// Detail (v1.1) carries Finding.Reason or a dedupe note for
	// explainability — additive omitempty.
	Detail string `json:"detail,omitempty"`
}

// BlockScore reports one scoring block's subtotal and whether its cap
// was reached.
type BlockScore struct {
	Subtotal int  `json:"subtotal"`
	Capped   bool `json:"capped"`
}

// Blocks carries the per-block subtotals of the score formula.
type Blocks struct {
	Direct   BlockScore `json:"direct"`
	Exposure BlockScore `json:"exposure"`
	Other    BlockScore `json:"other"`
}

// Verdict is the deterministic engine output.
type Verdict struct {
	PolicyVersion string   `json:"policy_version"`
	Score         *int     `json:"score"`
	RiskLevel     string   `json:"risk_level"`
	Flags         []string `json:"flags"`
	Blocks        Blocks   `json:"blocks"`
	Reasons       []Reason `json:"reasons"`
}
