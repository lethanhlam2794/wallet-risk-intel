package wallet

import (
	"fmt"
	"math/big"
)

// base58Alphabet is the Bitcoin/Solana alphabet — no '0', 'O', 'I', 'l'.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Index maps ASCII → digit value; -1 marks characters outside the
// alphabet (including every byte ≥ 128).
var base58Index = func() [128]int8 {
	var idx [128]int8
	for i := range idx {
		idx[i] = -1
	}
	for i := 0; i < len(base58Alphabet); i++ {
		idx[base58Alphabet[i]] = int8(i)
	}
	return idx
}()

var bigBase58 = big.NewInt(58)

// decodeBase58 decodes a base58 string into big-endian bytes. Each leading
// '1' produces one leading zero byte. Characters outside the alphabet,
// empty input, and any non-ASCII byte are rejected — the alphabet is
// case-sensitive, so no case folding is applied ever.
func decodeBase58(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrInvalidAddress
	}
	n := new(big.Int)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 128 || base58Index[c] < 0 {
			return nil, fmt.Errorf("%w: invalid base58 character %q", ErrInvalidAddress, string(c))
		}
		n.Mul(n, bigBase58)
		n.Add(n, big.NewInt(int64(base58Index[c])))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	body := n.Bytes() // big-endian, no leading zeros (empty when n == 0)
	out := make([]byte, zeros+len(body))
	copy(out[zeros:], body)
	return out, nil
}
