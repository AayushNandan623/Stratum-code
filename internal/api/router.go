// Package api contains the HTTP server, router, and middleware for the
// Stratum control plane. Phase 0 wires only the health endpoint; resource
// handlers are added in later phases.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/yourorg/stratum/internal/api/middleware"
)

// NewRouter builds the HTTP handler tree. It registers the request-id
// middleware around all routes so every request is correlatable. Phase 0
// exposes only GET /healthz.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	return middleware.RequestID(mux)
}

// healthHandler reports liveness. Phase 0 returns a static ok response and
// does not probe dependencies; deeper readiness checks are added with the
// bounded contexts in later phases.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
