package oklink

import (
	"math/big"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// envelope is {code, msg, data:[page-block]}; code "0" is the only
// success marker — any other code is a provider-level signal (degraded).
type envelope struct {
	Code string   `json:"code"`
	Msg  string   `json:"msg"`
	Data []okData `json:"data"`
}

// okData carries the page block; the native endpoint nests rows under
// transactionLists while the token endpoint uses transactionList
// (singular) — both are decoded and merged by rows().
type okData struct {
	Page             string  `json:"page"`
	TotalPage        string  `json:"totalPage"`
	Height           string  `json:"height"` // /blockchain/block head shape
	TransactionLists []okRow `json:"transactionLists"`
	TransactionList  []okRow `json:"transactionList"`
}

func (d okData) rows() []okRow {
	if len(d.TransactionLists) > 0 {
		return d.TransactionLists
	}
	return d.TransactionList
}

type okRow struct {
	TxID                 string `json:"txId"`
	Height               string `json:"height"`          // block number, decimal string
	TransactionTime      string `json:"transactionTime"` // ms epoch string
	From                 string `json:"from"`
	To                   string `json:"to"`
	Amount               string `json:"amount"` // normalized decimal, NOT raw integer
	TransactionSymbol    string `json:"transactionSymbol"`
	TxFee                string `json:"txFee"`
	State                string `json:"state"` // "success" | other
	TokenContractAddress string `json:"tokenContractAddress"`
}

// nativeWei converts a normalized BNB amount to raw wei — exact rational
// math; "" when the fraction exceeds 18 decimals (underivable is honest).
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

func mapRow(row okRow, stream string, head int64) (provider.Transfer, bool) {
	if row.TxID == "" || row.From == "" || row.To == "" {
		return provider.Transfer{}, false
	}
	block, _ := strconv.ParseInt(row.Height, 10, 64)
	if block <= 0 {
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 {
		conf = head - block + 1
	}
	ms, _ := strconv.ParseInt(row.TransactionTime, 10, 64)
	kind := provider.TransferKindNative
	asset, amount := "", nativeWei(row.Amount)
	if stream == streamToken {
		kind = provider.TransferKindToken
		asset = row.TokenContractAddress
		amount = "" // token decimals not published in the row — underivable
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.TxID), Stream: stream, Type: kind,
		From: strings.ToLower(row.From), To: strings.ToLower(row.To),
		Asset: strings.ToLower(asset), AmountRaw: amount,
		BlockNumber:    block,
		BlockTimestamp: time.UnixMilli(ms).UTC(),
		Confirmations:  conf,
		IsError:        row.State != "" && row.State != "success",
	}, true
}
