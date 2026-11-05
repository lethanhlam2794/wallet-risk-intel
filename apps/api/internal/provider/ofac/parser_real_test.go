package ofac

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestParse_RealOFACFile is a manual-verification harness: it only runs when
// OFAC_REAL_XML points at a downloaded SDN_ADVANCED.XML (never committed).
// It proves the streaming parser survives the real ~128MB document shape.
func TestParse_RealOFACFile(t *testing.T) {
	path := os.Getenv("OFAC_REAL_XML")
	if path == "" {
		t.Skip("set OFAC_REAL_XML to run the real-document verification")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var entities, wallets int
	start := time.Now()
	meta, err := NewParser().Parse(context.Background(), f, &countSink{n: &entities, w: &wallets})
	require.NoError(t, err)
	t.Logf("parsed real OFAC document in %s: %d parties, %d digital-currency listings, %d profiles with programs",
		time.Since(start), entities, wallets, len(meta.ProgramsByProfile))
	require.NotNil(t, meta.DateOfIssue)
	require.Positive(t, entities)
	require.Positive(t, wallets)
	require.Positive(t, len(meta.ProgramsByProfile))
}

type countSink struct{ n, w *int }

func (s *countSink) HandleEntity(_ context.Context, e *ParsedEntity) error {
	*s.n++
	*s.w += len(e.Wallets)
	return nil
}
