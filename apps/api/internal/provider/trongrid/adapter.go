// Package trongrid is the transaction-history adapter over the TronGrid
// API (api.trongrid.io). It is dormant by design under the §15 gate: the
// package compiles standalone and is imported by no production binary, so
// its init() allowlist registration never fires in prod, and no
// data_sources row, providerconfig descriptor, env surface, or selector
// wiring exists. Written against the provider.TxProvider contract at
// internal/provider/provider.go:282-288 and the semantics locked in
// docs/tron-indexer-semantics.md (tri-problem b1/b2/b3, INV-1/INV-2,
// base58-canonical emission, solidified-only finality).
//
// Enabled() layering: network.Network carries no activeness flag — the
// resolver gate (inactive networks never resolve) is what keeps an
// unconfigured network from reaching this adapter. Enabled() therefore
// proves only the config gate (API key + enabled chain); a disabled
// adapter returns ErrDisabled before any HTTP.
package trongrid

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

// SourceCode is the data_sources registry key — the row itself is an
// enablement task and is intentionally NOT created here.
const SourceCode = "trongrid"

const (
	streamTRX   = "trx"   // /v1/accounts/{a}/transactions — native TRX moves
	streamTRC20 = "trc20" // /v1/accounts/{a}/transactions/trc20 — TRC-20 events

	// trc20PageClamp bounds the per-step txinfo fan-out: a trc20 page of N
	// rows can need at most N block-resolution calls, so the trc20 page
	// size IS the fan-out bound (semantics §b2).
	trc20PageClamp = 50

	// upstreamLimitMax is TronGrid's hard cap on `limit` (201 → HTTP 400,
	// probed); the indexer's PageSize=1000 must be clamped, not passed
	// verbatim.
	upstreamLimitMax = 200

	// txInfoRetries bounds the in-step retry for txinfo index lag —
	// limiter-paced attempts; a persistent miss is ErrDegraded (INV-1).
	txInfoRetries = 3
)

// defaultAllowedHosts is the SSRF allowlist (spec §16).
var defaultAllowedHosts = []string{"api.trongrid.io"}

// Adapter implements provider.TxProvider over TronGrid.
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

// WithDurationFunc attaches a call-site latency observer.
func WithDurationFunc(f func(time.Duration)) Option {
	return func(a *Adapter) { a.onDuration = f }
}

// New validates the base URL and builds the resilience client (~2 req/s —
// under the keyless shared 3/s and conservative vs the keyed ~15/s Basic
// quota; the daily-call discipline rides the request-global
// UpstreamCalls budget, not the limiter). enabledChains carries
// lowercased network codes the adapter may serve.
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
		Rate:            rate.Every(500 * time.Millisecond), // ~2 req/s
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
		return nil, fmt.Errorf("trongrid: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("trongrid: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("trongrid: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("trongrid: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network: an API key
// is required (keyless quotas are dev-only per the provider decision doc)
// and the chain must be enabled in config.
func (a *Adapter) Enabled(net network.Network) bool {
	return a.apiKey != "" && a.chains[strings.ToLower(net.Code)]
}

// HeadBlock returns the SOLIDIFIED head only — walletsolidity/getnowblock.
// The unconfirmed getnowblock is never read (semantics §c).
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "trongrid: %s not enabled", net.Code)
	}
	res, err := a.post(ctx, "/walletsolidity/getnowblock", nil)
	if err != nil {
		return 0, err
	}
	var env blockEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "trongrid head: %v", err)
	}
	if env.BlockHeader == nil || env.BlockHeader.RawData.Number <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "trongrid head: missing block number")
	}
	return env.BlockHeader.RawData.Number, nil
}

// pageToken carries the resolved ms window plus per-stream fingerprint
// cursors — the window rides the token so continuation never re-bridges
// boundary blocks (semantics §b1). A nil cursor = stream exited
// (finished or kind-filtered); MinTs<0 = window unresolved.
type pageToken struct {
	MinTs int64   `json:"m"`
	MaxTs int64   `json:"x"`
	Trx   *string `json:"a,omitempty"`
	Trc20 *string `json:"b,omitempty"`
}

