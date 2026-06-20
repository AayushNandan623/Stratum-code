// Auth middleware: extracts the bearer credential from the Authorization
// header, validates it as either an API key (stratum_ prefix) or a JWT, and
// places the resulting Identity on the request context. Requests without a
// valid credential receive 401.
package middleware

import (
	"net/http"
	"strings"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/iam"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// Auth returns middleware that authenticates requests via the IAM service. The
// Identity is stored on the context for downstream handlers and the RBAC
// middleware.
func Auth(svc iam.IAMService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				httpjson.WriteError(w, domainerr.ErrUnauthorized)
				return
			}
			var (
				identity *iam.Identity
				err      error
			)
			if strings.HasPrefix(token, "stratum_") {
				identity, err = svc.ValidateAPIKey(r.Context(), token)
			} else {
				identity, err = svc.ValidateJWT(r.Context(), token)
			}
			if err != nil {
				httpjson.WriteError(w, domainerr.ErrUnauthorized)
				return
			}
			ctx := iam.WithIdentity(r.Context(), *identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearer pulls the token from an "Authorization: Bearer <token>" header.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
