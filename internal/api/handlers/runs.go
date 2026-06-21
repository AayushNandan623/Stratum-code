package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/api/ws"
	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/run"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// RunsHandler exposes run lifecycle endpoints and event streaming.
type RunsHandler struct {
	svc    run.RunService
	hub    *ws.NATSHub
	logger *slog.Logger
}

// NewRunsHandler constructs a RunsHandler.
func NewRunsHandler(svc run.RunService, hub *ws.NATSHub, logger *slog.Logger) *RunsHandler {
	return &RunsHandler{svc: svc, hub: hub, logger: logger}
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // permissive for dev
}

type createRunRequest struct {
	RunType       string `json:"run_type"`
	TriggerType   string `json:"trigger_type"`
	ConfigVersion string `json:"config_version"`
}

// Create handles POST /api/v1/stacks/{stack_id}/runs.
func (h *RunsHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	var req createRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	runType := run.RunType(req.RunType)
	switch runType {
	case run.RunTypePlan, run.RunTypeApply, run.RunTypeDestroy, run.RunTypeDriftDetect:
		// valid
	case "":
		runType = run.RunTypePlan
	default:
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	triggerType := run.TriggerType(req.TriggerType)
	if triggerType == "" {
		triggerType = run.TriggerManual
	}
	triggeredBy := identity.ID
	ra, err := h.svc.Create(r.Context(), run.CreateRunInput{
		OrgID:         identity.OrgID,
		StackID:       stackID,
		RunType:       runType,
		TriggerType:   triggerType,
		TriggeredBy:   &triggeredBy,
		ConfigVersion: req.ConfigVersion,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, ra)
}

// List handles GET /api/v1/stacks/{stack_id}/runs.
func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	stackIDCopy := stackID

	runs, total, err := h.svc.List(r.Context(), run.RunFilter{
		OrgID:   identity.OrgID,
		StackID: &stackIDCopy,
		Page:    page,
		Size:    size,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{
		"runs":  runs,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// Get handles GET /api/v1/runs/{run_id}.
func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUID(w, r, "run_id")
	if !ok {
		return
	}
	ra, err := h.svc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	if ra.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, ra)
}

// runFromRequest fetches the run and verifies it belongs to the caller's org.
// Returns the run on success or writes an error and returns nil.
func (h *RunsHandler) runFromRequest(w http.ResponseWriter, r *http.Request, identity iam.Identity) *run.Run {
	runID, ok := parseUUID(w, r, "run_id")
	if !ok {
		return nil
	}
	ra, err := h.svc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return nil
	}
	if ra.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return nil
	}
	return ra
}

// Cancel handles POST /api/v1/runs/{run_id}/cancel.
func (h *RunsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if ra := h.runFromRequest(w, r, identity); ra == nil {
		return
	}
	runID, _ := uuid.Parse(r.PathValue("run_id"))
	if err := h.svc.Cancel(r.Context(), runID, identity.ID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// Approve handles POST /api/v1/runs/{run_id}/approve.
func (h *RunsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if ra := h.runFromRequest(w, r, identity); ra == nil {
		return
	}
	runID, _ := uuid.Parse(r.PathValue("run_id"))
	if err := h.svc.Approve(r.Context(), runID, identity.ID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// Discard handles POST /api/v1/runs/{run_id}/discard.
func (h *RunsHandler) Discard(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if ra := h.runFromRequest(w, r, identity); ra == nil {
		return
	}
	runID, _ := uuid.Parse(r.PathValue("run_id"))
	if err := h.svc.Discard(r.Context(), runID, identity.ID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}

// GetTimeline handles GET /api/v1/runs/{run_id}/timeline.
func (h *RunsHandler) GetTimeline(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUID(w, r, "run_id")
	if !ok {
		return
	}
	// Verify the run belongs to the caller's org.
	ra, err := h.svc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	if ra.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	events, err := h.svc.GetTimeline(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"events": events})
}

// GetLogs handles GET /api/v1/runs/{run_id}/logs.
func (h *RunsHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	runID, ok := parseUUID(w, r, "run_id")
	if !ok {
		return
	}
	// Verify the run belongs to the caller's org.
	ra, err := h.svc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	if ra.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	logs, total, err := h.svc.GetLogs(r.Context(), runID, run.Pagination{Page: page, Size: size})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{
		"logs":  logs,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// EventStream handles the WS /api/v1/runs/{run_id}/events/stream endpoint.
// It upgrades the connection to WebSocket and forwards run events as they
// arrive via NATS. Historical events are served through the REST timeline
// endpoint (GET /api/v1/runs/{run_id}/timeline) at connect time.
func (h *RunsHandler) EventStream(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUID(w, r, "run_id")
	if !ok {
		return
	}
	// Verify run exists and belongs to caller's org.
	ra, err := h.svc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if ra.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade failed", "run_id", runID, "error", err)
		return
	}

	cleanup, err := h.hub.SubscribeToRun(r.Context(), runID, func(data []byte) {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			h.logger.Debug("ws write error", "run_id", runID, "error", err)
		}
	})
	if err != nil {
		h.logger.Error("nats subscribe failed", "run_id", runID, "error", err)
		conn.Close()
		return
	}
	defer cleanup()
	defer conn.Close()

	// Read pump detects client disconnect. Exit when the connection closes.
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	// Wait for either client disconnect (read error) or context cancel.
	select {
	case <-readErr:
	case <-r.Context().Done():
	}
}


