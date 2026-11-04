package screening

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"checkwallet/internal/enrichment"
	"checkwallet/internal/label"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/scoring"
)

// gathered is the raw collector output the mapper folds into a
// FindingSet and the §9.7 blocks. Each input carries its own
// availability signal — the fold keys on data_status, never error-only.
type gathered struct {
	sanctionsLookup *sanctions.WalletLookup
	sanctionsStatus string
	sanctionsSynced *time.Time
	sanctionsErr    bool

	labels          []label.Item
	labelsStatus    string
	labelsSynced    *time.Time
	labelsTruncated bool
	labelsErr       bool

	enrich    *enrichment.Result
	enrichErr bool // hard fan-out failure — every provider input fails
	enrichNil bool // no enrichment collaborator configured
}

// mapped is the mapper output: the engine input plus the response
// blocks the service composes.
type mapped struct {
	findings       []scoring.Finding
	sanctions      SanctionsBlock
	labels         LabelsBlock
	exposures      ExposuresBlock
	evidence       []EvidenceRef
	providerStatus ProviderStatus
	completeness   DataCompleteness
	catTypes       map[string]string
	usable         int
	degraded       int
	status         string
}

// mapGathered runs the availability fold and converts gathered inputs
// into findings + response blocks.
func mapGathered(g *gathered, opts Options, catTypes map[string]string) *mapped {
	m := &mapped{
		providerStatus: ProviderStatus{Providers: map[string]ProviderStatusEntry{}},
		catTypes:       catTypes,
	}

	m.foldSanctions(g, opts)
	m.foldLabels(g, opts)
	m.foldProviders(g)
	m.foldExposure(g, opts)

	m.status = foldStatus(m.usable, m.degraded)
	return m
}

// foldSanctions maps the OFAC dimension: direct entries → flat-100
// findings; cross-chain + unresolved listings → informational
// cross_chain_listing findings (never direct matches).
func (m *mapped) foldSanctions(g *gathered, opts Options) {
	dim := DimensionStatus{Requested: opts.IncludeSanctions}
	if !opts.IncludeSanctions {
		dim.DataStatus = DataNotRequested
		m.completeness.Sanctions = dim
		m.providerStatus.Sanctions = SnapshotStatus{DataStatus: DataNotRequested}
		m.sanctions = SanctionsBlock{Evaluated: false, Entries: []sanctions.SanctionEntry{}}
		m.findings = append(m.findings, scoring.Finding{
			Kind: scoring.KindNotEvaluated, Ref: "sanctions",
		})
		return
	}
	m.providerStatus.Sanctions = SnapshotStatus{
		DataStatus: orUnavailable(g.sanctionsStatus), LastSyncedAt: g.sanctionsSynced}
	switch {
	case g.sanctionsErr:
		dim.DataStatus, dim.Usable = DataUnavailable, false
		m.degraded++
	case g.sanctionsStatus == DataAvailable:
		dim.DataStatus, dim.Usable = DataAvailable, true
		m.usable++
	case g.sanctionsStatus == DataStale:
		// Stale stays usable but degrades the fold — and gets its own
		// informational reason so the verdict shows it.
		dim.DataStatus, dim.Usable, dim.Stale = DataStale, true, true
		m.usable++
		m.degraded++
		m.findings = append(m.findings, scoring.Finding{
			Kind: scoring.KindSnapshotStale, Source: "ofac", Ref: "sanctions_snapshot",
		})
	default:
		dim.DataStatus, dim.Usable = DataUnavailable, false
		m.degraded++
	}
	m.completeness.Sanctions = dim

	// Findings and entries emit only for a usable dimension — rows that
	// surfaced under UNAVAILABLE (contract drift upstream) must not read
	// as scoring evidence or direct matches.
	blk := SanctionsBlock{Evaluated: true, Entries: []sanctions.SanctionEntry{}}
	if g.sanctionsLookup != nil && dim.Usable {
		for _, e := range g.sanctionsLookup.Sanctions {
			blk.Entries = append(blk.Entries, e)
			m.findings = append(m.findings, scoring.Finding{
				Kind:               scoring.KindSanctionsDirect,
				Category:           "SANCTIONED",
				CategoryType:       "LEGAL",
				Source:             e.Source.Code,
				Ref:                fmt.Sprintf("sanctions_entity:%d", e.EntityID),
				VerificationStatus: scoring.VerificationVerified,
				Confidence:         scoring.ConfidenceHigh,
			})
			m.evidence = append(m.evidence, EvidenceRef{
				Kind: "sanctions", Ref: SanctionsEvidence{EntityID: e.EntityID},
			})
		}
		blk.Matched = g.sanctionsLookup.Matched && len(blk.Entries) > 0
		for _, o := range g.sanctionsLookup.CrossChainObservations {
			m.findings = append(m.findings, scoring.Finding{
				Kind: scoring.KindCrossChainListing, Category: "SANCTIONED",
				Source: o.SourceCode,
				Ref: fmt.Sprintf("sanctions_entity:%d@%s", o.EntityID,
					strOrEmpty(o.NetworkCode)),
			})
			m.evidence = append(m.evidence, EvidenceRef{
				Kind: "cross_chain_listing",
				Ref:  SanctionsEvidence{EntityID: o.EntityID, NetworkCode: o.NetworkCode},
			})
		}
		for _, o := range g.sanctionsLookup.UnresolvedNetworkListings {
			m.findings = append(m.findings, scoring.Finding{
				Kind: scoring.KindCrossChainListing, Category: "SANCTIONED",
				Source: o.SourceCode,
				Ref:    fmt.Sprintf("sanctions_entity:%d@unresolved", o.EntityID),
			})
			m.evidence = append(m.evidence, EvidenceRef{
				Kind: "cross_chain_listing",
				Ref:  SanctionsEvidence{EntityID: o.EntityID},
			})
		}
	}
	m.sanctions = blk
}

