// Package resilience is the shared outbound-HTTP toolkit every on-demand
// provider adapter wraps (spec §15): per-attempt token bucket, concurrency
// semaphore, consecutive-failure circuit breaker, bounded jittered retry,
// same-host-only redirects, bounded response bodies, and per-request host
// allowlisting (spec §16 SSRF).
package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"checkwallet/internal/provider"
)

// Config carries every resilience policy for one provider. Adapters supply
// hardcoded AllowedHosts and validated base URLs at construction (F6).
type Config struct {
	Source          string        // provider code — hooks/log label
	Timeout         time.Duration // per-attempt timeout (ctx-derived)
	Rate            rate.Limit    // token bucket refill; rate.Inf = unlimited
	Burst           int           // bucket size
	MaxLimiterWait  time.Duration // limiter delay beyond this fails fast
	Concurrency     int           // max simultaneous attempts
	MaxRetries      int           // retries AFTER the first attempt
	Backoff         time.Duration // retry backoff base (jittered exponential)
	BreakerFailures int           // consecutive failures before open
	BreakerOpen     time.Duration // open window before half-open probe
	MaxBodyBytes    int64         // response body cap
	AllowedHosts    []string      // validated per request (SSRF, spec §16)
	MaxRetryWait    time.Duration // cap on upstream Retry-After hints
	Hooks           Hooks
}

// Hooks decouples metrics emission from the toolkit (avoids a
// resilience→metrics import). All fields are optional.
type Hooks struct {
	OnResult      func(source, result string) // per completed Do — "ok" or an ErrKind
	OnError       func(source, reason string) // per failed Do — ErrKind reason
	OnRateLimited func(source string)         // 429 or local limiter fail-fast
	OnCircuit     func(source string, open bool)
}

// Result is one successful bounded response.
type Result struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Attempts   int
	Latency    time.Duration // wall time of the whole Do call
}

// Option is a construction-time knob (test hooks only — never used by
// production wiring).
type Option func(*Client)

// WithHTTPClient swaps the underlying http.Client (tests inject transports).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithClock swaps the breaker/timeout clock (tests advance open windows).
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		c.now = now
		c.breaker = newBreaker(c.breaker.threshold, c.breaker.openFor, now)
	}
}

// Client is a per-provider resilient HTTP executor. The zero value is not
// usable; build via NewClient.
type Client struct {
	cfg     Config
	http    *http.Client
	limiter *limiter
	sem     *semaphore.Weighted
	breaker *breaker
	hooks   Hooks
	now     func() time.Time
}

// NewClient validates the config and builds the toolkit.
func NewClient(cfg Config, opts ...Option) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	if cfg.MaxLimiterWait <= 0 {
		cfg.MaxLimiterWait = 250 * time.Millisecond
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.MaxRetries < 0 {
		return nil, fmt.Errorf("resilience %s: MaxRetries must be >= 0", cfg.Source)
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 200 * time.Millisecond
	}
	if cfg.BreakerFailures <= 0 {
		cfg.BreakerFailures = 5
	}
	if cfg.BreakerOpen <= 0 {
		cfg.BreakerOpen = time.Minute
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20
	}
	if cfg.MaxRetryWait <= 0 {
		cfg.MaxRetryWait = 5 * time.Minute
	}
	if cfg.Rate == 0 {
		cfg.Rate = rate.Inf
	}
	if cfg.Burst <= 0 && cfg.Rate != rate.Inf {
		cfg.Burst = 1
	}

	now := time.Now
	c := &Client{
		cfg:   cfg,
		hooks: cfg.Hooks,
		now:   now,
	}
	c.limiter = newLimiter(cfg.Rate, cfg.Burst, cfg.MaxLimiterWait)
	c.sem = semaphore.NewWeighted(int64(cfg.Concurrency))
	c.breaker = newBreaker(cfg.BreakerFailures, cfg.BreakerOpen, now)
	c.http = &http.Client{
		// No Client.Timeout — per-attempt deadlines ride the request ctx so
		// classification can distinguish attempt timeout vs caller cancel.
		CheckRedirect: redirectPolicy,
	}
	for _, o := range opts {
		o(c)
	}
	// Wire breaker transitions after options (WithClock may rebuild it).
	c.breaker.onTransition = func(open bool) { c.circuitChanged(open) }
	return c, nil
}

// redirectPolicy bounds redirects to the originating host and refuses
// https→http downgrades — a compromised upstream must neither pull the
// request (and its Authorization header) to another host nor strip TLS.
// Surfaced 3xx responses are classified as refused redirects in attempt().
func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return fmt.Errorf("resilience: too many redirects")
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		// Cross-host redirect — surface the 3xx (spec §16).
		return http.ErrUseLastResponse
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		// Same-host scheme downgrade — refused.
		return http.ErrUseLastResponse
	}
	return nil
}

