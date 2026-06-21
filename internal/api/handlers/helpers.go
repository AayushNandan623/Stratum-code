// Package handlers contains the HTTP handlers for the Stratum REST API. Each
// bounded context has its own handler file; this file holds shared helpers for
// identity extraction, path parsing, and body decoding.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/iam"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	"github.com/yourorg/stratum/internal/stack"
)

// extractBearerToken pulls the value from an "Authorization: Bearer <token>"
// header. Returns empty string when the header is missing or malformed.
func extractBearerToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return ""
	}
	parts := strings.SplitN(hdr, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}

// identityFromRequest extracts the authenticated identity, writing 401 if it is
// absent (e.g. the route is not behind the auth middleware).
func identityFromRequest(w http.ResponseWriter, r *http.Request) (iam.Identity, bool) {
	id, ok := iam.IdentityFromContext(r.Context())
	if !ok {
		httpjson.WriteError(w, domainerr.ErrUnauthorized)
		return iam.Identity{}, false
	}
	return id, true
}

// requireOrgMatch ensures the path org_id matches the caller's org, writing 403
// otherwise. Returns the org ID on success.
func requireOrgMatch(w http.ResponseWriter, r *http.Request, identity iam.Identity) (uuid.UUID, bool) {
	orgID, ok := parseUUID(w, r, "org_id")
	if !ok {
		return uuid.Nil, false
	}
	if orgID != identity.OrgID {
		httpjson.WriteError(w, domainerr.ErrForbidden)
		return uuid.Nil, false
	}
	return orgID, true
}

// parseUUID parses a path value as a UUID, writing 422 on failure.
func parseUUID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(key))
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return uuid.Nil, false
	}
	return id, true
}

// decodeJSON decodes the request body into v, writing 422 on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return false
	}
	return true
}

// verifyStackInOrg parses {stack_id} and confirms the stack belongs to the
// caller's org by loading it through the stack service. Writes the appropriate
// error and returns false if the stack is missing or out-of-org.
func verifyStackInOrg(w http.ResponseWriter, r *http.Request, stackSvc stack.StackService, identity iam.Identity) (uuid.UUID, bool) {
	stackID, ok := parseUUID(w, r, "stack_id")
	if !ok {
		return uuid.Nil, false
	}
	if _, err := stackSvc.Get(r.Context(), identity.OrgID, stackID); err != nil {
		httpjson.WriteError(w, err)
		return uuid.Nil, false
	}
	return stackID, true
}