// foldLabels maps the labels dimension: every collected active label
// becomes a label finding keyed by its wallet_labels.id.
func (m *mapped) foldLabels(g *gathered, opts Options) {
	dim := DimensionStatus{Requested: opts.IncludeLabels}
	m.completeness.LabelsCollected = len(g.labels)
	m.completeness.LabelsTruncated = g.labelsTruncated
	if !opts.IncludeLabels {
		dim.DataStatus = DataNotRequested
		m.completeness.Labels = dim
		m.providerStatus.Labels = SnapshotStatus{DataStatus: DataNotRequested}
		m.labels = LabelsBlock{Evaluated: false, Items: []label.Item{}}
		m.findings = append(m.findings, scoring.Finding{
			Kind: scoring.KindNotEvaluated, Ref: "labels",
		})
		return
	}
	m.providerStatus.Labels = SnapshotStatus{
		DataStatus: orUnavailable(g.labelsStatus), LastSyncedAt: g.labelsSynced}
	switch {
	case g.labelsErr:
		dim.DataStatus = DataUnavailable
		m.degraded++
	case g.labelsStatus == DataAvailable:
		dim.DataStatus, dim.Usable = DataAvailable, true
		m.usable++
	case g.labelsStatus == DataStale:
		dim.DataStatus, dim.Usable, dim.Stale = DataStale, true, true
		m.usable++
		m.degraded++
	default:
		dim.DataStatus = DataUnavailable
		m.degraded++
	}
	m.completeness.Labels = dim

	// Same gate as sanctions: an unusable dimension contributes no
	// findings and no items — the response block stays empty so a
	// lookup that returned rows under UNAVAILABLE never reads as
	// scored evidence.
	var items []label.Item
	if dim.Usable {
		items = make([]label.Item, len(g.labels))
		copy(items, g.labels)
	} else {
		items = []label.Item{}
	}
	for _, it := range items {
		ref := fmt.Sprintf("wallet_label:%d", it.WalletLabelID)
		m.findings = append(m.findings, scoring.Finding{
			Kind:               scoring.KindLabel,
			Category:           strings.ToUpper(it.Category),
			CategoryType:       it.CategoryType,
			Source:             it.Source.Code,
			Ref:                ref,
			VerificationStatus: it.VerificationStatus,
			Confidence:         it.Confidence,
		})
		m.evidence = append(m.evidence, EvidenceRef{
			Kind: "label", Ref: it.WalletLabelID,
		})
	}
	m.labels = LabelsBlock{
		Evaluated: true, Items: items,
		Collected: len(items), Truncated: g.labelsTruncated,
	}
}

