package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/tracing"
	"checkwallet/internal/transaction"
	"checkwallet/internal/wallet"
)

// TxHandler serves GET /wallets/{network}/{address}/transactions and
// /exposures. Thin: param resolution + query parsing only.
type TxHandler struct {
	tx       *transaction.Service
	tracing  *tracing.Service
	resolver *network.Resolver
}

// NewTxHandler wires both read paths.
func NewTxHandler(tx *transaction.Service, tr *tracing.Service,
	resolver *network.Resolver) *TxHandler {
	return &TxHandler{tx: tx, tracing: tr, resolver: resolver}
}

func (h *TxHandler) resolve(c *gin.Context) (network.Network, string, bool) {
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
		return network.Network{}, "", false
	}
	return net, normalized, true
}

func parseRefresh(c *gin.Context) (bool, bool) {
	raw := c.Query("refresh")
	if raw == "" {
		return false, true
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "refresh must parse as a boolean")
		return false, false
	}
	return v, true
}

// Transactions handles GET /wallets/:network/:address/transactions.
func (h *TxHandler) Transactions(c *gin.Context) {
	net, normalized, ok := h.resolve(c)
	if !ok {
		return
	}
	f := transaction.TxFilters{Cursor: c.Query("cursor")}
	if raw := c.Query("direction"); raw != "" {
		switch raw {
		case "in", "out", "self":
			f.Direction = raw
		default:
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "direction must be in|out|self")
			return
		}
	}
	f.Asset = strings.TrimSpace(c.Query("asset"))
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	if raw := c.Query("from_timestamp"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "from_timestamp must be RFC3339")
			return
		}
		f.FromTs = &t
	}
	if raw := c.Query("to_timestamp"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "to_timestamp must be RFC3339")
			return
		}
		f.ToTs = &t
	}
	if refresh, ok := parseRefresh(c); !ok {
		return
	} else {
		f.Refresh = refresh
	}

	res, err := h.tx.ListTransactions(c.Request.Context(), net, normalized, f)
	if err != nil {
		if errors.Is(err, transaction.ErrInvalidCursor) {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeInvalidCursor, "invalid cursor")
			return
		}
		if errors.Is(err, transaction.ErrInvalidInput) {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "invalid filter parameter")
			return
		}
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}

// Exposures handles GET /wallets/:network/:address/exposures.
func (h *TxHandler) Exposures(c *gin.Context) {
	net, normalized, ok := h.resolve(c)
	if !ok {
		return
	}
	req := tracing.Request{MaxHops: 2, Limit: 50}
	if raw := c.Query("max_hops"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "max_hops must be between 1 and 5")
			return
		}
		req.MaxHops = n
	}
	if raw := c.Query("direction"); raw != "" {
		req.Direction = raw
	}
	req.Category = strings.TrimSpace(c.Query("category"))
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest, "limit must be a positive integer")
			return
		}
		req.Limit = n
	}
	if refresh, ok := parseRefresh(c); !ok {
		return
	} else {
		req.Refresh = refresh
	}
	if err := tracing.ValidateRequest(&req); err != nil {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "max_hops must be between 1 and 5 and limit ≤ 100")
		return
	}

	res, err := h.tracing.Exposures(c.Request.Context(), net, normalized, req)
	if err != nil {
		middleware.InternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}
