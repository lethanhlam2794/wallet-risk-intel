// Package covalent is the transaction-history adapter over GoldRush
// Foundational API GET /v1/{chain}/address/{addr}/transactions_v3/page/{n}/
// (api.covalenthq.com) — bsc only. Pages iterate the address's whole
// history 0-indexed EARLIEST-FIRST (stable append pagination): an
// ascending stream may emit its covered prefix honestly — the service
// books coverage as max-block-minus-one, which is true here by
// construction. Bearer auth; log_events carry decoded ERC-20 Transfers.
package covalent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
const SourceCode = "covalent"

// streamTx is the single unified upstream list stream.
const streamTx = "tx"

// defaultAllowedHosts is the SSRF allowlist (spec §16).
var defaultAllowedHosts = []string{"api.covalenthq.com"}

// chains maps our network code to the GoldRush chain slug.
var chains = map[string]string{"bsc": "bsc-mainnet"}

// Adapter implements provider.TxProvider over GoldRush.
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
	maxScans      int
	scanTTL       time.Duration
	mu            sync.Mutex
	scans         map[string]*scanCursor
}

// scanCursor is the process-local walk position for one
// (net|wallet|fetchFrom) scan — ascending page index; cross-refresh
// progress lives here since the service always starts PageToken="".
type scanCursor struct {
	mu        sync.Mutex
	page      int
	exhausted bool // upstream list ended — persists; window-pass does not
	touched   time.Time
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
func WithScanLimits(maxPages, maxScans int, ttl time.Duration) Option {
	return func(a *Adapter) {
		if maxPages > 0 {
			a.maxDrainPages = maxPages
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
	a := &Adapter{
		baseURL:       strings.TrimRight(baseURL, "/"),
		apiKey:        apiKey,
		enabled:       map[string]bool{},
		allowedHosts:  defaultAllowedHosts,
		maxDrainPages: 5,
		maxScans:      256,
		scanTTL:       30 * time.Minute,
		scans:         map[string]*scanCursor{},
	}
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
		Source:          SourceCode,
		Timeout:         15 * time.Second,
		Rate:            rate.Every(500 * time.Millisecond),
		Burst:           1,
		MaxLimiterWait:  4 * time.Second,
		Concurrency:     2,
		MaxRetries:      2,
		Backoff:         300 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    8 << 20,
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("covalent: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("covalent: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("covalent: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("covalent: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network — key-gated
// plus enabled-chains; disabled ⇒ zero traffic.
func (a *Adapter) Enabled(net network.Network) bool {
	if a.apiKey == "" {
		return false
	}
	_, ok := chains[strings.ToLower(net.Code)]
	return ok && a.enabled[strings.ToLower(net.Code)]
}

// get performs one authenticated GET through the resilience client.
func (a *Adapter) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "covalent: build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	var env struct {
		Data         json.RawMessage `json:"data"`
		Error        bool            `json:"error"`
		ErrorMessage string          `json:"error_message"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return provider.Errorf(provider.ErrMalformed, "covalent %s: %v", path, err)
	}
	if env.Error {
		if strings.Contains(strings.ToLower(env.ErrorMessage), "rate") {
			return provider.Errorf(provider.ErrRateLimited, "covalent: %s", env.ErrorMessage)
		}
		return provider.Errorf(provider.ErrDegraded, "covalent: %s", env.ErrorMessage)
	}
	if len(env.Data) == 0 {
		return provider.Errorf(provider.ErrMalformed, "covalent %s: empty data", path)
	}
	return json.Unmarshal(env.Data, out)
}

// HeadBlock returns the upstream-observed head via block_v2/latest.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "covalent: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	var out struct {
		Items []struct {
			Height int64 `json:"height"`
		} `json:"items"`
	}
	if err := a.get(ctx, "/v1/"+chain+"/block_v2/latest/", &out); err != nil {
		return 0, err
	}
	if len(out.Items) == 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "covalent: empty latest block")
	}
	return out.Items[0].Height, nil
}

// LookupTransfers advances the ascending page walk by at most
// maxDrainPages upstream GETs. Pages below the window emit nothing but
// still advance the (net|wallet|fetchFrom) scan cursor — kept
// process-locally so a NEW refresh (PageToken="") resumes the walk
// instead of restarting at page 0, exactly like blockscout's pinned
// model. In-window rows emit directly (ascending prefix = honest
// covered prefix); the stream finishes once a page passes ToBlock or
// links.next is absent.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "covalent: disabled for %s", net.Code)
	}
	if opts.PageToken != "" {
		var tok struct {
			P int `json:"p"`
		}
		if err := json.Unmarshal([]byte(opts.PageToken), &tok); err != nil || tok.P < 0 {
			return nil, provider.Errorf(provider.ErrMalformed, "covalent: bad page token")
		}
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	key := strings.ToLower(net.Code) + "|" + wallet + "|" + strconv.FormatInt(opts.FromBlock, 10)
	a.mu.Lock()
	a.evict(time.Now())
	st, ok := a.scans[key]
	if !ok {
		st = &scanCursor{}
		a.scans[key] = st
	}
	st.touched = time.Now()
	a.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()

	res := &provider.TransferPage{}
	lw := strings.ToLower(wallet)
	finished := st.exhausted
	for drained := 0; drained < a.maxDrainPages && !finished; drained++ {
		var out txPage
		path := fmt.Sprintf("/v1/%s/address/%s/transactions_v3/page/%d/?block-signed-at-asc=true",
			chain, url.PathEscape(wallet), st.page)
		if err := a.get(ctx, path, &out); err != nil {
			return nil, err
		}
		res.UpstreamCalls++
		st.page++
		if out.Links.Next == nil || *out.Links.Next == "" || len(out.Items) == 0 {
			st.exhausted, finished = true, true
		}
		for _, tr := range out.Items {
			if tr.BlockHeight > opts.ToBlock {
				finished = true // ascending: window passed — NOT upstream exhaustion
				break
			}
			if tr.BlockHeight < opts.FromBlock {
				continue
			}
			res.Transfers = append(res.Transfers, mapTx(lw, tr, opts.HeadBlock)...)
		}
	}
	res.Streams = []provider.StreamStatus{{Name: streamTx, Finished: finished}}
	if !finished {
		res.NextPageToken = fmt.Sprintf(`{"p":%d}`, st.page)
	}
	return res, nil
}
