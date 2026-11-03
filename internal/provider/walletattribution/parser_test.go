package walletattribution

import (
	"context"
	"os"
	"strings"
	"testing"
)

func collect(t *testing.T, path string) ([]CSVRow, ParseStats) {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer fh.Close()
	var rows []CSVRow
	st, err := Parse(context.Background(), fh, func(r CSVRow) error {
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		t.Fatalf("Parse %s: %v", path, err)
	}
	return rows, st
}

func TestParse_GoldenFixture(t *testing.T) {
	rows, st := collect(t, "testdata/labels_golden.csv")
	if st.Rows != 7 {
		t.Fatalf("parsed rows = %d, want 7", st.Rows)
	}
	if len(rows) != 7 {
		t.Fatalf("emitted rows = %d, want 7 (all rows valid structurally)", len(rows))
	}
	if st.Rejected != 0 {
		t.Fatalf("rejected = %d, want 0", st.Rejected)
	}
	// Quoted-comma fields preserved verbatim.
	if rows[1].Entity != "Binance, Inc." || rows[1].Label != "Binance: Hot Wallet, US" {
		t.Fatalf("quoted fields mangled: %+v", rows[1])
	}
	// Multi-source '+' preserved verbatim.
	if rows[0].Source != "eth-labels+graphsense" {
		t.Fatalf("source_field = %q", rows[0].Source)
	}
	// Mixed-case address kept verbatim (normalization is the mapper's job).
	if rows[2].Address != "0xBBbd1bbb4f9b936c3604906d7592a644071de884" {
		t.Fatalf("address = %q", rows[2].Address)
	}
	// 'Null' entity is literal evidence, not empty.
	if rows[2].Entity != "Null" {
		t.Fatalf("entity = %q", rows[2].Entity)
	}
	// Empty entity is preserved as empty.
	if rows[3].Entity != "" {
		t.Fatalf("entity = %q", rows[3].Entity)
	}
	// Unparseable last_updated passes through (mapper flags it).
	if rows[5].LastUpdated != "not-a-date" {
		t.Fatalf("last_updated = %q", rows[5].LastUpdated)
	}
}

func TestParse_HeaderDrift(t *testing.T) {
	fh, err := os.Open("testdata/labels_badheader.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	_, err = Parse(context.Background(), fh, func(CSVRow) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "header drift") {
		t.Fatalf("expected header drift error, got %v", err)
	}
}

func TestParse_TruncatedMidRecord(t *testing.T) {
	csvData := "address,network,entity,label,category,source,source_url,confidence,last_updated\n" +
		"0x0ac2d6f5f5afc669d3ca38f830dad2b4f238ad3f,ethereum,0x,\"unterminated"
	_, err := Parse(context.Background(), strings.NewReader(csvData), func(CSVRow) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed error, got %v", err)
	}
}

func TestParse_RaggedAndBadUTF8RejectedNotFatal(t *testing.T) {
	hdr := "address,network,entity,label,category,source,source_url,confidence,last_updated\n"
	good := "0x0000000000000000000000000000000000000001,ethereum,E,L,entity,s,u,high,2026-10-01\n"
	ragged := "0x0000000000000000000000000000000000000002,ethereum,E,L,entity,s,u,high,2026-10-01,EXTRA\n"
	badUTF8 := "0x0000000000000000000000000000000000000003,ethereum,\xff\xfe,L,entity,s,u,high,2026-10-01\n"
	var rows []CSVRow
	st, err := Parse(context.Background(), strings.NewReader(hdr+good+ragged+badUTF8),
		func(r CSVRow) error { rows = append(rows, r); return nil })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Rows != 3 || st.Rejected != 2 || len(rows) != 1 {
		t.Fatalf("rows=%d rejected=%d emitted=%d, want 3/2/1", st.Rows, st.Rejected, len(rows))
	}
}

func TestParse_OversizedCategoryRejected(t *testing.T) {
	hdr := "address,network,entity,label,category,source,source_url,confidence,last_updated\n"
	big := "0x0000000000000000000000000000000000000001,ethereum,E,L," + strings.Repeat("x", 61) + ",s,u,high,2026-10-01\n"
	st, err := Parse(context.Background(), strings.NewReader(hdr+big), func(CSVRow) error { return nil })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if st.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", st.Rejected)
	}
}
