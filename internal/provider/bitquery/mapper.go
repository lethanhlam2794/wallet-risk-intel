package bitquery

import (
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// envelope is the GraphQL response: {data:{EVM:{Transfers:[...]}}} plus a
// top-level errors[] array — HTTP 200 with errors means the upstream
// answered but signalled failure (plan restrictions, dataset gaps) →
// degraded, never a clean empty page.
type envelope struct {
	Data struct {
		EVM struct {
			Transfers []transferRow `json:"Transfers"`
			Blocks    []blocksRow   `json:"Blocks"`
		} `json:"EVM"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type transferRow struct {
	Block       block    `json:"Block"`
	Transaction tx       `json:"Transaction"`
	Transfer    transfer `json:"Transfer"`
}

type blocksRow struct {
	Block struct {
		Number json.Number `json:"Number"`
	} `json:"Block"`
}

type block struct {
	Number json.Number `json:"Number"`
	Time   string      `json:"Time"` // ISO8601
}

type tx struct {
	Hash string `json:"Hash"`
}

type transfer struct {
	Sender   string   `json:"Sender"`
	Receiver string   `json:"Receiver"`
	Amount   string   `json:"Amount"` // normalized decimal string
	Success  bool     `json:"Success"`
	Currency currency `json:"Currency"`
}

type currency struct {
	SmartContract string `json:"SmartContract"`
	Symbol        string `json:"Symbol"`
	Decimals      int    `json:"Decimals"`
	Fungible      bool   `json:"Fungible"`
	Native        bool   `json:"Native"`
}

// rawAmount converts the normalized decimal amount to a raw integer using
// the per-row Currency.Decimals — exact rational math, "" when
// underivable (fraction exceeds decimals or unparsable).
func rawAmount(normalized string, decimals int) string {
	r, ok := new(big.Rat).SetString(normalized)
	if !ok || r.Sign() <= 0 || decimals < 0 {
		return ""
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	r.Mul(r, new(big.Rat).SetInt(scale))
	if !r.IsInt() {
		return ""
	}
	return r.Num().String()
}

// mapRow converts one Transfers row to a Transfer; rows with missing
// hash/sender/receiver are skipped (returned ok=false, never invented).
func mapRow(row transferRow, stream string, head int64) (provider.Transfer, bool) {
	tr := row.Transfer
	if row.Transaction.Hash == "" || tr.Sender == "" || tr.Receiver == "" {
		return provider.Transfer{}, false
	}
	blockNum, err := row.Block.Number.Int64()
	if err != nil {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 && blockNum > 0 {
		conf = head - blockNum + 1
	}
	kind := provider.TransferKindNative
	asset := ""
	if stream == streamToken {
		kind = provider.TransferKindToken
		asset = tr.Currency.SmartContract
	}
	ts, _ := time.Parse(time.RFC3339, row.Block.Time)
	return provider.Transfer{
		TxHash: strings.ToLower(row.Transaction.Hash), Stream: stream, Type: kind,
		From: strings.ToLower(tr.Sender), To: strings.ToLower(tr.Receiver),
		Asset:          strings.ToLower(asset),
		AmountRaw:      rawAmount(tr.Amount, tr.Currency.Decimals),
		BlockNumber:    blockNum,
		BlockTimestamp: ts,
		Confirmations:  conf,
		IsError:        !tr.Success,
	}, true
}
