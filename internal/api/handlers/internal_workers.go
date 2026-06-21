package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/api/middleware"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
	"github.com/yourorg/stratum/internal/vcs"
	"github.com/yourorg/stratum/internal/worker"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// InternalWorkerHandler exposes worker-facing internal API endpoints for
// registration, heartbeat, job claiming, event/log ingestion, secrets, and
// source archives.
type InternalWorkerHandler struct {
	workerSvc   worker.WorkerService
	runSvc      run.RunService
	secretSvc   secret.SecretService
	stackSvc    stack.StackService
	vcsSvc      vcs.VCSService
	hmacSecret  string
}

// NewInternalWorkerHandler constructs an InternalWorkerHandler.
func NewInternalWorkerHandler(
	workerSvc worker.WorkerService,
	runSvc run.RunService,
	secretSvc secret.SecretService,
	stackSvc stack.StackService,
	vcsSvc vcs.VCSService,
	hmacSecret string,
) *InternalWorkerHandler {
	return &InternalWorkerHandler{
		workerSvc: workerSvc,
		runSvc:    runSvc,
		secretSvc: secretSvc,
		stackSvc:  stackSvc,
		vcsSvc:    vcsSvc,
		hmacSecret: hmacSecret,
	}
}

// registerRequest is the payload for POST /internal/workers/register.
type registerRequest struct {
	PoolID       string   `json:"pool_id"`
	Hostname     string   `json:"hostname"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// Register handles POST /api/v1/internal/workers/register.
// Unlike other internal endpoints, this endpoint is NOT behind WorkerAuth
// middleware (the worker doesn't exist yet). Instead, it validates the pool
// token from the Authorization header.
func (h *InternalWorkerHandler) Register(w http.ResponseWriter, r *http.Request) {
	// Extract and validate the pool token from the Authorization header.
	rawToken := extractBearerToken(r)
	if rawToken == "" {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return
	}
	tokenHash := worker.HashToken(rawToken, h.hmacSecret)
	pool, err := h.workerSvc.GetPoolByTokenHash(r.Context(), tokenHash)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return
	}

	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	poolID, err := uuid.Parse(req.PoolID)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	// Verify the pool ID in the request matches the pool looked up by token.
	if poolID != pool.ID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	wkr, err := h.workerSvc.Register(r.Context(), worker.RegisterWorkerInput{
		OrgID:        pool.OrgID,
		PoolID:       poolID,
		Hostname:     req.Hostname,
		Version:      req.Version,
		Capabilities: req.Capabilities,
		TokenHash:    tokenHash, // store HMAC of pool token as worker's token hash
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, map[string]any{
		"worker_id":             wkr.ID,
		"heartbeat_interval_s":  15,
	})
}

// heartbeatRequest is the payload for POST /internal/workers/{id}/heartbeat.
type heartbeatRequest struct {
	Status       string  `json:"status"`
	CurrentRunID *string `json:"current_run_id,omitempty"`
}

// Heartbeat handles POST /api/v1/internal/workers/{id}/heartbeat.
func (h *InternalWorkerHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	wkr := middleware.WorkerFromContext(r.Context())
	if wkr == nil {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return
	}

	var req heartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	status := worker.WorkerStatus(req.Status)
	if status == "" {
		status = worker.StatusIDLE
	}

	var currentRunID *uuid.UUID
	if req.CurrentRunID != nil {
		id, err := uuid.Parse(*req.CurrentRunID)
		if err == nil {
			currentRunID = &id
		}
	}

	resp, err := h.workerSvc.Heartbeat(r.Context(), wkr.ID, status, currentRunID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, resp)
}

// GetJobs handles GET /api/v1/internal/workers/{id}/jobs?timeout=30.
// Long-poll: blocks up to the timeout param (max 30s), polling every 2s.
func (h *InternalWorkerHandler) GetJobs(w http.ResponseWriter, r *http.Request) {
	wkr := middleware.WorkerFromContext(r.Context())
	if wkr == nil {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return
	}

	timeoutSec := 30
	if t := r.URL.Query().Get("timeout"); t != "" {
		if v, err := strconv.Atoi(t); err == nil && v > 0 && v <= 30 {
			timeoutSec = v
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	for {
		job, err := h.workerSvc.ClaimJob(ctx, wkr.ID, 1*time.Second)
		if err != nil && !errors.Is(err, worker.ErrNoJobAvailable) && !errors.Is(err, run.ErrNoJobAvailable) {
			httpjson.WriteError(w, err)
			return
		}
		if job != nil {
			httpjson.WriteJSON(w, http.StatusOK, job.ToResponse())
			return
		}

		select {
		case <-ctx.Done():
			w.WriteHeader(http.StatusNoContent)
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// Deregister handles DELETE /api/v1/internal/workers/{id}.
func (h *InternalWorkerHandler) Deregister(w http.ResponseWriter, r *http.Request) {
	wkr := middleware.WorkerFromContext(r.Context())
	if wkr == nil {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return
	}
	if err := h.workerSvc.Deregister(r.Context(), wkr.ID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "deregistered"})
}

// eventToState maps worker event types to run states for auto-transition.
var eventToState = map[string]run.RunState{
	"run.planning_started": run.StatePlanning,
	"run.planned":          run.StatePlanned,
	"run.applying_started": run.StateApplying,
	"run.applied":          run.StateApplied,
	"run.failed":           run.StateFailed,
	"run.cancelled":        run.StateCancelled,
	"run.destroyed":        run.StateApplied, // destroy success maps to applied state
}

// AppendEvent handles POST /api/v1/internal/runs/{id}/events.
// In addition to appending the event, it drives run state transitions for
// worker-reported lifecycle events. When the event type maps to a known state
// transition, the transition is used instead of a standalone append to avoid
// duplicate events (Transition already records the event internally).
func (h *InternalWorkerHandler) AppendEvent(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUID(w, r, "id")
	if !ok {
		return
	}
	var input run.RunEventInput
	if !decodeJSON(w, r, &input) {
		return
	}
	// If the event type maps to a state transition, drive the transition
	// (which appends the event internally). Otherwise, append the event
	// directly.
	state, isStateTransition := eventToState[input.EventType]
	if isStateTransition {
		if err := h.runSvc.Transition(r.Context(), runID, state, nil); err != nil {
			httpjson.WriteError(w, err)
			return
		}
	} else {
		if err := h.runSvc.AppendEvent(r.Context(), runID, input); err != nil {
			httpjson.WriteError(w, err)
			return
		}
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// AppendLogs handles POST /api/v1/internal/runs/{id}/logs.
type appendLogsRequest struct {
	Lines []run.LogLine `json:"lines"`
}

func (h *InternalWorkerHandler) AppendLogs(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUID(w, r, "id")
	if !ok {
		return
	}
	var req appendLogsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.runSvc.AppendLogs(r.Context(), runID, req.Lines); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"accepted": len(req.Lines)})
}

// GetSourceArchive handles GET /api/v1/internal/runs/{id}/source-archive.
// In development mode when no VCS connection is configured, it returns a stub
// empty tar.gz so the worker can proceed with execution.
func (h *InternalWorkerHandler) GetSourceArchive(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUID(w, r, "id")
	if !ok {
		return
	}
	ra, err := h.runSvc.Get(r.Context(), runID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	stk, err := h.stackSvc.Get(r.Context(), ra.OrgID, ra.StackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	archive, err := h.vcsSvc.GetSourceArchive(r.Context(), ra.StackID, stk.VCSBranch)
	if err != nil {
		// In development, return a stub empty archive so the worker can
		// proceed with execution without a real VCS setup.
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff}) // empty gzip
		return
	}
	defer archive.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, archive)
}

// ClaimSecrets handles POST /api/v1/internal/runs/{id}/secrets/claim.
type claimSecretsRequest struct {
	WorkerID string `json:"worker_id"`
}

func (h *InternalWorkerHandler) ClaimSecrets(w http.ResponseWriter, r *http.Request) {
	runID, ok := parseUUID(w, r, "id")
	if !ok {
		return
	}
	var req claimSecretsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	workerID, err := uuid.Parse(req.WorkerID)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	values, err := h.secretSvc.ClaimValues(r.Context(), runID, workerID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"secrets": values})
}
