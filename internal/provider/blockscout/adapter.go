// Package blockscout is the transaction-history adapter over the public
// Blockscout v2 REST API (eth.blockscout.com) — keyless, ethereum only.
// Blockscout lists are NEWEST-FIRST with no block-range params, so the
// adapter runs a bounded descending scan per stream: each LookupTransfers
// step drains at most maxScanPages upstream pages into a process-local
// buffer and emits rows ONLY once the stream's [fetchFrom, fetchTo]
// window is contiguously covered — the only other honest outcome is
// zero rows with an unfinished stream (plan phase-02 critical coverage
// rule). Scan state is re-derivable from (net, wallet, fetchFrom); the
// page token carries the cursors so restarts after buffer eviction are
// honest re-spends, never mid-air resumes.
package blockscout

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// SourceCode is the data_sources registry key.
const SourceCode = "blockscout"

// Stream names — one per independently-paginated upstream list. The
// upstream lists are unified (both directions interleaved); partitioning
// by direction would fake coverage, so streams are kind-partitioned.
const (
	streamNative = "native"
	streamToken  = "token"
)

// defaultAllowedHosts is the SSRF allowlist (spec §16).
var defaultAllowedHosts = []string{"eth.blockscout.com"}

// chains is the adapter's served set: ethereum only in MVP.
var chains = map[string]bool{"ethereum": true}

// Adapter implements provider.TxProvider over Blockscout v2.
type Adapter struct {
	baseURL      string
	enabled      map[string]bool
	allowedHosts []string
	insecureHTTP bool
	hooks        resilience.Hooks
	onDuration   func(time.Duration)
	resOpts      []resilience.Option
	cli          *resilience.Client

	maxScanPages  int           // upstream pages drained per step
	maxBufferRows int           // per-scan buffered row cap
	maxScans      int           // global LRU bound across wallets
	scanTTL       time.Duration // idle eviction

	mu    sync.Mutex
	scans map[string]*scanState
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

// WithScanLimits tunes the drain/buffer bounds (tests shrink them).
func WithScanLimits(maxPages, maxRows, maxScans int, ttl time.Duration) Option {
	return func(a *Adapter) {
		if maxPages > 0 {
			a.maxScanPages = maxPages
		}
		if maxRows > 0 {
			a.maxBufferRows = maxRows
		}
		if maxScans > 0 {
			a.maxScans = maxScans
		}
		if ttl > 0 {
			a.scanTTL = ttl
		}
	}
}

// New validates the base URL and builds the resilience client. Keyless —
// gating is the enabled-chains list, never key presence.
func New(baseURL string, enabledChains []string, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		baseURL:       strings.TrimRight(baseURL, "/"),
		enabled:       map[string]bool{},
		allowedHosts:  defaultAllowedHosts,
		maxScanPages:  10,
		maxBufferRows: 20000,
		maxScans:      256,
		scanTTL:       30 * time.Minute,
		scans:         map[string]*scanState{},
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
		Rate:            rate.Every(500 * time.Millisecond), // ~2 calls/s, keyless politeness
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
		return nil, fmt.Errorf("blockscout: resilience client: %w", err)
	}
	a.cli = cli
	return a, nil
}

func (a *Adapter) validateBaseURL() error {
	u, err := url.Parse(a.baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("blockscout: invalid base url %q", a.baseURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && a.insecureHTTP) {
		return fmt.Errorf("blockscout: scheme %q not allowed", u.Scheme)
	}
	for _, h := range a.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("blockscout: host %q not in allowlist", u.Host)
}

// Name is the provider registry key.
func (a *Adapter) Name() string { return SourceCode }

// Enabled reports whether the adapter may serve the network — keyless, so
// only the enabled-chains list gates; a disabled adapter emits no traffic.
func (a *Adapter) Enabled(net network.Network) bool {
	return chains[strings.ToLower(net.Code)] && a.enabled[strings.ToLower(net.Code)]
}

// get performs one GET through the resilience client and decodes the
// envelope. UpstreamCalls accounting lives in the caller.
func (a *Adapter) get(ctx context.Context, path string, q url.Values, out any) error {
	u := a.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return provider.Errorf(provider.ErrInternal, "blockscout: build request: %v", err)
	}
	started := time.Now()
	res, err := a.cli.Do(ctx, req)
	if a.onDuration != nil {
		a.onDuration(time.Since(started))
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		return provider.Errorf(provider.ErrMalformed, "blockscout %s: %v", path, err)
	}
	return nil
}

// HeadBlock returns the newest canonical block height.
func (a *Adapter) HeadBlock(ctx context.Context, net network.Network) (int64, error) {
	if !a.Enabled(net) {
		return 0, provider.Errorf(provider.ErrDisabled, "blockscout: disabled for %s", net.Code)
	}
	var out struct {
		Items []struct {
			Height int64 `json:"height"`
		} `json:"items"`
	}
	if err := a.get(ctx, "/api/v2/blocks", url.Values{"type": {"block"}}, &out); err != nil {
		return 0, err
	}
	if len(out.Items) == 0 {
		return 0, provider.Errorf(provider.ErrMalformed, "blockscout: empty blocks page")
	}
	return out.Items[0].Height, nil
}

