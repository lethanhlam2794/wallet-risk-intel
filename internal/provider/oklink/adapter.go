// Package oklink is the transaction-history adapter over OKLink
// (www.oklink.com/api/v5/explorer) — bsc only. One endpoint serves both
// streams via protocolType=transaction|token_20 with explicit
// startBlockHeight/endBlockHeight bounds; page numbers are 1-based and
// the response carries totalPage for honest continuation. Auth:
// Ok-Access-Key header. Head via /blockchain/block.
package oklink

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
const SourceCode = "oklink"

const (
	streamNative = "native"
	streamToken  = "token"
	pageSize     = 100
	listPath     = "/api/v5/explorer/address/transaction-list"
	blockPath    = "/api/v5/explorer/blockchain/block"
)

var defaultAllowedHosts = []string{"www.oklink.com"}

// chains maps our code to the OKLink chainShortName.
var chains = map[string]string{"bsc": "bsc"}

// Adapter implements provider.TxProvider over OKLink.
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
		return nil, fmt.Errorf("oklink: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("oklink: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("oklink: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("oklink: host %q not in allowlist", u.Host)
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

// get performs one authenticated GET; a non-"0" code in the envelope is
// a provider-level signal → degraded, never a clean empty page.
func (a *Adapter) get(ctx context.Context, path string, q url.Values, out *envelope) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "oklink: build request: %v", err)
	}
	req.Header.Set("Ok-Access-Key", a.apiKey)
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "oklink %s: %v", path, err)
	}
	if out.Code != "0" {
		return provider.Errorf(provider.ErrDegraded, "oklink: code %s: %s", out.Code, out.Msg)
	}
	return nil
}

// HeadBlock returns the upstream-observed head via /blockchain/block.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "oklink: disabled for %s", net.Code)
	}
	var env envelope
	if err := a.get(ctx, blockPath, url.Values{
		"chainShortName": {chains[strings.ToLower(net.Code)]},
	}, &env); err != nil {
		return 0, err
	}
	if len(env.Data) == 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "oklink: empty head")
	}
	head, err := strconv.ParseInt(env.Data[0].Height, 10, 64)
	if err != nil || head <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "oklink: bad head %q",
			env.Data[0].Height)
	}
	return head, nil
}

// headRow reuses okData for the head block shape (data[0].height).
func (a *Adapter) pageTokenDecode(raw string) (pageToken, error) {
	tok := pageToken{Native: 1, Token: 1} // pages are 1-based
	if raw == "" {
		return tok, nil
	}
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return tok, provider.Errorf(provider.ErrMalformed, "oklink: bad page token")
	}
	return tok, nil
}

// pageToken carries the two per-stream page numbers (0 = done).
type pageToken struct {
	Native int `json:"n"`
	Token  int `json:"t"`
}

// LookupTransfers fetches one page per unfinished stream (at most 2
// upstream calls); each stream pins protocolType so the upstream
// partitioning stays honest.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "oklink: disabled for %s", net.Code)
	}
	tok, err := a.pageTokenDecode(opts.PageToken)
	if err != nil {
		return nil, err
	}
	page := &provider.TransferPage{}
	finished := map[string]bool{streamNative: true, streamToken: true}
	for _, st := range []struct {
		name    string
		proto   string
		page    int
		setNext func(int)
	}{
		{streamNative, "transaction", tok.Native, func(v int) { tok.Native = v }},
		{streamToken, "token_20", tok.Token, func(v int) { tok.Token = v }},
	} {
		if st.page <= 0 {
			continue
		}
		var env envelope
		if err := a.get(ctx, listPath, url.Values{
			"chainShortName":   {chains[strings.ToLower(net.Code)]},
			"address":          {wallet},
			"protocolType":     {st.proto},
			"startBlockHeight": {strconv.FormatInt(opts.FromBlock, 10)},
			"endBlockHeight":   {strconv.FormatInt(opts.ToBlock, 10)},
			"page":             {strconv.Itoa(st.page)},
			"limit":            {strconv.Itoa(pageSize)},
		}, &env); err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		var rows []okRow
		var totalPage int
		if len(env.Data) > 0 {
			rows = env.Data[0].rows()
			totalPage, _ = strconv.Atoi(env.Data[0].TotalPage)
		}
		for _, row := range rows {
			if t, ok := mapRow(row, st.name, opts.HeadBlock); ok {
				page.Transfers = append(page.Transfers, t)
			}
		}
		if len(rows) > 0 && st.page < totalPage {
			st.setNext(st.page + 1)
			finished[st.name] = false
		} else {
			st.setNext(0)
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

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
