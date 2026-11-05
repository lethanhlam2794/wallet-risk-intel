package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/systemuser"
)

// AuthHandler serves POST /api/v1/auth/login — the only public (pre-auth)
// route that issues Bearer JWTs for system users.
type AuthHandler struct {
	svc    *systemuser.Service
	secret []byte
	ttl    time.Duration
}

func NewAuthHandler(svc *systemuser.Service, secret []byte, ttl time.Duration) *AuthHandler {
	return &AuthHandler{svc: svc, secret: secret, ttl: ttl}
}

// loginBody whitelists login fields; unknown JSON keys are rejected.
type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var body loginBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — required fields: username, password")
		return
	}
	u, err := h.svc.Authenticate(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, systemuser.ErrInvalidCredentials) {
			middleware.RespondError(c, http.StatusUnauthorized,
				middleware.CodeInvalidCredentials, "invalid username or password")
			return
		}
		middleware.RespondError(c, http.StatusInternalServerError,
			middleware.CodeInternal, "authentication failed")
		return
	}
	tok, exp, err := middleware.IssueUserToken(h.secret, u.ID, u.Username, u.Role, h.ttl, time.Now())
	if err != nil {
		middleware.RespondError(c, http.StatusInternalServerError,
			middleware.CodeInternal, "token issuance failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"token":      tok,
		"token_type": "Bearer",
		"expires_at": exp.UTC(),
		"user": gin.H{
			"username": u.Username,
			"role":     u.Role,
		},
	}})
}
