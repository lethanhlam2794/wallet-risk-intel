package solanarpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var solNet = network.Network{ID: 10, Code: "solana", Namespace: "solana",
	ChainReference: "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"}
var ethNet = network.Network{ID: 1, Code: "ethereum", Namespace: "eip155", ChainReference: "1"}

const (
	walletA  = "9WzDTwDfEXAMPLEwalletaddressAAAAAAAAAAAAAAAAAAAAA"
	acctB    = "3hNzYSQstokenAccountBBBBBBBBBBBBBBBBBBBBBBBBBB"
	ownerC   = "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU"
	mintUSDC = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

// rpcDouble is a scripted Solana JSON-RPC server: one HTTP request per
// POST (single or batch body), responses keyed by id.
type rpcDouble struct {
	srv    *httptest.Server
	hits   *int64 // every HTTP request
	method map[string]*int64
	fn     func(method string, params json.RawMessage) (any, map[string]any)
}

func newRPC(t *testing.T, fn func(method string, params json.RawMessage) (any, map[string]any)) *rpcDouble {
	d := &rpcDouble{hits: new(int64), method: map[string]*int64{}, fn: fn}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(d.hits, 1)
		body, _ := io.ReadAll(r.Body)
		var batch []map[string]any
		single := map[string]any{}
		isBatch := strings.HasPrefix(strings.TrimSpace(string(body)), "[")
		if isBatch {
			require.NoError(t, json.Unmarshal(body, &batch))
		} else {
			require.NoError(t, json.Unmarshal(body, &single))
			batch = []map[string]any{single}
		}
		var resps []any
		for _, c := range batch {
			m, _ := c["method"].(string)
			cnt, ok := d.method[m]
			if !ok {
				cnt = new(int64)
				d.method[m] = cnt
			}
			atomic.AddInt64(cnt, 1)
			params, _ := json.Marshal(c["params"])
			result, rpcErr := d.fn(m, params)
			resp := map[string]any{"jsonrpc": "2.0", "id": c["id"]}
			if rpcErr != nil {
				resp["error"] = rpcErr
			} else {
				resp["result"] = result
			}
			resps = append(resps, resp)
		}
		w.Header().Set("Content-Type", "application/json")
		if isBatch {
			out, _ := json.Marshal(resps)
			_, _ = w.Write(out)
		} else {
			out, _ := json.Marshal(resps[0])
			_, _ = w.Write(out)
		}
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *rpcDouble) calls(method string) int64 {
	if c, ok := d.method[method]; ok {
		return atomic.LoadInt64(c)
	}
	return 0
}

func (d *rpcDouble) adapter(t *testing.T, chains []string, opts ...Option) *Adapter {
	t.Helper()
	host := strings.TrimPrefix(d.srv.URL, "http://")
	all := append([]Option{WithInsecureHTTP(), WithAllowedHosts(host)}, opts...)
	a, err := New(d.srv.URL, chains, all...)
	require.NoError(t, err)
	return a
}

// feed is a desc-ordered getSignaturesForAddress fixture.
type feed []map[string]any

func (f feed) page(before string, limit int) []any {
	start := 0
	if before != "" {
		for i, r := range f {
			if r["signature"] == before {
				start = i + 1 // exclusive — rows strictly below
				break
			}
		}
		// A sig NOT on this feed is a global positional seed (probe §4):
		// it anchors at the head of what it bounds — start stays 0.
	}
	end := start + limit
	if end > len(f) {
		end = len(f)
	}
	out := make([]any, 0, end-start)
	for _, r := range f[start:end] {
		out = append(out, r)
	}
	return out
}

func sig(s string, slot, idx int64) map[string]any {
	return map[string]any{
		"signature": s, "slot": slot, "err": nil,
		"blockTime": 1000 + slot, "confirmationStatus": "finalized",
		"transactionIndex": idx,
	}
}

func sigErr(s string, slot, idx int64) map[string]any {
	m := sig(s, slot, idx)
	m["err"] = map[string]any{"InstructionError": []any{0, "Custom"}}
	return m
}

// ---- getTransaction fixture builders ----

func systemTransfer(src, dst string, lamports int64) map[string]any {
	return map[string]any{
		"program": "system", "programId": "11111111111111111111111111111111",
		"parsed": map[string]any{"type": "transfer", "info": map[string]any{
			"source": src, "destination": dst, "lamports": lamports,
		}},
	}
}

func splChecked(src, dst, mint, amount string) map[string]any {
	return map[string]any{
		"program": "spl-token", "programId": "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
		"parsed": map[string]any{"type": "transferChecked", "info": map[string]any{
			"source": src, "destination": dst, "mint": mint,
			"tokenAmount": map[string]any{"amount": amount},
		}},
	}
}

func splTransfer(src, dst, amount string) map[string]any {
	return map[string]any{
		"program": "spl-token", "programId": "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA",
		"parsed": map[string]any{"type": "transfer", "info": map[string]any{
			"source": src, "destination": dst, "amount": amount,
		}},
	}
}

func noiseIx() map[string]any {
	return map[string]any{
		"program": "compute-budget", "programId": "ComputeBudget111111111111111111111111111111",
		"parsed": map[string]any{"type": "setComputeUnitLimit", "info": map[string]any{"computeUnitLimit": 200000}},
	}
}

// tx wraps instructions in a getTransaction result. accountKeys are the
// jsonParsed object form; bals is preTokenBalances+postTokenBalances.
func tx(slot int64, blockTime *int64, keys []string, ixs []map[string]any,
	inner [][]map[string]any, pre, post []map[string]any) map[string]any {
	ak := make([]map[string]any, len(keys))
	for i, k := range keys {
		ak[i] = map[string]any{"pubkey": k, "signer": i == 0, "writable": true, "source": "transaction"}
	}
	inSets := make([]map[string]any, len(inner))
	for i, set := range inner {
		inSets[i] = map[string]any{"index": i, "instructions": set}
	}
	var bt any = nil
	if blockTime != nil {
		bt = *blockTime
	}
	return map[string]any{
		"slot": slot, "blockTime": bt,
		"meta": map[string]any{
			"err": nil, "fee": 5000,
			"innerInstructions": inSets,
			"preTokenBalances":  pre,
			"postTokenBalances": post,
		},
		"transaction": map[string]any{
			"message": map[string]any{"accountKeys": ak, "instructions": ixs},
		},
	}
}

func bal(idx int, mint, owner string) map[string]any {
	return map[string]any{"accountIndex": idx, "mint": mint, "owner": owner,
		"programId": "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"}
}

func i64(v int64) *int64 { return &v }

// baseUpstream returns a handler serving: getSlot, getFirstAvailableBlock,
// getBlock (no blocks → nil), getSignaturesForAddress over f, and
// getTransaction over txs (sig→fixture; missing → null).
func baseUpstream(f feed, txs map[string]map[string]any, floor, head int64) func(string, json.RawMessage) (any, map[string]any) {
	return func(method string, params json.RawMessage) (any, map[string]any) {
		var p []json.RawMessage
		_ = json.Unmarshal(params, &p)
		switch method {
		case "getSlot":
			return head, nil
		case "getFirstAvailableBlock":
			return floor, nil
		case "getBlock":
			return nil, nil // skipped slot
		case "getSignaturesForAddress":
			var cfg struct {
				Limit  int    `json:"limit"`
				Before string `json:"before"`
			}
			if len(p) > 1 {
				_ = json.Unmarshal(p[1], &cfg)
			}
			if cfg.Limit <= 0 {
				cfg.Limit = 1000
			}
			return f.page(cfg.Before, cfg.Limit), nil
		case "getTransaction":
			var sig string
			_ = json.Unmarshal(p[0], &sig)
			if t, ok := txs[sig]; ok {
				return t, nil
			}
			return nil, nil // null result
		}
		return nil, map[string]any{"code": -32601, "message": "Method not found"}
	}
}

// ---- gating ----

func TestEnabled_Gates(t *testing.T) {
	d := newRPC(t, func(string, json.RawMessage) (any, map[string]any) {
		t.Error("no request may reach the test server")
		return nil, nil
	})
	assert.True(t, d.adapter(t, []string{"solana"}).Enabled(solNet))
	assert.False(t, d.adapter(t, []string{"solana"}).Enabled(ethNet), "non-solana network")
	assert.False(t, d.adapter(t, nil).Enabled(solNet), "no configuration")
	assert.False(t, d.adapter(t, []string{"ethereum"}).Enabled(solNet), "wrong chain")
	assert.False(t, d.adapter(t, []string{"solana"}).Enabled(
		network.Network{Code: "tron"}), "unknown code")
}

func TestDisabled_ZeroTraffic(t *testing.T) {
	d := newRPC(t, func(string, json.RawMessage) (any, map[string]any) {
		t.Error("disabled adapter must not emit HTTP")
		return nil, nil
	})
	a := d.adapter(t, nil)
	var le *provider.LookupError
	_, err := a.HeadBlock(context.Background(), solNet)
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
	_, err = a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 0, ToBlock: 10})
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
	assert.Equal(t, int64(0), atomic.LoadInt64(d.hits))
}

