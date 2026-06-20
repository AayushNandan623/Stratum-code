// State service: state version metadata queries and in-memory locking. The
// object-storage backend for state blobs arrives in Phase 3, so StoreState and
// GetVersion are stubs until then.
package state

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

type service struct {
	repo  *Repository
	db    *db.DB
	locks sync.Map // map[uuid.UUID]Lock
}

// NewService constructs a StateService backed by database.
func NewService(database *db.DB) StateService {
	return &service{repo: NewRepository(), db: database}
}

var _ StateService = (*service)(nil)

func (s *service) GetState(ctx context.Context, orgID, stackID uuid.UUID) (*StateVersion, error) {
	return s.repo.GetLatest(ctx, s.db.Pool, orgID, stackID)
}

func (s *service) ListVersions(ctx context.Context, orgID, stackID uuid.UUID) ([]*StateVersion, error) {
	return s.repo.ListVersions(ctx, s.db.Pool, orgID, stackID)
}

// StoreState is a stub until the object-storage backend lands in Phase 3.
func (s *service) StoreState(_ context.Context, _ uuid.UUID, _ []byte, _ string) (*StateVersion, error) {
	return nil, nil
}

// GetVersion is a stub until the object-storage backend lands in Phase 3.
func (s *service) GetVersion(_ context.Context, _ uuid.UUID) ([]byte, error) {
	return nil, nil
}

// AcquireLock takes the state lock for a stack. Returns ErrLockHeld if already
// held.
func (s *service) AcquireLock(_ context.Context, _, stackID uuid.UUID, req LockRequest) error {
	if req.LockID == "" {
		return domainerr.ErrValidation
	}
	lock := Lock{
		LockID:  req.LockID,
		Who:     req.Who,
		Info:    req.Info,
		Version: req.Version,
		Created: req.Created,
	}
	if lock.Created.IsZero() {
		lock.Created = time.Now()
	}
	if _, loaded := s.locks.LoadOrStore(stackID, lock); loaded {
		return ErrLockHeld
	}
	return nil
}

// ReleaseLock frees the state lock. The lock ID must match the held lock.
func (s *service) ReleaseLock(_ context.Context, _, stackID uuid.UUID, lockID string) error {
	val, ok := s.locks.Load(stackID)
	if !ok {
		return ErrLockNotHeld
	}
	held := val.(Lock)
	if held.LockID != lockID {
		return ErrLockMismatch
	}
	s.locks.Delete(stackID)
	return nil
}

// GetLock returns the held lock, or nil if the stack is unlocked.
func (s *service) GetLock(_ context.Context, _, stackID uuid.UUID) (*Lock, error) {
	val, ok := s.locks.Load(stackID)
	if !ok {
		return nil, nil
	}
	lock := val.(Lock)
	return &lock, nil
}
