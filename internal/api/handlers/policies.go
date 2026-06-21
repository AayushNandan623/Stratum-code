// Package handlers contains the HTTP handlers for the Stratum REST API. Policy
// management, policy sets, bindings, and dry-run evaluation are in this file.
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/policy"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	"github.com/yourorg/stratum/internal/stack"
)

// PoliciesHandler handles policy management and evaluation endpoints.
type PoliciesHandler struct {
	svc      policy.PolicyService
	stackSvc stack.StackService
}

func NewPoliciesHandler(svc policy.PolicyService, stackSvc stack.StackService) *PoliciesHandler {
	return &PoliciesHandler{svc: svc, stackSvc: stackSvc}
}

// ─── Request / Response types ──────────────────────────────────────────────

type createPolicyRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	RegoSource  string  `json:"rego_source"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Enforcement *string `json:"enforcement,omitempty"`
}

type updatePolicyRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Enforcement *string `json:"enforcement,omitempty"`
}

type updateSourceRequest struct {
	RegoSource string `json:"rego_source"`
}

type createPolicySetRequest struct {
	Name string `json:"name"`
}

type addMemberRequest struct {
	PolicyID string `json:"policy_id"`
}

type createBindingRequest struct {
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

type dryRunRequest struct {
	StackID  string `json:"stack_id"`
	PlanJSON string `json:"plan_json"`
}

// ─── Policies CRUD ─────────────────────────────────────────────────────────

func (h *PoliciesHandler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req createPolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RegoSource == "" || req.Name == "" {
		httpjson.WriteError(w, domainerr.New("VALIDATION", 422, "name and rego_source are required"))
		return
	}
	var enforcement *policy.EnforcementLevel
	if req.Enforcement != nil {
		e := policy.EnforcementLevel(*req.Enforcement)
		enforcement = &e
	}
	p, err := h.svc.Create(r.Context(), policy.CreatePolicyInput{
		OrgID:       orgID,
		Name:        req.Name,
		Description: req.Description,
		RegoSource:  req.RegoSource,
		Enabled:     req.Enabled,
		Enforcement: enforcement,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, p)
}

func (h *PoliciesHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	policies, err := h.svc.List(r.Context(), orgID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, policies)
}

func (h *PoliciesHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	policyID, ok := parseUUID(w, r, "policy_id")
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), policyID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, p)
}

func (h *PoliciesHandler) Update(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	policyID, ok := parseUUID(w, r, "policy_id")
	if !ok {
		return
	}
	var req updatePolicyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	input := policy.UpdatePolicyInput{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
	}
	if req.Enforcement != nil {
		e := policy.EnforcementLevel(*req.Enforcement)
		input.Enforcement = &e
	}
	p, err := h.svc.Update(r.Context(), policyID, input)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, p)
}

func (h *PoliciesHandler) UpdateSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	policyID, ok := parseUUID(w, r, "policy_id")
	if !ok {
		return
	}
	var req updateSourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.UpdateSource(r.Context(), policyID, req.RegoSource); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *PoliciesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	policyID, ok := parseUUID(w, r, "policy_id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), policyID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Policy sets ───────────────────────────────────────────────────────────

func (h *PoliciesHandler) CreatePolicySet(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req createPolicySetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ps, err := h.svc.CreatePolicySet(r.Context(), policy.CreatePolicySetInput{
		OrgID: orgID,
		Name:  req.Name,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, ps)
}

func (h *PoliciesHandler) AddToSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	setID, ok := parseUUID(w, r, "set_id")
	if !ok {
		return
	}
	var req addMemberRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	policyID, err := uuid.Parse(req.PolicyID)
	if err != nil {
		httpjson.WriteError(w, domainerr.New("VALIDATION", 422, "invalid policy_id"))
		return
	}
	if err := h.svc.AddToSet(r.Context(), setID, policyID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PoliciesHandler) RemoveFromSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	setID, ok := parseUUID(w, r, "set_id")
	if !ok {
		return
	}
	policyID, ok := parseUUID(w, r, "policy_id")
	if !ok {
		return
	}
	if err := h.svc.RemoveFromSet(r.Context(), setID, policyID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Bindings ──────────────────────────────────────────────────────────────

func (h *PoliciesHandler) BindSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	setID, ok := parseUUID(w, r, "set_id")
	if !ok {
		return
	}
	var req createBindingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resourceID, err := uuid.Parse(req.ResourceID)
	if err != nil {
		httpjson.WriteError(w, domainerr.New("VALIDATION", 422, "invalid resource_id"))
		return
	}
	if req.ResourceType != "ORG" && req.ResourceType != "SPACE" && req.ResourceType != "STACK" {
		httpjson.WriteError(w, domainerr.New("VALIDATION", 422, "resource_type must be ORG, SPACE, or STACK"))
		return
	}
	b, err := h.svc.BindSet(r.Context(), setID, req.ResourceType, resourceID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, b)
}

func (h *PoliciesHandler) UnbindSet(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	bindingID, ok := parseUUID(w, r, "binding_id")
	if !ok {
		return
	}
	if err := h.svc.UnbindSet(r.Context(), bindingID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Dry run ───────────────────────────────────────────────────────────────

func (h *PoliciesHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req dryRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	stackID, err := uuid.Parse(req.StackID)
	if err != nil {
		httpjson.WriteError(w, domainerr.New("VALIDATION", 422, "invalid stack_id"))
		return
	}
	// Verify stack belongs to org
	if _, err := h.stackSvc.Get(r.Context(), orgID, stackID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	result, err := h.svc.DryRun(r.Context(), policy.DryRunInput{
		OrgID:    orgID,
		StackID:  stackID,
		PlanJSON: req.PlanJSON,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, result)
}
