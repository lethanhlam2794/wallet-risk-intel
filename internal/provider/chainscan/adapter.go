// Package chainscan is the transaction-history adapter over the Etherscan
// V2 unified API (api.etherscan.io/v2/api?chainid=…) — plan §3.1. Streams
// are kind-partitioned: txlist (native transfers; value>0 filtered client
// side — a scope exclusion, not a skip) and tokentx (fungible token
// transfers; no isError upstream → token rows report is_error=false).
// INTERNAL transfers are out of scope in MVP (plan §6); bsc is DISABLED by
// default (chainid 56 is paid-tier-only) and enabled via config.
package chainscan

import (
	"context"
	"encoding/base64"
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
const SourceCode = "chainscan"

const (
	streamTxlist  = "txlist"
	streamTokentx = "tokentx"

	// recordCap is the upstream bound on retrievable records per query
	// (page*offset ≤ 10,000). A full page at the cap is ambiguous with
	// stream end — never reported as finished.
	recordCap = 10000
)

// defaultAllowedHosts is the SSRF allowlist (spec §16) — hardcoded per F6.
var defaultAllowedHosts = []string{"api.etherscan.io"}

// Adapter implements provider.TxProvider over Etherscan V2.
type Adapter struct {
	baseURL      string
	apiKey       string
	chains       map[string]bool
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

// WithResilienceOption forwards a construction-time knob to the toolkit
// (test-only clock/transport injection).
func WithResilienceOption(o resilience.Option) Option {
	return func(a *Adapter) { a.resOpts = append(a.resOpts, o) }
}

// WithDurationFunc attaches a call-site latency observer
// (provider_request_duration_seconds — resilience Hooks carries no
// duration field, so adapters time Do() themselves).
func WithDurationFunc(f func(time.Duration)) Option {
	return func(a *Adapter) { a.onDuration = f }
}

// New validates the base URL and builds the resilience client (~3 req/s
// sustained — Etherscan free tier is 5/s; the limiter paces, not queues).
// enabledChains carries lowercased network codes the adapter may serve.
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		chains:       map[string]bool{},
		allowedHosts: defaultAllowedHosts,
	}
	for _, c := range enabledChains {
		a.chains[strings.ToLower(c)] = true
	}
	for _, o := range opts {
		o(a)
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         8 * time.Second,
		Rate:            rate.Every(350 * time.Millisecond), // ~3 req/s
		Burst:           1,
		MaxLimiterWait:  2 * time.Second,
		Concurrency:     4,
		MaxRetries:      2,
		Backoff:         200 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    8 << 20,
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("chainscan: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("chainscan: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("chainscan: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("chainscan: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network: an API key is
// required and the chain must be enabled in config.
func (a *Adapter) Enabled(net network.Network) bool {
	return a.apiKey != "" && a.chains[strings.ToLower(net.Code)]
}

// HeadBlock fetches eth_blockNumber via the proxy module.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "chainscan: %s not enabled", net.Code)
	}
	q := url.Values{
		"chainid": {net.ChainReference},
		"module":  {"proxy"},
		"action":  {"eth_blockNumber"},
		"apikey":  {a.apiKey},
	}
	res, err := a.get(ctx, q)
	if err != nil {
		return 0, err
	}
	var env rpcEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "chainscan head: %v", err)
	}
	head, err := strconv.ParseInt(strings.TrimPrefix(env.Result, "0x"), 16, 64)
	if err != nil || env.Result == "" {
		return 0, provider.Errorf(provider.ErrMalformed, "chainscan head: bad result %q", env.Result)
	}
	return head, nil
}