// ---- HeadBlock ----

func TestHeadBlock_Finalized(t *testing.T) {
	d := newRPC(t, func(method string, params json.RawMessage) (any, map[string]any) {
		if method == "getSlot" {
			var p []map[string]any
			_ = json.Unmarshal(params, &p)
			assert.Equal(t, "finalized", p[0]["commitment"], "must pin finalized")
			return int64(466200957), nil
		}
		return nil, nil
	})
	head, err := d.adapter(t, []string{"solana"}).HeadBlock(context.Background(), solNet)
	require.NoError(t, err)
	assert.Equal(t, int64(466200957), head)
	assert.Equal(t, int64(1), atomic.LoadInt64(d.hits))
}

func TestHeadBlock_Malformed(t *testing.T) {
	for _, res := range []any{nil, "x", int64(0), int64(-4), []any{}} {
		d := newRPC(t, func(string, json.RawMessage) (any, map[string]any) { return res, nil })
		a := d.adapter(t, []string{"solana"})
		_, err := a.HeadBlock(context.Background(), solNet)
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "result %v", res)
		assert.Equal(t, provider.ErrMalformed, le.Kind, "result %v", res)
	}
}

// ---- LookupTransfers: collection → emission ----

// Multi-page desc feed + boundary sig below F: emission is ascending and
// covers the window's full sig set.
func TestLookup_DescFeedAscendingEmission(t *testing.T) {
	f := feed{
		sig("sA", 12, 0), sig("sB", 11, 0), sig("sC", 10, 0),
		sig("sD", 9, 0), sig("sX", 3, 0), // sX below F=5 terminates collection
	}
	txs := map[string]map[string]any{
		"sA": tx(12, i64(1012), []string{walletA, acctB}, []map[string]any{systemTransfer(walletA, acctB, 100)}, nil, nil, nil),
		"sB": tx(11, i64(1011), []string{walletA, acctB}, []map[string]any{systemTransfer(acctB, walletA, 200)}, nil, nil, nil),
		"sC": tx(10, i64(1010), []string{walletA, acctB}, []map[string]any{systemTransfer(walletA, acctB, 300)}, nil, nil, nil),
		"sD": tx(9, i64(1009), []string{walletA, acctB}, []map[string]any{systemTransfer(acctB, walletA, 400)}, nil, nil, nil),
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 12))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 12, HeadBlock: 12})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 4)
	assert.Equal(t, []string{"sD", "sC", "sB", "sA"},
		[]string{page.Transfers[0].TxHash, page.Transfers[1].TxHash,
			page.Transfers[2].TxHash, page.Transfers[3].TxHash},
		"desc feed → ascending emission")
	for i, tr := range page.Transfers {
		assert.Equal(t, int64(9+i), tr.BlockNumber)
		assert.Equal(t, streamSOL, tr.Stream)
	}
	assert.Empty(t, page.NextPageToken)
	for _, st := range page.Streams {
		assert.True(t, st.Finished, st.Name)
	}
	// sX (slot 3 < F) never fetched as a tx.
	assert.Equal(t, int64(4), d.calls("getTransaction"))
}

