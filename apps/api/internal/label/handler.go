package label

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

// Handler serves the labels endpoints.
type Handler struct {
	svc      *Service
	resolver *network.Resolver
}

func NewHandler(svc *Service, resolver *network.Resolver) *Handler {
	return &Handler{svc: svc, resolver: resolver}
}

var validVerificationStatuses = map[string]bool{
	"reported": true, "unverified": true, "verified": true, "disputed": true,
}

// WalletLabels: GET /api/v1/wallets/:network/:address/labels
func (h *Handler) WalletLabels(c *gin.Context) {
	net, normalized, err := wallet.ResolveAndNormalize(h.resolver,
		c.Param("network"), c.Param("address"))
	if err != nil {
		switch {
		case errors.Is(err, wallet.ErrInvalidNetwork):
			middleware.RespondError(c, http.StatusBadRequest, middleware.CodeInvalidNetwork, "unsupported network")
		default:
			middleware.RespondError(c, http.StatusBadRequest, middleware.CodeInvalidAddress, "invalid address")
		}
		return
	}

	f := Filters{ActiveOnly: true}
	q := c.Request.URL.Query()
	f.Category = q.Get("category")
	f.Source = q.Get("source")
	f.SourceField = q.Get("source_field")
	f.VerificationStatus = q.Get("verification_status")
	if f.VerificationStatus != "" && !validVerificationStatuses[f.VerificationStatus] {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid verification_status")
		return
	}
	if v := q.Get("active_only"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
				"invalid active_only")
			return
		}
		f.ActiveOnly = b
	}
	f.Cursor = q.Get("cursor")
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
				"invalid limit")
			return
		}
		f.Limit = n
	}

	res, dataStatus, lastSynced, err := h.svc.ListLabels(c.Request.Context(),
		net.ID, net.Code, normalized, f)
	if err != nil {
		if errors.Is(err, ErrInvalidCursor) {
			middleware.RespondError(c, http.StatusBadRequest, middleware.CodeInvalidCursor,
				"invalid cursor")
			return
		}
		middleware.InternalError(c)
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"network":            net.Code,
		"address":            c.Param("address"),
		"normalized_address": normalized,
		"labels":             res.Items,
		"labels_count":       res.Count,
		"next_cursor":        res.NextCursor,
		"data_status":        dataStatus,
		"last_synced_at":     lastSynced,
	}})
}
