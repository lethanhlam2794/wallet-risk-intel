package walletattribution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
)

// Importer streams snapshot files into stg_wallet_labels under runID.
// Staging inserts are idempotent via the (run_id, fingerprint) PK; a row
// that parses but fails the EVM-address check is reject-counted, and an
// insert affecting 0 rows is a fingerprint collision surfaced to the
// pipeline's reconciliation check.
type Importer struct {
	networks map[string]int64 // code → id (validated against live registry)
}

// NewImporter wires the importer with the live network registry.
func NewImporter(networks map[string]int64) *Importer {
	return &Importer{networks: networks}
}

// Import stages every snapshot file under runID. Per-file stats feed the
// pipeline's parsed-vs-staged reconciliation and per-network scoping.
func (imp *Importer) Import(ctx context.Context, q *db.Queries,
	snap provider.Snapshot, runID pgtype.UUID) (provider.ImportResult, *ImportMeta, error) {

	meta := &ImportMeta{}
	var res provider.ImportResult
	for _, f := range snap.Files {
		fs, err := imp.importFile(ctx, q, f, runID)
		if err != nil {
			return res, meta, err
		}
		res.Labels += fs.Staged
		res.Rejected += fs.Rejected
		meta.Files = append(meta.Files, *fs)
	}
	return res, meta, nil
}

func (imp *Importer) importFile(ctx context.Context, q *db.Queries,
	f provider.File, runID pgtype.UUID) (*FileStat, error) {

	st := &FileStat{Name: f.Name}
	fh, err := os.Open(f.Path)
	if err != nil {
		return st, fmt.Errorf("wallet-attribution import %q: %w", f.Name, err)
	}
	defer fh.Close()

	// The filename asserts the chain when its stem is a known network
	// (ethereum.csv → ethereum); otherwise it is derived from the first
	// emitted row. Rows disagreeing with the asserted network are rejected
	// per-row, and a fully-mismatched file fails the run at validation
	// (zero staged rows for a provided file = FAILED).
	fileNetwork := ""
	if stem, ok := strings.CutSuffix(f.Name, ".csv"); ok {
		if _, known := imp.networks[strings.ToLower(stem)]; known {
			fileNetwork = strings.ToLower(stem)
			st.NetworkCode = fileNetwork
		}
	}
	emit := func(r CSVRow) error {
		if fileNetwork == "" {
			code := strings.ToLower(r.Network)
			if _, ok := imp.networks[code]; !ok {
				return fmt.Errorf("wallet-attribution import %q: unsupported network %q", f.Name, r.Network)
			}
			fileNetwork = code
			st.NetworkCode = code
		}
		if r.Network != fileNetwork {
			st.Rejected++ // wrong network for the file's chain
			return nil
		}
		pl, err := MapLabel(r, f.Checksum)
		if err != nil {
			st.Rejected++ // invalid EVM address
			return nil
		}
		ev, _ := json.Marshal(pl.Evidence)
		n, err := q.StgInsertWalletLabel(ctx, db.StgInsertWalletLabelParams{
			RunID:            runID,
			NetworkCode:      fileNetwork,
			Address:          pl.Address,
			EntityName:       pgtext(pl.EntityName),
			Label:            pl.Label,
			UpstreamCategory: pl.UpstreamCategory,
			SourceField:      pgtext(pl.SourceField),
			SourceUrl:        pgtext(pl.SourceURL),
			Confidence:       pl.Confidence,
			LastUpdated:      pgtext(r.LastUpdated),
			Fingerprint:      pl.Fingerprint,
			Evidence:         ev,
		})
		if err != nil {
			return fmt.Errorf("wallet-attribution import %q: stage line %d: %w", f.Name, r.Line, err)
		}
		st.Attempted++
		st.Staged += n
		return nil
	}

	pstats, err := Parse(ctx, fh, emit)
	if err != nil {
		return st, err
	}
	st.Parsed = pstats.Rows
	st.Rejected += pstats.Rejected
	return st, nil
}

func pgtext(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
