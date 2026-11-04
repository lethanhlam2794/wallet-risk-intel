package alchemy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var bscNet = network.Network{ID: 2, Code: "bsc", ChainReference: "56"}
var ethNet = network.Network{ID: 1, Code: "ethereum", ChainReference: "1"}

// fakeUpstream serves canned JSON-RPC results keyed by "method" or
// "method:direction". It parses the REAL wire shape: params is a
// positional array whose element 0 is the assetTransferParams object —
// the same shape alchemy_getAssetTransfers documents. It records the last
// request's params[0] so tests can pin wire-level details (order:"asc",
// withMetadata chain gating, positional array form).
type fakeRPC struct {
	lastParams  atomic.Value // map[string]any — params[0]
	sawPosArray atomic.Bool
}

func fakeUpstream(t *testing.T, results map[string]any) (*Adapter, *fakeRPC) {
	t.Helper()
	fx := &fakeRPC{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string           `json:"method"`
			Params []map[string]any `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.Params) > 0 {
			fx.sawPosArray.Store(true)
			fx.lastParams.Store(req.Params[0])
		}
		res := results[req.Method]
		dir := "to"
		if len(req.Params) > 0 {
			if s, _ := req.Params[0]["fromAddress"].(string); s != "" {
				dir = "from"
			}
		}
		if m, ok := results[req.Method+":"+dir]; ok {
			res = m
		}
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "result": res})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, "key", []string{"ethereum", "bsc"},
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a, fx
}

// Test 1: alchemy_getAssetTransfers fixtures → normalized rows;
// rawContract pins the raw amount; metadata.blockTimestamp maps time;
// confirmations computed head−block+1; receiptsStatus maps is_error.
func TestLookupTransfers_Fixtures(t *testing.T) {
	a, fx := fakeUpstream(t, map[string]any{
		"alchemy_getAssetTransfers:from": map[string]any{
			"transfers": []map[string]any{},
			"pageKey":   "",
		},
		"alchemy_getAssetTransfers:to": map[string]any{
			"transfers": []map[string]any{
				{
					"blockNum": "0x100", "hash": "0xABC",
					"from":  "0x00000000000000000000000000000000000000a2",
					"to":    "0x00000000000000000000000000000000000000a1",
					"value": 0.5, "asset": "ETH", "category": "external",
					"rawContract": map[string]any{
						"value": "0x6f05b59d3b20000", "address": nil, "decimal": "0x12",
					},
					"metadata":       map[string]string{"blockTimestamp": "2023-11-14T22:13:20.000Z"},
					"receiptsStatus": "0x1",
				},
				{
					"blockNum": "0x101", "hash": "0xDEF",
					"from":  "0x00000000000000000000000000000000000000a1",
					"to":    "0x00000000000000000000000000000000000000b1",
					"value": 42, "asset": "USDT", "category": "erc20",
					"rawContract": map[string]any{
						"value": "0x2a", "address": "0x0000000000000000000000000000000000000c01", "decimal": "0x12",
					},
					"metadata":       map[string]string{"blockTimestamp": "2023-11-14T22:13:45.000Z"},
					"receiptsStatus": "0x0",
				},
			},
			"pageKey": "",
		},
	})

	page, err := a.LookupTransfers(context.Background(), ethNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1000, HeadBlock: 300})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2)

	native := page.Transfers[0]
	assert.Equal(t, "0xabc", native.TxHash)
	assert.Equal(t, provider.TransferKindNative, native.Type)
	assert.Equal(t, "to", native.Stream)
	assert.Equal(t, int64(0x100), native.BlockNumber)
	assert.Equal(t, int64(300-0x100+1), native.Confirmations)
	assert.Equal(t, "500000000000000000", native.AmountRaw, "rawContract hex → decimal")
	assert.Equal(t, "", native.Asset, "external transfer has no contract")
	assert.False(t, native.IsError)
	wantTs, _ := time.Parse(time.RFC3339, "2023-11-14T22:13:20.000Z")
	assert.True(t, native.BlockTimestamp.Equal(wantTs), "metadata.blockTimestamp parsed")

	tok := page.Transfers[1]
	assert.Equal(t, provider.TransferKindToken, tok.Type)
	assert.Equal(t, "42", tok.AmountRaw)
	assert.Equal(t, "0x0000000000000000000000000000000000000c01", tok.Asset)
	assert.True(t, tok.IsError, "receiptsStatus 0x0 ⇒ is_error")
	assert.Equal(t, 2, page.UpstreamCalls, "from + to direction streams")
	assert.Empty(t, page.NextPageToken, "both pageKey empty ⇒ done")
	assert.True(t, fx.sawPosArray.Load(), "positional params on the wire")

	p := fx.lastParams.Load().(map[string]any)
	assert.Equal(t, "asc", p["order"], "ascending order pinned upstream")
	assert.Equal(t, true, p["excludeZeroValue"])
	assert.Equal(t, "0x0", p["fromBlock"])
	assert.Equal(t, true, p["withMetadata"], "ethereum documents metadata support")
}

// BSC does not document withMetadata — the param must NOT be sent and a
// metadata-less response leaves BlockTimestamp zero (indexer stores NULL).
func TestLookupTransfers_BSCNoMetadataParam(t *testing.T) {
	a, fx := fakeUpstream(t, map[string]any{
		"alchemy_getAssetTransfers:from": map[string]any{
			"transfers": []map[string]any{}, "pageKey": "",
		},
		"alchemy_getAssetTransfers:to": map[string]any{
			"transfers": []map[string]any{
				{
					"blockNum": "0x64", "hash": "0x111",
					"from":  "0x00000000000000000000000000000000000000a2",
					"to":    "0x00000000000000000000000000000000000000a1",
					"value": 1, "asset": "BNB", "category": "external",
					"rawContract": map[string]any{"value": "0xde0b6b3a7640000", "address": nil},
				},
			},
			"pageKey": "",
		},
	})
	page, err := a.LookupTransfers(context.Background(), bscNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1000})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.True(t, page.Transfers[0].BlockTimestamp.IsZero(),
		"bsc rows carry no documented timestamp — indexer persists NULL")
	p := fx.lastParams.Load().(map[string]any)
	_, sent := p["withMetadata"]
	assert.False(t, sent, "withMetadata must not be sent on bsc (undocumented)")
}

// Kinds filter: NATIVE-only drops token rows; TOKEN-only drops native.
func TestLookupTransfers_KindFilter(t *testing.T) {
	mk := func() map[string]any {
		return map[string]any{
			"alchemy_getAssetTransfers:from": map[string]any{
				"transfers": []map[string]any{}, "pageKey": "",
			},
			"alchemy_getAssetTransfers:to": map[string]any{
				"transfers": []map[string]any{
					{"blockNum": "0x10", "hash": "0x1", "category": "external",
						"from":        "0x00000000000000000000000000000000000000a2",
						"to":          "0x00000000000000000000000000000000000000a1",
						"rawContract": map[string]any{"value": "0x1"}},
					{"blockNum": "0x11", "hash": "0x2", "category": "erc20",
						"from": "0x00000000000000000000000000000000000000a2",
						"to":   "0x00000000000000000000000000000000000000a1",
						"rawContract": map[string]any{
							"value": "0x7", "address": "0x0000000000000000000000000000000000000c01"}},
				},
				"pageKey": "",
			},
		}
	}
	a, _ := fakeUpstream(t, mk())
	page, err := a.LookupTransfers(context.Background(), ethNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{Kinds: []string{provider.TransferKindToken}})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, provider.TransferKindToken, page.Transfers[0].Type)

	a2, _ := fakeUpstream(t, mk())
	page2, err := a2.LookupTransfers(context.Background(), ethNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{Kinds: []string{provider.TransferKindNative}})
	require.NoError(t, err)
	require.Len(t, page2.Transfers, 1)
	assert.Equal(t, provider.TransferKindNative, page2.Transfers[0].Type)
}

// Pagination: an upstream pageKey continues the stream; a second call
// resumes via NextPageToken and finishes when pageKey returns empty.
func TestLookupTransfers_Pagination(t *testing.T) {
	a, _ := fakeUpstream(t, map[string]any{
		"alchemy_getAssetTransfers:from": map[string]any{
			"transfers": []map[string]any{}, "pageKey": "",
		},
		"alchemy_getAssetTransfers:to": map[string]any{
			"transfers": []map[string]any{
				{"blockNum": "0x10", "hash": "0x1", "category": "external",
					"from":        "0x00000000000000000000000000000000000000a2",
					"to":          "0x00000000000000000000000000000000000000a1",
					"rawContract": map[string]any{"value": "0x1"}},
			},
			"pageKey": "cursor-2",
		},
	})
	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000a1"
	page, err := a.LookupTransfers(ctx, ethNet, addr,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, page.NextPageToken)
	for _, ss := range page.Streams {
		if ss.Name == "to" {
			assert.False(t, ss.Finished, "open pageKey ⇒ unfinished")
		}
		if ss.Name == "from" {
			assert.True(t, ss.Finished, "empty pageKey ⇒ finished")
		}
	}

	page2, err := a.LookupTransfers(ctx, ethNet, addr,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 100, PageToken: page.NextPageToken})
	require.NoError(t, err)
	// from already finished: only the to stream calls upstream.
	assert.Equal(t, 1, page2.UpstreamCalls)
}

// RPC error mapping: rate-limit-ish codes/messages → rate_limited; other
// RPC errors → degraded; malformed bodies → malformed.
func TestLookupTransfers_Errors(t *testing.T) {
	errBody := func(code int, msg string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"error": map[string]any{"code": code, "message": msg}})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}
	}
	newAt := func(t *testing.T, h http.HandlerFunc) *Adapter {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		host := strings.TrimPrefix(srv.URL, "http://")
		a, err := New(srv.URL, "k", []string{"ethereum"},
			WithInsecureHTTP(), WithAllowedHosts(host))
		require.NoError(t, err)
		return a
	}
	addr := "0x00000000000000000000000000000000000000a1"

	a := newAt(t, errBody(-32005, "rate limit exceeded"))
	_, err := a.LookupTransfers(context.Background(), ethNet, addr,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)

	a = newAt(t, errBody(-32602, "invalid params"))
	_, err = a.LookupTransfers(context.Background(), ethNet, addr,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1})
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)

	a = newAt(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	})
	_, err = a.LookupTransfers(context.Background(), ethNet, addr,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1})
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrMalformed, le.Kind)
}

// Enabled matrix: key required; unlisted chains disabled; a disabled
// adapter never emits traffic and reports ErrDisabled.
func TestEnabled_Disabled(t *testing.T) {
	a, err := New("", "", []string{"ethereum", "bsc"})
	require.NoError(t, err)
	assert.False(t, a.Enabled(ethNet), "missing key ⇒ disabled")
	assert.False(t, a.Enabled(bscNet))

	b, err := New("", "k", []string{"ethereum"})
	require.NoError(t, err)
	assert.True(t, b.Enabled(ethNet))
	assert.False(t, b.Enabled(bscNet), "bsc not in enabled list")

	_, err = b.HeadBlock(context.Background(), bscNet)
	require.Error(t, err)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
}

// Per-network endpoints: the default map resolves the documented hosts;
// a non-empty base URL is an allowlisted override for every chain.
func TestEndpoints(t *testing.T) {
	a, err := New("", "k", []string{"ethereum", "bsc"})
	require.NoError(t, err)
	assert.Equal(t, "https://eth-mainnet.g.alchemy.com/v2", a.endpoint(ethNet))
	assert.Equal(t, "https://bnb-mainnet.g.alchemy.com/v2", a.endpoint(bscNet))

	b, err := New("https://eth-mainnet.g.alchemy.com/v2", "k", []string{"ethereum"})
	require.NoError(t, err)
	assert.Equal(t, "https://eth-mainnet.g.alchemy.com/v2", b.endpoint(ethNet))

	_, err = New("https://evil.example.com", "k", []string{"ethereum"})
	require.Error(t, err, "off-allowlist base URL rejected")
}

// HeadBlock parses the hex eth_blockNumber result.
func TestHeadBlock(t *testing.T) {
	a, _ := fakeUpstream(t, map[string]any{
		"eth_blockNumber": "0x1e6",
	})
	head, err := a.HeadBlock(context.Background(), ethNet)
	require.NoError(t, err)
	assert.Equal(t, int64(0x1e6), head)
}
