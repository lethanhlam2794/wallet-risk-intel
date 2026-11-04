// Package chainbase is the transaction-history adapter over Chainbase
// Web3 API (api.chainbase.online/v1) — ethereum + bsc, keyed
// (x-api-key). Two independent endpoints: native transfers come from
// /v1/account/txs (raw wei values, failed/zero-value rows skipped) and
// token transfers from /v1/token/transfers (raw integers; rows lacking
// contract_address skipped — asset ids are never invented). Both take
// server-side from_block/to_block windows with page/next_page cursors.
// Upstream row order is undocumented, so each stream buffers rows until
// its next_page chain exhausts, then emits once sorted ascending by
// block — an unfinished stream emits nothing. Head via
// /v1/block/number/latest.
package chainbase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key.
const SourceCode = "chainbase"

const (
	streamNative = "native"
	streamToken  = "token"
	pageSize     = 100 // upstream max
)

var defaultAllowedHosts = []string{"api.chainbase.online"}

// chains maps our network code to the chain_id.
var chains = map[string]string{"ethereum": "1", "bsc": "56"}

// Adapter implements provider.TxProvider over Chainbase.
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

	maxDrainPages int
	maxBufferRows int
	maxScans      int
	scanTTL       time.Duration
	mu            sync.Mutex
	scans         map[string]*scanState
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

// WithScanLimits tunes drain/eviction bounds (tests shrink them).
func WithScanLimits(maxPages, maxRows, maxScans int) Option {
	return func(a *Adapter) {
		if maxPages > 0 {
			a.maxDrainPages = maxPages
		}
		if maxRows > 0 {
			a.maxBufferRows = maxRows
		}
		if maxScans > 0 {
			a.maxScans = maxScans
		}
	}
}

// New validates the base URL and builds the resilience client.
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		enabled: map[string]bool{}, allowedHosts: defaultAllowedHosts,
		maxDrainPages: 5, maxBufferRows: 20000, maxScans: 256,
		scanTTL: 30 * time.Minute, scans: map[string]*scanState{}}
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
		return nil, fmt.Errorf("chainbase: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("chainbase: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("chainbase: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("chainbase: host %q not in allowlist", u.Host)
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

// get performs one authenticated GET; code != 0 in the envelope is a
// provider-level signal → degraded, never a clean empty page.
func (a *Adapter) get(ctx context.Context, path string, out *envelope) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "chainbase: build request: %v", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "chainbase %s: %v", path, err)
	}
	if out.Code != 0 {
		m := strings.ToLower(out.Message)
		if strings.Contains(m, "rate") || strings.Contains(m, "limit") {
			return provider.Errorf(provider.ErrRateLimited, "chainbase: code %d: %s", out.Code, out.Message)
		}
		return provider.Errorf(provider.ErrDegraded, "chainbase: code %d: %s", out.Code, out.Message)
	}
	return nil
}

// HeadBlock returns the upstream-observed head via block/number/latest;
// data may be a decimal int or hex string.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "chainbase: disabled for %s", net.Code)
	}
	var env envelope
	if err := a.get(ctx, "/v1/block/number/latest?chain_id="+chains[strings.ToLower(net.Code)], &env); err != nil {
		return 0, err
	}
	var n json.Number
	if err := json.Unmarshal(env.Data, &n); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "chainbase head: %v", err)
	}
	s := n.String()
	var h int64
	var err error
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		h, err = strconv.ParseInt(s[2:], 16, 64)
	} else {
		h, err = strconv.ParseInt(s, 10, 64)
	}
	if err != nil || h <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "chainbase: bad head %q", s)
	}
	return h, nil
}

// scanState buffers one (net|wallet|fetchFrom) walk over both lists.
type scanState struct {
	mu      sync.Mutex
	touched time.Time
	nPage   int // next account/txs page (0 = not started)
	tPage   int // next token/transfers page
	nDone   bool
	tDone   bool
	nRows   []provider.Transfer
	tRows   []provider.Transfer
	nEmit   bool
	tEmit   bool
	head    int64
}

// evict applies idle-TTL and global-LRU bounds; caller holds a.mu.
func (a *Adapter) evict(now time.Time) {
	for k, s := range a.scans {
		if now.Sub(s.touched) > a.scanTTL {
			delete(a.scans, k)
		}
	}
	for len(a.scans) > a.maxScans {
		var oldest string
		var ot time.Time
		for k, s := range a.scans {
			if oldest == "" || s.touched.Before(ot) {
				oldest, ot = k, s.touched
			}
		}
		delete(a.scans, oldest)
	}
}

// tokenState is the serialized continuation in NextPageToken.
type tokenState struct {
	V  int `json:"v"`
	NP int `json:"np"`
	TP int `json:"tp"`
}

