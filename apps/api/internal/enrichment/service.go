package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/wallet"
)

// Service orchestrates cache-first provider fan-out. The parent deadline
// bounds the whole fan-out; providers run concurrently via errgroup.Group —
// never WithContext (F4): a sibling failure must not cancel the others.
type Service struct {
	providers map[string]provider.Provider
	order     []string // registration order — deterministic response shape
	q         *db.Queries
	pool      *pgxpool.Pool // nil → persistence skipped (persisted:false)
	rdb       *redis.Client // nil/dead → cache bypassed (fail-open)
	wallets   *wallet.Service
	log       *slog.Logger
	posTTL    time.Duration
	negTTL    time.Duration
	cooldown  time.Duration
	deadline  time.Duration
	retain    int
	sf        singleflight.Group
}

// Config carries the env-tunable knobs (cmd/api wiring).
type Config struct {
	PositiveTTL     time.Duration
	NegativeTTL     time.Duration
	RefreshCooldown time.Duration
	Deadline        time.Duration
	Retain          int
}

// New builds the orchestrator. providers may be empty — the endpoint still
// answers INSUFFICIENT_DATA rather than 5xx.
func New(providers []provider.Provider, pool *pgxpool.Pool, q *db.Queries,
	rdb *redis.Client, wallets *wallet.Service, log *slog.Logger, cfg Config) *Service {

	s := &Service{
		providers: map[string]provider.Provider{},
		pool:      pool,
		q:         q,
		rdb:       rdb,
		wallets:   wallets,
		log:       log,
		posTTL:    cfg.PositiveTTL,
		negTTL:    cfg.NegativeTTL,
		cooldown:  cfg.RefreshCooldown,
		deadline:  cfg.Deadline,
		retain:    cfg.Retain,
	}
	for _, p := range providers {
		s.providers[p.Name()] = p
		s.order = append(s.order, p.Name())
	}
	if s.posTTL <= 0 {
		s.posTTL = 15 * time.Minute
	}
	if s.negTTL <= 0 {
		s.negTTL = time.Minute
	}
	if s.cooldown <= 0 {
		s.cooldown = time.Minute
	}
	if s.deadline <= 0 {
		s.deadline = 25 * time.Second
	}
	if s.retain <= 0 {
		s.retain = 25
	}
	return s
}

// Enrich resolves the wallet row, fans out to the selected providers under
// a parent deadline, and aggregates per-provider statuses.
func (s *Service) Enrich(ctx context.Context, net network.Network,
	rawAddress, normalized string, opts Options) (*Result, error) {

	selected, err := s.selectProviders(opts.Providers)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Network:           net.Code,
		Address:           rawAddress,
		NormalizedAddress: normalized,
		Providers:         make([]ProviderResult, len(selected)),
		EnrichedAt:        time.Now().UTC(),
	}
	if len(selected) == 0 {
		res.Status = StatusInsufficientData
		return res, nil
	}

	// Persist the FK target before any provider write so rows are never
	// orphaned — reuse wallet.Service (thin get-or-create).
	var walletID int64
	if s.wallets != nil {
		w, err := s.wallets.GetOrCreate(ctx, net.ID, rawAddress, normalized)
		if err != nil {
			return nil, err
		}
		walletID = w.ID
	}

	ctx, cancel := context.WithTimeout(ctx, s.deadline)
	defer cancel()

	// errgroup.Group without derived context (F4): each goroutine writes a
	// disjoint results index — a sibling failure is recorded, never
	// propagated as cancellation.
	g := errgroup.Group{}
	for i, p := range selected {
		g.Go(func() error {
			res.Providers[i] = s.enrichOne(ctx, p, net, normalized, walletID, opts.Refresh)
			return nil
		})
	}
	_ = g.Wait()

	res.Status = aggregate(res.Providers)
	return res, nil
}

func (s *Service) selectProviders(filter []string) ([]provider.Provider, error) {
	if len(filter) == 0 {
		out := make([]provider.Provider, 0, len(s.order))
		for _, name := range s.order {
			out = append(out, s.providers[name])
		}
		return out, nil
	}
	out := make([]provider.Provider, 0, len(filter))
	seen := map[string]bool{}
	for _, name := range filter {
		if seen[name] {
			continue // duplicate names must not emit duplicate blocks
		}
		seen[name] = true
		p, ok := s.providers[name]
		if !ok {
			return nil, ErrUnknownProvider
		}
		out = append(out, p)
	}
	return out, nil
}

