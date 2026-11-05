package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
)

type stubClientLookup struct {
	byHash map[string]db.ApiClient
	err    error
}

func (s stubClientLookup) GetAPIClientByKeyHash(_ context.Context, hash string) (db.ApiClient, error) {
	if s.err != nil {
		return db.ApiClient{}, s.err
	}
	c, ok := s.byHash[hash]
	if !ok {
		return db.ApiClient{}, pgx.ErrNoRows
	}
	return c, nil
}

func (s stubClientLookup) TouchAPIClient(_ context.Context, _ int64) error { return nil }

func authedRouter(lookup ClientLookup, role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID(), IdentifyClient(lookup))
	protected := r.Group("/api/v1", RequireRole("reader", "admin"))
	protected.GET("/thing", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	admin := r.Group("/api/v1/admin", RequireRole("admin"))
	admin.GET("/thing", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func TestAuth_NoKey_Unauthorized(t *testing.T) {
	r := authedRouter(stubClientLookup{}, "reader")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "UNAUTHORIZED")
	assert.Contains(t, w.Body.String(), "request_id")
}

func TestAuth_BadKey_Unauthorized(t *testing.T) {
	r := authedRouter(stubClientLookup{}, "reader")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_ReaderOK_AdminForbidden(t *testing.T) {
	readerKey := "reader-secret"
	lookup := stubClientLookup{byHash: map[string]db.ApiClient{
		HashAPIKey(readerKey): {ID: 7, Name: "reader1", Role: "reader", IsActive: true},
	}}
	r := authedRouter(lookup, "reader")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("X-API-Key", readerKey)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/thing", nil)
	req.Header.Set("X-API-Key", readerKey)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "FORBIDDEN")
}

func TestAuth_AdminOK(t *testing.T) {
	adminKey := "admin-secret"
	lookup := stubClientLookup{byHash: map[string]db.ApiClient{
		HashAPIKey(adminKey): {ID: 1, Name: "admin1", Role: "admin", IsActive: true},
	}}
	r := authedRouter(lookup, "admin")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/thing", nil)
	req.Header.Set("X-API-Key", adminKey)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuth_InactiveClient_Unauthorized(t *testing.T) {
	key := "disabled"
	lookup := stubClientLookup{byHash: map[string]db.ApiClient{
		HashAPIKey(key): {ID: 3, Name: "off", Role: "admin", IsActive: false},
	}}
	r := authedRouter(lookup, "admin")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/thing", nil)
	req.Header.Set("X-API-Key", key)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_LookupError_UnauthorizedNotPanic(t *testing.T) {
	lookup := stubClientLookup{err: errors.New("db down")}
	r := authedRouter(lookup, "reader")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("X-API-Key", "any")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	a := HashAPIKey("k1")
	b := HashAPIKey("k1")
	c := HashAPIKey("k2")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Len(t, a, 64) // sha256 hex
}
