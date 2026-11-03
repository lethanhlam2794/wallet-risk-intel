// Package enrichment orchestrates the live provider fan-out behind
// GET /wallets/{network}/{address}/enrichment (spec §19 phase 4): cache-first
// per provider, singleflight on miss, typed failures surfaced verbatim,
// raw observations persisted to provider_observations.
package enrichment

import (
	"encoding/json"
	"errors"
	"time"
)

// Aggregate statuses (same vocabulary as screening, spec §9).
const (
	StatusCompleted        = "COMPLETED"
	StatusPartial          = "PARTIAL"
	StatusFailed           = "FAILED"
	StatusInsufficientData = "INSUFFICIENT_DATA"
)

// ErrUnknownProvider rejects ?providers= filters naming an unregistered source.
var ErrUnknownProvider = errors.New("unknown provider")

// Options constrains one Enrich call.
type Options struct {
	// Refresh bypasses the cache read when the per-key cooldown allows it
	// (F3 quota guard — inside the cooldown the flag is ignored).
	Refresh bool
	// Providers restricts the fan-out; empty means every registered source.
	Providers []string
}

// Result is the API response payload.
type Result struct {
	Network           string           `json:"network"`
	Address           string           `json:"address"`
	NormalizedAddress string           `json:"normalized_address"`
	Status            string           `json:"status"`
	Providers         []ProviderResult `json:"providers"`
	EnrichedAt        time.Time        `json:"enriched_at"`
}

// ProviderResult is one provider's outcome — success and failure share the
// block; a failed provider is never elided and never folded into a verdict.
type ProviderResult struct {
	Source         string         `json:"source"`
	Enabled        bool           `json:"enabled"`
	Status         string         `json:"status"` // OK / typed failure / DISABLED
	Cached         bool           `json:"cached"`
	Cache          string         `json:"cache,omitempty"` // "bypassed" marks a Redis outage
	FetchedAt      *time.Time     `json:"fetched_at,omitempty"`
	CacheExpiresAt *time.Time     `json:"cache_expires_at,omitempty"`
	HTTPStatus     *int32         `json:"http_status,omitempty"`
	LatencyMS      *int64         `json:"latency_ms,omitempty"`
	Persisted      bool           `json:"persisted"`
	Error          *ErrorInfo     `json:"error,omitempty"`
	Observations   []Observation  `json:"observations"`
	Signals        map[string]any `json:"signals"`
	ProviderScore  *float64       `json:"provider_score,omitempty"`
	Exposures      []Exposure     `json:"exposures"`
}

// ErrorInfo mirrors the typed LookupError on the wire.
type ErrorInfo struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int64  `json:"retry_after,omitempty"` // seconds
}

// Observation is the wire shape of one normalized risk signal.
type Observation struct {
	Category           string          `json:"category"`
	Label              string          `json:"label"`
	Entity             string          `json:"entity"`
	SourceURL          string          `json:"source_url,omitempty"`
	Confidence         string          `json:"confidence"`
	VerificationStatus string          `json:"verification_status"`
	ObservedAt         time.Time       `json:"observed_at"`
	Evidence           json.RawMessage `json:"evidence,omitempty"`
}

// Exposure is an indirect (graph-derived) exposure — kept strictly separate
// from direct observations; a graph path never implies ownership.
type Exposure struct {
	Counterparty string   `json:"counterparty"`
	Category     string   `json:"category"`
	Hops         int      `json:"hops"`
	Score        *float64 `json:"score,omitempty"`
	Direction    string   `json:"direction"`
}
