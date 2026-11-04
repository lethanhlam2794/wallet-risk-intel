package tatum

import (
	"math/big"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// historyPage is the upstream envelope: rows are "movements relative to
// the queried address" (address + counterAddress + subtype), not raw
// from/to pairs. nextPage/prevPage are Tezos-only cursors — BSC paginates
// with pageSize+offset.
type historyPage struct {
	Result []historyRow `json:"result"`
}

type historyRow struct {
	Hash               string `json:"hash"`
	Address            string `json:"address"` // the queried wallet
	BlockNumber        int64  `json:"blockNumber"`
	TransactionType    string `json:"transactionType"`    // native|fungible|nft|multitoken
	TransactionSubtype string `json:"transactionSubtype"` // incoming|outgoing|zero-transfer
	Amount             string `json:"amount"`             // normalized decimal string, NOT raw integer
	Timestamp          int64  `json:"timestamp"`          // ms epoch
	TokenAddress       string `json:"tokenAddress"`
	TokenID            string `json:"tokenId"`
	CounterAddress     string `json:"counterAddress"`
}

// nativeWei converts a normalized native amount (BNB units) to raw wei —
// exact rational math; returns "" when the fraction exceeds 18 decimals
// (impossible for EVM native, so "" = underivable is honest, never a float).
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

// mapRow converts one movement row to a Transfer. Returns ok=false for
// zero-transfer rows (direction unresolvable upstream — emitting a
// guessed from/to would invent evidence) and direction-unset rows.
func mapRow(row historyRow, stream string, head int64) (provider.Transfer, bool) {
	if row.Hash == "" || row.CounterAddress == "" {
		return provider.Transfer{}, false
	}
	var from, to string
	switch row.TransactionSubtype {
	case "incoming":
		from, to = row.CounterAddress, row.Address
	case "outgoing":
		from, to = row.Address, row.CounterAddress
	default: // zero-transfer — no direction signal
		return provider.Transfer{}, false
	}
	var conf int64
	if head > 0 && row.BlockNumber > 0 {
		conf = head - row.BlockNumber + 1
	}
	kind := provider.TransferKindNative
	asset := ""
	amount := nativeWei(row.Amount)
	if stream == streamToken {
		kind = provider.TransferKindToken
		asset = row.TokenAddress
		amount = "" // token decimals unknown upstream — underivable
	}
	return provider.Transfer{
		TxHash: strings.ToLower(row.Hash), Stream: stream, Type: kind,
		From: strings.ToLower(from), To: strings.ToLower(to),
		Asset: strings.ToLower(asset), AmountRaw: amount,
		BlockNumber:    row.BlockNumber,
		BlockTimestamp: time.UnixMilli(row.Timestamp).UTC(),
		Confirmations:  conf,
	}, true
}