// enrichOne runs the per-provider pipeline: disabled → typed status without
// traffic; refresh-guarded cache read; singleflight fetch; persist; cache.
func (s *Service) enrichOne(ctx context.Context, p provider.Provider,
	net network.Network, normalized string, walletID int64, refresh bool) ProviderResult {

	name := p.Name()
	pr := ProviderResult{
		Source:       name,
		Enabled:      p.Enabled(),
		Observations: []Observation{},
		Signals:      map[string]any{},
		Exposures:    []Exposure{},
	}
	if !pr.Enabled {
		pr.Status = "DISABLED"
		pr.Error = &ErrorInfo{Code: string(provider.ErrDisabled),
			Message: "provider not enabled — no outbound call made"}
		return pr
	}

	cacheKey := name + ":" + net.Code + ":" + normalized
	granted, guardDown := s.refreshAllowed(ctx, name, net.Code, normalized, refresh)
	bypass := refresh && granted
	cacheDown := s.rdb == nil || guardDown
	if !bypass {
		e, getErr := s.get(ctx, name, net.Code, normalized)
		if errors.Is(getErr, errCacheDown) {
			cacheDown = true
		} else if e != nil {
			pr.Status = e.Status
			pr.Cached = true
			fetched := e.FetchedAt
			pr.FetchedAt = &fetched
			expires := e.CacheExpiresAt
			pr.CacheExpiresAt = &expires
			if e.HTTPStatus != 0 {
				pr.HTTPStatus = &e.HTTPStatus
			}
			if e.LatencyMS != 0 {
				pr.LatencyMS = &e.LatencyMS
			}
			pr.Persisted = e.Persisted
			pr.Error = e.Error
			fillData(&pr, e.Observations, e.Signals, e.ProviderScore, e.Exposures)
			return pr
		}
	}

	// Singleflight on (source, net, addr): concurrent misses coalesce to one
	// upstream call; the loser replays the shared outcome.
	v, err, _ := s.sf.Do(cacheKey, func() (any, error) {
		return s.fetchAndStore(ctx, p, net, normalized, walletID), nil
	})
	if err != nil {
		pr.Status = "ERROR"
		pr.Error = &ErrorInfo{Code: string(provider.ErrInternal), Message: err.Error()}
		return pr
	}
	out := v.(ProviderResult)
	if cacheDown {
		out.Cache = "bypassed" // Redis outage visible, never silent
	}
	return out
}

// fetchAndStore performs the upstream lookup, persists the observation row
// (failures isolated — persisted:false instead of a status change), and
// writes the self-describing cache entry.
func (s *Service) fetchAndStore(ctx context.Context, p provider.Provider,
	net network.Network, normalized string, walletID int64) ProviderResult {

	name := p.Name()
	pr := ProviderResult{
		Source:       name,
		Enabled:      true,
		Observations: []Observation{},
		Signals:      map[string]any{},
		Exposures:    []Exposure{},
	}

	start := time.Now()
	lr, err := p.LookupAddress(ctx, net, normalized)
	latency := time.Since(start).Milliseconds()
	pr.LatencyMS = &latency
	fetchedAt := time.Now().UTC()
	pr.FetchedAt = &fetchedAt

	entry := &cacheEntry{
		FetchedAt: fetchedAt,
		Persisted: false,
	}

	var lerr *provider.LookupError
	switch {
	case err == nil:
		pr.Status = "OK"
		entry.Status = "OK"
		if lr.HTTPStatus != 0 {
			hs := int32(lr.HTTPStatus)
			pr.HTTPStatus = &hs
			entry.HTTPStatus = hs
		}
		if lr.FetchedAt.IsZero() == false {
			fetchedAt = lr.FetchedAt.UTC()
			pr.FetchedAt = &fetchedAt
			entry.FetchedAt = fetchedAt
		}
		fillData(&pr, lr.Observations, lr.Signals, lr.ProviderScore, lr.Exposures)
		entry.Observations = lr.Observations
		entry.Signals = lr.Signals
		entry.ProviderScore = lr.ProviderScore
		entry.Exposures = lr.Exposures
	case errors.As(err, &lerr):
		pr.Status = statusFor(lerr.Kind)
		entry.Status = pr.Status
		pr.Error = errorInfo(lerr)
		entry.Error = pr.Error
		if lerr.HTTPStatus != 0 {
			hs := int32(lerr.HTTPStatus)
			pr.HTTPStatus = &hs
			entry.HTTPStatus = hs
		}
		entry.LatencyMS = latency
	default:
		pr.Status = "ERROR"
		entry.Status = "ERROR"
		pr.Error = &ErrorInfo{Code: string(provider.ErrInternal), Message: err.Error()}
		entry.Error = pr.Error
	}
	entry.LatencyMS = latency

	// Persist the attempt (append-only audit; DISABLED never reaches here).
	// requested_chain prefers the literal token the adapter resolved —
	// carried on typed errors post-build (§4.6); net.Code is the honest
	// fallback when no token was resolved.
	if s.pool != nil && s.q != nil && walletID != 0 {
		if s.persist(ctx, walletID, p.Name(), requestedChain(lr, lerr, net), pr, lr, lerr) {
			pr.Persisted = true
		} else if s.log != nil {
			s.log.Warn("provider observation persist failed; isolated from status",
				"source", name, "status", pr.Status)
		}
	}
	entry.Persisted = pr.Persisted

	// Refresh-guarded bypass still writes through; negative entries carry
	// their status so they can never replay clean.
	s.put(ctx, name, net.Code, normalized, entry)
	return pr
}

