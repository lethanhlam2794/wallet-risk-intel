package tatum

import (
	"context"
	"encoding/json"
	"io"
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
	auth     string
	lastQ    url.Values
	lastPath string
	rpcBody  string
	native   map[string]historyPage // keyed by offset
	token    map[string]historyPage
}

func newSrv(t *testing.T, native map[string]historyPage, token map[string]historyPage) *srv {
	s := &srv{native: native, token: token}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("x-api-key")
		s.lastPath, s.lastQ = r.URL.Path, r.URL.Query()
		if strings.HasPrefix(r.URL.Path, "/v3/blockchain/node/") {
			body, _ := io.ReadAll(r.Body)
			s.rpcBody = string(body)
			json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": 1, "result": "0x2b28d81",
			})
			return
		}
		if r.URL.Path != "/v4/data/transaction/history" {
			w.WriteHeader(404)
			return
		}
		off := r.URL.Query().Get("offset")
		switch r.URL.Query().Get("transactionTypes") {
		case "native":
			json.NewEncoder(w).Encode(s.native[off])
		case "fungible":
			json.NewEncoder(w).Encode(s.token[off])
		default:
			w.WriteHeader(400)
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

func TestHeadBlockRPC(t *testing.T) {
	s := newSrv(t, nil, nil)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	head, err := a.HeadBlock(context.Background(), bsc)
	if err != nil {
		t.Fatal(err)
	}
	if head != 0x2b28d81 {
		t.Fatalf("head %d", head)
	}
	if !strings.Contains(s.rpcBody, `"method":"eth_blockNumber"`) {
		t.Fatalf("rpc body %s", s.rpcBody)
	}
	if s.lastPath != "/v3/blockchain/node/bsc-mainnet" {
		t.Fatalf("rpc path %s", s.lastPath)
	}
	if s.auth != "KEY" {
		t.Fatalf("auth %q", s.auth)
	}
}

func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[string]historyPage{"0": {}}, map[string]historyPage{"0": {}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}); err != nil {
		t.Fatal(err)
	}
	if s.auth != "KEY" {
		t.Fatalf("auth %q", s.auth)
	}
	q := s.lastQ
	if q.Get("chain") != "bsc-mainnet" || q.Get("addresses") != w ||
		q.Get("transactionTypes") != "fungible" || q.Get("blockFrom") != "100" ||
		q.Get("blockTo") != "200" || q.Get("sort") != "ASC" ||
		q.Get("offset") != "0" || q.Get("pageSize") == "" {
		t.Fatalf("query %v", q)
	}
	if s.lastPath != "/v4/data/transaction/history" {
		t.Fatalf("path %s", s.lastPath)
	}
}

func TestMapping(t *testing.T) {
	s := newSrv(t,
		map[string]historyPage{"0": {Result: []historyRow{
			{Hash: "0xH1", Address: w, BlockNumber: 100, TransactionType: "native",
				TransactionSubtype: "incoming", Amount: "0.5",
				Timestamp: 1704067200000, CounterAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Hash: "0xH2", Address: w, BlockNumber: 101, TransactionType: "native",
				TransactionSubtype: "zero-transfer", Amount: "0",
				Timestamp: 1704067203000, CounterAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		}}},
		map[string]historyPage{"0": {Result: []historyRow{
			{Hash: "0xH3", Address: w, BlockNumber: 102, TransactionType: "fungible",
				TransactionSubtype: "outgoing", Amount: "12.34",
				Timestamp: 1704067206000, TokenAddress: "0xTOKEN",
				CounterAddress: "0xcccccccccccccccccccccccccccccccccccccccc"},
		}}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	p, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, HeadBlock: 109})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 rows (zero-transfer skipped), got %+v", p.Transfers)
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
		t.Fatal("short pages — both streams must finish")
	}
	if p.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p.UpstreamCalls)
	}
}

func TestPagination(t *testing.T) {
	full := make([]historyRow, 50)
	for i := range full {
		full[i] = historyRow{Hash: "0xH", Address: w, BlockNumber: 100 + int64(i),
			TransactionType: "native", TransactionSubtype: "incoming",
			Amount: "1", Timestamp: 1704067200000,
			CounterAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	}
	s := newSrv(t,
		map[string]historyPage{"0": {Result: full}, "50": {}},
		map[string]historyPage{"0": {}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextPageToken == "" {
		t.Fatal("full native page — must continue")
	}
	var natFin, tokFin bool
	for _, ss := range p1.Streams {
		if ss.Name == streamNative {
			natFin = ss.Finished
		}
		if ss.Name == streamToken {
			tokFin = ss.Finished
		}
	}
	if natFin || !tokFin {
		t.Fatalf("streams %+v", p1.Streams)
	}
	p2, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p2.NextPageToken != "" {
		t.Fatal("offset 50 empty — must finish")
	}
	if s.lastQ.Get("offset") != "50" {
		t.Fatalf("offset not advanced: %v", s.lastQ)
	}
}

func TestMalformed(t *testing.T) {
	s := &srv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	t.Cleanup(s.Server.Close)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	_, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrMalformed {
		t.Fatalf("want malformed, got %v", err)
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil, nil)
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