func encodeToken(t pageToken) string {
	if t.Trx == nil && t.Trc20 == nil {
		return ""
	}
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (pageToken, error) {
	first := ""
	if s == "" {
		return pageToken{MinTs: -1, Trx: &first, Trc20: &first}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "trongrid: bad page token")
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return pageToken{}, provider.Errorf(provider.ErrInternal, "trongrid: bad page token")
	}
	return t, nil
}

// LookupTransfers performs one stream-pair step: resolve the ms window
// (first step only), then at most one page per wanted, unfinished stream.
// INV-1: a trc20 step whose rows cannot all be block-resolved fails the
// step — no token is emitted, so the cursor is held. INV-2: both stream
// names are declared in page.Streams every step.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network,
	normalized string, opts provider.TransferLookupOpts) (*provider.TransferPage, error) {

	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "trongrid: %s not enabled", net.Code)
	}
	if opts.FromBlock > opts.ToBlock {
		return nil, provider.Errorf(provider.ErrInternal,
			"trongrid: window [%d,%d] inverted", opts.FromBlock, opts.ToBlock)
	}
	tok, err := decodeToken(opts.PageToken)
	if err != nil {
		return nil, err
	}
	calls := 0
	if tok.MinTs < 0 {
		minTs, maxTs, n, err := a.bridgeWindow(ctx, opts.FromBlock, opts.ToBlock)
		calls += n
		if err != nil {
			return nil, err
		}
		tok.MinTs, tok.MaxTs = minTs, maxTs
	}
	size := opts.PageSize
	if size <= 0 {
		size = 100
	}
	if size > upstreamLimitMax {
		size = upstreamLimitMax
	}

	wantTrx, wantTrc20 := true, true
	if len(opts.Kinds) > 0 {
		wantTrx, wantTrc20 = false, false
		for _, k := range opts.Kinds {
			switch k {
			case provider.TransferKindNative:
				wantTrx = true
			case provider.TransferKindToken:
				wantTrc20 = true
			}
		}
	}

	finished := map[string]bool{
		streamTRX:   tok.Trx == nil,
		streamTRC20: tok.Trc20 == nil,
	}
	anchors := map[string]int64{}
	var transfers []provider.Transfer

	// trx stream — native rows seed the txid→blockNumber anchor map used
	// to resolve trc20 events for free (semantics §b2).
	if tok.Trx != nil {
		if !wantTrx {
			finished[streamTRX], tok.Trx = true, nil
		} else {
			raw, next, err := a.accountPage(ctx, normalized, "transactions",
				*tok.Trx, tok.MinTs, tok.MaxTs, size)
			if err != nil {
				return nil, err
			}
			calls++
			var rows []acctTxRow
			if err := json.Unmarshal(raw, &rows); err != nil {
				return nil, provider.Errorf(provider.ErrMalformed, "trongrid trx: %v", err)
			}
			for _, r := range rows {
				anchors[strings.ToLower(r.TxID)] = r.BlockNumber
				mapped, err := mapTrxRow(r, opts.HeadBlock)
				if err != nil {
					return nil, err
				}
				transfers = append(transfers, mapped...)
			}
			if err := checkAscending(transfers); err != nil {
				return nil, err
			}
			tok.Trx, finished[streamTRX] = next, next == nil
		}
	}

	// trc20 stream — every emitted row carries a real block number via
	// anchor map then bounded txinfo fan-out; never block-less (INV-1).
	if tok.Trc20 != nil {
		if !wantTrc20 {
			finished[streamTRC20], tok.Trc20 = true, nil
		} else {
			rows, next, err := a.accountPage(ctx, normalized, "transactions/trc20",
				*tok.Trc20, tok.MinTs, tok.MaxTs, min(size, trc20PageClamp))
			if err != nil {
				return nil, err
			}
			calls++
			streamStart := len(transfers)
			n, err := a.resolveAndMapTrc20(ctx, rows, anchors, opts.HeadBlock, &transfers)
			calls += n
			if err != nil {
				return nil, err
			}
			if err := checkAscending(transfers[streamStart:]); err != nil {
				return nil, err
			}
			tok.Trc20, finished[streamTRC20] = next, next == nil
		}
	}

	if transfers == nil {
		transfers = []provider.Transfer{}
	}
	return &provider.TransferPage{
		Transfers:     transfers,
		NextPageToken: encodeToken(tok),
		Streams: []provider.StreamStatus{
			{Name: streamTRX, Finished: finished[streamTRX]},
			{Name: streamTRC20, Finished: finished[streamTRC20]},
		},
		UpstreamCalls: calls,
	}, nil
}

