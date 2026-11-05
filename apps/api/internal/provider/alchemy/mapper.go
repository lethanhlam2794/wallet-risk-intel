package alchemy

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// alchemyTransfer is the alchemy_getAssetTransfers item shape — same
// core layout as nr_getAssetTransfers plus the metadata.blockTimestamp
// extension (ETH-documented; absent where unsupported).
type alchemyTransfer struct {
	BlockNum    string          `json:"blockNum"`
	Hash        string          `json:"hash"`
	From        string          `json:"from"`
	To          *string         `json:"to"`
	Value       json.RawMessage `json:"value"`
	Asset       string          `json:"asset"`
	Category    string          `json:"category"`
	RawContract *struct {
		Value   string  `json:"value"`
		Address *string `json:"address"`
		Decimal string  `json:"decimal"`
	} `json:"rawContract"`
	Metadata *struct {
		BlockTimestamp string `json:"blockTimestamp"`
	} `json:"metadata"`
	ReceiptsStatus any `json:"receiptsStatus"`
}

// mapTransfer normalizes one upstream row; ok=false means the row is out
// of the declared categories (kind-filtered or NFT/INTERNAL classes —
// the upstream category filter should already exclude them, so this is
// belt-and-suspenders).
func mapTransfer(r alchemyTransfer, stream string, head int64,
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
	case "erc20", "20":
		if !wantToken {
			return t, false
		}
		t.Type = provider.TransferKindToken
		if r.RawContract != nil && r.RawContract.Address != nil {
			t.Asset = *r.RawContract.Address
		}
	default:
		return t, false
	}
	t.AmountRaw = rawAmount(r)
	t.BlockTimestamp = parseTimestamp(r)
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
func rawAmount(r alchemyTransfer) string {
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

// parseTimestamp reads metadata.blockTimestamp — an ISO-8601 string —
// when present; stays zero where the field is undocumented (BSC).
func parseTimestamp(r alchemyTransfer) time.Time {
	if r.Metadata == nil || r.Metadata.BlockTimestamp == "" {
		return time.Time{}
	}
	if tt, err := time.Parse(time.RFC3339, r.Metadata.BlockTimestamp); err == nil {
		return tt.UTC()
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
