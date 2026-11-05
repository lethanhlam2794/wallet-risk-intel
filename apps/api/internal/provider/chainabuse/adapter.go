package chainabuse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key.
const SourceCode = "chainabuse"

// defaultAllowedHosts is the SSRF allowlist (spec §16, F6).
var defaultAllowedHosts = []string{"api.chainabuse.com"}

// Adapter implements provider.Provider for Chainabuse.
type Adapter struct {
	baseURL      string
	apiKey       string
	allowedHosts []string
	insecureHTTP bool
	hooks        resilience.Hooks
	resOpts      []resilience.Option
	ratePerMin   int
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

// New validates the base URL and builds the resilience client (timeout 8s,
// concurrency 1, retry ≤1, breaker 3/120s, limiter default 1/min — the free
// tier is ~10 calls/month; env-tunable for partner keys).
func New(baseURL, apiKey string, ratePerMin int, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		allowedHosts: defaultAllowedHosts,
		ratePerMin:   ratePerMin,
	}
	for _, o := range opts {
		o(a)
	}
	if a.ratePerMin <= 0 {
		a.ratePerMin = 1
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         8 * time.Second,
		Rate:            rate.Every(time.Minute / time.Duration(a.ratePerMin)),
		Burst:           1,
		Concurrency:     1,
		MaxRetries:      1,
		Backoff:         500 * time.Millisecond,
		BreakerFailures: 3,
		BreakerOpen:     120 * time.Second,
		MaxBodyBytes:    4 << 20,
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("chainabuse: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("chainabuse: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("chainabuse: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("chainabuse: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether outbound calls are permitted — false without an
// API key (spec §4.5: only with access). The Authorization header is never
// logged (spec §16).
func (a *Adapter) Enabled() bool { return a.apiKey != "" }

// LookupAddress fetches community reports. A disabled adapter returns a
// typed disabled error without touching the wire.
func (a *Adapter) LookupAddress(ctx context.Context, net network.Network,
	normalized string) (*provider.LookupResult, error) {

	if !a.Enabled() {
		return nil, provider.Errorf(provider.ErrDisabled,
			"chainabuse disabled: no_api_key")
	}
	u, err := url.Parse(a.baseURL + "/v0/reports")
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "chainabuse: url: %v", err)
	}
	q := u.Query()
	q.Set("addresses", normalized)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "chainabuse: request: %v", err)
	}
	req.SetBasicAuth(a.apiKey, a.apiKey) // docs: API key as both user and pass

	res, err := a.cli.Do(ctx, req)
	if err != nil {
		provider.StampRequestedChain(err, net.Code)
		return nil, err // already typed
	}
	fetchedAt := time.Now()
	var reports []report
	if err := json.Unmarshal(res.Body, &reports); err != nil {
		lerr := provider.Errorf(provider.ErrMalformed, "chainabuse: %v", err)
		lerr.RequestedChain = net.Code
		return nil, lerr
	}
	return &provider.LookupResult{
		Source:         SourceCode,
		Network:        net.Code,
		Address:        normalized,
		RequestedChain: net.Code,
		Observations:   mapReports(net, normalized, reports, fetchedAt),
		Signals:        map[string]any{"report_count": len(reports)},
		Raw:            json.RawMessage(res.Body),
		FetchedAt:      fetchedAt,
		HTTPStatus:     res.StatusCode,
	}, nil
}