// bridgeWindow converts the block window to TronGrid's ms timestamp
// filters via walletsolidity/getblockbynum — one call per boundary.
// FromBlock≤0 skips its call (minTs=0; getblockbynum(0) returns genesis
// with raw_data eliding number/timestamp — probed). Missing
// block_header (block past head) is ErrDegraded, never fabricated.
func (a *Adapter) bridgeWindow(ctx context.Context, from, to int64) (int64, int64, int, error) {
	calls := 0
	minTs := int64(0)
	if from > 0 {
		ts, err := a.blockTimestamp(ctx, from)
		if err != nil {
			return 0, 0, calls, err
		}
		calls++
		minTs = ts
	}
	maxTs, err := a.blockTimestamp(ctx, to)
	if err != nil {
		return 0, 0, calls, err
	}
	calls++
	return minTs, maxTs, calls, nil
}

// blockTimestamp returns one block's ms timestamp via
// walletsolidity/getblockbynum.
func (a *Adapter) blockTimestamp(ctx context.Context, num int64) (int64, error) {
	body, _ := json.Marshal(map[string]int64{"num": num})
	res, err := a.post(ctx, "/walletsolidity/getblockbynum", body)
	if err != nil {
		return 0, err
	}
	var env blockEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "trongrid block %d: %v", num, err)
	}
	if env.BlockHeader == nil {
		return 0, provider.Errorf(provider.ErrDegraded,
			"trongrid block %d: absent (past head or skipped)", num)
	}
	return env.BlockHeader.RawData.Timestamp, nil
}

// accountPage fetches one page of an account-history endpoint.
// fingerprint "" = first request; the returned *string is the
// continuation cursor (nil when meta.links.next is absent = stream end).
func (a *Adapter) accountPage(ctx context.Context, normalized, endpoint,
	fingerprint string, minTs, maxTs int64, size int) (json.RawMessage, *string, error) {

	q := url.Values{
		"only_confirmed": {"true"},
		"order_by":       {"block_timestamp,asc"},
		"min_timestamp":  {strconv.FormatInt(minTs, 10)},
		"max_timestamp":  {strconv.FormatInt(maxTs, 10)},
		"limit":          {strconv.Itoa(size)},
	}
	if fingerprint != "" {
		q.Set("fingerprint", fingerprint)
	}
	res, err := a.get(ctx, "/v1/accounts/"+url.PathEscape(normalized)+"/"+endpoint, q)
	if err != nil {
		return nil, nil, err
	}
	var env acctEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, nil, provider.Errorf(provider.ErrMalformed, "trongrid %s: %v", endpoint, err)
	}
	if !env.Success {
		return nil, nil, provider.Errorf(provider.ErrDegraded,
			"trongrid %s: success=false %s", endpoint, env.Error)
	}
	var rows json.RawMessage = env.Data
	if len(rows) == 0 {
		rows = json.RawMessage("[]")
	}
	// meta.links.next is the continuation signal (absent = stream end);
	// meta.fingerprint is the cursor value it encodes.
	var next *string
	if env.Meta.Links.Next != "" {
		if env.Meta.Fingerprint == "" {
			return nil, nil, provider.Errorf(provider.ErrMalformed,
				"trongrid %s: links.next without fingerprint", endpoint)
		}
		fp := env.Meta.Fingerprint
		next = &fp
	}
	return rows, next, nil
}

