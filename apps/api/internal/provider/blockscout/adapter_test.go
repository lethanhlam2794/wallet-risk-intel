package blockscout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var eth = network.Network{Code: "ethereum"}
var bsc = network.Network{Code: "bsc"}

// srv is a fake Blockscout v2: it records the real request shape (method,
// path, query) and serves canned pages per (path, cursor-presence).
type srv struct {
	*httptest.Server
	requests []string
	native   map[string]listResponse // "p1","p2",...
	token    map[string]listResponse
	head     int64
	err      int // force status on all list calls
}

func txItem(block int64, hash, val string) listItem {
	it := listItem{BlockNumber: block, Hash: hash, Value: val,
		Status: "ok", Timestamp: "2026-11-04T00:00:00Z"}
	it.From.Hash = "0xaaa"
	it.To = &struct {
		Hash string `json:"hash"`
	}{Hash: "0xbbb"}
	return it
}

func ttItem(block int64, hash string, idx int, val string) listItem {
	it := listItem{BlockNumber: block, TransactionHash: hash, LogIndex: idx,
		Timestamp: "2026-11-04T00:00:00Z"}
	it.From.Hash = "0xccc"
	it.To = &struct {
		Hash string `json:"hash"`
	}{Hash: "0xddd"}
	it.Token.AddressHash = "0xTOKEN"
	it.Total.Value = val
	return it
}

func npp(block int64) map[string]any {
	return map[string]any{"block_number": block, "index": 1, "items_count": 50}
}

func newSrv(t *testing.T, native, token map[string]listResponse) *srv {
	s := &srv{native: native, token: token, head: 999}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if s.err != 0 {
			w.WriteHeader(s.err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/blocks":
			json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"height": s.head}}})
		case strings.HasSuffix(r.URL.Path, "/transactions"):
			page := "p1"
			if r.URL.Query().Get("block_number") != "" {
				page = "p2"
			}
			json.NewEncoder(w).Encode(s.native[page])
		case strings.HasSuffix(r.URL.Path, "/token-transfers"):
			if r.URL.Query().Get("type") != "ERC-20" {
				t.Errorf("token-transfers missing type=ERC-20: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(s.token["p1"])
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func newAdapter(t *testing.T, s *srv, chains []string, opts ...Option) *Adapter {
	u, _ := url.Parse(s.URL)
	opts = append([]Option{WithInsecureHTTP(), WithAllowedHosts(u.Host)}, opts...)
	a, err := New(s.URL, chains, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func blocks(n ...int64) []listItem {
	var out []listItem
	for _, b := range n {
		out = append(out, txItem(b, fmt.Sprintf("0xhash%03d", b), "100"))
	}
	return out
}

// TestDrainToCoverage: native needs 2 pages, token exhausts in 1. With
// budget 2 the first call drains one page per stream — token covered and
// emits, native holds back zero rows until page 2 completes coverage.
func TestDrainToCoverage(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: blocks(110, 105, 102), NextPageParams: npp(102)},
		"p2": {Items: blocks(101, 99)},
	}, map[string]listResponse{
		"p1": {Items: []listItem{ttItem(108, "0xt1", 0, "5")}},
	})
	a := newAdapter(t, s, []string{"ethereum"}, WithScanLimits(2, 0, 0, 0))
	ctx := context.Background()
	o := provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110}

	p1, err := a.LookupTransfers(ctx, eth, "0xw", o)
	if err != nil {
		t.Fatalf("call1: %v", err)
	}
	if p1.UpstreamCalls != 2 {
		t.Fatalf("call1 UpstreamCalls=%d want 2", p1.UpstreamCalls)
	}
	if p1.NextPageToken == "" {
		t.Fatal("call1 must not finish — native uncovered")
	}
	var sawTokenRow bool
	for _, tr := range p1.Transfers {
		if tr.Stream == streamNative {
			t.Fatalf("uncovered native stream emitted %+v", tr)
		}
		sawTokenRow = true
	}
	if !sawTokenRow {
		t.Fatal("covered token stream should have emitted")
	}
	st := map[string]bool{}
	for _, ss := range p1.Streams {
		st[ss.Name] = ss.Finished
	}
	if st[streamNative] || !st[streamToken] {
		t.Fatalf("stream states %+v", st)
	}

	p2, err := a.LookupTransfers(ctx, eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatalf("call2: %v", err)
	}
	if p2.NextPageToken != "" {
		t.Fatal("call2 must finish")
	}
	var nativeBlocks []int64
	for _, tr := range p2.Transfers {
		if tr.Stream == streamNative {
			nativeBlocks = append(nativeBlocks, tr.BlockNumber)
		}
	}
	want := []int64{101, 102, 105, 110}
	if len(nativeBlocks) != len(want) {
		t.Fatalf("native rows %v want %v", nativeBlocks, want)
	}
	for i := range want {
		if nativeBlocks[i] != want[i] {
			t.Fatalf("native rows %v want ascending %v", nativeBlocks, want)
		}
	}
	// Token rows already emitted on call1 — must not duplicate.
	for _, tr := range p2.Transfers {
		if tr.Stream == streamToken {
			t.Fatalf("token re-emitted %+v", tr)
		}
	}
}

// TestRequestShape pins the real REST contract: GET, v2 path, cursor
// params forwarded verbatim on page 2.
func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: blocks(105), NextPageParams: npp(105)},
		"p2": {},
	}, map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"}, WithScanLimits(3, 0, 0, 0))
	_, err := a.LookupTransfers(context.Background(), eth, "0xWALLET",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	if err != nil {
		t.Fatal(err)
	}
	var sawP2 bool
	for _, rq := range s.requests {
		if !strings.HasPrefix(rq, "GET /api/v2/addresses/0xWALLET/") {
			t.Errorf("bad request line %q", rq)
		}
		if strings.Contains(rq, "block_number=105") {
			sawP2 = true
		}
	}
	if !sawP2 {
		t.Fatalf("page-2 cursor params never sent: %v", s.requests)
	}
}