type rpcEnvelope struct {
	Result string          `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// pageToken encodes per-stream page cursors; a nil field = stream exited
// (finished OR record-capped — Capped distinguishes them for coverage).
type pageToken struct {
	Txlist  *int            `json:"a,omitempty"`
	Tokentx *int            `json:"b,omitempty"`
	Capped  map[string]bool `json:"c,omitempty"`
}

func encodeToken(t pageToken) string {
	if t.Txlist == nil && t.Tokentx == nil {
		return ""
	}
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (pageToken, error) {
	one := 1
	if s == "" {
		return pageToken{Txlist: &one, Tokentx: &one}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "chainscan: bad page token")
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "chainscan: bad page token")
	}
	if t.Capped == nil {
		t.Capped = map[string]bool{}
	}
	return t, nil
}

// LookupTransfers performs one stream-pair step: up to one txlist page and
// one tokentx page (a finished stream drops out of the token).
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network,
	normalized string, opts provider.TransferLookupOpts) (*provider.TransferPage, error) {

	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "chainscan: %s not enabled", net.Code)
	}
	tok, err := decodeToken(opts.PageToken)
	if err != nil {
		return nil, err
	}
	size := opts.PageSize
	if size <= 0 {
		size = 1000
	}
	wantNative, wantToken := true, true
	if len(opts.Kinds) > 0 {
		wantNative, wantToken = false, false
		for _, k := range opts.Kinds {
			switch k {
			case provider.TransferKindNative:
				wantNative = true
			case provider.TransferKindToken:
				wantToken = true
			}
		}
	}

	page := &provider.TransferPage{Transfers: []provider.Transfer{}}
	finished := map[string]bool{
		streamTxlist:  tok.Txlist == nil && !tok.Capped[streamTxlist],
		streamTokentx: tok.Tokentx == nil && !tok.Capped[streamTokentx],
	}

	// step advances one stream one page; nil cursor = stream exits the
	// token. A stream exits finished (more=false or kind-filtered) or
	// unfinished (record cap — upstream bounds retrievable records, so
	// a full page at the cap is NOT stream end; coverage must stop at
	// the stream's own max block — review M5).
	step := func(action string, want bool, cur *int) (*int, error) {
		if cur == nil {
			return nil, nil
		}
		if !want {
			finished[action] = true
			return nil, nil
		}
		rows, more, err := a.accountPage(ctx, net, normalized, action,
			*cur, size, opts.FromBlock, opts.ToBlock)
		if err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		if action == streamTxlist {
			for _, r := range rows {
				if keepTxlist(r) {
					page.Transfers = append(page.Transfers, mapTxlist(r))
				}
			}
		} else {
			for _, r := range rows {
				page.Transfers = append(page.Transfers, mapTokentx(r))
			}
		}
		// Upstream bounds retrievable records at recordCap per query —
		// reaching it on ANY page (full or short, dividing or not) means
		// records beyond may exist but are unreachable; the stream is
		// never finished past the cap (review M5 + r2-N2).
		if (*cur-1)*size+len(rows) >= recordCap {
			tok.Capped[action] = true
			return nil, nil
		}
		if !more {
			finished[action] = true
			return nil, nil
		}
		next := *cur + 1
		return &next, nil
	}

	if tok.Txlist, err = step(streamTxlist, wantNative, tok.Txlist); err != nil {
		return nil, err
	}
	if tok.Tokentx, err = step(streamTokentx, wantToken, tok.Tokentx); err != nil {
		return nil, err
	}

	page.NextPageToken = encodeToken(tok)
	page.Streams = []provider.StreamStatus{
		{Name: streamTxlist, Finished: finished[streamTxlist]},
		{Name: streamTokentx, Finished: finished[streamTokentx]},
	}
	return page, nil
}

// accountPage fetches one module=account page. more reports whether the
// page was full (another page may follow).
func (a *Adapter) accountPage(ctx context.Context, net network.Network,
	normalized, action string, pageNo, size int, from, to int64) ([]txRow, bool, error) {

	q := url.Values{
		"chainid":    {net.ChainReference},
		"module":     {"account"},
		"action":     {action},
		"address":    {normalized},
		"startblock": {strconv.FormatInt(from, 10)},
		"endblock":   {strconv.FormatInt(to, 10)},
		"page":       {strconv.Itoa(pageNo)},
		"offset":     {strconv.Itoa(size)},
		"sort":       {"asc"},
		"apikey":     {a.apiKey},
	}
	res, err := a.get(ctx, q)
	if err != nil {
		return nil, false, err
	}
	var env accountEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, false, provider.Errorf(provider.ErrMalformed, "chainscan %s: %v", action, err)
	}
	if env.Status != "1" {
		// status=0: an empty array is a finished stream; a string result is
		// an upstream-side error (rate limit text maps to ErrRateLimited).
		if len(env.Result) > 0 && env.Result[0] == '[' {
			return nil, false, nil
		}
		msg := env.Message + " " + string(env.Result)
		if strings.Contains(strings.ToLower(msg), "rate limit") {
			return nil, false, provider.Errorf(provider.ErrRateLimited, "chainscan %s: %s", action, msg)
		}
		return nil, false, provider.Errorf(provider.ErrDegraded, "chainscan %s: status=%s %s",
			action, env.Status, strings.TrimSpace(msg))
	}
	var rows []txRow
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		return nil, false, provider.Errorf(provider.ErrMalformed, "chainscan %s result: %v", action, err)
	}
	return rows, len(rows) >= size, nil
}

type accountEnvelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

func (a *Adapter) get(ctx context.Context, q url.Values) (*resilience.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "chainscan: build request: %v", err)
	}
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	return res, err
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
