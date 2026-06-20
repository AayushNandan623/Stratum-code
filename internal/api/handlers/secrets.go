// Secret handlers: set, delete, and list (metadata only) for stack-scoped
// secrets. Plaintext values never appear in responses.
package handlers

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
)

// SecretsHandler exposes stack-scoped secret endpoints.
type SecretsHandler struct {
	svc      secret.SecretService
	stackSvc stack.StackService
}

// NewSecretsHandler constructs a SecretsHandler.
func NewSecretsHandler(svc secret.SecretService, stackSvc stack.StackService) *SecretsHandler {
	return &SecretsHandler{svc: svc, stackSvc: stackSvc}
}

type setSecretRequest struct {
	Value string `json:"value"`
}

// Set handles PUT /api/v1/stacks/{stack_id}/secrets/{name}.
func (h *SecretsHandler) Set(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	var req setSecretRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	if err := h.svc.Set(r.Context(), secret.SetSecretInput{
		OrgID:   identity.OrgID,
		Scope:   secret.ScopeStack,
		ScopeID: stackID,
		Name:    name,
		Value:   req.Value,
	}); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete handles DELETE /api/v1/stacks/{stack_id}/secrets/{name}.
func (h *SecretsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), identity.OrgID, stackID, r.PathValue("name")); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// List handles GET /api/v1/stacks/{stack_id}/secrets. Returns names and
// metadata only; values are masked.
func (h *SecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	stackID, ok := verifyStackInOrg(w, r, h.stackSvc, identity)
	if !ok {
		return
	}
	metas, err := h.svc.List(r.Context(), identity.OrgID, stackID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]any{"secrets": metas})
}
