// Package probetest builds a throwaway tx-provider adapter from the
// stored provider_configs row and exercises it against the live upstream
// — the backend behind POST /admin/provider-configs/{code}/test. It
// answers "does the saved api_key/base_url actually work" without
// touching the boot-time adapter registry (requires_restart unchanged).
package probetest

import (
	"context"
	"fmt"
	"time"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/alchemy"
	"checkwallet/internal/provider/ankr"
	"checkwallet/internal/provider/bitquery"
	"checkwallet/internal/provider/blockscout"
	"checkwallet/internal/provider/blockscoutpro"
	"checkwallet/internal/provider/chainbase"
	"checkwallet/internal/provider/chainscan"
	"checkwallet/internal/provider/covalent"
	"checkwallet/internal/provider/moralis"
	"checkwallet/internal/provider/nodereal"
	"checkwallet/internal/provider/oklink"
	"checkwallet/internal/provider/tatum"
	"checkwallet/internal/provider/tokenview"
	"checkwallet/internal/providerconfig"
)

// Result is the honest probe outcome on the wire — provider failure is a
// status field, never an HTTP 500 (spec: failure ≠ clean result).
type Result struct {
	OK            bool   `json:"ok"`
	EnabledForNet bool   `json:"enabled_for_network"`
	HTTPStatus    int    `json:"http_status"`
	LatencyMS     int64  `json:"latency_ms"`
	HeadBlock     *int64 `json:"head_block"`
	TransfersSeen int    `json:"transfers_seen"`
	ErrorKind     string `json:"error_kind,omitempty"`
	Message       string `json:"message,omitempty"`
}

// Service resolves stored config and runs the probe.
type Service struct {
	cfg  *config.Config
	q    *db.Queries
	keys *providerconfig.Keystore
}

func NewService(cfg *config.Config, q *db.Queries, keys *providerconfig.Keystore) *Service {
	return &Service{cfg: cfg, q: q, keys: keys}
}

// adapterFor mirrors cmd/api's buildTransactions construction — the same
// provider, the same resolved values, minus metrics hooks.
func adapterFor(code string, r *providerconfig.Resolved) (provider.TxProvider, error) {
	switch code {
	case chainscan.SourceCode:
		return chainscan.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case nodereal.SourceCode:
		return nodereal.New(r.BaseURL, r.APIKey)
	case alchemy.SourceCode, alchemy.SourceCodeEth, alchemy.SourceCodeBsc:
		return alchemy.New(r.BaseURL, r.APIKey, r.EnabledChains,
			alchemy.WithSourceCode(code))
	case blockscout.SourceCode:
		return blockscout.New(r.BaseURL, r.EnabledChains)
	case blockscoutpro.SourceCode:
		return blockscoutpro.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case ankr.SourceCode:
		return ankr.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case covalent.SourceCode:
		return covalent.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case moralis.SourceCode:
		return moralis.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case tatum.SourceCode:
		return tatum.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case bitquery.SourceCode:
		return bitquery.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case oklink.SourceCode:
		return oklink.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case tokenview.SourceCode:
		return tokenview.New(r.BaseURL, r.APIKey, r.EnabledChains)
	case chainbase.SourceCode:
		return chainbase.New(r.BaseURL, r.APIKey, r.EnabledChains)
	}
	return nil, fmt.Errorf("unregistered provider code %q", code)
}

// Probe resolves the stored config for code and runs a bounded live
// upstream call: HeadBlock, then a tiny LookupTransfers window for the
// supplied address. The caller owns the ctx deadline.
func (s *Service) Probe(ctx context.Context, code string,
	net network.Network, normalized string) (*Result, error) {

	res, err := providerconfig.ResolveStored(ctx, s.cfg, s.q, s.keys, code)
	if err != nil {
		return nil, err
	}
	out := &Result{}
	adapter, err := adapterFor(code, res)
	if err != nil {
		return nil, err
	}

	out.EnabledForNet = adapter.Enabled(net)
	if !out.EnabledForNet {
		out.ErrorKind = string(provider.ErrDisabled)
		out.Message = "provider disabled for this network (missing key or chain not enabled)"
		return out, nil
	}

	start := time.Now()
	head, err := adapter.HeadBlock(ctx, net)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fail(out, err)
	}
	out.HeadBlock = &head

	page, err := adapter.LookupTransfers(ctx, net, normalized,
		provider.TransferLookupOpts{
			FromBlock: max(head-512, 0),
			ToBlock:   head,
			PageSize:  5,
			HeadBlock: head,
		})
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return fail(out, err)
	}
	out.TransfersSeen = len(page.Transfers)
	out.OK = true
	return out, nil
}

// fail maps a provider LookupError to an honest Result — upstream status,
// kind, and the provider's own message surface verbatim.
func fail(out *Result, err error) (*Result, error) {
	out.ErrorKind = string(provider.ErrInternal)
	out.Message = err.Error()
	if le, ok := err.(*provider.LookupError); ok {
		out.ErrorKind = string(le.Kind)
		out.HTTPStatus = le.HTTPStatus
	}
	return out, nil
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
