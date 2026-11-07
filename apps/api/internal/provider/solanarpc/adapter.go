// Package solanarpc is the transaction-history adapter over the keyless
// public Solana JSON-RPC (api.mainnet-beta.solana.com). It is dormant by
// design under the §15 gate: the package compiles standalone and is
// imported by no production binary, so its init() allowlist registration
// never fires in prod, and no data_sources row, providerconfig
// descriptor, env surface, or selector wiring exists. Written against
// the provider.TxProvider contract at internal/provider/provider.go:282-288
// and the semantics locked in docs/solana-indexer-semantics.md
// (contiguous-ascending-prefix INV-S1, finalized-only reads, ledger-floor
// truncation, scoped SPL discovery).
//
// The signature feed paginates DESC by (slot,transactionIndex) while the
// contract wants ascending emission, so each LookupTransfers step runs a
// two-phase protocol: collect desc pages until the frontier passes below
// the observable window bottom (zero emission meanwhile — INV-S1), then
// emit ascending slices of the collected signature set via batched
// getTransaction. All state rides the page token (in-process only).
package solanarpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key — the row itself is an
// enablement task and is intentionally NOT created here.
const SourceCode = "solana-rpc"

// Stream names. Both ride the SAME signature feed — the shared desc
// collection serves both; they differ only in extraction (semantics §e).
const (
	streamSOL = "sol" // native lamports — system transfer/transferWithSeed
	streamSPL = "spl" // SPL token — parsed transfer/transferChecked (+Token-2022)
)

// Pinned bounds (semantics §b) — named constants so tests can shrink
// them and the calls-per-ensure model stays auditable.
const (
	sigPageLimit       = 1000  // getSignaturesForAddress upstream max
	maxSigPagesPerStep = 10    // desc collection pages per step
	emitCap            = 100   // getTransaction logical calls per step
	batchSize          = 50    // logical calls per JSON-RPC batch HTTP
	maxCollectSigs     = 20000 // sigs held in the in-process token
	maxSeedWalk        = 5     // skipped-slot walk for the `before` seed
)

// defaultAllowedHosts is the SSRF allowlist (spec §16).
var defaultAllowedHosts = []string{"api.mainnet-beta.solana.com"}

// chains is the adapter's served set: solana only.
var chains = map[string]bool{"solana": true}

// Adapter implements provider.TxProvider over the public Solana JSON-RPC.
type Adapter struct {
	baseURL      string
	enabled      map[string]bool
	allowedHosts []string
	insecureHTTP bool
	hooks        resilience.Hooks
	onDuration   func(time.Duration)
	resOpts      []resilience.Option
	cli          *resilience.Client

	sigPagesPerStep int
	sigPageSize     int
	emitCap         int
	collectSigCap   int
	seedWalk        int
	batch           int
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

// WithBounds shrinks the step bounds (tests force multi-step collection
// and cap-hit paths). Zero/negative values keep the defaults.
func WithBounds(sigPages, emit, collect, seedWalk, batch int) Option {
	return func(a *Adapter) {
		if sigPages > 0 {
			a.sigPagesPerStep = sigPages
		}
		if emit > 0 {
			a.emitCap = emit
		}
		if collect > 0 {
			a.collectSigCap = collect
		}
		if seedWalk > 0 {
			a.seedWalk = seedWalk
		}
		if batch > 0 {
			a.batch = batch
		}
	}
}

// WithSigPageSize shrinks the per-request signature limit (tests force
// multi-page collection with small fixtures).
func WithSigPageSize(n int) Option {
	return func(a *Adapter) {
		if n > 0 {
			a.sigPageSize = n
		}
	}
}

// New validates the base URL and builds the resilience client (~2 req/s —
// under the documented 100 req/10s/IP public quota with headroom for the
// per-method 40/10s ceiling; see solana-provider-decision.md). Keyless —
// gating is the enabled-chains list, never key presence.
func New(baseURL string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:         strings.TrimRight(baseURL, "/"),
		enabled:         map[string]bool{},
		allowedHosts:    defaultAllowedHosts,
		sigPagesPerStep: maxSigPagesPerStep,
		sigPageSize:     sigPageLimit,
		emitCap:         emitCap,
		collectSigCap:   maxCollectSigs,
		seedWalk:        maxSeedWalk,
		batch:           batchSize,
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
		Rate:            rate.Every(500 * time.Millisecond), // ~2 req/s keyless politeness
		Burst:           1,
		MaxLimiterWait:  4 * time.Second,
		Concurrency:     2,
		MaxRetries:      2,
		Backoff:         300 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    16 << 20, // jsonParsed txs are large; batches ×50
		AllowedHosts:    a.allowedHosts,
		Hooks:           a.hooks,
	}, a.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("solanarpc: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("solanarpc: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("solanarpc: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("solanarpc: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network — keyless, so
// only the enabled-chains list gates; a disabled adapter emits no traffic.
func (a *Adapter) Enabled(net network.Network) bool {
	return chains[strings.ToLower(net.Code)] && a.enabled[strings.ToLower(net.Code)]
}

// HeadBlock returns the FINALIZED slot only (semantics §c). confirmed/
// processed are never read — confirmed can fork and processed is
// rejected outright on history endpoints (probe §8).
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "solanarpc: %s not enabled", net.Code)
	}
	raw, err := a.rpc(ctx, "getSlot", []any{
		map[string]any{"commitment": "finalized"},
	})
	if err != nil {
		return 0, err
	}
	var head int64
	if err := json.Unmarshal(raw, &head); err != nil || head <= 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "solanarpc head: %s", raw)
	}
	return head, nil
}

