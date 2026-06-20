// State handlers: current state metadata, version history, and locking.
package handlers

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/state"
	"github.com/yourorg/stratum/internal/stack"
)

// StateHandler exposes state metadata and lock endpoints.
type StateHandler struct {
	svc      state.StateService
	stackSvc stack.StackService
}

// NewStateHandler constructs a StateHandler.
func NewStateHandler(svc state.StateService, stackSvc stack.StackService) *StateHandler {
	return &StateHandler{svc: svc, stackSvc: stackSvc}
}

// Get handles GET /api/v1/stacks/{stack_id}/state.
func (h *StateHandler) Get(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	v, err := h.svc.GetState(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, v)
}

// ListVersions handles GET /api/v1/stacks/{stack_id}/state/versions.
func (h *StateHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	versions, err := h.svc.ListVersions(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

type lockRequest struct {
	LockID  string `json:"lock_id"`
	Who     string `json:"who"`
	Info    string `json:"info"`
	Version string `json:"version"`
}

// AcquireLock handles POST /api/v1/stacks/{stack_id}/state/lock. Returns 409 if
// the state is already locked.
func (h *StateHandler) AcquireLock(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	var req lockRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.AcquireLock(r.Context(), identity.OrgID, stackID, state.LockRequest{
		LockID:  req.LockID,
		Who:     req.Who,
		Info:    req.Info,
		Version: req.Version,
	}); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, map[string]string{"lock_id": req.LockID})
}

type releaseLockRequest struct {
	LockID string `json:"lock_id"`
}

// ReleaseLock handles DELETE /api/v1/stacks/{stack_id}/state/lock. The lock ID
// is read from the lock_id query parameter, falling back to a JSON body.
func (h *StateHandler) ReleaseLock(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	lockID := r.URL.Query().Get("lock_id")
	if lockID == "" {
		var req releaseLockRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		lockID = req.LockID
	}
	if err := h.svc.ReleaseLock(r.Context(), identity.OrgID, stackID, lockID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