// Same-slot ordering follows transactionIndex, not feed arrival order.
func TestLookup_SameSlotIndexOrder(t *testing.T) {
	f := feed{
		sig("s7", 10, 7), sig("s5", 10, 5), sig("s3", 10, 3), sig("sZ", 2, 0),
	}
	// Distinct destinations per tx to identify emission order.
	txs := map[string]map[string]any{}
	for _, s := range []string{"s3", "s5", "s7"} {
		txs[s] = tx(10, i64(1010), []string{walletA, s + "dst"},
			[]map[string]any{systemTransfer(walletA, s+"dst", 1)}, nil, nil, nil)
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 3)
	assert.Equal(t, "s3dst", page.Transfers[0].To)
	assert.Equal(t, "s5dst", page.Transfers[1].To)
	assert.Equal(t, "s7dst", page.Transfers[2].To)
}

// A multi-page collection emits NOTHING until the frontier passes below F.
func TestLookup_MidCollectionZeroEmission(t *testing.T) {
	f := feed{
		sig("sA", 12, 0), sig("sB", 11, 0), sig("sC", 10, 0), sig("sD", 9, 0),
	}
	txs := map[string]map[string]any{}
	for _, s := range []string{"sA", "sB", "sC", "sD"} {
		txs[s] = tx(0, i64(1000), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil)
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 12))
	a := d.adapter(t, []string{"solana"},
		WithBounds(1, 100, 20000, 5, 50), WithSigPageSize(3)) // 1 page × 3 sigs
	opts := provider.TransferLookupOpts{FromBlock: 5, ToBlock: 12}
	p1, err := a.LookupTransfers(context.Background(), solNet, walletA, opts)
	require.NoError(t, err)
	assert.Empty(t, p1.Transfers, "INV-S1: mid-collection emits zero rows")
	assert.NotEmpty(t, p1.NextPageToken)
	for _, st := range p1.Streams {
		assert.False(t, st.Finished)
	}
	// Page 2 is short ([sD] < 3) ⇒ bottomed ⇒ emission starts.
	opts.PageToken = p1.NextPageToken
	p2, err := a.LookupTransfers(context.Background(), solNet, walletA, opts)
	require.NoError(t, err)
	assert.Len(t, p2.Transfers, 4)
}

