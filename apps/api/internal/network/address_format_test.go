package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddressFormatForNamespace(t *testing.T) {
	cases := []struct {
		ns   string
		want AddressFormat
	}{
		{"eip155", AddressFormatEVM},
		{"solana", AddressFormatSolana},
		{"tron", AddressFormatTron},
		// Unknown or absent namespaces fail closed to "".
		{"", ""},
		{"bip122", ""},
		{"cosmos", ""},
		{"EIP155", ""}, // namespaces are canonical lowercase; no coercion
	}
	for _, c := range cases {
		assert.Equal(t, c.want, AddressFormatForNamespace(c.ns), "ns=%q", c.ns)
	}
}

func TestNetwork_AddressFormat(t *testing.T) {
	assert.Equal(t, AddressFormatEVM,
		Network{Namespace: "eip155", ChainReference: "1", Code: "ethereum"}.AddressFormat())
	assert.Equal(t, AddressFormatSolana,
		Network{Namespace: "solana", ChainReference: "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", Code: "solana"}.AddressFormat())
	assert.Equal(t, AddressFormatTron,
		Network{Namespace: "tron", ChainReference: "0x2b6653dc", Code: "tron"}.AddressFormat())
	// Zero-value Network has no format — callers must fail closed.
	assert.Equal(t, AddressFormat(""), Network{}.AddressFormat())
}
