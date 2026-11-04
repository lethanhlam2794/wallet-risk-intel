package ankr

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

var eth = network.Network{Code: "ethereum"}
var wA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
var wB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
var wC = "0xcccccccccccccccccccccccccccccccccccccccc"
var wD = "0x9999999999999999999999999999999999999999"
var bsc = network.Network{Code: "bsc"}

type srv struct {
	*httptest.Server
	lastParams map[string]any
	resp       map[string]any
	err        int
	calls      int
	path       string
}

func newSrv(t *testing.T, resp map[string]any) *srv {
	s := &srv{resp: resp}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.path = r.URL.Path
		if s.err != 0 {
			w.WriteHeader(s.err)
			return
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"` // OBJECT, not positional array
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode req: %v", err)
		}
		s.lastParams = req.Params
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1, "result": s.resp})
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

func tx(hash, block, from, to, value string, logs []map[string]any) map[string]any {
	m := map[string]any{
		"hash": hash, "blockNumber": block, "from": from, "to": to,
		"value": value, "status": "0x1", "timestamp": "0x654e2a80",
	}
	if logs != nil {
		m["logs"] = logs
	}
	return m
}

func transferLog(token, from, to, amount, hash string) map[string]any {
	pad := func(a string) string {
		return "0x" + strings.Repeat("0", 66-len(strings.TrimPrefix(a, "0x"))-2) + strings.TrimPrefix(a, "0x")
	}
	return map[string]any{
		"address": token, "data": amount, "transactionHash": hash, "logIndex": "0x0",
		"topics": []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", pad(from), pad(to)},
	}
}

// TestRequestShape pins params-as-object per Ankr docs.
func TestRequestShape(t *testing.T) {
	s := newSrv(t, map[string]any{"transactions": []any{}, "nextPageToken": ""})
	a := newAdapter(t, s, "KEY", []string{"ethereum"})
	_, err := a.LookupTransfers(context.Background(), eth, "0xW",
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if s.path != "/KEY" {
		t.Fatalf("api key must be in URL path, got %q", s.path)
	}
	p := s.lastParams
	if p["address"] != "0xW" || p["includeLogs"] != true || p["descOrder"] != false {
		t.Fatalf("params %+v", p)
	}
	chains, _ := p["blockchain"].([]any)
	if len(chains) != 1 || chains[0] != "eth" {
		t.Fatalf("blockchain %v", p["blockchain"])
	}
	if p["fromBlock"].(float64) != 100 || p["toBlock"].(float64) != 200 {
		t.Fatalf("range %v %v", p["fromBlock"], p["toBlock"])
	}
}

// TestMapping: native value + matching Transfer log → 1 native + 1
// token row, hex→decimal amounts, hex timestamp, confirmations.
func TestMapping(t *testing.T) {
	w := wA
	s := newSrv(t, map[string]any{
		"transactions": []any{
			tx("0xH1", "0x64", wA, wB, "0xde0b6b3a7640000", nil), // 1 ETH
			tx("0xH2", "0x65", wC, wA, "0x0", []map[string]any{
				transferLog("0xTOKEN", wC, wA, "0x3e8", "0xH2"), // 1000
				transferLog("0xTOKEN", wC, wD, "0x5", "0xH2"),   // not wallet
			}),
		},
		"nextPageToken": "",
	})
	a := newAdapter(t, s, "KEY", []string{"ethereum"})
	p, err := a.LookupTransfers(context.Background(), eth, w,
		provider.TransferLookupOpts{FromBlock: 99, ToBlock: 200, HeadBlock: 109})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Transfers) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(p.Transfers), p.Transfers)
	}
	nat, tok := p.Transfers[0], p.Transfers[1]
	if nat.Type != provider.TransferKindNative || nat.AmountRaw != "1000000000000000000" {
		t.Fatalf("native %+v", nat)
	}
	if nat.BlockNumber != 100 || nat.Confirmations != 10 {
		t.Fatalf("block/conf %+v", nat)
	}
	if nat.BlockTimestamp.IsZero() {
		t.Fatal("timestamp not mapped")
	}
	if tok.Type != provider.TransferKindToken || tok.Asset != "0xtoken" || tok.AmountRaw != "1000" {
		t.Fatalf("token %+v", tok)
	}
	if !p.Streams[0].Finished || p.NextPageToken != "" {
		t.Fatalf("streams %+v token %q", p.Streams, p.NextPageToken)
	}
}

// TestPagination: nextPageToken round-trips verbatim.
func TestPagination(t *testing.T) {
	s := newSrv(t, map[string]any{"transactions": []any{}, "nextPageToken": "PG2"})
	a := newAdapter(t, s, "KEY", []string{"ethereum"})
	p, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextPageToken != "PG2" || p.Streams[0].Finished {
		t.Fatalf("page %+v", p)
	}
	if _, err = a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, PageToken: "PG2"}); err != nil {
		t.Fatal(err)
	}
	if s.lastParams["pageToken"] != "PG2" {
		t.Fatalf("token not forwarded: %+v", s.lastParams)
	}
}

// TestOutOfSync: outOfSync indexer → degraded, never rows.
func TestOutOfSync(t *testing.T) {
	s := newSrv(t, map[string]any{
		"transactions": []any{tx("0xH1", "0x64", wA, wB, "0x1", nil)},
		"syncStatus":   map[string]any{"status": "outOfSync"},
	})
	a := newAdapter(t, s, "KEY", []string{"ethereum"})
	_, err := a.LookupTransfers(context.Background(), eth, wA,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrDegraded {
		t.Fatalf("want degraded, got %v", err)
	}
}

func TestDisabled(t *testing.T) {
	s := newSrv(t, nil)
	a := newAdapter(t, s, "", []string{"ethereum"}) // no key
	if a.Enabled(eth) || a.Enabled(bsc) {
		t.Fatal("enabled without key")
	}
	_, err := a.LookupTransfers(context.Background(), eth, "0xw",
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2})
	le, ok := err.(*provider.LookupError)
	if !ok || le.Kind != provider.ErrDisabled {
		t.Fatalf("want disabled, got %v", err)
	}
	if s.calls != 0 {
		t.Fatal("disabled adapter emitted traffic")
	}
}

func TestBSCChainToken(t *testing.T) {
	s := newSrv(t, map[string]any{"transactions": []any{}, "nextPageToken": ""})
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, "0xw",
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 2}); err != nil {
		t.Fatal(err)
	}
	chains, _ := s.lastParams["blockchain"].([]any)
	if len(chains) != 1 || chains[0] != "bsc" {
		t.Fatalf("bsc chain token %v", s.lastParams["blockchain"])
	}
}
