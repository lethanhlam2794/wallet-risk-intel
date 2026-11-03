package ofac

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
)

// Importer streams a snapshot file into the stg_* tables scoped by run_id.
type Importer struct {
	// networks maps network code → blockchain_networks.id for the two
	// indexed chains; codes absent from the map yield NULL network_id.
	networks map[string]int64
}

// NewImporter builds the importer; networks maps e.g. {"ethereum":1,"bsc":2}.
func NewImporter(networks map[string]int64) *Importer {
	return &Importer{networks: networks}
}

// Import parses snapshotPath and writes all staging rows under runID.
// Staging is idempotent per run: re-running the same fixture for the same
// run produces identical cardinality (unique index + ON CONFLICT).
// The returned DocMeta carries DateOfIssue and buffered program/reference
// associations for publish-time validation.
func (imp *Importer) Import(ctx context.Context, q *db.Queries, snapshotPath string, runID uuid.UUID) (provider.ImportResult, *DocMeta, error) {
	f, err := os.Open(snapshotPath)
	if err != nil {
		return provider.ImportResult{}, nil, fmt.Errorf("ofac import: open: %w", err)
	}
	defer f.Close()

	res := provider.ImportResult{}
	sink := &stagingSink{q: q, runID: runID, networks: imp.networks, res: &res}
	meta, err := NewParser().Parse(ctx, f, sink)
	if err != nil {
		return res, nil, err
	}

	// Flush the trailing SanctionsEntries joins (programs + references).
	programs := distinctPrograms(meta.ProgramsByProfile)
	for _, code := range programs {
		if err := q.StgInsertProgram(ctx, db.StgInsertProgramParams{
			RunID: pguuid(runID), Code: code,
		}); err != nil {
			return res, meta, fmt.Errorf("ofac import: program %q: %w", code, err)
		}
		res.Programs++
	}
	for profileID, codes := range meta.ProgramsByProfile {
		for _, code := range codes {
			if err := q.StgInsertEntityProgram(ctx, db.StgInsertEntityProgramParams{
				RunID: pguuid(runID), ExternalEntityID: profileID, ProgramCode: code,
			}); err != nil {
				return res, meta, fmt.Errorf("ofac import: entity program: %w", err)
			}
			res.EntityPrograms++
		}
	}
	for profileID, refs := range meta.ReferencesByProfile {
		for _, ref := range refs {
			if err := q.StgInsertReference(ctx, db.StgInsertReferenceParams{
				RunID:            pguuid(runID),
				ExternalEntityID: profileID,
				ReferenceType:    ref.ReferenceType,
				Title:            pgtext(ref.Title),
				ReferenceUrl:     pgtext(ref.URL),
				PublishedAt:      pgts(ref.PublishedAt),
				Details:          mustJSON(ref.Details),
			}); err != nil {
				return res, meta, fmt.Errorf("ofac import: reference: %w", err)
			}
			res.References++
		}
	}
	return res, meta, nil
}

func distinctPrograms(m map[string][]string) []string {
	set := map[string]bool{}
	for _, codes := range m {
		for _, c := range codes {
			set[c] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// stagingSink writes each parsed entity to staging as the stream proceeds.
type stagingSink struct {
	q        *db.Queries
	runID    uuid.UUID
	networks map[string]int64
	res      *provider.ImportResult
}

func (s *stagingSink) HandleEntity(ctx context.Context, e *ParsedEntity) error {
	if e.PrimaryName == "" {
		// Never silently drop: staged with empty name and counted as rejected
		// so the run counters surface the data-quality hole.
		s.res.Rejected++
	}
	if err := s.q.StgInsertEntity(ctx, db.StgInsertEntityParams{
		RunID:            pguuid(s.runID),
		ExternalEntityID: e.ExternalID,
		PrimaryName:      e.PrimaryName,
		EntityType:       pgtext(e.EntityType),
		Details:          mustJSON(e.Details),
	}); err != nil {
		return fmt.Errorf("ofac import: entity %q: %w", e.ExternalID, err)
	}
	s.res.Entities++

	for _, w := range e.Wallets {
		var netID pgtype.Int8
		if w.NetworkCode != "" {
			if id, ok := s.networks[w.NetworkCode]; ok {
				netID = pgtype.Int8{Int64: id, Valid: true}
			}
		}
		if err := s.q.StgInsertWallet(ctx, db.StgInsertWalletParams{
			RunID:              pguuid(s.runID),
			ExternalEntityID:   e.ExternalID,
			ListedAddress:      w.ListedAddress,
			NormalizedAddress:  w.NormalizedAddress,
			CurrencyCode:       pgtext(w.CurrencyCode),
			NetworkID:          netID,
			IdentifierType:     w.IdentifierType,
			SourceIdentifierID: pgtext(w.SourceIdentifierID),
			MatchScope:         w.MatchScope,
			Evidence:           mustJSON(w.Evidence),
		}); err != nil {
			return fmt.Errorf("ofac import: wallet %q: %w", w.ListedAddress, err)
		}
		s.res.Wallets++
	}
	return nil
}

// helpers

func pgtext(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func pguuid(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func pgts(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func mustJSON(v any) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
