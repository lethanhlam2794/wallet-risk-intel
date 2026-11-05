package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/apiclient"
	"checkwallet/internal/middleware"
)

// ClientHandler serves the admin api-clients surface: list/create/patch.
// Views never carry key_hash; the plaintext key appears exactly once in
// the POST 201 body under Cache-Control: no-store.
type ClientHandler struct {
	svc *apiclient.Service
}

func NewClientHandler(svc *apiclient.Service) *ClientHandler {
	return &ClientHandler{svc: svc}
}

func (h *ClientHandler) List(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}
	page, err := h.svc.List(c.Request.Context(), c.Query("cursor"), limit)
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": page})
}

// createBody whitelists POST fields; unknown JSON keys are rejected so a
// key_hash/api_key injection field can never reach the service.
type createBody struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

func (h *ClientHandler) Create(c *gin.Context) {
	var body createBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — allowed fields: name, role")
		return
	}
	res, err := h.svc.Create(c.Request.Context(), body.Name, body.Role, clientActor(c))
	if err != nil {
		h.err(c, err)
		return
	}
	// Plaintext disclosed exactly once; shared caches must not retain it.
	c.Header("Cache-Control", "no-store")
	v := res.View
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{
		"id":           v.ID,
		"name":         v.Name,
		"role":         v.Role,
		"is_active":    v.IsActive,
		"created_at":   v.CreatedAt,
		"last_seen_at": v.LastSeenAt,
		"api_key":      res.APIKey,
		"note":         "api_key is shown exactly once — store it securely; it cannot be retrieved again",
	}})
}

// patchBody whitelists PATCH fields — pointers distinguish absent from
// explicit values; PATCH {} is a no-op returning the current view.
type patchBody struct {
	Name     *string `json:"name"`
	Role     *string `json:"role"`
	IsActive *bool   `json:"is_active"`
}

func (h *ClientHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "invalid client id")
		return
	}
	var body patchBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — allowed fields: name, role, is_active")
		return
	}
	res, err := h.svc.Update(c.Request.Context(), id, apiclient.Patch{
		Name: body.Name, Role: body.Role, IsActive: body.IsActive,
	}, clientActor(c))
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res.View, "warnings": res.Warnings})
}

func (h *ClientHandler) err(c *gin.Context, err error) {
	switch {
	case errors.Is(err, apiclient.ErrNotFound):
		middleware.RespondError(c, http.StatusNotFound,
			middleware.CodeNotFound, "api client not found")
	case errors.Is(err, apiclient.ErrAdminLockout),
		errors.Is(err, apiclient.ErrSelfLockout):
		middleware.RespondError(c, http.StatusConflict,
			middleware.CodeAdminLockout, err.Error())
	case errors.Is(err, apiclient.ErrInvalidName),
		errors.Is(err, apiclient.ErrInvalidRole),
		errors.Is(err, apiclient.ErrInvalidCursor):
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, err.Error())
	default:
		middleware.InternalError(c)
	}
}

// clientActor derives the acting principal from the authenticated
// context and carries the request id into the domain log — never
// re-derived inside the service.
func clientActor(c *gin.Context) apiclient.Actor {
	a := apiclient.Actor{RequestID: middleware.GetRequestID(c)}
	if cl := middleware.ClientFrom(c); cl != nil {
		a.ID = cl.ID
		a.Name = cl.Name
	}
	return a
}