// cursor is an upstream next_page_params object carried verbatim.
type cursor map[string]any

func (c cursor) query() url.Values {
	q := url.Values{}
	for k, v := range c {
		switch t := v.(type) {
		case string:
			q.Set(k, t)
		case float64:
			q.Set(k, strconv.FormatInt(int64(t), 10))
		default:
			q.Set(k, fmt.Sprint(v))
		}
	}
	return q
}

// streamScan is one stream's bounded descending-scan state.
type streamScan struct {
	next      cursor // descending resume cursor (nil = not started)
	headNext  cursor // head catch-up cursor (nil = done/not started)
	catchUp   bool   // head segment (head..fetchTo] drain in flight
	exhausted bool   // upstream has no earlier page
	floor     int64  // last scanned item's block; covered ⇔ floor < fetchFrom
	floorSet  bool
	head      int64 // highest fetchTo joined so far
	rows      []provider.Transfer
	seen      map[string]bool
	emitted   bool
}

// covered reports whether [fetchFrom..head] is contiguously scanned: the
// boundary block itself may hold unscanned items on the next page, so the
// criterion is strictly floor < fetchFrom — or upstream exhaustion.
func (s *streamScan) covered(fetchFrom int64) bool {
	return s.exhausted || (s.floorSet && s.floor < fetchFrom)
}

// scanState is the process-local buffer for one (net,wallet,fetchFrom)
// scan; rows live here, cursors round-trip through NextPageToken. mu
// serializes drains of the same scan (distinct wallets stay parallel).
type scanState struct {
	mu      sync.Mutex
	touched time.Time
	streams map[string]*streamScan
}

// tokenState is the serialized continuation carried by NextPageToken.
type tokenState struct {
	V int                          `json:"v"`
	S map[string]*tokenStreamState `json:"s"`
}

type tokenStreamState struct {
	Next      cursor `json:"n,omitempty"`
	HeadNext  cursor `json:"hn,omitempty"`
	CatchUp   bool   `json:"cu,omitempty"`
	Exhausted bool   `json:"e,omitempty"`
	Floor     int64  `json:"f,omitempty"`
	FloorSet  bool   `json:"fs,omitempty"`
	Head      int64  `json:"h"`
}

