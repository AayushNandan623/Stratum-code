// Package httpjson provides shared JSON response helpers used by both the
// middleware and handler layers. It lives outside the handlers package so the
// middleware can write structured errors without importing handlers (which
// would create an import cycle).
package httpjson

import (
	"encoding/json"
	"errors"
	"net/http"

	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// ErrorBody is the machine-readable error detail returned to clients.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse wraps an error body under an "error" key.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// WriteJSON encodes v as JSON with the given status and content type.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError maps an error to an HTTP response. DomainErrors surface their
// declared status and code; anything else becomes a 500.
func WriteError(w http.ResponseWriter, err error) {
	var de *domainerr.DomainError
	if errors.As(err, &de) {
		WriteJSON(w, de.HTTPStatus, ErrorResponse{Error: ErrorBody{Code: de.Code, Message: de.Message}})
		return
	}
	WriteJSON(w, http.StatusInternalServerError, ErrorResponse{Error: ErrorBody{Code: "INTERNAL", Message: "internal error"}})
}
