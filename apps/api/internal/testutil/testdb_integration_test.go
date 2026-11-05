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

	var networks int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM blockchain_networks").Scan(&networks))
	assert.Equal(t, 2, networks, "ethereum + bsc seeds must survive truncation")

	var sources []string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT array_agg(code ORDER BY code) FROM data_sources").Scan(&sources))
	assert.Equal(t,
		[]string{"alchemy", "ankr", "bitquery", "blockscout", "chainabuse", "chainbase", "chainscan",
			"covalent", "goplus", "moralis", "nodereal", "ofac_sdn", "oklink",
			"publicaml", "tatum", "tokenview", "wallet_attribution"},
		sources, "all migration-seeded data sources must survive truncation")
}
