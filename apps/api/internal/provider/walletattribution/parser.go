package walletattribution

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"unicode/utf8"
)

// expectedHeader is pinned — schema drift upstream must fail the run loudly
// rather than silently misalign columns (acceptance: schema drift → FAILED).
var expectedHeader = []string{
	"address", "network", "entity", "label", "category",
	"source", "source_url", "confidence", "last_updated",
}

// maxCategoryLen matches risk_categories.code VARCHAR(60).
const maxCategoryLen = 60

// maxRowsPerFile bounds a single artifact file (~2M rows covers the ~105k
// current dataset with orders of magnitude of headroom).
const maxRowsPerFile = 2_000_000

// ParseStats counts structural row outcomes for one file.
type ParseStats struct {
	Rows     int64 // data rows seen (header excluded)
	Rejected int64 // structural rejects (never emitted)
}

// Parse streams CSV rows. Each structurally-valid data row is passed to
// emit; structural rejects (bad UTF-8, ragged arity, oversized category,
// row cap exceeded) are counted, never emitted, and never fail the run.
// Malformed CSV syntax and header drift are fatal errors — a file we cannot
// parse reliably is not a usable snapshot (never silently degrade).
func Parse(ctx context.Context, r io.Reader, emit func(CSVRow) error) (ParseStats, error) {
	var st ParseStats
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1 // manual arity check → reject, don't fail
	rd.ReuseRecord = true

	hdr, err := rd.Read()
	if err != nil {
		return st, fmt.Errorf("wallet-attribution csv: read header: %w", err)
	}
	if len(hdr) != len(expectedHeader) {
		return st, fmt.Errorf("wallet-attribution csv: header drift: got %d columns %v", len(hdr), hdr)
	}
	for i, want := range expectedHeader {
		if hdr[i] != want {
			return st, fmt.Errorf("wallet-attribution csv: header drift: column %d = %q, want %q", i, hdr[i], want)
		}
	}

	line := 1
	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		rec, err := rd.Read()
		if err == io.EOF {
			return st, nil
		}
		if err != nil {
			return st, fmt.Errorf("wallet-attribution csv: malformed record near line %d: %w", line, err)
		}
		line++
		st.Rows++
		if st.Rows > maxRowsPerFile {
			return st, fmt.Errorf("wallet-attribution csv: row cap exceeded (%d)", maxRowsPerFile)
		}
		if len(rec) != len(expectedHeader) {
			st.Rejected++ // ragged row — valid CSV, wrong arity
			continue
		}
		valid := true
		for _, f := range rec {
			if !utf8.ValidString(f) {
				valid = false
				break
			}
		}
		if !valid {
			st.Rejected++ // invalid UTF-8 — evidence we cannot store faithfully
			continue
		}
		if len(rec[4]) > maxCategoryLen {
			st.Rejected++ // category code exceeds schema bound
			continue
		}
		row := CSVRow{
			Line:        line,
			Address:     rec[0],
			Network:     rec[1],
			Entity:      rec[2],
			Label:       rec[3],
			Category:    rec[4],
			Source:      rec[5],
			SourceURL:   rec[6],
			Confidence:  rec[7],
			LastUpdated: rec[8],
		}
		if err := emit(row); err != nil {
			return st, err
		}
	}
}