// Cross-step emission stays globally ascending.
func TestLookup_CrossStepGlobalAscending(t *testing.T) {
	f := feed{sig("s5", 15, 0), sig("s4", 14, 0), sig("s3", 13, 0),
		sig("s2", 12, 0), sig("s1", 11, 0)}
	txs := map[string]map[string]any{}
	for _, s := range []string{"s1", "s2", "s3", "s4", "s5"} {
		txs[s] = tx(0, i64(1000), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil)
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 15))
	a := d.adapter(t, []string{"solana"}, WithBounds(10, 2, 20000, 5, 50))
	opts := provider.TransferLookupOpts{FromBlock: 10, ToBlock: 15}
	var all []provider.Transfer
	for step := 0; step < 6; step++ {
		page, err := a.LookupTransfers(context.Background(), solNet, walletA, opts)
		require.NoError(t, err, "step %d", step)
		all = append(all, page.Transfers...)
		opts.PageToken = page.NextPageToken
		if page.NextPageToken == "" {
			break
		}
	}
	require.Len(t, all, 5)
	for i := 1; i < len(all); i++ {
		assert.GreaterOrEqual(t, all[i].BlockNumber, all[i-1].BlockNumber,
			"global ascending across steps")
	}
	assert.Equal(t, []string{"s1", "s2", "s3", "s4", "s5"},
		[]string{all[0].TxHash, all[1].TxHash, all[2].TxHash, all[3].TxHash, all[4].TxHash})
}