func requestedChain(lr *provider.LookupResult, lerr *provider.LookupError,
	net network.Network) string {
	if lr != nil && lr.RequestedChain != "" {
		return lr.RequestedChain
	}
	if lerr != nil && lerr.RequestedChain != "" {
		return lerr.RequestedChain
	}
	return net.Code
}

// Postgres jsonb input is stricter than Go's json.Valid: it rejects
// \u0000 escapes, unpaired surrogate escapes, and invalid UTF-8 inside
// strings. Any of these rolls back the audit-row INSERT, so persisted
// payload bytes are normalized to Postgres rules first.

// jsonbText normalizes arbitrary upstream bytes into a string that can
// legally reach a jsonb column inside a marshaled envelope: invalid UTF-8
// and literal NUL bytes become U+FFFD, so json.Marshal can never emit a
// rejected \u0000 escape or invalid byte sequence.
func jsonbText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "\uFFFD")
	return strings.ReplaceAll(s, "\x00", "\uFFFD")
}

// jsonbSafe normalizes bytes intended for verbatim jsonb storage — the
// escape-level mirror of jsonbText, applied by a scanner that tracks
// backslash parity (a "\u" is only a real escape behind an ODD-length
// backslash run — `"\\ud83d"` is literal text, never an escape).
func jsonbSafe(b []byte) []byte {
	return []byte(jsonbEscapeClean(jsonbText(b)))
}

// jsonbEscapeClean rewrites the JSON \uXXXX escapes Postgres rejects —
// \u0000 and lone surrogates — to \ufffd, leaving valid surrogate pairs
// and every other escape verbatim. Escape-parity aware: `\\` consumes two
// characters, so text after an escaped backslash is never scanned as an
// escape. Anything structurally malformed stays byte-identical for the
// caller's json.Valid gate / envelope fallback.
func jsonbEscapeClean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) || s[i+1] != 'u' {
			end := i + 2
			if end > len(s) {
				end = len(s)
			}
			b.WriteString(s[i:end]) // \x / trailing \ — copy verbatim
			i = end
			continue
		}
		if i+6 > len(s) || !isHex4(s[i+2:i+6]) {
			b.WriteString(s[i : i+2]) // \u without 4 hex — invalid anyway
			i += 2
			continue
		}
		v := parseHex4(s[i+2 : i+6])
		switch {
		case v == 0x0000:
			b.WriteString(`\ufffd`)
			i += 6
		case v >= 0xD800 && v <= 0xDBFF:
			// High surrogate — a valid pair is Postgres-legal; keep it.
			if i+12 <= len(s) && s[i+6] == '\\' && s[i+7] == 'u' &&
				isHex4(s[i+8:i+12]) {
				v2 := parseHex4(s[i+8 : i+12])
				if v2 >= 0xDC00 && v2 <= 0xDFFF {
					b.WriteString(s[i : i+12])
					i += 12
					continue
				}
			}
			b.WriteString(`\ufffd`) // lone high surrogate
			i += 6
		case v >= 0xDC00 && v <= 0xDFFF:
			b.WriteString(`\ufffd`) // lone low surrogate
			i += 6
		default:
			b.WriteString(s[i : i+6])
			i += 6
		}
	}
	return b.String()
}

func isHex4(s string) bool {
	for i := 0; i < 4; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func parseHex4(s string) uint32 {
	var v uint32
	for i := 0; i < 4; i++ {
		c := s[i]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		default:
			d = c - 'A' + 10
		}
		v = v<<4 | uint32(d)
	}
	return v
}

// auditPayload picks the persisted payload: verbatim upstream body when it
// is Postgres-legal JSON, else an envelope carrying the sanitized body;
// an absent body gets an explicit attempt marker — never an empty payload.
func auditPayload(raw []byte) []byte {
	if len(raw) > 0 {
		if s := jsonbSafe(raw); json.Valid(s) {
			return s
		}
		env, _ := json.Marshal(map[string]any{"upstream_body": jsonbText(raw)})
		return env
	}
	return []byte(`{"attempt":true}`)
}

