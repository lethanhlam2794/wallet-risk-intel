package resilience

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"checkwallet/internal/provider"
)

// testConfig returns a sane fast config; AllowedHosts must be set per test
// server (SSRF policy is exercised, not bypassed).
func testConfig() Config {
	return Config{
		Source:          "testsrc",
		Timeout:         2 * time.Second,
		Rate:            rate.Inf,
		Burst:           1,
		MaxLimiterWait:  200 * time.Millisecond,
		Concurrency:     4,
		MaxRetries:      0,
		Backoff:         time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     time.Minute,
		MaxBodyBytes:    4 << 20,
		MaxRetryWait:    5 * time.Minute,
	}
}

// newServerClient builds a client pointed at the httptest host.
func newServerClient(t *testing.T, srv *httptest.Server, mutate func(*Config), opts ...Option) *Client {
	t.Helper()
	cfg := testConfig()
	cfg.AllowedHosts = []string{hostOf(t, srv)}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := NewClient(cfg, opts...)
	require.NoError(t, err)
	return c
}

func hostOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u := srv.URL
	require.True(t, strings.HasPrefix(u, "http://"))
	return strings.TrimPrefix(u, "http://")
}

func getReq(t *testing.T, ctx context.Context, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	return req
}

func requireLookupErr(t *testing.T, err error) *provider.LookupError {
	t.Helper()
	var le *provider.LookupError
	require.True(t, errors.As(err, &le), "expected *provider.LookupError, got %T: %v", err, err)
	return le
}

func TestDo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, nil)

	res, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.JSONEq(t, `{"ok":true}`, string(res.Body))
	assert.Equal(t, 1, res.Attempts)
	assert.Greater(t, res.Latency, time.Duration(0))
	assert.Equal(t, "yes", res.Header.Get("X-Test"))
}

func TestDo_429RateLimitedCarriesRetryAfter(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) { cfg.MaxRetries = 2 })

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Equal(t, http.StatusTooManyRequests, le.HTTPStatus)
	assert.Equal(t, 7*time.Second, le.RetryAfter)
	// 429 never sleeps in-request and never retries.
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

func TestDo_RetryAfterHintCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "99999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) { cfg.MaxRetryWait = 3 * time.Minute })

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Equal(t, 3*time.Minute, le.RetryAfter)
}

func TestDo_5xxRetriesThenUpstreamError(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) { cfg.MaxRetries = 2 })

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrUpstream5xx, le.Kind)
	assert.Equal(t, http.StatusBadGateway, le.HTTPStatus)
	// bounded retry: 1 + MaxRetries attempts, never more.
	assert.Equal(t, int64(3), atomic.LoadInt64(&hits))
}

func TestDo_4xxClientErrorNonRetriable(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad key"}`)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) { cfg.MaxRetries = 2 })

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrClient4xx, le.Kind)
	assert.Equal(t, http.StatusUnauthorized, le.HTTPStatus)
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
	// Upstream error body is preserved (truncated) for adapter diagnostics.
	assert.Contains(t, string(le.Response), "bad key")
}

func TestDo_ContextDeadlineIsTimeoutNotRetried(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.MaxRetries = 2
		cfg.Timeout = 5 * time.Second // parent ctx wins — must be reported as timeout
	})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := c.Do(ctx, getReq(t, ctx, srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrTimeout, le.Kind)
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

func TestDo_OversizedBodyIsMalformedNonRetriable(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write(make([]byte, 64))
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.MaxBodyBytes = 16
		cfg.MaxRetries = 2
	})

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrMalformed, le.Kind)
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

func TestDo_LimiterConsumesTokenPerAttempt(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.Rate = rate.Every(40 * time.Millisecond)
		cfg.Burst = 1
		cfg.MaxLimiterWait = 2 * time.Second
		cfg.MaxRetries = 2
		cfg.Backoff = time.Millisecond
	})

	start := time.Now()
	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	elapsed := time.Since(start)
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrUpstream5xx, le.Kind)
	// All 3 attempts reached the server AND each consumed a limiter token:
	// attempt 1 burst, attempts 2-3 waited ≥1 refill each (~80ms total).
	assert.Equal(t, int64(3), atomic.LoadInt64(&hits))
	assert.GreaterOrEqual(t, elapsed, 60*time.Millisecond)
}