// Refetch of the same range yields identical transfers (identity basis).
func TestLookup_ReplayDeterminism(t *testing.T) {
	f := feed{sig("sB", 12, 1), sig("sA", 12, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sA": tx(12, i64(1012), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 5)}, nil, nil, nil),
		"sB": tx(12, i64(1012), []string{acctB, walletA, mintUSDC},
			[]map[string]any{splChecked(acctB, walletA, mintUSDC, "77")}, nil, nil,
			[]map[string]any{bal(1, mintUSDC, walletA)}),
	}
	run := func() *provider.TransferPage {
		d := newRPC(t, baseUpstream(f, txs, 0, 12))
		a := d.adapter(t, []string{"solana"})
		page, err := a.LookupTransfers(context.Background(), solNet, walletA,
			provider.TransferLookupOpts{FromBlock: 5, ToBlock: 12, HeadBlock: 12})
		require.NoError(t, err)
		return page
	}
	assert.Equal(t, run().Transfers, run().Transfers)
}

// Kinds filter: unwanted stream exits finished and emits nothing.
func TestLookup_KindsFilter(t *testing.T) {
	f := feed{sig("sT", 10, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sT": tx(10, i64(1010), []string{walletA, acctB, mintUSDC},
			[]map[string]any{
				systemTransfer(walletA, acctB, 9),
				splChecked(acctB, walletA, mintUSDC, "50"),
			}, nil, nil, []map[string]any{bal(1, mintUSDC, walletA)}),
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10,
			Kinds: []string{provider.TransferKindNative}})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, streamSOL, page.Transfers[0].Stream)
	decl := map[string]bool{}
	for _, st := range page.Streams {
		decl[st.Name] = st.Finished
	}
	assert.True(t, decl[streamSPL], "kind-filtered stream exits finished")
	assert.True(t, decl[streamSOL])
}

// Ledger floor above FromBlock ⇒ emit honest data but NEVER finish the
// stream; token ends so the indexer writes partial + history_truncated.
func TestLookup_FloorAboveFromBlocksFinish(t *testing.T) {
	f := feed{sig("sA", 30, 0), sig("sB", 20, 0)}
	txs := map[string]map[string]any{
		"sA": tx(30, i64(1030), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil),
		"sB": tx(20, i64(1020), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 2)}, nil, nil, nil),
	}
	d := newRPC(t, baseUpstream(f, txs, 15 /* floor > F */, 30))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 30})
	require.NoError(t, err)
	assert.Len(t, page.Transfers, 2, "honest data still emitted")
	assert.Empty(t, page.NextPageToken, "token ends → partial")
	for _, st := range page.Streams {
		assert.False(t, st.Finished, "%s must never claim coverage over [F,floor)", st.Name)
	}
}

// Window sig-cap exceeded mid-collection ⇒ zero emission + ended token.
func TestLookup_CollectCapZeroEmission(t *testing.T) {
	f := feed{sig("s4", 14, 0), sig("s3", 13, 0), sig("s2", 12, 0), sig("s1", 11, 0)}
	txs := map[string]map[string]any{}
	for _, s := range []string{"s1", "s2", "s3", "s4"} {
		txs[s] = tx(0, i64(1000), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil)
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 14))
	a := d.adapter(t, []string{"solana"}, WithBounds(10, 100, 2 /* cap */, 5, 50))
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 14})
	require.NoError(t, err)
	assert.Empty(t, page.Transfers, "cap → zero emission, never a top-anchored slice")
	assert.Empty(t, page.NextPageToken)
	for _, st := range page.Streams {
		assert.False(t, st.Finished)
	}
}

// ---- extraction semantics ----

// meta.err txs emit nothing; fee is never a transfer row.
func TestLookup_FailedTxExcludedAndFeeNeverEmitted(t *testing.T) {
	f := feed{sig("sF", 11, 0), sig("sOK", 10, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sOK": tx(10, i64(1010), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 7)}, nil, nil, nil),
	}
	d := newRPC(t, func(method string, params json.RawMessage) (any, map[string]any) {
		if method == "getTransaction" {
			var p []json.RawMessage
			_ = json.Unmarshal(params, &p)
			var s string
			_ = json.Unmarshal(p[0], &s)
			if s == "sF" {
				failed := tx(11, i64(1011), []string{walletA, acctB},
					[]map[string]any{systemTransfer(walletA, acctB, 999)}, nil, nil, nil)
				failed["meta"].(map[string]any)["err"] = map[string]any{"InstructionError": "x"}
				return failed, nil
			}
		}
		return baseUpstream(f, txs, 0, 11)(method, params)
	})
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 11})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, "sOK", page.Transfers[0].TxHash)
	assert.Equal(t, "7", page.Transfers[0].AmountRaw,
		"fee (5000) is never a transfer row")
}

