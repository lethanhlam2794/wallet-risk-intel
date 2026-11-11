package scoring

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPriceForV1_1_Pinned(t *testing.T) {
	cases := []struct {
		net, asset   string
		wantMicro    int64
		wantDecimals int
	}{
		{"ethereum", "native", 3_000_000_000, 18},
		{"ethereum", "", 3_000_000_000, 18},
		{"ethereum", "0xdac17f958d2ee523a2206206994597c13d831ec7", 1_000_000, 6},
		{"ethereum", "0xa0b86991c6218b36c1d19f4a2e9eb0ce3606eb48", 1_000_000, 6},
		{"ethereum", "0x6b175474e89094c44da98b954eedeac495271d0f", 1_000_000, 18},
		{"bsc", "native", 600_000_000, 18},
		{"bsc", "0x55d398326f99059ff775485246999027b3197955", 1_000_000, 18},
		{"bsc", "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d", 1_000_000, 18},
		{"bsc", "0x1af3f329e8be154074d8769d1ffa4ee058b1dbc3", 1_000_000, 18},
	}
	for _, c := range cases {
		p, ok := PriceForV1_1(c.net, c.asset)
		require.True(t, ok, "%s:%s must be pinned", c.net, c.asset)
		assert.Equal(t, c.wantMicro, p.MicroUSD, c.asset)
		assert.Equal(t, c.wantDecimals, p.Decimals, c.asset)
	}

	// Case-insensitive contract keys.
	p, ok := PriceForV1_1("ethereum", "0xDAC17F958D2EE523A2206206994597C13D831EC7")
	require.True(t, ok)
	assert.Equal(t, int64(1_000_000), p.MicroUSD)

	for _, unpriced := range [][2]string{
		{"ethereum", "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		{"tron", "native"}, {"bsc", "0x0000000000000000000000000000000000000000"},
	} {
		_, ok := PriceForV1_1(unpriced[0], unpriced[1])
		assert.False(t, ok, "%s:%s must not be priced", unpriced[0], unpriced[1])
	}
}

// The $100 boundary is exact: at-floor is meaningful, one unit below is
// not — big.Int arithmetic, never float.
func TestIsMeaningfulV1_1_FloorBoundary(t *testing.T) {
	// ETH @ $3,000: $100 = 1/30 ETH; 10^18/30 = 33333333333333333.33 wei.
	m, priced, err := IsMeaningfulV1_1("ethereum", "native", "33333333333333333")
	require.NoError(t, err)
	assert.True(t, priced)
	assert.False(t, m, "1 wei below the $100 boundary must not count")

	m, _, err = IsMeaningfulV1_1("ethereum", "native", "33333333333333334")
	require.NoError(t, err)
	assert.True(t, m)

	// USDT-ETH @ $1, 6 decimals: exactly 100 units = $100.000000.
	m, _, err = IsMeaningfulV1_1("ethereum",
		"0xdac17f958d2ee523a2206206994597c13d831ec7", "100000000")
	require.NoError(t, err)
	assert.True(t, m, "exact $100 is meaningful")

	m, _, err = IsMeaningfulV1_1("ethereum",
		"0xdac17f958d2ee523a2206206994597c13d831ec7", "99999999")
	require.NoError(t, err)
	assert.False(t, m, "$99.999999 is not meaningful")

	// BNB @ $600: $100 = 1/6 BNB = 166666666666666666.67 wei.
	m, _, err = IsMeaningfulV1_1("bsc", "native", "166666666666666666")
	require.NoError(t, err)
	assert.False(t, m)
	m, _, err = IsMeaningfulV1_1("bsc", "native", "166666666666666667")
	require.NoError(t, err)
	assert.True(t, m)
}

func TestIsMeaningfulV1_1_UnpricedAndMalformed(t *testing.T) {
	// Unpriced asset: evidence-only, priced=false, never an error.
	m, priced, err := IsMeaningfulV1_1("ethereum",
		"0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "999999999999999999999")
	require.NoError(t, err)
	assert.False(t, m)
	assert.False(t, priced)

	// Malformed amounts must never pass as meaningful.
	for _, bad := range []string{"abc", "", "1.5", "0x10", "  ", "1e18"} {
		m, priced, err := IsMeaningfulV1_1("ethereum", "native", bad)
		assert.Error(t, err, "amount %q must be rejected", bad)
		assert.False(t, m)
		assert.True(t, priced)
	}
}

func TestParseScoringAssetPrices(t *testing.T) {
	// Empty = no overlay, not an error.
	m, err := ParseScoringAssetPrices("")
	require.NoError(t, err)
	assert.Nil(t, m)

	m, err = ParseScoringAssetPrices(`{
		"ethereum:native": {"micro_usd": 3100000000, "decimals": 18},
		"bsc:0x55d398326f99059ff775485246999027b3197955":
			{"micro_usd": 1000000, "decimals": 18}
	}`)
	require.NoError(t, err)
	require.Len(t, m, 2)
	assert.Equal(t, int64(3_100_000_000), m["ethereum:native"].MicroUSD)

	for _, bad := range []string{
		`{not json}`,
		`{"ethereum:0xZZZ": {"micro_usd": 1, "decimals": 6}}`,
		`{"ethereum": {"micro_usd": 1, "decimals": 6}}`,
		`{"ethereum:native": {"micro_usd": -1, "decimals": 18}}`,
		// finding #5: micro_usd must be > 0 — zero silently de-prices.
		`{"ethereum:native": {"micro_usd": 0, "decimals": 18}}`,
		`{"ethereum:native": {"micro_usd": 1, "decimals": 31}}`,
		`{"ethereum:native": {"micro_usd": 1, "decimals": -1}}`,
	} {
		_, err := ParseScoringAssetPrices(bad)
		assert.Error(t, err, "input %s must fail closed", bad)
	}
}

// A14: overlay network codes validate against the resolver's ACTIVE
// codes at wiring — a syntactically valid but unserved network fails.
func TestValidateScoringAssetPriceNetworks(t *testing.T) {
	active := map[string]bool{"ethereum": true, "bsc": true}
	overlay, err := ParseScoringAssetPrices(
		`{"ethereum:native": {"micro_usd": 1, "decimals": 18}}`)
	require.NoError(t, err)
	assert.NoError(t, ValidateScoringAssetPriceNetworks(overlay, active))

	overlay, err = ParseScoringAssetPrices(
		`{"tron:native": {"micro_usd": 1, "decimals": 6}}`)
	require.NoError(t, err) // shape is fine — membership fails instead
	assert.Error(t, ValidateScoringAssetPriceNetworks(overlay, active))
}