// sigRef is one collected feed signature riding the page token.
type sigRef struct {
	Sig  string `json:"s"`
	Slot int64  `json:"l"`
	Idx  int64  `json:"i"` // transactionIndex; -1 absent
}

// pageToken carries the whole collection/emission state — in-process
// only (it never crosses the wire; semantics §b).
type pageToken struct {
	V        int      `json:"v"`
	F        int64    `json:"f"`
	T        int64    `json:"t"`
	Floor    int64    `json:"fl"`
	FloorSet bool     `json:"fs"`
	Seed     string   `json:"se,omitempty"`
	SeedDone bool     `json:"sd"`
	Frontier string   `json:"fr,omitempty"`
	Bottomed bool     `json:"bo,omitempty"`
	Exhaust  bool     `json:"ex,omitempty"` // feed bottomed out (empty page)
	Sigs     []sigRef `json:"g,omitempty"`
	Emit     int      `json:"e"`
}

func encodeToken(t *pageToken) string {
	if t == nil {
		return ""
	}
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (*pageToken, error) {
	if s == "" {
		return &pageToken{V: 1}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "solanarpc: bad page token")
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil || t.V != 1 {
		return nil, provider.Errorf(provider.ErrInternal, "solanarpc: bad page token")
	}
	return &t, nil
}

// LookupTransfers performs one step of the two-phase protocol
// (semantics §b): first-step probes (ledger floor, `before` seed), then
// bounded desc signature collection, then bounded ascending emission.
// INV-S1: nothing is emitted until collection has passed below the
// observable window bottom — a mid-collection step emits zero rows.
// INV-S2: both stream names are declared every step.
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network,
	normalized string, opts provider.TransferLookupOpts) (*provider.TransferPage, error) {

	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "solanarpc: %s not enabled", net.Code)
	}
	if opts.FromBlock > opts.ToBlock {
		return nil, provider.Errorf(provider.ErrInternal,
			"solanarpc: window [%d,%d] inverted", opts.FromBlock, opts.ToBlock)
	}
	tok, err := decodeToken(opts.PageToken)
	if err != nil {
		return nil, err
	}
	if !tok.FloorSet { // fresh token — pin the window
		tok.F, tok.T = opts.FromBlock, opts.ToBlock
	}
	if tok.F != opts.FromBlock || tok.T != opts.ToBlock {
		return nil, provider.Errorf(provider.ErrInternal,
			"solanarpc: token window [%d,%d] mismatches request [%d,%d]",
			tok.F, tok.T, opts.FromBlock, opts.ToBlock)
	}
	calls := 0

	// First step: ledger floor — the observable bottom is max(F, floor)
	// (semantics §f). floor > F ⇒ the window prefix is unobservable:
	// emit everything honestly collected but NEVER finish the stream.
	if !tok.FloorSet {
		floor, err := a.ledgerFloor(ctx)
		if err != nil {
			return nil, err
		}
		calls++
		tok.Floor, tok.FloorSet = floor, true
	}
	lowerBound := tok.F
	if tok.Floor > lowerBound {
		lowerBound = tok.Floor
	}

	// `before` seed: when fetchTo < observed head, anchor collection at
	// the first signature of the first produced slot > T (before is
	// exclusive ⇒ covers all of slot T; probe §4). Seed-walk failure
	// degrades to a head-scan — still bounded by sig-pages.
	if !tok.SeedDone && opts.HeadBlock > 0 && opts.ToBlock < opts.HeadBlock {
		seed, n, err := a.seedBelow(ctx, opts.ToBlock)
		calls += n
		if err != nil {
			return nil, err
		}
		tok.Seed, tok.SeedDone = seed, true
	}

	wantSol, wantSPL := true, true
	if len(opts.Kinds) > 0 {
		wantSol, wantSPL = false, false
		for _, k := range opts.Kinds {
			switch k {
			case provider.TransferKindNative:
				wantSol = true
			case provider.TransferKindToken:
				wantSPL = true
			}
		}
	}

	// Collection phase — bounded desc pages until the frontier passes
	// below the observable bottom or the feed exhausts.
	if !tok.Bottomed {
		have := map[string]bool{}
		for _, s := range tok.Sigs {
			have[s.Sig] = true
		}
		for i := 0; i < a.sigPagesPerStep; i++ {
			rows, err := a.sigPage(ctx, normalized, tok.frontierOrSeed())
			if err != nil {
				return nil, err
			}
			calls++
			for _, r := range rows {
				tok.Frontier = r.Signature
				if r.Slot > tok.T {
					continue // above the window — position marker only
				}
				if r.Slot < lowerBound {
					tok.Bottomed = true
					break
				}
				if len(r.Err) > 0 && string(r.Err) != "null" {
					continue // failed tx — collected sig would emit nothing (§e)
				}
				if !have[r.Signature] {
					have[r.Signature] = true
					idx := int64(-1)
					if r.TransactionIndex != nil {
						idx = *r.TransactionIndex
					}
					tok.Sigs = append(tok.Sigs, sigRef{Sig: r.Signature, Slot: r.Slot, Idx: idx})
				}
			}
			// A short or empty page means the feed has nothing more
			// below — collection is complete by exhaustion (§f).
			if len(rows) < a.sigPageSize {
				tok.Bottomed, tok.Exhaust = true, true
			}
			if tok.Bottomed {
				break
			}
		}
		if tok.Bottomed {
			sort.Slice(tok.Sigs, func(i, j int) bool {
				if tok.Sigs[i].Slot != tok.Sigs[j].Slot {
					return tok.Sigs[i].Slot < tok.Sigs[j].Slot
				}
				if tok.Sigs[i].Idx != tok.Sigs[j].Idx {
					return tok.Sigs[i].Idx < tok.Sigs[j].Idx
				}
				return tok.Sigs[i].Sig < tok.Sigs[j].Sig
			})
		}
		// Window too large to hold honestly → zero emission, unfinished
		// streams, ended token ⇒ partial + history_truncated (semantics §b).
		if len(tok.Sigs) > a.collectSigCap {
			return &provider.TransferPage{
				Transfers: []provider.Transfer{},
				Streams: []provider.StreamStatus{
					{Name: streamSOL, Finished: false},
					{Name: streamSPL, Finished: false},
				},
				UpstreamCalls: calls,
			}, nil
		}
	}

	// Emission phase — ascending slices of the sorted collected set;
	// emitPos only advances ⇒ cross-step global ascending order.
	var transfers []provider.Transfer
	if tok.Bottomed && tok.Emit < len(tok.Sigs) {
		end := tok.Emit + a.emitCap
		if end > len(tok.Sigs) {
			end = len(tok.Sigs)
		}
		batch := tok.Sigs[tok.Emit:end]
		n, err := a.emitSlice(ctx, batch, wantSol, wantSPL, opts.HeadBlock, &transfers)
		calls += n
		if err != nil {
			return nil, err // INV-S3: unresolvable ⇒ no token advance
		}
		tok.Emit = end
	}
	if transfers == nil {
		transfers = []provider.Transfer{}
	}

	emitDone := tok.Bottomed && tok.Emit >= len(tok.Sigs)
	truncated := tok.Floor > tok.F // unobservable [F, floor) prefix (§f)
	// A finished stream means the window's coverage is complete. floor>F
	// can never finish (indistinguishable new-vs-purged prefix — §f);
	// kind-filtered streams exit trivially finished.
	finished := func(want bool) bool {
		if !want {
			return true
		}
		return emitDone && !truncated
	}
	next := ""
	if !emitDone {
		next = encodeToken(tok)
	}
	return &provider.TransferPage{
		Transfers:     transfers,
		NextPageToken: next,
		Streams: []provider.StreamStatus{
			{Name: streamSOL, Finished: finished(wantSol)},
			{Name: streamSPL, Finished: finished(wantSPL)},
		},
		UpstreamCalls: calls,
	}, nil
}

