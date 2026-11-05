package nodereal

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// nrTransfer is the nr_getAssetTransfers item shape (Alchemy-compatible
// plus NodeReal's blockTimeStamp/receiptsStatus extensions).
type nrTransfer struct {
	BlockNum    string          `json:"blockNum"`
	Hash        string          `json:"hash"`
	From        string          `json:"from"`
	To          *string         `json:"to"`
	Value       json.RawMessage `json:"value"`
	Asset       string          `json:"asset"`
	Category    string          `json:"category"`
	RawContract *struct {
		Value   string `json:"value"`
		Address string `json:"address"`
	} `json:"rawContract"`
	BlockTimeStamp any `json:"blockTimeStamp"`
	BlockTimestamp any `json:"blockTimestamp"`
	ReceiptsStatus any `json:"receiptsStatus"`
}

// mapTransfer normalizes one upstream row; ok=false means the row is out
// of the declared categories (kind-filtered or NFT/INTERNAL classes —
// upstream category filter should already exclude them, so this is
// belt-and-suspenders).
func mapTransfer(r nrTransfer, stream string, head int64,
	wantNative, wantToken bool) (provider.Transfer, bool) {

	t := provider.Transfer{
		TxHash:      strings.ToLower(r.Hash),
		Stream:      stream,
		From:        r.From,
		BlockNumber: hexOrDec(r.BlockNum),
	}
	if r.To != nil {
		t.To = *r.To
	}
	switch r.Category {
	case "external":
		if !wantNative {
			return t, false
		}
		t.Type = provider.TransferKindNative
	case "20", "erc20":
		if !wantToken {
			return t, false
		}
		t.Type = provider.TransferKindToken
		if r.RawContract != nil {
			t.Asset = r.RawContract.Address
		}
	default:
		return t, false
	}
	t.AmountRaw = rawAmount(r)
	t.BlockTimestamp = parseTimestamp(r.BlockTimeStamp, r.BlockTimestamp)
	if head > 0 && t.BlockNumber > 0 {
		t.Confirmations = head - t.BlockNumber + 1
	}
	t.IsError = receiptsFailed(r.ReceiptsStatus)
	return t, true
}

// rawAmount prefers rawContract.value (hex-pinned raw integer); falls back
// to value only when it is itself an integer/hex string — a JSON float is
// decimal-unit and is never reverse-engineered into a raw amount ("" =
// underivable; the indexer skips + counts the row).
func rawAmount(r nrTransfer) string {
	if r.RawContract != nil {
		if s := intString(r.RawContract.Value); s != "" {
			return s
		}
	}
	var s string
	if err := json.Unmarshal(r.Value, &s); err == nil {
		return intString(s)
	}
	return ""
}

// intString accepts "0x…" hex or decimal integer strings and returns the
// canonical decimal form; anything else → "".
func intString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, ok := new(big.Int).SetString(s[2:], 16)
		if !ok {
			return ""
		}
		return n.String()
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return ""
	}
	return n.String()
}

func hexOrDec(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, _ := strconv.ParseInt(s[2:], 16, 64)
		return n
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseTimestamp tolerates NodeReal's blockTimeStamp/blockTimestamp being
// a hex string, decimal unix seconds, JSON number, or RFC3339.
func parseTimestamp(fields ...any) time.Time {
	for _, f := range fields {
		switch v := f.(type) {
		case nil:
			continue
		case string:
			if ts := hexOrDec(v); ts > 0 {
				return time.Unix(ts, 0).UTC()
			}
			if tt, err := time.Parse(time.RFC3339, v); err == nil {
				return tt.UTC()
			}
		case float64:
			if v > 0 {
				return time.Unix(int64(v), 0).UTC()
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n > 0 {
				return time.Unix(n, 0).UTC()
			}
		}
	}
	return time.Time{}
}

// receiptsFailed maps the receiptsStatus exec flag: explicit zero/false
// means the mined tx reverted (is_error); absent means unknown-success.
func receiptsFailed(v any) bool {
	switch s := v.(type) {
	case nil:
		return false
	case bool:
		return !s
	case float64:
		return s == 0
	case string:
		s = strings.TrimSpace(strings.ToLower(s))
		return s == "0" || s == "0x0" || s == "false"
	case json.Number:
		n, err := s.Int64()
		return err == nil && n == 0
	}
	return false
}
