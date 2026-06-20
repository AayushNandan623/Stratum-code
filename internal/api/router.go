// Package api contains the HTTP server, router, and middleware for the
// Stratum control plane. NewRouter builds the full route tree for Phase 1,
// applying auth and RBAC middleware to the appropriate endpoints.
package api

import (
	"encoding/json"
	"net/http"
	"log/slog"

	"github.com/yourorg/stratum/internal/api/handlers"
	"github.com/yourorg/stratum/internal/api/middleware"
	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
	"github.com/yourorg/stratum/internal/state"
	"github.com/yourorg/stratum/internal/vcs"
)

// Deps bundles the services the router needs to construct handlers.
type Deps struct {
	IAMSvc    iam.IAMService
	StackSvc  stack.StackService
	SecretSvc secret.SecretService
	StateSvc  state.StateService
	VCSSvc    vcs.VCSService
	Logger    *slog.Logger
}

// NewRouter builds the HTTP handler tree for Phase 1. Every request is wrapped
// in the request-id middleware; authenticated routes additionally use the auth
// middleware, and mutating routes require the appropriate role.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	orgsH := handlers.NewOrgsHandler(deps.IAMSvc)
	authH := handlers.NewAuthHandler(deps.IAMSvc)
	stacksH := handlers.NewStacksHandler(deps.StackSvc)
	secretsH := handlers.NewSecretsHandler(deps.SecretSvc, deps.StackSvc)
	stateH := handlers.NewStateHandler(deps.StateSvc, deps.StackSvc)
	webhooksH := handlers.NewWebhooksHandler(deps.VCSSvc, deps.StackSvc, deps.Logger)

	auth := middleware.Auth(deps.IAMSvc)
	reader := middleware.RequireRole(iam.RoleStackReader)
	writer := middleware.RequireRole(iam.RoleStackWriter)
	admin := middleware.RequireAdmin()

	// Health.
	mux.HandleFunc("GET /healthz", healthHandler)

	// Public: org bootstrap, auth login/refresh, webhooks.
	mux.Handle("POST /api/v1/orgs", h(orgsH.Create))
	mux.Handle("POST /api/v1/auth/login", h(authH.Login))
	mux.Handle("POST /api/v1/auth/refresh", h(authH.Refresh))
	mux.Handle("POST /api/v1/webhooks/github", h(webhooksH.GitHub))
	mux.Handle("POST /api/v1/webhooks/gitlab", h(webhooksH.GitLab))

	// Authenticated: org read, logout.
	mux.Handle("GET /api/v1/orgs/{org_id}", chain(orgsH.Get, auth))
	mux.Handle("POST /api/v1/auth/logout", chain(authH.Logout, auth))

	// API key management (admin only).
	mux.Handle("POST /api/v1/orgs/{org_id}/api-keys", chain(authH.CreateAPIKey, auth, admin))
	mux.Handle("DELETE /api/v1/orgs/{org_id}/api-keys/{id}", chain(authH.RevokeAPIKey, auth, admin))

	// Stacks — org-scoped.
	mux.Handle("POST /api/v1/orgs/{org_id}/stacks", chain(stacksH.Create, auth, writer))
	mux.Handle("GET /api/v1/orgs/{org_id}/stacks", chain(stacksH.List, auth, reader))

	// Stacks — by stack id.
	mux.Handle("GET /api/v1/stacks/{stack_id}", chain(stacksH.Get, auth, reader))
	mux.Handle("PATCH /api/v1/stacks/{stack_id}", chain(stacksH.Update, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}", chain(stacksH.Delete, auth, writer))

	// Stack dependencies.
	mux.Handle("POST /api/v1/stacks/{stack_id}/dependencies", chain(stacksH.AddDependency, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}/dependencies/{dep_id}", chain(stacksH.RemoveDependency, auth, writer))
	mux.Handle("GET /api/v1/stacks/{stack_id}/dependencies", chain(stacksH.GetDependencies, auth, reader))

	// Stack variables.
	mux.Handle("PUT /api/v1/stacks/{stack_id}/variables/{key}", chain(stacksH.SetVariable, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}/variables/{key}", chain(stacksH.DeleteVariable, auth, writer))
	mux.Handle("GET /api/v1/stacks/{stack_id}/variables", chain(stacksH.ListVariables, auth, reader))

	// Secrets.
	mux.Handle("PUT /api/v1/stacks/{stack_id}/secrets/{name}", chain(secretsH.Set, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}/secrets/{name}", chain(secretsH.Delete, auth, writer))
	mux.Handle("GET /api/v1/stacks/{stack_id}/secrets", chain(secretsH.List, auth, reader))

	// State.
	mux.Handle("GET /api/v1/stacks/{stack_id}/state", chain(stateH.Get, auth, reader))
	mux.Handle("GET /api/v1/stacks/{stack_id}/state/versions", chain(stateH.ListVersions, auth, reader))
	mux.Handle("POST /api/v1/stacks/{stack_id}/state/lock", chain(stateH.AcquireLock, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}/state/lock", chain(stateH.ReleaseLock, auth, writer))

	return middleware.RequestID(mux)
}

// h adapts a handler function to an http.Handler.
func h(fn func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(fn)
}

// chain wraps a handler in the given middleware, applying them right-to-left so
// the first listed runs outermost.
func chain(fn func(http.ResponseWriter, *http.Request), mws ...func(http.Handler) http.Handler) http.Handler {
	var handler http.Handler = http.HandlerFunc(fn)
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}
	return handler
}

// healthHandler reports liveness.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
