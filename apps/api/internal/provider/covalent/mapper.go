package covalent

import (
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// txPage is the transactions_v3 data payload.
type txPage struct {
	Items []txRow `json:"items"`
	Links struct {
		Prev *string `json:"prev"`
		Next *string `json:"next"`
	} `json:"links"`
}

// txRow is one v3 transaction row (decimal-string amounts).
type txRow struct {
	TxHash        string `json:"tx_hash"`
	BlockHeight   int64  `json:"block_height"`
	BlockSignedAt string `json:"block_signed_at"`
	FromAddress   string `json:"from_address"`
	ToAddress     string `json:"to_address"` // "" on contract creation
	Value         string `json:"value"`
	Successful    bool   `json:"successful"`
	LogEvents     []struct {
		SenderContractAddress string `json:"sender_contract_address"`
		Decoded               *struct {
			Name   string `json:"name"`
			Params []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"params"`
		} `json:"decoded"`
	} `json:"log_events"`
}

// mapTx emits a NATIVE row when the tx moved value, plus one TOKEN row
// per decoded ERC-20 Transfer log event touching the wallet.
func mapTx(wallet string, tx txRow, head int64) []provider.Transfer {
	var conf int64
	if head > 0 && tx.BlockHeight > 0 {
		conf = head - tx.BlockHeight + 1
	}
	ts, _ := time.Parse(time.RFC3339, tx.BlockSignedAt)
	isErr := !tx.Successful
	var out []provider.Transfer
	if tx.Value != "" && tx.Value != "0" {
		out = append(out, provider.Transfer{
			TxHash:         strings.ToLower(tx.TxHash),
			Stream:         streamTx,
			Type:           provider.TransferKindNative,
			From:           strings.ToLower(tx.FromAddress),
			To:             strings.ToLower(tx.ToAddress),
			AmountRaw:      tx.Value,
			BlockNumber:    tx.BlockHeight,
			BlockTimestamp: ts,
			Confirmations:  conf,
			IsError:        isErr,
		})
	}
	for _, lg := range tx.LogEvents {
		if lg.Decoded == nil || !strings.EqualFold(lg.Decoded.Name, "Transfer") {
			continue
		}
		var from, to, amt string
		for _, p := range lg.Decoded.Params {
			switch strings.ToLower(p.Name) {
			case "from":
				from = strings.ToLower(p.Value)
			case "to":
				to = strings.ToLower(p.Value)
			case "value":
				amt = p.Value
			}
		}
		if (from != wallet && to != wallet) || amt == "" {
			continue
		}
		out = append(out, provider.Transfer{
			TxHash:         strings.ToLower(tx.TxHash),
			Stream:         streamTx,
			Type:           provider.TransferKindToken,
			From:           from,
			To:             to,
			Asset:          strings.ToLower(lg.SenderContractAddress),
			AmountRaw:      amt,
			BlockNumber:    tx.BlockHeight,
			BlockTimestamp: ts,
			Confirmations:  conf,
			IsError:        isErr,
		})
	}
	return out
}
