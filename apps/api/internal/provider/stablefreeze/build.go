package stablefreeze

import (
	"fmt"
	"strings"

	"checkwallet/internal/db"
	"checkwallet/internal/providerconfig"
)

// endpointFor derives the JSON-RPC endpoint + SSRF allowlist for a
// resolved provider. Only providers that expose raw Ethereum JSON-RPC are
// eligible; anything else is a hard error — the caller fails closed.
//
// Ankr: node API lives at {base minus /multichain}/{chain}/{key}
// (mirrors internal/provider/ankr/adapter.go — the multichain base is
// Query-API-only).
func endpointFor(code string, res *providerconfig.Resolved) (endpoint string, hosts []string, err error) {
	if res == nil {
		return "", nil, fmt.Errorf("stablefreeze: provider %q not registered", code)
	}
	if res.Status != "active" {
		return "", nil, fmt.Errorf("stablefreeze: provider %q status %q", code, res.Status)
	}
	enabled := false
	for _, c := range res.EnabledChains {
		if strings.EqualFold(strings.TrimSpace(c), "ethereum") {
			enabled = true
		}
	}
	if !enabled {
		return "", nil, fmt.Errorf("stablefreeze: provider %q has ethereum disabled", code)
	}
	switch code {
	case "ankr":
		if res.APIKey == "" || res.BaseURL == "" {
			return "", nil, fmt.Errorf("stablefreeze: ankr api key/base url required")
		}
		// Fixed allowlist — same host the ankr adapter pins; spelled out
		// here because worker binaries don't link the ankr adapter's
		// init-time host registration.
		return strings.TrimSuffix(res.BaseURL, "/multichain") + "/eth/" +
			res.APIKey, []string{"rpc.ankr.com"}, nil
	default:
		return "", nil, fmt.Errorf("stablefreeze: rpc provider %q unsupported", code)
	}
}

// BuildScanner constructs a SnapshotSource over the resolved RPC
// provider's config. A nil resolver, inactive/disabled provider, or
// missing credentials is an error — the caller drops the fetcher so the
// run fails closed ("no downloader configured") rather than fabricating
// an empty artifact.
func BuildScanner(q *db.Queries, res *providerconfig.Resolver,
	rpcProvider string, cfg Config) (*Scanner, error) {

	if res == nil {
		return nil, fmt.Errorf("stablefreeze: provider resolver required")
	}
	r := res.Get(rpcProvider)
	endpoint, hosts, err := endpointFor(rpcProvider, r)
	if err != nil {
		return nil, err
	}
	cli, err := NewRPCClient(endpoint, hosts)
	if err != nil {
		return nil, err
	}
	return NewScanner(NewCursorStore(q), cli, cfg)
}
