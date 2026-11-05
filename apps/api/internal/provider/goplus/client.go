package goplus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"checkwallet/internal/network"
)

// buildRequest creates the chain-scoped address_security request. chain_id
// is the network's CAIP-2 reference verbatim (eip155 "1"/"56" — the exact
// tokens GoPlus expects).
func (a *Adapter) buildRequest(ctx context.Context, net network.Network,
	normalized string) (*http.Request, error) {

	u, err := url.Parse(a.baseURL +
		"/api/v1/address_security/" + url.PathEscape(normalized))
	if err != nil {
		return nil, fmt.Errorf("goplus: build url: %w", err)
	}
	q := u.Query()
	q.Set("chain_id", net.ChainReference)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("goplus: build request: %w", err)
	}
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	return req, nil
}
