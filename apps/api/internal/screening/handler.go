package screening

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
	"checkwallet/internal/network"
	"checkwallet/internal/wallet"
)

const (
	// Idempotency-Key protocol (plan §3.3).
	headerIdempotencyKey   = "Idempotency-Key"
	headerIdempotentReplay = "Idempotent-Replay"

	pollInterval = 250 * time.Millisecond
	pollDeadline = 30 * time.Second
)

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// Handler serves POST/GET /api/v1/screenings. Thin: validation,
// idempotency plumbing, and response envelopes live here; the pipeline
// belongs to the service.
type Handler struct {
	svc      *Service
	resolver *network.Resolver
	q        *db.Queries
	log      *slog.Logger
}

// NewHandler wires the service to Gin.
func NewHandler(svc *Service, resolver *network.Resolver,
	q *db.Queries, log *slog.Logger) *Handler {
	return &Handler{svc: svc, resolver: resolver, q: q, log: log}
}

// Create implements POST /api/v1/screenings — synchronous execution,
// HTTP 201 + Location (documented deviation from the spec's 202+PENDING
// example; PENDING is a transient claim state, never persisted).
func (h *Handler) Create(c *gin.Context) {
	client := middleware.ClientFrom(c)
	if client == nil {
		middleware.RespondError(c, http.StatusUnauthorized,
			middleware.CodeUnauthorized, "valid API key required")
		return
	}

	key := c.GetHeader(headerIdempotencyKey)
	keyed := strings.TrimSpace(key) != ""
	if keyed {
		if len(key) > 255 || !idempotencyKeyRe.MatchString(key) {
			middleware.RespondError(c, http.StatusBadRequest,
				middleware.CodeBadRequest,
				"Idempotency-Key must be 1-255 chars of [A-Za-z0-9._~-]")
			return
		}
	}

	var req Request
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "invalid request body")
		return
	}
	net, normalized, err := wallet.ResolveAndNormalize(h.resolver, req.Network, req.Address)
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
	opts := req.Options.Resolve()
	if opts.MaxHops < 0 || opts.MaxHops > 2 {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "options.max_hops must be between 0 and 2")
		return
	}
	canon := CanonicalRequest{Network: net.Code, Address: normalized,
		RawAddress: req.Address, Options: opts}
	hash := requestHash(canon)

	if !keyed {
		h.runAndRespond(c, net, canon, nil)
		return
	}

	h.claimAndRun(c, client.ID, net, canon, hash, key)
}

// GetByID serves GET /api/v1/screenings/{id} — the stored result body,
// byte-identical, or 404.
func (h *Handler) GetByID(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		middleware.RespondError(c, http.StatusBadRequest,
			middleware.CodeBadRequest, "invalid screening id")
		return
	}
	row, err := h.q.GetScreeningResultByID(c.Request.Context(),
		pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			middleware.RespondError(c, http.StatusNotFound,
				middleware.CodeNotFound, "screening not found")
			return
		}
		middleware.InternalError(c)
		return
	}
	c.Data(http.StatusOK, "application/json", dataEnvelope(row.Result))
}

// claimAndRun implements the claim-first state machine (plan §3.3):
// INSERT claim → 23505 → hash-check FIRST → replay / poll / takeover.
func (h *Handler) claimAndRun(c *gin.Context, clientID int64, net network.Network,
	canon CanonicalRequest, hash, key string) {

	for attempt := 0; attempt < 3; attempt++ {
		claim, err := h.q.InsertScreeningRequest(c.Request.Context(),
			db.InsertScreeningRequestParams{
				ClientID: clientID, IdempotencyKey: key, RequestHash: hash,
			})
		if err == nil {
			// Fresh claim — we own the pipeline leg.
			h.runAndRespond(c, net, canon, &claim.ID)
			return
		}
		if !isUniqueViolation(err) {
			h.logger().Error("screening claim insert failed",
				"request_id", middleware.GetRequestID(c), "error", err)
			middleware.InternalError(c)
			return
		}

		existing, err := h.q.GetScreeningRequestByKey(c.Request.Context(),
			db.GetScreeningRequestByKeyParams{
				ClientID: clientID, IdempotencyKey: key})
		if err != nil {
			// Claim vanished between insert-conflict and read — retry.
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			middleware.InternalError(c)
			return
		}

		// request_hash first — a key is bound to its payload for its
		// whole lifecycle, in every claim state.
		if existing.RequestHash != hash {
			middleware.RespondError(c, http.StatusConflict,
				middleware.CodeIdempotencyConflict,
				"Idempotency-Key already used with a different request")
			return
		}

		if existing.ScreeningResultID.Valid {
			h.replay(c, existing.ScreeningResultID)
			return
		}

		if !existing.ClaimStale {
			h.pollOrConflict(c, clientID, key, hash)
			return
		}

		// Stale open claim — owner presumed dead. The guarded delete
		// re-checks staleness+openness inside the DELETE; losing the
		// race just loops back to the conflict branch.
		n, err := h.q.DeleteStaleScreeningRequest(c.Request.Context(), existing.ID)
		if err != nil {
			middleware.InternalError(c)
			return
		}
		if n == 0 {
			continue // completed (or reclaimed) under us — re-read.
		}
		// Claim deleted — next attempt re-inserts.
	}

	middleware.RespondError(c, http.StatusConflict,
		middleware.CodeIdempotencyInFlight,
		"another screening with this key is still in flight")
}

