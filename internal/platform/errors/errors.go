// Package errors defines the shared DomainError type used across all bounded
// contexts. Domain errors carry a stable machine-readable Code and the HTTP
// status that should be surfaced to API clients.
//
// Bounded contexts define their own sentinel errors (e.g. ErrStackNotFound) in
// later phases by wrapping or instantiating DomainError. This package only
// holds the type and a small set of generic platform-level codes.
package errors

import "fmt"

// DomainError is the canonical error type for all domain-level failures. It
// implements error and carries enough metadata for the API layer to map it to
// an HTTP response without type-switching on every context's errors.
type DomainError struct {
	// Code is a stable, machine-readable error code (e.g. "STACK_NOT_FOUND").
	Code string
	// Message is a human-readable description of the failure.
	Message string
	// HTTPStatus is the HTTP status code to return to the client.
	HTTPStatus int
}

// Error implements the error interface.
func (e *DomainError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New constructs a DomainError with the given code, HTTP status, and message.
func New(code string, httpStatus int, message string) *DomainError {
	return &DomainError{Code: code, Message: message, HTTPStatus: httpStatus}
}

// Generic platform-level error codes. Bounded contexts declare their own
// specific sentinels in later phases; these cover the cross-cutting cases.
var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = New("NOT_FOUND", 404, "resource not found")
	// ErrConflict is returned on concurrent modification or duplicate creation.
	ErrConflict = New("CONFLICT", 409, "resource conflict")
	// ErrUnauthorized is returned when authentication is missing or invalid.
	ErrUnauthorized = New("UNAUTHORIZED", 401, "unauthorized")
	// ErrForbidden is returned when the caller lacks permission.
	ErrForbidden = New("FORBIDDEN", 403, "forbidden")
	// ErrValidation is returned when input fails validation.
	ErrValidation = New("VALIDATION", 422, "validation error")
	// ErrInternal is returned for unexpected internal failures.
	ErrInternal = New("INTERNAL", 500, "internal error")
)
