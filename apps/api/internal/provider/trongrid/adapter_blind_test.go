package trongrid

// Blind/anonymous saturation suite (pipeline Stage 4): randomized
// fixtures + property assertions that know only the TxProvider contract —
// not the named test fixtures. Seeded per run via math/rand/v2 so every
// iteration exercises a different payload shape.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/provider"
)

// blindSeed returns the per-iteration seed: BLIND_SEED env (set by the
// saturation loop) or the fixed default — a failure is reproducible by
// re-running with the same BLIND_SEED.
func blindSeed(def uint64) uint64 {
	if s := os.Getenv("BLIND_SEED"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// rndHexAddr builds a valid "41"+20-byte TRON hex address from a random
// payload so fixtures don't share the two canned addresses.
func rndHexAddr(r *rand.Rand) string {
	b := make([]byte, 20)
	for i := range b {
		b[i] = byte(r.IntN(256))
	}
	return "41" + fmt.Sprintf("%x", b)
}

// rndTxRow builds a native row with n parallel contract/ret elements —
// ctype/ret are drawn randomly per element.
func rndTxRow(r *rand.Rand, txid string, block, ts int64, n int) map[string]any {
	types := []string{"TransferContract", "TriggerSmartContract", "FreezeBalanceV2Contract"}
	rets := []string{"SUCCESS", "OUT_OF_ENERGY", "REVERT"}
	ret := make([]map[string]any, n)
	contract := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		ret[i] = map[string]any{"contractRet": rets[r.IntN(len(rets))]}
		contract[i] = map[string]any{
			"type": types[r.IntN(len(types))],
			"parameter": map[string]any{"value": map[string]any{
				"owner_address": rndHexAddr(r), "to_address": rndHexAddr(r),
				"amount": json.RawMessage(strconv.Itoa(1 + r.IntN(999_999))),
			}},
		}
	}
	return map[string]any{
		"txID": txid, "blockNumber": block, "block_timestamp": ts,
		"ret": ret, "raw_data": map[string]any{"contract": contract},
	}
}

// A randomized page of native+trc20 rows over a random window: every
// emitted transfer satisfies the contract — ascending per stream,
// nonzero block, declared stream, integer amount — and UpstreamCalls
// equals the requests the double actually served (all-200 path).
func TestBlind_StreamInvariants(t *testing.T) {
	for trial := 0; trial < 8; trial++ {
		r := rand.New(rand.NewPCG(
			blindSeed(0)+uint64(trial*7919+13), uint64(trial*104729+7)))
		nTrx := 1 + r.IntN(5)
		nTrc := r.IntN(4)
		from, to := int64(1+r.IntN(50)), int64(0)
		to = from + int64(1+r.IntN(50))
		head := to + int64(10+r.IntN(100))

		var trxRows []map[string]any
		anchorTxids := map[string]bool{}
		anchorBlocks := map[string]int64{}
		for i := 0; i < nTrx; i++ {
			txid := fmt.Sprintf("ntx%02d", i)
			trxRows = append(trxRows, rndTxRow(r, txid,
				from+int64(i), 1000+from+int64(i), 1+r.IntN(3)))
			anchorTxids[strings.ToLower(txid)] = true
			anchorBlocks[strings.ToLower(txid)] = from + int64(i)
		}
		// trc20 rows arrive ordered by block_timestamp — their resolved
		// blocks must be non-decreasing, as real upstream data is. Keep a
		// per-unanchored-txid txinfo block map so monotonicity holds
		// across mixed anchored/txinfo-resolved events.
		var trcRows []map[string]any
		txinfoBlocks := map[string]int64{}
		lastBlock := from
		for i := 0; i < nTrc; i++ {
			// Anchored only when an anchor row exists at block >= lastBlock;
			// otherwise the event must go the txinfo path to keep resolved
			// blocks non-decreasing (real upstream emits rows in order).
			var cands []string
			for id, b := range anchorBlocks {
				if b >= lastBlock {
					cands = append(cands, id)
				}
			}
			var txid string
			if len(cands) > 0 && r.IntN(2) == 0 {
				txid = cands[r.IntN(len(cands))]
				lastBlock = anchorBlocks[txid]
			} else {
				txid = fmt.Sprintf("xtrc%02d", i)
				txinfoBlocks[txid] = lastBlock + int64(r.IntN(3))
				lastBlock = txinfoBlocks[txid]
			}
			trcRows = append(trcRows, trc20Ev(strings.ToUpper(txid),
				1000+from+int64(i), strconv.Itoa(1+r.IntN(9999)), "Transfer"))
		}

		u := newUpstream(t, func(path string, body []byte) (int, string) {
			switch {
			case strings.Contains(path, "getblockbynum"):
				var req map[string]int64
				_ = json.Unmarshal(body, &req)
				return 200, blockJSON(req["num"], 1000+req["num"])
			case strings.Contains(path, "gettransactioninfobyid"):
				var req map[string]string
				_ = json.Unmarshal(body, &req)
				b, ok := txinfoBlocks[strings.ToLower(req["value"])]
				if !ok {
					return 200, `{}`
				}
				return 200, txInfo(b, "SUCCESS")
			case strings.Contains(path, "/transactions/trc20"):
				return 200, acctPage(trcRows, "")
			case strings.Contains(path, "/transactions"):
				return 200, acctPage(trxRows, "")
			}
			return 500, ""
		})
		a := u.adapter(t, "k", []string{"tron"})
		page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: from, ToBlock: to, HeadBlock: head,
				PageSize: r.IntN(5000)})
		require.NoError(t, err, "trial %d", trial)

		// Per-stream ascending; nonzero block; declared stream.
		last := map[string]int64{}
		for _, tr := range page.Transfers {
			assert.Positive(t, tr.BlockNumber, "trial %d: %s", trial, tr.TxHash)
			assert.GreaterOrEqual(t, tr.BlockNumber, last[tr.Stream],
				"trial %d: %s stream order", trial, tr.Stream)
			last[tr.Stream] = tr.BlockNumber
			assert.Regexp(t, `^[0-9]+$`, tr.AmountRaw)
		}
		declared := map[string]bool{}
		for _, st := range page.Streams {
			declared[st.Name] = true
		}
		for _, tr := range page.Transfers {
			assert.True(t, declared[tr.Stream], "trial %d undeclared %s", trial, tr.Stream)
		}
		// Anchored trc20 events resolve to their real anchor block.
		for _, tr := range page.Transfers {
			if tr.Stream == streamTRC20 && anchorTxids[tr.TxHash] {
				assert.Equal(t, anchorBlocks[tr.TxHash], tr.BlockNumber,
					"trial %d anchored block", trial)
			}
		}
		// All-200 path: the budget metric equals real wire requests.
		assert.Equal(t, u.called(""), int64(page.UpstreamCalls),
			"trial %d: UpstreamCalls honest", trial)
	}
}

