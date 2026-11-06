package wallet

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// TRON base58check layout: 0x41 version byte + 20-byte payload + 4-byte
// checksum = 25 decoded bytes.
const (
	tronVersionByte = 0x41
	tronPayloadLen  = 21 // version + 20-byte payload
	tronChecksumLen = 4
	tronDecodedLen  = tronPayloadLen + tronChecksumLen
)

// NormalizeTron validates a TRON mainnet address: base58check whose
// decoded form is 0x41 + 20-byte payload + the first 4 bytes of
// SHA256(SHA256(payload)). The normalized form is the input verbatim —
// base58 is case-sensitive ('T'-prefixed by construction).
func NormalizeTron(input string) (string, error) {
	decoded, err := decodeBase58(input)
	if err != nil {
		return "", err
	}
	if len(decoded) != tronDecodedLen {
		return "", fmt.Errorf("%w: %q is not a tron address", ErrInvalidAddress, input)
	}
	payload := decoded[:tronPayloadLen]
	if payload[0] != tronVersionByte {
		return "", fmt.Errorf("%w: %q has wrong tron version byte", ErrInvalidAddress, input)
	}
	sum := sha256.Sum256(payload)
	sum = sha256.Sum256(sum[:])
	if !bytes.Equal(decoded[tronPayloadLen:], sum[:tronChecksumLen]) {
		return "", fmt.Errorf("%w: %q failed tron checksum", ErrInvalidAddress, input)
	}
	return input, nil
}
