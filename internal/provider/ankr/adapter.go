// Package ankr is the transaction-history adapter over Ankr Advanced
// API's ankr_getTransactionsByAddress (rpc.ankr.com/multichain/{key}) —
// ethereum + bsc. The upstream list is a UNIFIED per-address stream
// (from-or-to, both kinds once includeLogs is set) so the adapter
// declares a single "tx" stream: partitioning a unified list would fake
// coverage. params is an OBJECT (per Ankr docs — not a positional
// array); rows arrive ascending when descOrder=false; hex-encoded
// amounts/timestamps/blocks decode to NUMERIC-faithful decimals.
package ankr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
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
const SourceCode = "ankr"

// streamTx is the single unified upstream list stream.
const streamTx = "tx"

// defaultAllowedHosts is the SSRF allowlist (spec §16).
var defaultAllowedHosts = []string{"rpc.ankr.com"}

// chains is the adapter's served set; ankrChain maps to the upstream
// blockchain token used in params.blockchain.
var chains = map[string]string{"ethereum": "eth", "bsc": "bsc"}

// Adapter implements provider.TxProvider over Ankr Advanced API.
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
	a := &Adapter{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		enabled:      map[string]bool{},
		allowedHosts: defaultAllowedHosts,
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
		return nil, fmt.Errorf("ankr: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("ankr: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("ankr: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("ankr: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network — key-gated
// plus the enabled-chains list; a disabled adapter emits no traffic.
func (a *Adapter) Enabled(net network.Network) bool {
	if a.apiKey == "" {
		return false
	}
	_, ok := chains[strings.ToLower(net.Code)]
	return ok && a.enabled[strings.ToLower(net.Code)]
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call performs one JSON-RPC POST through the resilience client. The API
// key lives in the URL path (Ankr convention), never in headers/body.
// pathBase carries the endpoint family: the Advanced-API multichain
// base for lookups, the chain-scoped Node-API base for head.
func (a *Adapter) call(ctx context.Context, pathBase, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "ankr: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		pathBase+"/"+a.apiKey, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "ankr: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return nil, err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcErr         `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "ankr %s: %v", method, err)
	}
	if env.Error != nil {
		msg := env.Error.Message
		if env.Error.Code == -32005 || env.Error.Code == 429 ||
			strings.Contains(strings.ToLower(msg), "rate") {
			return nil, provider.Errorf(provider.ErrRateLimited, "ankr %s: %s", method, msg)
		}
		return nil, provider.Errorf(provider.ErrDegraded, "ankr %s: rpc error %d: %s",
			method, env.Error.Code, msg)
	}
	if len(env.Result) == 0 {
		return nil, provider.Errorf(provider.ErrMalformed, "ankr %s: empty result", method)
	}
	return env.Result, nil
}

// HeadBlock returns the upstream-observed head via the chain-scoped
// Node-API endpoint (rpc.ankr.com/{chain}/{key}) — multichain serves
// Query-API methods only.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "ankr: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	base := strings.TrimSuffix(a.baseURL, "/multichain") + "/" + chain
	raw, err := a.call(ctx, base, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	n, ok := hexToBig(string(raw))
	if !ok {
		return 0, provider.Errorf(provider.ErrMalformed, "ankr head: bad hex %s", string(raw))
	}
	return n.Int64(), nil
}

// LookupTransfers fetches the next ascending page of the unified tx
// list. PageToken is the upstream nextPageToken verbatim (already
// opaque); empty upstream token + a returned page ⇒ stream finished.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "ankr: disabled for %s", net.Code)
	}
	chain, _ := chains[strings.ToLower(net.Code)]
	pageSize := opts.PageSize
	if pageSize <= 0 || pageSize > 10000 {
		pageSize = 1000
	}
	params := map[string]any{
		"address":     wallet,
		"blockchain":  []string{chain},
		"fromBlock":   opts.FromBlock,
		"toBlock":     opts.ToBlock,
		"descOrder":   false,
		"includeLogs": true,
		"pageSize":    pageSize,
	}
	if opts.PageToken != "" {
		params["pageToken"] = opts.PageToken
	}
	raw, err := a.call(ctx, a.baseURL, "ankr_getTransactionsByAddress", params)
	if err != nil {
		return nil, err
	}
	var res struct {
		Transactions []txRow `json:"transactions"`
		NextToken    string  `json:"nextPageToken"`
		SyncStatus   *struct {
			Status string `json:"status"`
		} `json:"syncStatus"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "ankr: result: %v", err)
	}
	// An out-of-sync indexer is an honest degraded signal, not rows.
	if res.SyncStatus != nil && strings.EqualFold(res.SyncStatus.Status, "outOfSync") {
		return nil, provider.Errorf(provider.ErrDegraded, "ankr: indexer outOfSync")
	}
	page := &provider.TransferPage{UpstreamCalls: 1}
	lw := strings.ToLower(wallet)
	for _, tr := range res.Transactions {
		rows := mapTx(lw, tr, opts.HeadBlock)
		page.Transfers = append(page.Transfers, rows...)
	}
	page.Streams = []provider.StreamStatus{{Name: streamTx, Finished: res.NextToken == ""}}
	if res.NextToken != "" {
		page.NextPageToken = res.NextToken
	}
	return page, nil
}

// hexToBig decodes a 0x-hex quantity to big.Int; accepts a bare JSON
// string (with quotes trimmed) or raw bytes.
func hexToBig(s string) (*big.Int, bool) {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if !strings.HasPrefix(s, "0x") {
		return nil, false
	}
	n, ok := new(big.Int).SetString(s[2:], 16)
	return n, ok
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