// foldProviders maps the enrichment fan-out: per-source provider_status
// verbatim; OK observations/scores → findings; typed failures →
// provider_failed findings; DISABLED → reported, excluded from the fold.
func (m *mapped) foldProviders(g *gathered) {
	pc := ProvidersCompleteness{Usable: []string{}, Degraded: []string{}, Disabled: []string{}}
	if g.enrichNil {
		m.completeness.Providers = pc
		return
	}
	if g.enrichErr || g.enrich == nil {
		// Hard fan-out failure: the providers dimension failed wholesale.
		// Reported on the named fanout field — never a pseudo-provider
		// key in the per-source map.
		m.degraded++
		m.providerStatus.Fanout = &ProviderStatusEntry{
			Status: "ERROR", Enabled: true,
			Error: &enrichment.ErrorInfo{
				Code: "internal", Message: "enrichment fan-out failed"},
		}
		m.completeness.Providers = pc
		return
	}
	for _, pr := range g.enrich.Providers {
		m.providerStatus.Providers[pr.Source] = ProviderStatusEntry{
			Status: pr.Status, Enabled: pr.Enabled, Cached: pr.Cached,
			FetchedAt: pr.FetchedAt, Error: pr.Error, ProviderScore: pr.ProviderScore,
		}
		switch pr.Status {
		case "DISABLED":
			pc.Disabled = append(pc.Disabled, pr.Source)
			continue
		case "OK":
			pc.Usable = append(pc.Usable, pr.Source)
			m.usable++
			m.mapProviderData(pr)
		default:
			pc.Degraded = append(pc.Degraded, pr.Source)
			m.degraded++
			m.findings = append(m.findings, scoring.Finding{
				Kind: scoring.KindProviderFailed, Source: pr.Source, Ref: pr.Source,
			})
		}
	}
	sort.Strings(pc.Usable)
	sort.Strings(pc.Degraded)
	sort.Strings(pc.Disabled)
	m.completeness.Providers = pc
}

// mapProviderData converts one OK provider result into observations,
// score, and exposure findings + evidence.
func (m *mapped) mapProviderData(pr enrichment.ProviderResult) {
	for _, o := range pr.Observations {
		// JSON-encoded identity — pipe-joining collides when a
		// component field itself contains the separator.
		ib, _ := json.Marshal([]string{o.Category, o.Label, o.Entity})
		identity := string(ib)
		m.findings = append(m.findings, scoring.Finding{
			Kind:               scoring.KindProviderObservation,
			Category:           strings.ToUpper(o.Category),
			CategoryType:       categoryType(o.Category, m.catTypes),
			Source:             pr.Source,
			Ref:                fmt.Sprintf("%s:%s", pr.Source, identity),
			VerificationStatus: o.VerificationStatus,
			Confidence:         o.Confidence,
		})
		m.evidence = append(m.evidence, EvidenceRef{
			Kind: "provider_observation",
			Ref: ProviderEvidence{
				Source: pr.Source, FetchedAt: pr.FetchedAt, Identity: identity,
			},
		})
	}
	if pr.ProviderScore != nil {
		score := int(math.Round(*pr.ProviderScore))
		m.findings = append(m.findings, scoring.Finding{
			Kind:               scoring.KindProviderScore,
			Source:             pr.Source,
			Ref:                pr.Source + ":score",
			ProviderScore:      &score,
			VerificationStatus: scoring.VerificationReported,
		})
		m.evidence = append(m.evidence, EvidenceRef{
			Kind: "provider_score",
			Ref:  ProviderEvidence{Source: pr.Source, FetchedAt: pr.FetchedAt},
		})
	}
}

