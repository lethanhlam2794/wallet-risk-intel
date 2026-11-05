// Package provider hosts external source adapters and the spec §13
// contracts. Only the interfaces this increment needs are defined — not
// every provider supports every interface (spec §13).
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"checkwallet/internal/network"
)

// File is one validated artifact file on local disk.
type File struct {
	Path      string // local file path
	Name      string // logical name (e.g. "ethereum.csv") — canonical ordering key
	Checksum  string // sha256 hex of this file's bytes
	SizeBytes int64
	FinalURL  string // effective URL after redirects — audit trail for SSRF checks
}

// Snapshot is a validated downloaded artifact set on local disk. F2: a
// source artifact may be multiple files (wallet-attribution ships one CSV
// per network); Checksum composes the set deterministically.
type Snapshot struct {
	Files     []File // ≥1
	Checksum  string // combined checksum — see CombinedChecksum
	SizeBytes int64  // summed across files
}

// CombinedChecksum deterministically composes a snapshot checksum over the
// provided files. One file → its own checksum verbatim (single-file
// semantics unchanged); N files → sha256 over the per-file sha256 hex
// concatenated in filename order, so fetch order cannot change the result.
func CombinedChecksum(files []File) string {
	if len(files) == 1 {
		return files[0].Checksum
	}
	sums := make([]string, 0, len(files))
	names := make([]string, 0, len(files))
	byName := map[string]string{}
	for _, f := range files {
		names = append(names, f.Name)
		byName[f.Name] = f.Checksum
	}
	sort.Strings(names)
	for _, n := range names {
		sums = append(sums, byName[n])
	}
	h := sha256.New()
	for _, s := range sums {
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TotalSize sums file sizes for a snapshot.
func TotalSize(files []File) int64 {
	var n int64
	for _, f := range files {
		n += f.SizeBytes
	}
	return n
}

// ImportResult reports staging-write counters for one run.
type ImportResult struct {
	Entities       int64
	Wallets        int64
	Programs       int64
	EntityPrograms int64
	References     int64
	Labels         int64 // wallet labels staged (wallet-attribution source)
	Rejected       int64
}

// SnapshotSource is the spec §13 file-based bulk-source contract.
type SnapshotSource interface {
	Download(ctx context.Context) (Snapshot, error)
}

// ---- On-demand provider contracts (spec §13, extended — see LookupResult) ----

// Observation is one normalized risk signal extracted from a provider
// response (spec §13 field list). VerificationStatus is always "reported"
// or "unverified" for live provider data — never auto-verified.
type Observation struct {
	Network            string
	Address            string
	Category           string
	Label              string
	Entity             string
	Source             string
	OriginalSource     string
	SourceURL          string
	Confidence         string
	VerificationStatus string
	ObservedAt         time.Time
	Evidence           json.RawMessage
}

// Exposure summarizes an indirect (graph-derived) exposure reported by a
// provider. It is never a direct attribution and never implies ownership —
// spec §4.6 keeps direct attribution and indirect exposure strictly
// separated, and a graph path cannot prove shared ownership.
type Exposure struct {
	Counterparty string
	Category     string
	Hops         int
	Score        *float64
	Direction    string
}

// LookupResult is the result envelope for an on-demand lookup. It is a
// deliberate, documented extension of the spec §13 contract: spec §13's
// bare []Observation cannot carry the verbatim raw payload (§4.6 "lưu
// nguyên response"), the provider-computed score (§4.6 provider risk score
// — never the internal risk score), the literal chain token sent upstream
// (§4.6 "lưu chain requested"), or the fetch timestamp that §4.6 requires
// persisting.
type LookupResult struct {
	Source         string
	Network        string
	Address        string
	RequestedChain string // literal chain token sent upstream (§4.6)
	Observations   []Observation
	Signals        map[string]any // provider-native flags; unmapped kept raw
	// ProviderScore is the provider's own score (e.g. PublicAML aml_score).
	// It is never the internal risk score and a 0 value is not a safe
	// verdict, especially under a non-OK status.
	ProviderScore *float64
	Exposures     []Exposure // indirect-only, never folded into Observations
	Raw           json.RawMessage
	FetchedAt     time.Time
	HTTPStatus    int // upstream HTTP status of the successful exchange (0 = unknown)
}

// Provider is the on-demand lookup contract (spec §13 extended, see
// LookupResult). Enabled() reports whether the adapter may emit outbound
// traffic — a disabled provider must never call upstream and reports
// LookupError{Kind: ErrDisabled} (spec §4.5: use an API only with access).
type Provider interface {
	Name() string
	Enabled() bool
	LookupAddress(ctx context.Context, net network.Network, normalized string) (*LookupResult, error)
}

// ErrKind is the closed vocabulary of typed provider failures. It maps
// 1:1 to provider_status codes and persisted observation statuses.
type ErrKind string

const (
	ErrRateLimited ErrKind = "rate_limited" // 429 or bounded limiter wait exceeded
	ErrTimeout     ErrKind = "timeout"      // ctx deadline or attempt timeout
	ErrUpstream5xx ErrKind = "upstream_5xx" // 5xx after retries exhausted
	ErrClient4xx   ErrKind = "client_4xx"   // other 4xx — non-retriable
	ErrMalformed   ErrKind = "malformed"    // unparseable/oversized response body
	ErrCircuitOpen ErrKind = "circuit_open" // breaker open — no attempt made
	ErrDisabled    ErrKind = "disabled"     // provider not enabled — no attempt made
	ErrDegraded    ErrKind = "degraded"     // upstream answered but signalled degraded
	ErrTransport   ErrKind = "transport"    // dial/DNS/TLS — no HTTP exchange
	ErrInternal    ErrKind = "internal"     // catch-all: request build, unexpected status
)

// LookupError is the typed provider failure. RetryAfter carries the
// upstream Retry-After hint to the caller (bounded by the client) — the
// resilience client never sleeps on a 429 in-request (F2). Response holds
// a truncated upstream error body when one was sent, so adapters can
// surface the provider's own error text without leaking internals.
type LookupError struct {
	Kind       ErrKind
	HTTPStatus int
	RetryAfter time.Duration
	Msg        string
	Response   []byte
	// RequestedChain carries the literal chain token the adapter resolved
	// before dispatching — preserved on post-build failures so the audit
	// row records what §4.6 requires. Empty when no token was resolved.
	RequestedChain string
}

func (e *LookupError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return string(e.Kind)
}

// Errorf builds a typed LookupError with a formatted message — the way
// adapters report parse/degraded failures the transport cannot see.
func Errorf(kind ErrKind, format string, args ...any) *LookupError {
	return &LookupError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// StampRequestedChain records the literal chain token on a typed error in
// place — adapters call it on their Do-path failures so audit rows keep
// the real requested_chain (§4.6) instead of a network-code fallback.
func StampRequestedChain(err error, chain string) {
	if le, ok := err.(*LookupError); ok {
		le.RequestedChain = chain
	}
}

// ---- Transaction-history provider contract (phase 6, plan §3.1) ----
// Deliberately separate from Provider: history lookup is a paged
// stream-pair scan, not a single-shot address lookup (spec §13 — not every
// provider supports every interface).

// Transfer types stored in transactions.transfer_type.
const (
	TransferKindNative = "NATIVE"
	TransferKindToken  = "TOKEN"
)

// Transfer is one normalized on-chain value transfer. AmountRaw is a raw
// integer decimal string — NUMERIC fidelity, never a float. TxHash is
// lowercase. From/To/Asset carry the upstream representations verbatim —
// the indexer owns normalization (wallet.NormalizeEVM) so invalid rows can
// be skipped and counted rather than silently dropped. Stream identifies
// the adapter stream that produced the row (chainscan: txlist|tokentx;
// nodereal: from|to) — the indexer needs per-stream occurrence counts for
// content-derived transfer_index identity.
type Transfer struct {
	TxHash         string
	Stream         string
	Type           string
	From           string
	To             string
	Asset          string // token contract (upstream form); "" for NATIVE
	AmountRaw      string // raw integer decimal string; "" = underivable
	BlockNumber    int64
	BlockTimestamp time.Time
	Confirmations  int64
	IsError        bool
}

// TransferLookupOpts bounds one stream-pair fetch step. PageToken is
// opaque and encodes the per-stream cursors; empty means first step.
// HeadBlock carries the request's observed head so adapters can compute
// confirmations (head − blockNum + 1) without an extra upstream call;
// 0 means unknown (Confirmations stays 0 → 'pending').
type TransferLookupOpts struct {
	FromBlock int64
	ToBlock   int64 // inclusive
	Kinds     []string
	PageToken string
	PageSize  int
	HeadBlock int64
}

// StreamStatus reports one upstream stream's pagination state after a
// step — the indexer derives per-stream contiguous coverage from it.
type StreamStatus struct {
	Name     string
	Finished bool
}

// TransferPage is the product of one LookupTransfers step: rows from every
// active stream, the continuation token (empty = all streams finished),
// per-stream states, and UpstreamCalls — the real number of outbound
// requests performed this step, which the request-global provider budget
// charges against.
//
// Ordering contract: within one stream, transfers must be emitted in
// ascending block order — the indexer's contiguous-coverage watermark
// (max-block-minus-one per stream) depends on it.
type TransferPage struct {
	Transfers     []Transfer
	NextPageToken string
	Streams       []StreamStatus
	UpstreamCalls int
}

// TxProvider is the on-demand transaction-history contract (plan §3.1).
// Enabled is per-network: chainscan gates on enabled_chains, nodereal on
// key presence — a disabled adapter must never emit traffic.
type TxProvider interface {
	Name() string
	Enabled(net network.Network) bool
	// HeadBlock returns the upstream-observed head block number.
	HeadBlock(ctx context.Context, net network.Network) (int64, error)
	LookupTransfers(ctx context.Context, net network.Network, normalized string, opts TransferLookupOpts) (*TransferPage, error)
}
