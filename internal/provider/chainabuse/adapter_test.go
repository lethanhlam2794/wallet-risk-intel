package chainabuse

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

var ethNet = network.Network{ID: 1, Namespace: "eip155", ChainReference: "1", Code: "ethereum", Name: "Ethereum"}

const addr = "0x098b716b8aaf21512996dc57eb0615e2383e2f96"

// countingTransport proves disabled adapters emit zero traffic.
type countingTransport struct{ n int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt64(&c.n, 1)
	return nil, fmt.Errorf("must never be called")
}

func lookupErr(t *testing.T, err error) *provider.LookupError {
	t.Helper()
	var le *provider.LookupError
	require.True(t, errors.As(err, &le), "expected *provider.LookupError, got %T: %v", err, err)
	return le
}

func TestAdapter_DisabledWithoutKeyZeroTraffic(t *testing.T) {
	ct := &countingTransport{}
	a, err := New("https://api.chainabuse.com", "", 1,
		WithResilienceOption(resilience.WithHTTPClient(&http.Client{Transport: ct})))
	require.NoError(t, err)

	assert.Equal(t, "chainabuse", a.Name())
	assert.False(t, a.Enabled(), "no API key → disabled (10 calls/month tier is unusable)")

	_, err = a.LookupAddress(context.Background(), ethNet, addr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrDisabled, le.Kind)
	assert.Contains(t, le.Error(), "no_api_key")
	assert.Equal(t, int64(0), atomic.LoadInt64(&ct.n), "disabled → zero outbound calls")
}

func testAdapter(t *testing.T, srv *httptest.Server, key string) *Adapter {
	t.Helper()
	host := strings.TrimPrefix(srv.URL, "http://")
	a, err := New(srv.URL, key, 60000,
		WithInsecureHTTP(), WithAllowedHosts(host))
	require.NoError(t, err)
	return a
}

func TestLookup_ReportsMapToReportedOnlyObservations(t *testing.T) {
	body, err := os.ReadFile("testdata/reports_sample.json")
	require.NoError(t, err)
	var gotAuth, gotQuery atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotQuery.Store(r.URL.Query().Get("addresses"))
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "ca-key-123")

	res, err := a.LookupAddress(context.Background(), ethNet, addr)
	require.NoError(t, err)

	// Basic auth key:key per TRM docs → base64("ca-key-123:ca-key-123").
	auth := gotAuth.Load().(string)
	assert.True(t, strings.HasPrefix(auth, "Basic "))
	assert.Equal(t, "ca-key-123", reqUser(t, auth))
	assert.Equal(t, addr, gotQuery.Load().(string))

	require.Len(t, res.Observations, 2)
	got := map[string]string{}
	for _, o := range res.Observations {
		got[o.Label] = o.Category
		// Community reports are never auto-verified.
		assert.Equal(t, "reported", o.VerificationStatus)
		assert.Equal(t, "unknown", o.Confidence)
		assert.Equal(t, "chainabuse", o.Source)
		assert.Equal(t, "ethereum", o.Network)
		assert.NotEmpty(t, o.SourceURL)
	}
	assert.Equal(t, "SCAM", got["Scam"])
	assert.Equal(t, "RANSOMWARE", got["Ransomware"])
	// Chainabuse is address-keyed — no literal chain token is sent upstream,
	// so requested_chain records our internal network code.
	assert.Equal(t, "ethereum", res.RequestedChain)
}

func TestLookup_UnmappedCategoryLandsOnOther(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":1,"category":"Something New","addresses":[]}]`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "k")

	res, err := a.LookupAddress(context.Background(), ethNet, addr)
	require.NoError(t, err)
	require.Len(t, res.Observations, 1)
	assert.Equal(t, "OTHER", res.Observations[0].Category)
}

func TestLookup_EmptyListOKEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "k")

	res, err := a.LookupAddress(context.Background(), ethNet, addr)
	require.NoError(t, err)
	assert.Empty(t, res.Observations)
	assert.NotNil(t, res.Signals)
}

func TestLookup_401ClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "bad-key")

	_, err := a.LookupAddress(context.Background(), ethNet, addr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrClient4xx, le.Kind)
	assert.Equal(t, http.StatusUnauthorized, le.HTTPStatus)
}

func TestLookup_EnabledWithKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "k")
	assert.True(t, a.Enabled())
}

func reqUser(t *testing.T, auth string) string {
	t.Helper()
	// decode "Basic base64(user:pass)" and return user
	require.True(t, strings.HasPrefix(auth, "Basic "))
	dec, err := b64decode(strings.TrimPrefix(auth, "Basic "))
	require.NoError(t, err)
	parts := strings.SplitN(dec, ":", 2)
	require.Len(t, parts, 2)
	assert.Equal(t, parts[0], parts[1], "key used as both user and pass")
	return parts[0]
}

func b64decode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}

var _ provider.Provider = (*Adapter)(nil)
