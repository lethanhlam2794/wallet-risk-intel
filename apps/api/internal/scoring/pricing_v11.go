package scoring

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// AssetPriceV1_1 is one pinned-price entry: micro-USD per whole unit and
// the asset's decimal exponent. All v1.1 money math is big.Int over this
// shape — float64 is forbidden in the pricing path.
type AssetPriceV1_1 struct {
	MicroUSD int64 `json:"micro_usd"`
	Decimals int   `json:"decimals"`
}

// NativeAssetV1_1 is the sentinel asset key for a chain's native coin —
// the same literal transactions rows use for NATIVE transfer_type.
const NativeAssetV1_1 = "native"

// v1.1 deployment-pinned price table (proposal §4.2). Prices are policy
// constants, not market data — a price change is a NEW policy version.
// Keys are "<network>:<asset>" where network is the canonical
// blockchain_networks.code and asset is "native" or the lowercased
// 0x contract address.
var pinnedPricesV1_1 = map[string]AssetPriceV1_1{
	// Ethereum mainnet.
	"ethereum:native": {MicroUSD: 3_000_000_000, Decimals: 18}, // ETH $3,000
	"ethereum:0xdac17f958d2ee523a2206206994597c13d831ec7": {MicroUSD: 1_000_000, Decimals: 6},  // USDT
	"ethereum:0xa0b86991c6218b36c1d19f4a2e9eb0ce3606eb48": {MicroUSD: 1_000_000, Decimals: 6},  // USDC
	"ethereum:0x6b175474e89094c44da98b954eedeac495271d0f": {MicroUSD: 1_000_000, Decimals: 18}, // DAI
	// BNB Smart Chain.
	"bsc:native": {MicroUSD: 600_000_000, Decimals: 18}, // BNB $600
	"bsc:0x55d398326f99059ff775485246999027b3197955": {MicroUSD: 1_000_000, Decimals: 18}, // USDT
	"bsc:0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d": {MicroUSD: 1_000_000, Decimals: 18}, // USDC
	"bsc:0x1af3f329e8be154074d8769d1ffa4ee058b1dbc3": {MicroUSD: 1_000_000, Decimals: 18}, // DAI
}

// PriceTableV1_1 is the analyzer's pricing seam — the pinned table is
// the production implementation; tests may substitute a fake.
type PriceTableV1_1 interface {
	IsMeaningful(network, asset, amountRaw string) (meaningful, priced bool, err error)
}

type pinnedTableV1_1 struct{}

// DefaultPriceTableV1_1 returns the pinned price table.
func DefaultPriceTableV1_1() PriceTableV1_1 { return pinnedTableV1_1{} }

func (pinnedTableV1_1) IsMeaningful(network, asset, amountRaw string) (bool, bool, error) {
	return IsMeaningfulV1_1(network, asset, amountRaw)
}

// PriceForV1_1 resolves the pinned price for a network+asset pair.
// asset is normalized (lowercased); "native" or empty asset resolves to
// the chain coin — an empty asset on a NATIVE transfer row is the same
// coin. ok=false marks unpriced assets (evidence-only, never an error).
func PriceForV1_1(network, asset string) (AssetPriceV1_1, bool) {
	a := strings.ToLower(strings.TrimSpace(asset))
	if a == "" {
		a = NativeAssetV1_1
	}
	if p, ok := overlayPricesV1_1[network+":"+a]; ok {
		return p, true
	}
	p, ok := pinnedPricesV1_1[network+":"+a]
	return p, ok
}

// IsMeaningfulV1_1 reports whether amount_raw clears the $100 materiality
// floor under the pinned table. Math is pure big.Int:
//
//	value_micro_usd = amount_raw * micro_usd / 10^decimals
//
// Comparison is exact — a $100.00 transfer is meaningful, $99.99 is not.
// Unpriced assets return (false, false, nil): evidence-only, never an
// error. Malformed amount_raw returns an error — malformed data must
// never pass as meaningful.
func IsMeaningfulV1_1(network, asset, amountRaw string) (meaningful, priced bool, err error) {
	p, ok := PriceForV1_1(network, asset)
	if !ok {
		return false, false, nil
	}
	amt, ok := new(big.Int).SetString(strings.TrimSpace(amountRaw), 10)
	if !ok {
		return false, true, fmt.Errorf("malformed amount_raw %q", amountRaw)
	}
	value := new(big.Int).Mul(amt, big.NewInt(p.MicroUSD))
	value.Quo(value, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p.Decimals)), nil))
	floor := big.NewInt(meaningfulFloorUSDV1_1 * 1_000_000)
	return value.Cmp(floor) >= 0, true, nil
}

// overlayPricesV1_1 is the deployment SCORING_ASSET_PRICES overlay —
// kept separate from the pinned table so an overlay can never corrupt
// policy constants and is reset by applying nil.
var overlayPricesV1_1 map[string]AssetPriceV1_1

// pricingKeyRe validates "<network>:<asset>" overlay key SHAPE — the
// network-code membership check is ValidateScoringAssetPriceNetworks,
// run at wiring against the resolver's active codes (A14).
var pricingKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}:(native|0x[0-9a-fA-F]{40})$`)

// ParseScoringAssetPrices parses the SCORING_ASSET_PRICES deployment
// overlay — a JSON object of "<net>:<asset|native>" →
// {"micro_usd":N,"decimals":D}. Fail-closed validation (phase-02):
// malformed JSON, unknown network, malformed key, negative micro_usd,
// and decimals >30 or <0 are all startup errors. An empty string is a
// valid no-overlay.
func ParseScoringAssetPrices(raw string) (map[string]AssetPriceV1_1, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var m map[string]AssetPriceV1_1
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("SCORING_ASSET_PRICES: invalid JSON: %w", err)
	}
	out := make(map[string]AssetPriceV1_1, len(m))
	for k, v := range m {
		if !pricingKeyRe.MatchString(k) {
			return nil, fmt.Errorf("SCORING_ASSET_PRICES: invalid key %q "+
				"(want \"<ethereum|bsc>:<native|0x+40hex>\")", k)
		}
		if v.MicroUSD <= 0 {
			return nil, fmt.Errorf("SCORING_ASSET_PRICES: %s micro_usd must be > 0", k)
		}
		if v.Decimals < 0 || v.Decimals > 30 {
			return nil, fmt.Errorf("SCORING_ASSET_PRICES: %s decimals must be 0..30", k)
		}
		out[strings.ToLower(k)] = v
	}
	return out, nil
}

// ValidateScoringAssetPriceNetworks fails closed when an overlay key
// names a network the resolver does not serve (A14). Call at wiring,
// where active network codes exist — never inside Parse (config has
// no resolver).
func ValidateScoringAssetPriceNetworks(overlay map[string]AssetPriceV1_1,
	activeNets map[string]bool) error {
	for k := range overlay {
		net, _, _ := strings.Cut(k, ":")
		if !activeNets[net] {
			return fmt.Errorf(
				"SCORING_ASSET_PRICES: %s is not an active network", net)
		}
	}
	return nil
}

// ApplyScoringAssetPrices installs the validated deployment overlay —
// a separate map consulted before the pinned table, so policy constants
// are never mutated. Call once at boot; nil resets.
func ApplyScoringAssetPrices(overlay map[string]AssetPriceV1_1) {
	overlayPricesV1_1 = overlay
}
