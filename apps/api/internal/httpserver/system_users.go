package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/middleware"
	"checkwallet/internal/systemuser"
)

// SystemUserHandler serves the superadmin-only system-users surface.
// Views never carry password_hash.
type SystemUserHandler struct {
	svc *systemuser.Service
}

func NewSystemUserHandler(svc *systemuser.Service) *SystemUserHandler {
	return &SystemUserHandler{svc: svc}
}

func (h *SystemUserHandler) List(c *gin.Context) {
	rows, err := h.svc.List(c.Request.Context())
	if err != nil {
		middleware.RespondError(c, http.StatusInternalServerError,
			middleware.CodeInternal, "list failed")
		return
	}
	views := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		views = append(views, userView(r.ID, r.Username, r.Role, r.IsActive, r.CreatedAt, r.LastLoginAt))
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

// createUserBody whitelists POST fields — password hashes can never be
// injected because only plaintext `password` is accepted.
type createUserBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (h *SystemUserHandler) Create(c *gin.Context) {
	var body createUserBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — allowed fields: username, password, role")
		return
	}
	u, err := h.svc.Create(c.Request.Context(), body.Username, body.Password, body.Role)
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": userView(
		u.ID, u.Username, u.Role, u.IsActive, u.CreatedAt, u.LastLoginAt)})
}

func (h *SystemUserHandler) err(c *gin.Context, err error) {
	switch {
	case errors.Is(err, systemuser.ErrInvalidUsername),
		errors.Is(err, systemuser.ErrInvalidPassword),
		errors.Is(err, systemuser.ErrInvalidRole):
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, err.Error())
	case errors.Is(err, systemuser.ErrUsernameTaken):
		middleware.RespondError(c, http.StatusConflict,
			middleware.CodeBadRequest, "username already exists")
	default:
		middleware.RespondError(c, http.StatusInternalServerError,
			middleware.CodeInternal, "create failed")
	}
}

// userView emits the wire shape — UTC timestamps, null when unset.
func userView(id int64, username, role string, active bool, createdAt, lastLoginAt pgtype.Timestamptz) gin.H {
	var lastLogin *time.Time
	if lastLoginAt.Valid {
		t := lastLoginAt.Time.UTC()
		lastLogin = &t
	}
	return gin.H{
		"id":            id,
		"username":      username,
		"role":          role,
		"is_active":     active,
		"created_at":    createdAt.Time.UTC(),
		"last_login_at": lastLogin,
	}
}