func TestDo_LimiterQueueBeyondMaxWaitFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.Rate = rate.Every(time.Hour)
		cfg.Burst = 1
		cfg.MaxLimiterWait = 50 * time.Millisecond
	})

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	require.NoError(t, err) // burst token consumed

	start := time.Now()
	_, err = c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	assert.Less(t, time.Since(start), 500*time.Millisecond, "must fail fast, not queue")
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Greater(t, le.RetryAfter, time.Duration(0))
}

func TestDo_LimiterWaitCancelledByContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.Rate = rate.Every(250 * time.Millisecond)
		cfg.Burst = 1
		cfg.MaxLimiterWait = 2 * time.Second // wait allowed, ctx kills it
	})

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err = c.Do(ctx, getReq(t, ctx, srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrTimeout, le.Kind)
}

func TestDo_SemaphoreCapsConcurrency(t *testing.T) {
	var inFlight, maxSeen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&inFlight, 1)
		for {
			m := atomic.LoadInt64(&maxSeen)
			if n <= m || atomic.CompareAndSwapInt64(&maxSeen, m, n) {
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) { cfg.Concurrency = 1 })

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
			done <- err
		}()
	}
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	assert.Equal(t, int64(1), atomic.LoadInt64(&maxSeen))
}

func TestDo_CrossHostRedirectRefused(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "evil")
	}))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, nil)

	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrInternal, le.Kind)
}

func TestDo_SameHostRedirectFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/real" {
			fmt.Fprint(w, "ok-body")
			return
		}
		http.Redirect(w, r, "/real", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, nil)

	res, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	require.NoError(t, err)
	assert.Equal(t, "ok-body", string(res.Body))
}

func TestRedirectPolicy_SchemeDowngradeRefused(t *testing.T) {
	mk := func(scheme, host string) *http.Request {
		return &http.Request{URL: &url.URL{Scheme: scheme, Host: host, Path: "/x"}}
	}
	via := []*http.Request{mk("https", "api.example.com")}

	// Same-host https→http downgrade is refused.
	assert.ErrorIs(t, redirectPolicy(mk("http", "api.example.com"), via),
		http.ErrUseLastResponse)
	// Multi-hop: original https, intermediate https, target http — refused.
	via2 := []*http.Request{mk("https", "api.example.com"), mk("https", "api.example.com")}
	assert.ErrorIs(t, redirectPolicy(mk("http", "api.example.com"), via2),
		http.ErrUseLastResponse)
	// Same-host https→https stays allowed; http→http same-host allowed.
	assert.NoError(t, redirectPolicy(mk("https", "api.example.com"), via))
	httpVia := []*http.Request{mk("http", "api.example.com")}
	assert.NoError(t, redirectPolicy(mk("http", "api.example.com"), httpVia))
	// Cross-host refused on either scheme.
	assert.ErrorIs(t, redirectPolicy(mk("https", "evil.example.com"), via),
		http.ErrUseLastResponse)
	assert.ErrorIs(t, redirectPolicy(mk("http", "evil.example.com"), httpVia),
		http.ErrUseLastResponse)
}

func TestDo_CallerCancelDoesNotFeedBreaker(t *testing.T) {
	// R7: a caller cancel mid-flight is not upstream-failure evidence —
	// it must not count toward opening the circuit.
	var serve atomic.Value // stores http.HandlerFunc
	serve.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until the request ctx is canceled
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serve.Load().(http.HandlerFunc)(w, r)
	}))
	t.Cleanup(srv.Close)
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.BreakerFailures = 1 // any counted failure opens the circuit
		cfg.BreakerOpen = time.Minute
		cfg.MaxRetries = 0
	})

	// Two caller cancels while requests are in flight.
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := c.Do(ctx, getReq(t, ctx, srv.URL))
			done <- err
		}()
		time.Sleep(30 * time.Millisecond) // let the request get in flight
		cancel()
		require.Error(t, <-done)
	}

	// If cancels counted, the breaker is open — a healthy request must
	// instead succeed, proving cancels were not breaker-failure evidence.
	serve.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	res, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le, isLE := err.(*provider.LookupError)
	if isLE && le.Kind == provider.ErrCircuitOpen {
		t.Fatal("caller cancellations opened the breaker — must not count")
	}
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestDo_DisallowedHostFailsBeforeWire(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
	}))
	t.Cleanup(srv.Close)
	cfg := testConfig()
	cfg.AllowedHosts = []string{"example.com"}
	c, err := NewClient(cfg)
	require.NoError(t, err)

	_, err = c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrInternal, le.Kind)
	assert.Equal(t, int64(0), atomic.LoadInt64(&hits))
}

