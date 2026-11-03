package ingestion

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
)

// AdminHandler exposes the admin ingestion endpoints.
type AdminHandler struct {
	svc *Service
	q   *db.Queries
	log *slog.Logger
}

func NewAdminHandler(svc *Service, q *db.Queries, log *slog.Logger) *AdminHandler {
	return &AdminHandler{svc: svc, q: q, log: log}
}

type triggerBody struct {
	Source string `json:"source"`
}

// TriggerRun: POST /api/v1/admin/ingestion-runs {source:"ofac_sdn"} → 202.
// 409 INGESTION_RUNNING when a run is already in flight.
func (h *AdminHandler) TriggerRun(c *gin.Context) {
	var body triggerBody
	// Empty body is allowed: default to the only registered source.
	_ = c.ShouldBindJSON(&body)
	if body.Source == "" {
		body.Source = "ofac_sdn"
	}
	triggeredBy := "unknown"
	if cl := middleware.ClientFrom(c); cl != nil {
		triggeredBy = cl.Name
	}
	runID, err := h.svc.Enqueue(c.Request.Context(), RunRequest{
		SourceCode:  body.Source,
		TriggeredBy: triggeredBy,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrIngestionRunning):
			middleware.RespondError(c, http.StatusConflict, "INGESTION_RUNNING",
				"an ingestion run is already in progress")
		case errors.Is(err, pgx.ErrNoRows):
			middleware.RespondError(c, http.StatusBadRequest, "INVALID_SOURCE",
				"unknown source")
		default:
			middleware.InternalError(c)
		}
		return
	}
	// Audit line (spec §16): actor, source, run id, request id.
	h.log.Info("ingestion triggered",
		"actor", triggeredBy,
		"source", body.Source,
		"run_id", runID.String(),
		"request_id", middleware.GetRequestID(c),
	)
	c.JSON(http.StatusAccepted, gin.H{"data": gin.H{
		"run_id": runID.String(), "status": "RUNNING", "source": body.Source,
	}})
}

// GetRun: GET /api/v1/admin/ingestion-runs/:id
func (h *AdminHandler) GetRun(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		middleware.RespondError(c, http.StatusBadRequest, "INVALID_RUN_ID", "invalid run id")
		return
	}
	run, err := h.q.GetIngestionRun(c.Request.Context(),
		pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		middleware.RespondError(c, http.StatusNotFound, middleware.CodeNotFound, "run not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": runDTO(run)})
}

func runDTO(r db.IngestionRun) gin.H {
	out := gin.H{
		"run_id":            uuid.UUID(r.ID.Bytes).String(),
		"source_id":         r.SourceID,
		"status":            r.Status,
		"started_at":        r.StartedAt.Time.UTC(),
		"records_fetched":   r.RecordsFetched,
		"records_inserted":  r.RecordsInserted,
		"records_updated":   r.RecordsUpdated,
		"records_rejected":  r.RecordsRejected,
		"triggered_by":      textOrNil(r.TriggeredBy),
		"snapshot_checksum": textOrNil(r.SnapshotChecksum),
		"snapshot_version":  textOrNil(r.SnapshotVersion),
		"error_message":     textOrNil(r.ErrorMessage),
	}
	if r.CompletedAt.Valid {
		out["completed_at"] = r.CompletedAt.Time.UTC()
	} else {
		out["completed_at"] = nil
	}
	return out
}

func textOrNil(t pgtype.Text) any {
	if !t.Valid {
		return nil
	}
	return t.String
}
