package wallet

import (
	"context"
	"errors"
	"fmt"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
)

// ErrUnknownAddressFormat is returned when a network's namespace has no
// known address format — a registry-invariant violation, never malformed
// client input. Wallet rows must never carry an empty address_type.
var ErrUnknownAddressFormat = errors.New("unknown address format")

// Service wraps wallet persistence (thin; business rules stay out of pgx).
type Service struct {
	q *db.Queries
}

// NewService builds the service over sqlc queries.
func NewService(q *db.Queries) *Service { return &Service{q: q} }

// GetOrCreate returns the wallet row for (network, normalized address),
// creating it on first sight. The original address representation is
// preserved in `address`; address_type stamps the network's resolved
// AddressFormat and fails closed on an unknown namespace.
func (s *Service) GetOrCreate(ctx context.Context, net network.Network, address, normalized string) (db.Wallet, error) {
	format := net.AddressFormat()
	if format == "" {
		return db.Wallet{}, fmt.Errorf("%w: namespace %q", ErrUnknownAddressFormat, net.Namespace)
	}
	return s.q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
		NetworkID:         net.ID,
		Address:           address,
		NormalizedAddress: normalized,
		AddressType:       string(format),
	})
}
