package sanctions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/cache"
	"checkwallet/internal/platform/metrics"
)

const (
	lookupCacheTTL = time.Hour // spec §14
)

// Service assembles sanctions responses and owns the versioned cache.
type Service struct {
	q          *db.Queries
	rdb        *redis.Client
	staleAfter time.Duration
	netByID    map[int64]string // network_id → code, for cross-chain display
	group      singleflight.Group
	metrics    *metrics.Metrics
}

// SetMetrics attaches the match counter (nil-safe when unset).
func (s *Service) SetMetrics(m *metrics.Metrics) { s.metrics = m }

func NewService(q *db.Queries, rdb *redis.Client, resolver *network.Resolver,
	staleAfter time.Duration) *Service {
	netByID := map[int64]string{}
	for _, n := range resolver.List() {
		netByID[n.ID] = n.Code
	}
	return &Service{q: q, rdb: rdb, staleAfter: staleAfter, netByID: netByID}
}

// LookupWallet resolves the snapshot-versioned cache, then the DB. The
// cached payload never carries freshness — data_status is recomputed per
// response so a cached AVAILABLE cannot outlive the staleness threshold.
func (s *Service) LookupWallet(ctx context.Context, networkID int64, networkCode,
	normalized string) (*WalletLookup, string, *time.Time, error) {

	version, snapshot, err := s.currentSnapshot(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	dataStatus, lastSynced := freshness(snapshot, s.staleAfter, time.Now())

	cacheKey := cache.WalletSanctions(networkCode, normalized, version)
	if version != "" && s.rdb != nil {
		if payload, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			var lookup WalletLookup
			if json.Unmarshal(payload, &lookup) == nil {
				return &lookup, dataStatus, lastSynced, nil
			}
		}
	}

	// Miss or uncacheable: single-flight the repo work.
	v, err, _ := s.group.Do(cacheKey, func() (any, error) {
		return s.fetchLookup(ctx, networkID, networkCode, normalized)
	})
	if err != nil {
		return nil, "", nil, err
	}
	lookup := v.(*WalletLookup)

	if version != "" && s.rdb != nil {
		if payload, err := json.Marshal(lookup); err == nil {
			// Failure to cache is not a failed lookup.
			_ = s.rdb.Set(ctx, cacheKey, payload, lookupCacheTTL).Err()
		}
	}
	return lookup, dataStatus, lastSynced, nil
}

