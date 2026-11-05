package apiclient

// Admin api-clients service tests — real test DB throughout (package-
// private check_wallet_apiclient_test per RequireTestPoolDSN contract):
// key issuance + auth round-trip, keyset pagination, whitelisted patch
// validation, admin-lockout guards (self + post-state last-admin),
// env_managed disclosure, serialized concurrent demotes.

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
	"checkwallet/internal/testutil"
)

const svcTestDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_apiclient_test?sslmode=disable"

func newSvc(t *testing.T, bootstrapHashes []string) (*Service, *pgxpool.Pool, *db.Queries) {
	t.Helper()
	pool := testutil.RequireTestPoolDSN(t, svcTestDSN)
	q := db.New(pool)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(pool, q, bootstrapHashes, log), pool, q
}

func actorFor(id int64, name string) Actor {
	return Actor{ID: id, Name: name, RequestID: "req-test-1"}
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

// upsertRow mirrors the env-bootstrap seeding path for fixture clients.
func upsertRow(t *testing.T, q *db.Queries, name, rawKey, role string) db.ApiClient {
	t.Helper()
	row, err := q.UpsertAPIClient(context.Background(), db.UpsertAPIClientParams{
		Name: name, KeyHash: middleware.HashAPIKey(rawKey), Role: role,
	})
	require.NoError(t, err)
	return row
}

func TestGenerateKey_Format(t *testing.T) {
	k1, err := GenerateKey()
	require.NoError(t, err)
	k2, err := GenerateKey()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(k1, "cw_"), "key must carry the cw_ prefix")
	assert.GreaterOrEqual(t, len(k1), 40, "32-byte entropy keeps keys long")
	assert.NotEqual(t, k1, k2, "keys are random")
	_, err = base64.RawURLEncoding.DecodeString(strings.TrimPrefix(k1, "cw_"))
	assert.NoError(t, err, "key body is base64url")
}

func TestService_CreateThenKeyAuthenticates(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()

	adminRow := upsertRow(t, q, "ops-admin", "seed-admin-key", RoleAdmin)

	created, err := svc.Create(ctx, "partner-x", RoleReader, actorFor(adminRow.ID, "ops-admin"))
	require.NoError(t, err)
	require.NotNil(t, created.View)
	assert.True(t, strings.HasPrefix(created.APIKey, "cw_"))
	assert.Equal(t, "partner-x", created.View.Name)
	assert.Equal(t, RoleReader, created.View.Role)
	assert.True(t, created.View.IsActive)

	// The generated key resolves through the real auth lookup path.
	row, err := q.GetAPIClientByKeyHash(ctx, middleware.HashAPIKey(created.APIKey))
	require.NoError(t, err)
	assert.True(t, row.IsActive)
	assert.Equal(t, created.View.ID, row.ID)

	// Arbitrary-format keys (bootstrap/legacy, non cw_) still authenticate.
	legacy, err := q.GetAPIClientByKeyHash(ctx, middleware.HashAPIKey("seed-admin-key"))
	require.NoError(t, err)
	assert.True(t, legacy.IsActive)
}

func TestService_CreateValidation(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	ctx := context.Background()
	actor := actorFor(0, "tester")

	_, err := svc.Create(ctx, "   ", RoleReader, actor)
	assert.ErrorIs(t, err, ErrInvalidName, "blank name rejected")

	_, err = svc.Create(ctx, strings.Repeat("e", 151), RoleReader, actor)
	assert.ErrorIs(t, err, ErrInvalidName, ">150 runes rejected")

	// 150 multibyte runes pass — VARCHAR(150) counts characters, not bytes.
	ok := strings.Repeat("é", 150)
	require.Equal(t, 150, len([]rune(ok)))
	_, err = svc.Create(ctx, ok, RoleReader, actor)
	assert.NoError(t, err)

	_, err = svc.Create(ctx, "x", "superuser", actor)
	assert.ErrorIs(t, err, ErrInvalidRole)
}

func TestService_CreateDuplicateNamesAllowed(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	ctx := context.Background()
	actor := actorFor(0, "tester")

	a, err := svc.Create(ctx, "same-name", RoleReader, actor)
	require.NoError(t, err)
	b, err := svc.Create(ctx, "same-name", RoleReader, actor)
	require.NoError(t, err)
	assert.NotEqual(t, a.View.ID, b.View.ID, "name is display metadata; id is identity")
}

