package enrichment

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

// Handler serves GET /api/v1/wallets/{network}/{address}/enrichment.
// Thin: param resolution + query parsing; the service owns the rules.
type Handler struct {
	svc      *Service
	resolver *network.Resolver
}

// NewHandler wires the service to Gin.
func NewHandler(svc *Service, resolver *network.Resolver) *Handler {
	return &Handler{svc: svc, resolver: resolver}
}

// Enrich resolves and normalizes the wallet identity, parses the refresh
// flag (strconv.ParseBool — strict) and the providers subset filter.
func (h *Handler) Enrich(c *gin.Context) {
	net, normalized, err := wallet.ResolveAndNormalize(h.resolver,
		c.Param("network"), c.Param("address"))
	if err != nil {
		switch {
		case errors.Is(err, wallet.ErrInvalidNetwork):
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeInvalidNetwork, "unsupported network")
		default:
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeInvalidAddress, "invalid address")
		}
		return
	}

	opts := Options{}
	if raw := c.Query("refresh"); raw != "" {
		refresh, err := strconv.ParseBool(raw)
		if err != nil {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "refresh must parse as a boolean")
			return
		}
		opts.Refresh = refresh
	}
	if raw := strings.TrimSpace(c.Query("providers")); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				opts.Providers = append(opts.Providers, name)
			}
		}
	}

	res, err := h.svc.Enrich(c.Request.Context(), net, c.Param("address"), normalized, opts)
	if err != nil {
		if errors.Is(err, ErrUnknownProvider) {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "unknown provider in providers filter")
			return
		}
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}
