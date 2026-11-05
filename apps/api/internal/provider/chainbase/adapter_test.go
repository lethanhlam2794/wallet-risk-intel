package chainbase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	lastQ    url.Values
	natPages map[int][]acctTx  // /v1/account/txs page -> rows
	tokPages map[int][]tokenTx // /v1/token/transfers page -> rows
	head     int64
}

func newSrv(t *testing.T, nat map[int][]acctTx, tokp map[int][]tokenTx, head int64) *srv {
	s := &srv{natPages: nat, tokPages: tokp, head: head}
	s.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		s.calls++
		s.lastPath = r.URL.Path
		s.lastQ = r.URL.Query()
		if r.Header.Get("x-api-key") == "" {
			rw.WriteHeader(401)
			json.NewEncoder(rw).Encode(map[string]any{"code": -401, "message": "Invalid API Key"})
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/block/number/latest"):
			json.NewEncoder(rw).Encode(map[string]any{"code": 0, "message": "ok", "data": s.head})
		case strings.HasSuffix(r.URL.Path, "/v1/account/txs"):
			pg := 1
			if p := r.URL.Query().Get("page"); p != "" {
				json.Unmarshal([]byte(p), &pg)
			}
			rows := s.natPages[pg]
			next := 0
			if _, more := s.natPages[pg+1]; more && len(rows) > 0 {
				next = pg + 1
			}
			json.NewEncoder(rw).Encode(map[string]any{
				"code": 0, "message": "ok", "data": rows, "next_page": next, "count": len(rows)})
		case strings.HasSuffix(r.URL.Path, "/v1/token/transfers"):
			pg := 1
			if p := r.URL.Query().Get("page"); p != "" {
				json.Unmarshal([]byte(p), &pg)
			}
			rows := s.tokPages[pg]
			next := 0
			if _, more := s.tokPages[pg+1]; more && len(rows) > 0 {
				next = pg + 1
			}
			json.NewEncoder(rw).Encode(map[string]any{
				"code": 0, "message": "ok", "data": rows, "next_page": next, "count": len(rows)})
		default:
			rw.WriteHeader(404)
		}
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
	s := newSrv(t, nil, nil, 45000002)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	head, err := a.HeadBlock(context.Background(), bsc)
	if err != nil {
		t.Fatal(err)
	}
	if head != 45000002 {
		t.Fatalf("head %d", head)
	}
	if s.lastQ.Get("chain_id") != "56" {
		t.Fatalf("chain_id %q", s.lastQ.Get("chain_id"))
	}
}

func TestRequestShapeAndMapping(t *testing.T) {
	nat := map[int][]acctTx{1: {
		{Status: 1, BlockNumber: 100, BlockTimestamp: "2024-01-01T00:00:00Z",
			TxHash: "0xH1", TxIndex: 2, From: "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			To: w, Value: "500000000000000000"},
		{Status: 0, BlockNumber: 101, BlockTimestamp: "2024-01-01T00:00:03Z",
			TxHash: "0xHFAIL", TxIndex: 3, From: w, To: w, Value: "9"},
		{Status: 1, BlockNumber: 102, BlockTimestamp: "2024-01-01T00:00:06Z",
			TxHash: "0xH2", TxIndex: 1, From: w,
			To: "0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", Value: "0"},
	}}
	tok := map[int][]tokenTx{1: {
		{BlockNumber: 100, BlockTimestamp: "2024-01-01T00:00:01Z",
			TxHash: "0xH1", TxIndex: 2, LogIndex: 7,
			Contract: "0x55d398326f99059fF775485246999027B3197955",
			From:     w, To: "0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
			Value: "219772901"},
	}}
	s := newSrv(t, nat, tok, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, HeadBlock: 300})
	if err != nil {
		t.Fatal(err)
	}
	if s.lastQ.Get("chain_id") != "56" || s.lastQ.Get("address") != w ||
		s.lastQ.Get("from_block") != "1" || s.lastQ.Get("to_block") != "200" {
		t.Fatalf("query %v path %s", s.lastQ, s.lastPath)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 rows (failed tx + zero-value skipped), got %+v", p.Transfers)
	}
	nat0, tok0 := p.Transfers[0], p.Transfers[1]
	if nat0.Type != provider.TransferKindNative || nat0.AmountRaw != "500000000000000000" ||
		nat0.Stream != streamNative || nat0.Confirmations != 201 ||
		nat0.From != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("native %+v", nat0)
	}
	if tok0.Type != provider.TransferKindToken || tok0.Stream != streamToken ||
		tok0.Asset != "0x55d398326f99059ff775485246999027b3197955" ||
		tok0.AmountRaw != "219772901" {
		t.Fatalf("token %+v", tok0)
	}
	if p.NextPageToken != "" {
		t.Fatalf("both streams exhausted: %+v", p)
	}
	if p.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p.UpstreamCalls)
	}
	for _, ss := range p.Streams {
		if !ss.Finished {
			t.Fatalf("unfinished %+v", p.Streams)
		}
	}
}