// SPL: mint from transferChecked; owner attribution via tokenBalances;
// unattributed endpoints keep the token account verbatim.
func TestLookup_SPLExtraction(t *testing.T) {
	// keys: 0=walletA(owner), 1=srcATA, 2=dstATA, 3=mint
	f := feed{sig("sT", 10, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sT": tx(10, i64(1010), []string{walletA, "srcATA", "dstATA", mintUSDC},
			[]map[string]any{splChecked("srcATA", "dstATA", mintUSDC, "1234")}, nil, nil,
			[]map[string]any{bal(1, mintUSDC, ownerC), bal(2, mintUSDC, walletA)}),
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	tr := page.Transfers[0]
	assert.Equal(t, streamSPL, tr.Stream)
	assert.Equal(t, provider.TransferKindToken, tr.Type)
	assert.Equal(t, ownerC, tr.From, "owner attribution via postTokenBalances")
	assert.Equal(t, walletA, tr.To)
	assert.Equal(t, mintUSDC, tr.Asset)
	assert.Equal(t, "1234", tr.AmountRaw)
}

// Bare `transfer` resolves mint via tokenBalances; unresolvable mint
// skips the row honestly.
func TestLookup_SPLBareTransferMintResolution(t *testing.T) {
	f := feed{sig("sT", 10, 0), sig("sU", 10, 1), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sT": tx(10, i64(1010), []string{walletA, "srcA", "dstA"},
			[]map[string]any{splTransfer("srcA", "dstA", "55")}, nil, nil,
			[]map[string]any{bal(2, mintUSDC, walletA)}), // dst → mint USDC
		"sU": tx(10, i64(1010), []string{walletA, "srcB", "dstB"},
			[]map[string]any{splTransfer("srcB", "dstB", "66")}, nil, nil, nil), // no balances → skip
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.Equal(t, mintUSDC, page.Transfers[0].Asset)
	assert.Equal(t, "sT", page.Transfers[0].TxHash)
}

// Missing blockTime ⇒ zero BlockTimestamp (NULL downstream), never fabricated.
func TestLookup_NullBlockTime(t *testing.T) {
	f := feed{sig("sN", 10, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sN": tx(10, nil, []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 3)}, nil, nil, nil),
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	require.NoError(t, err)
	require.Len(t, page.Transfers, 1)
	assert.True(t, page.Transfers[0].BlockTimestamp.IsZero())
}

// Malformed upstream bodies ⇒ ErrMalformed.
func TestLookup_Malformed(t *testing.T) {
	for _, body := range []string{
		`not json`, `[{"id":1}`, `{"result":`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		host := strings.TrimPrefix(srv.URL, "http://")
		a, err := New(srv.URL, []string{"solana"},
			WithInsecureHTTP(), WithAllowedHosts(host))
		require.NoError(t, err)
		_, err = a.LookupTransfers(context.Background(), solNet, walletA,
			provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "body %q", body)
		assert.Equal(t, provider.ErrMalformed, le.Kind, "body %q", body)
	}
}

// JSON-RPC element errors map typed: data faults → ErrDegraded, our-bug
// class → ErrInternal.
func TestLookup_RPCErrorTyped(t *testing.T) {
	d := newRPC(t, func(method string, _ json.RawMessage) (any, map[string]any) {
		switch method {
		case "getFirstAvailableBlock":
			return int64(0), nil
		case "getSignaturesForAddress":
			return nil, map[string]any{"code": -32002, "message": "Transaction simulation failed"}
		}
		return nil, nil
	})
	a := d.adapter(t, []string{"solana"})
	_, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
}

// A feed sig whose getTransaction persistently returns null ⇒ typed
// error, NO page, NO token advance (INV-S3).
func TestLookup_NullTxResultNoAdvance(t *testing.T) {
	f := feed{sig("sG", 10, 0), sig("sX", 2, 0)}
	d := newRPC(t, baseUpstream(f, map[string]map[string]any{} /* sG → null */, 0, 10))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
	assert.Nil(t, page)
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
}

// Batch convention: one HTTP request per ≤50-logical-call batch.
func TestLookup_BatchUpstreamCalls(t *testing.T) {
	var f feed
	txs := map[string]map[string]any{}
	for i := 0; i < 60; i++ {
		s := "s" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		slot := int64(11 + (i / 10)) // 6 slots × 10 sigs
		f = append(f, sig(s, slot, int64(i%10)))
		txs[s] = tx(slot, i64(1000+slot), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil)
	}
	// feed must be desc — built asc here, reverse it.
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
	d := newRPC(t, baseUpstream(f, txs, 0, 16))
	a := d.adapter(t, []string{"solana"}, WithBounds(10, 200, 20000, 5, 50))
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 10, ToBlock: 16})
	require.NoError(t, err)
	assert.Len(t, page.Transfers, 60)
	assert.Equal(t, int64(60), d.calls("getTransaction"), "60 logical calls")
	// HTTP: 1 floor + 1 sig page + 2 batches (50+10) = 4.
	assert.Equal(t, int64(4), atomic.LoadInt64(d.hits))
	assert.Equal(t, 4, page.UpstreamCalls, "batch counts as 1 request")
}

// `before` seed: fetchTo < head ⇒ getBlock seed walk anchors collection.
func TestLookup_BeforeSeedWhenToBelowHead(t *testing.T) {
	f := feed{sig("sT", 10, 0), sig("sX", 2, 0)}
	txs := map[string]map[string]any{
		"sT": tx(10, i64(1010), []string{walletA, acctB},
			[]map[string]any{systemTransfer(walletA, acctB, 1)}, nil, nil, nil),
	}
	var sawBefore string
	d := newRPC(t, func(method string, params json.RawMessage) (any, map[string]any) {
		var p []json.RawMessage
		_ = json.Unmarshal(params, &p)
		if method == "getBlock" {
			var slot int64
			_ = json.Unmarshal(p[0], &slot)
			if slot == 11 {
				return map[string]any{"signatures": []string{"seedSIG11"}}, nil
			}
			return nil, nil
		}
		if method == "getSignaturesForAddress" {
			var cfg map[string]any
			_ = json.Unmarshal(p[1], &cfg)
			if b, ok := cfg["before"].(string); ok {
				sawBefore = b
			}
		}
		return baseUpstream(f, txs, 0, 30)(method, params)
	})
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10, HeadBlock: 30})
	require.NoError(t, err)
	assert.Equal(t, "seedSIG11", sawBefore, "collection anchored at block-11 first sig")
	assert.Equal(t, int64(1), d.calls("getBlock"), "first produced slot had sigs")
	assert.Len(t, page.Transfers, 1)
}

