package wallet_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

const testDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_wallet_test?sslmode=disable"

func lookupNet(t *testing.T, pool *pgxpool.Pool, code string) network.Network {
	t.Helper()
	var net network.Network
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id, code, name, chain_reference, namespace FROM blockchain_networks
		 WHERE code=$1`, code).Scan(
		&net.ID, &net.Code, &net.Name, &net.ChainReference, &net.Namespace))
	return net
}

// GetOrCreate stamps address_type from the resolved network's address
// format — "evm" for eip155, "tron" for tron — and fails closed on an
// unknown namespace (a registry-invariant violation, never an empty or
// defaulted stamp). EVM-stamped values stay byte-identical.
func TestGetOrCreate_AddressTypeByFormat(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := wallet.NewService(db.New(pool))
	ctx := context.Background()

	eth := lookupNet(t, pool, "ethereum")
	trx := lookupNet(t, pool, "tron")

	const evmAddr = "0x0000000000000000000000000000000000000abc"
	const tronAddr = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

	w, err := svc.GetOrCreate(ctx, eth, evmAddr, evmAddr)
	require.NoError(t, err)
	assert.Equal(t, "evm", w.AddressType)

	// Repeat lookup is idempotent and keeps the stamped type verbatim.
	w2, err := svc.GetOrCreate(ctx, eth, evmAddr, evmAddr)
	require.NoError(t, err)
	assert.Equal(t, w.ID, w2.ID)
	assert.Equal(t, "evm", w2.AddressType)

	w3, err := svc.GetOrCreate(ctx, trx, tronAddr, tronAddr)
	require.NoError(t, err)
	assert.Equal(t, "tron", w3.AddressType)
}

// Blind saturation: address_type is never stamped outside the known
// format set — randomized namespaces either stamp their format or fail
// closed with no row written.
func TestBlind_AddressTypeOnlyKnownFormats(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := wallet.NewService(db.New(pool))
	ctx := context.Background()
	trx := lookupNet(t, pool, "tron")

	want := map[string]string{"eip155": "evm", "solana": "solana", "tron": "tron"}
	namespaces := []string{
		"eip155", "solana", "tron",
		"EIP155", "Tron", "eip155 ", "", "bip122", "cosmos", "eip1551",
		"eip155\x00", "tron_trx", "solan",
	}
	for i, ns := range namespaces {
		net := trx
		net.Namespace = ns
		addr := "TBlind" + fmt.Sprintf("%02d", i) + strings.Repeat("x", 8)
		w, err := svc.GetOrCreate(ctx, net, addr, addr)
		if exp, ok := want[ns]; ok {
			require.NoError(t, err, "ns=%q", ns)
			assert.Equal(t, exp, w.AddressType, "ns=%q", ns)
			assert.NotEmpty(t, w.AddressType)
			continue
		}
		assert.ErrorIs(t, err, wallet.ErrUnknownAddressFormat, "ns=%q", ns)
		var n int64
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM wallets WHERE network_id=$1 AND normalized_address=$2`,
			trx.ID, addr).Scan(&n))
		assert.Zero(t, n, "ns=%q must not write", ns)
	}
}

// An unknown-namespace network must never write a wallet row — the typed
// error fires before any insert.
func TestGetOrCreate_UnknownFormatFailsClosed(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := wallet.NewService(db.New(pool))
	ctx := context.Background()

	trx := lookupNet(t, pool, "tron")
	trx.Namespace = "bip122" // no AddressFormatForNamespace mapping → ""

	const addr = "TWGd9idELBV3is6rrtC5PQUhudiJYeCr7E"
	_, err := svc.GetOrCreate(ctx, trx, addr, addr)
	assert.ErrorIs(t, err, wallet.ErrUnknownAddressFormat)

	var n int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM wallets WHERE network_id=$1 AND normalized_address=$2`,
		trx.ID, addr).Scan(&n))
	assert.Zero(t, n, "fail-closed path must not write a wallet row")
}