// frontierOrSeed is the `before` cursor for the next collection page:
// the lowest collected sig, else the seed, else absent (head-scan).
func (t *pageToken) frontierOrSeed() string {
	if t.Frontier != "" {
		return t.Frontier
	}
	return t.Seed
}

// ledgerFloor = getFirstAvailableBlock — the advertised retained-ledger
// floor (0 = archival on mainnet-beta; semantics §f).
func (a *Adapter) ledgerFloor(ctx context.Context) (int64, error) {
	raw, err := a.rpc(ctx, "getFirstAvailableBlock", []any{})
	if err != nil {
		return 0, err
	}
	var floor int64
	if err := json.Unmarshal(raw, &floor); err != nil || floor < 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "solanarpc floor: %s", raw)
	}
	return floor, nil
}

// seedBelow walks produced slots T+1, T+2, … ≤ seedWalk times for the
// first block carrying signatures; returns its FIRST signature (the
// exclusive `before` seed that covers all of slot T). "" means no seed —
// the caller falls back to a head-scan. getBlock errors and skipped
// slots (null result) both continue the bounded walk.
func (a *Adapter) seedBelow(ctx context.Context, toBlock int64) (string, int, error) {
	calls := 0
	for s := toBlock + 1; s <= toBlock+int64(a.seedWalk); s++ {
		raw, err := a.rpc(ctx, "getBlock", []any{
			s,
			map[string]any{
				"commitment":                     "finalized",
				"encoding":                       "jsonParsed",
				"transactionDetails":             "signatures",
				"rewards":                        false,
				"maxSupportedTransactionVersion": 0,
			},
		})
		calls++
		if err != nil {
			continue // bounded walk — a missed seed degrades to head-scan
		}
		var blk *struct {
			Signatures []string `json:"signatures"`
		}
		if err := json.Unmarshal(raw, &blk); err != nil {
			continue
		}
		if blk == nil || len(blk.Signatures) == 0 {
			continue
		}
		return blk.Signatures[0], calls, nil
	}
	return "", calls, nil
}

