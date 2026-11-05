package tokenview

import (
	"math/big"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// listEnvelope is {code:1, msg, data:[{txs:[...]}]}; code 1 (integer) is
// the ONLY success marker — any other code is a provider-level signal.
type listEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		Txs []txRow `json:"txs"`
	} `json:"data"`
}

// txRow is one normal-list entry (newest-first). Value is a normalized
// native decimal; tokenTransfer entries carry RAW integer values plus
// the token contract/decimals — one endpoint yields both streams.
type txRow struct {
	Height        int64              `json:"height"`
	Index         int                `json:"index"`
	Time          int64              `json:"time"` // unix seconds
	TxID          string             `json:"txid"`
	From          string             `json:"from"`
	To            string             `json:"to"`
	Value         string             `json:"value"` // normalized native decimal
	TokenTransfer []tokenTransferRow `json:"tokenTransfer"`
}

type tokenTransferRow struct {
	Index         int    `json:"index"`
	TokenAddr     string `json:"tokenAddr"`
	TokenSymbol   string `json:"tokenSymbol"`
	TokenDecimals string `json:"tokenDecimals"`
	From          string `json:"from"`
	To            string `json:"to"`
	Value         string `json:"value"` // raw integer
}

// nativeWei converts a normalized native amount to raw wei — exact
// rational math; "" when the fraction exceeds 18 decimals.
var wei = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func nativeWei(amount string) string {
	r, ok := new(big.Rat).SetString(amount)
	if !ok || r.Sign() <= 0 {
		return ""
	}
	r.Mul(r, new(big.Rat).SetInt(wei))
	if !r.IsInt() {
		return ""
	}
	return r.Num().String()
}

// mapNative emits the native transfer for a tx row; zero-value rows
// (contract calls) are skipped — consistent with the moralis rule.
func mapNative(row txRow, head int64) (provider.Transfer, bool) {
	if row.TxID == "" || row.From == "" || row.To == "" || row.Height <= 0 {
		return provider.Transfer{}, false
	}
	amount := nativeWei(row.Value)
	if amount == "" {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 {
		conf = head - row.Height + 1
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.TxID), Stream: streamNative,
		Type: provider.TransferKindNative,
		From: strings.ToLower(row.From), To: strings.ToLower(row.To),
		AmountRaw: amount, BlockNumber: row.Height,
		BlockTimestamp: time.Unix(row.Time, 0).UTC(), Confirmations: conf,
	}, true
}

// mapToken emits one token transfer nested in a tx row (raw value
// verbatim); missing essentials → skipped.
func mapToken(row txRow, tr tokenTransferRow, head int64) (provider.Transfer, bool) {
	if row.TxID == "" || tr.TokenAddr == "" || tr.From == "" || tr.To == "" {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 && row.Height > 0 {
		conf = head - row.Height + 1
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.TxID), Stream: streamToken,
		Type: provider.TransferKindToken,
		From: strings.ToLower(tr.From), To: strings.ToLower(tr.To),
		Asset: strings.ToLower(tr.TokenAddr), AmountRaw: tr.Value,
		BlockNumber:    row.Height,
		BlockTimestamp: time.Unix(row.Time, 0).UTC(), Confirmations: conf,
	}, true
}

// tokenDedup keys a nested tokenTransfer entry within its tx.
func tokenDedup(row txRow, tr tokenTransferRow) string {
	return row.TxID + "#" + tr.TokenAddr + "#" + tr.From + ">" + tr.To +
		"#" + strconv.Itoa(tr.Index)
}