// foldExposure applies the §3.6 exposure gate: include_exposure=false
// marks coverage.disabled_by_request and the block reads
// evaluated:false. Provider-reported items list regardless of hop
// eligibility — hiding reported evidence is a silent drop — while
// Scored records hops ≤ max_hops ∧ ≤ 2.
func (m *mapped) foldExposure(g *gathered, opts Options) {
	blk := ExposuresBlock{
		Evaluated: opts.IncludeExposure,
		Items:     []ExposureItem{},
		Coverage: Coverage{
			Tracing:           "provider_reported_only",
			MaxHopsRequested:  opts.MaxHops,
			DisabledByRequest: !opts.IncludeExposure,
		},
	}
	if !opts.IncludeExposure {
		m.exposures = blk
		m.completeness.Exposure = DimensionStatus{
			Requested: false, DataStatus: DataNotRequested}
		m.findings = append(m.findings, scoring.Finding{
			Kind: scoring.KindNotEvaluated, Ref: "exposure",
		})
		return
	}

	providerUsable := len(m.completeness.Providers.Usable) > 0
	if g.enrich != nil {
		for _, pr := range g.enrich.Providers {
			if pr.Status != "OK" {
				continue
			}
			for _, e := range pr.Exposures {
				item := ExposureItem{
					Counterparty: e.Counterparty,
					Category:     strings.ToUpper(e.Category),
					Hops:         e.Hops,
					Direction:    e.Direction,
					Score:        e.Score,
					Source:       pr.Source,
					Basis:        scoring.BasisProviderReported,
					Scored:       e.Hops >= 1 && e.Hops <= opts.MaxHops && e.Hops <= 2,
				}
				blk.Items = append(blk.Items, item)
				m.findings = append(m.findings, scoring.Finding{
					Kind:     scoring.KindExposure,
					Category: item.Category,
					Source:   pr.Source,
					Ref: fmt.Sprintf("%s:%s:h%d:%s", pr.Source,
						e.Counterparty, e.Hops, e.Direction),
					Basis:              scoring.BasisProviderReported,
					Hops:               e.Hops,
					VerificationStatus: scoring.VerificationReported,
				})
				m.evidence = append(m.evidence, EvidenceRef{
					Kind: "exposure",
					Ref: ProviderEvidence{
						Source: pr.Source, FetchedAt: pr.FetchedAt,
						Identity: fmt.Sprintf("%s:h%d", e.Counterparty, e.Hops),
					},
				})
			}
		}
	}
	m.exposures = blk
	m.completeness.Exposure = DimensionStatus{
		Requested:  true,
		DataStatus: availabilityOf(providerUsable),
		Usable:     providerUsable,
	}
}

// availabilityOf reports AVAILABLE when any provider leg is usable.
func availabilityOf(any bool) string {
	if any {
		return DataAvailable
	}
	return DataUnavailable
}

// foldStatus implements §3.5: zero usable inputs → INSUFFICIENT_DATA;
// any degraded/failed input alongside usable → PARTIAL; else COMPLETED.
func foldStatus(usable, degraded int) string {
	switch {
	case usable == 0:
		return StatusInsufficientData
	case degraded > 0:
		return StatusPartial
	default:
		return StatusCompleted
	}
}

// categoryType resolves code→category_type via the risk_categories map;
// unseen codes land at OTHER (dynamic upstream categories).
func categoryType(code string, catTypes map[string]string) string {
	if t, ok := catTypes[strings.ToUpper(code)]; ok {
		return t
	}
	return "OTHER"
}

func orUnavailable(s string) string {
	if s == "" {
		return DataUnavailable
	}
	return s
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
