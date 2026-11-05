package wallet

import (
	"math/rand"
	"strings"
	"testing"
)

// Property-based saturation: inputs are generated randomly at run time, so
// the implementation cannot be tuned to the fixture (blind test discipline).

const propIterations = 5000

var hexChars = []byte("0123456789abcdefABCDEF")

func randHexAddr(r *rand.Rand) string {
	b := make([]byte, 40)
	for i := range b {
		b[i] = hexChars[r.Intn(len(hexChars))]
	}
	return "0x" + string(b)
}

// Any random 20-byte hex address is valid, canonicalizes to lowercase, and
// normalization is idempotent.
func TestNormalizeEVM_RandomValidProperties(t *testing.T) {
	r := rand.New(rand.NewSource(20261103))
	for i := 0; i < propIterations; i++ {
		in := randHexAddr(r)
		got, err := NormalizeEVM(in)
		if err != nil {
			t.Fatalf("iter %d: valid input %q rejected: %v", i, in, err)
		}
		if got != strings.ToLower(got) || !strings.HasPrefix(got, "0x") || len(got) != 42 {
			t.Fatalf("iter %d: output %q not canonical lowercase 0x-form", i, got)
		}
		if got != "0x"+strings.ToLower(in[2:]) {
			t.Fatalf("iter %d: output %q lost hex content of %q", i, got, in)
		}
		again, err := NormalizeEVM(got)
		if err != nil || again != got {
			t.Fatalf("iter %d: not idempotent: %q -> %q -> %q, err=%v", i, in, got, again, err)
		}
		// Missing prefix normalizes to the same canonical form.
		np, err := NormalizeEVM(in[2:])
		if err != nil || np != got {
			t.Fatalf("iter %d: unprefixed %q -> %q want %q, err=%v", i, in[2:], np, got, err)
		}
	}
}

// Random case shuffles of the same address must canonicalize identically.
func TestNormalizeEVM_CaseInvariance(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	base := randHexAddr(r)
	want, err := NormalizeEVM(base)
	if err != nil {
		t.Fatalf("base rejected: %v", err)
	}
	for i := 0; i < propIterations; i++ {
		b := []byte(base)
		for j := 2; j < len(b); j++ {
			c := b[j]
			if c >= 'a' && c <= 'f' && r.Intn(2) == 0 {
				b[j] = c - 32
			} else if c >= 'A' && c <= 'F' && r.Intn(2) == 0 {
				b[j] = c + 32
			}
		}
		got, err := NormalizeEVM(string(b))
		if err != nil || got != want {
			t.Fatalf("iter %d: case shuffle %q -> %q want %q err=%v", i, string(b), got, want, err)
		}
	}
}

// Randomly corrupted inputs (bad length, non-hex chars, whitespace, double
// prefix) must always be rejected — never silently coerced.
func TestNormalizeEVM_RandomInvalidRejected(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	badChars := []byte("ghijklmnopqrstuvwxyz!@#$%^&*()_-+=[]{}|;:'\",.<>/?`~ \t\n\r")
	for i := 0; i < propIterations; i++ {
		in := randHexAddr(r)
		switch r.Intn(5) {
		case 0: // inject non-hex char at random position
			b := []byte(in)
			pos := 2 + r.Intn(40)
			b[pos] = badChars[r.Intn(len(badChars))]
			in = string(b)
		case 1: // truncate
			in = in[:2+r.Intn(39)]
		case 2: // pad
			in = in + string(hexChars[r.Intn(len(hexChars))])
		case 3: // whitespace anywhere
			pos := r.Intn(len(in) + 1)
			in = in[:pos] + " \t"[r.Intn(2):][:1] + in[pos:]
		case 4: // double prefix
			in = "0x" + in
		}
		if _, err := NormalizeEVM(in); err == nil {
			t.Fatalf("iter %d: corrupted input %q accepted", i, in)
		}
	}
}
