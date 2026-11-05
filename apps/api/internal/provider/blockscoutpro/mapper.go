package blockscoutpro

import (
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// txRow is the union of txlist and tokentx row shapes — unused fields stay
// zero per endpoint.
type txRow struct {
	BlockNumber     string `json:"blockNumber"`
	TimeStamp       string `json:"timeStamp"`
	Hash            string `json:"hash"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`
	IsError         string `json:"isError"`
	ContractAddress string `json:"contractAddress"`
	Confirmations   string `json:"confirmations"`
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func unixTs(s string) time.Time {
	return time.Unix(atoi(s), 0).UTC()
}

// mapTxlist maps a native-transfer row. Zero-value rows (contract calls,
// not transfers) are excluded upstream of the indexer per plan §6 — a
// scope exclusion, never counted in rows_skipped.
func mapTxlist(r txRow) provider.Transfer {
	return provider.Transfer{
		TxHash:         strings.ToLower(r.Hash),
		Stream:         streamTxlist,
		Type:           provider.TransferKindNative,
		From:           r.From,
		To:             r.To,
		Asset:          "",
		AmountRaw:      strings.TrimSpace(r.Value),
		BlockNumber:    atoi(r.BlockNumber),
		BlockTimestamp: unixTs(r.TimeStamp),
		Confirmations:  atoi(r.Confirmations),
		IsError:        r.IsError == "1",
	}
}

// mapTokentx maps a token-transfer row; tokentx carries no isError flag so
// token rows report is_error=false. A missing contractAddress reaches the
// indexer with Asset="" — it is skipped and counted, never silently dropped.
func mapTokentx(r txRow) provider.Transfer {
	return provider.Transfer{
		TxHash:         strings.ToLower(r.Hash),
		Stream:         streamTokentx,
		Type:           provider.TransferKindToken,
		From:           r.From,
		To:             r.To,
		Asset:          r.ContractAddress,
		AmountRaw:      strings.TrimSpace(r.Value),
		BlockNumber:    atoi(r.BlockNumber),
		BlockTimestamp: unixTs(r.TimeStamp),
		Confirmations:  atoi(r.Confirmations),
		IsError:        false,
	}
}

// keepTxlist reports whether a txlist row is a real value transfer —
// value>0 client-side filter (plan §6 scope exclusion).
func keepTxlist(r txRow) bool {
	v := strings.TrimSpace(r.Value)
	if v == "" || v == "0" {
		return false
	}
	// Value is a decimal integer string; >0 iff any non-zero digit.
	for _, c := range v {
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}
