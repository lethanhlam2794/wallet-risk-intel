package ofac

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collectSink struct {
	entities []*ParsedEntity
	err      error
}

func (s *collectSink) HandleEntity(_ context.Context, e *ParsedEntity) error {
	if s.err != nil {
		return s.err
	}
	s.entities = append(s.entities, e)
	return nil
}

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParse_ValidFixture(t *testing.T) {
	p := NewParser()
	sink := &collectSink{}
	meta, err := p.Parse(context.Background(), openFixture(t, "valid_small.xml"), sink)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Len(t, sink.entities, 2)

	laz := sink.entities[0]
	assert.Equal(t, "31940", laz.ExternalID)
	assert.Equal(t, "LAZARUS GROUP", laz.PrimaryName)
	assert.Equal(t, "Entity", laz.EntityType)
	assert.Contains(t, laz.NonPrimaryNames, "APT38")

	// 3 digital currency features staged (ETH, USDT, XBT).
	require.Len(t, laz.Wallets, 3)
	byID := map[string]ParsedWallet{}
	for _, w := range laz.Wallets {
		byID[w.SourceIdentifierID] = w
	}
	eth := byID["40577"]
	assert.Equal(t, "ETH", eth.CurrencyCode)
	assert.Equal(t, "0x098b716b8aaf21512996dc57eb0615e2383e2f96", eth.NormalizedAddress)
	assert.Equal(t, "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", eth.ListedAddress)
	assert.Equal(t, "CHAIN_SPECIFIC", eth.MatchScope)
	assert.Equal(t, "ethereum", eth.NetworkCode)

	usdt := byID["40578"]
	assert.Equal(t, "USDT", usdt.CurrencyCode)
	assert.Equal(t, "UNRESOLVED", usdt.MatchScope)
	assert.Empty(t, usdt.NetworkCode)

	xbt := byID["40579"]
	assert.Equal(t, "XBT", xbt.CurrencyCode)
	assert.Equal(t, "CHAIN_SPECIFIC", xbt.MatchScope)
	assert.Empty(t, xbt.NetworkCode)

	kim := sink.entities[1]
	assert.Equal(t, "KIM SOK IL", kim.PrimaryName)
	assert.Equal(t, "Individual", kim.EntityType)
	require.Len(t, kim.Wallets, 2)
	nets := map[string]string{}
	for _, w := range kim.Wallets {
		nets[w.CurrencyCode] = w.NetworkCode
		assert.Equal(t, "CHAIN_SPECIFIC", w.MatchScope)
	}
	assert.Equal(t, "bsc", nets["BNB"], "BNB+EVM resolves to eip155:56")
	assert.Equal(t, "bsc", nets["BSC"], "BSC+EVM resolves to eip155:56")

	// Programs come from the trailing SanctionsEntries section.
	assert.ElementsMatch(t, []string{"DPRK3", "CYBER2"}, meta.ProgramsByProfile["31940"])
	assert.ElementsMatch(t, []string{"DPRK3"}, meta.ProgramsByProfile["40243"])

	// Legal-basis references resolved from LegalBasisTypeValues.
	refs := meta.ReferencesByProfile["31940"]
	require.Len(t, refs, 1)
	assert.Equal(t, "LEGAL_AUTHORITY", refs[0].ReferenceType)
	assert.Equal(t, "Executive Order 14024", refs[0].Title)

	// DateOfIssue extracted for the freshness guard.
	require.NotNil(t, meta.DateOfIssue)
	assert.Equal(t, "2026-10-28", meta.DateOfIssue.Format("2006-01-02"))
}

func TestParse_TruncatedFails(t *testing.T) {
	sink := &collectSink{}
	_, err := NewParser().Parse(context.Background(), openFixture(t, "truncated.xml"), sink)
	require.Error(t, err)
}

func TestParse_MalformedFails(t *testing.T) {
	sink := &collectSink{}
	_, err := NewParser().Parse(context.Background(), openFixture(t, "malformed.xml"), sink)
	require.Error(t, err)
}

func TestParse_WrongRootFails(t *testing.T) {
	sink := &collectSink{}
	_, err := NewParser().Parse(context.Background(), openFixture(t, "wrong_root.xml"), sink)
	require.Error(t, err)
}

func TestParse_DOCTYPEAttackFails(t *testing.T) {
	sink := &collectSink{}
	_, err := NewParser().Parse(context.Background(), openFixture(t, "doctype_attack.xml"), sink)
	require.Error(t, err)
}

func TestParse_BOMHandled(t *testing.T) {
	data, err := os.ReadFile("testdata/valid_small.xml")
	require.NoError(t, err)
	bom := append([]byte{0xEF, 0xBB, 0xBF}, data...)
	sink := &collectSink{}
	meta, err := NewParser().Parse(context.Background(), strings.NewReader(string(bom)), sink)
	require.NoError(t, err)
	assert.Len(t, sink.entities, 2)
	assert.NotNil(t, meta.DateOfIssue)
}

func TestParse_DuplicateIdentifiersDeduped(t *testing.T) {
	sink := &collectSink{}
	_, err := NewParser().Parse(context.Background(), openFixture(t, "duplicate_identifiers.xml"), sink)
	require.NoError(t, err)
	require.Len(t, sink.entities, 1)
	// Feature 100 lists the same ETH address under two FeatureVersions:
	// the (entity, normalized, type, currency, featureID) tuple collapses to 1
	// row for Feature@100 plus a distinct row for Feature@101.
	wallets := sink.entities[0].Wallets
	assert.Len(t, wallets, 2)
}
