package walletattribution

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func baseRow() CSVRow {
	return CSVRow{
		Address:     "0xBBbd1bbb4f9b936c3604906d7592a644071de884",
		Network:     "ethereum",
		Entity:      "Binance",
		Label:       "Binance: Hot Wallet",
		Category:    "exchange",
		Source:      "eth-labels+cex-list",
		SourceURL:   "https://example.com",
		Confidence:  "high",
		LastUpdated: "2026-10-01",
	}
}

func TestFingerprint_PinnedFormula(t *testing.T) {
	pl, err := MapLabel(baseRow(), "sumA")
	if err != nil {
		t.Fatal(err)
	}
	// Formula uses code strings, never DB ids.
	want := sha256.Sum256([]byte("wallet_attribution|ethereum|" +
		"0xbbbd1bbb4f9b936c3604906d7592a644071de884|EXCHANGE|Binance: Hot Wallet"))
	if pl.Fingerprint != hex.EncodeToString(want[:]) {
		t.Fatalf("fingerprint mismatch: got %s", pl.Fingerprint)
	}
	// Stable across calls.
	pl2, _ := MapLabel(baseRow(), "sumB")
	if pl.Fingerprint != pl2.Fingerprint {
		t.Fatal("fingerprint not stable")
	}
	// Label is in the preimage: different label → different fingerprint.
	r := baseRow()
	r.Label = "Other"
	pl3, _ := MapLabel(r, "sumA")
	if pl3.Fingerprint == pl.Fingerprint {
		t.Fatal("fingerprint ignores label")
	}
}

func TestMapLabel_NormalizationAndPassthrough(t *testing.T) {
	pl, err := MapLabel(baseRow(), "sumA")
	if err != nil {
		t.Fatal(err)
	}
	if pl.NormalizedAddress != "0xbbbd1bbb4f9b936c3604906d7592a644071de884" {
		t.Fatalf("normalized = %q", pl.NormalizedAddress)
	}
	if pl.CategoryCode != "EXCHANGE" || pl.UpstreamCategory != "exchange" {
		t.Fatalf("category code=%q upstream=%q", pl.CategoryCode, pl.UpstreamCategory)
	}
	if pl.Confidence != "high" {
		t.Fatalf("confidence = %q", pl.Confidence)
	}
	if pl.ObservedAt == nil || pl.Evidence["observed_at"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("observed_at = %v ev=%v", pl.ObservedAt, pl.Evidence["observed_at"])
	}
	if pl.Evidence["source_field"] != "eth-labels+cex-list" {
		t.Fatalf("evidence source_field = %v", pl.Evidence["source_field"])
	}
	ur := pl.Evidence["upstream_record"].(map[string]any)
	if ur["entity"] != "Binance" || ur["label"] != "Binance: Hot Wallet" {
		t.Fatalf("upstream_record = %v", ur)
	}
	if pl.Evidence["fetch_checksum_part"] != "sumA" {
		t.Fatalf("checksum part = %v", pl.Evidence["fetch_checksum_part"])
	}
}

func TestMapLabel_ConfidenceFallbackAndDateUnparsed(t *testing.T) {
	r := baseRow()
	r.Confidence = "bogus"
	r.LastUpdated = "not-a-date"
	pl, err := MapLabel(r, "sumA")
	if err != nil {
		t.Fatal(err)
	}
	if pl.Confidence != "unknown" {
		t.Fatalf("confidence = %q", pl.Confidence)
	}
	if !pl.DateUnparsed || pl.ObservedAt != nil {
		t.Fatalf("DateUnparsed=%v ObservedAt=%v", pl.DateUnparsed, pl.ObservedAt)
	}
	if pl.Evidence["date_unparsed"] != true {
		t.Fatalf("evidence = %v", pl.Evidence)
	}
}

func TestMapLabel_BadAddressRejected(t *testing.T) {
	r := baseRow()
	r.Address = "not-an-address"
	if _, err := MapLabel(r, "sumA"); err == nil {
		t.Fatal("expected bad-address error")
	}
}

func TestMapLabel_NullEntityStaysLiteral(t *testing.T) {
	r := baseRow()
	r.Entity = "Null"
	pl, err := MapLabel(r, "sumA")
	if err != nil {
		t.Fatal(err)
	}
	if pl.EntityName != "Null" {
		t.Fatalf("entity = %q", pl.EntityName)
	}
}
