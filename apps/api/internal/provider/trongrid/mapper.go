package trongrid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// acctTxRow is one row of /v1/accounts/{a}/transactions. Only the fields
// the mapping needs are decoded.
type acctTxRow struct {
	TxID           string `json:"txID"`
	BlockNumber    int64  `json:"blockNumber"`
	BlockTimestamp int64  `json:"block_timestamp"`
	Ret            []struct {
		ContractRet string `json:"contractRet"`
	} `json:"ret"`
	RawData struct {
		Contract []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value struct {
					OwnerAddress string          `json:"owner_address"`
					ToAddress    string          `json:"to_address"`
					Amount       json.RawMessage `json:"amount"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
}

// trc20Row is one row of /v1/accounts/{a}/transactions/trc20 — notably no
// block number (semantics §b2); it is resolved separately.
type trc20Row struct {
	TxID           string `json:"transaction_id"`
	From           string `json:"from"`
	To             string `json:"to"`
	Value          string `json:"value"`
	Type           string `json:"type"`
	BlockTimestamp int64  `json:"block_timestamp"`
	TokenInfo      struct {
		Address string `json:"address"`
	} `json:"token_info"`
}

// mapTrxRow emits one Transfer per TransferContract element of the row's
// contract array (multi-contract txs carry more than one transfer). Rows
// with no TransferContract are scope-excluded — but the caller still
// anchors their txid→blockNumber for trc20 resolution.
func mapTrxRow(r acctTxRow, head int64) ([]provider.Transfer, error) {
	var out []provider.Transfer
	for i, c := range r.RawData.Contract {
		if c.Type != "TransferContract" {
			continue
		}
		amt, err := rawAmount(c.Parameter.Value.Amount)
		if err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"trongrid trx %s: bad amount %s", r.TxID, c.Parameter.Value.Amount)
		}
		if !positive(amt) {
			continue // amount ≤ 0 is a scope exclusion (semantics §e)
		}
		from, err := hexToBase58(c.Parameter.Value.OwnerAddress)
		if err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"trongrid trx %s owner_address: %v", r.TxID, err)
		}
		to, err := hexToBase58(c.Parameter.Value.ToAddress)
		if err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"trongrid trx %s to_address: %v", r.TxID, err)
		}
		// ret[] is contract-parallel upstream — a shorter array means
		// absent IsError evidence, never a safe default (honest
		// malformed, not fabricated success).
		if i >= len(r.Ret) {
			return nil, provider.Errorf(provider.ErrMalformed,
				"trongrid trx %s: ret[%d] absent for %d contracts",
				r.TxID, i, len(r.RawData.Contract))
		}
		isErr := r.Ret[i].ContractRet != "SUCCESS"
		out = append(out, provider.Transfer{
			TxHash:         strings.ToLower(r.TxID),
			Stream:         streamTRX,
			Type:           provider.TransferKindNative,
			From:           from,
			To:             to,
			Asset:          "",
			AmountRaw:      amt,
			BlockNumber:    r.BlockNumber,
			BlockTimestamp: msTs(r.BlockTimestamp),
			Confirmations:  confirmations(head, r.BlockNumber),
			IsError:        isErr,
		})
	}
	return out, nil
}

// mapTrc20Row maps a resolved TRC-20 Transfer event. block comes from the
// anchor map or txinfo — it is never fabricated; isErr comes from
// txinfo's receipt.result when that call resolved the row (an event
// existing in the index is itself success evidence — semantics §e).
func mapTrc20Row(r trc20Row, block int64, isErr bool, head int64) (provider.Transfer, error) {
	amt := strings.TrimSpace(r.Value)
	if amt == "" || !digitsOnly(amt) {
		return provider.Transfer{}, provider.Errorf(provider.ErrMalformed,
			"trongrid trc20 %s: bad value %q", r.TxID, r.Value)
	}
	return provider.Transfer{
		TxHash:         strings.ToLower(r.TxID),
		Stream:         streamTRC20,
		Type:           provider.TransferKindToken,
		From:           r.From,
		To:             r.To,
		Asset:          r.TokenInfo.Address,
		AmountRaw:      amt,
		BlockNumber:    block,
		BlockTimestamp: msTs(r.BlockTimestamp),
		Confirmations:  confirmations(head, block),
		IsError:        isErr,
	}, nil
}

// rawAmount normalizes an amount that upstream may send as a JSON number
// or string into a raw integer decimal string — never a float.
func rawAmount(rm json.RawMessage) (string, error) {
	s := strings.Trim(strings.TrimSpace(string(rm)), `"`)
	if s == "" {
		return "", nil
	}
	if !digitsOnly(s) {
		return "", fmt.Errorf("non-integer amount %q", s)
	}
	return s, nil
}

// digitsOnly reports whether s is a non-empty unsigned decimal integer.
func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// positive reports whether a decimal integer string is > 0.
func positive(s string) bool {
	if !digitsOnly(s) {
		return false
	}
	for _, c := range s {
		if c != '0' {
			return true
		}
	}
	return false
}

// msTs converts a TronGrid ms timestamp to UTC.
func msTs(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// confirmations = head − block + 1; 0 when either is unknown → 'pending'.
func confirmations(head, block int64) int64 {
	if head <= 0 || block <= 0 || head < block {
		return 0
	}
	return head - block + 1
}

// b58Alphabet is the Bitcoin/TRON base58check alphabet.
const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// hexToBase58 converts a TRON hex address ("41"+20-byte payload — the
// form raw_data carries) to canonical base58check. A non-41 or
// wrong-length payload is an upstream contract break — an error, never
// passed through (semantics §d).
func hexToBase58(h string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(h), "0x"))
	if err != nil || len(raw) != 21 || raw[0] != 0x41 {
		return "", fmt.Errorf("bad tron hex address %q", h)
	}
	sum := sha256.Sum256(raw)
	sum = sha256.Sum256(sum[:])
	return b58Encode(append(raw, sum[:4]...)), nil
}

func b58Encode(b []byte) string {
	x := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for x.Sign() > 0 {
		x.QuoRem(x, base, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for _, v := range b {
		if v != 0 {
			break
		}
		out = append(out, b58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
