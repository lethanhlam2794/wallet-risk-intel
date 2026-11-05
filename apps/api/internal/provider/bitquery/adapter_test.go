package bitquery

import (
	"context"
	"encoding/json"
	"io"
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
	auth     string
	lastBody string
	native   map[string][]transferRow // keyed by offset in limit clause
	token    map[string][]transferRow
	blocks   int64
}

func newSrv(t *testing.T, native, token map[string][]transferRow, blocks int64) *srv {
	s := &srv{native: native, token: token, blocks: blocks}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls++
		s.auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		s.lastBody = string(body)
		var env struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			w.WriteHeader(400)
			return
		}
		if strings.Contains(env.Query, "Blocks") {
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"EVM": map[string]any{
					"Blocks": []map[string]any{{"Block": map[string]any{"Number": s.blocks}}},
				}},
			})
			return
		}
		// offset is inlined into limit:{count: N, offset: M}
		off := "0"
		if i := strings.Index(env.Query, "offset: "); i >= 0 {
			rest := env.Query[i+8:]
			if j := strings.IndexAny(rest, " },"); j >= 0 {
				off = rest[:j]
			}
		}
		var rows []transferRow
		if strings.Contains(env.Query, `Native: true`) {
			rows = s.native[off]
		} else if strings.Contains(env.Query, `Native: false`) {
			rows = s.token[off]
		} else {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"EVM": map[string]any{"Transfers": rows}},
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

func TestHeadBlockGraphQL(t *testing.T) {
	s := newSrv(t, nil, nil, 45000001)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	head, err := a.HeadBlock(context.Background(), bsc)
	if err != nil {
		t.Fatal(err)
	}
	if head != 45000001 {
		t.Fatalf("head %d", head)
	}
	if s.auth != "Bearer KEY" {
		t.Fatalf("auth %q", s.auth)
	}
	if !strings.Contains(s.lastBody, "descending: Block_Number") {
		t.Fatalf("query %s", s.lastBody)
	}
}

func TestQueryShape(t *testing.T) {
	s := newSrv(t,
		map[string][]transferRow{"0": {}},
		map[string][]transferRow{"0": {}}, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	if _, err := a.LookupTransfers(context.Background(), bsc, w,
		provider.TransferLookupOpts{FromBlock: 100, ToBlock: 200}); err != nil {
		t.Fatal(err)
	}
	q := s.lastBody
	for _, want := range []string{
		`network: bsc`, `0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`,
		`ge: \"100\"`, `le: \"200\"`, `Native: false`,
		"Block_Number", "Transaction_Index", "offset: 0",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("query missing %q:\n%s", want, q)
		}
	}
	if s.auth != "Bearer KEY" {
		t.Fatalf("auth %q", s.auth)
	}
}

func TestMapping(t *testing.T) {
	mk := func(hash string, blockNum int64, sender, receiver, amount string,
		decimals int, native bool, smartContract string) transferRow {
		return transferRow{
			Block:       block{Number: json.Number(strconv.FormatInt(blockNum, 10)), Time: "2024-01-01T00:00:00Z"},
			Transaction: tx{Hash: hash},
			Transfer: transfer{
				Sender: sender, Receiver: receiver, Amount: amount, Success: true,
				Currency: currency{SmartContract: smartContract, Decimals: decimals,
					Native: native, Fungible: !native},
			},
		}
	}
	s := newSrv(t,
		map[string][]transferRow{"0": {
			mk("0xH1", 100, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", w, "0.5", 18, true, ""),
		}},
		map[string][]transferRow{"0": {
			mk("0xH3", 102, w, "0xcccccccccccccccccccccccccccccccccccccccc",
				"12.34", 2, false, "0xTOKEN"),
			// NFT rows must be dropped from the token stream.
			transferRow{
				Block:       block{Number: "103", Time: "2024-01-01T00:00:01Z"},
				Transaction: tx{Hash: "0xNFT"},
				Transfer: transfer{Sender: w,
					Receiver: "0xdddddddddddddddddddddddddddddddddddddddd",
					Amount:   "1", Success: true,
					Currency: currency{SmartContract: "0xNFT", Fungible: false}},
			},
		}}, 0)
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
		tok.AmountRaw != "1234" || tok.From != w ||
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
	full := make([]transferRow, 50)
	for i := range full {
		full[i] = transferRow{
			Block:       block{Number: json.Number(strconv.Itoa(100 + i)), Time: "2024-01-01T00:00:00Z"},
			Transaction: tx{Hash: "0xH"},
			Transfer: transfer{Sender: w, Receiver: w, Amount: "1", Success: true,
				Currency: currency{Native: true, Decimals: 18}},
		}
	}
	s := newSrv(t,
		map[string][]transferRow{"0": full, "50": {}},
		map[string][]transferRow{"0": {}}, 0)
	a := newAdapter(t, s, "KEY", []string{"bsc"})
	ctx := context.Background()
	p1, err := a.LookupTransfers(ctx, bsc, w, provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200})
	if err != nil {
		t.Fatal(err)
	}
	if p1.NextPageToken == "" {
		t.Fatal("full native page — must continue")
	}
	p2, err := a.LookupTransfers(ctx, bsc, w,
		provider.TransferLookupOpts{FromBlock: 1, ToBlock: 200, PageToken: p1.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if p2.NextPageToken != "" {
		t.Fatal("offset 50 empty — must finish")
	}
	if !strings.Contains(s.lastBody, "offset: 50") {
		t.Fatalf("offset not advanced: %s", s.lastBody)
	}
}

func TestGraphQLErrorsDegraded(t *testing.T) {
	s := &srv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data":   nil,
			"errors": []map[string]any{{"message": "access restricted: your plan only allows \"realtime\""}},
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
