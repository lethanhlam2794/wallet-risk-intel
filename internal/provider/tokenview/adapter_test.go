package tokenview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

var bsc = network.Network{Code: "bsc"}
var eth = network.Network{Code: "ethereum"}

const w = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type srv struct {
	*httptest.Server
	calls    int
	lastPath string
	pages    map[int][]txRow // normal-tx-list pages, 1-based
	head     int64
}

func newSrv(t *testing.T, pages map[int][]txRow, head int64) *srv {
	s := &srv{pages: pages, head: head}
	s.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s.calls++
		s.lastPath = r.URL.Path
		if strings.HasSuffix(r.URL.Path, "/block/last") {
			json.NewEncoder(rw).Encode(map[string]any{
				"code": 1, "msg": "success",
				"data": map[string]any{"height": s.head},
			})
			return
		}
		// /vipapi/address/{coin}/{addr}/{page}/{size}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		pg, _ := strconv.Atoi(parts[len(parts)-2])
		json.NewEncoder(rw).Encode(map[string]any{
			"code": 1, "msg": "success",
			"data": []map[string]any{{"txs": s.pages[pg]}},
		})
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func newAdapter(t *testing.T, s *srv, key string, chains []string) *Adapter {
	u, _ := url.Parse(s.URL)
	a, err := New(s.URL, key, chains, WithInsecureHTTP(), WithAllowedHosts(u.Host))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestHeadBlock(t *testing.T) {
	s := newSrv(t, nil, 45000001)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	head, err := a.HeadBlock(context.Background(), bsc)
	if err != nil {
		t.Fatal(err)
	}
	if head != 45000001 {
		t.Fatalf("head %d", head)
	}
	if !strings.Contains(s.lastPath, "/vipapi/bsc/block/last") {
		t.Fatalf("head path %s", s.lastPath)
	}
}

func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[int][]txRow{1: {}}, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.lastPath, "/vipapi/address/bsc/"+w+"/1/") {
		t.Fatalf("path %s", s.lastPath)
	}
}

func TestMappingAndOrder(t *testing.T) {
	s := newSrv(t, map[int][]txRow{
		1: { // newest-first upstream order
			{Height: 102, Time: 1704067206, TxID: "0xH2", From: w,
				To: "0xcccccccccccccccccccccccccccccccccccccccc", Value: "0",
				TokenTransfer: []tokenTransferRow{
					{Index: 3, TokenAddr: "0xTOKEN", TokenDecimals: "6",
						From: w, To: "0xcccccccccccccccccccccccccccccccccccccccc",
						Value: "219772901"},
				}},
			{Height: 100, Time: 1704067200, TxID: "0xH1",
				From: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", To: w, Value: "0.5"},
		},
	}, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, HeadBlock: 109})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 rows, got %+v", p.Transfers)
	}
	// emitted ascending even though upstream is newest-first
	nat, tok := p.Transfers[0], p.Transfers[1]
	if nat.Type != provider.TransferKindNative || nat.AmountRaw != "500000000000000000" ||
		nat.Confirmations != 10 || nat.Stream != streamNative || nat.BlockNumber != 100 {
		t.Fatalf("native %+v", nat)
	}
	if tok.Type != provider.TransferKindToken || tok.Asset != "0xtoken" ||
		tok.AmountRaw != "219772901" || tok.Stream != streamToken ||
		tok.BlockNumber != 102 {
		t.Fatalf("token %+v", tok)
	}
	if p.NextPageToken != "" {
		t.Fatal("floor below from — must finish")
	}
	// page 1 (data) + page 2 (empty → genuine exhaustion, proves coverage)
	if p.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p.UpstreamCalls)
	}
	var nFin, tFin bool
	for _, ss := range p.Streams {
		if ss.Name == streamNative {
			nFin = ss.Finished
		}
		if ss.Name == streamToken {
			tFin = ss.Finished
		}
	}
	if !nFin || !tFin {
		t.Fatalf("shared-list coverage must finish together: %+v", p.Streams)
	}
}

func TestUnfinishedDrainsNextStep(t *testing.T) {
	// Rows all above fetchFrom → covered never reached; page cap 1/step.
	s := newSrv(t, map[int][]txRow{
		1: {{Height: 190, Time: 1, TxID: "0xA", From: w, To: w, Value: "1"}},
		2: {{Height: 150, Time: 1, TxID: "0xB", From: w, To: w, Value: "1"}},
		3: {{Height: 50, Time: 1, TxID: "0xC", From: w, To: w, Value: "1"}},
	}, 0)
	u, _ := url.Parse(s.URL)
	a, err := New(s.URL, "KEY", []string{"bsc"},
		WithInsecureHTTP(), WithAllowedHosts(u.Host),
		WithScanLimits(1, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextPageToken == "" || len(p1.Transfers) != 0 {
		t.Fatalf("uncovered window must emit nothing: %+v", p1)
	}
	p2, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p2.NextPageToken == "" {
		t.Fatal("floor 150 still ≥ 100 — unfinished")
	}
	p3, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200, PageToken: p2.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p3.NextPageToken != "" || len(p3.Transfers) != 2 {
		t.Fatalf("floor 50 < 100 — emit 2 in-window rows: %+v", p3)
	}
	if p3.Transfers[0].BlockNumber != 150 || p3.Transfers[1].BlockNumber != 190 {
		t.Fatalf("ascending order violated: %+v", p3.Transfers)
	}
}

func TestHardPageCapExhausts(t *testing.T) {
	// 50 pages drained without floor < fetchFrom → stream marks exhausted
	// (upstream ceiling reached) and emits what it has — honest partial.
	s := &srv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s.calls++
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		pg, _ := strconv.Atoi(parts[len(parts)-2])
		if pg > 50 {
			json.NewEncoder(rw).Encode(map[string]any{"code": 1, "data": []any{}})
			return
		}
		json.NewEncoder(rw).Encode(map[string]any{"code": 1, "data": []map[string]any{
			{"txs": []txRow{{Height: 999 - int64(pg), Time: 1, TxID: "0x", From: w, To: w, Value: "1"}}},
		}})
	}))
	t.Cleanup(s.Server.Close)
	u, _ := url.Parse(s.URL)
	a, err := New(s.URL, "KEY", []string{"bsc"},
		WithInsecureHTTP(), WithAllowedHosts(u.Host), WithScanLimits(60, 0, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	// page-51 ceiling is an ARTIFICIAL cap, not end-of-history: the
	// stream must stay unfinished (honest permanent PARTIAL) and carry
	// no continuation — never claim coverage it can't prove.
	if p.NextPageToken != "" {
		t.Fatalf("capped scan has no continuation: %+v", p)
	}
	for _, ss := range p.Streams {
		if ss.Finished {
			t.Fatalf("capped scan must not claim coverage: %+v", p.Streams)
		}
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil, 0)
	a := newAdapter(t, s, "", []string{"bsc"})
	if a.Enabled(bsc) || a.Enabled(eth) {
		t.Fatal("enabled without key")
	}
	_, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrDisabled {
		t.Fatalf("want disabled, got %v", err)
	}
	if s.calls != 0 {
		t.Fatal("disabled adapter emitted traffic")
	}
}
