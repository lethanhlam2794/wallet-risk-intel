// Package sanctions serves read APIs over published sanctions data.
// Domain invariants (spec §1.2): a non-match is never presented as "safe";
// no-match answers carry data_status so callers see freshness/availability;
// reasons are never invented.
package sanctions

// Reason status codes (spec §8.3).
const (
	ReasonOfficialExplicit   = "OFFICIAL_EXPLICIT"
	ReasonProgramContextOnly = "PROGRAM_CONTEXT_ONLY"
	ReasonNotAvailable       = "NOT_AVAILABLE"
)

// data_status values.
const (
	StatusAvailable   = "AVAILABLE"
	StatusStale       = "STALE"
	StatusUnavailable = "UNAVAILABLE"
)

// Reason is the spec §8.3 reason block. Summary is only ever populated
// from official source text; it is nil otherwise.
type Reason struct {
	Summary *string `json:"summary"`
	Status  string  `json:"status"`
}

// ProgramRef is a sanctions program attached to an entity.
type ProgramRef struct {
	Code        string  `json:"code"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	SourceURL   *string `json:"source_url"`
}

// SourceRef identifies the list provider a record came from.
type SourceRef struct {
	Code string  `json:"code"`
	Name string  `json:"name"`
	URL  *string `json:"url"`
}

// SanctionEntry is one deduplicated sanctions record for a wallet.
type SanctionEntry struct {
	EntityID         int64        `json:"sanctions_entity_id"`
	ExternalEntityID string       `json:"external_entity_id"`
	EntityName       string       `json:"entity_name"`
	EntityType       *string      `json:"entity_type"`
	ListedAddress    string       `json:"listed_address"`
	CurrencyCode     *string      `json:"currency_code"`
	IdentifierType   string       `json:"identifier_type"`
	MatchScope       string       `json:"match_scope"`
	Programs         []ProgramRef `json:"programs"`
	Reason           Reason       `json:"reason"`
	Source           SourceRef    `json:"source"`
}

// Observation is a listing of the same normalized address that cannot
// count as a direct match — either evidenced on a different network
// (cross-chain) or on no resolvable network (unresolved).
type Observation struct {
	NetworkCode    *string `json:"network_code"`
	CurrencyCode   *string `json:"currency_code"`
	ListedAddress  string  `json:"listed_address"`
	EntityID       int64   `json:"sanctions_entity_id"`
	EntityName     string  `json:"entity_name"`
	MatchScope     string  `json:"match_scope"`
	IdentifierType string  `json:"identifier_type"`
	SourceCode     string  `json:"source_code"`
}

// WalletLookup is the §9.3 response payload (freshness fields live on the
// envelope, not in this struct — they are recomputed at serve time and
// never cached).
type WalletLookup struct {
	Matched                   bool            `json:"matched"`
	MatchType                 *string         `json:"match_type"`
	MatchScope                *string         `json:"match_scope"`
	Sanctions                 []SanctionEntry `json:"sanctions"`
	CrossChainObservations    []Observation   `json:"cross_chain_observations"`
	UnresolvedNetworkListings []Observation   `json:"unresolved_network_listings"`
}
