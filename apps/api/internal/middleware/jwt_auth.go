package middleware

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// CtxUser is the gin context key for the authenticated system user.
const CtxUser = "system_user"

// UserIdentity is the authenticated system-user principal (admin console
// humans), carried by a Bearer JWT issued by POST /api/v1/auth/login.
type UserIdentity struct {
	ID       int64
	Username string
	Role     string
}

// jwtClaims mirrors the token payload issued at login.
type jwtClaims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueUserToken signs an HS256 JWT for a system user. The subject is the
// user ID; username/role ride as claims so middleware needs no DB lookup.
func IssueUserToken(secret []byte, userID int64, username, role string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	exp := now.Add(ttl)
	claims := jwtClaims{
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	return tok, exp, err
}

// IdentifyUser resolves `Authorization: Bearer <jwt>` to a system-user
// identity when the token is present, validly signed, and unexpired. Like
// IdentifyClient it never rejects by itself — RequireRole enforces.
func IdentifyUser(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if raw, ok := strings.CutPrefix(h, "Bearer "); ok && raw != "" {
			var claims jwtClaims
			_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, errors.New("unexpected signing method")
				}
				return secret, nil
			})
			if err == nil {
				id, _ := strconv.ParseInt(claims.Subject, 10, 64)
				c.Set(CtxUser, &UserIdentity{ID: id, Username: claims.Username, Role: claims.Role})
			}
		}
		c.Next()
	}
}

// UserFrom extracts the authenticated system user (nil when absent).
func UserFrom(c *gin.Context) *UserIdentity {
	if v, ok := c.Get(CtxUser); ok {
		if u, ok := v.(*UserIdentity); ok {
			return u
		}
	}
	return nil
}
