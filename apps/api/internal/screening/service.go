package screening

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/label"
	"checkwallet/internal/network"
	"checkwallet/internal/platform/metrics"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/scoring"
)

const (
	// labelPageSize follows the labels service's max page (200); the
	// cursor loop collects to exhaustion.
	labelPageSize = 200
	// labelHardCap bounds the collected set; hitting it marks
	// data_completeness.labels_truncated.
	labelHardCap = 1000
	// defaultDeadline bounds the detached pipeline when no deadline is
	// configured (enrichment's own 25s deadline + local margin).
	defaultDeadline = 30 * time.Second
)

// Collaborator seams — the concrete phase-2/3/4 services satisfy them;
// unit tests substitute fakes without distorting return contracts.
type (
	// SanctionsLookup mirrors sanctions.Service.LookupWallet.
	SanctionsLookup interface {
		LookupWallet(ctx context.Context, networkID int64, networkCode,
			normalized string) (*sanctions.WalletLookup, string, *time.Time, error)
	}
	// LabelsLookup mirrors label.Service.ListLabels.
	LabelsLookup interface {
		ListLabels(ctx context.Context, networkID int64, networkCode,
			normalized string, f label.Filters) (*label.ListResult, string, *time.Time, error)
	}
	// Enricher mirrors enrichment.Service.Enrich.
	Enricher interface {
		Enrich(ctx context.Context, net network.Network, rawAddress,
			normalized string, opts enrichment.Options) (*enrichment.Result, error)
	}
	// WalletStore mirrors wallet.Service.GetOrCreate.
	WalletStore interface {
		GetOrCreate(ctx context.Context, net network.Network, address,
			normalized string) (db.Wallet, error)
	}
)

// Deps carries the orchestrator's collaborators. Sanctions, Labels and
// Enrichment may be nil — the fold reports their dimensions unusable /
// absent rather than hard-failing (enrichment nil → local data answers).
type Deps struct {
	Pool       *pgxpool.Pool
	Queries    *db.Queries
	Sanctions  SanctionsLookup
	Labels     LabelsLookup
	Enrichment Enricher
	Wallets    WalletStore
	Logger     *slog.Logger
	Metrics    *metrics.Metrics
	// Policy is the deployment scoring policy (SCORING_POLICY). Empty
	// falls back to scoring.PolicyV1; a request's
	// options.policy_version overrides it.
	Policy string
	// PriceTable is the v1.1 materiality seam; nil → pinned defaults.
	PriceTable scoring.PriceTableV1_1
	// FreshTTL is the index-state staleness horizon (IndexingFreshTTL).
	FreshTTL time.Duration
	// Enqueuer enqueues the candidate for warm indexing when v1.1 finds
	// no index coverage (self-healing rollout). Nil-safe.
	Enqueuer func(ctx context.Context, networkID int64, addr string)
	// Deadline bounds the detached pipeline; <=0 → 30s default.
	Deadline time.Duration
}

// Service runs the screening pipeline.
type Service struct {
	d Deps
}

// New builds the orchestrator.
func New(d Deps) *Service {
	if d.Deadline <= 0 {
		d.Deadline = defaultDeadline
	}
	return &Service{d: d}
}

// RunMeta carries request-scoped context for logs and claim plumbing.
type RunMeta struct {
	RequestID string
	// ClaimID, when non-nil, is attached to the inserted result inside
	// the same transaction (idempotency claim → result link).
	ClaimID *int64
}

// Run executes one synchronous screening: the pipeline detaches via
// context.WithoutCancel so a client disconnect still lands the terminal
// audit row. Returns the composed §9.7 result (already persisted).
func (s *Service) Run(ctx context.Context, net network.Network,
	req CanonicalRequest, meta RunMeta) (*Result, error) {

	start := time.Now()
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.d.Deadline)
	defer cancel()

	res, err := s.run(pctx, net, req, meta)
	status := StatusFailed
	if res != nil {
		status = res.Status
	}
	if s.d.Metrics != nil {
		s.d.Metrics.ObserveScreening(status, time.Since(start))
	}
	attrs := []any{
		"request_id", meta.RequestID,
		"network", net.Code,
		"status", status,
	}
	if res != nil {
		attrs = append(attrs, "screening_id", res.ScreeningID)
	}
	if err != nil {
		attrs = append(attrs, "error_code", "INTERNAL")
		s.log().Error("screening failed", append(attrs, "error", err)...)
	} else {
		s.log().Info("screening completed", attrs...)
	}
	return res, err
}

