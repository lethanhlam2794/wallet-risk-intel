package chainbase

import (
	"encoding/json"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// envelope is Chainbase's {code:0,message:"ok",data,next_page,count}
// shape; code != 0 is a provider-level signal → degraded.
type envelope struct {
	Code     int             `json:"code"`
	Message  string          `json:"message"`
	NextPage *int            `json:"next_page"`
	Data     json.RawMessage `json:"data"`
}

// acctTx is one /v1/account/txs row. value is a raw wei integer;
// status 0 = failed tx. Rows are newest-first upstream.
type acctTx struct {
	Status         int    `json:"status"`
	BlockNumber    int64  `json:"block_number"`
	BlockTimestamp string `json:"block_timestamp"`
	TxHash         string `json:"transaction_hash"`
	TxIndex        int    `json:"transaction_index"`
	From           string `json:"from_address"`
	To             string `json:"to_address"`
	Value          string `json:"value"`
}

// tokenTx is one /v1/token/transfers row — raw integer value. The
// documented schema omits contract_address; real responses may carry
// it. Rows lacking it are skipped (asset id must never be invented).
type tokenTx struct {
	BlockNumber    int64  `json:"block_number"`
	BlockTimestamp string `json:"block_timestamp"`
	TxHash         string `json:"transaction_hash"`
	TxIndex        int    `json:"transaction_index"`
	LogIndex       int    `json:"log_index"`
	Contract       string `json:"contract_address"`
	From           string `json:"from_address"`
	To             string `json:"to_address"`
	Value          string `json:"value"`
}

func parseTS(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// mapNative emits a native transfer for successful non-zero-value rows;
// failed txs and contract calls/creations (no to, zero value) skipped.
func mapNative(row acctTx, head int64) (provider.Transfer, bool) {
	if row.TxHash == "" || row.From == "" || row.To == "" ||
		row.Status == 0 || row.BlockNumber <= 0 {
		return provider.Transfer{}, false
	}
	if row.Value == "" || row.Value == "0" {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 {
		conf = head - row.BlockNumber + 1
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.TxHash), Stream: streamNative,
		Type: provider.TransferKindNative,
		From: strings.ToLower(row.From), To: strings.ToLower(row.To),
		AmountRaw: row.Value, BlockNumber: row.BlockNumber,
		BlockTimestamp: parseTS(row.BlockTimestamp), Confirmations: conf,
	}, true
}

// mapToken emits a token transfer; rows without contract_address are
// skipped — asset identity cannot be derived.
func mapToken(row tokenTx, head int64) (provider.Transfer, bool) {
	if row.TxHash == "" || row.Contract == "" || row.From == "" ||
		row.To == "" || row.BlockNumber <= 0 {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 {
		conf = head - row.BlockNumber + 1
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.TxHash), Stream: streamToken,
		Type: provider.TransferKindToken,
		From: strings.ToLower(row.From), To: strings.ToLower(row.To),
		Asset: strings.ToLower(row.Contract), AmountRaw: row.Value,
		BlockNumber:    row.BlockNumber,
		BlockTimestamp: parseTS(row.BlockTimestamp), Confirmations: conf,
	}, true
}