// Do executes the request through limit → semaphore → breaker → attempt.
// One limiter token is consumed per HTTP attempt (F2: retries cannot
// triple-burn upstream quota). 429 never sleeps in-request — the typed
// error carries the Retry-After hint to the caller.
func (c *Client) Do(ctx context.Context, req *http.Request) (*Result, error) {
	started := c.now()
	if err := c.checkHost(req.URL); err != nil {
		return nil, c.fail(err)
	}
	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := backoffFor(attempt, c.cfg.Backoff)
			select {
			case <-ctx.Done():
				return nil, c.fail(lookupErr(provider.ErrTimeout, "context done during retry backoff: %v", ctx.Err()))
			case <-time.After(wait):
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, c.fail(err)
		}
		if err := c.sem.Acquire(ctx, 1); err != nil {
			return nil, c.fail(lookupErr(provider.ErrTimeout, "semaphore acquire: %v", ctx.Err()))
		}
		if err := c.breaker.Allow(); err != nil {
			c.sem.Release(1)
			return nil, c.fail(err)
		}
		res, retryable, err := c.attempt(ctx, req, attempt+1, started)
		c.sem.Release(1)
		if err == nil {
			c.breaker.Report(true)
			c.ok()
			return res, nil
		}
		if ctx.Err() == nil {
			// Caller cancel/deadline is not upstream-failure evidence —
			// only genuine attempt outcomes feed the breaker (F-review).
			c.breaker.Report(false)
		}
		lastErr = err
		if !retryable {
			return nil, c.fail(err)
		}
		if ctx.Err() != nil {
			return nil, c.fail(lookupErr(provider.ErrTimeout, "context done: %v", ctx.Err()))
		}
	}
	return nil, c.fail(lastErr)
}

// attempt performs one HTTP try; retryable reports whether another attempt
// is permitted by policy (5xx/transport/attempt-timeout only).
func (c *Client) attempt(ctx context.Context, req *http.Request, attemptNo int, started time.Time) (*Result, bool, error) {
	attCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	r2, err := cloneForAttempt(attCtx, req)
	if err != nil {
		return nil, false, lookupErr(provider.ErrInternal, "clone request: %v", err)
	}
	resp, err := c.http.Do(r2)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			// Caller cancelled/deadlined — never retriable.
			return nil, false, lookupErr(provider.ErrTimeout, "context done: %v", ctx.Err())
		case attCtx.Err() != nil:
			return nil, true, lookupErr(provider.ErrTimeout, "attempt timeout after %s", c.cfg.Timeout)
		default:
			return nil, true, lookupErr(provider.ErrTransport, "%v", err)
		}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		hint := parseRetryAfter(resp.Header.Get("Retry-After"))
		if hint > c.cfg.MaxRetryWait {
			hint = c.cfg.MaxRetryWait
		}
		return nil, false, &provider.LookupError{
			Kind: provider.ErrRateLimited, HTTPStatus: resp.StatusCode,
			RetryAfter: hint, Msg: "upstream 429",
		}
	case resp.StatusCode >= 500:
		return nil, true, &provider.LookupError{
			Kind: provider.ErrUpstream5xx, HTTPStatus: resp.StatusCode,
			Msg:      fmt.Sprintf("upstream status %d", resp.StatusCode),
			Response: readSnippet(resp.Body),
		}
	case resp.StatusCode >= 400:
		return nil, false, &provider.LookupError{
			Kind: provider.ErrClient4xx, HTTPStatus: resp.StatusCode,
			Msg:      fmt.Sprintf("upstream status %d", resp.StatusCode),
			Response: readSnippet(resp.Body),
		}
	case resp.StatusCode >= 300:
		return nil, false, lookupErr(provider.ErrInternal,
			"cross-host redirect refused (status %d)", resp.StatusCode)
	case resp.StatusCode < 200:
		return nil, false, lookupErr(provider.ErrInternal,
			"unexpected upstream status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, false, lookupErr(provider.ErrInternal, "read body: %v", err)
	}
	if int64(len(body)) > c.cfg.MaxBodyBytes {
		return nil, false, lookupErr(provider.ErrMalformed,
			"response body exceeds %d bytes", c.cfg.MaxBodyBytes)
	}
	return &Result{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       body,
		Attempts:   attemptNo,
		Latency:    c.now().Sub(started),
	}, false, nil
}

// checkHost enforces the per-request SSRF allowlist.
func (c *Client) checkHost(u *url.URL) error {
	if len(c.cfg.AllowedHosts) == 0 {
		return lookupErr(provider.ErrInternal, "no allowed hosts configured")
	}
	for _, h := range c.cfg.AllowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return lookupErr(provider.ErrInternal, "host %q not in allowlist", u.Host)
}

// cloneForAttempt rebuilds the request so retries can replay the body —
// adapters must construct bodies via bytes.Reader/NewRequest so GetBody
// is populated.
func cloneForAttempt(ctx context.Context, req *http.Request) (*http.Request, error) {
	r2 := req.Clone(ctx)
	if req.GetBody != nil {
		b, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r2.Body = b
	}
	return r2, nil
}

// lookupErr is the internal constructor for transport-side typed errors.
func lookupErr(kind provider.ErrKind, format string, args ...any) *provider.LookupError {
	return provider.Errorf(kind, format, args...)
}

func (c *Client) ok() {
	if c.hooks.OnResult != nil {
		c.hooks.OnResult(c.cfg.Source, "ok")
	}
}

// fail emits outcome hooks for one failed Do and returns the error as-is.
func (c *Client) fail(err error) error {
	var le *provider.LookupError
	kind := provider.ErrInternal
	if errors.As(err, &le) {
		kind = le.Kind
	}
	if c.hooks.OnResult != nil {
		c.hooks.OnResult(c.cfg.Source, string(kind))
	}
	if c.hooks.OnError != nil {
		c.hooks.OnError(c.cfg.Source, string(kind))
	}
	if kind == provider.ErrRateLimited && c.hooks.OnRateLimited != nil {
		c.hooks.OnRateLimited(c.cfg.Source)
	}
	return err
}

// circuitChanged is called by the breaker on every state transition.
func (c *Client) circuitChanged(open bool) {
	if c.hooks.OnCircuit != nil {
		c.hooks.OnCircuit(c.cfg.Source, open)
	}
}

// readSnippet drains a small prefix of an error body for adapter
// diagnostics; bounded so a hostile body cannot grow memory.
func readSnippet(r io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return b
}