// resolvePolicy picks the scoring policy for this run: the request
// override, then the deployment default, then v1.
func (s *Service) resolvePolicy(req CanonicalRequest) string {
	if req.Options.PolicyVersion != "" {
		return req.Options.PolicyVersion
	}
	if s.d.Policy != "" {
		return s.d.Policy
	}
	return scoring.PolicyV1
}

func (s *Service) run(ctx context.Context, net network.Network,
	req CanonicalRequest, meta RunMeta) (*Result, error) {

	opts := req.Options
	policy := s.resolvePolicy(req)

	// 1. Resolve/create the wallet row — FK target for persistence and
	// provider observations. A failure here is pre-resolution: no row.
	// The address column keeps the caller's original representation;
	// program callers that never saw raw input fall back to normalized.
	raw := req.RawAddress
	if raw == "" {
		raw = req.Address
	}
	w, err := s.d.Wallets.GetOrCreate(ctx, net, raw, req.Address)
	if err != nil {
		return nil, fmt.Errorf("wallet resolve: %w", err)
	}

	// 2. Gather inputs in parallel — local lookups never block on the
	// provider leg's deadline.
	g := &gathered{}
	eg, egctx := errgroup.WithContext(ctx)
	if opts.IncludeSanctions && s.d.Sanctions != nil {
		eg.Go(func() error {
			lookup, status, synced, err := s.d.Sanctions.LookupWallet(
				egctx, net.ID, net.Code, req.Address)
			if err != nil {
				g.sanctionsErr = true
				return nil // degraded input, not a pipeline failure
			}
			g.sanctionsLookup, g.sanctionsStatus, g.sanctionsSynced = lookup, status, synced
			return nil
		})
	} else if opts.IncludeSanctions && s.d.Sanctions == nil {
		g.sanctionsStatus = DataUnavailable
	}
	if opts.IncludeLabels && s.d.Labels != nil {
		eg.Go(func() error {
			items, status, synced, truncated, err := s.collectLabels(egctx, net, req.Address)
			if err != nil {
				g.labelsErr = true
				return nil
			}
			g.labels, g.labelsStatus, g.labelsSynced, g.labelsTruncated =
				items, status, synced, truncated
			return nil
		})
	} else if opts.IncludeLabels && s.d.Labels == nil {
		g.labelsStatus = DataUnavailable
	}
	if s.d.Enrichment != nil {
		eg.Go(func() error {
			res, err := s.d.Enrichment.Enrich(egctx, net, raw, req.Address,
				enrichment.Options{Refresh: opts.RefreshProviders})
			if err != nil {
				g.enrichErr = true
				return nil
			}
			g.enrich = res
			return nil
		})
	} else {
		g.enrichNil = true
	}
	// 2b. v1.1 onchain analyzer leg — indexed data only, runs only when
	// include_exposure ∧ resolved policy v1.1. Never calls EnsureIndexed
	// or any provider.
	if opts.IncludeExposure && policy == scoring.PolicyV1_1 && s.d.Queries != nil {
		eg.Go(func() error {
			pt := s.d.PriceTable
			if pt == nil {
				pt = scoring.DefaultPriceTableV1_1()
			}
			az := &exposureAnalyzer{
				store: s.d.Queries, prices: pt,
				freshTTL: s.d.FreshTTL, now: time.Now}
			o := az.analyze(egctx, net.ID, net.Code, w.ID, req.Address)
			g.onchain = &o
			if o.onchainCoverage == "absent" && s.d.Enqueuer != nil {
				s.d.Enqueuer(egctx, net.ID, req.Address)
			}
			return nil
		})
	}
	_ = eg.Wait()

	// 3. Category map for provider-observation category_type resolution.
	catTypes := map[string]string{}
	if s.d.Queries != nil {
		if rows, err := s.d.Queries.ListRiskCategories(ctx); err == nil {
			for _, r := range rows {
				catTypes[r.Code] = r.CategoryType
			}
		} else {
			s.log().Warn("risk_categories load failed; category_type falls back to OTHER",
				"error", err)
		}
	}

	// 4. Fold → map → score.
	m := mapGathered(g, opts, catTypes, policy)
	maxHops := 0
	if opts.IncludeExposure {
		maxHops = opts.MaxHops
	}
	svcClassified := false
	if g.onchain != nil {
		svcClassified = g.onchain.serviceClassified
	}
	verdict := scoring.EvaluateFor(scoring.FindingSet{
		HasUsableData:              m.usable > 0,
		MaxHops:                    maxHops,
		CandidateServiceClassified: svcClassified,
		Findings:                   m.findings,
	}, policy)

	// 5. Compose the §9.7 result.
	screenedAt := time.Now().UTC()
	res := &Result{
		ScreeningID:      uuid.NewString(),
		Network:          net.Code,
		Address:          req.Address,
		Status:           m.status,
		RiskScore:        verdict.Score,
		RiskLevel:        verdict.RiskLevel,
		DirectSanctions:  m.sanctions,
		DirectLabels:     m.labels,
		Exposures:        m.exposures,
		Evidence:         m.evidence,
		ProviderStatus:   m.providerStatus,
		DataCompleteness: m.completeness,
		ScreenedAt:       screenedAt,
		Scoring: ScoringBlock{
			PolicyVersion: verdict.PolicyVersion,
			Score:         verdict.Score,
			RiskLevel:     verdict.RiskLevel,
			Flags:         orEmpty(verdict.Flags),
			Blocks:        verdict.Blocks,
			Reasons:       orEmpty(verdict.Reasons),
		},
	}
	if res.Evidence == nil {
		res.Evidence = []EvidenceRef{}
	}
	if m.status == StatusPartial && s.d.Metrics != nil {
		s.d.Metrics.IncScreeningPartial()
	}

	// 6. Persist — result INSERT + optional claim attach in ONE tx.
	if err := s.persist(ctx, w.ID, req, res, m, meta.ClaimID); err != nil {
		// Plan §3.5: FAILED is persisted when a wallet row exists. The
		// pipeline context may already be dead (deadline/drift), so the
		// audit row gets a fresh bounded context; the claim is still
		// released by the caller so a retry is never held hostage.
		if ferr := s.persistFailed(w.ID, req, policy); ferr != nil {
			s.log().Warn("failed-screening audit row not persisted",
				"request_id", meta.RequestID, "error", ferr)
		}
		return nil, fmt.Errorf("persist: %w", err)
	}
	return res, nil
}

