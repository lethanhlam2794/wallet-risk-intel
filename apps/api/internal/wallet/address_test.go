package wallet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEVM_Valid(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// EIP-55 checksum form normalizes to lowercase 0x-prefixed.
		{"0x098B716B8Aaf21512996dC57EB0615e2383E2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		{"0x098b716b8aaf21512996dc57eb0615e2383e2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		{"0x098B716B8AAF21512996DC57EB0615E2383E2F96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		// Missing 0x prefix is accepted and normalized to the canonical form.
		{"098b716b8aaf21512996dc57eb0615e2383e2f96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		{"098B716B8AAF21512996DC57EB0615E2383E2F96", "0x098b716b8aaf21512996dc57eb0615e2383e2f96"},
		// All-zero address is valid.
		{"0x0000000000000000000000000000000000000000", "0x0000000000000000000000000000000000000000"},
	}
	for _, c := range cases {
		got, err := NormalizeEVM(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
}

func TestNormalizeEVM_Invalid(t *testing.T) {
	cases := []string{
		"",
		"0x",
		"0x0",
		"0x098b716b8aaf21512996dc57eb0615e2383e2f9",   // 19 bytes
		"0x098b716b8aaf21512996dc57eb0615e2383e2f960", // 21 bytes
		"0xZZ8b716b8aaf21512996dc57eb0615e2383e2f96",  // non-hex
		"not-an-address",
		"0x098b716b8aaf21512996dc57eb0615e2383e2f96extra",
		" 0x098b716b8aaf21512996dc57eb0615e2383e2f96", // leading space
	}
	for _, in := range cases {
		_, err := NormalizeEVM(in)
		assert.Error(t, err, in)
	}
}
