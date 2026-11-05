package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/probetest"
	"checkwallet/internal/providerconfig"
	"checkwallet/internal/wallet"
)

// probeTimeout bounds the whole upstream probe — the admin UI must never
// hang on a dead provider.
const probeTimeout = 15 * time.Second

// ProbeHandler serves POST /admin/provider-configs/{code}/test — a live
// upstream check against the stored config.
type ProbeHandler struct {
	svc      *probetest.Service
	resolver *network.Resolver
}

func NewProbeHandler(svc *probetest.Service, resolver *network.Resolver) *ProbeHandler {
	return &ProbeHandler{svc: svc, resolver: resolver}
}

// probeBody whitelists POST fields; unknown JSON keys are rejected.
type probeBody struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

func (h *ProbeHandler) Test(c *gin.Context) {
	var body probeBody
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		middleware.RespondError(c, http.StatusBadRequest, middleware.CodeBadRequest,
			"invalid JSON body — required fields: network, address")
		return
	}
	net, normalized, err := wallet.ResolveAndNormalize(h.resolver, body.Network, body.Address)
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
	defer cancel()
	res, err := h.svc.Probe(ctx, c.Param("code"), net, normalized)
	if err != nil {
		if pe, ok := err.(*providerconfig.Error); ok {
			middleware.RespondError(c, http.StatusNotFound,
				middleware.CodeNotFound, pe.Error())
			return
		}
		middleware.RespondError(c, http.StatusInternalServerError,
			middleware.CodeInternal, "probe failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": res})
}