// Empty feed + floor ≤ F ⇒ new address ⇒ honest Finished.
func TestLookup_EmptyFeedFinished(t *testing.T) {
	d := newRPC(t, baseUpstream(feed{}, nil, 0, 30))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 30})
	require.NoError(t, err)
	assert.Empty(t, page.Transfers)
	assert.Empty(t, page.NextPageToken)
	for _, st := range page.Streams {
		assert.True(t, st.Finished, st.Name)
	}
}

// Empty feed + floor > F ⇒ indistinguishable new-vs-purged ⇒ unfinished.
func TestLookup_EmptyFeedAboveFloorUnfinished(t *testing.T) {
	d := newRPC(t, baseUpstream(feed{}, nil, 40 /* floor > T? no: >F */, 30))
	a := d.adapter(t, []string{"solana"})
	page, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 5, ToBlock: 30})
	require.NoError(t, err)
	for _, st := range page.Streams {
		assert.False(t, st.Finished, "floor > F can never claim complete")
	}
}

// Inverted window ⇒ ErrInternal before any HTTP.
func TestLookup_InvertedWindow(t *testing.T) {
	d := newRPC(t, func(string, json.RawMessage) (any, map[string]any) {
		t.Error("no traffic")
		return nil, nil
	})
	a := d.adapter(t, []string{"solana"})
	_, err := a.LookupTransfers(context.Background(), solNet, walletA,
		provider.TransferLookupOpts{FromBlock: 20, ToBlock: 10})
	var le *provider.LookupError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, provider.ErrInternal, le.Kind)
	assert.Equal(t, int64(0), atomic.LoadInt64(d.hits))
}
