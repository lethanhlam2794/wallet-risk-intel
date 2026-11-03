package ofac

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Currency-code × address-format network resolution rules
// (reports/ofac-xml-shape.md, verified 2026-11-03).
func TestResolveListing(t *testing.T) {
	cases := []struct {
		currency, addr string
		wantNet        string
		wantScope      string
	}{
		{"ETH", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "ethereum", "CHAIN_SPECIFIC"},
		{"BSC", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "bsc", "CHAIN_SPECIFIC"},
		{"BNB", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "bsc", "CHAIN_SPECIFIC"},
		// Ambiguous multi-chain codes on EVM format: chain not evidenced.
		{"USDT", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "", "UNRESOLVED"},
		{"USDC", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "", "UNRESOLVED"},
		{"ARB", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "", "UNRESOLVED"},
		{"ETH", "garbage-not-evm", "", "CHAIN_SPECIFIC"},
		// Non-EVM listings are chain-specific on chains we do not index.
		{"XBT", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", "", "CHAIN_SPECIFIC"},
		{"XMR", "44AFFq5kSiGBoZ4NMDwYtS18e4hD5K8fY", "", "CHAIN_SPECIFIC"},
		{"TRX", "TNPee2vM4kD8x3mVqF6wKpL7bEJ4hX9cN", "", "CHAIN_SPECIFIC"},
		{"SOL", "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", "", "CHAIN_SPECIFIC"},
	}
	for _, c := range cases {
		net, scope := resolveListing(c.currency, c.addr)
		assert.Equal(t, c.wantNet, net, "%s %s", c.currency, c.addr)
		assert.Equal(t, c.wantScope, scope, "%s %s", c.currency, c.addr)
	}
}

func TestCurrencyFromFeatureType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Digital Currency Address - ETH", "ETH"},
		{"Digital Currency Address - XBT", "XBT"},
		{"Digital Currency Address -  usdt ", "USDT"},
		{"Digital Currency Address - BSC", "BSC"},
		{"Vessel Call Sign", ""},
		{"Birthdate", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, currencyFromFeatureType(c.in), c.in)
	}
}
