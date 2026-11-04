package covalent

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

var bsc = network.Network{Code: "bsc"}
var eth = network.Network{Code: "ethereum"}

const w = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type srv struct {
	*httptest.Server
	pages    map[int]txPage
	calls    int
	auth     string
	lastPath string
	head     int64
}

func newSrv(t *testing.T, pages map[int]txPage) *srv {
	s := &srv{pages: pages, head: 999}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("Authorization")
		s.lastPath = r.URL.Path
		if strings.Contains(r.URL.Path, "block_v2") {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"items": []map[string]any{{"height": s.head}}}, "error": false})
			return
		}
		var pg int
		fmt.Sscanf(r.URL.Path[strings.LastIndex(r.URL.Path[:len(r.URL.Path)-1], "/")+1:], "%d", &pg)
		p, ok := s.pages[pg]
		if !ok {
			p = txPage{}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": p, "error": false})
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func newAdapter(t *testing.T, s *srv, key string, chains []string, opts ...Option) *Adapter {
	u, _ := url.Parse(s.URL)
	opts = append([]Option{WithInsecureHTTP(), WithAllowedHosts(u.Host)}, opts...)
	a, err := New(s.URL, key, chains, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func tx(hash string, block int64, value string) txRow {
	return txRow{TxHash: hash, BlockHeight: block, FromAddress: w,
		ToAddress: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Value:     value, Successful: true, BlockSignedAt: "2024-01-01T00:00:00Z"}
}

func withNext(p txPage) txPage {
	n := "next"
	p.Links.Next = &n
	return p
}

// TestRequestShape: real path /v1/bsc-mainnet/address/{w}/transactions_v3/
// page/{n}/ + Bearer auth + asc sort param.
func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[int]txPage{0: {Items: []txRow{tx("0xh", 100, "5")}}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200}); err != nil {
		t.Fatal(err)
	}
	if s.auth != "Bearer KEY" {
		t.Fatalf("auth %q", s.auth)
	}
	if !strings.Contains(s.lastPath, "/v1/bsc-mainnet/address/"+w+"/transactions_v3/page/0/") {
		t.Fatalf("path %q", s.lastPath)
	}
}

// TestAscendingWindow: in-window rows emit ascending; below-window pages
// emit nothing but advance; stream finishes when window passed.
func TestAscendingWindow(t *testing.T) {
	s := newSrv(t, map[int]txPage{
		0: withNext(txPage{Items: []txRow{tx("0xh50", 50, "5"), tx("0xh99", 99, "7")}}),
		1: withNext(txPage{Items: []txRow{tx("0xh100", 100, "9"), tx("0xh150", 150, "0x2a")}}),
		2: {Items: []txRow{tx("0xh400", 400, "1")}},
	})
	a := newAdapter(t, s, "KEY", []string{"bsc"}, WithScanLimits(2, 0, 0))
	ctx := context.Background()
	o := provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}

	p1, err := a.LookupTransfers(ctx, bsc, w, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Transfers) != 2 || p1.Transfers[0].BlockNumber != 100 {
		t.Fatalf("page-0 rows skipped correctly, page-1 emitted: %+v", p1.Transfers)
	}
	if p1.NextPageToken == "" || p1.Streams[0].Finished {
		t.Fatal("window not yet passed — must stay unfinished")
	}
	if p1.UpstreamCalls != 2 {
		t.Fatalf("calls %d", p1.UpstreamCalls)
	}
}

// TestCrossRefreshProgress: token="" second refresh resumes the page
// walk from the process-local cursor, not page 0.
func TestCrossRefreshProgress(t *testing.T) {
	s := newSrv(t, map[int]txPage{
		0: withNext(txPage{Items: []txRow{tx("0xh50", 50, "5")}}),
		1: {Items: []txRow{tx("0xh110", 110, "5")}}, // no next → exhausts
	})
	a := newAdapter(t, s, "KEY", []string{"bsc"}, WithScanLimits(1, 0, 0))
	ctx := context.Background()
	o := provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}
	// Refresh 1: drains page 0 only.
	p1, _ := a.LookupTransfers(ctx, bsc, w, o)
	if len(p1.Transfers) != 0 {
		t.Fatalf("below-window row emitted: %+v", p1.Transfers)
	}
	// Refresh 2 (PageToken=""): must resume at page 1, not restart at 0.
	p2, err := a.LookupTransfers(ctx, bsc, w, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Transfers) != 1 || p2.Transfers[0].BlockNumber != 110 {
		t.Fatalf("progress lost — restarted at page 0: %+v", p2.Transfers)
	}
	if !p2.Streams[0].Finished {
		t.Fatal("exhausted upstream must finish stream")
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil)
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

func TestHeadBlock(t *testing.T) {
	s := newSrv(t, nil)
	s.head = 45000001
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	h, err := a.HeadBlock(context.Background(), bsc)
	if err != nil || h != 45000001 {
		t.Fatalf("head=%d err=%v", h, err)
	}
}

func TestBadToken(t *testing.T) {
	s := newSrv(t, map[int]txPage{0: {}})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	_, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2, PageToken: "junk"})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrMalformed {
		t.Fatalf("want malformed, got %v", err)
	}
}
