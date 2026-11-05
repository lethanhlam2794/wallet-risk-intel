// Package wallet owns wallet identity: address normalization, validation,
// and persistence of wallet rows.
package wallet

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"checkwallet/internal/network"
)

var (
	// ErrInvalidAddress is returned when input fails chain-specific validation.
	ErrInvalidAddress = errors.New("invalid address")
	// ErrInvalidNetwork is returned when the network code is not supported.
	ErrInvalidNetwork = errors.New("invalid network")
)

var evmAddressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// NormalizeEVM validates a 20-byte EVM address and returns the canonical
// indexing form: lowercase, 0x-prefixed. The original representation is
// preserved by callers (spec §7.3). Validation is strict — the 0x prefix
// may be omitted, but surrounding whitespace or any other deviation is
// rejected; callers own input hygiene.
func NormalizeEVM(input string) (string, error) {
	if input == "" {
		return "", ErrInvalidAddress
	}
	s := input
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		s = "0x" + s
	}
	if !evmAddressRe.MatchString(s) {
		return "", fmt.Errorf("%w: %q is not a 20-byte hex address", ErrInvalidAddress, input)
	}
	return "0x" + strings.ToLower(s[2:]), nil
}

// IsEVMAddress reports whether input is a syntactically valid EVM address
// (with or without the 0x prefix) without normalizing.
func IsEVMAddress(input string) bool {
	if !strings.HasPrefix(input, "0x") && !strings.HasPrefix(input, "0X") {
		input = "0x" + input
	}
	return evmAddressRe.MatchString(input)
}

// ResolveAndNormalize is the shared input-validation helper for every
// {network}/{address} endpoint: resolve the network code, then normalize
// the address under EVM rules. Both failures map to HTTP 400 with distinct
// stable codes.
func ResolveAndNormalize(resolver *network.Resolver, networkCode, rawAddress string) (network.Network, string, error) {
	net, ok := resolver.Resolve(networkCode)
	if !ok {
		return network.Network{}, "", ErrInvalidNetwork
	}
	normalized, err := NormalizeEVM(rawAddress)
	if err != nil {
		return network.Network{}, "", err
	}
	return net, normalized, nil
}
