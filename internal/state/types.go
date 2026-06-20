// Package state implements the remote state bounded context: state version
// metadata and locking. The S3/object backend for state blobs arrives in Phase
// 3; Phase 1 provides version metadata queries and in-memory locks. It does not
// import the iam context; the caller's orgID is passed in for tenancy scoping.
package state

import (
	"context"
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// StateVersion is a recorded version of a stack's state file.
type StateVersion struct {
	ID         uuid.UUID `json:"id"`
	StackID    uuid.UUID `json:"stack_id"`
	Serial     int       `json:"serial"`
	SHA256     string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
	StorageURI string    `json:"storage_uri"`
	CreatedAt  time.Time `json:"created_at"`
}

// Lock represents a held state lock.
type Lock struct {
	LockID  string    `json:"lock_id"`
	Who     string    `json:"who"`
	Info    string    `json:"info"`
	Version string    `json:"version"`
	Created time.Time `json:"created"`
}

// LockRequest is the payload to acquire a lock.
type LockRequest struct {
	LockID  string    `json:"lock_id"`
	Who     string    `json:"who"`
	Info    string    `json:"info"`
	Version string    `json:"version"`
	Created time.Time `json:"created"`
}

// StateService is the boundary contract for the state context.
type StateService interface {
	GetState(ctx context.Context, orgID, stackID uuid.UUID) (*StateVersion, error)
	StoreState(ctx context.Context, stackID uuid.UUID, data []byte, sha256 string) (*StateVersion, error) // Phase 3
	ListVersions(ctx context.Context, orgID, stackID uuid.UUID) ([]*StateVersion, error)
	GetVersion(ctx context.Context, versionID uuid.UUID) ([]byte, error) // Phase 3

	AcquireLock(ctx context.Context, orgID, stackID uuid.UUID, lock LockRequest) error
	ReleaseLock(ctx context.Context, orgID, stackID uuid.UUID, lockID string) error
	GetLock(ctx context.Context, orgID, stackID uuid.UUID) (*Lock, error)
}

// Sentinel domain errors.
var (
	ErrStateNotFound  = domainerr.New("STATE_NOT_FOUND", 404, "no state found for stack")
	ErrLockHeld       = domainerr.New("STATE_LOCK_HELD", 409, "state is already locked")
	ErrLockNotHeld    = domainerr.New("STATE_LOCK_NOT_HELD", 409, "state is not locked")
	ErrLockMismatch   = domainerr.New("STATE_LOCK_MISMATCH", 409, "lock ID does not match the held lock")
)