// Random PageSize draws: every wire `limit` stays within (0,200].
func TestBlind_LimitNeverExceedsCap(t *testing.T) {
	var sent []string
	u := newUpstream(t, func(path string, _ []byte) (int, string) {
		if strings.Contains(path, "/transactions") {
			q, _ := url.ParseQuery(strings.SplitN(path, "?", 2)[1])
			sent = append(sent, q.Get("limit"))
		}
		switch {
		case strings.Contains(path, "getblockbynum"):
			return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(nil, "")
		}
		return 500, ""
	})
	a := u.adapter(t, "k", []string{"tron"})
	for _, sz := range []int{-7, 0, 1, 42, 199, 200, 201, 1000, 4096} {
		_, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20, PageSize: sz})
		require.NoError(t, err, "PageSize=%d", sz)
	}
	require.NotEmpty(t, sent)
	for _, l := range sent {
		n, err := strconv.Atoi(l)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, n, 1)
		assert.LessOrEqual(t, n, upstreamLimitMax, "wire limit %d", n)
	}
}

// Random garbage page tokens: decode must never panic and must produce a
// typed error — never a silent zero token that would restart the window.
func TestBlind_PageTokenGarbage(t *testing.T) {
	for i, s := range []string{
		"!!!", "AAAA", "aGVsbG8", "eyJtIjoiYWJjIn0", "====", "%%%$",
		strings.Repeat("x", 5000),
	} {
		_, err := decodeToken(s)
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "token[%d]", i)
		assert.Equal(t, provider.ErrInternal, le.Kind, "token[%d]", i)
	}
	// Valid round-trip preserves fields.
	fp := "fp1"
	tok := pageToken{MinTs: 111, MaxTs: 222, Trx: &fp}
	got, err := decodeToken(encodeToken(tok))
	require.NoError(t, err)
	assert.Equal(t, int64(111), got.MinTs)
	assert.Equal(t, int64(222), got.MaxTs)
	require.NotNil(t, got.Trx)
	assert.Equal(t, "fp1", *got.Trx)
	assert.Nil(t, got.Trc20)
}

// Random malformed upstream bodies → typed error or valid page, never a
// panic and never a nil-page-success.
func TestBlind_MalformedNeverPanic(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(42), 1))
	bodies := []string{
		"", "null", "[]", "{}", `{"success":`, `{"data":"x"}`,
		`{"success":true,"data":` + strings.Repeat("9", 4000),
		`{"success":true,"data":[{"txID":1}]}`,
	}
	for i := 0; i < 12; i++ {
		bodies = append(bodies, string([]byte{
			byte(r.IntN(256)), byte(r.IntN(256)), byte(r.IntN(256)),
		}))
	}
	for i, body := range bodies {
		u := newUpstream(t, func(path string, _ []byte) (int, string) {
			if strings.Contains(path, "getblockbynum") {
				return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
			}
			return 200, body
		})
		a := u.adapter(t, "k", []string{"tron"})
		page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
		if err == nil {
			require.NotNil(t, page, "body[%d]: success must carry a page", i)
			require.NotNil(t, page.Transfers)
		} else {
			var le *provider.LookupError
			require.ErrorAs(t, err, &le, "body[%d] must be typed", i)
		}
	}
}