func encodeToken(st *tokenState) string {
	b, _ := json.Marshal(st)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (*tokenState, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var st tokenState
	if err := json.Unmarshal(b, &st); err != nil || st.V != 1 || st.S == nil {
		return nil, fmt.Errorf("bad token")
	}
	return &st, nil
}

func scanKey(net network.Network, wallet string, fetchFrom int64) string {
	return net.Code + "|" + wallet + "|" + strconv.FormatInt(fetchFrom, 10)
}

// evict applies idle-TTL and global-LRU bounds; caller holds a.mu.
func (a *Adapter) evict(now time.Time) {
	for k, s := range a.scans {
		if now.Sub(s.touched) > a.scanTTL {
			delete(a.scans, k)
		}
	}
	for len(a.scans) > a.maxScans {
		var oldest string
		var ot time.Time
		for k, s := range a.scans {
			if oldest == "" || s.touched.Before(ot) {
				oldest, ot = k, s.touched
			}
		}
		delete(a.scans, oldest)
	}
}

// fetchPage pulls one list page for a stream; returns items (newest
// first), the continuation cursor (nil = list exhausted).
func (a *Adapter) fetchPage(ctx context.Context, net network.Network, wallet, stream string,
	c cursor) ([]listItem, cursor, error) {
	var path string
	q := url.Values{}
	switch stream {
	case streamNative:
		path = "/api/v2/addresses/" + url.PathEscape(wallet) + "/transactions"
	case streamToken:
		path = "/api/v2/addresses/" + url.PathEscape(wallet) + "/token-transfers"
		q.Set("type", "ERC-20")
	}
	for k, vs := range c.query() {
		q[k] = vs
	}
	var out listResponse
	if err := a.get(ctx, path, q, &out); err != nil {
		return nil, nil, err
	}
	return out.Items, cursor(out.NextPageParams), nil
}

// LookupTransfers advances each stream's descending scan by at most
// maxScanPages upstream pages, emitting a stream's buffered rows only
// once its window is covered. Zero-row unfinished pages are the honest
// partial outcome; they never claim coverage (phase-02 critical rule).
func (a *Adapter) LookupTransfers(ctx context.Context, net network.Network, wallet string,
	opts provider.TransferLookupOpts) (*provider.TransferPage, error) {
	if !a.Enabled(net) {
		return nil, provider.Errorf(provider.ErrDisabled, "blockscout: disabled for %s", net.Code)
	}
	if opts.PageToken != "" {
		if _, err := decodeToken(opts.PageToken); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed, "blockscout: bad page token")
		}
	}

	a.mu.Lock()
	a.evict(time.Now())
	key := scanKey(net, wallet, opts.FromBlock)
	st, ok := a.scans[key]
	if !ok {
		// Fresh scan. A new refresh arrives with PageToken="" yet MUST
		// continue an existing buffer — deterministic keying is what makes
		// cross-refresh progress possible. Only a missing buffer (evicted,
		// TTL'd, first ever) restarts — honest re-spend from the top.
		st = &scanState{touched: time.Now(), streams: map[string]*streamScan{
			streamNative: {seen: map[string]bool{}},
			streamToken:  {seen: map[string]bool{}},
		}}
		a.scans[key] = st
		for _, s := range st.streams {
			s.head = opts.ToBlock
		}
	}
	st.touched = time.Now()
	a.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	if opts.PageToken == "" {
		// Refresh boundary: rows emit again — the service re-upserts each
		// refresh (idempotent by content identity); holding them back would
		// starve a re-requested window.
		for _, s := range st.streams {
			s.emitted = false
		}
	}
	drop := func(err error) (*provider.TransferPage, error) {
		a.mu.Lock()
		delete(a.scans, key)
		a.mu.Unlock()
		return nil, err
	}

	calls := 0
	budget := a.maxScanPages
	names := []string{streamNative, streamToken}

	// Head-segment join: a higher fetchTo first drains (head..fetchTo].
	for _, n := range names {
		s := st.streams[n]
		if opts.ToBlock > s.head {
			s.catchUp = true
		}
	}

	for budget > 0 {
		progressed := false
		for _, n := range names {
			if budget <= 0 {
				break
			}
			s := st.streams[n]
			switch {
			case s.catchUp:
				items, next, err := a.fetchPage(ctx, net, wallet, n, s.headNext)
				if err != nil {
					return nil, err
				}
				calls++
				budget--
				progressed = true
				done := false
				for _, it := range items {
					if it.block() <= s.head {
						done = true
						break
					}
					if it.block() <= opts.ToBlock {
						if err := a.bufferRow(s, n, it); err != nil {
							return drop(err)
						}
					}
				}
				if done || next == nil {
					s.catchUp = false
					s.headNext = nil
					s.head = opts.ToBlock
				} else {
					s.headNext = next
				}
			case !s.exhausted && !s.covered(opts.FromBlock):
				items, next, err := a.fetchPage(ctx, net, wallet, n, s.next)
				if err != nil {
					return nil, err
				}
				calls++
				budget--
				progressed = true
				for _, it := range items {
					s.floor, s.floorSet = it.block(), true
					if it.block() >= opts.FromBlock && it.block() <= opts.ToBlock {
						if err := a.bufferRow(s, n, it); err != nil {
							return drop(err)
						}
					}
					if it.block() < opts.FromBlock {
						break // boundary reached — items below stay unscanned
					}
				}
				if next == nil {
					s.exhausted = true
				} else {
					s.next = next
				}
			}
		}
		if !progressed {
			break
		}
	}

	// Emit per covered stream; unfinished streams emit nothing.
	page := &provider.TransferPage{UpstreamCalls: calls}
	allCovered := true
	for _, n := range names {
		s := st.streams[n]
		cov := s.covered(opts.FromBlock)
		if !cov {
			allCovered = false
		}
		fin := cov && !s.catchUp
		if !fin {
			allCovered = false
		}
		page.Streams = append(page.Streams, provider.StreamStatus{Name: n, Finished: fin})
		if fin && !s.emitted {
			s.emitted = true
			rows := make([]provider.Transfer, 0, len(s.rows))
			for _, t := range s.rows {
				if t.BlockNumber >= opts.FromBlock && t.BlockNumber <= opts.ToBlock {
					rows = append(rows, t)
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].BlockNumber < rows[j].BlockNumber })
			page.Transfers = append(page.Transfers, rows...)
		}
	}
	if !allCovered {
		ts := &tokenState{V: 1, S: map[string]*tokenStreamState{}}
		for _, n := range names {
			s := st.streams[n]
			ts.S[n] = &tokenStreamState{Next: s.next, HeadNext: s.headNext,
				CatchUp: s.catchUp, Exhausted: s.exhausted, Floor: s.floor,
				FloorSet: s.floorSet, Head: s.head}
		}
		page.NextPageToken = encodeToken(ts)
	}
	return page, nil
}

// bufferRow maps and dedupes one item into the stream buffer; exceeding
// the row cap is a deterministic local bound → internal error + caller
// drops the scan (never silently truncated coverage).
func (a *Adapter) bufferRow(s *streamScan, stream string, it listItem) error {
	t, ok := mapItem(stream, it)
	if !ok {
		return nil
	}
	id := t.TxHash
	if stream == streamToken {
		id += "#" + strconv.Itoa(it.LogIndex)
	}
	if s.seen[id] {
		return nil
	}
	s.seen[id] = true
	s.rows = append(s.rows, t)
	if len(s.rows) > a.maxBufferRows {
		return provider.Errorf(provider.ErrInternal,
			"blockscout: scan buffer cap %d exceeded", a.maxBufferRows)
	}
	return nil
}
