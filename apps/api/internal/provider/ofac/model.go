// Package ofac implements the OFAC SDN Advanced XML SnapshotSource:
// bounded download, streaming parse, and mapping to staging rows.
package ofac

import (
	"context"
	"time"
)

// Match scopes (spec §7.9): CHAIN_SPECIFIC means the listing is bound to a
// chain (indexed or not); UNRESOLVED means the currency+format evidence does
// not identify a single chain.
const (
	ScopeChainSpecific = "CHAIN_SPECIFIC"
	ScopeUnresolved    = "UNRESOLVED"
)

// ParsedEntity is one OFAC party ready for staging.
type ParsedEntity struct {
	ExternalID      string
	PrimaryName     string
	EntityType      string
	NonPrimaryNames []string
	Wallets         []ParsedWallet
	Details         map[string]any
}

// ParsedWallet is one digital-currency identifier for an entity.
type ParsedWallet struct {
	ListedAddress      string
	NormalizedAddress  string
	CurrencyCode       string
	IdentifierType     string // verbatim feature type name
	SourceIdentifierID string // Feature@ID
	MatchScope         string
	NetworkCode        string // "ethereum" | "bsc" | "" when unresolvable
	AddressValid       bool
	Evidence           map[string]any
}

// ParsedReference is one official reference for an entity.
type ParsedReference struct {
	ReferenceType string
	Title         string
	URL           string
	PublishedAt   *time.Time
	Details       map[string]any
}

// DocMeta holds document-level data collected during the stream: the
// freshness stamp and the trailing SanctionsEntries joins (programs and
// legal-basis references per profile id).
type DocMeta struct {
	DateOfIssue         *time.Time
	ProgramsByProfile   map[string][]string
	ReferencesByProfile map[string][]ParsedReference
}

// EntitySink receives each party as it is parsed (streaming contract — the
// document is never fully materialized).
type EntitySink interface {
	HandleEntity(ctx context.Context, e *ParsedEntity) error
}
