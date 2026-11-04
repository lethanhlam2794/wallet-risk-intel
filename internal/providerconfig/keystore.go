// Package providerconfig resolves per-provider operational config from
// provider_configs rows over env (DB wins when set, env is the fallback)
// for the tx-history adapters. Resolution errors are boot-fatal and name
// provider+field; an absent row reproduces the env-only path exactly.
package providerconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// Wire envelope for stored api_key values: enc:v1:<b64 nonce>:<b64 ct>.
// Values without an enc: prefix are honest plaintext — the two coexist
// deliberately (KEK unset ⇒ plaintext storage).
const (
	encPrefix   = "enc:"
	encV1Prefix = "enc:v1:"
)

// Keystore encrypts api_key values on write and decrypts on read. A nil
// key means plaintext mode (PROVIDER_CONFIG_KEK unset) — documented
// behaviour, never silent degradation of stored ciphertext.
type Keystore struct {
	key []byte
}

// NewKeystore parses PROVIDER_CONFIG_KEK: a 32-byte key in base64 or hex.
// Empty means plaintext mode. Anything else is a misconfiguration — fatal.
func NewKeystore(raw string) (*Keystore, error) {
	if raw == "" {
		return &Keystore{}, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return &Keystore{key: b}, nil
	}
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return &Keystore{key: b}, nil
	}
	return nil, fmt.Errorf("providerconfig: PROVIDER_CONFIG_KEK must be a 32-byte base64 or hex key")
}

// Enabled reports whether writes should encrypt (KEK set).
func (k *Keystore) Enabled() bool { return k != nil && k.key != nil }

// Encrypt produces the enc:v1 envelope. Plaintext mode returns the value
// unchanged — encrypt-on-write iff KEK set.
func (k *Keystore) Encrypt(plaintext string) (string, error) {
	if !k.Enabled() {
		return plaintext, nil
	}
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("providerconfig: nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return encV1Prefix +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(ct), nil
}

// Decrypt resolves a stored value: no enc: prefix ⇒ plaintext verbatim;
// the full enc:v1:<b64>:<b64> shape ⇒ AES-256-GCM open. An enc: prefix on
// anything else is fail-closed — a value claiming to be encoded that we
// cannot decode is never passed to an adapter as if it were plaintext.
func (k *Keystore) Decrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	parts := strings.SplitN(stored, ":", 4)
	if len(parts) != 4 || parts[1] != "v1" || parts[2] == "" || parts[3] == "" {
		return "", fmt.Errorf("malformed enc value (expected %s<nonce>:<ct>)", encV1Prefix)
	}
	if !k.Enabled() {
		return "", fmt.Errorf("enc:v1 value requires PROVIDER_CONFIG_KEK to decrypt")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("malformed enc:v1 nonce: %w", err)
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("malformed enc:v1 ciphertext: %w", err)
	}
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", fmt.Errorf("malformed enc:v1 nonce size")
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed: %w", err)
	}
	return string(pt), nil
}

func (k *Keystore) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, fmt.Errorf("providerconfig: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("providerconfig: gcm: %w", err)
	}
	return gcm, nil
}
