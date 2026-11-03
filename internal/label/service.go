// Package label serves the public wallet-labels read path (spec §9.8) and
// the wallet-endpoint label summary. Labels are upstream attributions —
// never auto-verified, never a risk verdict; absent labels are not evidence
// of safety.
package label

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"checkwallet/internal/db"
	"checkwallet/internal/platform/cache"
)

const lookupCacheTTL = time.Hour // spec §14

// ErrInvalidCursor maps to the INVALID_CURSOR error code.
var ErrInvalidCursor = fmt.Errorf("invalid cursor")

// Filters constrains the labels list. Zero values mean "no filter".
type Filters struct {
	Category           string // matches normalized code (case-insensitive)
	Source             string // data_sources.code
	SourceField        string // substring match on evidence.source_field
	VerificationStatus string // reported|unverified|verified|disputed
	ActiveOnly         bool   // default true — revoked hidden
	Cursor             string // opaque base64 label id
	Limit              int    // default 50, max 200
}

// Item is one label in the API shape.
type Item struct {
	WalletLabelID      int64           `json:"wallet_label_id"`
	Category           string          `json:"category"`
	CategoryType       string          `json:"category_type"`
	Label              string          `json:"label"`
	Entity             *EntityRef      `json:"entity"`
	Source             SourceRef       `json:"source"`
	SourceField        string          `json:"source_field"`
	SourceURL          *string         `json:"source_url"`
	UpstreamCategory   *string         `json:"upstream_category"`
	Confidence         string          `json:"confidence"`
	VerificationStatus string          `json:"verification_status"`
	Evidence           json.RawMessage `json:"evidence"`
	ObservedAt         *time.Time      `json:"observed_at"`
	FirstSeenAt        *time.Time      `json:"first_seen_at"`
	LastSeenAt         *time.Time      `json:"last_seen_at"`
	RevokedAt          *time.Time      `json:"revoked_at"`
}

// EntityRef is the resolved shared-entity link.
type EntityRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SourceRef is the providing data source.
type SourceRef struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// SummaryItem is the compact label shape on the wallet endpoint.
type SummaryItem struct {
	WalletLabelID      int64      `json:"wallet_label_id"`
	Category           string     `json:"category"`
	CategoryType       string     `json:"category_type"`
	Label              string     `json:"label"`
	Entity             *EntityRef `json:"entity"`
	Confidence         string     `json:"confidence"`
	VerificationStatus string     `json:"verification_status"`
}

// labelSet is the cached payload: the full label list for (net, addr) at a
// snapshot version. Filters/pagination apply on top so one cache entry
// serves every combination; freshness is NEVER cached.
type labelSet struct {
	Items []Item `json:"items"`
}