// TestBoundedDrain: budget 1 forces one page per call; uncovered streams
// emit zero rows across calls (cross-call buffering via the token).
func TestBoundedDrain(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: blocks(110), NextPageParams: npp(110)},
		"p2": {Items: blocks(99)},
	}, map[string]listResponse{"p1": {Items: blocks(0) /*placeholder*/}})
	a := newAdapter(t, s, []string{"ethereum"}, WithScanLimits(1, 0, 0, 0))
	ctx := context.Background()
	tok := ""
	var emitted int
	for i := 0; i < 5; i++ {
		p, err := a.LookupTransfers(ctx, eth, "0xw",
			provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110, PageToken: tok})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		emitted += len(p.Transfers)
		tok = p.NextPageToken
		if tok == "" {
			if p.UpstreamCalls < 1 {
				t.Fatal("final call did no work")
			}
			return
		}
	}
	t.Fatalf("never finished; emitted %d", emitted)
}

// TestEmptyWallet: a wallet with no history is honestly covered — both
// streams exhaust on page 1, zero rows, terminal (not a failure).
func TestEmptyWallet(t *testing.T) {
	s := newSrv(t, map[string]listResponse{"p1": {}},
		map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"})
	p, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 0 || p.NextPageToken != "" {
		t.Fatalf("empty wallet: %+v", p)
	}
	for _, ss := range p.Streams {
		if !ss.Finished {
			t.Fatalf("empty page must finish stream %s", ss.Name)
		}
	}
}

// TestZeroValueFiltered: zero-value contract calls are not NATIVE value
// transfers — they are scanned but never buffered/emitted.
func TestZeroValueFiltered(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: []listItem{txItem(110, "0xh1", "100"), txItem(108, "0xh2", "0"), txItem(102, "0xh3", "7")}},
	}, map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"})
	p, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 native rows, got %d", len(p.Transfers))
	}
	for _, tr := range p.Transfers {
		if tr.AmountRaw == "0" {
			t.Fatal("zero-value row emitted")
		}
	}
}

func TestMalformed(t *testing.T) {
	s := newSrv(t, nil, nil)
	s.Server.Close()
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not-json"))
	}))
	t.Cleanup(s.Server.Close)
	a := newAdapter(t, s, []string{"ethereum"})
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrMalformed {
		t.Fatalf("want malformed, got %v", err)
	}
}

func TestRateLimited(t *testing.T) {
	s := newSrv(t, nil, nil)
	s.err = 429
	a := newAdapter(t, s, []string{"ethereum"})
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrRateLimited {
		t.Fatalf("want rate_limited, got %v", err)
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil, nil)
	a := newAdapter(t, s, nil) // no enabled chains
	if a.Enabled(eth) {
		t.Fatal("enabled without chain opt-in")
	}
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrDisabled {
		t.Fatalf("want disabled, got %v", err)
	}
	if len(s.requests) != 0 {
		t.Fatalf("disabled adapter emitted traffic: %v", s.requests)
	}
	if a.Enabled(bsc) {
		t.Fatal("bsc must never be enabled")
	}
}

func TestBadToken(t *testing.T) {
	s := newSrv(t, map[string]listResponse{"p1": {}}, map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"})
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110, PageToken: "!!garbage"})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrMalformed {
		t.Fatalf("want malformed, got %v", err)
	}
}

// TestBufferCap: exceeding the row cap is a deterministic internal
// bound — error kind internal (never-failover), buffer dropped.
func TestBufferCap(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: blocks(110, 109, 108, 107, 106, 105, 104, 103, 102, 101, 99)},
	}, map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"}, WithScanLimits(10, 5, 0, 0))
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrInternal {
		t.Fatalf("want internal, got %v", err)
	}
}

// TestHeadCatchUp: after coverage, a higher fetchTo drains only the new
// head segment, then emits the merged window.
func TestHeadCatchUp(t *testing.T) {
	s := newSrv(t, map[string]listResponse{
		"p1": {Items: blocks(110), NextPageParams: nil},
	}, map[string]listResponse{"p1": {}})
	a := newAdapter(t, s, []string{"ethereum"})
	ctx := context.Background()
	if _, err := a.LookupTransfers(ctx, eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 110}); err != nil {
		t.Fatal(err)
	}
	// New head: page1 now returns a newer item then the old boundary.
	s.native["p1"] = listResponse{Items: []listItem{txItem(115, "0xhz", "9"), txItem(110, "0xha", "100")}}
	p, err := a.LookupTransfers(ctx, eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 115})
	if err != nil {
		t.Fatal(err)
	}
	var saw115 bool
	for _, tr := range p.Transfers {
		if tr.BlockNumber == 115 {
			saw115 = true
		}
	}
	if !saw115 {
		t.Fatalf("head row never emitted: %+v", p.Transfers)
	}
	for _, ss := range p.Streams {
		if !ss.Finished {
			t.Fatalf("stream %s unfinished after head join", ss.Name)
		}
	}
}

func TestHeadBlock(t *testing.T) {
	s := newSrv(t, nil, nil)
	s.head = 26340007
	a := newAdapter(t, s, []string{"ethereum"})
	h, err := a.HeadBlock(context.Background(), eth)
	if err != nil || h != 26340007 {
		t.Fatalf("head=%d err=%v", h, err)
	}
}
