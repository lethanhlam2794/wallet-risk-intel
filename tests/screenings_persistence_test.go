package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
	"checkwallet/internal/testutil"
)

// screeningsPool shares check_wallet_test — every assertion below is
// scoped to rows this test inserts (per-test TruncateAll applies first).
func TestScreenings_ClaimResultAttachRoundTrip(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	key := seedClient(t, pool, "scr-roundtrip", "reader")
	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM api_clients WHERE key_hash = $1",
		middleware.HashAPIKey(key)).Scan(&clientID))

	w, err := q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
		NetworkID: 1, Address: "0x0000000000000000000000000000000000000aa1",
		NormalizedAddress: "0x0000000000000000000000000000000000000aa1",
		AddressType:       "evm",
	})
	require.NoError(t, err)

	// Claim-first: INSERT the request row, then attach the result inside
	// one transaction — the pair commits atomically.
	claim, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "k-1", RequestHash: "hash-1",
	})
	require.NoError(t, err)
	assert.False(t, claim.ScreeningResultID.Valid)

	resultID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	screenedAt := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	qtx := q.WithTx(tx)
	stored, err := qtx.InsertScreeningResult(ctx, db.InsertScreeningResultParams{
		ID: resultID, WalletID: w.ID, RiskScore: pgtype.Int2{Int16: 42, Valid: true},
		RiskLevel: "MEDIUM", ScreeningStatus: "COMPLETED",
		ScoringPolicyVersion: "v1.0.0",
		Findings:             []byte(`[{"kind":"label"}]`),
		ProviderStatus:       []byte(`{"goplus":{"status":"OK"}}`),
		ScreenedAt:           pgtype.Timestamptz{Time: screenedAt, Valid: true},
		Request:              []byte(`{"network":"ethereum"}`),
		Result:               []byte(`{"screening_id":"x","risk_score":42}`),
	})
	require.NoError(t, err)
	require.NoError(t, qtx.AttachScreeningResultToRequest(ctx,
		db.AttachScreeningResultToRequestParams{
			ID: claim.ID, ScreeningResultID: stored,
		}))
	require.NoError(t, tx.Commit(ctx))

	got, err := q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.Equal(t, resultID.Bytes, got.ScreeningResultID.Bytes,
		"claim must point at the persisted result")

	row, err := q.GetScreeningResultByID(ctx, resultID)
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", row.ScreeningStatus)
	assert.Equal(t, "v1.0.0", row.ScoringPolicyVersion)
	// result is type JSON — the stored body returns byte-identically.
	assert.JSONEq(t, `{"screening_id":"x","risk_score":42}`, string(row.Result))
	assert.True(t, row.RiskScore.Valid && row.RiskScore.Int16 == 42)

	// History query returns the row for the wallet.
	hist, err := q.ListScreeningResultsByWallet(ctx,
		db.ListScreeningResultsByWalletParams{WalletID: w.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, resultID.Bytes, hist[0].ID.Bytes)
}

func TestScreenings_ClaimConflict23505(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	key := seedClient(t, pool, "scr-conflict", "reader")
	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM api_clients WHERE key_hash = $1",
		middleware.HashAPIKey(key)).Scan(&clientID))

	_, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "dup-key", RequestHash: "h1",
	})
	require.NoError(t, err)

	// Same (client_id, idempotency_key) → 23505, observable for the
	// claim-conflict branch of the state machine.
	_, err = q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "dup-key", RequestHash: "h2",
	})
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr) && pgErr.Code == "23505",
		"duplicate claim must surface 23505, got %v", err)

	// A different client may reuse the same key independently.
	key2 := seedClient(t, pool, "scr-conflict-2", "reader")
	var clientID2 int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM api_clients WHERE key_hash = $1",
		middleware.HashAPIKey(key2)).Scan(&clientID2))
	_, err = q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID2, IdempotencyKey: "dup-key", RequestHash: "h2",
	})
	require.NoError(t, err)
}

