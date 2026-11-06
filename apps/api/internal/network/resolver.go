// Package network manages the supported-chain registry (eip155 namespace).
package network

import (
	"context"
	"fmt"
	"strings"
)

// Network is a supported blockchain. Its address encoding family is
// derived from Namespace via AddressFormat() — never stored separately.
type Network struct {
	ID             int64
	Namespace      string
	ChainReference string
	Code           string
	Name           string
}

// CAIP2 returns the CAIP-2 chain identifier (e.g. "eip155:1").
func (n Network) CAIP2() string { return n.Namespace + ":" + n.ChainReference }

// Resolver maps public network codes to registry rows.
type Resolver struct {
	byCode map[string]Network
	list   []Network
}

// NewResolver builds a resolver from a loaded network list.
func NewResolver(networks []Network) *Resolver {
	r := &Resolver{byCode: make(map[string]Network, len(networks))}
	for _, n := range networks {
		r.byCode[strings.ToLower(n.Code)] = n
		r.list = append(r.list, n)
	}
	return r
}

// Resolve returns the network for a code (case-insensitive).
func (r *Resolver) Resolve(code string) (Network, bool) {
	n, ok := r.byCode[strings.ToLower(strings.TrimSpace(code))]
	return n, ok
}

// List returns all registered networks.
func (r *Resolver) List() []Network { return r.list }

// NetworkLister abstracts the DB so the resolver is unit-testable.
type NetworkLister interface {
	ListActiveNetworks(ctx context.Context) ([]NetworkRow, error)
}

// NetworkRow mirrors the persistence row without leaking the db package type.
type NetworkRow struct {
	ID             int64
	Namespace      string
	ChainReference string
	Code           string
	Name           string
}

// LoadResolver fetches active networks and builds the resolver.
func LoadResolver(ctx context.Context, lister NetworkLister) (*Resolver, error) {
	rows, err := lister.ListActiveNetworks(ctx)
	if err != nil {
		return nil, fmt.Errorf("network: load: %w", err)
	}
	nets := make([]Network, 0, len(rows))
	for _, r := range rows {
		nets = append(nets, Network{
			ID: r.ID, Namespace: r.Namespace, ChainReference: r.ChainReference,
			Code: r.Code, Name: r.Name,
		})
	}
	return NewResolver(nets), nil
}
