// Package publicaml is the on-demand PublicAML adapter (spec §4.6) — free
// non-profit keyless tier; PUBLICAML_API_KEY only raises the limit.
// Chain-specific per-address calls only (batch can diverge). Provider
// scores pass through as provider_score, never the internal risk score;
// direct attribution and indirect exposure are kept strictly separate.
package publicaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
const SourceCode = "publicaml"

// defaultAllowedHosts is the SSRF allowlist (spec §16, F6).
var defaultAllowedHosts = []string{"intelapi.publicaml.org"}

// includeFields is the live-verified include list.
var includeFields = []string{
	"aml_score", "axes", "category", "risk", "incidents", "source_of_funds",
}

const topN = 5 // bounds outbound_exposure.items / counterparties

// Adapter implements provider.Provider for PublicAML.
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

// New validates the base URL and builds the resilience client (timeout 10s
// — the endpoint does graph work; concurrency 2; retry ≤2; breaker 5/60s;
// limiter default 15/min under the ~1k/hour public tier).
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
		a.ratePerMin = 15
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         10 * time.Second,
		Rate:            rate.Every(time.Minute / time.Duration(a.ratePerMin)),
		Burst:           1,
		Concurrency:     2,
		MaxRetries:      2,
		Backoff:         300 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    4 << 20,
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("publicaml: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("publicaml: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("publicaml: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("publicaml: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether outbound calls are permitted — keyless tier is
// always usable.
func (a *Adapter) Enabled() bool { return true }

// LookupAddress enriches a single address on its resolved chain. The
// request is per-address and chain-specific by construction (spec §4.6).
func (a *Adapter) LookupAddress(ctx context.Context, net network.Network,
	normalized string) (*provider.LookupResult, error) {

	chain, ok := chainByCode[net.Code]
	if !ok {
		return nil, provider.Errorf(provider.ErrInternal,
			"publicaml: no chain token for network %q", net.Code)
	}
	body, err := json.Marshal(enrichRequest{
		Addresses: []addressRef{{WalletAddress: normalized, Chain: chain}},
		Include:   includeFields,
		TopN:      topN,
	})
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "publicaml: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/v1/enrich", bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "publicaml: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	res, err := a.cli.Do(ctx, req)
	if err != nil {
		provider.StampRequestedChain(err, chain)
		return nil, enrichError(err)
	}
	lr, lerr := a.mapResponse(net, normalized, res.Body)
	if lerr == nil {
		lr.HTTPStatus = res.StatusCode
	} else {
		provider.StampRequestedChain(lerr, chain)
	}
	return lr, lerr
}

// enrichError rewrites a typed transport error to carry the upstream
// {error} message when the provider sent one — same Kind, better Msg.
func enrichError(err error) error {
	var le *provider.LookupError
	if !errors.As(err, &le) || len(le.Response) == 0 {
		return err
	}
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(le.Response, &env) == nil && env.Error != "" {
		cp := *le
		cp.Msg = env.Error
		return &cp
	}
	return err
}

// mapResponse parses the envelope; a top-level {error} on a 200 is a
// degraded provider answer (§4.6), never a clean result.
func (a *Adapter) mapResponse(net network.Network, normalized string,
	body []byte) (*provider.LookupResult, error) {

	fetchedAt := time.Now()
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "publicaml envelope: %v", err)
	}
	if env.Error != "" {
		return nil, provider.Errorf(provider.ErrDegraded, "publicaml: %s", env.Error)
	}
	if len(env.Entities) == 0 {
		// Unknown to the provider — data, not a verdict and not a failure.
		return &provider.LookupResult{
			Source:         SourceCode,
			Network:        net.Code,
			Address:        normalized,
			RequestedChain: requestedChain(net),
			Signals:        map[string]any{},
			Raw:            json.RawMessage(body),
			FetchedAt:      fetchedAt,
		}, nil
	}
	res, lerr := mapEntity(net, normalized, env.Entities[0], fetchedAt)
	if lerr != nil {
		return nil, lerr
	}
	res.Raw = json.RawMessage(body)
	return res, nil
}
