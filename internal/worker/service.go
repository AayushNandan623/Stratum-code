package worker

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	runpkg "github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
)

// WorkerService is the boundary contract for the worker context.
type WorkerService interface {
	// Pool management
	CreatePool(ctx context.Context, input CreatePoolInput) (*WorkerPool, string, error)
	GetPool(ctx context.Context, id uuid.UUID) (*WorkerPool, error)
	ListPools(ctx context.Context, orgID uuid.UUID) ([]*WorkerPool, error)
	DeletePool(ctx context.Context, id uuid.UUID) error
	RotatePoolToken(ctx context.Context, id uuid.UUID) (string, error)
	GetPoolByTokenHash(ctx context.Context, tokenHash string) (*WorkerPool, error)

	// Worker lifecycle
	Register(ctx context.Context, input RegisterWorkerInput) (*Worker, error)
	Heartbeat(ctx context.Context, id uuid.UUID, status WorkerStatus, currentRunID *uuid.UUID) (*HeartbeatResponse, error)
	Deregister(ctx context.Context, id uuid.UUID) error
	GetByTokenHash(ctx context.Context, hash string) (*Worker, error)

	// Job dispatch
	ClaimJob(ctx context.Context, workerID uuid.UUID, timeout time.Duration) (*Job, error)
	CompleteJob(ctx context.Context, jobID uuid.UUID, success bool) error
	ListActiveWorkers(ctx context.Context, poolID uuid.UUID) ([]*Worker, error)
}

type service struct {
	repo          *Repository
	db            *db.DB
	runRepo       *runpkg.Repository
	runSvc        runpkg.RunService
	stackSvc      stack.StackService
	secretSvc     secret.SecretService
	hmacSecret    string
	logger        *slog.Logger
}

// NewService constructs a WorkerService.
func NewService(database *db.DB, runSvc runpkg.RunService, stackSvc stack.StackService, secretSvc secret.SecretService, hmacSecret string, logger *slog.Logger) WorkerService {
	return &service{
		repo:       NewRepository(),
		db:         database,
		runRepo:    runpkg.NewRepository(),
		runSvc:     runSvc,
		stackSvc:   stackSvc,
		secretSvc:  secretSvc,
		hmacSecret: hmacSecret,
		logger:     logger,
	}
}

var _ WorkerService = (*service)(nil)

// ─── Pool management ────────────────────────────────────────────────────────

func (s *service) CreatePool(ctx context.Context, input CreatePoolInput) (*WorkerPool, string, error) {
	if input.OrgID == uuid.Nil || input.Name == "" {
		return nil, "", domainerr.ErrValidation
	}
	if input.PoolType == "" {
		input.PoolType = PoolTypePrivate
	}
	if input.MaxConcurrency <= 0 {
		input.MaxConcurrency = 5
	}

	// Check uniqueness
	existing, err := s.repo.PoolByName(ctx, s.db.Pool, input.OrgID, input.Name)
	if err != nil && !errors.Is(err, ErrPoolNotFound) {
		return nil, "", err
	}
	if existing != nil {
		return nil, "", ErrPoolNameExists
	}

	rawToken, tokenHash, err := generateToken(s.hmacSecret)
	if err != nil {
		return nil, "", err
	}

	pool := &WorkerPool{
		ID:             uuid.New(),
		OrgID:          input.OrgID,
		Name:           input.Name,
		PoolType:       input.PoolType,
		TokenHash:      tokenHash,
		MaxConcurrency: input.MaxConcurrency,
		Labels:         input.Labels,
		CreatedAt:      time.Now(),
	}
	if pool.Labels == nil {
		pool.Labels = []byte("{}")
	}
	if err := s.repo.CreatePool(ctx, s.db.Pool, pool); err != nil {
		return nil, "", err
	}
	return pool, rawToken, nil
}

func (s *service) GetPool(ctx context.Context, id uuid.UUID) (*WorkerPool, error) {
	return s.repo.GetPool(ctx, s.db.Pool, id)
}

func (s *service) ListPools(ctx context.Context, orgID uuid.UUID) ([]*WorkerPool, error) {
	return s.repo.ListPools(ctx, s.db.Pool, orgID)
}

func (s *service) DeletePool(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeletePool(ctx, s.db.Pool, id)
}

func (s *service) RotatePoolToken(ctx context.Context, id uuid.UUID) (string, error) {
	rawToken, tokenHash, err := generateToken(s.hmacSecret)
	if err != nil {
		return "", err
	}
	if err := s.repo.UpdatePoolToken(ctx, s.db.Pool, id, tokenHash); err != nil {
		return "", err
	}
	return rawToken, nil
}

func (s *service) GetPoolByTokenHash(ctx context.Context, tokenHash string) (*WorkerPool, error) {
	return s.repo.PoolByTokenHash(ctx, s.db.Pool, tokenHash)
}

// ─── Worker lifecycle ───────────────────────────────────────────────────────

