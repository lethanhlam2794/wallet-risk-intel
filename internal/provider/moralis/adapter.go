// Package moralis is the transaction-history adapter over Moralis
// Web3 API v2.2 (deep-index.moralis.io) — bsc only. Two bounded
// endpoints: GET /v2.2/{addr} (native txs, order=ASC) and
// GET /v2.2/{addr}/erc20/transfers (token, order=ASC) — both accept
// from_block/to_block + opaque cursor, so each stream is an honest
// ascending prefix with a server-side window (no local buffer needed).
// Auth: X-API-Key header.
package moralis

import (
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
const SourceCode = "moralis"

const (
	streamNative = "native"
	streamToken  = "token"
)

var defaultAllowedHosts = []string{"deep-index.moralis.io"}

// chains maps our code to the Moralis chain slug.
var chains = map[string]string{"bsc": "bsc"}

// Adapter implements provider.TxProvider over Moralis.
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
		return nil, fmt.Errorf("moralis: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("moralis: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("moralis: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("moralis: host %q not in allowlist", u.Host)
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

// get performs one authenticated GET.
func (a *Adapter) get(ctx context.Context, path string, q url.Values, out any) error {
	u := a.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "moralis: build request: %v", err)
	}
	req.Header.Set("X-API-Key", a.apiKey)
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "moralis %s: %v", path, err)
	}
	return nil
}

// HeadBlock returns the upstream-observed head via the dateToBlock
// endpoint (latest ≈ now).
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "moralis: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	var out struct {
		Block int64 `json:"block"`
	}
	if err := a.get(ctx, "/api/v2.2/dateToBlock",
		url.Values{"chain": {chain}}, &out); err != nil {
		return 0, err
	}
	if out.Block <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "moralis: bad head %d", out.Block)
	}
	return out.Block, nil
}

// pageToken carries the two per-stream opaque cursors.
type pageToken struct {
	Native string `json:"n,omitempty"`
	Token  string `json:"t,omitempty"`
}

func encodeToken(t pageToken) string {
	b, _ := json.Marshal(t)
	return string(b)
}

// LookupTransfers fetches one page per unfinished stream (at most 2
// upstream calls), emitting in-window ascending rows. Streams are
// kind-partitioned because the upstream endpoints paginate
// independently — coverage is per-stream honest.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "moralis: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	var tok pageToken
	if opts.PageToken != "" {
		if err := json.Unmarshal([]byte(opts.PageToken), &tok); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "moralis: bad page token")
		}
	}
	page := &provider.TransferPage{}
	finished := map[string]bool{}
	for _, st := range []string{streamNative, streamToken} {
		finished[st] = true // set false when the stream still paginates
	}
	// Native stream.
	if tok.Native != "done" {
		var out nativePage
		q := a.listQuery(chain, opts, tok.Native)
		if err := a.get(ctx, "/api/v2.2/"+url.PathEscape(wallet), q, &out); err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		for _, tr := range out.Result {
			if t, ok := mapNative(tr, opts.HeadBlock); ok {
				page.Transfers = append(page.Transfers, t)
			}
		}
		if out.Cursor != "" && len(out.Result) > 0 {
			tok.Native = out.Cursor
			finished[streamNative] = false
		} else {
			tok.Native = "done"
		}
	}
	// Token stream.
	if tok.Token != "done" {
		var out tokenPage
		q := a.listQuery(chain, opts, tok.Token)
		if err := a.get(ctx, "/api/v2.2/"+url.PathEscape(wallet)+"/erc20/transfers", q, &out); err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		for _, tr := range out.Result {
			if t, ok := mapToken(tr, opts.HeadBlock); ok {
				page.Transfers = append(page.Transfers, t)
			}
		}
		if out.Cursor != "" && len(out.Result) > 0 {
			tok.Token = out.Cursor
			finished[streamToken] = false
		} else {
			tok.Token = "done"
		}
	}
	page.Streams = []provider.StreamStatus{
		{Name: streamNative, Finished: finished[streamNative]},
		{Name: streamToken, Finished: finished[streamToken]},
	}
	if !finished[streamNative] || !finished[streamToken] {
		page.NextPageToken = encodeToken(tok)
	}
	return page, nil
}

func (a *Adapter) listQuery(chain string, opts provider.TransferLookupOpts, cursor string) url.Values {
	q := url.Values{
		"chain":      {chain},
		"from_block": {strconv.FormatInt(opts.FromBlock, 10)},
		"to_block":   {strconv.FormatInt(opts.ToBlock, 10)},
		"order":      {"ASC"},
		"limit":      {"100"},
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
