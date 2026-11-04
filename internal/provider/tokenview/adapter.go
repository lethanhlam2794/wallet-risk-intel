// Package tokenview is the transaction-history adapter over Tokenview
// (services.tokenview.io/vipapi) — eth + bsc, keyed.
// The normal-tx list is NEWEST-FIRST with no block-range params and a
// hard ceiling (page ≤ 50 × size ≤ 50), so the adapter mirrors the
// blockscout drain/buffer model with a single scan feeding both streams:
// native rows derive from each tx's normalized value, token rows from
// the nested tokenTransfer entries (raw integers upstream). Reaching
// page 51 is an ARTIFICIAL cap — the stream stays unfinished (honest
// permanent PARTIAL), never claimed as covered.
// Auth: apikey query param. Head via /vipapi/{coin}/block/last.
package tokenview

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
const SourceCode = "tokenview"

const (
	streamNative = "native"
	streamToken  = "token"
	pageSize     = 50
	maxPage      = 50 // upstream hard ceiling — deeper history unreachable
)

var defaultAllowedHosts = []string{"services.tokenview.io"}

// chains maps our code to the Tokenview coin abbreviation.
var chains = map[string]string{"ethereum": "eth", "bsc": "bsc"}

// Adapter implements provider.TxProvider over Tokenview.
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

	maxScanPages  int
	maxBufferRows int
	maxScans      int
	scanTTL       time.Duration

	mu    sync.Mutex
	scans map[string]*scanState
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

// WithScanLimits tunes the drain/buffer bounds (tests shrink them).
func WithScanLimits(maxPages, maxRows, maxScans int, ttl time.Duration) Option {
	return func(a *Adapter) {
		if maxPages > 0 {
			a.maxScanPages = maxPages
		}
		if maxRows > 0 {
			a.maxBufferRows = maxRows
		}
		if maxScans > 0 {
			a.maxScans = maxScans
		}
		if ttl > 0 {
			a.scanTTL = ttl
		}
	}
}

// New validates the base URL and builds the resilience client.
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey,
		enabled: map[string]bool{}, allowedHosts: defaultAllowedHosts,
		maxScanPages: 10, maxBufferRows: 20000, maxScans: 256,
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
		return nil, fmt.Errorf("tokenview: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("tokenview: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("tokenview: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("tokenview: host %q not in allowlist", u.Host)
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

// get performs one authenticated GET; code != 1 in the envelope is a
// provider-level signal → degraded, never a clean empty page.
func (a *Adapter) get(ctx context.Context, path string, out *listEnvelope) error {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	u := fmt.Sprintf("%s%s%sapikey=%s", a.baseURL, path, sep, url.QueryEscape(a.apiKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "tokenview: build request: %v", err)
	}
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "tokenview %s: %v", path, err)
	}
	if out.Code != 1 {
		return provider.Errorf(provider.ErrDegraded, "tokenview: code %d: %s", out.Code, out.Msg)
	}
	return nil
}

// HeadBlock returns the upstream-observed head via /{coin}/block/last.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "tokenview: disabled for %s", net.Code)
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Height int64 `json:"height"`
		} `json:"data"`
	}
	path := "/vipapi/" + chains[strings.ToLower(net.Code)] + "/block/last"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+path+"?apikey="+url.QueryEscape(a.apiKey), nil)
	if err != nil {
		return 0, provider.Errorf(provider.ErrInternal, "tokenview: build request: %v", err)
	}
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "tokenview head: %v", err)
	}
	if out.Code != 1 {
		return 0, provider.Errorf(provider.ErrDegraded, "tokenview: code %d: %s", out.Code, out.Msg)
	}
	if out.Data.Height <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "tokenview: bad head %d", out.Data.Height)
	}
	return out.Data.Height, nil
}

