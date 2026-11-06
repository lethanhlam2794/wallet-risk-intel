package wallet

import (
	"crypto/sha256"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
)

// encodeBase58 is the test-side encoder feeding the validators' input.
// Production code only decodes; encoding here keeps the production surface
// minimal while letting property tests round-trip arbitrary byte arrays.
func encodeBase58(b []byte) string {
	n := new(big.Int).SetBytes(b)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, big.NewInt(58), mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < len(b) && b[i] == 0; i++ {
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// tronAddress builds a base58check TRON address from a 20-byte payload.
func tronAddress(payload []byte) string {
	body := append([]byte{0x41}, payload...)
	sum := sha256.Sum256(body)
	sum = sha256.Sum256(sum[:])
	return encodeBase58(append(body, sum[:4]...))
}

// Well-known mainnet vectors (independent oracle — not generated here).
var solanaValid = []string{
	"11111111111111111111111111111111",             // System Program
	"So11111111111111111111111111111111111111112",  // Wrapped SOL mint
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", // USDC mint
	"Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", // USDT mint
	"TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",  // SPL Token program
	"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM", // Binance hot wallet
}

var tronValid = []string{
	"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", // USDT (TRC-20) contract
	"TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8", // USDC (TRC-20) contract
	tronAddress([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13}),
}

func TestNormalizeSolana_Valid(t *testing.T) {
	for _, in := range solanaValid {
		got, err := NormalizeSolana(in)
		require.NoError(t, err, in)
		// Normalized form is verbatim — base58 is case-sensitive and any
		// case folding would corrupt the address.
		assert.Equal(t, in, got, in)
	}
}

func TestNormalizeSolana_Invalid(t *testing.T) {
	cases := []string{
		"",
		"abc",                               // too short (<32 chars)
		strings.Repeat("1", 31),             // 31 chars
		strings.Repeat("1", 45),             // too long (>44 chars)
		"0o1111111111111111111111111111111", // '0' and 'O' not in alphabet
		"Illlllllllllllllllllllllllllllll",  // 'I'/'l' not in alphabet
		" So11111111111111111111111111111111111111112",           // leading space
		"So11111111111111111111111111111111111111112 ",           // trailing space
		"So11 11111111111111111111111111111111111112",            // inner space
		"So1111111111111111111111111111111111111111\t",           // tab
		encodeBase58(make([]byte, 33)),                           // 33 decoded bytes
		encodeBase58(append(make([]byte, 30), 0x01, 0x02, 0x03)), // 33 bytes
	}
	for _, in := range cases {
		_, err := NormalizeSolana(in)
		assert.ErrorIs(t, err, ErrInvalidAddress, "input %q must be rejected", in)
	}
	// A 31-byte payload inside the char bounds still fails decoded-length
	// (high byte set → encodes to ~43 chars, but only 31 decoded bytes).
	short31 := encodeBase58(append([]byte{0xff}, make([]byte, 30)...))
	require.GreaterOrEqual(t, len(short31), 32)
	_, err := NormalizeSolana(short31)
	assert.ErrorIs(t, err, ErrInvalidAddress, "31-byte decode must be rejected")
}

func TestNormalizeSolana_CasePreserved(t *testing.T) {
	// An all-lowercased form of a mixed-case address must never be produced:
	// the output is byte-identical to the input, whatever its casing.
	in := "So11111111111111111111111111111111111111112"
	got, err := NormalizeSolana(in)
	require.NoError(t, err)
	assert.Equal(t, in, got)
	assert.NotEqual(t, strings.ToLower(in), got)
}

func TestNormalizeTron_Valid(t *testing.T) {
	for _, in := range tronValid {
		got, err := NormalizeTron(in)
		require.NoError(t, err, in)
		assert.Equal(t, in, got, in)
	}
}

func TestNormalizeTron_Invalid(t *testing.T) {
	valid := tronValid[0]
	// Corrupt the checksum: flip the last char to a different alphabet char.
	last := valid[len(valid)-1]
	replacement := byte('2')
	if last == '2' {
		replacement = '3'
	}
	badChecksum := valid[:len(valid)-1] + string(replacement)

	// Wrong version byte with a *valid* checksum for that payload.
	v42 := append([]byte{0x42}, make([]byte, 20)...)
	sum42 := sha256.Sum256(v42)
	sum42 = sha256.Sum256(sum42[:])
	badVersion := encodeBase58(append(v42, sum42[:4]...))

	cases := []string{
		"",
		badChecksum,                    // corrupted checksum char
		badVersion,                     // 0x42 version + valid checksum
		encodeBase58(make([]byte, 24)), // 24 decoded bytes
		encodeBase58(make([]byte, 26)), // 26 decoded bytes
		strings.ToLower(valid),         // 't' prefix — case-sensitive
		" " + valid,                    // leading space
		valid + " ",                    // trailing space
		valid[:len(valid)-1],           // truncated checksum
		"0" + valid[1:],                // non-alphabet char
		solanaValid[1],                 // solana address is not tron
	}
	for _, in := range cases {
		_, err := NormalizeTron(in)
		assert.ErrorIs(t, err, ErrInvalidAddress, "input %q must be rejected", in)
	}
}

func TestNormalize_Dispatch(t *testing.T) {
	solNet := network.Network{ID: 3, Namespace: "solana", ChainReference: "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", Code: "solana", Name: "Solana Mainnet"}
	tronNet := network.Network{ID: 4, Namespace: "tron", ChainReference: "0x2b6653dc", Code: "tron", Name: "TRON Mainnet"}
	evmNet := network.Network{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum", Name: "Ethereum Mainnet"}

	got, err := Normalize(evmNet, "0x098B716B8Aaf21512996dC57EB0615e2383E2f96")
	require.NoError(t, err)
	assert.Equal(t, "0x098b716b8aaf21512996dc57eb0615e2383e2f96", got)

	got, err = Normalize(solNet, solanaValid[1])
	require.NoError(t, err)
	assert.Equal(t, solanaValid[1], got) // verbatim — NOT lowercased

	got, err = Normalize(tronNet, tronValid[0])
	require.NoError(t, err)
	assert.Equal(t, tronValid[0], got)

	// Wrong-format input for the resolved network is rejected.
	_, err = Normalize(solNet, "0x098b716b8aaf21512996dc57eb0615e2383e2f96")
	assert.ErrorIs(t, err, ErrInvalidAddress)
	_, err = Normalize(evmNet, solanaValid[1])
	assert.ErrorIs(t, err, ErrInvalidAddress)
	_, err = Normalize(tronNet, solanaValid[1])
	assert.ErrorIs(t, err, ErrInvalidAddress)

	// Unknown/empty format fails closed.
	_, err = Normalize(network.Network{Namespace: "bip122", Code: "bitcoin"}, "1A1zP1")
	assert.ErrorIs(t, err, ErrInvalidAddress)
	_, err = Normalize(network.Network{}, "anything")
	assert.ErrorIs(t, err, ErrInvalidAddress)
}

func TestResolveAndNormalize_NonEVMRegistered(t *testing.T) {
	// Once a solana/tron row is active the dispatch path normalizes under
	// that chain's rules — groundwork proves the wiring, not the rollout.
	resolver := network.NewResolver([]network.Network{
		{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum"},
		{ID: 3, Namespace: "solana", ChainReference: "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp", Code: "solana"},
		{ID: 4, Namespace: "tron", ChainReference: "0x2b6653dc", Code: "tron"},
	})

	net, got, err := ResolveAndNormalize(resolver, "solana", solanaValid[1])
	require.NoError(t, err)
	assert.Equal(t, "solana", net.Code)
	assert.Equal(t, solanaValid[1], got)

	net, got, err = ResolveAndNormalize(resolver, "tron", tronValid[0])
	require.NoError(t, err)
	assert.Equal(t, "tron", net.Code)
	assert.Equal(t, tronValid[0], got)

	_, _, err = ResolveAndNormalize(resolver, "solana", "not-an-address")
	assert.ErrorIs(t, err, ErrInvalidAddress)
	_, _, err = ResolveAndNormalize(resolver, "solana", tronValid[0])
	assert.ErrorIs(t, err, ErrInvalidAddress)
}
