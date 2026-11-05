package wallet

import (
	"context"

	"checkwallet/internal/db"
)

// Service wraps wallet persistence (thin; business rules stay out of pgx).
type Service struct {
	q *db.Queries
}

// NewService builds the service over sqlc queries.
func NewService(q *db.Queries) *Service { return &Service{q: q} }

// GetOrCreate returns the wallet row for (network, normalized address),
// creating it on first sight. The original address representation is
// preserved in `address`.
func (s *Service) GetOrCreate(ctx context.Context, networkID int64, address, normalized string) (db.Wallet, error) {
	return s.q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
		NetworkID:         networkID,
		Address:           address,
		NormalizedAddress: normalized,
		AddressType:       "evm",
	})
}
