package stablefreeze

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/providerconfig"
)

func res(over func(*providerconfig.Resolved)) *providerconfig.Resolved {
	r := &providerconfig.Resolved{
		SourceCode:    "ankr",
		APIKey:        "key123",
		BaseURL:       "https://rpc.ankr.com/multichain",
		EnabledChains: []string{"ethereum", "bsc"},
		Status:        "active",
	}
	if over != nil {
		over(r)
	}
	return r
}

// The ankr node endpoint is derived exactly like the adapter:
// {base minus /multichain}/eth/{key}.
func TestEndpointFor_Ankr(t *testing.T) {
	ep, hosts, err := endpointFor("ankr", res(nil))
	require.NoError(t, err)
	assert.Equal(t, "https://rpc.ankr.com/eth/key123", ep)
	assert.Equal(t, []string{"rpc.ankr.com"}, hosts)

	// A base without the multichain suffix still derives the node path.
	ep, _, err = endpointFor("ankr", res(func(r *providerconfig.Resolved) {
		r.BaseURL = "https://rpc.ankr.com"
	}))
	require.NoError(t, err)
	assert.Equal(t, "https://rpc.ankr.com/eth/key123", ep)
}

// Fail-closed cases: unregistered code, inactive status, ethereum not
// enabled, missing credentials, unsupported provider — every one errors
// so the caller drops the fetcher and runs fail honestly.
func TestEndpointFor_FailClosed(t *testing.T) {
	_, _, err := endpointFor("ankr", nil)
	require.Error(t, err)

	_, _, err = endpointFor("ankr", res(func(r *providerconfig.Resolved) {
		r.Status = "disabled"
	}))
	require.ErrorContains(t, err, "status")

	_, _, err = endpointFor("ankr", res(func(r *providerconfig.Resolved) {
		r.EnabledChains = []string{"bsc"}
	}))
	require.ErrorContains(t, err, "ethereum disabled")

	_, _, err = endpointFor("ankr", res(func(r *providerconfig.Resolved) {
		r.APIKey = ""
	}))
	require.Error(t, err)

	_, _, err = endpointFor("goplus", res(func(r *providerconfig.Resolved) {
		r.SourceCode = "goplus"
	}))
	require.ErrorContains(t, err, "unsupported")
}
