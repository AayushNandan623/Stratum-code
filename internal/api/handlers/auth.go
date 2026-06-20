// Auth handlers: login, refresh, logout, and API key CRUD.
package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/iam"
)

// AuthHandler exposes authentication and API key endpoints.
type AuthHandler struct {
	svc iam.IAMService
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(svc iam.IAMService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, session)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	session, err := h.svc.RefreshSession(r.Context(), req.RefreshToken)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, session)
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout handles POST /api/v1/auth/logout.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	_ = h.svc.Logout(r.Context(), req.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

type createAPIKeyRequest struct {
	Name      string     `json:"name"`
	Roles     []string   `json:"roles"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type createAPIKeyResponse struct {
	APIKey *iam.APIKey `json:"api_key"`
	Key    string      `json:"key"`
}

// CreateAPIKey handles POST /api/v1/orgs/{org_id}/api-keys. The plaintext key
// is returned exactly once.
func (h *AuthHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	orgID, ok := requireOrgMatch(w, r, identity)
	if !ok {
		return
	}
	var req createAPIKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var userID *uuid.UUID
	if identity.Type == iam.IdentityUser {
		uid := identity.ID
		userID = &uid
	}
	key, raw, err := h.svc.CreateAPIKey(r.Context(), iam.CreateAPIKeyInput{
		OrgID:     orgID,
		UserID:    userID,
		Name:      req.Name,
		Roles:     req.Roles,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusCreated, createAPIKeyResponse{APIKey: key, Key: raw})
}

// RevokeAPIKey handles DELETE /api/v1/orgs/{org_id}/api-keys/{id}.
func (h *AuthHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	if _, ok = requireOrgMatch(w, r, identity); !ok {
		return
	}
	keyID, ok := parseUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.RevokeAPIKey(r.Context(), keyID); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
