// Package goplus is the on-demand GoPlus Security adapter (spec §4.4) —
// keyless free tier; GOPLUS_API_KEY only raises quota. code != 1 is a
// typed DEGRADED error, never parsed as clean; a "0" flag is data, never
// a safe verdict. Non-personal use requires "Powered by Go+ Security"
// attribution (README note).
package goplus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key.
const SourceCode = "goplus"

// defaultAllowedHosts is the SSRF allowlist (spec §16) — hardcoded in the
// adapter per F6; the configured base URL is validated against it at
// construction, failing fast at startup.
var defaultAllowedHosts = []string{"api.gopluslabs.io"}

// Adapter implements provider.Provider for GoPlus.
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

// Option configures the adapter (test hooks only — production wiring uses
// New with the real base URL).
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

// WithResilienceOption forwards a construction-time knob to the toolkit
// (test-only clock/transport injection).
func WithResilienceOption(o resilience.Option) Option {
	return func(a *Adapter) { a.resOpts = append(a.resOpts, o) }
}

// New validates the base URL against the allowlist + scheme policy and
// builds the resilience client (timeout 8s, concurrency 4, retry ≤2,
// breaker 5/60s — phase-02 policy).
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
		a.ratePerMin = 30 // public tier ≈30 calls/min (plan-verified)
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         8 * time.Second,
		Rate:            rate.Every(time.Minute / time.Duration(a.ratePerMin)),
		Burst:           1,
		Concurrency:     4,
		MaxRetries:      2,
		Backoff:         200 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    4 << 20,
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("goplus: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

// validateBaseURL enforces https (unless the insecure test hook) and the
// hardcoded host allowlist (F6).
func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("goplus: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("goplus: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("goplus: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether outbound calls are permitted — GoPlus is always
// enabled (keyless public tier; a key only raises quota).
func (a *Adapter) Enabled() bool { return true }

// LookupAddress fetches address_security for the normalized address.
func (a *Adapter) LookupAddress(ctx context.Context, net network.Network,
	normalized string) (*provider.LookupResult, error) {

	req, err := a.buildRequest(ctx, net, normalized)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "%v", err)
	}
	res, err := a.cli.Do(ctx, req)
	if err != nil {
		provider.StampRequestedChain(err, net.ChainReference)
		return nil, err // already typed LookupError
	}
	lr, lerr := a.mapResponse(net, normalized, res.Body)
	if lerr == nil {
		lr.HTTPStatus = res.StatusCode
	} else {
		// Map errors (DEGRADED/MALFORMED) still sent the literal token —
		// keep it for the audit row.
		provider.StampRequestedChain(lerr, net.ChainReference)
	}
	return lr, lerr
}

// mapResponse parses the envelope; code != 1 → typed DEGRADED (never
// clean), null/empty result → OK-empty.
func (a *Adapter) mapResponse(net network.Network, normalized string,
	body []byte) (*provider.LookupResult, error) {

	fetchedAt := time.Now()
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "goplus envelope: %v", err)
	}
	if env.Code != 1 {
		return nil, provider.Errorf(provider.ErrDegraded,
			"goplus degraded: code=%d message=%q", env.Code, env.Message)
	}
	obs, signals, lerr := mapResult(net, normalized, env.Result, fetchedAt)
	if lerr != nil {
		return nil, lerr
	}
	return &provider.LookupResult{
		Source:         SourceCode,
		Network:        net.Code,
		Address:        normalized,
		RequestedChain: net.ChainReference,
		Observations:   obs,
		Signals:        signals,
		Raw:            json.RawMessage(body),
		FetchedAt:      fetchedAt,
	}, nil
}
