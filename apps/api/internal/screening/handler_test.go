package screening

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/testutil"
)

func init() { gin.SetMode(gin.TestMode) }

// R3-M1: a poller must re-check request_hash on every tick — a claim
// that was released and re-claimed under a DIFFERENT payload must not
// be replayed for this request.
func TestHandler_PollRejectsReclaimedDifferentHash(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	ctx := context.Background()
	q := db.New(pool)

	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO api_clients (name, key_hash, role)
		 VALUES ('poll-hash', 'h', 'reader') RETURNING id`).Scan(&clientID))
	// The claim on key "k" belongs to payload "other-hash".
	_, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "k", RequestHash: "other-hash"})
	require.NoError(t, err)

	h := NewHandler(nil, nil, q, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/screenings", nil)

	// Poller expects payload "my-hash" — first tick must 409, not
	// replay the foreign claim.
	h.pollOrConflict(c, clientID, "k", "my-hash")

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "IDEMPOTENCY_CONFLICT")
}

// Same-hash open claims stay in the poll loop (bounded by deadline) —
// here the claim vanishes mid-poll → treated as in-flight failure.
func TestHandler_PollVanishingClaim_Conflict(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	ctx := context.Background()
	q := db.New(pool)

	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO api_clients (name, key_hash, role)
		 VALUES ('poll-vanish', 'h', 'reader') RETURNING id`).Scan(&clientID))
	claim, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "kv", RequestHash: "same-hash"})
	require.NoError(t, err)
	// Delete the claim before the poll starts — first read hits
	// ErrNoRows → break → 409 in-flight.
	require.NoError(t, q.DeleteScreeningRequest(ctx, claim.ID))

	h := NewHandler(nil, nil, q, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/screenings", nil)

	h.pollOrConflict(c, clientID, "kv", "same-hash")

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "IDEMPOTENCY_IN_FLIGHT")
}

// handlerResolver is a one-network resolver for Create-level tests.
func handlerResolver() *network.Resolver {
	return network.NewResolver([]network.Network{{
		ID: 1, Namespace: "eip155", ChainReference: "1",
		Code: "ethereum", Name: "Ethereum"}})
}

func postCreate(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/screenings",
		strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(middleware.CtxClient, &middleware.ClientIdentity{ID: 1, Role: "reader"})
	h.Create(c)
	return w
}

// Unknown options.policy_version → 400 before requestHash/claim work —
// a nil Queries would panic on the claim path, so reaching 400 proves
// validation ran first.
func TestHandler_UnknownPolicyVersion_400(t *testing.T) {
	h := NewHandler(nil, handlerResolver(), nil, nil)
	w := postCreate(t, h, `{"network":"ethereum","address":"`+testAddr+
		`","options":{"policy_version":"v9.9.9"}}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "policy_version")
}

// A registered v1.1.0 passes validation and reaches the pipeline.
func TestHandler_PolicyVersionV1_1_Accepted(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	svc := newService(t, pool,
		&fakeSanctions{lookup: nil, status: DataAvailable},
		&fakeLabels{status: DataAvailable}, nil)
	h := NewHandler(svc, handlerResolver(), q, nil)

	w := postCreate(t, h, `{"network":"ethereum","address":"`+testAddr+
		`","options":{"policy_version":"v1.1.0"}}`)
	assert.Equal(t, http.StatusCreated, w.Code, "%s", w.Body.String())
}

// Idempotency stability: policy_version participates in the canonical
// hash (A/B separates claims) while absent stays byte-identical for
// clients that never send it (A5).
func TestRequestHash_PolicyVersionSeparatesClaims(t *testing.T) {
	base := CanonicalRequest{Network: "ethereum", Address: testAddr,
		Options: DefaultOptions()}
	v11 := base
	v11.Options.PolicyVersion = "v1.1.0"
	v10 := base
	v10.Options.PolicyVersion = "v1.0.0"

	assert.NotEqual(t, requestHash(base), requestHash(v11),
		"explicit v1.1.0 must hash differently from absent")
	assert.NotEqual(t, requestHash(v10), requestHash(v11),
		"explicit v1.0.0 and v1.1.0 are different A/B requests")
}
