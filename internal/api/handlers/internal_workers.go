package handlers

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// InternalWorkerHandler exposes worker-facing internal API endpoints. All
// methods are stubs returning 501 in Phase 2; real implementations land in
// Phase 3 when the worker agent is built.
type InternalWorkerHandler struct{}

// NewInternalWorkerHandler returns a stub handler.
func NewInternalWorkerHandler() *InternalWorkerHandler {
	return &InternalWorkerHandler{}
}

var errNotImplemented = domainerr.New("NOT_IMPLEMENTED", 501, "not implemented in Phase 2 — coming in Phase 3")

// Register receives a worker registration request.
// Phase 3: validates pool token, creates worker record, returns worker_id.
func (h *InternalWorkerHandler) Register(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// GetJobs returns available jobs for a worker (long-poll).
// Phase 3: uses ClaimJob with SKIP LOCKED, blocks up to timeout param.
func (h *InternalWorkerHandler) GetJobs(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// Heartbeat handles worker liveness pings.
// Phase 3: updates last_heartbeat, returns cancel signal if applicable.
func (h *InternalWorkerHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// Deregister handles graceful worker shutdown.
// Phase 3: marks worker as deregistered, releases claim.
func (h *InternalWorkerHandler) Deregister(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// AppendEvent receives run events from workers.
// Phase 3: validates worker claim, appends via RunService.AppendEvent.
func (h *InternalWorkerHandler) AppendEvent(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// AppendLogs receives log chunks from workers.
// Phase 3: inserts log lines via RunService.AppendLogs.
func (h *InternalWorkerHandler) AppendLogs(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// GetSourceArchive returns a source code archive for a run.
// Phase 3: fetches git archive from VCS provider and streams it.
func (h *InternalWorkerHandler) GetSourceArchive(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}

// ClaimSecrets performs a one-time secret value claim for a worker.
// Phase 3: validates worker claim, returns decrypted secrets.
func (h *InternalWorkerHandler) ClaimSecrets(w http.ResponseWriter, r *http.Request) {
	httpjson.WriteError(w, errNotImplemented)
}
