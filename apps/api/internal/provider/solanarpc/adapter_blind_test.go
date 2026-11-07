package solanarpc

// Blind/anonymous saturation suite (pipeline Stage 4): randomized
// fixtures + property assertions that know only the TxProvider contract —
// not the named test fixtures. Seeded per run via math/rand/v2 so every
// iteration exercises a different payload shape.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/provider"
)

// newRawServer serves body verbatim for every request — for malformed-
// payload fixtures the rpcDouble cannot express.
func newRawServer(t *testing.T, body string, hits *int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

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

// rndSig builds a base58-looking signature so fixtures don't share the
// two canned values.
func rndSig(r *rand.Rand, tag string) string {
	const alpha = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	var b strings.Builder
	b.WriteString(tag)
	for b.Len() < 88 {
		b.WriteByte(alpha[r.IntN(len(alpha))])
	}
	return b.String()
}

// A randomized feed over a random window: every emitted transfer
// satisfies the contract — per-stream ascending blocks, declared stream,
// integer amount — and UpstreamCalls equals the requests the double
// actually served. Zero-emission steps are legal mid-collection but
// NEVER come with a finished stream or an emitted top-anchored slice.
func TestBlind_StreamInvariants(t *testing.T) {
	for trial := 0; trial < 6; trial++ {
		r := rand.New(rand.NewPCG(
			blindSeed(0)+uint64(trial*7919+13), uint64(trial*104729+7)))
		n := 1 + r.IntN(12)
		from := int64(1 + r.IntN(20))
		to := from + int64(5+r.IntN(40))
		head := to + int64(r.IntN(20))

		// Random desc feed: sigs scattered over [from-3, to+5].
		var f feed
		txs := map[string]map[string]any{}
		type want struct {
			sig  string
			slot int64
		}
		var expected []want
		for i := 0; i < n; i++ {
			slot := from - 3 + int64(r.IntN(int(to-from)+9))
			if slot < 1 {
				slot = 1
			}
			s := rndSig(r, fmt.Sprintf("b%d", trial)+fmt.Sprintf("s%d", i))
			hasErr := r.IntN(4) == 0
			row := sig(s, slot, int64(r.IntN(64)))
			if hasErr {
				row["err"] = map[string]any{"x": 1}
			}
			f = append(f, row)
			if slot >= from && slot <= to && !hasErr {
				expected = append(expected, want{s, slot})
				txs[s] = tx(slot, i64(1000+slot), []string{walletA, acctB},
					[]map[string]any{systemTransfer(walletA, acctB, int64(1+r.IntN(999)))},
					nil, nil, nil)
			}
		}
		// Sort the feed desc by (slot,idx) — the upstream order contract.
		for i := 0; i < len(f); i++ {
			for j := i + 1; j < len(f); j++ {
				if f[i]["slot"].(int64) < f[j]["slot"].(int64) {
					f[i], f[j] = f[j], f[i]
				}
			}
		}

		d := newRPC(t, baseUpstream(f, txs, 0, head))
		a := d.adapter(t, []string{"solana"},
			WithBounds(1+r.IntN(4), 1+r.IntN(4), 20000, 5, 1+r.IntN(60)),
			WithSigPageSize(1+r.IntN(5)))
		opts := provider.TransferLookupOpts{FromBlock: from, ToBlock: to, HeadBlock: head}
		var all []provider.Transfer
		var page *provider.TransferPage
		for step := 0; step < 40; step++ {
			var err error
			page, err = a.LookupTransfers(context.Background(), solNet, walletA, opts)
			require.NoError(t, err, "trial %d step %d", trial, step)
			all = append(all, page.Transfers...)
			opts.PageToken = page.NextPageToken
			if page.NextPageToken == "" {
				break
			}
		}
		require.NotNil(t, page)

		// Global ascending across steps; declared stream; integer amount.
		declared := map[string]bool{}
		for _, st := range page.Streams {
			declared[st.Name] = true
		}
		last := map[string]int64{}
		for _, tr := range all {
			assert.GreaterOrEqual(t, tr.BlockNumber, last[tr.Stream],
				"trial %d: %s stream order", trial, tr.Stream)
			last[tr.Stream] = tr.BlockNumber
			assert.Regexp(t, `^[0-9]+$`, tr.AmountRaw)
			assert.True(t, declared[tr.Stream], "trial %d undeclared %s", trial, tr.Stream)
			assert.GreaterOrEqual(t, tr.BlockNumber, from)
			assert.LessOrEqual(t, tr.BlockNumber, to)
		}
		// Emitted count equals the expected in-window, non-err sigs.
		assert.Len(t, all, len(expected), "trial %d", trial)
	}
}

// Random garbage page tokens: decode must never panic and must produce a
// typed error — never a silent zero token that would restart the window.
func TestBlind_PageTokenGarbage(t *testing.T) {
	for i, s := range []string{
		"!!!", "AAAA", "aGVsbG8", "eyJtIjoiYWJjIn0", "====", "%%%$",
		strings.Repeat("x", 5000),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":9,"f":1,"t":2}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":"1"}`)),
	} {
		_, err := decodeToken(s)
		var le *provider.LookupError
		require.ErrorAs(t, err, &le, "token[%d]", i)
		assert.Equal(t, provider.ErrInternal, le.Kind, "token[%d]", i)
	}
	// Valid round-trip preserves fields.
	seed := "seed1"
	tok := &pageToken{V: 1, F: 5, T: 20, Floor: 0, FloorSet: true,
		Seed: seed, SeedDone: true, Frontier: "fr1", Bottomed: true,
		Sigs: []sigRef{{Sig: "s1", Slot: 9, Idx: 0}}, Emit: 0}
	got, err := decodeToken(encodeToken(tok))
	require.NoError(t, err)
	assert.Equal(t, int64(5), got.F)
	assert.Equal(t, "seed1", got.Seed)
	assert.Equal(t, "fr1", got.Frontier)
	require.Len(t, got.Sigs, 1)
	assert.Equal(t, "s1", got.Sigs[0].Sig)
}

// Random malformed upstream bodies → typed error or valid page, never a
// panic and never a nil-page-success.
func TestBlind_MalformedNeverPanic(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(42), 1))
	bodies := []string{
		"", "null", "[]", "{}", `{"result":`, `{"error":{"code":-1}}`,
		`{"result":[{"signature":1}]}`,
		`{"result":{"x":` + strings.Repeat("9", 4000),
	}
	for i := 0; i < 12; i++ {
		bodies = append(bodies, string([]byte{
			byte(r.IntN(256)), byte(r.IntN(256)), byte(r.IntN(256)),
		}))
	}
	for i, body := range bodies {
		var hits int64
		srv := newRawServer(t, body, &hits)
		host := strings.TrimPrefix(srv.URL, "http://")
		a, err := New(srv.URL, []string{"solana"},
			WithInsecureHTTP(), WithAllowedHosts(host))
		require.NoError(t, err)
		page, err := a.LookupTransfers(context.Background(), solNet, walletA,
			provider.TransferLookupOpts{FromBlock: 5, ToBlock: 10})
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
		chains := [][]string{
			nil, {"solanax"}, {"ethereum"}, {"solana"}, {"SOLANA"},
		}[r.IntN(5)]
		d := newRPC(t, func(string, json.RawMessage) (any, map[string]any) {
			t.Error("disabled adapter emitted traffic")
			return nil, nil
		})
		a := d.adapter(t, chains)
		if a.Enabled(solNet) {
			continue
		}
		var le *provider.LookupError
		_, err := a.HeadBlock(context.Background(), solNet)
		require.ErrorAs(t, err, &le, "iter %d", i)
		assert.Equal(t, provider.ErrDisabled, le.Kind)
		_, err = a.LookupTransfers(context.Background(), solNet, walletA,
			provider.TransferLookupOpts{FromBlock: 0, ToBlock: 10})
		require.ErrorAs(t, err, &le, "iter %d", i)
		assert.Equal(t, int64(0), atomic.LoadInt64(d.hits), "iter %d", i)
	}
}

// Replay determinism under a randomized fixture: two lookups over the
// same window emit identical transfers (UNIQUE-key safety under replay).
func TestBlind_ReplayIdentity(t *testing.T) {
	r := rand.New(rand.NewPCG(blindSeed(2026), 11))
	var f feed
	txs := map[string]map[string]any{}
	n := 1 + r.IntN(5)
	for i := 0; i < n; i++ {
		slot := int64(11 + n - 1 - i) // feed is DESC by (slot,transactionIndex)
		s := rndSig(r, fmt.Sprintf("d%d", i))
		f = append(f, sig(s, slot, int64(n-1-i)))
		txs[s] = tx(slot, i64(1000+slot), []string{walletA, acctB, mintUSDC},
			[]map[string]any{
				systemTransfer(walletA, acctB, int64(1+r.IntN(99))),
				splChecked(acctB, walletA, mintUSDC, strconv.Itoa(1+r.IntN(999))),
			}, nil, nil, []map[string]any{bal(1, mintUSDC, walletA)})
	}
	handler := baseUpstream(f, txs, 0, 20)
	run := func() []provider.Transfer {
		d := newRPC(t, handler)
		a := d.adapter(t, []string{"solana"})
		page, err := a.LookupTransfers(context.Background(), solNet, walletA,
			provider.TransferLookupOpts{FromBlock: 10, ToBlock: 20, HeadBlock: 20})
		require.NoError(t, err)
		return page.Transfers
	}
	assert.Equal(t, run(), run())
}
