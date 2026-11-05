package moralis

import (
	"strings"
	"time"

	"checkwallet/internal/provider"
)

type nativePage struct {
	Result []nativeRow `json:"result"`
	Cursor string      `json:"cursor"`
}

type nativeRow struct {
	Hash           string `json:"hash"`
	BlockNumber    string `json:"block_number"`    // decimal string
	BlockTimestamp string `json:"block_timestamp"` // RFC3339
	FromAddress    string `json:"from_address"`
	ToAddress      string `json:"to_address"`
	Value          string `json:"value"`
	ReceiptStatus  string `json:"receipt_status"` // "0"/"1"
}

type tokenPage struct {
	Result []tokenRow `json:"result"`
	Cursor string     `json:"cursor"`
}

type tokenRow struct {
	TransactionHash string `json:"transaction_hash"`
	Address         string `json:"address"` // token contract
	FromAddress     string `json:"from_address"`
	ToAddress       string `json:"to_address"`
	Value           string `json:"value"`
	BlockNumber     string `json:"block_number"`
	BlockTimestamp  string `json:"block_timestamp"`
}

// atoi parses a strict decimal string; any non-digit byte or empty
// input fails — a truncated prefix like "12x"→12 is a silent lie.
func atoi(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

func mapNative(tx nativeRow, head int64) (provider.Transfer, bool) {
	if tx.Value == "" || tx.Value == "0" {
		return provider.Transfer{}, false
	}
	block, ok := atoi(tx.BlockNumber)
	if !ok {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 && block > 0 {
		conf = head - block + 1
	}
	ts, _ := time.Parse(time.RFC3339, tx.BlockTimestamp)
	return provider.Transfer{
		TxHash: strings.ToLower(tx.Hash), Stream: streamNative,
		Type: provider.TransferKindNative,
		From: strings.ToLower(tx.FromAddress), To: strings.ToLower(tx.ToAddress),
		AmountRaw: tx.Value, BlockNumber: block, BlockTimestamp: ts,
		Confirmations: conf, IsError: tx.ReceiptStatus == "0",
	}, true
}

func mapToken(tx tokenRow, head int64) (provider.Transfer, bool) {
	block, ok := atoi(tx.BlockNumber)
	if !ok {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 && block > 0 {
		conf = head - block + 1
	}
	ts, _ := time.Parse(time.RFC3339, tx.BlockTimestamp)
	return provider.Transfer{
		TxHash: strings.ToLower(tx.TransactionHash), Stream: streamToken,
		Type: provider.TransferKindToken,
		From: strings.ToLower(tx.FromAddress), To: strings.ToLower(tx.ToAddress),
		Asset: strings.ToLower(tx.Address), AmountRaw: tx.Value,
		BlockNumber: block, BlockTimestamp: ts, Confirmations: conf,
	}, true
}
