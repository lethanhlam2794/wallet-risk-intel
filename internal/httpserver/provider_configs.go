package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/providerconfig"
)

// ProviderConfigHandler serves the admin provider-config surface.
type ProviderConfigHandler struct {
	svc *providerconfig.Service
}

func NewProviderConfigHandler(svc *providerconfig.Service) *ProviderConfigHandler {
	return &ProviderConfigHandler{svc: svc}
}

func (h *ProviderConfigHandler) List(c *gin.Context) {
	views, err := h.svc.List(c.Request.Context())
	if err != nil {
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

func (h *ProviderConfigHandler) Get(c *gin.Context) {
	v, err := h.svc.Get(c.Request.Context(), c.Param("code"))
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": v})
}

// putBody whitelists the mutable fields; pointers distinguish absent from
// explicit empty overrides. Unknown JSON keys are rejected.
type putBody struct {
	APIKey        *string   `json:"api_key"`
	BaseURL       *string   `json:"base_url"`
	EnabledChains *[]string `json:"enabled_chains"`
	Status        *string   `json:"status"`
}

func (h *ProviderConfigHandler) Update(c *gin.Context) {
	var body putBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — allowed fields: api_key, base_url, enabled_chains, status")
		return
	}
	actor := providerconfig.Actor{Type: "api_client"}
	if cl := middleware.ClientFrom(c); cl != nil {
		actor.ID = cl.Name
	}
	res, err := h.svc.Update(c.Request.Context(), c.Param("code"), actor,
		providerconfig.Patch{
			APIKey:        body.APIKey,
			BaseURL:       body.BaseURL,
			EnabledChains: body.EnabledChains,
			Status:        body.Status,
		})
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res.View, "warnings": res.Warnings})
}

func (h *ProviderConfigHandler) Changes(c *gin.Context) {
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
	page, err := h.svc.ListChanges(c.Request.Context(), c.Param("code"),
		c.Query("cursor"), limit)
	if err != nil {
		h.err(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": page})
}

func (h *ProviderConfigHandler) err(c *gin.Context, err error) {
	var hostErr *providerconfig.HostNotAllowedError
	switch {
	case errors.Is(err, providerconfig.ErrUnknownProvider):
		middleware.RespondError(c, http.StatusNotFound, middleware.CodeNotFound,
			"unknown provider")
	case errors.Is(err, providerconfig.ErrInvalidStatus),
		errors.Is(err, providerconfig.ErrInvalidChain),
		errors.Is(err, providerconfig.ErrEncPrefix),
		errors.Is(err, providerconfig.ErrInvalidCursor):
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest, err.Error())
	case errors.As(err, &hostErr):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"code":          middleware.CodeBadRequest,
			"message":       hostErr.Error(),
			"allowed_hosts": hostErr.Allowed,
		}})
	default:
		middleware.InternalError(c)
	}
}
