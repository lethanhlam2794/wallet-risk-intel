package oklink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	auth     string
	lastQ    url.Values
	lastPath string
	native   map[string]okData // keyed by page
	token    map[string]okData
	head     string
}

func newSrv(t *testing.T, native, token map[string]okData, head string) *srv {
	s := &srv{native: native, token: token, head: head}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("Ok-Access-Key")
		s.lastPath, s.lastQ = r.URL.Path, r.URL.Query()
		if r.URL.Path == "/api/v5/explorer/blockchain/block" {
			json.NewEncoder(w).Encode(map[string]any{
				"code": "0", "msg": "",
				"data": []map[string]any{{"height": s.head}},
			})
			return
		}
		if r.URL.Path != "/api/v5/explorer/address/transaction-list" {
			w.WriteHeader(404)
			return
		}
		pg := r.URL.Query().Get("page")
		var d okData
		switch r.URL.Query().Get("protocolType") {
		case "transaction":
			d = s.native[pg]
		case "token_20":
			d = s.token[pg]
		default:
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []okData{d}})
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
	s := newSrv(t, nil, nil, "45000001")
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	head, err := a.HeadBlock(context.Background(), bsc)
	if err != nil {
		t.Fatal(err)
	}
	if head != 45000001 {
		t.Fatalf("head %d", head)
	}
	if s.lastPath != "/api/v5/explorer/blockchain/block" || s.lastQ.Get("chainShortName") != "bsc" {
		t.Fatalf("head call %s %v", s.lastPath, s.lastQ)
	}
	if s.auth != "KEY" {
		t.Fatalf("auth %q", s.auth)
	}
}

func TestRequestShape(t *testing.T) {
	s := newSrv(t,
		map[string]okData{"1": {TransactionLists: []okRow{}}},
		map[string]okData{"1": {TransactionList: []okRow{}}}, "0")
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}); err != nil {
		t.Fatal(err)
	}
	if s.auth != "KEY" {
		t.Fatalf("auth %q", s.auth)
	}
	q := s.lastQ
	if q.Get("chainShortName") != "bsc" || q.Get("address") != w ||
		q.Get("protocolType") != "token_20" || q.Get("startBlockHeight") != "100" ||
		q.Get("endBlockHeight") != "200" || q.Get("page") != "1" ||
		q.Get("limit") == "" {
		t.Fatalf("query %v", q)
	}
	if s.lastPath != "/api/v5/explorer/address/transaction-list" {
		t.Fatalf("path %s", s.lastPath)
	}
}

func TestMapping(t *testing.T) {
	s := newSrv(t,
		map[string]okData{"1": {TransactionLists: []okRow{
			{TxID: "0xH1", Height: "100", TransactionTime: "1704067200000",
				From: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", To: w,
				Amount: "0.5", State: "success", TransactionSymbol: "BNB"},
		}}},
		map[string]okData{"1": {TransactionList: []okRow{
			{TxID: "0xH3", Height: "102", TransactionTime: "1704067206000",
				From: w, To: "0xcccccccccccccccccccccccccccccccccccccccc",
				Amount: "12.34", State: "success", TransactionSymbol: "USDT",
				TokenContractAddress: "0xTOKEN"},
		}}}, "0")
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, HeadBlock: 109})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 rows, got %+v", p.Transfers)
	}
	nat, tok := p.Transfers[0], p.Transfers[1]
	if nat.Type != provider.TransferKindNative || nat.AmountRaw != "500000000000000000" ||
		nat.From != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || nat.To != w ||
		nat.Confirmations != 10 || nat.Stream != streamNative {
		t.Fatalf("native %+v", nat)
	}
	if tok.Type != provider.TransferKindToken || tok.Asset != "0xtoken" ||
		tok.AmountRaw != "" || tok.From != w ||
		tok.To != "0xcccccccccccccccccccccccccccccccccccccccc" ||
		tok.Stream != streamToken {
		t.Fatalf("token %+v", tok)
	}
	if p.NextPageToken != "" {
		t.Fatal("single pages — both streams must finish")
	}
	if p.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p.UpstreamCalls)
	}
}

func TestPagination(t *testing.T) {
	full := make([]okRow, 100)
	for i := range full {
		full[i] = okRow{TxID: "0xH", Height: "100", TransactionTime: "1704067200000",
			From: w, To: w, Amount: "1", State: "success"}
	}
	s := newSrv(t,
		map[string]okData{
			"1": {TransactionLists: full, TotalPage: "2"},
			"2": {TransactionLists: []okRow{}, TotalPage: "2"},
		},
		map[string]okData{"1": {TransactionList: []okRow{}, TotalPage: "1"}}, "0")
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextPageToken == "" {
		t.Fatal("page 1 of 2 — must continue")
	}
	p2, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p2.NextPageToken != "" {
		t.Fatal("page 2 empty — must finish")
	}
	if s.lastQ.Get("page") != "2" {
		t.Fatalf("page not advanced: %v", s.lastQ)
	}
}

func TestErrorCodeDegraded(t *testing.T) {
	s := &srv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"code": "50001", "msg": "quota exceeded", "data": []any{},
		})
	}))
	t.Cleanup(s.Server.Close)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	_, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrDegraded {
		t.Fatalf("want degraded, got %v", err)
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil, nil, "0")
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
