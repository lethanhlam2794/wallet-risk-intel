package sanctions

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

// jsonOrNull renders a JSONB column as raw JSON or null.
func jsonOrNull(b []byte) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return json.RawMessage(b)
}

// Handler serves the /sanctions/* reader endpoints.
type Handler struct {
	svc      *Service
	resolver *network.Resolver
}

func NewHandler(svc *Service, resolver *network.Resolver) *Handler {
	return &Handler{svc: svc, resolver: resolver}
}

// WalletLookup: GET /api/v1/sanctions/wallets/:network/:address
func (h *Handler) WalletLookup(c *gin.Context) {
	net, normalized, err := wallet.ResolveAndNormalize(
		h.resolver, c.Param("network"), c.Param("address"))
	if err != nil {
		if errors.Is(err, wallet.ErrInvalidNetwork) {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeInvalidNetwork, "unsupported network")
		} else {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeInvalidAddress, "invalid address")
		}
		return
	}
	lookup, dataStatus, lastSynced, err := h.svc.LookupWallet(
		c.Request.Context(), net.ID, net.Code, normalized)
	if err != nil {
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"network":                     net.Code,
		"address":                     c.Param("address"),
		"normalized_address":          normalized,
		"matched":                     lookup.Matched,
		"match_type":                  lookup.MatchType,
		"match_scope":                 lookup.MatchScope,
		"sanctions":                   lookup.Sanctions,
		"cross_chain_observations":    lookup.CrossChainObservations,
		"unresolved_network_listings": lookup.UnresolvedNetworkListings,
		"data_status":                 dataStatus,
		"last_synced_at":              lastSynced,
	}})
}

// Entity: GET /api/v1/sanctions/entities/:id — {id} is the internal
// sanctions_entities.id PK (external_entity_id is returned in the body).
func (h *Handler) Entity(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		middleware.RespondError(c, http.StatusBadRequest,
			"INVALID_ENTITY_ID", "entity id must be a positive integer")
		return
	}
	detail, err := h.svc.Entity(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			middleware.RespondError(c, http.StatusNotFound,
				middleware.CodeNotFound, "sanctions entity not found")
			return
		}
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"sanctions_entity_id":        detail.EntityID,
		"external_entity_id":         detail.ExternalEntityID,
		"entity_name":                detail.EntityName,
		"entity_type":                detail.EntityType,
		"source_url":                 detail.SourceURL,
		"revoked":                    detail.Revoked,
		"programs":                   detail.Programs,
		"reason":                     detail.Reason,
		"digital_currency_addresses": detail.DigitalCurrencyAddresses,
		"official_references":        detail.OfficialReferences,
		"details":                    jsonOrNull(detail.Details),
		"source":                     detail.Source,
		"data_status":                detail.DataStatus,
		"last_synced_at":             detail.LastSyncedAt,
	}})
}

// Program: GET /api/v1/sanctions/programs/:code
func (h *Handler) Program(c *gin.Context) {
	code := c.Param("code")
	if code == "" {
		middleware.RespondError(c, http.StatusBadRequest,
			"INVALID_PROGRAM_CODE", "program code required")
		return
	}
	detail, err := h.svc.Program(c.Request.Context(), code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			middleware.RespondError(c, http.StatusNotFound,
				middleware.CodeNotFound, "program not found")
			return
		}
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}