func TestScreenings_StaleClaimTakeoverGuard(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	key := seedClient(t, pool, "scr-stale", "reader")
	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM api_clients WHERE key_hash = $1",
		middleware.HashAPIKey(key)).Scan(&clientID))

	claim, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "stale-key", RequestHash: "h",
	})
	require.NoError(t, err)

	// Fresh claim → guarded delete must not remove it.
	n, err := q.DeleteStaleScreeningRequest(ctx, claim.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	// Age the claim past the 5-minute takeover boundary.
	_, err = pool.Exec(ctx,
		"UPDATE screening_requests SET claimed_at = NOW() - INTERVAL '6 minutes' WHERE id = $1",
		claim.ID)
	require.NoError(t, err)
	n, err = q.DeleteStaleScreeningRequest(ctx, claim.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "stale open claim must be takeover-deletable")

	// A completed claim (result attached) is never takeover-deletable.
	claim2, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "done-key", RequestHash: "h",
	})
	require.NoError(t, err)
	w, err := q.GetOrCreateWallet(ctx, db.GetOrCreateWalletParams{
		NetworkID: 1, Address: "0x0000000000000000000000000000000000000bb2",
		NormalizedAddress: "0x0000000000000000000000000000000000000bb2",
		AddressType:       "evm",
	})
	require.NoError(t, err)
	rid := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err = q.InsertScreeningResult(ctx, db.InsertScreeningResultParams{
		ID: rid, WalletID: w.ID, RiskLevel: "UNKNOWN",
		ScreeningStatus: "INSUFFICIENT_DATA", ScoringPolicyVersion: "v1.0.0",
		Findings: []byte(`[]`), ProviderStatus: []byte(`{}`),
		ScreenedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		Request:    []byte(`{}`), Result: []byte(`{"status":"INSUFFICIENT_DATA"}`),
	})
	require.NoError(t, err)
	require.NoError(t, q.AttachScreeningResultToRequest(ctx,
		db.AttachScreeningResultToRequestParams{ID: claim2.ID, ScreeningResultID: rid}))
	_, err = pool.Exec(ctx,
		"UPDATE screening_requests SET claimed_at = NOW() - INTERVAL '6 minutes' WHERE id = $1",
		claim2.ID)
	require.NoError(t, err)
	n, err = q.DeleteStaleScreeningRequest(ctx, claim2.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "completed claims are never deleted")

	// Absent row → ErrNoRows for the get-by-key path.
	_, err = q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "never-claimed",
	})
	assert.True(t, errors.Is(err, pgx.ErrNoRows))
}

func TestScreenings_ClaimStaleFlag_Boundary(t *testing.T) {
	pool := testutil.RequireTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	key := seedClient(t, pool, "scr-flag", "reader")
	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id FROM api_clients WHERE key_hash = $1",
		middleware.HashAPIKey(key)).Scan(&clientID))

	claim, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "flag-key", RequestHash: "h",
	})
	require.NoError(t, err)

	// Fresh claim → not stale (DB-computed, no app-clock involvement).
	got, err := q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "flag-key"})
	require.NoError(t, err)
	assert.False(t, got.ClaimStale, "fresh claim is not stale")

	// Just inside the boundary → still not stale.
	_, err = pool.Exec(ctx,
		"UPDATE screening_requests SET claimed_at = NOW() - INTERVAL '4 minutes' WHERE id = $1",
		claim.ID)
	require.NoError(t, err)
	got, err = q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "flag-key"})
	require.NoError(t, err)
	assert.False(t, got.ClaimStale, "4-minute claim is inside the boundary")

	// Past the boundary → stale.
	_, err = pool.Exec(ctx,
		"UPDATE screening_requests SET claimed_at = NOW() - INTERVAL '6 minutes' WHERE id = $1",
		claim.ID)
	require.NoError(t, err)
	got, err = q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "flag-key"})
	require.NoError(t, err)
	assert.True(t, got.ClaimStale, "6-minute claim is takeover-eligible")
}
