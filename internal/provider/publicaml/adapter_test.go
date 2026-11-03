package publicaml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestLookup_SanctionedFixture(t *testing.T) {
	srv := serveFixture(t, "enrich_sanctioned.json")
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "publicaml", res.Source)
	assert.Equal(t, "ETH", res.RequestedChain)
	require.NotNil(t, res.ProviderScore)
	assert.Equal(t, 100.0, *res.ProviderScore)

	// Direct attribution → SANCTIONED observation with entity label
	// carried through; sanctioned flag → separate provider claim, both
	// reported-only.
	var found bool
	for _, o := range res.Observations {
		assert.Equal(t, "publicaml", o.Source)
		assert.Equal(t, "reported", o.VerificationStatus)
		assert.Equal(t, "unknown", o.Confidence)
		if o.Category == "SANCTIONED" {
			found = true
			if o.Entity != "" {
				assert.Equal(t, "Lazarus Group", o.Entity)
			}
		}
		assert.NotEmpty(t, o.Evidence)
	}
	assert.True(t, found, "SANCTIONED observation expected")

	// Indirect exposures ride separately — never folded into observations.
	require.NotEmpty(t, res.Exposures)
	exp := res.Exposures[0]
	assert.Equal(t, "0x35fb6f6db4fb05e6a4ce86f2c93691425626d4b1", exp.Counterparty)
	assert.Equal(t, "sanction", exp.Category)
	assert.Equal(t, 1, exp.Hops)
	assert.Equal(t, "outbound", exp.Direction)
	require.NotNil(t, exp.Score)
	assert.Equal(t, 90.0, *exp.Score)

	assert.NotEmpty(t, res.Raw)
	assert.NotNil(t, res.Signals)
	assert.Equal(t, "Lazarus Group", res.Signals["label"])
}

func TestLookup_UnknownFixtureScoreZeroIsData(t *testing.T) {
	srv := serveFixture(t, "enrich_unknown.json")
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet,
		"0x000000000000000000000000000000000000dead")
	require.NoError(t, err)
	// aml_score:0 passes through as data — never a safe verdict.
	require.NotNil(t, res.ProviderScore)
	assert.Equal(t, 0.0, *res.ProviderScore)
	assert.Empty(t, res.Observations) // no direct_attribution, no sanctioned flag
}

func TestLookup_RequestBodyPerAddressChainSpecific(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"entities":[]}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	var req map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &req))
	addrs := req["addresses"].([]any)
	require.Len(t, addrs, 1, "per-address chain-specific calls only — never batch")
	entry := addrs[0].(map[string]any)
	assert.Equal(t, sanctionedAddr, entry["wallet_address"])
	assert.Equal(t, "ETH", entry["chain"], "F1: ethereum must map to ETH, not ETHEREUM")
	assert.Equal(t, float64(5), req["top_n"])
	assert.NotEmpty(t, req["include"])

	_, err = a.LookupAddress(context.Background(), bscNet, sanctionedAddr)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(gotBody, &req))
	entry = req["addresses"].([]any)[0].(map[string]any)
	assert.Equal(t, "BSC", entry["chain"])
}

func TestLookup_UnknownNetworkRejectedClientSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must never send upstream for unmappable chain")
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	bad := network.Network{ID: 9, Namespace: "eip155", ChainReference: "999", Code: "fakenet"}
	_, err := a.LookupAddress(context.Background(), bad, sanctionedAddr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fakenet")
}

func TestLookup_ErrorEnvelopeTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"upstream backend degraded"}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrUpstream5xx, le.Kind)
	assert.Contains(t, le.Error(), "upstream backend degraded")
}

func TestLookup_429RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	le := lookupErr(t, err)
	assert.Equal(t, provider.ErrRateLimited, le.Kind)
	assert.Equal(t, 30*time.Second, le.RetryAfter)
}

func TestLookup_500RetriedThenUpstream(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	assert.Equal(t, provider.ErrUpstream5xx, lookupErr(t, err).Kind)
	assert.Equal(t, int64(3), atomic.LoadInt64(&hits))
}

func TestLookup_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[broken`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	assert.Equal(t, provider.ErrMalformed, lookupErr(t, err).Kind)
}

func TestLookup_EmptyEntitiesOKEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"entities":[],"not_found":["0x1"]}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	res, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Empty(t, res.Observations)
	assert.Nil(t, res.ProviderScore)
	assert.NotNil(t, res.Signals)
}

func TestLookup_AuthorizationSentOnlyWithKey(t *testing.T) {
	var authSeen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"entities":[]}`)
	}))
	t.Cleanup(srv.Close)

	a := testAdapter(t, srv, "")
	_, err := a.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "", authSeen.Load().(string))

	a2 := testAdapter(t, srv, "pk-live")
	_, err = a2.LookupAddress(context.Background(), ethNet, sanctionedAddr)
	require.NoError(t, err)
	assert.Equal(t, "Bearer pk-live", authSeen.Load().(string))
}

func TestLookup_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv, "")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := a.LookupAddress(ctx, ethNet, sanctionedAddr)
	assert.Equal(t, provider.ErrTimeout, lookupErr(t, err).Kind)
}

func TestNew_RejectsDisallowedBaseURL(t *testing.T) {
	_, err := New("https://evil.example.com", "", 15)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

func TestAdapter_EnabledAlways(t *testing.T) {
	a, err := New("https://intelapi.publicaml.org", "", 15)
	require.NoError(t, err)
	assert.Equal(t, "publicaml", a.Name())
	assert.True(t, a.Enabled())
}

var _ provider.Provider = (*Adapter)(nil)
