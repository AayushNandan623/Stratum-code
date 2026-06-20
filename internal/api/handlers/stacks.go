// Stack handlers: CRUD, variables, and dependency graph endpoints.
package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	"github.com/yourorg/stratum/internal/stack"
)

// StacksHandler exposes stack, variable, and dependency endpoints.
type StacksHandler struct {
	svc stack.StackService
}

// NewStacksHandler constructs a StacksHandler.
func NewStacksHandler(svc stack.StackService) *StacksHandler {
	return &StacksHandler{svc: svc}
}

type createStackRequest struct {
	Name              string `json:"name"`
	VCSRepo           string `json:"vcs_repo"`
	VCSBranch         string `json:"vcs_branch"`
	WorkingDir        string `json:"working_dir"`
	IACTool           string `json:"iac_tool"`
	IACVersion        string `json:"iac_version"`
	AutoApply         bool   `json:"auto_apply"`
	ReconcileInterval int64  `json:"reconcile_interval_seconds"`
	DriftMode         string `json:"drift_mode"`
}

// Create handles POST /api/v1/orgs/{org_id}/stacks.
func (h *StacksHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req createStackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s, err := h.svc.Create(r.Context(), stack.CreateStackInput{
		OrgID:             orgID,
		Name:              req.Name,
		VCSRepo:           req.VCSRepo,
		VCSBranch:         req.VCSBranch,
		WorkingDir:        req.WorkingDir,
		IACTool:           req.IACTool,
		IACVersion:        req.IACVersion,
		AutoApply:         req.AutoApply,
		ReconcileInterval: time.Duration(req.ReconcileInterval) * time.Second,
		DriftMode:         stack.DriftMode(req.DriftMode),
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, s)
}

// List handles GET /api/v1/orgs/{org_id}/stacks.
func (h *StacksHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	stacks, total, err := h.svc.GetByOrgID(r.Context(), orgID, stack.Pagination{Page: page, Size: size})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{
		"stacks": stacks,
		"total":  total,
		"page":   page,
		"size":   size,
	})
}

// Get handles GET /api/v1/stacks/{stack_id}.
func (h *StacksHandler) Get(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	s, err := h.svc.Get(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, s)
}

type updateStackRequest struct {
	VCSRepo           *string `json:"vcs_repo"`
	VCSBranch         *string `json:"vcs_branch"`
	WorkingDir        *string `json:"working_dir"`
	IACTool           *string `json:"iac_tool"`
	IACVersion        *string `json:"iac_version"`
	AutoApply         *bool   `json:"auto_apply"`
	ReconcileInterval *int64  `json:"reconcile_interval_seconds"`
	DriftMode         *string `json:"drift_mode"`
	Status            *string `json:"status"`
}

// Update handles PATCH /api/v1/stacks/{stack_id}.
func (h *StacksHandler) Update(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	var req updateStackRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	input := stack.UpdateStackInput{
		VCSRepo:    req.VCSRepo,
		VCSBranch:  req.VCSBranch,
		WorkingDir: req.WorkingDir,
		IACTool:    req.IACTool,
		IACVersion: req.IACVersion,
		AutoApply:  req.AutoApply,
	}
	if req.ReconcileInterval != nil {
		d := time.Duration(*req.ReconcileInterval) * time.Second
		input.ReconcileInterval = &d
	}
	if req.DriftMode != nil {
		dm := stack.DriftMode(*req.DriftMode)
		input.DriftMode = &dm
	}
	if req.Status != nil {
		st := stack.StackStatus(*req.Status)
		input.Status = &st
	}
	s, err := h.svc.Update(r.Context(), identity.OrgID, stackID, input)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, s)
}

// Delete handles DELETE /api/v1/stacks/{stack_id}.
func (h *StacksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), identity.OrgID, stackID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type addDependencyRequest struct {
	DependsOnID string `json:"depends_on_id"`
}

// AddDependency handles POST /api/v1/stacks/{stack_id}/dependencies. Returns
// 409 if the edge would create a cycle.
func (h *StacksHandler) AddDependency(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	var req addDependencyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	dependsOnID, err := uuid.Parse(req.DependsOnID)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	if err := h.svc.AddDependency(r.Context(), identity.OrgID, stackID, dependsOnID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveDependency handles DELETE /api/v1/stacks/{stack_id}/dependencies/{dep_id}.
func (h *StacksHandler) RemoveDependency(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	depID, ok := parseUUID(w, r, "dep_id")
	if !ok {
		return
	}
	if err := h.svc.RemoveDependency(r.Context(), identity.OrgID, stackID, depID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetDependencies handles GET /api/v1/stacks/{stack_id}/dependencies.
func (h *StacksHandler) GetDependencies(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	deps, err := h.svc.GetDependencies(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"dependencies": deps})
}

type setVariableRequest struct {
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
	Category  string `json:"category"`
}

// SetVariable handles PUT /api/v1/stacks/{stack_id}/variables/{key}.
func (h *StacksHandler) SetVariable(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	var req setVariableRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.SetVariable(r.Context(), identity.OrgID, stackID, stack.VariableInput{
		Key:       r.PathValue("key"),
		Value:     req.Value,
		Sensitive: req.Sensitive,
		Category:  req.Category,
	}); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteVariable handles DELETE /api/v1/stacks/{stack_id}/variables/{key}.
func (h *StacksHandler) DeleteVariable(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	if err := h.svc.DeleteVariable(r.Context(), identity.OrgID, stackID, r.PathValue("key")); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type variableResponse struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
	Category  string `json:"category"`
}

// ListVariables handles GET /api/v1/stacks/{stack_id}/variables. Sensitive
// values are masked.
func (h *StacksHandler) ListVariables(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return
	}
	vars, err := h.svc.ListVariables(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	out := make([]variableResponse, 0, len(vars))
	for _, v := range vars {
		val := v.Value
		if v.Sensitive {
			val = "***"
		}
		out = append(out, variableResponse{Key: v.Key, Value: val, Sensitive: v.Sensitive, Category: v.Category})
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"variables": out})
}
