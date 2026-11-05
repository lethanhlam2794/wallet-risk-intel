package screening

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
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
