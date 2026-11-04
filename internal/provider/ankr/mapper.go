package ankr

import (
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// transferTopic is keccak("Transfer(address,address,uint256)").
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// txRow is the upstream transaction shape (hex-quantity fields per the
// Ankr OpenAPI schema).
type txRow struct {
	Hash        string `json:"hash"`
	BlockNumber string `json:"blockNumber"`
	From        string `json:"from"`
	To          string `json:"to"` // null/absent on contract creation
	Value       string `json:"value"`
	Status      string `json:"status"` // "0x1"/"0x0"
	Timestamp   string `json:"timestamp"`
	Logs        []struct {
		Address         string   `json:"address"`
		Topics          []string `json:"topics"`
		Data            string   `json:"data"`
		LogIndex        string   `json:"logIndex"`
		TransactionHash string   `json:"transactionHash"`
	} `json:"logs"`
}

func hexToDec(s string) (string, bool) {
	n, ok := hexToBig(s)
	if !ok {
		return "", false
	}
	return n.String(), true
}

func hexToInt64(s string) int64 {
	n, ok := hexToBig(s)
	if !ok {
		return 0
	}
	return n.Int64()
}

// topicAddr extracts the 20-byte address from a 32-byte topic word.
func topicAddr(t string) string {
	t = strings.ToLower(strings.TrimPrefix(t, "0x"))
	if len(t) < 40 {
		return ""
	}
	return "0x" + t[len(t)-40:]
}

// mapTx emits a NATIVE row when the tx itself moved value, plus one
// TOKEN row per ERC-20 Transfer log touching the wallet. Rows arrive in
// upstream order (ascending) — the caller appends without re-sorting.
func mapTx(wallet string, tx txRow, head int64) []provider.Transfer {
	block := hexToInt64(tx.BlockNumber)
	var conf int64
	if head > 0 && block > 0 {
		conf = head - block + 1
	}
	var ts time.Time // missing/0 timestamp stays zero → NULL, never epoch
	if u := hexToInt64(tx.Timestamp); u > 0 {
		ts = time.Unix(u, 0).UTC()
	}
	isErr := strings.EqualFold(tx.Status, "0x0")
	var out []provider.Transfer
	if dec, ok := hexToDec(tx.Value); ok && dec != "0" {
		out = append(out, provider.Transfer{
			TxHash:         strings.ToLower(tx.Hash),
			Stream:         streamTx,
			Type:           provider.TransferKindNative,
			From:           strings.ToLower(tx.From),
			To:             strings.ToLower(tx.To),
			AmountRaw:      dec,
			BlockNumber:    block,
			BlockTimestamp: ts,
			Confirmations:  conf,
			IsError:        isErr,
		})
	}
	for _, lg := range tx.Logs {
		if len(lg.Topics) != 3 || !strings.EqualFold(lg.Topics[0], transferTopic) {
			continue
		}
		from, to := topicAddr(lg.Topics[1]), topicAddr(lg.Topics[2])
		if from != wallet && to != wallet {
			continue
		}
		amt, ok := hexToDec(lg.Data)
		if !ok {
			continue
		}
		hash := strings.ToLower(lg.TransactionHash)
		if hash == "" {
			hash = strings.ToLower(tx.Hash)
		}
		out = append(out, provider.Transfer{
			TxHash:         hash,
			Stream:         streamTx,
			Type:           provider.TransferKindToken,
			From:           from,
			To:             to,
			Asset:          strings.ToLower(lg.Address),
			AmountRaw:      amt,
			BlockNumber:    block,
			BlockTimestamp: ts,
			Confirmations:  conf,
			IsError:        isErr,
		})
	}
	return out
}