func TestService_ListPagination(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	ctx := context.Background()
	actor := actorFor(0, "tester")

	var ids []int64
	for _, n := range []string{"c1", "c2", "c3"} {
		c, err := svc.Create(ctx, n, RoleReader, actor)
		require.NoError(t, err)
		ids = append(ids, c.View.ID)
	}

	_, err := svc.List(ctx, "garbage-cursor", 0)
	assert.ErrorIs(t, err, ErrInvalidCursor)

	p1, err := svc.List(ctx, "", 2)
	require.NoError(t, err)
	require.Len(t, p1.Items, 2)
	require.NotEmpty(t, p1.NextCursor)
	assert.Equal(t, ids[2], p1.Items[0].ID, "newest first")
	assert.Equal(t, ids[1], p1.Items[1].ID)

	p2, err := svc.List(ctx, p1.NextCursor, 2)
	require.NoError(t, err)
	require.Len(t, p2.Items, 1)
	assert.Equal(t, ids[0], p2.Items[0].ID)
	assert.Empty(t, p2.NextCursor, "no cursor when not truncated")

	// Limit clamps: >200 clamps to 200, <=0 defaults to 50 — never errors.
	big, err := svc.List(ctx, "", 500)
	require.NoError(t, err)
	assert.Len(t, big.Items, 3)
}

func TestService_UpdateRenameAndRole(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()

	adminRow := upsertRow(t, q, "ops-admin", "a-key", RoleAdmin)
	actor := actorFor(adminRow.ID, "ops-admin")

	target, err := svc.Create(ctx, "old-name", RoleReader, actor)
	require.NoError(t, err)

	res, err := svc.Update(ctx, target.View.ID,
		Patch{Name: strp("  new-name  "), Role: strp(RoleAdmin)}, actor)
	require.NoError(t, err)
	assert.Equal(t, "new-name", res.View.Name, "name stored trimmed")
	assert.Equal(t, RoleAdmin, res.View.Role)
	assert.True(t, res.View.IsActive)
	assert.Empty(t, res.Warnings)

	// Unknown id → ErrNotFound.
	_, err = svc.Update(ctx, 424242, Patch{Name: strp("x")}, actor)
	assert.ErrorIs(t, err, ErrNotFound)

	// Validation: bad role / bad name.
	_, err = svc.Update(ctx, target.View.ID, Patch{Role: strp("root")}, actor)
	assert.ErrorIs(t, err, ErrInvalidRole)
	_, err = svc.Update(ctx, target.View.ID, Patch{Name: strp("   ")}, actor)
	assert.ErrorIs(t, err, ErrInvalidName)
}

func TestService_UpdateEmptyPatchNoOp(t *testing.T) {
	svc, _, _ := newSvc(t, nil)
	ctx := context.Background()
	actor := actorFor(0, "tester")

	c, err := svc.Create(ctx, "noop-target", RoleReader, actor)
	require.NoError(t, err)
	res, err := svc.Update(ctx, c.View.ID, Patch{}, actor)
	require.NoError(t, err)
	assert.Equal(t, "noop-target", res.View.Name, "empty patch returns current view")
	assert.Empty(t, res.Warnings)
}

func TestService_UpdatePreservesLastSeenAt(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()
	actor := actorFor(0, "tester")

	c, err := svc.Create(ctx, "seen", RoleReader, actor)
	require.NoError(t, err)
	require.NoError(t, q.TouchAPIClient(ctx, c.View.ID))

	res, err := svc.Update(ctx, c.View.ID, Patch{Name: strp("seen-2")}, actor)
	require.NoError(t, err)
	require.NotNil(t, res.View.LastSeenAt, "column-scoped writes must not reset last_seen_at")
}

func TestService_SelfDemoteAndDeactivateRejected(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()

	adminRow := upsertRow(t, q, "self-admin", "self-key", RoleAdmin)
	upsertRow(t, q, "other-admin", "other-key", RoleAdmin)
	actor := actorFor(adminRow.ID, "self-admin")

	_, err := svc.Update(ctx, adminRow.ID, Patch{Role: strp(RoleReader)}, actor)
	assert.ErrorIs(t, err, ErrSelfLockout, "self-demote is always rejected")

	_, err = svc.Update(ctx, adminRow.ID, Patch{IsActive: boolp(false)}, actor)
	assert.ErrorIs(t, err, ErrSelfLockout, "self-deactivate is always rejected")

	// Self no-op role ({role:"admin"} on an admin) and self-rename pass.
	_, err = svc.Update(ctx, adminRow.ID, Patch{Role: strp(RoleAdmin)}, actor)
	require.NoError(t, err)
	res, err := svc.Update(ctx, adminRow.ID, Patch{Name: strp("renamed-self")}, actor)
	require.NoError(t, err)
	assert.Equal(t, "renamed-self", res.View.Name)
}

