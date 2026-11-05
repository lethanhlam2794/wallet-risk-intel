package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"checkwallet/internal/db"
)

// HeaderAPIKey is the API-key header.
const HeaderAPIKey = "X-API-Key"

// CtxClient is the gin context key for the authenticated client.
const CtxClient = "api_client"

// ClientIdentity is the authenticated API client principal.
type ClientIdentity struct {
	ID   int64
	Name string
	Role string
}

// ClientLookup abstracts the api_clients repository for testability.
type ClientLookup interface {
	GetAPIClientByKeyHash(ctx context.Context, keyHash string) (db.ApiClient, error)
	TouchAPIClient(ctx context.Context, id int64) error
}

// HashAPIKey returns the sha256 hex digest stored in api_clients.key_hash.
// Raw keys are never stored or logged.
func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// IdentifyClient resolves X-API-Key to a client identity when the key is
// present, valid, and active. It never rejects by itself — RequireRole
// enforces the posture; unidentified requests fall into the anonymous
// rate-limit bucket and are rejected there or at RequireRole.
func IdentifyClient(l ClientLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader(HeaderAPIKey)
		if key != "" {
			client, err := l.GetAPIClientByKeyHash(c.Request.Context(), HashAPIKey(key))
			if err == nil && client.IsActive {
				c.Set(CtxClient, &ClientIdentity{ID: client.ID, Name: client.Name, Role: client.Role})
				// Best-effort audit touch; failure must not break the request.
				_ = l.TouchAPIClient(c.Request.Context(), client.ID)
			} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				// Lookup errors degrade to anonymous, not to a crash.
				c.Set("auth_lookup_error", true)
			}
		}
		c.Next()
	}
}

// ClientFrom extracts the authenticated client (nil when unauthenticated).
func ClientFrom(c *gin.Context) *ClientIdentity {
	if v, ok := c.Get(CtxClient); ok {
		if id, ok := v.(*ClientIdentity); ok {
			return id
		}
	}
	return nil
}

// RequireRole rejects unauthenticated requests (401) and clients whose role
// is not in the allowed set (403).
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		role, ok := PrincipalRole(c)
		if !ok {
			RespondError(c, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		if !allowed[role] && !(role == "superadmin" && allowed["admin"]) {
			RespondError(c, http.StatusForbidden, CodeForbidden, "insufficient role")
			return
		}
		c.Next()
	}
}

// PrincipalRole returns the role of whichever principal is authenticated —
// API client (X-API-Key) or system user (Bearer JWT). superadmin satisfies
// any "admin" requirement via the check in RequireRole.
func PrincipalRole(c *gin.Context) (string, bool) {
	if cl := ClientFrom(c); cl != nil {
		return cl.Role, true
	}
	if u := UserFrom(c); u != nil {
		return u.Role, true
	}
	return "", false
}