// persist writes the observation row + retention prune in one transaction.
// Payload precedence: verbatim upstream body; else the bounded upstream
// error snippet; else an explicit attempt marker — never an empty payload.
func (s *Service) persist(ctx context.Context, walletID int64, source,
	reqChain string, pr ProviderResult, lr *provider.LookupResult,
	lerr *provider.LookupError) bool {

	src, err := s.q.GetSourceByCode(ctx, source)
	if err != nil {
		return false
	}
	raw := []byte(nil)
	if lr != nil && len(lr.Raw) > 0 {
		raw = lr.Raw
	} else if lerr != nil && len(lerr.Response) > 0 {
		raw = lerr.Response
	}
	payload := auditPayload(raw)
	var errJSON []byte
	if pr.Error != nil {
		// Message can carry upstream-controlled text (e.g. publicaml's
		// {error} envelope) — sanitize to jsonb-legal bytes like payload.
		safe := *pr.Error
		safe.Message = jsonbText([]byte(safe.Message))
		errJSON, _ = json.Marshal(&safe)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)
	_, err = qtx.InsertProviderObservation(ctx, db.InsertProviderObservationParams{
		SourceID:       src.ID,
		WalletID:       walletID,
		RequestedChain: reqChain,
		Status:         pr.Status,
		HttpStatus:     int4Ptr(pr.HTTPStatus),
		LatencyMs:      int4Ptr64(pr.LatencyMS),
		Payload:        payload,
		ProviderScore:  float8Ptr(pr.ProviderScore),
		Error:          errJSON,
		FetchedAt:      timestamptz(pr.FetchedAt),
	})
	if err != nil {
		return false
	}
	if err := qtx.PruneProviderObservations(ctx, db.PruneProviderObservationsParams{
		SourceID: src.ID, WalletID: walletID, Limit: int32(s.retain),
	}); err != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}

func statusFor(kind provider.ErrKind) string {
	switch kind {
	case provider.ErrRateLimited:
		return "RATE_LIMITED"
	case provider.ErrTimeout:
		return "TIMEOUT"
	case provider.ErrUpstream5xx:
		return "UPSTREAM_5XX"
	case provider.ErrClient4xx:
		return "CLIENT_4XX"
	case provider.ErrMalformed:
		return "MALFORMED"
	case provider.ErrCircuitOpen:
		return "CIRCUIT_OPEN"
	case provider.ErrDisabled:
		return "DISABLED"
	case provider.ErrDegraded:
		return "DEGRADED"
	case provider.ErrTransport:
		return "TRANSPORT"
	default:
		return "ERROR"
	}
}

func errorInfo(le *provider.LookupError) *ErrorInfo {
	info := &ErrorInfo{Code: string(le.Kind), Message: le.Error()}
	if le.RetryAfter > 0 {
		info.RetryAfter = int64(le.RetryAfter / time.Second)
	}
	return info
}

// fillData maps provider-domain fields to the wire shape. Signals is
// never nil — it must serialize {} per the response contract.
func fillData(pr *ProviderResult, obs []provider.Observation,
	signals map[string]any, score *float64, exps []provider.Exposure) {

	for _, o := range obs {
		pr.Observations = append(pr.Observations, Observation{
			Category:           o.Category,
			Label:              o.Label,
			Entity:             o.Entity,
			SourceURL:          o.SourceURL,
			Confidence:         o.Confidence,
			VerificationStatus: o.VerificationStatus,
			ObservedAt:         o.ObservedAt.UTC(),
			Evidence:           o.Evidence,
		})
	}
	if signals != nil {
		pr.Signals = signals
	}
	pr.ProviderScore = score
	for _, e := range exps {
		pr.Exposures = append(pr.Exposures, Exposure{
			Counterparty: e.Counterparty,
			Category:     e.Category,
			Hops:         e.Hops,
			Score:        e.Score,
			Direction:    e.Direction,
		})
	}
}

// aggregate folds per-provider statuses. "Attempted" excludes DISABLED —
// a provider that made no call cannot fail. All-ok → COMPLETED; mix →
// PARTIAL; all attempted failed → FAILED; none attempted → INSUFFICIENT_DATA.
func aggregate(providers []ProviderResult) string {
	attempted, ok, failed := 0, 0, 0
	for _, p := range providers {
		if p.Status == "DISABLED" {
			continue
		}
		attempted++
		if p.Status == "OK" {
			ok++
		} else {
			failed++
		}
	}
	switch {
	case attempted == 0:
		return StatusInsufficientData
	case ok == attempted:
		return StatusCompleted
	case failed == attempted:
		return StatusFailed
	default:
		return StatusPartial
	}
}

func int4Ptr(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func int4Ptr64(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func float8Ptr(v *float64) pgtype.Float8 {
	if v == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *v, Valid: true}
}

func timestamptz(v *time.Time) pgtype.Timestamptz {
	if v == nil {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	return pgtype.Timestamptz{Time: v.UTC(), Valid: true}
}
