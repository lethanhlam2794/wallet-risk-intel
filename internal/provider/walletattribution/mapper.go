package walletattribution

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"checkwallet/internal/wallet"
)

// SourceCode is the registered data_sources.code for this adapter.
const SourceCode = "wallet_attribution"

// Fingerprint is the pinned identity formula (code strings, never DB ids):
// sha256("wallet_attribution|<network_code>|<normalized_address>|<CATEGORY>|<label>")
// Stable across runs, so upsert-by-fingerprint is idempotent and a re-list
// revives rather than duplicates.
func Fingerprint(networkCode, normalizedAddress, categoryCode, label string) string {
	sum := sha256.Sum256([]byte("wallet_attribution|" + networkCode + "|" +
		normalizedAddress + "|" + categoryCode + "|" + label))
	return hex.EncodeToString(sum[:])
}

// MapLabel converts a validated CSV row to a ParsedLabel. A bad address is
// the only reject path — everything else is preserved verbatim (never
// invent or drop evidence).
func MapLabel(r CSVRow, fileChecksum string) (*ParsedLabel, error) {
	norm, err := wallet.NormalizeEVM(r.Address)
	if err != nil {
		return nil, fmt.Errorf("address %q: %w", r.Address, err)
	}
	cat := strings.ToUpper(r.Category)
	conf := r.Confidence
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "unknown"
	}
	pl := &ParsedLabel{
		Address:           r.Address,
		NormalizedAddress: norm,
		NetworkCode:       r.Network,
		EntityName:        r.Entity,
		Label:             r.Label,
		UpstreamCategory:  r.Category,
		CategoryCode:      cat,
		SourceField:       r.Source,
		SourceURL:         r.SourceURL,
		Confidence:        conf,
		Fingerprint:       Fingerprint(r.Network, norm, cat, r.Label),
	}
	ev := map[string]any{
		"source_field": r.Source,
		"upstream_record": map[string]any{
			"label":  r.Label,
			"entity": r.Entity,
		},
		"fetch_checksum_part": fileChecksum,
	}
	if t, ok := parseObservedAt(r.LastUpdated); ok {
		pl.ObservedAt = &t
		ev["observed_at"] = t.UTC().Format(time.RFC3339)
	} else if r.LastUpdated != "" {
		// Unparseable date stays NULL — never invent observation times;
		// the raw string remains in stg.last_updated for forensics.
		pl.DateUnparsed = true
		ev["date_unparsed"] = true
	}
	pl.Evidence = ev
	return pl, nil
}

// parseObservedAt accepts the upstream date stamp (YYYY-MM-DD). Anything
// else is unparseable, not fatal.
func parseObservedAt(s string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}
