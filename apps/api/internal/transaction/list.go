package transaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

// MaxListLimit is the /transactions page bound (spec).
const MaxListLimit = 100

const defaultListLimit = 50

// cursorPayload is the keyset page token (block_timestamp DESC, id DESC).
type cursorPayload struct {
	Ts time.Time `json:"t"`
	ID int64     `json:"i"`
}

func encodeCursor(ts time.Time, id int64) string {
	b, _ := json.Marshal(cursorPayload{Ts: ts.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("bad cursor")
	}
	var c cursorPayload
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, 0, fmt.Errorf("bad cursor")
	}
	return c.Ts, c.ID, nil
}

// ListTransactions runs EnsureIndexed under the hop-0 gates then pages
// the stored rows (plan §3.5) — a no-fetch response still reports the
// persisted coverage honestly.
func (s *Service) ListTransactions(ctx context.Context, net network.Network,
	normalized string, f TxFilters) (*ListResult, error) {

	outcome, err := s.EnsureIndexed(ctx, net, normalized, EnsureOptions{
		Refresh: f.Refresh,
		Budget:  NewBudget(int64(s.BudgetFor(net))),
	})
	if err != nil {
		return nil, err
	}
	res := &ListResult{IndexOutcome: outcome, Items: []TxItem{}}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	var curTs pgtype.Timestamptz
	var curID int64
	if f.Cursor != "" {
		ts, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, ErrInvalidCursor
		}
		curTs = pgtype.Timestamptz{Time: ts, Valid: true}
		curID = id
	}
	var fromTs, toTs pgtype.Timestamptz
	if f.FromTs != nil {
		fromTs = pgtype.Timestamptz{Time: *f.FromTs, Valid: true}
	}
	if f.ToTs != nil {
		toTs = pgtype.Timestamptz{Time: *f.ToTs, Valid: true}
	}
	var asset pgtype.Text
	switch f.Asset {
	case "":
	case "native":
		asset = pgtype.Text{String: "native", Valid: true}
	default:
		a, err := wallet.NormalizeEVM(f.Asset)
		if err != nil {
			return nil, ErrInvalidInput
		}
		asset = pgtype.Text{String: a, Valid: true}
	}

	rows, err := s.q.ListTransactions(ctx, db.ListTransactionsParams{
		NetworkID:    net.ID,
		FromWalletID: pgtype.Int8{Int64: outcome.WalletID, Valid: true},
		Direction:    pgtype.Text{String: f.Direction, Valid: f.Direction != ""},
		Asset:        asset,
		FromTs:       fromTs,
		ToTs:         toTs,
		CursorTs:     curTs,
		CursorID:     pgtype.Int8{Int64: curID, Valid: f.Cursor != ""},
		Lim:          int64(limit + 1),
	})
	if err != nil {
		return nil, err
	}

	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		res.NextCursor = encodeCursor(last.BlockTimestamp.Time, last.ID)
	}
	for _, r := range rows {
		res.Items = append(res.Items, txItem(r, normalized))
	}
	return res, nil
}

// txItem maps a stored row to the wire item; direction comes from SQL
// ('self' is the row's fact regardless of the request filter), and the
// counterparty is the far endpoint — or the wallet itself on self rows.
func txItem(r db.ListTransactionsRow, self string) TxItem {
	cp := r.ToAddress
	switch r.Direction {
	case "self":
		cp = self
	case "in":
		cp = r.FromAddress
	}
	item := TxItem{
		TxHash:             r.TxHash,
		Direction:          r.Direction,
		TransferType:       r.TransferType,
		AmountRaw:          r.AmountRaw,
		Counterparty:       cp,
		ConfirmationStatus: r.ConfirmationStatus,
		IsError:            r.IsError,
		TransferIndex:      r.TransferIndex,
	}
	if r.AssetIdentifier.Valid {
		a := r.AssetIdentifier.String
		item.AssetIdentifier = &a
	}
	if r.BlockNumber.Valid {
		n := r.BlockNumber.Int64
		item.BlockNumber = &n
	}
	if r.BlockTimestamp.Valid {
		t := r.BlockTimestamp.Time.UTC()
		item.BlockTimestamp = &t
	}
	return item
}