func TestDo_BreakerOpensAfterConsecutiveFailures(t *testing.T) {
	var hits int64
	var serve atomic.Value // stores http.HandlerFunc
	serve.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		serve.Load().(http.HandlerFunc)(w, r)
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	clock := func() time.Time { return now }
	c := newServerClient(t, srv, func(cfg *Config) {
		cfg.BreakerFailures = 3
		cfg.BreakerOpen = time.Minute
		cfg.MaxRetries = 0
	}, WithClock(clock))

	for i := 0; i < 3; i++ {
		_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
		requireLookupErr(t, err)
	}
	require.Equal(t, int64(3), atomic.LoadInt64(&hits))

	// Open: rejects fast without touching the wire.
	_, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	le := requireLookupErr(t, err)
	assert.Equal(t, provider.ErrCircuitOpen, le.Kind)
	assert.Equal(t, int64(3), atomic.LoadInt64(&hits))

	// Still open inside the window.
	now = now.Add(30 * time.Second)
	_, err = c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	assert.Equal(t, provider.ErrCircuitOpen, requireLookupErr(t, err).Kind)

	// Past the open window a half-open probe is allowed; on success the
	// breaker closes.
	now = now.Add(2 * time.Minute)
	atomic.StoreInt64(&hits, 0)
	serve.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	res, err := c.Do(context.Background(), getReq(t, context.Background(), srv.URL))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

func TestDo_HooksRecordMetrics(t *testing.T) {
	var results, errs, rateLimited, circuit []string
	hooks := Hooks{
		OnResult:      func(src, res string) { results = append(results, src+":"+res) },
		OnError:       func(src, reason string) { errs = append(errs, src+":"+reason) },
		OnRateLimited: func(src string) { rateLimited = append(rateLimited, src) },
		OnCircuit:     func(src string, open bool) { circuit = append(circuit, fmt.Sprintf("%s:%v", src, open)) },
	}

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(okSrv.Close)
	c := newServerClient(t, okSrv, func(cfg *Config) { cfg.Hooks = hooks })
	_, err := c.Do(context.Background(), getReq(t, context.Background(), okSrv.URL))
	require.NoError(t, err)

	rlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(rlSrv.Close)
	c2 := newServerClient(t, rlSrv, func(cfg *Config) { cfg.Hooks = hooks })
	_, err = c2.Do(context.Background(), getReq(t, context.Background(), rlSrv.URL))
	requireLookupErr(t, err)

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	c3 := newServerClient(t, failSrv, func(cfg *Config) {
		cfg.Hooks = hooks
		cfg.BreakerFailures = 1
	})
	_, err = c3.Do(context.Background(), getReq(t, context.Background(), failSrv.URL))
	requireLookupErr(t, err)

	assert.Contains(t, results, "testsrc:ok")
	assert.Contains(t, results, "testsrc:rate_limited")
	assert.Contains(t, errs, "testsrc:rate_limited")
	assert.Contains(t, rateLimited, "testsrc")
	assert.Contains(t, circuit, "testsrc:true")
}

func TestMalformedHelperProducesTypedError(t *testing.T) {
	err := provider.Errorf(provider.ErrMalformed, "decode: %s", "bad json")
	var le *provider.LookupError
	require.True(t, errors.As(err, &le))
	assert.Equal(t, provider.ErrMalformed, le.Kind)
	assert.Contains(t, err.Error(), "bad json")
}
