// Package tatum is the transaction-history adapter over Tatum
// Blockchain Data API v4 (api.tatum.io) — bsc only.
// GET /v4/data/transaction/history returns movements relative to the
// queried address (address/counterAddress/subtype); upstream docs pin
// offset pagination as stable ONLY within a single transactionTypes
// value, so the adapter runs one request per stream (native + fungible)
// with blockFrom+blockTo always sent together (a lone bound is
// auto-completed as ±1000 upstream).
// Auth: x-api-key header. Head via JSON-RPC /v3/blockchain/node/<chain>.
package tatum

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key.
const SourceCode = "tatum"

const (
	streamNative = "native"
	streamToken  = "token"
	historyPath  = "/v4/data/transaction/history"
)

var defaultAllowedHosts = []string{"api.tatum.io"}

// chains maps our code to the Tatum chain slug.
var chains = map[string]string{"bsc": "bsc-mainnet"}

// Adapter implements provider.TxProvider over Tatum.
type Adapter struct {
	baseURL      string
	apiKey       string
	enabled      map[string]bool
	allowedHosts []string
	insecureHTTP bool
	hooks        resilience.Hooks
	onDuration   func(time.Duration)
	resOpts      []resilience.Option
	cli          *resilience.Client
}

// Option configures the adapter (test hooks only).
type Option func(*Adapter)

// WithAllowedHosts overrides the SSRF allowlist (tests point at httptest).
func WithAllowedHosts(hosts ...string) Option {
	return func(a *Adapter) { a.allowedHosts = hosts }
}

// WithInsecureHTTP permits plain-http base URLs. Test-only.
func WithInsecureHTTP() Option {
	return func(a *Adapter) { a.insecureHTTP = true }
}

// WithHooks attaches resilience metrics hooks.
func WithHooks(h resilience.Hooks) Option {
	return func(a *Adapter) { a.hooks = h }
}

// WithResilienceOption forwards a construction-time knob to the toolkit.
func WithResilienceOption(o resilience.Option) Option {
	return func(a *Adapter) { a.resOpts = append(a.resOpts, o) }
}

// WithDurationFunc attaches a call-site latency observer.
func WithDurationFunc(f func(time.Duration)) Option {
	return func(a *Adapter) { a.onDuration = f }
}

// New validates the base URL and builds the resilience client.
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		enabled: map[string]bool{}, allowedHosts: defaultAllowedHosts}
	for _, c := range enabledChains {
		a.enabled[strings.ToLower(strings.TrimSpace(c))] = true
	}
	for _, o := range opts {
		o(a)
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source: SourceCode, Timeout: 15 * time.Second,
		Rate: rate.Every(500 * time.Millisecond), Burst: 1,
		MaxLimiterWait: 4 * time.Second, Concurrency: 2, MaxRetries: 2,
		Backoff: 300 * time.Millisecond, BreakerFailures: 5,
		BreakerOpen: 60 * time.Second, MaxBodyBytes: 8 << 20,
		AllowedHosts: a.allowedHosts, Hooks: a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("tatum: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("tatum: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("tatum: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("tatum: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network — key-gated.
func (a *Adapter) Enabled(net network.Network) bool {
	if a.apiKey == "" {
		return false
	}
	_, ok := chains[strings.ToLower(net.Code)]
	return ok && a.enabled[strings.ToLower(net.Code)]
}

// do performs one authenticated request through the resilience client.
func (a *Adapter) do(ctx context.Context, req *http.Request, out any) error {
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "tatum %s: %v", req.URL.Path, err)
	}
	return nil
}

// HeadBlock returns the upstream-observed head via the Tatum node RPC
// gateway (eth_blockNumber) — same host, same key.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "tatum: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	u := a.baseURL + "/v3/blockchain/node/" + chain
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber", "params": []any{},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, provider.Errorf(provider.ErrInternal, "tatum: build request: %v", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("content-type", "application/json")
	var out struct {
		Result string `json:"result"`
	}
	if err := a.do(ctx, req, &out); err != nil {
		return 0, err
	}
	head, err := strconv.ParseInt(strings.TrimPrefix(out.Result, "0x"), 16, 64)
	if err != nil || head <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "tatum: bad head %q", out.Result)
	}
	return head, nil
}

// pageToken carries the two per-stream offsets (int; -1 = done marker
// encoded as empty string absent the field).
type pageToken struct {
	Native int `json:"n"` // -1 = finished
	Token  int `json:"t"`
}

// LookupTransfers fetches one offset page per unfinished stream (at most
// 2 upstream calls), each pinned to a single transactionTypes value —
// the only pagination mode upstream guarantees stable.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "tatum: disabled for %s", net.Code)
	}
	tok := pageToken{Native: 0, Token: 0}
	if opts.PageToken != "" {
		if err := json.Unmarshal([]byte(opts.PageToken), &tok); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "tatum: bad page token")
		}
	}
	page := &provider.TransferPage{}
	finished := map[string]bool{streamNative: true, streamToken: true}
	for _, st := range []struct {
		name    string
		types   string
		offset  int
		setNext func(int)
	}{
		{streamNative, "native", tok.Native, func(v int) { tok.Native = v }},
		{streamToken, "fungible", tok.Token, func(v int) { tok.Token = v }},
	} {
		if st.offset < 0 {
			continue // already done
		}
		var out historyPage
		if err := a.fetchStream(ctx, chains[strings.ToLower(net.Code)], wallet,
			st.types, st.offset, opts, &out); err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		for _, row := range out.Result {
			if t, ok := mapRow(row, st.name, opts.HeadBlock); ok {
				page.Transfers = append(page.Transfers, t)
			}
		}
		if len(out.Result) >= pageSize {
			st.setNext(st.offset + len(out.Result))
			finished[st.name] = false
		} else {
			st.setNext(-1)
		}
	}
	page.Streams = []provider.StreamStatus{
		{Name: streamNative, Finished: finished[streamNative]},
		{Name: streamToken, Finished: finished[streamToken]},
	}
	if !finished[streamNative] || !finished[streamToken] {
		b, _ := json.Marshal(tok)
		page.NextPageToken = string(b)
	}
	return page, nil
}

const pageSize = 50

// fetchStream issues one history GET pinned to a single transactionTypes
// value, ascending sort, and both block bounds always present.
func (a *Adapter) fetchStream(ctx context.Context, chain, wallet, types string,
	offset int, opts provider.TransferLookupOpts, out *historyPage) error {
	q := url.Values{
		"chain":            {chain},
		"addresses":        {wallet},
		"transactionTypes": {types},
		"blockFrom":        {strconv.FormatInt(opts.FromBlock, 10)},
		"blockTo":          {strconv.FormatInt(opts.ToBlock, 10)},
		"sort":             {"ASC"},
		"pageSize":         {strconv.Itoa(pageSize)},
		"offset":           {strconv.Itoa(offset)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+historyPath+"?"+q.Encode(), nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "tatum: build request: %v", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	return a.do(ctx, req, out)
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
