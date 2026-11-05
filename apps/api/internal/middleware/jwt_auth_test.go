package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("test-secret-32-bytes-long-enough!")

func issue(t *testing.T, id int64, username, role string, ttl time.Duration) string {
	t.Helper()
	tok, _, err := IssueUserToken(testSecret, id, username, role, ttl, time.Now())
	require.NoError(t, err)
	return tok
}

func jwtRouter(role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(IdentifyUser(testSecret))
	protected := r.Group("/api/v1", RequireRole(role))
	protected.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r
}

func doReq(r *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestIdentifyUser_ValidToken(t *testing.T) {
	r := jwtRouter("admin")
	w := doReq(r, "Bearer "+issue(t, 7, "superadmin", "superadmin", time.Hour))
	assert.Equal(t, http.StatusNoContent, w.Code) // superadmin satisfies admin
}

func TestIdentifyUser_AdminToken(t *testing.T) {
	r := jwtRouter("admin")
	w := doReq(r, "Bearer "+issue(t, 8, "ops.lead", "admin", time.Hour))
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestIdentifyUser_AdminBlockedFromSuperadminRoute(t *testing.T) {
	r := jwtRouter("superadmin")
	w := doReq(r, "Bearer "+issue(t, 8, "ops.lead", "admin", time.Hour))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestIdentifyUser_MissingHeader(t *testing.T) {
	r := jwtRouter("admin")
	assert.Equal(t, http.StatusUnauthorized, doReq(r, "").Code)
}

func TestIdentifyUser_BadSignature(t *testing.T) {
	tok := issue(t, 7, "x", "admin", time.Hour)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(IdentifyUser([]byte("other-secret")), RequireRole("admin"))
	r.GET("/api/v1/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	assert.Equal(t, http.StatusUnauthorized, doReq(r, "Bearer "+tok).Code)
}

func TestIdentifyUser_Expired(t *testing.T) {
	r := jwtRouter("admin")
	w := doReq(r, "Bearer "+issue(t, 7, "x", "admin", -time.Minute))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestIdentifyUser_WrongScheme(t *testing.T) {
	r := jwtRouter("admin")
	w := doReq(r, "Basic "+issue(t, 7, "x", "admin", time.Hour))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestIssueUserToken_Payload(t *testing.T) {
	now := time.Now()
	tok, exp, err := IssueUserToken(testSecret, 42, "superadmin", "superadmin", 8*time.Hour, now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(8*time.Hour).Unix(), exp.Unix())
	assert.NotEmpty(t, tok)
}
