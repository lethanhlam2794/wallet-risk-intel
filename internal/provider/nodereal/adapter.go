// Package nodereal is the transaction-history adapter over NodeReal
// MegaNode's nr_getAssetTransfers JSON-RPC (bsc-mainnet.nodereal.io) —
// plan §3.1. Streams are direction-partitioned (fromAddress / toAddress);
// categories are pinned to "external" (native BNB) and "20" (BEP-20) with
// excludeZeroValue=true — INTERNAL and NFT classes are out of scope in MVP.
// The free tier is ~150 CUPS at 250 CU/call ⇒ the limiter runs a
// conservative 0.6 calls/s; the toBlock−fromBlock span is hard-capped at
// 100,000 blocks upstream (the indexer's window cap enforces it).
package nodereal

import (
	"bytes"
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
const SourceCode = "nodereal"

const (
	streamFrom = "from"
	streamTo   = "to"
)

// defaultAllowedHosts is the SSRF allowlist (spec §16) — hardcoded per F6.
var defaultAllowedHosts = []string{"bsc-mainnet.nodereal.io"}

// chains is the adapter's served set: nr_getAssetTransfers is wired for
// BSC only in MVP.
var chains = map[string]bool{"bsc": true}

// Adapter implements provider.TxProvider over NodeReal.
type Adapter struct {
	baseURL      string
	apiKey       string
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

// WithDurationFunc attaches a call-site latency observer.
func WithDurationFunc(f func(time.Duration)) Option {
	return func(a *Adapter) { a.onDuration = f }
}

// New validates the base URL and builds the resilience client. Rate is a
// conservative 0.6 calls/s — the free-tier CUP budget is the binding
// constraint on BSC hop-2 by design (plan §3.1).
func New(baseURL, apiKey string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		allowedHosts: defaultAllowedHosts,
	}
	for _, o := range opts {
		o(a)
	}
	if err := a.validateBaseURL(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         10 * time.Second,
		Rate:            rate.Every(1667 * time.Millisecond), // ~0.6 calls/s
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
		return nil, fmt.Errorf("nodereal: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("nodereal: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("nodereal: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("nodereal: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network: BSC only, and
// only when NODEREAL_API_KEY is configured — empty key ⇒ DISABLED.
func (a *Adapter) Enabled(net network.Network) bool {
	return a.apiKey != "" && chains[strings.ToLower(net.Code)]
}

// rpcReq is one JSON-RPC call. id is fixed — calls are sequential.
type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call performs one JSON-RPC request through the resilience client.
func (a *Adapter) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "nodereal: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/"+a.apiKey, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "nodereal: build request: %v", err)
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
	var env rpcResp
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "nodereal %s: %v", method, err)
	}
	if env.Error != nil {
		msg := env.Error.Message
		if strings.Contains(strings.ToLower(msg), "rate") ||
			env.Error.Code == -32005 || env.Error.Code == 429 {
			return nil, provider.Errorf(provider.ErrRateLimited, "nodereal %s: %s", method, msg)
		}
		return nil, provider.Errorf(provider.ErrDegraded, "nodereal %s: rpc error %d: %s",
			method, env.Error.Code, msg)
	}
	if len(env.Result) == 0 {
		return nil, provider.Errorf(provider.ErrMalformed, "nodereal %s: empty result", method)
	}
	return env.Result, nil
}

// HeadBlock fetches eth_blockNumber.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "nodereal: %s not enabled", net.Code)
	}
	raw, err := a.call(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	var hexNum string
	if err := json.Unmarshal(raw, &hexNum); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "nodereal head: %v", err)
	}
	head, err := strconv.ParseInt(strings.TrimPrefix(hexNum, "0x"), 16, 64)
	if err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "nodereal head: bad result %q", hexNum)
	}
	return head, nil
}

// pageToken encodes the two direction cursors; nil = finished stream.
type pageToken struct {
	From *string `json:"a,omitempty"`
	To   *string `json:"b,omitempty"`
}

func encodeToken(t pageToken) string {
	if t.From == nil && t.To == nil {
		return ""
	}
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (pageToken, error) {
	start := ""
	if s == "" {
		return pageToken{From: &start, To: &start}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "nodereal: bad page token")
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "nodereal: bad page token")
	}
	return t, nil
}

// transferParams is the nr_getAssetTransfers argument shape.
type transferParams struct {
	FromBlock        string   `json:"fromBlock"`
	ToBlock          string   `json:"toBlock"`
	FromAddress      string   `json:"fromAddress,omitempty"`
	ToAddress        string   `json:"toAddress,omitempty"`
	Category         []string `json:"category"`
	ExcludeZeroValue bool     `json:"excludeZeroValue"`
	MaxCount         string   `json:"maxCount"`
	PageKey          string   `json:"pageKey,omitempty"`
}

type transfersResult struct {
	Transfers []nrTransfer `json:"transfers"`
	PageKey   string       `json:"pageKey"`
}

// LookupTransfers performs one stream-pair step: one page per active
// direction stream (from + to = up to two upstream calls).
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network,
	normalized string, opts provider.TransferLookupOpts) (*provider.TransferPage, error) {

	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "nodereal: %s not enabled", net.Code)
	}
	tok, err := decodeToken(opts.PageToken)
	if err != nil {
		return nil, err
	}
	size := opts.PageSize
	if size <= 0 || size > 1000 {
		size = 1000 // upstream maxCount cap is 0x3E8
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
	finished := map[string]bool{streamFrom: tok.From == nil, streamTo: tok.To == nil}
	head := opts.HeadBlock

	for _, dir := range []struct {
		name string
		key  **string
	}{
		{streamFrom, &tok.From},
		{streamTo, &tok.To},
	} {
		if *dir.key == nil {
			continue
		}
		params := transferParams{
			FromBlock:        "0x" + strconv.FormatInt(opts.FromBlock, 16),
			ToBlock:          "0x" + strconv.FormatInt(opts.ToBlock, 16),
			Category:         []string{"external", "20"},
			ExcludeZeroValue: true,
			MaxCount:         "0x" + strconv.FormatInt(int64(size), 16),
			PageKey:          **dir.key,
		}
		if dir.name == streamFrom {
			params.FromAddress = normalized
		} else {
			params.ToAddress = normalized
		}
		// NodeReal rejects object params with -32700; wrap in the
		// JSON-RPC positional array form.
		raw, err := a.call(ctx, "nr_getAssetTransfers", []any{params})
		if err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		var tr transfersResult
		if err := json.Unmarshal(raw, &tr); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "nodereal transfers: %v", err)
		}
		for _, r := range tr.Transfers {
			t, ok := mapTransfer(r, dir.name, head, wantNative, wantToken)
			if ok {
				page.Transfers = append(page.Transfers, t)
			}
		}
		if tr.PageKey == "" {
			*dir.key = nil
		} else {
			**dir.key = tr.PageKey
		}
		finished[dir.name] = *dir.key == nil
	}

	page.NextPageToken = encodeToken(tok)
	page.Streams = []provider.StreamStatus{
		{Name: streamFrom, Finished: finished[streamFrom]},
		{Name: streamTo, Finished: finished[streamTo]},
	}
	return page, nil
}

// init publishes the default SSRF allowlist so non-adapter callers
// (config resolution, admin writes) apply the same host check.
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }
