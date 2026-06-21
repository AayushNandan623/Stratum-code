// Package api contains the HTTP server, router, and middleware for the
// Stratum control plane. NewRouter builds the full route tree for Phase 3,
// applying auth and RBAC middleware to the appropriate endpoints.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/yourorg/stratum/internal/api/handlers"
	"github.com/yourorg/stratum/internal/api/middleware"
	"github.com/yourorg/stratum/internal/api/ws"
	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/policy"
	"github.com/yourorg/stratum/internal/reconcile"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
	"github.com/yourorg/stratum/internal/state"
	"github.com/yourorg/stratum/internal/vcs"
	"github.com/yourorg/stratum/internal/worker"
)

// Deps bundles the services the router needs to construct handlers.
type Deps struct {
	IAMSvc           iam.IAMService
	StackSvc         stack.StackService
	SecretSvc        secret.SecretService
	PolicySvc        policy.PolicyService
	ReconcileSvc     reconcile.ReconcileService
	StateSvc         state.StateService
	VCSSvc           vcs.VCSService
	RunSvc           run.RunService
	WorkerSvc        worker.WorkerService
	WorkerHMACSecret string // HMAC secret for worker token validation
	WsHub            *ws.NATSHub
	Logger           *slog.Logger
}

// NewRouter builds the HTTP handler tree for Phase 3. Every request is wrapped
// in the request-id middleware; authenticated routes additionally use the auth
// middleware, and mutating routes require the appropriate role.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	authH := handlers.NewAuthHandler(deps.IAMSvc)
	orgsH := handlers.NewOrgsHandler(deps.IAMSvc)
	stacksH := handlers.NewStacksHandler(deps.StackSvc)
	secretsH := handlers.NewSecretsHandler(deps.SecretSvc, deps.StackSvc)
	stateH := handlers.NewStateHandler(deps.StateSvc, deps.StackSvc)
	webhooksH := handlers.NewWebhooksHandler(deps.VCSSvc, deps.StackSvc, deps.Logger)
	runsH := handlers.NewRunsHandler(deps.RunSvc, deps.WsHub, deps.Logger)
	workersH := handlers.NewWorkersHandler(deps.WorkerSvc)
	internalWorkersH := handlers.NewInternalWorkerHandler(
		deps.WorkerSvc,
		deps.RunSvc,
		deps.SecretSvc,
		deps.StackSvc,
		deps.VCSSvc,
		deps.WorkerHMACSecret,
	)
	policiesH := handlers.NewPoliciesHandler(deps.PolicySvc, deps.StackSvc)
	reconcileH := handlers.NewReconcileHandler(deps.ReconcileSvc, deps.StackSvc)

	auth := middleware.Auth(deps.IAMSvc)
	reader := middleware.RequireRole(iam.RoleStackReader)
	writer := middleware.RequireRole(iam.RoleStackWriter)
	admin := middleware.RequireAdmin()

	// Worker internal auth middleware.
	workerAuth := middleware.WorkerAuth(deps.WorkerSvc, deps.WorkerHMACSecret)

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

	// Worker pools — org-scoped.
	mux.Handle("POST /api/v1/orgs/{org_id}/worker-pools", chain(workersH.CreatePool, auth, admin))
	mux.Handle("GET /api/v1/orgs/{org_id}/worker-pools", chain(workersH.ListPools, auth, reader))

	// Worker pools — by pool id.
	mux.Handle("GET /api/v1/worker-pools/{pool_id}", chain(workersH.GetPool, auth, reader))
	mux.Handle("DELETE /api/v1/worker-pools/{pool_id}", chain(workersH.DeletePool, auth, writer))
	mux.Handle("POST /api/v1/worker-pools/{pool_id}/rotate-token", chain(workersH.RotatePoolToken, auth, admin))
	mux.Handle("GET /api/v1/worker-pools/{pool_id}/workers", chain(workersH.ListActiveWorkers, auth, reader))

	// Stacks — org-scoped.
	mux.Handle("POST /api/v1/orgs/{org_id}/stacks", chain(stacksH.Create, auth, writer))
	mux.Handle("GET /api/v1/orgs/{org_id}/stacks", chain(stacksH.List, auth, reader))

	// Stacks — by stack id.
	mux.Handle("GET /api/v1/stacks/{stack_id}", chain(stacksH.Get, auth, reader))
	mux.Handle("PATCH /api/v1/stacks/{stack_id}", chain(stacksH.Update, auth, writer))
	mux.Handle("DELETE /api/v1/stacks/{stack_id}", chain(stacksH.Delete, auth, writer))

	// Reconcile / drift detection — stack-scoped.
	mux.Handle("GET /api/v1/stacks/{stack_id}/reconcile", chain(reconcileH.GetSchedule, auth, reader))
	mux.Handle("PATCH /api/v1/stacks/{stack_id}/reconcile", chain(reconcileH.UpdateSchedule, auth, writer))
	mux.Handle("POST /api/v1/stacks/{stack_id}/reconcile/trigger", chain(reconcileH.TriggerNow, auth, writer))

	// Drift records — stack-scoped.
	mux.Handle("GET /api/v1/stacks/{stack_id}/drift", chain(reconcileH.ListDriftRecords, auth, reader))
	mux.Handle("GET /api/v1/orgs/{org_id}/drift", chain(reconcileH.ListDriftRecords, auth, reader))

	// Drift records — by id.
	mux.Handle("GET /api/v1/drift/{drift_id}", chain(reconcileH.GetDriftRecord, auth, reader))
	mux.Handle("POST /api/v1/drift/{drift_id}/ignore", chain(reconcileH.IgnoreDrift, auth, writer))

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

	// Runs — stack-scoped.
	mux.Handle("POST /api/v1/stacks/{stack_id}/runs", chain(runsH.Create, auth, writer))
	mux.Handle("GET /api/v1/stacks/{stack_id}/runs", chain(runsH.List, auth, reader))

	// Runs — by run id.
	mux.Handle("GET /api/v1/runs/{run_id}", chain(runsH.Get, auth, reader))
	mux.Handle("POST /api/v1/runs/{run_id}/cancel", chain(runsH.Cancel, auth, writer))
	mux.Handle("POST /api/v1/runs/{run_id}/approve", chain(runsH.Approve, auth, writer))
	mux.Handle("POST /api/v1/runs/{run_id}/discard", chain(runsH.Discard, auth, writer))

	// Run timeline and logs.
	mux.Handle("GET /api/v1/runs/{run_id}/timeline", chain(runsH.GetTimeline, auth, reader))
	mux.Handle("GET /api/v1/runs/{run_id}/logs", chain(runsH.GetLogs, auth, reader))

	// Run event stream (WebSocket).
	mux.Handle("GET /api/v1/runs/{run_id}/events/stream", chain(runsH.EventStream, auth))

	// Policy management — org-scoped.
	mux.Handle("POST /api/v1/orgs/{org_id}/policies", chain(policiesH.Create, auth, writer))
	mux.Handle("GET /api/v1/orgs/{org_id}/policies", chain(policiesH.List, auth, reader))
	mux.Handle("POST /api/v1/orgs/{org_id}/policies/dry-run", chain(policiesH.DryRun, auth, writer))

	// Policy — by policy id.
	mux.Handle("GET /api/v1/policies/{policy_id}", chain(policiesH.Get, auth, reader))
	mux.Handle("PATCH /api/v1/policies/{policy_id}", chain(policiesH.Update, auth, writer))
	mux.Handle("PUT /api/v1/policies/{policy_id}/source", chain(policiesH.UpdateSource, auth, writer))
	mux.Handle("DELETE /api/v1/policies/{policy_id}", chain(policiesH.Delete, auth, writer))

	// Policy sets — org-scoped.
	mux.Handle("POST /api/v1/orgs/{org_id}/policy-sets", chain(policiesH.CreatePolicySet, auth, writer))

	// Policy sets — by set id.
	mux.Handle("POST /api/v1/policy-sets/{set_id}/members", chain(policiesH.AddToSet, auth, writer))
	mux.Handle("DELETE /api/v1/policy-sets/{set_id}/members/{policy_id}", chain(policiesH.RemoveFromSet, auth, writer))
	mux.Handle("POST /api/v1/policy-sets/{set_id}/bindings", chain(policiesH.BindSet, auth, writer))
	mux.Handle("DELETE /api/v1/policy-sets/{set_id}/bindings/{binding_id}", chain(policiesH.UnbindSet, auth, writer))

	// Internal worker API — register uses pool token auth (no worker record yet);
	// all other internal endpoints use worker token auth.
	mux.Handle("POST /api/v1/internal/workers/register", chain(internalWorkersH.Register))
	mux.Handle("GET /api/v1/internal/workers/{id}/jobs", chain(internalWorkersH.GetJobs, workerAuth))
	mux.Handle("POST /api/v1/internal/workers/{id}/heartbeat", chain(internalWorkersH.Heartbeat, workerAuth))
	mux.Handle("DELETE /api/v1/internal/workers/{id}", chain(internalWorkersH.Deregister, workerAuth))
	mux.Handle("POST /api/v1/internal/runs/{id}/events", chain(internalWorkersH.AppendEvent, workerAuth))
	mux.Handle("POST /api/v1/internal/runs/{id}/logs", chain(internalWorkersH.AppendLogs, workerAuth))
	mux.Handle("GET /api/v1/internal/runs/{id}/source-archive", chain(internalWorkersH.GetSourceArchive, workerAuth))
	mux.Handle("POST /api/v1/internal/runs/{id}/secrets/claim", chain(internalWorkersH.ClaimSecrets, workerAuth))

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
