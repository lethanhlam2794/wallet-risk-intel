package moralis

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
	auth     string
	lastQ    url.Values
	lastPath string
	native   map[string]nativePage
	token    map[string]tokenPage
}

func newSrv(t *testing.T, native map[string]nativePage, token map[string]tokenPage) *srv {
	s := &srv{native: native, token: token}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("X-API-Key")
		s.lastPath, s.lastQ = r.URL.Path, r.URL.Query()
		if strings.Contains(r.URL.Path, "dateToBlock") {
			json.NewEncoder(w).Encode(map[string]any{"block": 45000001})
			return
		}
		cur := r.URL.Query().Get("cursor")
		if strings.HasSuffix(r.URL.Path, "/erc20/transfers") {
			json.NewEncoder(w).Encode(s.token[cur])
		} else {
			json.NewEncoder(w).Encode(s.native[cur])
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

func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[string]nativePage{"": {}}, map[string]tokenPage{"": {}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}); err != nil {
		t.Fatal(err)
	}
	if s.auth != "KEY" {
		t.Fatalf("auth %q", s.auth)
	}
	q := s.lastQ
	if q.Get("chain") != "bsc" || q.Get("from_block") != "100" ||
		q.Get("to_block") != "200" || q.Get("order") != "ASC" {
		t.Fatalf("query %v", q)
	}
	if !strings.HasSuffix(s.lastPath, "/erc20/transfers") {
		t.Fatalf("token endpoint not hit: %s", s.lastPath)
	}
}

func TestMapping(t *testing.T) {
	s := newSrv(t,
		map[string]nativePage{"": {Result: []nativeRow{
			{Hash: "0xH1", BlockNumber: "100", FromAddress: w,
				ToAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Value:     "1000000000000000000", ReceiptStatus: "1",
				BlockTimestamp: "2024-01-01T00:00:00.000Z"},
			{Hash: "0xH2", BlockNumber: "101", FromAddress: w, ToAddress: w, Value: "0"},
		}}},
		map[string]tokenPage{"": {Result: []tokenRow{
			{TransactionHash: "0xH3", Address: "0xTOKEN", FromAddress: w,
				ToAddress: "0xcccccccccccccccccccccccccccccccccccccccc",
				Value:     "555", BlockNumber: "102", BlockTimestamp: "2024-01-01T00:00:05.000Z"},
		}}})
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
	if nat.Type != provider.TransferKindNative || nat.AmountRaw != "1000000000000000000" ||
		nat.Confirmations != 10 || nat.Stream != streamNative {
		t.Fatalf("native %+v", nat)
	}
	if tok.Type != provider.TransferKindToken || tok.Asset != "0xtoken" ||
		tok.AmountRaw != "555" || tok.Stream != streamToken {
		t.Fatalf("token %+v", tok)
	}
	if p.NextPageToken != "" {
		t.Fatal("both streams exhausted — must finish")
	}
	if p.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p.UpstreamCalls)
	}
}

func TestPagination(t *testing.T) {
	s := newSrv(t,
		map[string]nativePage{
			"":   {Result: []nativeRow{{Hash: "0xH1", BlockNumber: "100", Value: "5"}}, Cursor: "N2"},
			"N2": {},
		},
		map[string]tokenPage{"": {}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextPageToken == "" {
		t.Fatal("native cursor live — must not finish")
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
		t.Fatal("second call must finish")
	}
	if s.lastQ.Get("cursor") != "N2" {
		t.Fatalf("cursor not forwarded: %v", s.lastQ)
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
