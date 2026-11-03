package enrichment

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"checkwallet/internal/db"
	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

var ethNet = network.Network{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum", Name: "Ethereum"}

// testAddr is deliberately distinct from addresses used by other packages'
// integration tests — packages run in parallel against one check_wallet_test
// database and a shared wallet row would leak between tests.
const testAddr = "0x0000000000000000000000000000000000000e01"

// fakeProvider is a scripted provider.Provider — no contract distortion,
// just canned LookupResult/LookupError returns.
type fakeProvider struct {
	name    string
	enabled bool
	calls   int64
	delay   time.Duration
	result  *provider.LookupResult
	err     error
}

func (f *fakeProvider) Name() string  { return f.name }
func (f *fakeProvider) Enabled() bool { return f.enabled }

func (f *fakeProvider) LookupAddress(ctx context.Context, net network.Network,
	normalized string) (*provider.LookupResult, error) {
	atomic.AddInt64(&f.calls, 1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, &provider.LookupError{Kind: provider.ErrTimeout, Msg: "ctx"}
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func okResult(source string) *provider.LookupResult {
	return &provider.LookupResult{
		Source:         source,
		Network:        "ethereum",
		Address:        testAddr,
		RequestedChain: "ETH",
		Observations: []provider.Observation{{
			Network: "ethereum", Address: testAddr, Category: "SANCTIONED",
			Label: "sanction", Entity: "Lazarus Group", Source: source,
			Confidence: "unknown", VerificationStatus: "reported",
			ObservedAt: time.Now(), Evidence: json.RawMessage(`{"k":"v"}`),
		}},
		Signals:       map[string]any{"label": "Lazarus Group"},
		ProviderScore: fp(100.0),
		FetchedAt:     time.Now(),
		Raw:           json.RawMessage(`{"entities":[]}`),
	}
}

func fp(v float64) *float64 { return &v }

// packageDSN is this package's private test database — go test runs
// packages in parallel, so sharing check_wallet_test lets a sibling
// package's per-test TRUNCATE wipe rows between our insert and assert.
// (Same reasoning for Redis logical DB 10 instead of the shared 9.)
const packageDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_enrichment_test?sslmode=disable"

func packageRedis(t *testing.T) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(testutil.TestRedisURL())
	require.NoError(t, err)
	opts.DB = 10
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, rdb.Ping(ctx).Err())
	require.NoError(t, rdb.FlushDB(ctx).Err())
	return rdb
}

func newService(t *testing.T, pool bool, provs ...provider.Provider) *Service {
	t.Helper()
	rdb := packageRedis(t)
	s := &Service{
		providers: map[string]provider.Provider{},
		rdb:       rdb,
		log:       slog.New(slog.DiscardHandler),
		posTTL:    15 * time.Minute,
		negTTL:    time.Minute,
		cooldown:  time.Minute,
		deadline:  25 * time.Second,
		retain:    25,
	}
	if pool {
		p := testutil.RequireTestPoolDSN(t, packageDSN)
		s.pool = p
		s.q = db.New(p)
		s.wallets = wallet.NewService(s.q)
	}
	for _, p := range provs {
		s.providers[p.Name()] = p
		s.order = append(s.order, p.Name())
	}
	return s
}

func TestEnrich_CacheHitOnSecondCall(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, false, fake)

	r1, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, r1.Status)
	require.Len(t, r1.Providers, 1)
	assert.False(t, r1.Providers[0].Cached)
	assert.Equal(t, "OK", r1.Providers[0].Status)

	r2, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r2.Providers[0].Cached, "second call must serve cache")
	assert.Equal(t, int64(1), atomic.LoadInt64(&fake.calls))
	assert.Equal(t, "OK", r2.Providers[0].Status)
}

func TestEnrich_RefreshBypassesThenCooldownAllowsCache(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, false, fake)

	_, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{Refresh: true})
	require.NoError(t, err)
	assert.False(t, r.Providers[0].Cached, "refresh=1 must bypass cache read")
	assert.Equal(t, int64(2), atomic.LoadInt64(&fake.calls))

	r, err = s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{Refresh: true})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Cached, "inside cooldown refresh behaves as a normal request")
	assert.Equal(t, int64(2), atomic.LoadInt64(&fake.calls))
}

