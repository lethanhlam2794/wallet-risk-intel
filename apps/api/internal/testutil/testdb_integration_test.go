package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercises RequireTestPool end-to-end against the real dedicated test DB:
// harness connects, applies the manifest, truncates mutable domain tables,
// and keeps the static reference seeds (networks, data_sources) that
// migrations own.
func TestRequireTestPool_SeededSchema(t *testing.T) {
	pool := RequireTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var networks []string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT array_agg(code ORDER BY code) FROM blockchain_networks").Scan(&networks))
	assert.Equal(t, []string{"bsc", "ethereum", "solana", "tron"}, networks,
		"all migration-seeded networks must survive truncation")

	// solana/tron are registered INACTIVE (migration 000026 groundwork) —
	// the resolver keeps rejecting them until a §15-reviewed enablement.
	var inactive int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM blockchain_networks WHERE code IN ('solana','tron') AND is_active = FALSE").Scan(&inactive))
	assert.Equal(t, 2, inactive, "solana/tron seeds must stay inactive")

	var sources []string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT array_agg(code ORDER BY code) FROM data_sources").Scan(&sources))
	assert.Equal(t,
		[]string{"alchemy", "alchemy_bsc", "alchemy_eth", "ankr", "bitquery", "blockscout", "blockscout_pro", "chainabuse", "chainbase", "chainscan",
			"covalent", "goplus", "moralis", "nodereal", "ofac_sdn", "oklink",
			"publicaml", "stablecoin_freeze", "tatum", "tokenview", "wallet_attribution"},
		sources, "all migration-seeded data sources must survive truncation")
}