// fetchLookup runs the three explicit match predicates.
func (s *Service) fetchLookup(ctx context.Context, networkID int64,
	networkCode, normalized string) (*WalletLookup, error) {

	direct, err := s.q.MatchSanctionsWallets(ctx, db.MatchSanctionsWalletsParams{
		NormalizedAddress: normalized,
		NetworkID:         pgtype.Int8{Int64: networkID, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	cross, err := s.q.CrossChainSanctionsWallets(ctx, db.CrossChainSanctionsWalletsParams{
		NormalizedAddress: normalized,
		NetworkID:         pgtype.Int8{Int64: networkID, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	unresolved, err := s.q.UnresolvedNetworkListings(ctx, normalized)
	if err != nil {
		return nil, err
	}

	// Collect entity ids for one batched program fetch (no N+1).
	entityIDs := map[int64]bool{}
	for _, r := range direct {
		entityIDs[r.SanctionsEntityID] = true
	}
	ids := make([]int64, 0, len(entityIDs))
	for id := range entityIDs {
		ids = append(ids, id)
	}
	programsByEntity := map[int64][]ProgramRef{}
	if len(ids) > 0 {
		rows, err := s.q.ListProgramsForEntities(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			programsByEntity[p.SanctionsEntityID] = append(
				programsByEntity[p.SanctionsEntityID], ProgramRef{
					Code:        p.Code,
					Name:        textPtr(p.Name),
					Description: textPtr(p.Description),
					SourceURL:   textPtr(p.SourceUrl),
				})
		}
	}

	// Deduplicate by sanctions_entity_id: OFAC may re-list the same
	// address+currency under different Feature@IDs for one entity.
	seenEntity := map[int64]bool{}
	entries := []SanctionEntry{}
	for _, r := range direct {
		if seenEntity[r.SanctionsEntityID] {
			continue
		}
		seenEntity[r.SanctionsEntityID] = true
		progs := programsByEntity[r.SanctionsEntityID]
		entries = append(entries, SanctionEntry{
			EntityID:         r.SanctionsEntityID,
			ExternalEntityID: r.ExternalEntityID,
			EntityName:       r.PrimaryName,
			EntityType:       textPtr(r.EntityType),
			ListedAddress:    r.ListedAddress,
			CurrencyCode:     textPtr(r.CurrencyCode),
			IdentifierType:   r.IdentifierType,
			MatchScope:       r.MatchScope,
			Programs:         progs,
			Reason:           reasonFor(nil, len(progs)),
			Source:           SourceRef{Code: r.SourceCode, Name: r.SourceName},
		})
	}

	lookup := &WalletLookup{
		Sanctions:                 entries,
		CrossChainObservations:    []Observation{},
		UnresolvedNetworkListings: []Observation{},
	}
	for _, r := range cross {
		lookup.CrossChainObservations = append(lookup.CrossChainObservations, Observation{
			NetworkCode:    s.networkCode(r.NetworkID),
			CurrencyCode:   textPtr(r.CurrencyCode),
			ListedAddress:  r.ListedAddress,
			EntityID:       r.SanctionsEntityID,
			EntityName:     r.PrimaryName,
			MatchScope:     r.MatchScope,
			IdentifierType: r.IdentifierType,
			SourceCode:     r.SourceCode,
		})
	}
	for _, r := range unresolved {
		lookup.UnresolvedNetworkListings = append(lookup.UnresolvedNetworkListings, Observation{
			NetworkCode:    nil,
			CurrencyCode:   textPtr(r.CurrencyCode),
			ListedAddress:  r.ListedAddress,
			EntityID:       r.SanctionsEntityID,
			EntityName:     r.PrimaryName,
			MatchScope:     r.MatchScope,
			IdentifierType: r.IdentifierType,
			SourceCode:     r.SourceCode,
		})
	}

	if len(entries) > 0 {
		lookup.Matched = true
		mt, ms := "WALLET_EXACT", "CHAIN_SPECIFIC"
		lookup.MatchType, lookup.MatchScope = &mt, &ms
		if s.metrics != nil {
			s.metrics.IncSanctionsMatch()
		}
	}
	return lookup, nil
}

// currentSnapshot resolves the active snapshot straight from the snapshots
// table — a separate cache pointer could silently diverge and serve
// pre-publish results labeled as fresh. Empty version means nothing ever
// published.
func (s *Service) currentSnapshot(ctx context.Context) (string, *db.SanctionsSnapshot, error) {
	snap, err := s.q.LatestSnapshotPublished(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return snap.Version, &snap, nil
}

// freshness computes data_status against the configured threshold.
func freshness(snap *db.SanctionsSnapshot, staleAfter time.Duration, now time.Time) (string, *time.Time) {
	if snap == nil || !snap.PublishedAt.Valid {
		return StatusUnavailable, nil
	}
	pub := snap.PublishedAt.Time.UTC()
	if now.Sub(pub) > staleAfter {
		return StatusStale, &pub
	}
	return StatusAvailable, &pub
}

// DataStatus implements httpserver.FreshnessProvider for the wallet API.
func (s *Service) DataStatus(ctx context.Context) (string, *time.Time) {
	_, snap, err := s.currentSnapshot(ctx)
	if err != nil {
		return StatusUnavailable, nil
	}
	return freshness(snap, s.staleAfter, time.Now())
}

// Entity returns the full §9.4 entity detail payload.
func (s *Service) Entity(ctx context.Context, id int64) (*EntityDetail, error) {
	row, err := s.q.GetSanctionsEntityByID(ctx, id)
	if err != nil {
		return nil, err
	}
	progs, err := s.q.ListEntityPrograms(ctx, id)
	if err != nil {
		return nil, err
	}
	wallets, err := s.q.ListEntityWallets(ctx, id)
	if err != nil {
		return nil, err
	}
	refs, err := s.q.ListEntityReferences(ctx, id)
	if err != nil {
		return nil, err
	}
	progRefs := make([]ProgramRef, 0, len(progs))
	for _, p := range progs {
		progRefs = append(progRefs, ProgramRef{
			Code:        p.Code,
			Name:        textPtr(p.Name),
			Description: textPtr(p.Description),
			SourceURL:   textPtr(p.SourceUrl),
			Revoked:     p.LinkRevokedAt.Valid,
		})
	}
	addresses := make([]EntityAddress, 0, len(wallets))
	for _, w := range wallets {
		addresses = append(addresses, EntityAddress{
			ListedAddress:      w.ListedAddress,
			NormalizedAddress:  w.NormalizedAddress,
			CurrencyCode:       textPtr(w.CurrencyCode),
			NetworkID:          int8Ptr(w.NetworkID),
			IdentifierType:     w.IdentifierType,
			SourceIdentifierID: textPtr(w.SourceIdentifierID),
			MatchScope:         w.MatchScope,
			Revoked:            w.RevokedAt.Valid,
		})
	}
	references := make([]EntityReference, 0, len(refs))
	for _, r := range refs {
		references = append(references, EntityReference{
			Type:        r.ReferenceType,
			Title:       textPtr(r.Title),
			URL:         textPtr(r.ReferenceUrl),
			PublishedAt: tsPtr(r.PublishedAt),
			Revoked:     r.RevokedAt.Valid,
		})
	}
	dataStatus, lastSynced := s.DataStatus(ctx)
	return &EntityDetail{
		EntityID:                 row.ID,
		ExternalEntityID:         row.ExternalEntityID,
		EntityName:               row.PrimaryName,
		EntityType:               textPtr(row.EntityType),
		SourceURL:                textPtr(row.SourceUrl),
		Revoked:                  row.RevokedAt.Valid,
		Programs:                 progRefs,
		Reason:                   reasonFor(nil, len(progRefs)),
		DigitalCurrencyAddresses: addresses,
		OfficialReferences:       references,
		Details:                  row.Details,
		Source: SourceRef{
			Code: row.SourceCode, Name: row.SourceName,
			URL: textPtr(row.SourceHomepage),
		},
		DataStatus:   dataStatus,
		LastSyncedAt: lastSynced,
	}, nil
}

// Program returns the §9.5 program detail payload.
func (s *Service) Program(ctx context.Context, code string) (*ProgramDetail, error) {
	row, err := s.q.GetProgramByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	n, err := s.q.CountProgramEntities(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	dataStatus, lastSynced := s.DataStatus(ctx)
	return &ProgramDetail{
		Code:                    row.Code,
		Name:                    textPtr(row.Name),
		Description:             textPtr(row.Description),
		SourceURL:               textPtr(row.SourceUrl),
		SourceCode:              row.SourceCode,
		AssociatedEntitiesCount: n,
		DataStatus:              dataStatus,
		LastSyncedAt:            lastSynced,
	}, nil
}

// EntityDetail is the §9.4 entity response.
type EntityDetail struct {
	EntityID                 int64             `json:"sanctions_entity_id"`
	ExternalEntityID         string            `json:"external_entity_id"`
	EntityName               string            `json:"entity_name"`
	EntityType               *string           `json:"entity_type"`
	SourceURL                *string           `json:"source_url"`
	Revoked                  bool              `json:"revoked"`
	Programs                 []ProgramRef      `json:"programs"`
	Reason                   Reason            `json:"reason"`
	DigitalCurrencyAddresses []EntityAddress   `json:"digital_currency_addresses"`
	OfficialReferences       []EntityReference `json:"official_references"`
	Details                  []byte            `json:"details"`
	Source                   SourceRef         `json:"source"`
	DataStatus               string            `json:"data_status"`
	LastSyncedAt             *time.Time        `json:"last_synced_at"`
}

// EntityAddress is one listing under an entity detail.
type EntityAddress struct {
	ListedAddress      string  `json:"listed_address"`
	NormalizedAddress  string  `json:"normalized_address"`
	CurrencyCode       *string `json:"currency_code"`
	NetworkID          *int64  `json:"network_id"`
	IdentifierType     string  `json:"identifier_type"`
	SourceIdentifierID *string `json:"source_identifier_id"`
	MatchScope         string  `json:"match_scope"`
	Revoked            bool    `json:"revoked"`
}

// EntityReference is an official reference row under an entity detail.
// Revoked marks a reference absent from the latest snapshot — retained so
// the audit trail for delisted entities stays answerable (spec §6).
type EntityReference struct {
	Type        string     `json:"reference_type"`
	Title       *string    `json:"title"`
	URL         *string    `json:"reference_url"`
	PublishedAt *time.Time `json:"published_at"`
	Revoked     bool       `json:"revoked"`
}

// ProgramDetail is the §9.5 program response.
type ProgramDetail struct {
	Code                    string     `json:"code"`
	Name                    *string    `json:"name"`
	Description             *string    `json:"description"`
	SourceURL               *string    `json:"source_url"`
	SourceCode              string     `json:"source_code"`
	AssociatedEntitiesCount int64      `json:"associated_entities_count"`
	DataStatus              string     `json:"data_status"`
	LastSyncedAt            *time.Time `json:"last_synced_at"`
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// networkCode resolves an evidenced network_id to its code for display.
func (s *Service) networkCode(id pgtype.Int8) *string {
	if !id.Valid {
		return nil
	}
	if code, ok := s.netByID[id.Int64]; ok {
		return &code
	}
	return nil
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}