func TestTokenRowMissingContractSkipped(t *testing.T) {
	// The documented schema omits contract_address — a row without it
	// carries no asset id and must be skipped, never invented.
	tok := map[int][]tokenTx{1: {
		{BlockNumber: 5, TxHash: "0xT", From: w, To: w, Value: "1"}, // no Contract
		{BlockNumber: 6, TxHash: "0xT2", From: w, To: w, Value: "1",
			Contract: "0xTOKEN"},
	}}
	s := newSrv(t, nil, tok, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 1 || p.Transfers[0].TxHash != "0xt2" {
		t.Fatalf("contract-less row must be skipped: %+v", p.Transfers)
	}
}

func TestUnfinishedStreamHonest(t *testing.T) {
	// Native has a 2nd page; token exhausts page 1. Step budget 1 page.
	nat := map[int][]acctTx{
		1: {{Status: 1, BlockNumber: 3, BlockTimestamp: "2024-01-01T00:00:00Z",
			TxHash: "0xN1", From: w, To: w, Value: "1"}},
		2: {{Status: 1, BlockNumber: 1, BlockTimestamp: "2024-01-01T00:00:00Z",
			TxHash: "0xN2", From: w, To: w, Value: "2"}},
	}
	tok := map[int][]tokenTx{1: {
		{BlockNumber: 2, BlockTimestamp: "2024-01-01T00:00:00Z", TxHash: "0xT",
			Contract: "0xTOKEN", From: w, To: w, Value: "5"},
	}}
	s := newSrv(t, nat, tok, 0)
	u, _ := url.Parse(s.URL)
	a, err := New(s.URL, "KEY", []string{"bsc"},
		WithInsecureHTTP(), WithAllowedHosts(u.Host), WithScanLimits(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 1, ToBlock: 10})
	if err != nil {
		t.Fatal(err)
	}
	// budget 1: only native page 1 fetched; native unfinished (page 2
	// known-pending) → emits nothing yet, token not yet fetched.
	if p1.NextPageToken == "" {
		t.Fatalf("native page 2 pending — must continue: %+v", p1)
	}
	if len(p1.Transfers) != 0 {
		t.Fatalf("unfinished stream emits nothing: %+v", p1.Transfers)
	}
	// step 2: native page 2 exhausts its next_page chain -> native emits
	// buffered rows sorted ASCENDING (upstream returned 3 then 1), token
	// stream still unfetched -> continues.
	p2, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 10, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Transfers) != 2 || p2.Transfers[0].BlockNumber != 1 ||
		p2.Transfers[1].BlockNumber != 3 {
		t.Fatalf("native emit must be ascending buffered rows: %+v", p2.Transfers)
	}
	if p2.NextPageToken == "" {
		t.Fatalf("token stream still pending: %+v", p2)
	}
	// step 3: token page exhausts -> emits; both finished.
	p3, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 10, PageToken: p2.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p3.NextPageToken != "" {
		t.Fatalf("should be finished: %+v", p3)
	}
	if len(p3.Transfers) != 1 || p3.Transfers[0].Stream != streamToken {
		t.Fatalf("token emit: %+v", p3.Transfers)
	}
	for _, ss := range p3.Streams {
		if !ss.Finished {
			t.Fatalf("unfinished %+v", p3.Streams)
		}
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil, nil, 0)
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