// scanState is the process-local buffer for one (net,wallet,fetchFrom)
// descending scan over the single unified list — both streams share the
// coverage boundary because the rows come from the same pages.
type scanState struct {
	mu        sync.Mutex
	touched   time.Time
	page      int  // next page to fetch, 1-based
	exhausted bool // empty page ≤50 — genuine end of reachable history
	capped    bool // page 51 ceiling hit while rows still flowed — dead end
	floor     int64
	floorSet  bool
	head      int64 // fetchTo joined so far
	headPage  int   // head catch-up page cursor (0 = not draining)
	// headStalled is the smallest fetchTo whose head catch-up exhausted
	// the upstream page cap without joining — any fetchTo ≥ it needs
	// strictly more pages, so it stays unprovable (dead end, no re-burn).
	headStalled int64
	native      []provider.Transfer
	token       []provider.Transfer
	seen        map[string]bool
	emitted     bool
}

func (s *scanState) covered(fetchFrom int64) bool {
	return s.exhausted || (s.floorSet && s.floor < fetchFrom)
}

// tokenState is the serialized continuation in NextPageToken.
type tokenState struct {
	V         int   `json:"v"`
	Page      int   `json:"p"`
	HeadPage  int   `json:"hp,omitempty"`
	Exhausted bool  `json:"e,omitempty"`
	Floor     int64 `json:"f,omitempty"`
	FloorSet  bool  `json:"fs,omitempty"`
	Head      int64 `json:"h"`
}

