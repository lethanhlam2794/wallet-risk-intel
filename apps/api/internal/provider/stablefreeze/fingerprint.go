package stablefreeze

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint is the wallet_labels.fingerprint twin for this source —
// identical formula to walletattribution.Fingerprint but namespaced
// "stablecoin_freeze|" so freeze labels can never collide with
// wallet-attribution rows (a different namespace would let the
// full-replace labels pipeline revoke freeze rows by absence, and vice
// versa). Never change the prefix: fingerprint stability is provenance.
func Fingerprint(networkCode, normalizedAddress, categoryCode, label string) string {
	sum := sha256.Sum256([]byte("stablecoin_freeze|" + networkCode + "|" +
		normalizedAddress + "|" + categoryCode + "|" + label))
	return hex.EncodeToString(sum[:])
}
