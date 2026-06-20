// Package middleware contains HTTP middleware shared across all API routes.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// ctxKey is an unexported type used to scope request-id context values so they
// cannot collide with keys from other packages.
type ctxKey struct{ name string }

var requestIDKey = ctxKey{"requestID"}

// RequestID ensures every request carries an X-Request-ID. If the client
// supplies one it is reused; otherwise a random 32-character hex ID is
// generated. The ID is stored in the request context and echoed back on the
// response so it can be correlated in downstream logs and clients.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithRequestID returns a copy of ctx that carries the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID stored in ctx, or the empty
// string if none is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// newID returns a random 32-character hex string. It panics only if the system
// CSPRNG is unavailable, which is a fatal environment error.
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("middleware: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