func (s *service) Register(ctx context.Context, input RegisterWorkerInput) (*Worker, error) {
	if input.OrgID == uuid.Nil || input.PoolID == uuid.Nil || input.TokenHash == "" {
		return nil, domainerr.ErrValidation
	}
	now := time.Now()

	// Check if a worker with this token_hash already exists. This handles the
	// re-registration case — after a crash or restart the worker connects with
	// the same pool token, so we re-activate the previous record instead of
	// INSERTing a duplicate (token_hash has a UNIQUE index).
	existing, err := s.repo.GetWorkerByTokenHash(ctx, s.db.Pool, input.TokenHash)
	if err != nil && !errors.Is(err, ErrWorkerNotFound) {
		return nil, err
	}
	if existing != nil {
		if existing.Status == StatusDeregistered {
			// Re-activate the deregistered worker.
			existing.Status = StatusIDLE
			existing.Hostname = input.Hostname
			existing.Version = input.Version
			existing.Capabilities = input.Capabilities
			existing.LastHeartbeat = &now
			existing.CurrentRunID = nil
			// Update in the database — reset status, refresh metadata, clear run.
			err := s.repo.ReactivateWorker(ctx, s.db.Pool, existing)
			if err != nil {
				return nil, err
			}
			s.logger.Info("worker re-registered (reactivated)",
				"worker_id", existing.ID, "pool_id", input.PoolID)
			return existing, nil
		}
		// Worker is still active — another instance is using this token.
		return nil, domainerr.New("WORKER_ALREADY_REGISTERED", 409,
			"a worker with this token is already registered and active")
	}

	w := &Worker{
		ID:             uuid.New(),
		PoolID:         input.PoolID,
		OrgID:          input.OrgID,
		Hostname:       input.Hostname,
		Version:        input.Version,
		Capabilities:   input.Capabilities,
		Status:         StatusIDLE,
		TokenHash:      input.TokenHash,
		LastHeartbeat:  &now,
		CurrentRunID:   nil,
		RegisteredAt:   now,
	}
	if err := s.repo.CreateWorker(ctx, s.db.Pool, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *service) Heartbeat(ctx context.Context, id uuid.UUID, status WorkerStatus, currentRunID *uuid.UUID) (*HeartbeatResponse, error) {
	if err := s.repo.UpdateWorkerHeartbeat(ctx, s.db.Pool, id, status, currentRunID); err != nil {
		return nil, err
	}
	// Check if there's a pending cancellation for the worker's current run.
	resp := &HeartbeatResponse{}
	if currentRunID != nil {
		run, err := s.runRepo.GetByID(ctx, s.db.Pool, *currentRunID)
		if err == nil && run.CurrentState == runpkg.StateCancelled {
			resp.CancelRunID = &run.ID
		}
	}
	return resp, nil
}

func (s *service) Deregister(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeregisterWorker(ctx, s.db.Pool, id)
}

func (s *service) GetByTokenHash(ctx context.Context, hash string) (*Worker, error) {
	return s.repo.GetWorkerByTokenHash(ctx, s.db.Pool, hash)
}

// ─── Job dispatch ───────────────────────────────────────────────────────────

func (s *service) ClaimJob(ctx context.Context, workerID uuid.UUID, timeout time.Duration) (*Job, error) {
	// The timeout parameter from the handler is the long-poll wait duration.
	// The job expiry must be independent — use a fixed 5-minute window so the
	// worker has time to execute without the job being prematurely re-queued.
	jobExpiry := 5 * time.Minute

	// Look up the worker to get pool_id for scoped job claiming.
	worker, err := s.repo.GetWorkerByID(ctx, s.db.Pool, workerID)
	if err != nil {
		s.logger.Error("claim job: worker lookup", "worker_id", workerID, "error", err)
		return nil, err
	}

	var job *Job
	err = s.db.InTx(ctx, func(q db.DBTX) error {
		runJob, err := s.runRepo.ClaimJob(ctx, q, workerID, jobExpiry, &worker.PoolID)
		if err != nil {
			return err
		}
		// Fetch run details to construct the Job.
		run, err := s.runRepo.GetByIDTx(ctx, q, runJob.RunID)
		if err != nil {
			return err
		}
		// Transition run to ASSIGNED.
		if err := s.runSvc.Transition(ctx, runJob.RunID, runpkg.StateAssigned, nil); err != nil {
			return err
		}
		job = &Job{
			ID:         runJob.ID,
			RunID:      runJob.RunID,
			PoolID:     uuid.Nil,
			RunType:    string(run.RunType),
			StackID:    run.StackID,
			IACTool:    runJob.ID.String(), // set from stack below (outside txn)
			IACVersion: runJob.ID.String(), // set from stack below (outside txn)
			OrgID:      run.OrgID,
			Status:     runJob.Status,
			Attempt:    runJob.Attempt,
			CreatedAt:  runJob.CreatedAt,
		}
		// Fetch stack to populate IACTool and IACVersion.
		stk, stackErr := s.stackSvc.Get(ctx, run.OrgID, run.StackID)
		if stackErr == nil {
			job.IACTool = stk.IACTool
			job.IACVersion = stk.IACVersion
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *service) CompleteJob(ctx context.Context, jobID uuid.UUID, success bool) error {
	return nil // Phase 4: job completion tracking
}

func (s *service) ListActiveWorkers(ctx context.Context, poolID uuid.UUID) ([]*Worker, error) {
	return s.repo.ListActiveWorkers(ctx, s.db.Pool, poolID)
}

// ─── Token helpers ──────────────────────────────────────────────────────────

// generateToken creates a random token prefixed with "wpt_" and its HMAC-SHA256 hash.
func generateToken(hmacSecret string) (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	rawToken := "wpt_" + hex.EncodeToString(buf)
	tokenHash := hashToken(rawToken, hmacSecret)
	return rawToken, tokenHash, nil
}

// HashToken returns the HMAC-SHA256 hash of the token.
func HashToken(token, hmacSecret string) string {
	return hashToken(token, hmacSecret)
}

func hashToken(token, hmacSecret string) string {
	mac := hmac.New(sha256.New, []byte(hmacSecret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