func scanKey(net network.Network, wallet string, fetchFrom int64) string {
	return net.Code + "|" + wallet + "|" + strconv.FormatInt(fetchFrom, 10)
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

// fetchPage pulls one newest-first page of the unified list.
func (a *Adapter) fetchPage(ctx context.Context, net network.Network, wallet string,
	pg int) ([]txRow, error) {
	path := fmt.Sprintf("/vipapi/address/%s/%s/%d/%d",
		chains[strings.ToLower(net.Code)], url.PathEscape(wallet), pg, pageSize)
	var env listEnvelope
	if err := a.get(ctx, path, &env); err != nil {
		return nil, err
	}
	if len(env.Data) == 0 {
		return nil, nil
	}
	return env.Data[0].Txs, nil
}

// bufferRow appends the mapped native/token rows of one tx to the scan;
// exceeding the row cap is a deterministic bound → internal error.
func (a *Adapter) bufferRow(s *scanState, row txRow, head int64) error {
	if t, ok := mapNative(row, head); ok && !s.seen["n"+t.TxHash] {
		s.seen["n"+t.TxHash] = true
		s.native = append(s.native, t)
	}
	for _, tr := range row.TokenTransfer {
		t, ok := mapToken(row, tr, head)
		if !ok {
			continue
		}
		id := tokenDedup(row, tr)
		if s.seen["t"+id] {
			continue
		}
		s.seen["t"+id] = true
		s.token = append(s.token, t)
	}
	if len(s.native)+len(s.token) > a.maxBufferRows {
		return provider.Errorf(provider.ErrInternal,
			"tokenview: scan buffer cap %d exceeded", a.maxBufferRows)
	}
	return nil
}

// LookupTransfers advances the single descending scan by at most
// maxScanPages upstream pages; rows emit once coverage is proven.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "tokenview: disabled for %s", net.Code)
	}
	if opts.PageToken != "" {
		var st tokenState
		if err := json.Unmarshal([]byte(opts.PageToken), &st); err != nil || st.V != 1 {
			return nil, provider.Errorf(provider.ErrMalformed, "tokenview: bad page token")
		}
	}

	a.mu.Lock()
	a.evict(time.Now())
	key := scanKey(net, wallet, opts.FromBlock)
	st, ok := a.scans[key]
	if !ok {
		st = &scanState{touched: time.Now(), page: 1,
			head: opts.ToBlock, seen: map[string]bool{}}
		a.scans[key] = st
	}
	st.touched = time.Now()
	a.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if opts.PageToken == "" {
		st.emitted = false // refresh boundary: rows emit again
	}
	// headBlocked: a previous catch-up proved this window's head segment
	// unreachable within the upstream page cap — dead end, never re-burn.
	headBlocked := func() bool {
		return st.headStalled > 0 && opts.ToBlock >= st.headStalled
	}
	if opts.ToBlock > st.head && st.headPage == 0 && !headBlocked() {
		st.headPage = 1 // drain (head..newTo] newest-first
	}
	drop := func(err error) (*provider.TransferPage, error) {
		a.mu.Lock()
		delete(a.scans, key)
		a.mu.Unlock()
		return nil, err
	}

	calls := 0
	budget := a.maxScanPages
	for budget > 0 && !headBlocked() {
		switch {
		case st.headPage > 0:
			// Head catch-up drains from page 1 until hitting rows ≤ head.
			rows, err := a.fetchPage(ctx, net, wallet, st.headPage)
			if err != nil {
				return nil, err
			}
			calls++
			budget--
			done := len(rows) == 0
			for _, row := range rows {
				if row.Height <= st.head {
					done = true
					break
				}
				if row.Height <= opts.ToBlock {
					if err := a.bufferRow(st, row, opts.HeadBlock); err != nil {
						return drop(err)
					}
				}
			}
			if done {
				st.headPage = 0
				st.head = opts.ToBlock
			} else if st.headPage >= maxPage {
				// Catch-up hit the upstream ceiling without reaching a
				// boundary row — the head segment is unprovable. Record
				// the stalled boundary and stop spending: claiming join
				// here would emit rows over an unfetched hole.
				st.headPage = 0
				if st.headStalled == 0 || opts.ToBlock < st.headStalled {
					st.headStalled = opts.ToBlock
				}
			} else {
				st.headPage++
			}
		case !st.exhausted && !st.capped && !st.covered(opts.FromBlock):
			if st.page > maxPage {
				st.capped = true // artificial ceiling — never claimed covered
				break
			}
			rows, err := a.fetchPage(ctx, net, wallet, st.page)
			if err != nil {
				return nil, err
			}
			calls++
			budget--
			if len(rows) == 0 {
				st.exhausted = true
				break
			}
			for _, row := range rows {
				st.floor, st.floorSet = row.Height, true
				if row.Height >= opts.FromBlock && row.Height <= opts.ToBlock {
					if err := a.bufferRow(st, row, opts.HeadBlock); err != nil {
						return drop(err)
					}
				}
				if row.Height < opts.FromBlock {
					break
				}
			}
			st.page++
		default:
			budget = 0
		}
	}

	cov := st.covered(opts.FromBlock)
	// Joined requires the proven boundary to cover this call's fetchTo —
	// a stalled or in-progress catch-up can never claim it.
	fin := cov && st.headPage == 0 && st.head >= opts.ToBlock
	page := &provider.TransferPage{UpstreamCalls: calls,
		Streams: []provider.StreamStatus{
			{Name: streamNative, Finished: fin},
			{Name: streamToken, Finished: fin},
		}}
	if fin && !st.emitted {
		st.emitted = true
		emit := func(rows []provider.Transfer) {
			out := make([]provider.Transfer, 0, len(rows))
			for _, t := range rows {
				if t.BlockNumber >= opts.FromBlock && t.BlockNumber <= opts.ToBlock {
					out = append(out, t)
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i].BlockNumber < out[j].BlockNumber })
			page.Transfers = append(page.Transfers, out...)
		}
		emit(st.native)
		emit(st.token)
	}
	// Continuation only when fetching remains possible: capped or
	// head-stalled (dead ends) carry no token — unfinished streams alone
	// report the partial.
	if !fin && !st.capped && !headBlocked() {
		b, _ := json.Marshal(tokenState{V: 1, Page: st.page, HeadPage: st.headPage,
			Exhausted: st.exhausted, Floor: st.floor, FloorSet: st.floorSet, Head: st.head})
		page.NextPageToken = string(b)
	}
	return page, nil
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