// persistFailed writes a best-effort FAILED audit row after a
// post-resolution pipeline error. It is intentionally not attached to
// the idempotency claim — the claim is released so a same-key retry
// can re-claim — but the audit record preserves the fact that this
// wallet's screening failed, per plan §3.5. The error message is a
// static sanitized string: the result blob is replayed verbatim to
// clients, so raw driver/schema detail must never be persisted.
func (s *Service) persistFailed(walletID int64, req CanonicalRequest,
	policy string) error {
	if s.d.Pool == nil || s.d.Queries == nil {
		return fmt.Errorf("no persistence configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	screenedAt := time.Now().UTC()
	res := &Result{
		ScreeningID: uuid.NewString(),
		Network:     req.Network,
		Address:     req.Address,
		Status:      StatusFailed,
		RiskLevel:   scoring.RiskUnknown,
		DirectSanctions: SanctionsBlock{
			Evaluated: false, Entries: []sanctions.SanctionEntry{}},
		DirectLabels: LabelsBlock{Evaluated: false, Items: []label.Item{}},
		Exposures: ExposuresBlock{
			Evaluated: false, Items: []ExposureItem{},
			Coverage: Coverage{Tracing: "provider_reported_only",
				MaxHopsRequested: req.Options.MaxHops}},
		Evidence: []EvidenceRef{},
		ProviderStatus: ProviderStatus{
			Providers: map[string]ProviderStatusEntry{},
			Fanout: &ProviderStatusEntry{
				Status: "ERROR", Enabled: true,
				Error: &enrichment.ErrorInfo{
					Code: "internal", Message: "screening persist failed"}},
			Sanctions: SnapshotStatus{DataStatus: DataUnavailable},
			Labels:    SnapshotStatus{DataStatus: DataUnavailable},
		},
		Scoring: ScoringBlock{
			PolicyVersion: policy,
			RiskLevel:     scoring.RiskUnknown,
			Flags:         []string{},
			Blocks:        scoring.Blocks{},
			Reasons:       []scoring.Reason{},
		},
		ScreenedAt: screenedAt,
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return err
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return err
	}
	providerJSON, err := json.Marshal(res.ProviderStatus)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(res.ScreeningID)
	if err != nil {
		return err
	}
	_, err = s.d.Queries.InsertScreeningResult(ctx, db.InsertScreeningResultParams{
		ID:                   pgtype.UUID{Bytes: id, Valid: true},
		WalletID:             walletID,
		RiskScore:            pgtype.Int2{},
		RiskLevel:            res.RiskLevel,
		ScreeningStatus:      StatusFailed,
		ScoringPolicyVersion: policy,
		Findings:             []byte("[]"),
		ProviderStatus:       providerJSON,
		ScreenedAt:           pgtype.Timestamptz{Time: screenedAt, Valid: true},
		Request:              requestJSON,
		Result:               resultJSON,
	})
	return err
}

// collectLabels drains the cursor loop: page size 200, to exhaustion,
// hard-capped at 1000 with a truncated marker.
func (s *Service) collectLabels(ctx context.Context, net network.Network,
	normalized string) ([]label.Item, string, *time.Time, bool, error) {

	var out []label.Item
	var dataStatus string
	var synced *time.Time
	cursor := ""
	for {
		res, status, lastSynced, err := s.d.Labels.ListLabels(ctx, net.ID, net.Code,
			normalized, label.Filters{
				ActiveOnly: true, Limit: labelPageSize, Cursor: cursor,
			})
		if err != nil {
			return nil, "", nil, false, err
		}
		dataStatus, synced = status, lastSynced
		out = append(out, res.Items...)
		if len(out) >= labelHardCap {
			return out[:labelHardCap], dataStatus, synced, true, nil
		}
		if res.NextCursor == "" {
			return out, dataStatus, synced, false, nil
		}
		cursor = res.NextCursor
	}
}

// persist writes the terminal row; the optional claim attach commits in
// the same transaction so a claim and its result are never torn apart.
func (s *Service) persist(ctx context.Context, walletID int64,
	req CanonicalRequest, res *Result, m *mapped, claimID *int64) error {

	if s.d.Pool == nil || s.d.Queries == nil {
		return fmt.Errorf("no persistence configured")
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return err
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return err
	}
	// A nil findings slice marshals to JSON `null` — the column contract
	// is an array, so normalize empty to `[]` (same convention as the
	// FAILED audit row).
	findingsJSON, err := json.Marshal(orEmpty(m.findings))
	if err != nil {
		return err
	}
	providerJSON, err := json.Marshal(res.ProviderStatus)
	if err != nil {
		return err
	}

	id, err := uuid.Parse(res.ScreeningID)
	if err != nil {
		return err
	}
	params := db.InsertScreeningResultParams{
		ID:       pgtype.UUID{Bytes: id, Valid: true},
		WalletID: walletID,
		RiskScore: pgtype.Int2{Valid: res.RiskScore != nil,
			Int16: int16(scoreOrZero(res.RiskScore))},
		RiskLevel:            res.RiskLevel,
		ScreeningStatus:      res.Status,
		ScoringPolicyVersion: res.Scoring.PolicyVersion,
		Findings:             findingsJSON,
		ProviderStatus:       providerJSON,
		ScreenedAt:           pgtype.Timestamptz{Time: res.ScreenedAt, Valid: true},
		Request:              requestJSON,
		Result:               resultJSON,
	}

	tx, err := s.d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	qtx := s.d.Queries.WithTx(tx)
	if _, err := qtx.InsertScreeningResult(ctx, params); err != nil {
		return err
	}
	if claimID != nil {
		if err := qtx.AttachScreeningResultToRequest(ctx,
			db.AttachScreeningResultToRequestParams{
				ID: *claimID, ScreeningResultID: params.ID,
			}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) log() *slog.Logger {
	if s.d.Logger != nil {
		return s.d.Logger
	}
	return slog.Default()
}

func scoreOrZero(score *int) int {
	if score == nil {
		return 0
	}
	return *score
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