func TestEnrich_NegativeCacheReplaysAsNonClean(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrRateLimited, RetryAfter: 30 * time.Second, Msg: "429"}}
	s := newService(t, false, fake)

	r1, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, r1.Status)
	assert.Equal(t, "RATE_LIMITED", r1.Providers[0].Status)
	require.NotNil(t, r1.Providers[0].Error)
	assert.Equal(t, int64(30), r1.Providers[0].Error.RetryAfter)

	r2, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r2.Providers[0].Cached)
	assert.Equal(t, "RATE_LIMITED", r2.Providers[0].Status,
		"a negative entry must never replay as clean")
	assert.Equal(t, int64(1), atomic.LoadInt64(&fake.calls))
}

func TestEnrich_PartialOnMixedOutcomes(t *testing.T) {
	ok := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	bad := &fakeProvider{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrTimeout, Msg: "deadline"}}
	s := newService(t, false, ok, bad)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusPartial, r.Status)
}

func TestEnrich_FailedWhenAllAttemptedFail(t *testing.T) {
	b1 := &fakeProvider{name: "goplus", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrUpstream5xx, Msg: "502"}}
	b2 := &fakeProvider{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrTransport, Msg: "dial"}}
	s := newService(t, false, b1, b2)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, r.Status)
}

func TestEnrich_InsufficientDataWhenAllDisabled(t *testing.T) {
	d := &fakeProvider{name: "chainabuse", enabled: false}
	s := newService(t, false, d)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusInsufficientData, r.Status)
	require.Len(t, r.Providers, 1)
	assert.False(t, r.Providers[0].Enabled)
	assert.Equal(t, "DISABLED", r.Providers[0].Status)
	assert.Equal(t, int64(0), atomic.LoadInt64(&d.calls), "disabled → zero calls")
}

func TestEnrich_RedisDownBypassesCacheAndStillServes(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, false, fake)
	// Point at a dead port — every Redis op errors → fail-open.
	s.rdb = redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399",
		DialTimeout: 50 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { s.rdb.Close() })

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, "OK", r.Providers[0].Status)
	assert.Equal(t, "bypassed", r.Providers[0].Cache,
		"Redis outage must be visible, never silent")
}

func TestEnrich_RefreshWithRedisDownStillMarksBypassed(t *testing.T) {
	// Round-1 R3 regression: refresh + Redis outage must surface the
	// bypassed marker even though the guard itself fails open.
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, false, fake)
	s.rdb = redis.NewClient(&redis.Options{Addr: "127.0.0.1:6399",
		DialTimeout: 50 * time.Millisecond, MaxRetries: 0})
	t.Cleanup(func() { s.rdb.Close() })

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr,
		Options{Refresh: true})
	require.NoError(t, err)
	assert.Equal(t, "OK", r.Providers[0].Status)
	assert.Equal(t, "bypassed", r.Providers[0].Cache,
		"refresh+outage must not silently lose the bypassed marker")
	assert.False(t, r.Providers[0].Cached)
}

func TestEnrich_PersistsObservationRow(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, true, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Persisted)

	var status, reqChain string
	var payload []byte
	var score *float64
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT po.status, po.requested_chain, po.payload, po.provider_score
		FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'goplus' ORDER BY po.fetched_at DESC LIMIT 1`).
		Scan(&status, &reqChain, &payload, &score))
	assert.Equal(t, "OK", status)
	assert.Equal(t, "ETH", reqChain)
	assert.JSONEq(t, `{"entities":[]}`, string(payload))
	require.NotNil(t, score)
	assert.Equal(t, 100.0, *score)
}

func TestEnrich_PersistsFailureRowToo(t *testing.T) {
	fake := &fakeProvider{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrUpstream5xx, HTTPStatus: 502, Msg: "bad gateway"}}
	s := newService(t, true, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Persisted)

	var status string
	var httpStatus *int32
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT po.status, po.http_status FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'publicaml' ORDER BY po.fetched_at DESC LIMIT 1`).
		Scan(&status, &httpStatus))
	assert.Equal(t, "UPSTREAM_5XX", status)
	require.NotNil(t, httpStatus)
	assert.Equal(t, int32(502), *httpStatus)
}

