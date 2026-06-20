// RBAC middleware: enforces that the authenticated Identity holds at least one
// of the required roles. Must run after the Auth middleware so an Identity is
// present on the context; otherwise the request is 401.
package middleware

import (
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	"github.com/yourorg/stratum/internal/iam"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// RequireRole returns middleware that allows the request only if the Identity
// on the context holds one of the given roles. Admins always pass.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := iam.IdentityFromContext(r.Context())
			if !ok {
				httpjson.WriteError(w, domainerr.ErrUnauthorized)
				return
			}
			if identity.HasRole(iam.RoleAdmin) {
				next.ServeHTTP(w, r)
				return
			}
			for _, role := range roles {
				if identity.HasRole(role) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpjson.WriteError(w, domainerr.ErrForbidden)
		})
	}
}

// RequireAdmin is shorthand for RequireRole(RoleAdmin).
func RequireAdmin() func(http.Handler) http.Handler {
	return RequireRole(iam.RoleAdmin)
}