// resolveAndMapTrc20 resolves every kept (type=Transfer) row's block
// number — anchor map first, then one walletsolidity/
// gettransactioninfobyid call per unresolved unique txid, retried
// txInfoRetries times for index lag. A persistent miss is ErrDegraded;
// the step fails before any page is emitted (INV-1). Returns the number
// of upstream fan-out calls performed.
func (a *Adapter) resolveAndMapTrc20(ctx context.Context, raw json.RawMessage,
	anchors map[string]int64, head int64, out *[]provider.Transfer) (int, error) {

	var rows []trc20Row
	if err := json.Unmarshal(raw, &rows); err != nil {
		return 0, provider.Errorf(provider.ErrMalformed, "trongrid trc20: %v", err)
	}
	calls := 0
	resolved := map[string]*txInfoResult{}
	for _, r := range rows {
		if r.Type != "Transfer" {
			continue // Approval et al. are scope-excluded (semantics §e)
		}
		txid := strings.ToLower(r.TxID)
		block := int64(0)
		isErr := false
		if bn, ok := anchors[txid]; ok {
			block = bn // event in an indexed tx is success evidence
		} else {
			info, seen := resolved[txid]
			if !seen {
				for i := 0; i < txInfoRetries; i++ {
					inf, err := a.txInfo(ctx, txid)
					calls++
					if err != nil {
						return calls, err
					}
					if inf != nil {
						info = inf
						break
					}
				}
				resolved[txid] = info
			}
			if info == nil {
				return calls, provider.Errorf(provider.ErrDegraded,
					"trongrid trc20: tx %s unresolved after %d txinfo calls (index lag)",
					txid, txInfoRetries)
			}
			block = info.BlockNumber
			isErr = info.IsError
		}
		tr, err := mapTrc20Row(r, block, isErr, head)
		if err != nil {
			return calls, err
		}
		*out = append(*out, tr)
	}
	return calls, nil
}

// txInfoResult carries the two pieces of evidence txinfo provides.
type txInfoResult struct {
	BlockNumber int64
	IsError     bool
}

// txInfo returns nil,nil on a miss ({} body — index lag), never a
// zeroed result.
func (a *Adapter) txInfo(ctx context.Context, txid string) (*txInfoResult, error) {
	body, _ := json.Marshal(map[string]string{"value": txid})
	res, err := a.post(ctx, "/walletsolidity/gettransactioninfobyid", body)
	if err != nil {
		return nil, err
	}
	var env txInfoEnvelope
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "trongrid txinfo %s: %v", txid, err)
	}
	if env.Error != "" {
		return nil, provider.Errorf(provider.ErrDegraded, "trongrid txinfo %s: %s", txid, env.Error)
	}
	if env.ID == "" || env.BlockNumber <= 0 {
		return nil, nil // miss — index lag, retried by caller
	}
	return &txInfoResult{
		BlockNumber: env.BlockNumber,
		IsError:     env.Receipt != nil && env.Receipt.Result != "SUCCESS",
	}, nil
}

// checkAscending enforces the contract's within-stream ascending-block
// invariant — block_timestamp ordering only implies it under upstream
// timestamp monotonicity, so a violation is an upstream contract break
// (ErrDegraded), never silently emitted.
func checkAscending(ts []provider.Transfer) error {
	for i := 1; i < len(ts); i++ {
		if ts[i].BlockNumber < ts[i-1].BlockNumber {
			return provider.Errorf(provider.ErrDegraded,
				"trongrid: stream order violated (%d after %d)",
				ts[i].BlockNumber, ts[i-1].BlockNumber)
		}
	}
	return nil
}

type blockEnvelope struct {
	BlockHeader *struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

type acctEnvelope struct {
	Success bool            `json:"success"`
	Error   string          `json:"Error"`
	Data    json.RawMessage `json:"data"`
	Meta    struct {
		Fingerprint string `json:"fingerprint"`
		Links       struct {
			Next string `json:"next"`
		} `json:"links"`
	} `json:"meta"`
}

type txInfoEnvelope struct {
	ID          string `json:"id"`
	BlockNumber int64  `json:"blockNumber"`
	Error       string `json:"Error"`
	Receipt     *struct {
		Result string `json:"result"`
	} `json:"receipt"`
}

func (a *Adapter) get(ctx context.Context, path string, q url.Values) (*resilience.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.baseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "trongrid: build request: %v", err)
	}
	return a.do(ctx, req)
}

func (a *Adapter) post(ctx context.Context, path string, body []byte) (*resilience.Result, error) {
	if body == nil {
		body = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "trongrid: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(ctx, req)
}

func (a *Adapter) do(ctx context.Context, req *http.Request) (*resilience.Result, error) {
	if a.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", a.apiKey)
	}
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	return res, err
}

// init publishes the default SSRF allowlist — dormant package, so this
// never runs in production binaries (nothing imports it).
func init() { provider.RegisterAllowedHosts(SourceCode, defaultAllowedHosts) }

var _ provider.TxProvider = (*Adapter)(nil)
