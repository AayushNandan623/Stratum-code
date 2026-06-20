// Organization handlers: org creation (public bootstrap) and retrieval.
package handlers

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/iam"
)

// OrgsHandler exposes organization endpoints.
type OrgsHandler struct {
	svc iam.IAMService
}

// NewOrgsHandler constructs an OrgsHandler.
func NewOrgsHandler(svc iam.IAMService) *OrgsHandler {
	return &OrgsHandler{svc: svc}
}

type createOrgRequest struct {
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
}

// Create handles POST /api/v1/orgs. It is public so the first org and admin
// user can be bootstrapped before any credentials exist.
func (h *OrgsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createOrgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	org, err := h.svc.CreateOrg(r.Context(), iam.CreateOrgInput{
		Name:          req.Name,
		Slug:          req.Slug,
		AdminEmail:    req.AdminEmail,
		AdminPassword: req.AdminPassword,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, org)
}

// Get handles GET /api/v1/orgs/{org_id}.
func (h *OrgsHandler) Get(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	org, err := h.svc.GetOrg(r.Context(), orgID)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, org)
}
