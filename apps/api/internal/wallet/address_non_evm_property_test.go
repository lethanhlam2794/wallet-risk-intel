package wallet

import (
	"math/rand"
	"strings"
	"testing"
)

// Property-based saturation for the non-EVM validators, matching
// address_property_test.go conventions (seeded math/rand, 5000 iters).

var base58Chars = []byte(base58Alphabet)
var nonBase58Chars = []byte("0OIl!@#$%^&*()_-+=[]{}|;:'\",.<>/?`~ \t\n\r")

func randBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}

// Any random 32-byte payload base58-encodes to a valid Solana address;
// normalization returns it verbatim and is idempotent.
func TestNormalizeSolana_RandomValidProperties(t *testing.T) {
	r := rand.New(rand.NewSource(20261106))
	for i := 0; i < propIterations; i++ {
		in := encodeBase58(randBytes(r, 32))
		got, err := NormalizeSolana(in)
		if err != nil {
			t.Fatalf("iter %d: valid input %q rejected: %v", i, in, err)
		}
		if got != in {
			t.Fatalf("iter %d: output %q differs from input %q — normalization must be verbatim", i, got, in)
		}
		if got == strings.ToLower(in) && got != in {
			t.Fatalf("iter %d: output %q was lowercased", i, got)
		}
		again, err := NormalizeSolana(got)
		if err != nil || again != got {
			t.Fatalf("iter %d: not idempotent: %q -> %q -> %q, err=%v", i, in, got, again, err)
		}
	}
}

// Random 20-byte payloads under a 0x41 version byte with a real
// double-SHA256 checksum encode to valid TRON addresses, verbatim.
func TestNormalizeTron_RandomValidProperties(t *testing.T) {
	r := rand.New(rand.NewSource(20261106))
	for i := 0; i < propIterations; i++ {
		in := tronAddress(randBytes(r, 20))
		got, err := NormalizeTron(in)
		if err != nil {
			t.Fatalf("iter %d: valid input %q rejected: %v", i, in, err)
		}
		if got != in {
			t.Fatalf("iter %d: output %q differs from input %q — normalization must be verbatim", i, got, in)
		}
		again, err := NormalizeTron(got)
		if err != nil || again != got {
			t.Fatalf("iter %d: not idempotent: %q -> %q, err=%v", i, got, again, err)
		}
	}
}

// Corrupted inputs must never be silently normalized. A corruption that
// still satisfies the format rules (e.g. a case flip that coincidentally
// decodes to 32 bytes) is a *different* valid address — acceptable, but
// only verbatim: normalizers never "repair" casing or padding.
func TestNonEVM_RandomCorruptedNeverSilentlyNormalized(t *testing.T) {
	r := rand.New(rand.NewSource(31337))
	for i := 0; i < propIterations; i++ {
		var in string
		var isSol bool
		if r.Intn(2) == 0 {
			in = encodeBase58(randBytes(r, 32))
			isSol = true
		} else {
			in = tronAddress(randBytes(r, 20))
		}
		orig := in
		switch r.Intn(4) {
		case 0: // inject a non-alphabet char at a random position
			b := []byte(in)
			pos := r.Intn(len(b))
			b[pos] = nonBase58Chars[r.Intn(len(nonBase58Chars))]
			in = string(b)
		case 1: // case-flip random letters
			b := []byte(in)
			for j := range b {
				if r.Intn(4) == 0 {
					if b[j] >= 'a' && b[j] <= 'z' {
						b[j] -= 32
					} else if b[j] >= 'A' && b[j] <= 'Z' {
						b[j] += 32
					}
				}
			}
			in = string(b)
		case 2: // swap a char with a different alphabet char
			b := []byte(in)
			pos := r.Intn(len(b))
			c := base58Chars[r.Intn(len(base58Chars))]
			for c == b[pos] {
				c = base58Chars[r.Intn(len(base58Chars))]
			}
			b[pos] = c
			in = string(b)
		case 3: // whitespace injection
			pos := r.Intn(len(in) + 1)
			in = in[:pos] + " \t"[r.Intn(2):][:1] + in[pos:]
		}

		var got string
		var err error
		if isSol {
			got, err = NormalizeSolana(in)
		} else {
			got, err = NormalizeTron(in)
		}
		if err == nil {
			// Accepted corruptions are only legal when the output is the
			// verbatim (corrupted) input — never a repaired/case-folded form.
			if got != in {
				t.Fatalf("iter %d: silently normalized %q -> %q (orig %q)", i, in, got, orig)
			}
			if !isSol {
				t.Fatalf("iter %d: corrupted tron %q accepted (checksum must catch single-char edits)", i, in)
			}
		}
	}
}

// A case flip on base58 must never round-trip back to the original mixed
// case — lowercased/case-folded variants are different addresses, and any
// acceptance must return the *variant*, verbatim.
func TestNonEVM_CaseFlipNeverRepairs(t *testing.T) {
	r := rand.New(rand.NewSource(777))
	for i := 0; i < propIterations; i++ {
		in := encodeBase58(randBytes(r, 32))
		lower := strings.ToLower(in)
		if lower == in {
			continue // no letters to flip
		}
		got, err := NormalizeSolana(lower)
		if err == nil && got != lower {
			t.Fatalf("iter %d: case-folded input %q repaired to %q", i, lower, got)
		}
	}
}
