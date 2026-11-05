package providerconfig

import (
	"sort"

	"checkwallet/internal/config"
)

// Descriptor binds one tx-history provider to its env surface: the env var
// names (for error messages and origin tracking) and the effective env
// values already merged by config.Load (env var set ⇒ env origin; unset ⇒
// the compiled default). KeyEnv is empty for keyless providers
// (blockscout); ChainsEnv is empty where no chains env exists (nodereal).
type Descriptor struct {
	SourceCode string
	KeyEnv     string
	BaseURLEnv string
	ChainsEnv  string

	apiKey func(*config.Config) string
	base   func(*config.Config) string
	chains func(*config.Config) string
}

// descriptors covers every registered tx-history provider (plan D5);
// keyed by data_sources.code. Non-tx providers are a data add, not rework.
var descriptors = map[string]Descriptor{
	"alchemy": {
		SourceCode: "alchemy",
		KeyEnv:     "ALCHEMY_API_KEY", BaseURLEnv: "ALCHEMY_BASE_URL", ChainsEnv: "ALCHEMY_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.AlchemyAPIKey },
		base:   func(c *config.Config) string { return c.AlchemyBaseURL },
		chains: func(c *config.Config) string { return c.AlchemyEnabledChains },
	},
	"ankr": {
		SourceCode: "ankr",
		KeyEnv:     "ANKR_API_KEY", BaseURLEnv: "ANKR_BASE_URL", ChainsEnv: "ANKR_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.AnkrAPIKey },
		base:   func(c *config.Config) string { return c.AnkrBaseURL },
		chains: func(c *config.Config) string { return c.AnkrEnabledChains },
	},
	"bitquery": {
		SourceCode: "bitquery",
		KeyEnv:     "BITQUERY_API_KEY", BaseURLEnv: "BITQUERY_BASE_URL", ChainsEnv: "BITQUERY_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.BitqueryAPIKey },
		base:   func(c *config.Config) string { return c.BitqueryBaseURL },
		chains: func(c *config.Config) string { return c.BitqueryEnabledChains },
	},
	"blockscout": {
		SourceCode: "blockscout",
		BaseURLEnv: "BLOCKSCOUT_BASE_URL", ChainsEnv: "BLOCKSCOUT_ENABLED_CHAINS",
		apiKey: func(*config.Config) string { return "" },
		base:   func(c *config.Config) string { return c.BlockscoutBaseURL },
		chains: func(c *config.Config) string { return c.BlockscoutEnabledChains },
	},
	"chainbase": {
		SourceCode: "chainbase",
		KeyEnv:     "CHAINBASE_API_KEY", BaseURLEnv: "CHAINBASE_BASE_URL", ChainsEnv: "CHAINBASE_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.ChainbaseAPIKey },
		base:   func(c *config.Config) string { return c.ChainbaseBaseURL },
		chains: func(c *config.Config) string { return c.ChainbaseEnabledChains },
	},
	"chainscan": {
		SourceCode: "chainscan",
		KeyEnv:     "ETHERSCAN_API_KEY", BaseURLEnv: "CHAINSCAN_BASE_URL", ChainsEnv: "CHAINSCAN_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.EtherscanAPIKey },
		base:   func(c *config.Config) string { return c.ChainscanBaseURL },
		chains: func(c *config.Config) string { return c.ChainscanEnabledChains },
	},
	"covalent": {
		SourceCode: "covalent",
		KeyEnv:     "COVALENT_API_KEY", BaseURLEnv: "COVALENT_BASE_URL", ChainsEnv: "COVALENT_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.CovalentAPIKey },
		base:   func(c *config.Config) string { return c.CovalentBaseURL },
		chains: func(c *config.Config) string { return c.CovalentEnabledChains },
	},
	"moralis": {
		SourceCode: "moralis",
		KeyEnv:     "MORALIS_API_KEY", BaseURLEnv: "MORALIS_BASE_URL", ChainsEnv: "MORALIS_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.MoralisAPIKey },
		base:   func(c *config.Config) string { return c.MoralisBaseURL },
		chains: func(c *config.Config) string { return c.MoralisEnabledChains },
	},
	"nodereal": {
		SourceCode: "nodereal",
		KeyEnv:     "NODEREAL_API_KEY", BaseURLEnv: "NODEREAL_BASE_URL",
		apiKey: func(c *config.Config) string { return c.NoderealAPIKey },
		base:   func(c *config.Config) string { return c.NoderealBaseURL },
		chains: func(*config.Config) string { return "" },
	},
	"oklink": {
		SourceCode: "oklink",
		KeyEnv:     "OKLINK_API_KEY", BaseURLEnv: "OKLINK_BASE_URL", ChainsEnv: "OKLINK_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.OklinkAPIKey },
		base:   func(c *config.Config) string { return c.OklinkBaseURL },
		chains: func(c *config.Config) string { return c.OklinkEnabledChains },
	},
	"tatum": {
		SourceCode: "tatum",
		KeyEnv:     "TATUM_API_KEY", BaseURLEnv: "TATUM_BASE_URL", ChainsEnv: "TATUM_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.TatumAPIKey },
		base:   func(c *config.Config) string { return c.TatumBaseURL },
		chains: func(c *config.Config) string { return c.TatumEnabledChains },
	},
	"tokenview": {
		SourceCode: "tokenview",
		KeyEnv:     "TOKENVIEW_API_KEY", BaseURLEnv: "TOKENVIEW_BASE_URL", ChainsEnv: "TOKENVIEW_ENABLED_CHAINS",
		apiKey: func(c *config.Config) string { return c.TokenviewAPIKey },
		base:   func(c *config.Config) string { return c.TokenviewBaseURL },
		chains: func(c *config.Config) string { return c.TokenviewEnabledChains },
	},
}

// Codes lists the registered provider codes (sorted) — the admin surface
// and list endpoint iterate this set.
func Codes() []string {
	out := make([]string, 0, len(descriptors))
	for code := range descriptors {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func descriptorFor(code string) *Descriptor {
	d, ok := descriptors[code]
	if !ok {
		return nil
	}
	return &d
}