func TestEnrich_PersistsNonJSONErrorBody(t *testing.T) {
	// Round-2 defect: a raw upstream error body (HTML 502 page) must not
	// roll back the JSONB payload insert — it rides in an envelope so the
	// audit row always persists.
	fake := &fakeProvider{name: "goplus", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrUpstream5xx,
			HTTPStatus:     502,
			Msg:            "bad gateway",
			RequestedChain: "1",
			Response:       []byte("<html><body>502 Bad Gateway</body></html>")}}
	s := newService(t, true, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Persisted,
		"non-JSON upstream body must not drop the audit row")

	var status, reqChain string
	var payload []byte
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT po.status, po.requested_chain, po.payload
		FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'goplus' ORDER BY po.fetched_at DESC LIMIT 1`).
		Scan(&status, &reqChain, &payload))
	assert.Equal(t, "UPSTREAM_5XX", status)
	assert.Equal(t, "1", reqChain, "stamped literal token survives persist")
	assert.JSONEq(t, `{"upstream_body":"<html><body>502 Bad Gateway</body></html>"}`,
		string(payload))
}

// TestEnrich_PersistsJSONBHostileBodies proves the audit row lands for
// every byte shape Postgres jsonb rejects but Go tolerates — lone
// surrogates, \u0000 escapes, invalid UTF-8, and literal NUL bytes, on
// BOTH the success-raw and error-response paths (round-3 finding).
func TestEnrich_PersistsJSONBHostileBodies(t *testing.T) {
	field := func(t *testing.T, p []byte, key string) string {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal(p, &m))
		v, ok := m[key].(string)
		require.True(t, ok, "payload missing string field %q: %s", key, p)
		return v
	}
	cases := []struct {
		name  string
		raw   string // verbatim-JSON source body (lr.Raw)
		err   string // error-path body (lerr.Response)
		check func(t *testing.T, payload []byte)
	}{
		// Postgres decodes escapes on read-back, so assertions check the
		// decoded value: hostile bytes normalize to U+FFFD, Postgres-legal
		// content (surrogate pairs) decodes to the real char.
		{name: "nul-escape", raw: `{"e":"\u0000"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "\uFFFD", field(t, p, "e"))
			}},
		{name: "literal-nul-inside-json", raw: "{\"e\":\"\x00\"}",
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "\uFFFD", field(t, p, "e"))
			}},
		{name: "lone-surrogate", raw: `{"e":"\ud800"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "\uFFFD", field(t, p, "e"))
			}},
		{name: "lone-surrogate-uppercase-D", raw: `{"e":"\uD800"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "\uFFFD", field(t, p, "e"))
			}},
		{name: "valid-surrogate-pair-preserved", raw: `{"e":"\ud83d\ude00"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "😀", field(t, p, "e"),
					"Postgres-legal pairs must persist verbatim")
			}},
		// Escaped-backslash parity (round-4 edge): `\\ud83d` is LITERAL
		// text, not an escape — so the trailing `\udc00` is a real lone
		// low surrogate that must still be neutralized.
		{name: "escaped-backslash-then-lone-low", raw: `{"e":"\\ud83d\udc00"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, `\ud83d`+"\uFFFD", field(t, p, "e"))
			}},
		// Same parity, benign side: `\\uD800` literal text has no real
		// surrogate escape at all — persists verbatim.
		{name: "escaped-backslash-literal-surrogate-text", raw: `{"e":"\\uD800"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, `\uD800`, field(t, p, "e"))
			}},
		{name: "invalid-utf8-inside-json", raw: "{\"e\":\"caf\xe9\"}",
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "caf\uFFFD", field(t, p, "e"))
			}},
		{name: "binary-error-body", err: "\x00\x01binary-err",
			check: func(t *testing.T, p []byte) {
				v := field(t, p, "upstream_body")
				assert.Contains(t, v, "binary-err")
				assert.NotContains(t, v, "\x00",
					"envelope must never emit a rejected NUL")
			}},
		{name: "json-error-body-verbatim", err: `{"error":"denied"}`,
			check: func(t *testing.T, p []byte) {
				assert.Equal(t, "denied", field(t, p, "error"))
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fake *fakeProvider
			if tc.raw != "" {
				res := okResult("goplus")
				res.Raw = json.RawMessage(tc.raw)
				fake = &fakeProvider{name: "goplus", enabled: true, result: res}
			} else {
				fake = &fakeProvider{name: "goplus", enabled: true,
					err: &provider.LookupError{Kind: provider.ErrUpstream5xx,
						HTTPStatus: 502, Msg: "bad", RequestedChain: "1",
						Response: []byte(tc.err)}}
			}
			s := newService(t, true, fake)

			r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
			require.NoError(t, err)
			assert.True(t, r.Providers[0].Persisted,
				"hostile bytes must never roll back the audit row")

			var payload []byte
			require.NoError(t, s.pool.QueryRow(context.Background(), `
				SELECT po.payload FROM provider_observations po
				JOIN data_sources ds ON ds.id = po.source_id
				WHERE ds.code = 'goplus' ORDER BY po.fetched_at DESC LIMIT 1`).
				Scan(&payload))
			tc.check(t, payload)
		})
	}
}

func TestEnrich_PersistsHostileErrorMessage(t *testing.T) {
	// The error JSONB column is marshaled separately from payload — an
	// upstream-controlled Msg (e.g. publicaml's {error} text) carrying
	// NUL/invalid-UTF-8 must not roll back the row either.
	fake := &fakeProvider{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrDegraded,
			Msg: "upstream: \x00bad\xe9utf8"}}
	s := newService(t, true, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Persisted,
		"hostile error message must not roll back the audit row")

	var errJSON []byte
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT po.error FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'publicaml' ORDER BY po.fetched_at DESC LIMIT 1`).
		Scan(&errJSON))
	var m map[string]any
	require.NoError(t, json.Unmarshal(errJSON, &m))
	msg, ok := m["message"].(string)
	require.True(t, ok, "error.message missing: %s", errJSON)
	assert.NotContains(t, msg, "\x00", "NUL must be sanitized out")
	assert.Contains(t, msg, "\uFFFD", "hostile bytes normalize to U+FFFD")
}