// sigRow is one getSignaturesForAddress result element.
type sigRow struct {
	Signature        string          `json:"signature"`
	Slot             int64           `json:"slot"`
	Err              json.RawMessage `json:"err"`
	TransactionIndex *int64          `json:"transactionIndex"`
}

// sigPage fetches one desc signature page. `before` is exclusive and is
// omitted when empty (head-scan start). A null result is malformed —
// the feed contract is an array.
func (a *Adapter) sigPage(ctx context.Context, address, before string) ([]sigRow, error) {
	cfg := map[string]any{
		"commitment": "finalized",
		"limit":      a.sigPageSize,
	}
	if before != "" {
		cfg["before"] = before
	}
	raw, err := a.rpc(ctx, "getSignaturesForAddress", []any{address, cfg})
	if err != nil {
		return nil, err
	}
	var rows []sigRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "solanarpc sigs: %v", err)
	}
	if rows == nil {
		return nil, provider.Errorf(provider.ErrMalformed, "solanarpc sigs: null result")
	}
	for i, r := range rows {
		if r.Signature == "" || r.Slot <= 0 {
			return nil, provider.Errorf(provider.ErrMalformed,
				"solanarpc sigs[%d]: sig=%q slot=%d", i, r.Signature, r.Slot)
		}
	}
	return rows, nil
}

