package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Server wraps net/http.Server with sensible timeouts and structured startup
// and shutdown logging.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// New constructs a Server bound to addr and serving handler. Timeouts are set
// to protect against slowloris-style attacks and stuck connections.
func New(addr string, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
		logger: logger,
	}
}

// Start begins serving HTTP requests. It blocks until the server is shut down
// or a non-ErrServerClosed error occurs. ErrServerClosed (returned on graceful
// shutdown) is treated as a normal exit.
func (s *Server) Start() error {
	s.logger.Info("http server starting", "addr", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api.Server.Start: %w", err)
	}
	s.logger.Info("http server stopped")
	return nil
}

// Shutdown gracefully drains in-flight requests until ctx is cancelled. Callers
// should pass a context with a deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("http server shutting down")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("api.Server.Shutdown: %w", err)
	}
	return nil
}