// pollOrConflict waits for an in-flight same-hash claim to land its
// terminal row — 250 ms ticks, bounded at 30 s. The hash is re-checked
// every tick: a failed owner's claim can be released and the key
// re-claimed under a DIFFERENT payload mid-poll; replaying that result
// for this request would be a wrong-payload replay.
func (h *Handler) pollOrConflict(c *gin.Context, clientID int64, key, hash string) {
	deadline := time.Now().Add(pollDeadline)
	for time.Now().Before(deadline) {
		select {
		case <-c.Request.Context().Done():
			// Caller left or server is shutting down — still emit the
			// truthful envelope (in-flight) rather than a bare 200.
			middleware.RespondError(c, http.StatusConflict,
				middleware.CodeIdempotencyInFlight,
				"another screening with this key is still in flight")
			return
		case <-time.After(pollInterval):
		}
		claim, err := h.q.GetScreeningRequestByKey(c.Request.Context(),
			db.GetScreeningRequestByKeyParams{
				ClientID: clientID, IdempotencyKey: key})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				break // claim vanished — treat as in-flight failure
			}
			middleware.InternalError(c)
			return
		}
		if claim.RequestHash != hash {
			middleware.RespondError(c, http.StatusConflict,
				middleware.CodeIdempotencyConflict,
				"Idempotency-Key already used with a different request")
			return
		}
		if claim.ScreeningResultID.Valid {
			h.replay(c, claim.ScreeningResultID)
			return
		}
	}
	middleware.RespondError(c, http.StatusConflict,
		middleware.CodeIdempotencyInFlight,
		"another screening with this key is still in flight")
}

// replay serves the stored result body with the replay marker.
func (h *Handler) replay(c *gin.Context, resultID pgtype.UUID) {
	row, err := h.q.GetScreeningResultByID(c.Request.Context(), resultID)
	if err != nil {
		middleware.InternalError(c)
		return
	}
	c.Header(headerIdempotentReplay, "true")
	c.Data(http.StatusOK, "application/json", dataEnvelope(row.Result))
}

// runAndRespond executes the pipeline and writes the 201 response. On
// pipeline failure an owned claim is released so a retry is not held
// hostage by a row that can never complete.
func (h *Handler) runAndRespond(c *gin.Context, net network.Network,
	canon CanonicalRequest, claimID *int64) {

	res, err := h.svc.Run(c.Request.Context(), net, canon, RunMeta{
		RequestID: middleware.GetRequestID(c),
		ClaimID:   claimID,
	})
	if err != nil {
		if claimID != nil {
			// Bounded — pool acquisition must not hang forever under DB
			// distress, exactly when this path runs.
			rctx, rcancel := context.WithTimeout(context.Background(),
				10*time.Second)
			derr := h.q.DeleteScreeningRequest(rctx, *claimID)
			rcancel()
			if derr != nil {
				h.logger().Warn("screening claim release failed",
					"request_id", middleware.GetRequestID(c), "error", derr)
			}
		}
		middleware.InternalError(c)
		return
	}
	body, err := json.Marshal(res)
	if err != nil {
		middleware.InternalError(c)
		return
	}
	c.Header("Location", "/api/v1/screenings/"+res.ScreeningID)
	c.Data(http.StatusCreated, "application/json", dataEnvelope(body))
}

// dataEnvelope wraps a stored/computed result in the {"data":…}
// envelope — used for POST 201, replay 200, and GET so all three
// serialize identically.
func dataEnvelope(result []byte) []byte {
	out := make([]byte, 0, len(result)+12)
	out = append(out, `{"data":`...)
	out = append(out, result...)
	out = append(out, '}')
	return out
}

// requestHash is sha256 over the canonical request JSON — resolved
// network, normalized address, defaulted options; never raw body bytes.
func requestHash(canon CanonicalRequest) string {
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (h *Handler) logger() *slog.Logger {
	if h.log != nil {
		return h.log
	}
	return slog.Default()
}