// emitSlice fans out getTransaction (batches ≤ batchSize) over one
// ascending sig slice and maps each tx into transfers. A persistent
// null result is an index inconsistency → ErrDegraded (never a skip —
// the sig was on the finalized feed). Returns the batch HTTP count.
func (a *Adapter) emitSlice(ctx context.Context, sigs []sigRef,
	wantSol, wantSPL bool, head int64, out *[]provider.Transfer) (int, error) {

	calls := 0
	for i := 0; i < len(sigs); i += a.batch {
		j := i + a.batch
		if j > len(sigs) {
			j = len(sigs)
		}
		txs, err := a.txBatch(ctx, sigs[i:j])
		calls++
		if err != nil {
			return calls, err
		}
		for _, s := range sigs[i:j] {
			tx := txs[s.Sig]
			if tx == nil {
				return calls, provider.Errorf(provider.ErrDegraded,
					"solanarpc tx %s: null result for feed sig", s.Sig)
			}
			rows, err := mapTx(tx, s, wantSol, wantSPL, head)
			if err != nil {
				return calls, err
			}
			*out = append(*out, rows...)
		}
	}
	return calls, nil
}

// ---- JSON-RPC plumbing ----

type rpcErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcErrorObj    `json:"error"`
}

// rpc performs one JSON-RPC call (one HTTP request) and returns the raw
// result — callers decode so a literal `null` result carries its own
// meaning (skipped block vs malformed). JSON-RPC errors map typed:
// -32602/-32601 are adapter-bug class (ErrInternal); anything else is
// upstream-degraded evidence.
func (a *Adapter) rpc(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	res, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var resp rpcResponse
	if err := json.Unmarshal(res.Body, &resp); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "solanarpc %s: %v", method, err)
	}
	if resp.Error != nil {
		return nil, rpcErr(method, resp.Error)
	}
	if len(resp.Result) == 0 {
		return nil, provider.Errorf(provider.ErrMalformed, "solanarpc %s: missing result", method)
	}
	return resp.Result, nil
}

// txBatch fetches ≤batchSize transactions in ONE HTTP request — a
// JSON-RPC batch. Responses key by id ⇒ order-independent. Any
// element-level error fails the batch typed (INV-S3).
func (a *Adapter) txBatch(ctx context.Context, sigs []sigRef) (map[string]*getTxResult, error) {
	calls := make([]map[string]any, len(sigs))
	for i, s := range sigs {
		calls[i] = map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "method": "getTransaction",
			"params": []any{
				s.Sig,
				map[string]any{
					"commitment":                     "finalized",
					"encoding":                       "jsonParsed",
					"maxSupportedTransactionVersion": 0,
				},
			},
		}
	}
	body, _ := json.Marshal(calls)
	res, err := a.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var resps []rpcResponse
	if err := json.Unmarshal(res.Body, &resps); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "solanarpc batch: %v", err)
	}
	byID := map[int64]*rpcResponse{}
	for i := range resps {
		var id int64
		if err := json.Unmarshal(resps[i].ID, &id); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "solanarpc batch: bad id")
		}
		byID[id] = &resps[i]
	}
	out := map[string]*getTxResult{}
	for i, s := range sigs {
		r, ok := byID[int64(i+1)]
		if !ok {
			return nil, provider.Errorf(provider.ErrMalformed,
				"solanarpc batch: missing result for %s", s.Sig)
		}
		if r.Error != nil {
			return nil, rpcErr("getTransaction", r.Error)
		}
		if len(r.Result) == 0 || string(r.Result) == "null" {
			out[s.Sig] = nil // caller: null = index inconsistency
			continue
		}
		var tx getTxResult
		if err := json.Unmarshal(r.Result, &tx); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: %v", s.Sig, err)
		}
		out[s.Sig] = &tx
	}
	return out, nil
}

// rpcErr classifies a JSON-RPC error object. Param/method faults are our
// bug class (ErrInternal); data faults are upstream-degraded evidence.
func rpcErr(method string, e *rpcErrorObj) error {
	kind := provider.ErrDegraded
	if e.Code == -32601 || e.Code == -32602 {
		kind = provider.ErrInternal
	}
	return provider.Errorf(kind, "solanarpc %s: rpc %d %s", method, e.Code, e.Message)
}

func (a *Adapter) post(ctx context.Context, body []byte) (*resilience.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "solanarpc: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
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