// Disabled configurations drawn randomly: zero wire hits, ErrDisabled.
func TestBlind_DisabledNeverHitsUpstream(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(7), 11))
	for i := 0; i < 6; i++ {
		key := []string{"", "k"}[r.IntN(2)]
		chains := [][]string{
			nil, {"tronx"}, {"ethereum"}, {"tron"}, {"TRON"},
		}[r.IntN(5)]
		u := newUpstream(t, func(string, []byte) (int, string) {
			t.Error("disabled adapter emitted traffic")
			return 500, ""
		})
		a := u.adapter(t, key, chains)
		if a.Enabled(tronNet) {
			continue // enabled combos are covered by the happy-path tests
		}
		_, err := a.HeadBlock(context.Background(), tronNet)
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "iter %d", i)
		assert.Equal(t, provider.ErrDisabled, le.Kind)
		_, err = a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 10})
		require.ErrorAs(t, err, &le, "iter %d", i)
		assert.Equal(t, int64(0), u.called(""), "iter %d: zero wire hits", i)
	}
}

// ret[]/contract[] parallelism under random shapes: ANY TransferContract
// past the ret array is malformed; parallel ones feed IsError honestly.
func TestBlind_RetParallelism(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(99), 3))
	for trial := 0; trial < 6; trial++ {
		nContract := 1 + r.IntN(3)
		row := rndTxRow(r, fmt.Sprintf("rp%02d", trial), 15, 1015, nContract)
		if r.IntN(2) == 0 && nContract > 1 {
			row["ret"] = row["ret"].([]map[string]any)[:nContract-1] // short ret
		}
		// Force at least one TransferContract at a random index (others may
		// already carry that type at random — expectation must scan all).
		cs := row["raw_data"].(map[string]any)["contract"].([]map[string]any)
		idx := r.IntN(nContract)
		cs[idx]["type"] = "TransferContract"
		rets := row["ret"].([]map[string]any)

		// Expected outcome: malformed iff ANY TransferContract index
		// outruns ret[]; otherwise every emitted transfer's IsError must
		// equal its parallel ret[i].
		malformed := false
		wantErr := map[int]bool{}
		for i, c := range cs {
			if c["type"] != "TransferContract" {
				continue
			}
			if i >= len(rets) {
				malformed = true
			} else {
				wantErr[i] = rets[i]["contractRet"] != "SUCCESS"
			}
		}

		u := newUpstream(t, func(path string, _ []byte) (int, string) {
			switch {
			case strings.Contains(path, "getblockbynum"):
				return 200, `{"block_header":{"raw_data":{"number":1,"timestamp":1}}}`
			case strings.Contains(path, "/transactions/trc20"):
				return 200, acctPage(nil, "")
			case strings.Contains(path, "/transactions"):
				return 200, acctPage([]map[string]any{row}, "")
			}
			return 500, ""
		})
		a := u.adapter(t, "k", []string{"tron"})
		page, err := a.LookupTransfers(context.Background(), tronNet, addrTo,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 20})
		if malformed {
			var le *provider.LookupError
			require.ErrorAs(t, err, &le, "trial %d: absent ret must be malformed", trial)
			assert.Equal(t, provider.ErrMalformed, le.Kind)
			continue
		}
		require.NoError(t, err, "trial %d", trial)
		assert.Len(t, page.Transfers, len(wantErr), "trial %d", trial)
		// Emitted transfers are in contract order — IsError[i] must match
		// the ret entry of the i-th TransferContract.
		j := 0
		for i := 0; i < nContract; i++ {
			if cs[i]["type"] != "TransferContract" {
				continue
			}
			assert.Equal(t, wantErr[i], page.Transfers[j].IsError,
				"trial %d contract[%d]", trial, i)
			j++
		}
	}
}

// Refetch determinism under a randomized fixture: two lookups over the
// same window emit identical transfers (UNIQUE-key safety under replay).
func TestBlind_ReplayIdentity(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(2026), 11))
	var trxRows []map[string]any
	for i := 0; i < 1+r.IntN(4); i++ {
		trxRows = append(trxRows, rndTxRow(r, fmt.Sprintf("dtx%02d", i),
			11+int64(i), 1011+int64(i), 1+r.IntN(3)))
	}
	handler := func(path string, body []byte) (int, string) {
		switch {
		case strings.Contains(path, "getblockbynum"):
			var req map[string]int64
			_ = json.Unmarshal(body, &req)
			return 200, blockJSON(req["num"], 1000+req["num"])
		case strings.Contains(path, "/transactions/trc20"):
			return 200, acctPage(nil, "")
		case strings.Contains(path, "/transactions"):
			return 200, acctPage(trxRows, "")
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
	require.Equal(t, p1.Transfers, p2.Transfers)
}
