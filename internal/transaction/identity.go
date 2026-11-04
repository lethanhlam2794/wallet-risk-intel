package transaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/wallet"
)

// txRowJSON is one transaction row in the batch-upsert payload; field
// names are the table's column names so jsonb_populate_recordset maps
// them directly. AmountRaw stays a decimal string — Postgres casts it to
// NUMERIC, never a float.
type txRowJSON struct {
	TxHash             string     `json:"tx_hash"`
	TransferIndex      string     `json:"transfer_index"`
	TransferType       string     `json:"transfer_type"`
	FromWalletID       int64      `json:"from_wallet_id"`
	ToWalletID         int64      `json:"to_wallet_id"`
	AssetIdentifier    *string    `json:"asset_identifier"`
	AmountRaw          string     `json:"amount_raw"`
	BlockNumber        int64      `json:"block_number"`
	BlockTimestamp     *time.Time `json:"block_timestamp"`
	ConfirmationStatus string     `json:"confirmation_status"`
	IsError            bool       `json:"is_error"`
}

// contentHash is the 16-hex SHA-256 prefix over the normalized semantic
// fields (plan §3.2) — context-free: the same transfer seen from either
// side or either wallet derives the same identity.
func contentHash(kind, asset, from, to, amount string) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{kind, asset, from, to, amount}, "|")))
	return hex.EncodeToString(sum[:8])
}

// kindLetter is the transfer_index prefix.
func kindLetter(kind string) string {
	if kind == provider.TransferKindToken {
		return "t"
	}
	return "n"
}

// groupKey identifies identical content within one tx hash.
type groupKey struct {
	txHash, hash string
}

// normalized holds one transfer's canonical fields after validation.
type normalized struct {
	t      provider.Transfer
	from   string
	to     string
	asset  *string
	amount string
	hash   string
}

// buildRows normalizes, validates, derives content identities with the
// max-per-stream occurrence rule, resolves both endpoint wallet ids in
// bulk, and serializes the batch for the upsert.
func (s *Service) buildRows(ctx context.Context, net network.Network,
	transfers []provider.Transfer) ([]byte, RowsSkipped, int64, error) {

	var skipped RowsSkipped
	groups := map[groupKey]*normalized{}
	counts := map[groupKey]map[string]int{} // per-stream occurrence counts
	var order []groupKey                    // deterministic emission

	addrSet := map[string]struct{}{}
	for _, tr := range transfers {
		kind := tr.Type
		if kind != provider.TransferKindNative && kind != provider.TransferKindToken {
			skipped.InvalidAddress++
			continue
		}
		from, err := wallet.NormalizeEVM(tr.From)
		if err != nil {
			skipped.InvalidAddress++
			continue
		}
		to, err := wallet.NormalizeEVM(tr.To)
		if err != nil {
			skipped.InvalidAddress++
			continue
		}
		var asset *string
		if kind == provider.TransferKindToken {
			if tr.Asset == "" {
				skipped.MissingContract++
				continue
			}
			a, err := wallet.NormalizeEVM(tr.Asset)
			if err != nil {
				skipped.InvalidAddress++
				continue
			}
			asset = &a
		}
		amount, ok := canonAmount(tr.AmountRaw)
		if !ok {
			skipped.InvalidAddress++ // invalid-field bucket (documented)
			continue
		}
		txHash := strings.ToLower(tr.TxHash)
		if txHash == "" {
			skipped.InvalidAddress++
			continue
		}
		astr := ""
		if asset != nil {
			astr = *asset
		}
		h := contentHash(kind, astr, from, to, amount)
		gk := groupKey{txHash, h}
		if counts[gk] == nil {
			counts[gk] = map[string]int{}
		}
		counts[gk][tr.Stream]++
		if _, seen := groups[gk]; !seen {
			groups[gk] = &normalized{
				t: tr, from: from, to: to, asset: asset,
				amount: amount, hash: h,
			}
			order = append(order, gk)
		}
		addrSet[from] = struct{}{}
		addrSet[to] = struct{}{}
	}
	if len(order) == 0 {
		return []byte("[]"), skipped, 0, nil
	}

	// Resolve endpoint wallet ids in one round trip (creates rows —
	// plan §3.2 "bulk wallet resolution").
	addrs := make([]string, 0, len(addrSet))
	for a := range addrSet {
		addrs = append(addrs, a)
	}
	ws, err := s.q.BulkResolveWallets(ctx, db.BulkResolveWalletsParams{
		Column1: net.ID, Column2: addrs})
	if err != nil {
		return nil, skipped, 0, err
	}
	ids := make(map[string]int64, len(ws))
	for _, w := range ws {
		ids[w.NormalizedAddress] = w.ID
	}

	// Max-per-stream occurrence: the .k suffix count is the maximum
	// across streams, so the same transfer observed on both sides of a
	// W→W or multi-stream page yields one identity set.
	rows := make([]txRowJSON, 0, len(order))
	sort.Slice(order, func(i, j int) bool {
		a, b := groups[order[i]], groups[order[j]]
		if a.t.BlockTimestamp.Equal(b.t.BlockTimestamp) {
			if a.t.TxHash == b.t.TxHash {
				return a.hash < b.hash
			}
			return a.t.TxHash < b.t.TxHash
		}
		return a.t.BlockTimestamp.Before(b.t.BlockTimestamp)
	})
	for _, gk := range order {
		n := 0
		for _, c := range counts[gk] {
			if c > n {
				n = c
			}
		}
		g := groups[gk]
		fromID := ids[g.from]
		toID := ids[g.to]
		if fromID == 0 || toID == 0 {
			return nil, skipped, 0, fmt.Errorf("transaction: wallet resolution incomplete")
		}
		conf := "confirmed"
		if g.t.Confirmations <= 0 {
			conf = "pending"
		}
		var bts *time.Time
		if !g.t.BlockTimestamp.IsZero() {
			v := g.t.BlockTimestamp.UTC()
			bts = &v // unknown upstream timestamp persists NULL, never year-1
		}
		for k := 1; k <= n; k++ {
			rows = append(rows, txRowJSON{
				TxHash:             gk.txHash,
				TransferIndex:      fmt.Sprintf("%s:%s.%d", kindLetter(g.t.Type), gk.hash, k),
				TransferType:       g.t.Type,
				FromWalletID:       fromID,
				ToWalletID:         toID,
				AssetIdentifier:    g.asset,
				AmountRaw:          g.amount,
				BlockNumber:        g.t.BlockNumber,
				BlockTimestamp:     bts,
				ConfirmationStatus: conf,
				IsError:            g.t.IsError,
			})
		}
	}
	data, err := json.Marshal(rows)
	return data, skipped, int64(len(rows)), err
}

// canonAmount validates an amount as a non-negative raw integer and
// returns the canonical decimal form (strips leading zeros).
func canonAmount(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 {
		return "", false
	}
	return n.String(), true
}
