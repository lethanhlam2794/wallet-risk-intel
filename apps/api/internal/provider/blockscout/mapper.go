package blockscout

import (
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// listResponse is the Blockscout v2 list envelope shared by the two
// endpoints used here: items newest-first + next_page_params (null when
// the list is exhausted).
type listResponse struct {
	Items          []listItem     `json:"items"`
	NextPageParams map[string]any `json:"next_page_params"`
}

// listItem is the union of the /transactions and /token-transfers item
// shapes; unused fields stay zero per endpoint.
type listItem struct {
	// shared
	BlockNumber int64  `json:"block_number"`
	Timestamp   string `json:"timestamp"`
	// transactions
	Hash          string `json:"hash"`
	Value         string `json:"value"`
	Status        string `json:"status"`
	Confirmations int64  `json:"confirmations"`
	// token-transfers
	TransactionHash string `json:"transaction_hash"`
	LogIndex        int    `json:"log_index"`
	Token           struct {
		AddressHash string `json:"address_hash"`
	} `json:"token"`
	Total struct {
		Value string `json:"value"`
	} `json:"total"`
	From struct {
		Hash string `json:"hash"`
	} `json:"from"`
	To *struct {
		Hash string `json:"hash"`
	} `json:"to"` // null on contract creation
}

// block is the item's ordering key.
func (it listItem) block() int64 { return it.BlockNumber }

// mapItem converts one upstream row to a provider.Transfer; ok=false for
// rows that are not value transfers (zero-value contract calls on the
// native stream — a call is not a NATIVE transfer).
func mapItem(stream string, it listItem) (provider.Transfer, bool) {
	to := ""
	if it.To != nil {
		to = strings.ToLower(it.To.Hash)
	}
	ts, _ := time.Parse(time.RFC3339, it.Timestamp) // unparseable → zero → NULL
	switch stream {
	case streamNative:
		if it.Value == "" || it.Value == "0" {
			return provider.Transfer{}, false
		}
		return provider.Transfer{
			TxHash:         strings.ToLower(it.Hash),
			Stream:         streamNative,
			Type:           provider.TransferKindNative,
			From:           strings.ToLower(it.From.Hash),
			To:             to,
			AmountRaw:      it.Value,
			BlockNumber:    it.BlockNumber,
			BlockTimestamp: ts,
			Confirmations:  it.Confirmations,
			IsError:        strings.EqualFold(it.Status, "error"),
		}, true
	case streamToken:
		return provider.Transfer{
			TxHash:         strings.ToLower(it.TransactionHash),
			Stream:         streamToken,
			Type:           provider.TransferKindToken,
			From:           strings.ToLower(it.From.Hash),
			To:             to,
			Asset:          strings.ToLower(it.Token.AddressHash),
			AmountRaw:      it.Total.Value,
			BlockNumber:    it.BlockNumber,
			BlockTimestamp: ts,
			Confirmations:  it.Confirmations,
		}, true
	}
	return provider.Transfer{}, false
}