func TestEnrich_PersistsJSONErrorBodyVerbatim(t *testing.T) {
	fake := &fakeProvider{name: "publicaml", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrClient4xx,
			HTTPStatus: 400, Msg: "bad request",
			RequestedChain: "ETH",
			Response:       []byte(`{"error":"unknown chain"}`)}}
	s := newService(t, true, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.True(t, r.Providers[0].Persisted)

	var payload []byte
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT po.payload FROM provider_observations po
		JOIN data_sources ds ON ds.id = po.source_id
		WHERE ds.code = 'publicaml' ORDER BY po.fetched_at DESC LIMIT 1`).
		Scan(&payload))
	assert.JSONEq(t, `{"error":"unknown chain"}`, string(payload))
}

func TestEnrich_DuplicateProviderFilterDedupes(t *testing.T) {
	ok := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	s := newService(t, false, ok)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr,
		Options{Providers: []string{"goplus", "goplus"}})
	require.NoError(t, err)
	require.Len(t, r.Providers, 1, "duplicate filter names must not duplicate blocks")
	assert.Equal(t, int64(1), atomic.LoadInt64(&ok.calls))
}

func TestEnrich_ProviderSubsetFilter(t *testing.T) {
	ok := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	other := &fakeProvider{name: "publicaml", enabled: true, result: okResult("publicaml")}
	s := newService(t, false, ok, other)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr,
		Options{Providers: []string{"publicaml"}})
	require.NoError(t, err)
	require.Len(t, r.Providers, 1)
	assert.Equal(t, "publicaml", r.Providers[0].Source)
	assert.Equal(t, int64(0), atomic.LoadInt64(&ok.calls))
}

func TestEnrich_UnknownProviderRejected(t *testing.T) {
	s := newService(t, false)
	_, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr,
		Options{Providers: []string{"notarealprovider"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
}

func TestEnrich_SignalsNeverNull(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus")}
	fake.result.Signals = nil
	s := newService(t, false, fake)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	b, _ := json.Marshal(r.Providers[0])
	assert.Contains(t, string(b), `"signals":{}`, "signals must emit {} not null")
}

func TestEnrich_SingleflightCoalescesConcurrentMisses(t *testing.T) {
	fake := &fakeProvider{name: "goplus", enabled: true, result: okResult("goplus"),
		delay: 50 * time.Millisecond}
	s := newService(t, false, fake)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(1), atomic.LoadInt64(&fake.calls),
		"concurrent misses on the same key must coalesce")
}

func TestEnrich_SiblingFailureDoesNotCancelOthers(t *testing.T) {
	// F4: the fast-failing sibling must not fabricate ctx-canceled on the
	// slow in-flight sibling — outcomes are recorded independently.
	fast := &fakeProvider{name: "goplus", enabled: true,
		err: &provider.LookupError{Kind: provider.ErrUpstream5xx, Msg: "instant 502"}}
	slow := &fakeProvider{name: "publicaml", enabled: true, result: okResult("publicaml"),
		delay: 40 * time.Millisecond}
	s := newService(t, false, fast, slow)

	r, err := s.Enrich(context.Background(), ethNet, testAddr, testAddr, Options{})
	require.NoError(t, err)
	assert.Equal(t, StatusPartial, r.Status)
	bySource := map[string]string{}
	for _, p := range r.Providers {
		bySource[p.Source] = p.Status
	}
	assert.Equal(t, "UPSTREAM_5XX", bySource["goplus"])
	assert.Equal(t, "OK", bySource["publicaml"],
		"slow sibling must complete — never a fabricated cancel")
}
