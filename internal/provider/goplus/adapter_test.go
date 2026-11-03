package goplus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

var (
	ethNet = network.Network{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum", Name: "Ethereum"}
	bscNet = network.Network{ID: 2, Namespace: "eip155", ChainReference: "56", Code: "bsc", Name: "BNB Smart Chain"}
)

const sanctionedAddr = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

func testAdapter(t *testing.T, srv *httptest.Server, apiKey string) *Adapter {
	t.Helper()
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, apiKey, 60000,
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a
}

func serveFixture(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lookupErr(t *testing.T, err error) *provider.LookupError {
	t.Helper()
	var le *provider.LookupError
	require.True(t, errors.As(err, &le), "expected *provider.LookupError, got %T: %v", err, err)
	return le
}

func TestLookup_SanctionedFixtureMapsObservations(t *testing.T) {
	srv := serveFixture(t, "address_security_sanctioned.json")
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "goplus", res.Source)
	assert.Equal(t, "ethereum", res.Network)
	assert.Equal(t, sanctionedAddr, res.Address)
	assert.Equal(t, "1", res.RequestedChain)
	assert.NotEmpty(t, res.Raw)
	assert.False(t, res.FetchedAt.IsZero())

	// sanctioned:1 → SANCTIONED, stealing_attack:1 → HACK,
	// blacklist_doubt:1 → OTHER (F8: confident mappings only).
	cats := map[string]string{}
	for _, o := range res.Observations {
		cats[o.Label] = o.Category
		assert.Equal(t, "goplus", o.Source)
		assert.Equal(t, "reported", o.VerificationStatus)
		assert.Equal(t, "unknown", o.Confidence)
		assert.Equal(t, "SlowMist,BlockSec", o.OriginalSource)
		assert.Equal(t, "ethereum", o.Network)
		assert.False(t, o.ObservedAt.IsZero())
		var ev map[string]any
		require.NoError(t, json.Unmarshal(o.Evidence, &ev))
		assert.Equal(t, "SlowMist,BlockSec", ev["data_source"])
	}
	require.Len(t, res.Observations, 3)
	assert.Equal(t, "SANCTIONED", cats["Sanctioned"])
	assert.Equal(t, "HACK", cats["Stealing attack"])
	assert.Equal(t, "OTHER", cats["Blacklist doubt"])

	// All raw flags ride in Signals — including "0" flags which produce
	// NO observations and no safe verdict.
	assert.Equal(t, "0", res.Signals["cybercrime"])
	assert.Equal(t, "1", res.Signals["sanctioned"])
	assert.Equal(t, "SlowMist,BlockSec", res.Signals["data_source"])
}

func TestLookup_CleanFixtureZeroObservationsSignalsKept(t *testing.T) {
	srv := serveFixture(t, "address_security_clean.json")
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet,
		"0x0000000000000000000000000000000000000000")
	require.NoError(t, err)
	assert.Empty(t, res.Observations)
	// "0" fields are data, not a verdict — they still land in Signals.
	assert.Equal(t, "0", res.Signals["sanctioned"])
	assert.Equal(t, "0", res.Signals["stealing_attack"])
	assert.NotNil(t, res.Signals)
}

func TestLookup_CodeNonOneIsDegraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"message":"degraded backend","result":null}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrDegraded, le.Kind)
	assert.Contains(t, le.Error(), "degraded")
}

func TestLookup_NullResultParsesOKEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":1,"message":"ok","result":null}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Empty(t, res.Observations)
	assert.NotNil(t, res.Signals) // emits {} not null (F9)
}

func TestLookup_EmptyResultParsesOKEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":1,"message":"ok","result":{}}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Empty(t, res.Observations)
	assert.NotNil(t, res.Signals)
}

func TestLookup_UnmappedFlagLandsOnOther(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A flag GoPlus adds later must degrade to OTHER, never be dropped.
		fmt.Fprint(w, `{"code":1,"message":"ok","result":{"brand_new_risk":"1","data_source":"X"}}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	require.Len(t, res.Observations, 1)
	assert.Equal(t, "OTHER", res.Observations[0].Category)
	assert.Equal(t, "Brand new risk", res.Observations[0].Label)
}

func TestLookup_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{not json`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	assert.Equal(t, provider.ErrMalformed, lookupErr(t, err).Kind)
}

func TestLookup_429RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Equal(t, 3*time.Second, le.RetryAfter)
}

func TestLookup_500RetriedThenUpstream(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrUpstream5xx, le.Kind)
	assert.Equal(t, int64(3), atomic.LoadInt64(&hits)) // 1 + 2 retries
}

func TestLookup_AuthorizationSentOnlyWithKey(t *testing.T) {
	var authSeen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"code":1,"message":"ok","result":{}}`)
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv, "")
	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "", authSeen.Load().(string))

	a2 := testAdapter(t, srv, "gk-secret")
	_, err = a2.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "Bearer gk-secret", authSeen.Load().(string))
}

func TestLookup_ChainIDFromChainReference(t *testing.T) {
	var gotChain atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotChain.Store(r.URL.Query().Get("chain_id"))
		fmt.Fprint(w, `{"code":1,"message":"ok","result":{}}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "1", gotChain.Load().(string))

	_, err = a.LookupAddress(context.Background(), bscNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "56", gotChain.Load().(string))
}

func TestLookup_RequestPathAndEnabled(t *testing.T) {
	var pathSeen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathSeen.Store(r.URL.Path)
		fmt.Fprint(w, `{"code":1,"message":"ok","result":{}}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	assert.Equal(t, "goplus", a.Name())
	assert.True(t, a.Enabled())

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/address_security/"+sanctionedAddr, pathSeen.Load().(string))
}

func TestNew_RejectsDisallowedBaseURL(t *testing.T) {
	_, err := New("https://evil.example.com", "", 30)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

func TestNew_RejectsHTTPInProduction(t *testing.T) {
	_, err := New("http://api.gopluslabs.io", "", 30)
	require.Error(t, err)
}

func TestNew_AcceptsDefaultURL(t *testing.T) {
	a, err := New("https://api.gopluslabs.io", "", 30)
	require.NoError(t, err)
	assert.True(t, a.Enabled())
}

// Compile-time contract check.
var _ provider.Provider = (*Adapter)(nil)

func TestAdapter_ResilienceHooksOptional(t *testing.T) {
	srv := serveFixture(t, "address_security_clean.json")
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, "", 60000,
		WithInsecureHTTP(), WithAllowedHosts(host),
		WithResilienceOption(resilience.WithClock(time.Now)))
	require.NoError(t, err)
	_, err = a.LookupAddress(context.Background(), ethNet, "0x0000000000000000000000000000000000000000")
	require.NoError(t, err)
}
