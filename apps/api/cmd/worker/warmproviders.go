package main

// Warm-index provider lane construction (warm-index-queue v1, plan D1).
// Each allowlisted provider gets its own transaction.Service whose
// adapter map is a single-member chain per resolver network, so a drain
// can only ever spend the selected provider's budget — the interactive
// lane's failover chains and shared pool are structurally untouchable.
// This factory duplicates cmd/api's constructor wiring on purpose (the
// plan's accepted trade-off) rather than refactoring the API path.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

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
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
	"checkwallet/internal/warmindex"
)

// warmLaneBudget mirrors the cmd/api Budget map entry for one code —
// a lane's request pool is exactly its own TXINDEX_BUDGET_* knob.
func warmLaneBudget(cfg *config.Config, code string) int {
	switch code {
	case chainscan.SourceCode:
		return cfg.IndexingBudgetETH
	case nodereal.SourceCode:
		return cfg.IndexingBudgetBSC
	case alchemy.SourceCode:
		return cfg.IndexingBudgetAlchemy
	case blockscout.SourceCode:
		return cfg.IndexingBudgetBlockscout
	case blockscoutpro.SourceCode:
		return cfg.IndexingBudgetBlockscoutPro
	case ankr.SourceCode:
		return cfg.IndexingBudgetAnkr
	case covalent.SourceCode:
		return cfg.IndexingBudgetCovalent
	case moralis.SourceCode:
		return cfg.IndexingBudgetMoralis
	case tatum.SourceCode:
		return cfg.IndexingBudgetTatum
	case bitquery.SourceCode:
		return cfg.IndexingBudgetBitquery
	case oklink.SourceCode:
		return cfg.IndexingBudgetOklink
	case tokenview.SourceCode:
		return cfg.IndexingBudgetTokenview
	case chainbase.SourceCode:
		return cfg.IndexingBudgetChainbase
	}
	return 0
}

// buildWarmAdapter constructs one tx provider for an allowlist code with
// the same ctor args cmd/api uses — minus metrics hooks, which the warm
// worker does not expose in v1. Unknown codes name themselves and fail.
func buildWarmAdapter(code string,
	res *providerconfig.Resolved) (provider.TxProvider, error) {

	switch code {
	case chainscan.SourceCode:
		return chainscan.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case nodereal.SourceCode:
		return nodereal.New(res.BaseURL, res.APIKey)
	case alchemy.SourceCode:
		return alchemy.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case blockscout.SourceCode:
		return blockscout.New(res.BaseURL, res.EnabledChains)
	case blockscoutpro.SourceCode:
		return blockscoutpro.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case ankr.SourceCode:
		return ankr.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case covalent.SourceCode:
		return covalent.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case moralis.SourceCode:
		return moralis.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case tatum.SourceCode:
		return tatum.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case bitquery.SourceCode:
		return bitquery.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case oklink.SourceCode:
		return oklink.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case tokenview.SourceCode:
		return tokenview.New(res.BaseURL, res.APIKey, res.EnabledChains)
	case chainbase.SourceCode:
		return chainbase.New(res.BaseURL, res.APIKey, res.EnabledChains)
	default:
		return nil, fmt.Errorf("warm index: unknown provider code %q", code)
	}
}

// buildWarmLanes constructs one ProviderLane per allowlist code in
// allowlist order. Boot checks mirror cmd/api verbatim: every lane's
// adapter must resolve a data_sources row (a typo'd Name() would
// silently defeat the provider-switch reset) and must carry a positive
// pool budget (a typo'd TXINDEX_BUDGET_* env would leave the lane's
// request pool unlimited).
func buildWarmLanes(ctx context.Context, cfg *config.Config,
	pool *pgxpool.Pool, q *db.Queries, resolver *network.Resolver,
	wallets *wallet.Service, pcRes *providerconfig.Resolver,
	checker transaction.StatusChecker, codes []string,
	log *slog.Logger) ([]warmindex.ProviderLane, error) {

	lanes := make([]warmindex.ProviderLane, 0, len(codes))
	for _, code := range codes {
		res := pcRes.Get(code)
		if res == nil {
			return nil, fmt.Errorf(
				"warm index: provider %q has no resolved config", code)
		}
		adapter, err := buildWarmAdapter(code, res)
		if err != nil {
			return nil, err
		}
		if _, err := q.GetSourceByCode(ctx, adapter.Name()); err != nil {
			return nil, fmt.Errorf("warm index provider %s: "+
				"data_sources row missing: %w", adapter.Name(), err)
		}
		budget := warmLaneBudget(cfg, adapter.Name())
		if budget <= 0 {
			return nil, fmt.Errorf("warm index provider %s: "+
				"budget must be > 0", adapter.Name())
		}
		// Single-member chain per network — failover can never spend
		// a non-selected provider's quota (D1).
		adapters := map[string][]provider.TxProvider{}
		for _, net := range resolver.List() {
			adapters[net.Code] = []provider.TxProvider{adapter}
		}
		txSvc := transaction.New(pool, q, adapters, wallets,
			transaction.Config{
				FreshTTL:     cfg.IndexingFreshTTL,
				StaleAfter:   cfg.IndexingStaleAfter,
				NegativeTTL:  cfg.EnrichmentNegativeTTL,
				PageSize:     cfg.IndexingPageSize,
				MaxPagesHop0: cfg.IndexingMaxPagesHop0,
				MaxPagesHopN: cfg.IndexingMaxPagesHopN,
				MaxPagesHop3: cfg.IndexingMaxPagesHop3,
				ReorgTail: map[string]int64{
					"ethereum": cfg.IndexingReorgTailETH,
					"bsc":      cfg.IndexingReorgTailBSC,
				},
				WindowCap: map[string]int64{"bsc": cfg.IndexingWindowCapBSC},
				Budget:    map[string]int{adapter.Name(): budget},
			}, log)
		txSvc.SetStatusChecker(checker)
		lanes = append(lanes, warmindex.ProviderLane{
			Code:    adapter.Name(),
			Adapter: adapter,
			Tx:      txSvc,
		})
	}
	return lanes, nil
}
