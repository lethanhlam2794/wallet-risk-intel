package apiclient

import (
	"crypto/rand"
	"encoding/base64"
)

// keyPrefix marks generated keys; there is no format gate on inbound
// X-API-Key — bootstrap/legacy keys of any shape keep working.
const keyPrefix = "cw_"

// GenerateKey returns a new API key: "cw_" + base64url(32 crypto/rand
// bytes) — identifiable, high entropy, URL-safe. The plaintext is shown
// exactly once in the POST 201 body; only the SHA-256 hex digest is
// stored (middleware.HashAPIKey).
func GenerateKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}
