package providerconfig

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Obviously fake 32-byte KEKs for tests — never a real key.
var (
	testKEKB64 = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	testKEKHex = "3031323334353637383961626364656630313233343536373839616263646566"
)

var encShape = regexp.MustCompile(`^enc:v1:[A-Za-z0-9_-]+:[A-Za-z0-9_-]+$`)

func TestNewKeystore_ParsesBase64AndHex(t *testing.T) {
	for _, raw := range []string{testKEKB64, testKEKHex} {
		k, err := NewKeystore(raw)
		require.NoError(t, err)
		require.NotNil(t, k)
	}
	_, err := NewKeystore("c2hvcnQ=") // valid b64, wrong length
	require.Error(t, err)
	_, err = NewKeystore("not-a-key!!!")
	require.Error(t, err)
}

func TestNewKeystore_EmptyIsPlaintextMode(t *testing.T) {
	k, err := NewKeystore("")
	require.NoError(t, err)
	enc, err := k.Encrypt("my-secret")
	require.NoError(t, err)
	assert.Equal(t, "my-secret", enc, "no KEK ⇒ honest plaintext storage")
}

func TestKeystore_EncryptDecryptRoundTrip(t *testing.T) {
	k, err := NewKeystore(testKEKB64)
	require.NoError(t, err)

	enc, err := k.Encrypt("sk-test-key-12345")
	require.NoError(t, err)
	assert.True(t, encShape.MatchString(enc), "wire shape enc:v1:<b64 nonce>:<b64 ct>: %q", enc)
	assert.NotContains(t, enc, "sk-test-key-12345")

	out, err := k.Decrypt(enc)
	require.NoError(t, err)
	assert.Equal(t, "sk-test-key-12345", out)
}

func TestKeystore_PlaintextPassesThroughOnRead(t *testing.T) {
	k, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	out, err := k.Decrypt("legacy-plaintext-key")
	require.NoError(t, err)
	assert.Equal(t, "legacy-plaintext-key", out)
}

func TestKeystore_TamperedCiphertextErrors(t *testing.T) {
	k, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	enc, err := k.Encrypt("sk-test-key-12345")
	require.NoError(t, err)

	parts := strings.SplitN(enc, ":", 4)
	require.Len(t, parts, 4)
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	require.NoError(t, err)
	ct[0] ^= 0xff
	tampered := parts[0] + ":" + parts[1] + ":" + parts[2] + ":" + base64.RawURLEncoding.EncodeToString(ct)

	_, err = k.Decrypt(tampered)
	require.Error(t, err, "tampered ciphertext must error, never return garbage as plaintext")
}

func TestKeystore_EncPrefixedMalformedIsFatal(t *testing.T) {
	k, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	// Plaintext that happens to carry the enc: prefix but not the full
	// enc:v1:<nonce>:<ct> shape ⇒ boot-fatal misconfiguration.
	for _, v := range []string{"enc:abc", "enc:v1:", "enc:v1:nonce", "enc:v2:a:b"} {
		_, err := k.Decrypt(v)
		require.Error(t, err, "malformed enc value %q must not pass as plaintext", v)
	}
}

func TestKeystore_EncryptedWithoutKEKIsFatal(t *testing.T) {
	k, err := NewKeystore(testKEKB64)
	require.NoError(t, err)
	enc, err := k.Encrypt("sk-test-key-12345")
	require.NoError(t, err)

	noKey, err := NewKeystore("")
	require.NoError(t, err)
	_, err = noKey.Decrypt(enc)
	require.Error(t, err, "enc:v1 value without KEK is undecryptable — fail closed")
}
