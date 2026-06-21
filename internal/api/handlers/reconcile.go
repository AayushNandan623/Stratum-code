package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/reconcile"
	"github.com/yourorg/stratum/internal/stack"
)

// ReconcileHandler handles drift detection and schedule management endpoints.
type ReconcileHandler struct {
	svc      reconcile.ReconcileService
	stackSvc stack.StackService
}

func NewReconcileHandler(svc reconcile.ReconcileService, stackSvc stack.StackService) *ReconcileHandler {
	return &ReconcileHandler{svc: svc, stackSvc: stackSvc}
}

// ─── Request / Response types ──────────────────────────────────────────────

type updateScheduleRequest struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	ReconcileInterval *int    `json:"reconcile_interval_seconds,omitempty"`
	DriftMode         *string `json:"drift_mode,omitempty"`
}

type ignoreDriftRequest struct {
	Reason string `json:"reason"`
}

// ─── Schedule ──────────────────────────────────────────────────────────────

func (h *ReconcileHandler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	schedule, err := h.svc.GetSchedule(r.Context(), stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, schedule)
}

func (h *ReconcileHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	var req updateScheduleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	input := reconcile.UpdateScheduleInput{
		Enabled:   req.Enabled,
		DriftMode: (*reconcile.DriftMode)(req.DriftMode),
	}
	if req.ReconcileInterval != nil {
		d := reconcile.Duration(time.Duration(*req.ReconcileInterval) * time.Second)
		input.ReconcileInterval = &d
	}
	schedule, err := h.svc.UpdateSchedule(r.Context(), stackID, input)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, schedule)
}

// ─── Manual trigger ────────────────────────────────────────────────────────

func (h *ReconcileHandler) TriggerNow(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	run, err := h.svc.TriggerNow(r.Context(), stackID, identity.ID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, map[string]any{
		"run_id": run.ID,
		"status": "drift_detect_run_created",
	})
}

// ─── Drift records ─────────────────────────────────────────────────────────

func (h *ReconcileHandler) ListDriftRecords(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	filter := reconcile.DriftFilter{
		OrgID: orgID,
		Page:  1,
		Size:  50,
	}
	if stackIDStr := r.URL.Query().Get("stack_id"); stackIDStr != "" {
		id, err := uuid.Parse(stackIDStr)
		if err == nil {
			filter.StackID = &id
		}
	}
	records, total, err := h.svc.ListDriftRecords(r.Context(), filter)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
	})
}

func (h *ReconcileHandler) GetDriftRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	driftID, ok := parseUUID(w, r, "drift_id")
	if !ok {
		return
	}
	rec, err := h.svc.GetDriftRecord(r.Context(), driftID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, rec)
}

func (h *ReconcileHandler) IgnoreDrift(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	driftID, ok := parseUUID(w, r, "drift_id")
	if !ok {
		return
	}
	var req ignoreDriftRequest
	_ = decodeJSON(w, r, &req) // body is optional
	if err := h.svc.IgnoreDrift(r.Context(), driftID, identity.ID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
}