// ListResult carries the filtered page plus pagination metadata.
type ListResult struct {
	Items      []Item `json:"items"`
	Count      int64  `json:"count"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Service resolves labels with snapshot-versioned caching and singleflight.
type Service struct {
	q          *db.Queries
	rdb        *redis.Client
	staleAfter time.Duration
	group      singleflight.Group
}

func NewService(q *db.Queries, rdb *redis.Client, staleAfter time.Duration) *Service {
	return &Service{q: q, rdb: rdb, staleAfter: staleAfter}
}

// ListLabels returns the filtered, paginated label page for a wallet plus
// freshness metadata (recomputed per call — never cached).
func (s *Service) ListLabels(ctx context.Context, networkID int64,
	networkCode, normalized string, f Filters) (*ListResult, string, *time.Time, error) {

	version, snapshot, err := s.currentSnapshot(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	dataStatus, lastSynced := freshness(snapshot, s.staleAfter, time.Now())

	set, err := s.loadSet(ctx, networkID, networkCode, normalized, version)
	if err != nil {
		return nil, "", nil, err
	}

	cursorID, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, "", nil, ErrInvalidCursor
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var out []Item
	var lastID int64
	more := false
	for _, it := range set.Items {
		if !matches(it, f) || it.WalletLabelID <= cursorID {
			continue
		}
		if len(out) == limit {
			more = true
			break
		}
		out = append(out, it)
		lastID = it.WalletLabelID
	}
	if out == nil {
		out = []Item{}
	}
	res := &ListResult{Items: out}
	res.Count = countMatches(set.Items, f)
	if more {
		res.NextCursor = encodeCursor(lastID)
	}
	return res, dataStatus, lastSynced, nil
}

// loadSet resolves the full label set — cached under the snapshot-versioned
// key, singleflighted on miss, negative results cached too.
func (s *Service) loadSet(ctx context.Context, networkID int64,
	networkCode, normalized, version string) (*labelSet, error) {

	cacheKey := cache.WalletLabels(networkCode, normalized, version)
	if version != "" && s.rdb != nil {
		if payload, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			var set labelSet
			if json.Unmarshal(payload, &set) == nil {
				return &set, nil
			}
		}
	}

	v, err, _ := s.group.Do(cacheKey, func() (any, error) {
		rows, err := s.q.ListAllWalletLabels(ctx, db.ListAllWalletLabelsParams{
			NormalizedAddress: normalized,
			NetworkID:         networkID,
		})
		if err != nil {
			return nil, err
		}
		set := &labelSet{Items: make([]Item, 0, len(rows))}
		for _, r := range rows {
			set.Items = append(set.Items, itemFromRow(r))
		}
		return set, nil
	})
	if err != nil {
		return nil, err
	}
	set := v.(*labelSet)

	if version != "" && s.rdb != nil {
		if payload, err := json.Marshal(set); err == nil {
			_ = s.rdb.Set(ctx, cacheKey, payload, lookupCacheTTL).Err()
		}
	}
	return set, nil
}

// currentSnapshot resolves the latest published wallet-attribution snapshot
// (version for the cache key + freshness basis). No published snapshot →
// version "" → uncacheable, data_status UNAVAILABLE.
func (s *Service) currentSnapshot(ctx context.Context) (string, *db.DataSourceSnapshot, error) {
	snap, err := s.q.LatestLabelSnapshotPublished(ctx)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil, nil
		}
		return "", nil, err
	}
	return snap.Version, &snap, nil
}

// Summary returns the first-5 active label summaries plus the active total
// for the wallet endpoint.
func (s *Service) Summary(ctx context.Context, walletID int64) ([]SummaryItem, int64, error) {
	rows, err := s.q.ListWalletLabelsSummary(ctx, walletID)
	if err != nil {
		return nil, 0, err
	}
	count, err := s.q.CountActiveWalletLabels(ctx, walletID)
	if err != nil {
		return nil, 0, err
	}
	out := make([]SummaryItem, 0, len(rows))
	for _, r := range rows {
		it := SummaryItem{
			WalletLabelID:      r.WalletLabelID,
			Category:           r.CategoryCode,
			CategoryType:       r.CategoryType,
			Label:              r.Label,
			Confidence:         r.Confidence,
			VerificationStatus: r.VerificationStatus,
		}
		if r.EntityID.Valid {
			it.Entity = &EntityRef{ID: r.EntityID.Int64, Name: r.EntityName.String}
		}
		out = append(out, it)
	}
	return out, count, nil
}

func matches(it Item, f Filters) bool {
	if f.Category != "" && !strings.EqualFold(it.Category, f.Category) {
		return false
	}
	if f.Source != "" && it.Source.Code != f.Source {
		return false
	}
	if f.SourceField != "" &&
		!strings.Contains(strings.ToLower(it.SourceField), strings.ToLower(f.SourceField)) {
		return false
	}
	if f.VerificationStatus != "" && it.VerificationStatus != f.VerificationStatus {
		return false
	}
	if f.ActiveOnly && it.RevokedAt != nil {
		return false
	}
	return true
}

// countMatches returns the total matching the active filter set —
// cursor-independent, per the documented labels_count contract.
func countMatches(items []Item, f Filters) int64 {
	var n int64
	for _, it := range items {
		if matches(it, f) {
			n++
		}
	}
	return n
}

// freshness maps published_at → AVAILABLE|STALE|UNAVAILABLE. Recomputed per
// response — staleness is a function of time, so it can never be cached.
func freshness(snap *db.DataSourceSnapshot, staleAfter time.Duration, now time.Time) (string, *time.Time) {
	if snap == nil || !snap.PublishedAt.Valid {
		return "UNAVAILABLE", nil
	}
	pub := snap.PublishedAt.Time.UTC()
	if now.Sub(pub) > staleAfter {
		return "STALE", &pub
	}
	return "AVAILABLE", &pub
}

func itemFromRow(r db.ListAllWalletLabelsRow) Item {
	it := Item{
		WalletLabelID:      r.WalletLabelID,
		Category:           r.CategoryCode,
		CategoryType:       r.CategoryType,
		Label:              r.Label,
		Confidence:         r.Confidence,
		VerificationStatus: r.VerificationStatus,
		UpstreamCategory:   textPtr(r.UpstreamCategory),
		Evidence:           json.RawMessage(r.Evidence),
		Source: SourceRef{
			Code: r.SourceCode,
			Name: r.SourceName,
		},
	}
	if r.EntityID.Valid {
		it.Entity = &EntityRef{ID: r.EntityID.Int64, Name: r.EntityName.String}
	}
	it.SourceURL = textPtr(r.SourceUrl)
	it.SourceField = evidenceSourceField(r.Evidence)
	it.ObservedAt = tsPtr(r.ObservedAt)
	it.FirstSeenAt = tsPtr(r.FirstSeenAt)
	it.LastSeenAt = tsPtr(r.LastSeenAt)
	it.RevokedAt = tsPtr(r.RevokedAt)
	return it
}

// evidenceSourceField pulls source_field out of the evidence payload — the
// upstream `source` column lives inside evidence per the pinned DDL.
func evidenceSourceField(ev []byte) string {
	var m struct {
		SourceField string `json:"source_field"`
	}
	if json.Unmarshal(ev, &m) == nil {
		return m.SourceField
	}
	return ""
}

func decodeCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad cursor")
	}
	return n, nil
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	tt := t.Time.UTC()
	return &tt
}
