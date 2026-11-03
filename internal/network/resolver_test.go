package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolver_Resolve(t *testing.T) {
	r := NewResolver([]Network{
		{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum", Name: "Ethereum Mainnet"},
		{ID: 2, Namespace: "eip155", ChainReference: "56", Code: "bsc", Name: "BNB Smart Chain"},
	})

	eth, ok := r.Resolve("ethereum")
	require.True(t, ok)
	assert.Equal(t, int64(1), eth.ID)
	assert.Equal(t, "eip155:1", eth.CAIP2())

	bsc, ok := r.Resolve("bsc")
	require.True(t, ok)
	assert.Equal(t, int64(2), bsc.ID)
	assert.Equal(t, "eip155:56", bsc.CAIP2())
	assert.NotEqual(t, eth.ID, bsc.ID)

	// Code matching is case-insensitive (canonicalize on read, P-003).
	ethUpper, ok := r.Resolve("ETHEREUM")
	require.True(t, ok)
	assert.Equal(t, eth.ID, ethUpper.ID)

	_, ok = r.Resolve("solana")
	assert.False(t, ok)

	_, ok = r.Resolve("")
	assert.False(t, ok)
}

func TestResolver_List(t *testing.T) {
	r := NewResolver([]Network{
		{ID: 1, Code: "ethereum", Name: "Ethereum Mainnet", Namespace: "eip155", ChainReference: "1"},
		{ID: 2, Code: "bsc", Name: "BNB Smart Chain", Namespace: "eip155", ChainReference: "56"},
	})
	assert.Len(t, r.List(), 2)
}
