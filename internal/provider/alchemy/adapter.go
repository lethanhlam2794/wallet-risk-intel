// Package alchemy is the transaction-history adapter over Alchemy's
// alchemy_getAssetTransfers JSON-RPC ({net}-mainnet.g.alchemy.com/v2) —
// plan phase-01. Streams are direction-partitioned (fromAddress /
// toAddress) like nodereal; categories are pinned to "external" (native)
// and "erc20" (token) with excludeZeroValue=true — INTERNAL and NFT
// classes are out of scope in MVP. order:"asc" is pinned upstream so
// every stream emits ascending block order. metadata.blockTimestamp is
// documented only on ETH (withMetadata is sent there only); BSC rows
// therefore carry a zero timestamp and persist block_timestamp=NULL —
// never a fabricated year-0001.
package alchemy

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
const SourceCode = "alchemy"

const (
	streamFrom = "from"
	streamTo   = "to"
)

// defaultEndpoints maps each served network to its documented API host.
var defaultEndpoints = map[string]string{
	"ethereum": "https://eth-mainnet.g.alchemy.com/v2",
	"bsc":      "https://bnb-mainnet.g.alchemy.com/v2",
}

// defaultAllowedHosts is the SSRF allowlist (spec §16) — hardcoded per F6.
var defaultAllowedHosts = []string{
	"eth-mainnet.g.alchemy.com",
	"bnb-mainnet.g.alchemy.com",
}

// metadataChains lists networks where alchemy_getAssetTransfers documents
// metadata.blockTimestamp (docs: "Available only on ETH, Base, Polygon,
// Arbitrum, Optimism"). Sending it elsewhere is undocumented — BSC rows
// return no metadata field and keep a zero timestamp.
var metadataChains = map[string]bool{"ethereum": true}

// chains is the adapter's servable set before the enabled-chains gate.
var chains = map[string]bool{"ethereum": true, "bsc": true}

// Adapter implements provider.TxProvider over Alchemy.
type Adapter struct {
	baseURL      string // optional override applied to every chain (dev/test)
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

// WithDurationFunc attaches a call-site latency observer.
func WithDurationFunc(f func(time.Duration)) Option {
	return func(a *Adapter) { a.onDuration = f }
}

// New validates the base URL and builds the resilience client. baseURL=""
// selects the documented per-network endpoints; a non-empty baseURL
// overrides every chain (dev/test only — still allowlist-checked).
// Rate is a conservative ~0.6 calls/s — alchemy_getAssetTransfers is
// billed ~120 CU/call so the free-tier CUPS budget is binding by design.
func New(baseURL, apiKey string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       apiKey,
		chains:       map[string]bool{},
		allowedHosts: defaultAllowedHosts,
	}
	for _, c := range enabledChains {
		a.chains[strings.ToLower(strings.TrimSpace(c))] = true
	}
	for _, o := range opts {
		o(a)
	}
	if err := a.validateBaseURLs(); err != nil {
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
		return nil, fmt.Errorf("alchemy: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

// endpoint resolves the API base URL for a network — the override when
// set, else the documented per-chain host.
func (a *Adapter) endpoint(net network.Network) string {
	if a.baseURL != "" {
		return a.baseURL
	}
	return defaultEndpoints[strings.ToLower(net.Code)]
}

func (a *Adapter) validateBaseURLs() error {
	check := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("alchemy: invalid base url %q", raw)
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
			return fmt.Errorf("alchemy: scheme %q not allowed", u.Scheme)
		}
		for _, h := range a.allowedHosts {
			if strings.EqualFold(u.Host, h) {
				return nil
			}
		}
		return fmt.Errorf("alchemy: host %q not in allowlist", u.Host)
	}
	if a.baseURL != "" {
		return check(a.baseURL)
	}
	for code, raw := range defaultEndpoints {
		if !chains[code] {
			continue
		}
		if err := check(raw); err != nil {
			return err
		}
	}
	return nil
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network: an API key
// is required and the chain must be enabled in config.
func (a *Adapter) Enabled(net network.Network) bool {
	return a.apiKey != "" && a.chains[strings.ToLower(net.Code)]
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

// call performs one JSON-RPC request against the network's endpoint
// through the resilience client. The API key rides the URL path
// (/{apiKey}) — never a header or a log field.
func (a *Adapter) call(ctx context.Context, net network.Network, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "alchemy: marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.endpoint(net)+"/"+a.apiKey, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "alchemy: build request: %v", err)
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
		return nil, provider.Errorf(provider.ErrMalformed, "alchemy %s: %v", method, err)
	}
	if env.Error != nil {
		msg := env.Error.Message
		if strings.Contains(strings.ToLower(msg), "rate") ||
			env.Error.Code == -32005 || env.Error.Code == 429 {
			return nil, provider.Errorf(provider.ErrRateLimited, "alchemy %s: %s", method, msg)
		}
		return nil, provider.Errorf(provider.ErrDegraded, "alchemy %s: rpc error %d: %s",
			method, env.Error.Code, msg)
	}
	if len(env.Result) == 0 {
		return nil, provider.Errorf(provider.ErrMalformed, "alchemy %s: empty result", method)
	}
	return env.Result, nil
}

// HeadBlock fetches eth_blockNumber.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "alchemy: %s not enabled", net.Code)
	}
	raw, err := a.call(ctx, net, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	var hexNum string
	if err := json.Unmarshal(raw, &hexNum); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "alchemy head: %v", err)
	}
	head, err := strconv.ParseInt(strings.TrimPrefix(hexNum, "0x"), 16, 64)
	if err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "alchemy head: bad result %q", hexNum)
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
		return pageToken{}, provider.Errorf(provider.ErrInternal, "alchemy: bad page token")
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "alchemy: bad page token")
	}
	return t, nil
}

// transferParams is the alchemy_getAssetTransfers assetTransferParams
// shape (positional params element 0).
type transferParams struct {
	FromBlock        string   `json:"fromBlock"`
	ToBlock          string   `json:"toBlock"`
	FromAddress      string   `json:"fromAddress,omitempty"`
	ToAddress        string   `json:"toAddress,omitempty"`
	Category         []string `json:"category"`
	ExcludeZeroValue bool     `json:"excludeZeroValue"`
	Order            string   `json:"order"`
	MaxCount         string   `json:"maxCount"`
	PageKey          string   `json:"pageKey,omitempty"`
	WithMetadata     *bool    `json:"withMetadata,omitempty"`
}

type transfersResult struct {
	Transfers []alchemyTransfer `json:"transfers"`
	PageKey   string            `json:"pageKey"`
}

// LookupTransfers performs one stream-pair step: one page per active
// direction stream (from + to = up to two upstream calls).
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network,
	normalized string, opts provider.TransferLookupOpts) (*provider.TransferPage, error) {

	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "alchemy: %s not enabled", net.Code)
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
	var withMeta *bool
	if metadataChains[strings.ToLower(net.Code)] {
		on := true
		withMeta = &on
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
			Category:         []string{"external", "erc20"},
			ExcludeZeroValue: true,
			Order:            "asc",
			MaxCount:         "0x" + strconv.FormatInt(int64(size), 16),
			PageKey:          **dir.key,
			WithMetadata:     withMeta,
		}
		if dir.name == streamFrom {
			params.FromAddress = normalized
		} else {
			params.ToAddress = normalized
		}
		raw, err := a.call(ctx, net, "alchemy_getAssetTransfers", []any{params})
		if err != nil {
			return nil, err
		}
		page.UpstreamCalls++
		var tr transfersResult
		if err := json.Unmarshal(raw, &tr); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "alchemy transfers: %v", err)
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
