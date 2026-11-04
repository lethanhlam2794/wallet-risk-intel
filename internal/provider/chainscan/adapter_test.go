package chainscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var ethNet = network.Network{ID: 1, Code: "ethereum", ChainReference: "1"}
var bscNet = network.Network{ID: 2, Code: "bsc", ChainReference: "56"}

// fakeUpstream serves canned Etherscan V2 envelopes keyed by action.
func fakeUpstream(t *testing.T, handlers map[string]string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		body, ok := handlers[action]
		if !ok {
			body = `{"status":"0","message":"unexpected","result":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, "key", []string{"ethereum"},
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a
}

func txlistRow(hash, from, to, value, isErr string) map[string]string {
	return map[string]string{
		"blockNumber": "19000000", "timeStamp": "1700000000",
		"hash": hash, "from": from, "to": to, "value": value,
		"isError": isErr, "confirmations": "10",
	}
}

// Test 1: txlist/tokentx fixtures → normalized transfers; zero-value
// txlist rows are scope-excluded; token rows carry the contract.
func TestLookupTransfers_Fixtures(t *testing.T) {
	rows, _ := json.Marshal([]map[string]string{
		txlistRow("0xAAA", "0x00000000000000000000000000000000000000a2",
			"0x00000000000000000000000000000000000000a1", "1000", "0"),
		txlistRow("0xBBB", "0x00000000000000000000000000000000000000a2",
			"0x00000000000000000000000000000000000000a1", "0", "0"), // zero-value → out
	})
	tokens, _ := json.Marshal([]map[string]string{{
		"blockNumber": "19000001", "timeStamp": "1700000012",
		"hash": "0xCCC", "from": "0x00000000000000000000000000000000000000a1",
		"to":    "0x00000000000000000000000000000000000000b1",
		"value": "42", "contractAddress": "0x0000000000000000000000000000000000000c01",
		"confirmations": "8",
	}})
	a := fakeUpstream(t, map[string]string{
		"txlist":  `{"status":"1","message":"OK","result":` + string(rows) + `}`,
		"tokentx": `{"status":"1","message":"OK","result":` + string(tokens) + `}`,
	})

	page, err := a.LookupTransfers(context.Background(), ethNet,
		"0x00000000000000000000000000000000000000a1",
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20000000})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2)
	native := page.Transfers[0]
	assert.Equal(t, "0xaaa", native.TxHash, "tx_hash lowercased")
	assert.Equal(t, provider.TransferKindNative, native.Type)
	assert.Equal(t, "1000", native.AmountRaw)
	assert.Equal(t, int64(19000000), native.BlockNumber)
	tok := page.Transfers[1]
	assert.Equal(t, provider.TransferKindToken, tok.Type)
	assert.Equal(t, "0x0000000000000000000000000000000000000c01", tok.Asset)
	assert.False(t, tok.IsError, "tokentx has no isError → false")
	assert.Equal(t, 2, page.UpstreamCalls)
	assert.Empty(t, page.NextPageToken)
}

// Test 3/16: Enabled matrix — key required; bsc off unless configured.
func TestEnabled_Matrix(t *testing.T) {
	a, err := New("https://api.etherscan.io/v2/api", "", []string{"ethereum"})
	require.NoError(t, err)
	assert.False(t, a.Enabled(ethNet), "no key ⇒ disabled")
	a2, err := New("https://api.etherscan.io/v2/api", "k", []string{"ethereum"})
	require.NoError(t, err)
	assert.True(t, a2.Enabled(ethNet))
	assert.False(t, a2.Enabled(bscNet), "bsc disabled by default")
	a3, err := New("https://api.etherscan.io/v2/api", "k", []string{"ethereum", "bsc"})
	require.NoError(t, err)
	assert.True(t, a3.Enabled(bscNet), "config override enables bsc")

	_, err = a2.HeadBlock(context.Background(), bscNet)
	require.Error(t, err)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
}

// HeadBlock parses the hex eth_blockNumber result.
func TestHeadBlock(t *testing.T) {
	a := fakeUpstream(t, map[string]string{
		"eth_blockNumber": `{"jsonrpc":"2.0","id":1,"result":"0x12d687"}`,
	})
	head, err := a.HeadBlock(context.Background(), ethNet)
	require.NoError(t, err)
	assert.Equal(t, int64(0x12d687), head)
}

// Review M5 regression: upstream caps retrievable records at 10k — a
// full page at the cap is NOT stream end; the stream stays unfinished
// so coverage stops at its own boundary.
func TestLookupTransfers_RecordCapNotFinished(t *testing.T) {
	const size = 1000
	var bigRows []map[string]string
	for i := 0; i < size; i++ {
		bigRows = append(bigRows, txlistRow("0xcap"+strings.Repeat("0", 6)+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"0x00000000000000000000000000000000000000a1",
			"0x00000000000000000000000000000000000000a2", "1", "0"))
	}
	big, _ := json.Marshal(bigRows)
	a := fakeUpstream(t, map[string]string{
		"txlist":  `{"status":"1","message":"OK","result":` + string(big) + `}`,
		"tokentx": `{"status":"1","message":"OK","result":[]}`,
	})

	ctx := context.Background()
	addr := "0x00000000000000000000000000000000000000a1"
	token := ""
	var last *provider.TransferPage
	for i := 0; i < 11; i++ {
		page, err := a.LookupTransfers(ctx, ethNet, addr,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20000000,
				PageToken: token, PageSize: size})
		require.NoError(t, err)
		last = page
		token = page.NextPageToken
		if token == "" {
			break
		}
	}
	require.Empty(t, token, "cap drops the cursor — no infinite paging")
	for _, ss := range last.Streams {
		if ss.Name == streamTxlist {
			assert.False(t, ss.Finished,
				"capped stream must report unfinished")
		}
	}
}
