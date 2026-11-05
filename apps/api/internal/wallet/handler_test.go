package wallet_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

func TestResolveAndNormalize(t *testing.T) {
	resolver := network.NewResolver([]network.Network{
		{ID: 1, Code: "ethereum", Namespace: "eip155", ChainReference: "1"},
		{ID: 2, Code: "bsc", Namespace: "eip155", ChainReference: "56"},
	})

	net, norm, err := wallet.ResolveAndNormalize(resolver, "ethereum", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96")
	assert.NoError(t, err)
	assert.Equal(t, int64(1), net.ID)
	assert.Equal(t, "0x098b716b8aaf21512996dc57eb0615e2383e2f96", norm)

	_, _, err = wallet.ResolveAndNormalize(resolver, "solana", "0x098B716B8Aaf21512996dC57EB0615e2383E2f96")
	assert.ErrorIs(t, err, wallet.ErrInvalidNetwork)

	_, _, err = wallet.ResolveAndNormalize(resolver, "ethereum", "garbage")
	assert.ErrorIs(t, err, wallet.ErrInvalidAddress)
}

func TestErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestID())
	r.GET("/e", func(c *gin.Context) {
		middleware.RespondError(c, http.StatusBadRequest, "INVALID_ADDRESS", "bad address")
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/e", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"INVALID_ADDRESS"`)
	assert.Contains(t, w.Body.String(), `"request_id"`)
}
