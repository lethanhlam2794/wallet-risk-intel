package wallet

import "fmt"

// NormalizeSolana validates a Solana address: a base58 string 32–44 chars
// long decoding to exactly 32 bytes (an ed25519 public key). The
// normalized form is the input verbatim — base58 is case-sensitive and
// Solana defines no canonical casing, so any case folding would corrupt
// the address.
func NormalizeSolana(input string) (string, error) {
	if len(input) < 32 || len(input) > 44 {
		return "", fmt.Errorf("%w: %q is not a solana address", ErrInvalidAddress, input)
	}
	decoded, err := decodeBase58(input)
	if err != nil {
		return "", err
	}
	if len(decoded) != 32 {
		return "", fmt.Errorf("%w: %q does not decode to 32 bytes", ErrInvalidAddress, input)
	}
	return input, nil
}