func TestService_LastAdminGuardAndInactiveEdge(t *testing.T) {
	svc, _, q := newSvc(t, nil)
	ctx := context.Background()

	// Sole active admin cannot be removed even when the actor is another
	// (non-admin) row — guard (b) is the post-state count check.
	sole := upsertRow(t, q, "sole-admin", "sole-key", RoleAdmin)
	readerRow := upsertRow(t, q, "a-reader", "r-key", RoleReader)

	_, err := svc.Update(ctx, sole.ID, Patch{Role: strp(RoleReader)},
		actorFor(readerRow.ID, "a-reader"))
	assert.ErrorIs(t, err, ErrAdminLockout, "removing the last active admin is rejected")

	// Second admin appears → demoting the first now leaves one behind.
	second := upsertRow(t, q, "second-admin", "s2-key", RoleAdmin)
	res, err := svc.Update(ctx, sole.ID, Patch{Role: strp(RoleReader)},
		actorFor(second.ID, "second-admin"))
	require.NoError(t, err)
	assert.Equal(t, RoleReader, res.View.Role)

	// Demoting/deactivating an already-inactive admin succeeds (no false 409).
	inactive := upsertRow(t, q, "ghost-admin", "g-key", RoleAdmin)
	_, err = svc.Update(ctx, inactive.ID, Patch{IsActive: boolp(false)},
		actorFor(second.ID, "second-admin"))
	require.NoError(t, err, "deactivating an active admin while another remains is fine")

	_, err = svc.Update(ctx, inactive.ID, Patch{IsActive: boolp(false)},
		actorFor(second.ID, "second-admin"))
	assert.NoError(t, err, "deactivate on already-inactive is a no-op, not lockout")

	_, err = svc.Update(ctx, inactive.ID, Patch{Role: strp(RoleReader)},
		actorFor(second.ID, "second-admin"))
	assert.NoError(t, err, "demoting an inactive admin is a no-op for the guard")
}

func TestService_ConcurrentDemoteKeepsAnAdmin(t *testing.T) {
	svc, pool, q := newSvc(t, nil)
	ctx := context.Background()

	a := upsertRow(t, q, "admin-a", "a1", RoleAdmin)
	b := upsertRow(t, q, "admin-b", "b1", RoleAdmin)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i, pair := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func(i int, actorID, targetID int64) {
			defer wg.Done()
			<-start
			_, err := svc.Update(ctx, targetID, Patch{Role: strp(RoleReader)},
				actorFor(actorID, "racer"))
			errs[i] = err
		}(i, pair[0], pair[1])
	}
	close(start)
	wg.Wait()

	var lockouts, oks int
	for _, err := range errs {
		if err == nil {
			oks++
		} else if assert.ErrorIs(t, err, ErrAdminLockout) {
			lockouts++
		}
	}
	assert.Equal(t, 1, lockouts, "serialized guard rejects exactly one racer")
	assert.Equal(t, 1, oks)

	var admins int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM api_clients WHERE role='admin' AND is_active").Scan(&admins))
	assert.GreaterOrEqual(t, admins, 1, "at least one active admin remains")
}

func TestService_EnvManagedWarning(t *testing.T) {
	const envKey = "env-bootstrap-key"
	svc, _, q := newSvc(t, []string{middleware.HashAPIKey(envKey)})
	ctx := context.Background()

	envRow := upsertRow(t, q, "bootstrap-admin", envKey, RoleAdmin)
	actor := actorFor(envRow.ID, "bootstrap-admin")

	res, err := svc.Update(ctx, envRow.ID, Patch{Name: strp("renamed-env")}, actor)
	require.NoError(t, err)
	require.NotEmpty(t, res.Warnings, "env-managed row patch must disclose resurrection")
	assert.Equal(t, "env_managed", res.Warnings[0].Code)
	assert.Contains(t, res.Warnings[0].Message, "API_BOOTSTRAP_")

	// Any patch on the env row warns — including empty/no-op patches.
	res, err = svc.Update(ctx, envRow.ID, Patch{}, actor)
	require.NoError(t, err)
	require.NotEmpty(t, res.Warnings)
	assert.Equal(t, "env_managed", res.Warnings[0].Code)

	// Non-env row: no warning.
	other, err := svc.Create(ctx, "plain", RoleReader, actor)
	require.NoError(t, err)
	res, err = svc.Update(ctx, other.View.ID, Patch{Name: strp("plain-2")}, actor)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
}
