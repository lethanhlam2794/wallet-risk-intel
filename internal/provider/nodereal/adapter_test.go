package nodereal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var bscNet = network.Network{ID: 2, Code: "bsc", ChainReference: "56"}
var ethNet = network.Network{ID: 1, Code: "ethereum", ChainReference: "1"}

func direction(req struct {
	Method string `json:"method"`
	Params struct {
		FromAddress string `json:"fromAddress"`
		ToAddress   string `json:"toAddress"`
	} `json:"params"`
}) string {
	if req.Params.FromAddress != "" {
		return "from"
	}
	return "to"
}

// fakeUpstream returns a canned JSON-RPC result per method.
func fakeUpstream(t *testing.T, results map[string]any) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				FromAddress string `json:"fromAddress"`
				ToAddress   string `json:"toAddress"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		res := results[req.Method]
		if m, ok := results[req.Method+":"+direction(req)]; ok {
			res = m
		}
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "result": res})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, "key",
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a
}

// Test 1: nr_getAssetTransfers fixtures → normalized rows; rawContract
// pins the raw amount; receiptsStatus maps is_error; confirmations
// computed head−block+1.
func TestLookupTransfers_Fixtures(t *testing.T) {
	a := fakeUpstream(t, map[string]any{
		"nr_getAssetTransfers:from": map[string]any{
			"transfers": []map[string]any{},
			"pageKey":   "",
		},
		"nr_getAssetTransfers:to": map[string]any{
			"transfers": []map[string]any{
				{
					"blockNum": "0x100", "hash": "0xABC",
					"from":     "0x00000000000000000000000000000000000000a2",
					"to":       "0x00000000000000000000000000000000000000a1",
					"category": "external", "asset": "BNB",
					"blockTimeStamp": "0x65432100",
					"receiptsStatus": "0x1",
				},
				{
					"blockNum": "0x101", "hash": "0xDEF",
					"from":     "0x00000000000000000000000000000000000000a1",
					"to":       "0x00000000000000000000000000000000000000b1",
					"category": "20", "asset": "USDT",
					"rawContract": map[string]string{
						"value":   "0x2a",
						"address": "0x0000000000000000000000000000000000000c01",
					},
					"blockTimeStamp": "1700000000",
					"receiptsStatus": "0x0",
				},
			},
			"pageKey": "",
		},
	})

	page, err := a.LookupTransfers(context.Background(), bscNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 1000, HeadBlock: 300})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2)
	native := page.Transfers[0]
	assert.Equal(t, "0xabc", native.TxHash)
	assert.Equal(t, provider.TransferKindNative, native.Type)
	assert.Equal(t, int64(0x100), native.BlockNumber)
	assert.Equal(t, int64(300-0x100+1), native.Confirmations)
	assert.False(t, native.IsError)
	tok := page.Transfers[1]
	assert.Equal(t, provider.TransferKindToken, tok.Type)
	assert.Equal(t, "42", tok.AmountRaw, "rawContract hex → decimal")
	assert.Equal(t, "0x0000000000000000000000000000000000000c01", tok.Asset)
	assert.True(t, tok.IsError, "receiptsStatus 0x0 ⇒ is_error")
	assert.Equal(t, 2, page.UpstreamCalls, "from + to direction streams")
}

// Test 3/16: no key ⇒ DISABLED, never a crash; non-bsc chains disabled.
func TestEnabled_Disabled(t *testing.T) {
	a, err := New("https://bsc-mainnet.nodereal.io/v1", "")
	require.NoError(t, err)
	assert.False(t, a.Enabled(bscNet), "missing key ⇒ disabled")
	assert.False(t, a.Enabled(ethNet), "bsc-only adapter")

	b, err := New("https://bsc-mainnet.nodereal.io/v1", "k")
	require.NoError(t, err)
	assert.True(t, b.Enabled(bscNet))
	assert.False(t, b.Enabled(ethNet))

	_, err = a.HeadBlock(context.Background(), bscNet)
	require.Error(t, err)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
}

func TestHeadBlock(t *testing.T) {
	a := fakeUpstream(t, map[string]any{
		"eth_blockNumber": "0x1e6",
	})
	head, err := a.HeadBlock(context.Background(), bscNet)
	require.NoError(t, err)
	assert.Equal(t, int64(0x1e6), head)
}
