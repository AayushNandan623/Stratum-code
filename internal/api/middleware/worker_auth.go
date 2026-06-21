// Worker auth middleware: validates bearer tokens against worker records via
// HMAC-SHA256 lookup. Used exclusively on /api/v1/internal/* routes.
package middleware

import (
	"context"
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/worker"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

type workerCtxKey struct{}

// WorkerFromContext extracts the authenticated worker from the context.
func WorkerFromContext(ctx context.Context) *worker.Worker {
	w, _ := ctx.Value(workerCtxKey{}).(*worker.Worker)
	return w
}

// WorkerAuth returns middleware that authenticates requests via worker bearer
// tokens. The Worker is stored on the context for downstream handlers.
func WorkerAuth(workerSvc worker.WorkerService, hmacSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				httpjson.WriteError(w, domainerr.ErrUnauthorized)
				return
			}
			tokenHash := worker.HashToken(token, hmacSecret)
			wkr, err := workerSvc.GetByTokenHash(r.Context(), tokenHash)
			if err != nil {
				httpjson.WriteError(w, domainerr.ErrUnauthorized)
				return
			}
			if wkr.Status == worker.StatusDeregistered {
				httpjson.WriteError(w, domainerr.ErrForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), workerCtxKey{}, wkr)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