// fetchPage pulls one page of either list for the block window.
func (a *Adapter) fetchPage(ctx context.Context, net network.Network, wallet string,
	token bool, pg int, from, to int64) (*envelope, error) {
	cid := chains[strings.ToLower(net.Code)]
	ep := "/v1/account/txs"
	if token {
		ep = "/v1/token/transfers"
	}
	path := fmt.Sprintf("%s?chain_id=%s&address=%s&from_block=%d&to_block=%d&page=%d&limit=%d",
		ep, cid, url.QueryEscape(wallet), from, to, pg, pageSize)
	var env envelope
	if err := a.get(ctx, path, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// LookupTransfers advances both stream cursors by at most maxDrainPages
// total upstream GETs (native first, then token). A stream emits its
// buffered rows — sorted ascending — only when its next_page chain
// exhausts; rows emit again on each fresh refresh (PageToken="").
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "chainbase: disabled for %s", net.Code)
	}
	if opts.PageToken != "" {
		var tok tokenState
		if err := json.Unmarshal([]byte(opts.PageToken), &tok); err != nil || tok.V != 1 {
			return nil, provider.Errorf(provider.ErrMalformed, "chainbase: bad page token")
		}
	}
	key := strings.ToLower(net.Code) + "|" + wallet + "|" + strconv.FormatInt(opts.FromBlock, 10)
	a.mu.Lock()
	a.evict(time.Now())
	st, ok := a.scans[key]
	if !ok {
		st = &scanState{nPage: 1, tPage: 1}
		a.scans[key] = st
	}
	st.touched = time.Now()
	a.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if opts.PageToken == "" {
		st.nEmit, st.tEmit = false, false // refresh boundary: rows emit again
	}
	st.head = opts.HeadBlock
	drop := func(err error) (*provider.TransferPage, error) {
		a.mu.Lock()
		delete(a.scans, key)
		a.mu.Unlock()
		return nil, err
	}

	res := &provider.TransferPage{}
	budget := a.maxDrainPages
	for budget > 0 && (!st.nDone || !st.tDone) {
		if !st.nDone {
			env, err := a.fetchPage(ctx, net, wallet, false, st.nPage, opts.FromBlock, opts.ToBlock)
			if err != nil {
				return nil, err
			}
			res.UpstreamCalls++
			budget--
			var rows []acctTx
			if err := json.Unmarshal(env.Data, &rows); err != nil {
				return drop(provider.Errorf(provider.ErrMalformed, "chainbase txs: %v", err))
			}
			for _, r := range rows {
				if t, ok := mapNative(r, st.head); ok {
					st.nRows = append(st.nRows, t)
				}
			}
			if env.NextPage == nil || *env.NextPage <= st.nPage || len(rows) == 0 {
				st.nDone = true
			} else {
				st.nPage = *env.NextPage
			}
		} else {
			env, err := a.fetchPage(ctx, net, wallet, true, st.tPage, opts.FromBlock, opts.ToBlock)
			if err != nil {
				return nil, err
			}
			res.UpstreamCalls++
			budget--
			var rows []tokenTx
			if err := json.Unmarshal(env.Data, &rows); err != nil {
				return drop(provider.Errorf(provider.ErrMalformed, "chainbase token transfers: %v", err))
			}
			for _, r := range rows {
				if t, ok := mapToken(r, st.head); ok {
					st.tRows = append(st.tRows, t)
				}
			}
			if env.NextPage == nil || *env.NextPage <= st.tPage || len(rows) == 0 {
				st.tDone = true
			} else {
				st.tPage = *env.NextPage
			}
		}
		if len(st.nRows)+len(st.tRows) > a.maxBufferRows {
			return drop(provider.Errorf(provider.ErrInternal,
				"chainbase: scan buffer cap %d exceeded", a.maxBufferRows))
		}
	}

	emit := func(rows []provider.Transfer, done bool) []provider.Transfer {
		out := make([]provider.Transfer, 0, len(rows))
		for _, t := range rows {
			if t.BlockNumber >= opts.FromBlock && t.BlockNumber <= opts.ToBlock {
				out = append(out, t)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].BlockNumber < out[j].BlockNumber })
		return out
	}
	if st.nDone && !st.nEmit {
		st.nEmit = true
		res.Transfers = append(res.Transfers, emit(st.nRows, true)...)
	}
	if st.tDone && !st.tEmit {
		st.tEmit = true
		res.Transfers = append(res.Transfers, emit(st.tRows, true)...)
	}
	res.Streams = []provider.StreamStatus{
		{Name: streamNative, Finished: st.nDone},
		{Name: streamToken, Finished: st.tDone},
	}
	if !st.nDone || !st.tDone {
		b, _ := json.Marshal(tokenState{V: 1, NP: st.nPage, TP: st.tPage})
		res.NextPageToken = string(b)
	}
	return res, nil
}
