package trongrid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var tronNet = network.Network{ID: 9, Code: "tron", Namespace: "tron", ChainReference: "mainnet"}
var ethNet = network.Network{ID: 1, Code: "ethereum", Namespace: "eip155", ChainReference: "1"}

const (
	hexFrom  = "41deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	hexTo    = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	addrFrom = "TWGd9idELBV3is6rrtC5PQUhudiJYeCr7E"
	addrTo   = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	addrUsdt = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
)

// upstream is a scripted TronGrid double with per-path request counts.
type upstream struct {
	srv     *httptest.Server
	counts  map[string]*int64
	handler func(path string, body []byte) (int, string)
}

func newUpstream(t *testing.T, h func(path string, body []byte) (int, string)) *upstream {
	u := &upstream{counts: map[string]*int64{}, handler: h}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.count(r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		status, payload := u.handler(r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery, body)
		w.Header().Set("Content-Type", "application/json")
		if status == 429 {
			w.Header().Set("Retry-After", "5")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) count(path string) {
	c, ok := u.counts[path]
	if !ok {
		c = new(int64)
		u.counts[path] = c
	}
	atomic.AddInt64(c, 1)
}

func (u *upstream) called(match string) int64 {
	var n int64
	for k, c := range u.counts {
		if strings.Contains(k, match) {
			n += atomic.LoadInt64(c)
		}
	}
	return n
}

func (u *upstream) adapter(t *testing.T, key string, chains []string) *Adapter {
	t.Helper()
	host := strings.TrimPrefix(u.srv.URL, "http://")
	a, err := New(u.srv.URL, key, chains,
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a
}

func blockJSON(num int64, ts int64) string {
	return `{"blockID":"x","block_header":{"raw_data":{"number":` +
		itoa(num) + `,"timestamp":` + itoa(ts) + `}}}`
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// trxRow builds a native-stream row; ctype selects the contract type and
// ret the contractRet of the parallel ret[0] element.
func trxRow(txid string, block, ts int64, ctype, ret, amount string) map[string]any {
	return map[string]any{
		"txID": txid, "blockNumber": block, "block_timestamp": ts,
		"ret": []map[string]any{{"contractRet": ret}},
		"raw_data": map[string]any{"contract": []map[string]any{{
			"type": ctype,
			"parameter": map[string]any{"value": map[string]any{
				"owner_address": hexFrom, "to_address": hexTo,
				"amount": json.RawMessage(amount),
			}},
		}}},
	}
}

func trc20Ev(txid string, ts int64, value, typ string) map[string]any {
	return map[string]any{
		"transaction_id": txid, "from": addrFrom, "to": addrTo,
		"value": value, "type": typ, "block_timestamp": ts,
		"token_info": map[string]any{"address": addrUsdt, "symbol": "USDT",
			"decimals": 6, "name": "Tether"},
	}
}

func acctPage(rows []map[string]any, nextFp string) string {
	meta := ``
	if nextFp != "" {
		meta = `,"meta":{"fingerprint":"` + nextFp + `","links":{"next":"` + nextFp + `"}}`
	}
	b, _ := json.Marshal(rows)
	return `{"success":true,"data":` + string(b) + meta + `}`
}

func txInfo(block int64, result string) string {
	return `{"id":"x","blockNumber":` + itoa(block) +
		`,"receipt":{"result":"` + result + `"}}`
}

// ---- gating ----

func TestEnabled_Gates(t *testing.T) {
	u := newUpstream(t, func(string, []byte) (int, string) {
		t.Error("no request may reach the test server")
		return 500, ""
	})
	keyed := u.adapter(t, "k", []string{"tron"})
	assert.True(t, keyed.Enabled(tronNet))
	assert.False(t, keyed.Enabled(ethNet), "chain not enabled")
	noKey := u.adapter(t, "", []string{"tron"})
	assert.False(t, noKey.Enabled(tronNet), "api key required")
	noChain := u.adapter(t, "k", nil)
	assert.False(t, noChain.Enabled(tronNet))
}

func TestDisabled_ZeroTraffic(t *testing.T) {
	u := newUpstream(t, func(string, []byte) (int, string) {
		t.Error("disabled adapter must not emit HTTP")
		return 500, ""
	})
	a := u.adapter(t, "", []string{"tron"}) // no key → disabled
	_, err := a.HeadBlock(context.Background(), tronNet)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
	_, err = a.LookupTransfers(context.Background(), tronNet, addrFrom,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 10})
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
	assert.Equal(t, int64(0), u.called(""))
}

// ---- HeadBlock ----

func TestHeadBlock_Solidified(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		if strings.Contains(path, "getnowblock") {
			return 200, blockJSON(87765540, 1793970309000)
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	head, err := a.HeadBlock(context.Background(), tronNet)
	require.NoError(t, err)
	assert.Equal(t, int64(87765540), head)
}

func TestHeadBlock_Malformed(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{}`,                               // no block_header
		`{"block_header":{"raw_data":{}}}`, // number absent → 0
		`{"block_header":{"raw_data":{"number":-3}}}`,
	} {
		u := newUpstream(t, func(string, []byte) (int, string) { return 200, body })
		a := u.adapter(t, "k", []string{"tron"})
		_, err := a.HeadBlock(context.Background(), tronNet)
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "body %q", body)
		assert.Equal(t, provider.ErrMalformed, le.Kind, "body %q", body)
	}
}

// ---- LookupTransfers ----

// happy-path two-stream step: native transfer + TRC-20 resolved via the
// anchor map (the event's txid appears in the native page).
func TestLookup_TwoStreams_AnchorResolved(t *testing.T) {
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			return 200, blockJSON(req["num"], 1000+req["num"])
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("AA20", 1005, "5000000", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("aa10", 11, 1011, "TransferContract", "SUCCESS", "1000000"),
				// TRC-20 recipient tx: no TransferContract but carries
				// the block anchor for AA20's event.
				trxRow("AA20", 12, 1012, "TriggerSmartContract", "SUCCESS", "0"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 10, ToBlock: 20, HeadBlock: 100})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2)

	native := page.Transfers[0]
	assert.Equal(t, streamTRX, native.Stream)
	assert.Equal(t, provider.TransferKindNative, native.Type)
	assert.Equal(t, "aa10", native.TxHash)
	assert.Equal(t, addrFrom, native.From, "hex converted to base58")
	assert.Equal(t, addrTo, native.To)
	assert.Equal(t, "1000000", native.AmountRaw)
	assert.Equal(t, int64(11), native.BlockNumber)
	assert.Equal(t, int64(90), native.Confirmations)

	tok := page.Transfers[1]
	assert.Equal(t, streamTRC20, tok.Stream)
	assert.Equal(t, provider.TransferKindToken, tok.Type)
	assert.Equal(t, addrUsdt, tok.Asset)
	assert.Equal(t, "5000000", tok.AmountRaw)
	assert.Equal(t, int64(12), tok.BlockNumber, "anchor-resolved, no txinfo call")
	assert.Equal(t, int64(0), u.called("gettransactioninfobyid"))

	// UpstreamCalls: 2 bridge + 1 trx page + 1 trc20 page = 4.
	assert.Equal(t, 4, page.UpstreamCalls)
	// Both streams finished (no links.next) → empty token.
	assert.Empty(t, page.NextPageToken)
	for _, st := range page.Streams {
		assert.True(t, st.Finished, st.Name)
	}
}

// Window is token-carried: the second step issues ZERO bridge calls.
func TestLookup_TokenCarriesWindow(t *testing.T) {
	fp := "fp1"
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			return 200, blockJSON(req["num"], 1000+req["num"])
		case strings.Contains(path, "/transactions/trc20"):
			if strings.Contains(path, "fingerprint=fp1") {
				return 200, acctPage([]map[string]any{}, "")
			}
			return 200, acctPage([]map[string]any{
				trc20Ev("bb20", 1005, "1", "Transfer"),
			}, fp)
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("bb20", 12, 1012, "TriggerSmartContract", "SUCCESS", "0"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	opts := provider.TransferLookupOpts{FromBlock: 10, ToBlock: 20, HeadBlock: 100}

	page1, err := a.LookupTransfers(context.Background(), tronNet, addrTo, opts)
	require.NoError(t, err)
	assert.NotEmpty(t, page1.NextPageToken, "trc20 has a next page")
	assert.Equal(t, int64(2), u.called("getblockbynum"))

	opts.PageToken = page1.NextPageToken
	page2, err := a.LookupTransfers(context.Background(), tronNet, addrTo, opts)
	require.NoError(t, err)
	assert.Equal(t, int64(2), u.called("getblockbynum"),
		"second step must not re-bridge — window rides the token")
	// trx finished in step 1, so only the trc20 page is fetched: 1 call.
	assert.Equal(t, 1, page2.UpstreamCalls, "trc20 page only, 0 bridge")
	// trc20 finished now (empty page, no next); trx finished in step 1.
	assert.Empty(t, page2.NextPageToken)
}

// Kinds filter drops a stream early: it's declared Finished and never
// fetched.
func TestLookup_KindsFilter(t *testing.T) {
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			return 200, blockJSON(req["num"], 1000+req["num"])
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("aa10", 11, 1011, "TransferContract", "SUCCESS", "5"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20,
			Kinds: []string{provider.TransferKindNative}})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, streamTRX, page.Transfers[0].Stream)
	assert.Equal(t, int64(0), u.called("trc20"), "unwanted stream never fetched")
	var trc20Seen bool
	for _, st := range page.Streams {
		if st.Name == streamTRC20 {
			trc20Seen = true
			assert.True(t, st.Finished, "kind-filtered stream exits finished")
		}
	}
	assert.True(t, trc20Seen, "INV-2: stream always declared")
}

// Ascending within-stream order is asserted on emitted transfers.
func TestLookup_Ascending(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("t1", 10, 1010, "TransferContract", "SUCCESS", "1"),
				trxRow("t2", 12, 1012, "TransferContract", "SUCCESS", "2"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20, HeadBlock: 100})
	require.NoError(t, err)
	for i := 1; i < len(page.Transfers); i++ {
		assert.GreaterOrEqual(t, page.Transfers[i].BlockNumber,
			page.Transfers[i-1].BlockNumber)
	}
}

// An upstream order violation is a contract break — ErrDegraded.
func TestLookup_OrderViolationIsDegraded(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("t1", 12, 1012, "TransferContract", "SUCCESS", "1"),
				trxRow("t2", 10, 1010, "TransferContract", "SUCCESS", "2"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
}

// INV-1: a trc20 row whose txinfo never resolves fails the step — no
// page, no token, cursor held.
func TestLookup_INV1_UnresolvedTrc20(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "gettransactioninfobyid"):
			return 200, `{}` // persistent miss — index lag
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("cc20", 1005, "1", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	assert.Nil(t, page, "no page on unresolved rows")
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
	assert.Equal(t, int64(txInfoRetries), u.called("gettransactioninfobyid"),
		"bounded in-step retry")
}

// txinfo success resolves the row and feeds IsError from receipt.result.
func TestLookup_TxInfoResolves(t *testing.T) {
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "gettransactioninfobyid"):
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			if req["value"] == "dd20" {
				return 200, txInfo(15, "SUCCESS")
			}
			return 200, txInfo(15, "OUT_OF_ENERGY")
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("dd20", 1005, "7", "Transfer"),
				trc20Ev("dd21", 1006, "8", "Transfer"),
				trc20Ev("dd22", 1007, "9", "Approval"), // scope-excluded
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20, HeadBlock: 100})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2, "Approval excluded")
	assert.Equal(t, int64(15), page.Transfers[0].BlockNumber)
	assert.False(t, page.Transfers[0].IsError)
	assert.Equal(t, "dd21", page.Transfers[1].TxHash)
	assert.True(t, page.Transfers[1].IsError, "receipt != SUCCESS")
	assert.Equal(t, int64(2), u.called("gettransactioninfobyid"),
		"one call per unresolved txid")
	// UpstreamCalls: 1 bridge (from=0) + 1 trx + 1 trc20 + 2 txinfo = 5.
	assert.Equal(t, 5, page.UpstreamCalls)
}

// INV-2: every emitted stream name is declared in page.Streams.
func TestLookup_INV2_StreamsDeclared(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("ee20", 1005, "1", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("ee20", 12, 1012, "TriggerSmartContract", "SUCCESS", "0"),
				trxRow("ee21", 13, 1013, "TransferContract", "SUCCESS", "3"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	require.NoError(t, err)
	declared := map[string]bool{}
	for _, st := range page.Streams {
		declared[st.Name] = true
	}
	for _, tr := range page.Transfers {
		assert.True(t, declared[tr.Stream], "undeclared stream %s", tr.Stream)
	}
}

// FromBlock=0 skips its bridge call entirely (getblockbynum(0) is
// degenerate upstream). Bridge failure surfaces typed, not fabricated.
func TestLookup_BridgeBoundary(t *testing.T) {
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			assert.NotZero(t, req["num"], "num=0 must not be bridged")
			return 200, blockJSON(req["num"], 2000)
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), u.called("getblockbynum"), "from=0 → only ToBlock bridged")
	assert.Equal(t, 3, page.UpstreamCalls)
}

func TestLookup_BridgeFailure(t *testing.T) {
	for _, body := range []string{
		`{}`, // block past head
		`{"Error":"x"}`,
		`not json`,
	} {
		u := newUpstream(t, func(string, []byte) (int, string) { return 200, body })
		a := u.adapter(t, "k", []string{"tron"})
		_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: 5, ToBlock: 20})
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "body %q", body)
		assert.Contains(t, []provider.ErrKind{provider.ErrDegraded, provider.ErrMalformed},
			le.Kind, "body %q", body)
	}
}

// Replay determinism: the same window refetched yields identical transfers.
func TestLookup_ReplayDeterminism(t *testing.T) {
	handler := func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			return 200, blockJSON(req["num"], 1000+req["num"])
		case strings.Contains(path, "gettransactioninfobyid"):
			return 200, txInfo(16, "SUCCESS")
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("ff20", 1005, "5", "Transfer"),
				trc20Ev("ff20", 1005, "5", "Transfer"), // duplicate same-content event
				trc20Ev("ff21", 1006, "6", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("ff20", 12, 1012, "TriggerSmartContract", "SUCCESS", "0"),
				trxRow("ff30", 14, 1014, "TransferContract", "SUCCESS", "9"),
			}, "")
		}
		return 500, ""
	}
	u := newUpstream(t, handler)
	a := u.adapter(t, "k", []string{"tron"})
	opts := provider.TransferLookupOpts{FromBlock: 10, ToBlock: 20, HeadBlock: 100}
	p1, err := a.LookupTransfers(context.Background(), tronNet, addrTo, opts)
	require.NoError(t, err)
	p2, err := a.LookupTransfers(context.Background(), tronNet, addrTo, opts)
	require.NoError(t, err)
	require.Equal(t, p1.Transfers, p2.Transfers, "same window → identical set")
	require.Len(t, p1.Transfers, 4, "duplicates emitted — .k dedup is the indexer's job")
	// The two duplicate ff20 events are identical content-wise.
	assert.Equal(t, p1.Transfers[1], p1.Transfers[2])
}

// AmountRaw is always a raw integer decimal — property check over fixtures.
func TestLookup_RawIntegerAmounts(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{
				trxRow("g1", 11, 1011, "TransferContract", "SUCCESS", "12345678901234567890"),
			}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	require.NoError(t, err)
	for _, tr := range page.Transfers {
		assert.Regexp(t, `^[0-9]+$`, tr.AmountRaw, "raw integer decimal only")
	}
	assert.Equal(t, "12345678901234567890", page.Transfers[0].AmountRaw)
}

// No silently-zeroed block numbers: a trc20 row resolved only via
// txinfo's real blockNumber.
func TestLookup_NoZeroedBlocks(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "gettransactioninfobyid"):
			return 200, txInfo(9764845, "SUCCESS")
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("h120", 1005, "1", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, int64(9764845), page.Transfers[0].BlockNumber)
	assert.NotZero(t, page.Transfers[0].BlockNumber)
}

// ---- typed transport errors ----

func TestLookup_429(t *testing.T) {
	u := newUpstream(t, func(string, []byte) (int, string) { return 429, `{}` })
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.HeadBlock(context.Background(), tronNet)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Equal(t, 5*int64(time.Second), int64(le.RetryAfter))
}

func TestLookup_5xx(t *testing.T) {
	u := newUpstream(t, func(string, []byte) (int, string) { return 500, `{}` })
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.HeadBlock(context.Background(), tronNet)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrUpstream5xx, le.Kind)
	assert.Equal(t, int64(3), u.called(""), "1 try + MaxRetries=2")
}

func TestLookup_MalformedJSON(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		if strings.Contains(path, "getblockbynum") {
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		}
		return 200, `{"success":bogus`
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrMalformed, le.Kind)
}

func TestLookup_UnallowlistedHost(t *testing.T) {
	// Construction refuses a host not in the allowlist — before any socket.
	_, err := New("https://evil.example.com", "k", []string{"tron"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

func TestLookup_InvertedWindow(t *testing.T) {
	u := newUpstream(t, func(string, []byte) (int, string) {
		t.Error("inverted window must not issue calls")
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 20, ToBlock: 10})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrInternal, le.Kind)
	assert.Equal(t, int64(0), u.called(""))
}

// PageSize is clamped to the upstream limit cap — the indexer's
// PageSize=1000 must never reach the wire (TronGrid rejects limit>200).
func TestLookup_LimitClamped(t *testing.T) {
	var limits []string
	u := newUpstream(t, func(path string, body []byte) (int, string) {
		if strings.Contains(path, "/transactions") {
			q, _ := url.ParseQuery(strings.SplitN(path, "?", 2)[1])
			limits = append(limits, q.Get("limit"))
		}
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20, PageSize: 1000})
	require.NoError(t, err)
	require.NotEmpty(t, limits)
	for _, l := range limits {
		n, _ := strconv.Atoi(l)
		assert.LessOrEqual(t, n, upstreamLimitMax, "sent limit must be clamped")
	}
}

// A success:false envelope is upstream-degraded, never a clean empty page.
func TestLookup_SuccessFalseEnvelope(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		if strings.Contains(path, "getblockbynum") {
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		}
		return 200, `{"success":false,"Error":"rate limit exceeded","data":[]}`
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
}

// A txinfo Error payload is degraded evidence — the row is never emitted
// with a fabricated block.
func TestLookup_TxInfoErrorPath(t *testing.T) {
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "gettransactioninfobyid"):
			return 200, `{"Error":"tx not found"}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage([]map[string]any{
				trc20Ev("zz20", 1005, "1", "Transfer"),
			}, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	assert.Nil(t, page, "no page — unresolved rows never emit")
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
}

// A multi-TransferContract tx emits one Transfer per contract element.
func TestLookup_MultiContractTx(t *testing.T) {
	multi := map[string]any{
		"txID": "mc01", "blockNumber": 15, "block_timestamp": 1015,
		"ret": []map[string]any{
			{"contractRet": "SUCCESS"}, {"contractRet": "OUT_OF_ENERGY"},
		},
		"raw_data": map[string]any{"contract": []map[string]any{
			{"type": "TransferContract",
				"parameter": map[string]any{"value": map[string]any{
					"owner_address": hexFrom, "to_address": hexTo,
					"amount": json.RawMessage("100")}}},
			{"type": "TransferContract",
				"parameter": map[string]any{"value": map[string]any{
					"owner_address": hexFrom, "to_address": hexTo,
					"amount": json.RawMessage("200")}}},
		}},
	}
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{multi}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 2, "one row per TransferContract element")
	assert.Equal(t, "100", page.Transfers[0].AmountRaw)
	assert.False(t, page.Transfers[0].IsError)
	assert.Equal(t, "200", page.Transfers[1].AmountRaw)
	assert.True(t, page.Transfers[1].IsError, "parallel ret[1] feeds IsError")
}

// A contract element without a parallel ret[] entry is malformed
// upstream evidence — never silently IsError=false.
func TestLookup_RetContractMismatch(t *testing.T) {
	bad := map[string]any{
		"txID": "rm01", "blockNumber": 15, "block_timestamp": 1015,
		"ret": []map[string]any{{"contractRet": "SUCCESS"}}, // 1 ret, 2 contracts
		"raw_data": map[string]any{"contract": []map[string]any{
			{"type": "TransferContract",
				"parameter": map[string]any{"value": map[string]any{
					"owner_address": hexFrom, "to_address": hexTo,
					"amount": json.RawMessage("100")}}},
			{"type": "TransferContract",
				"parameter": map[string]any{"value": map[string]any{
					"owner_address": hexFrom, "to_address": hexTo,
					"amount": json.RawMessage("200")}}},
		}},
	}
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage([]map[string]any{bad}, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrMalformed, le.Kind)
}

// ---- mapper unit checks ----

func TestHexToBase58_KnownPairs(t *testing.T) {
	cases := map[string]string{
		"41a614f803b6fd780986a42c78ec9c7f77e6ded13c": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		"410000000000000000000000000000000000000000": "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb",
		"41deadbeefdeadbeefdeadbeefdeadbeefdeadbeef": "TWGd9idELBV3is6rrtC5PQUhudiJYeCr7E",
	}
	for h, want := range cases {
		got, err := hexToBase58(h)
		require.NoError(t, err, h)
		assert.Equal(t, want, got)
	}
	_, err := hexToBase58("42deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	assert.Error(t, err, "non-41 version byte rejected")
	_, err = hexToBase58("41deadbeef")
	assert.Error(t, err, "wrong length rejected")
}
