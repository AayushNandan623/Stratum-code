package handlers

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	"github.com/yourorg/stratum/internal/worker"
)

// WorkersHandler exposes worker pool management endpoints for the user-facing API.
type WorkersHandler struct {
	svc worker.WorkerService
}

// NewWorkersHandler constructs a WorkersHandler.
func NewWorkersHandler(svc worker.WorkerService) *WorkersHandler {
	return &WorkersHandler{svc: svc}
}

type createPoolRequest struct {
	Name           string `json:"name"`
	PoolType       string `json:"pool_type"`
	MaxConcurrency int    `json:"max_concurrency"`
}

// CreatePool handles POST /api/v1/orgs/{org_id}/worker-pools.
func (h *WorkersHandler) CreatePool(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req createPoolRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	poolType := worker.PoolType(req.PoolType)
	if poolType == "" {
		poolType = worker.PoolTypePrivate
	}
	pool, token, err := h.svc.CreatePool(r.Context(), worker.CreatePoolInput{
		OrgID:          orgID,
		Name:           req.Name,
		PoolType:       poolType,
		MaxConcurrency: req.MaxConcurrency,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, map[string]any{
		"pool":  pool,
		"token": token,
	})
}

// GetPool handles GET /api/v1/worker-pools/{pool_id}.
func (h *WorkersHandler) GetPool(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUID(w, r, "pool_id")
	if !ok {
		return
	}
	pool, err := h.svc.GetPool(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	// Verify caller belongs to the pool's org.
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if pool.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, pool)
}

// ListPools handles GET /api/v1/orgs/{org_id}/worker-pools.
func (h *WorkersHandler) ListPools(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	pools, err := h.svc.ListPools(r.Context(), orgID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"pools": pools})
}

// DeletePool handles DELETE /api/v1/worker-pools/{pool_id}.
func (h *WorkersHandler) DeletePool(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUID(w, r, "pool_id")
	if !ok {
		return
	}
	pool, err := h.svc.GetPool(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if pool.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	if err := h.svc.DeletePool(r.Context(), poolID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// RotatePoolToken handles POST /api/v1/worker-pools/{pool_id}/rotate-token.
func (h *WorkersHandler) RotatePoolToken(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUID(w, r, "pool_id")
	if !ok {
		return
	}
	pool, err := h.svc.GetPool(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if pool.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	token, err := h.svc.RotatePoolToken(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"token": token})
}

// ListActiveWorkers handles GET /api/v1/worker-pools/{pool_id}/workers.
func (h *WorkersHandler) ListActiveWorkers(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUID(w, r, "pool_id")
	if !ok {
		return
	}
	pool, err := h.svc.GetPool(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if pool.OrgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return
	}
	workers, err := h.svc.ListActiveWorkers(r.Context(), poolID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"workers": workers})
}
