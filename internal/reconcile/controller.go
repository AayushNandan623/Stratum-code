package reconcile

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/stack"
)

// Controller is a goroutine pool that processes due reconcile schedules. Each
// worker claims one stack at a time using SKIP LOCKED and creates drift-detect
// runs for stacks that are due.
type Controller struct {
	repo     *Repository
	runSvc   run.RunService
	stackSvc stack.StackService
	db       *db.DB
	poolSize int
	logger   *slog.Logger
}

// NewController creates a reconcile controller with the given pool size.
// Default pool size is 5 if poolSize <= 0.
func NewController(database *db.DB, runSvc run.RunService, stackSvc stack.StackService, poolSize int, logger *slog.Logger) *Controller {
	if poolSize <= 0 {
		poolSize = 5
	}
	return &Controller{
		repo:     NewRepository(),
		runSvc:   runSvc,
		stackSvc: stackSvc,
		db:       database,
		poolSize: poolSize,
		logger:   logger,
	}
}

// Start begins the controller goroutine pool. It blocks until ctx is cancelled
// and all workers have exited.
func (c *Controller) Start(ctx context.Context) {
	c.logger.Info("reconcile controller starting", "pool_size", c.poolSize)
	var wg sync.WaitGroup
	for i := 0; i < c.poolSize; i++ {
		wg.Add(1)
		go func(workerN int) {
			defer wg.Done()
			c.workerLoop(ctx, workerN)
		}(i)
	}
	wg.Wait()
	c.logger.Info("reconcile controller stopped")
}

// workerLoop is the main loop for a single worker goroutine.
func (c *Controller) workerLoop(ctx context.Context, n int) {
	backoff := 10 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		schedule, err := c.repo.ClaimNextDue(ctx, c.db.Pool)
		if err != nil {
			c.logger.Error("reconcile claim error", "worker", n, "err", err)
			sleepWithJitter(ctx, backoff)
			continue
		}
		if schedule == nil {
			// Nothing due — sleep and retry with jitter
			sleepWithJitter(ctx, backoff)
			continue
		}

		backoff = 10 * time.Second // reset backoff on successful claim
		c.processStack(ctx, schedule)
	}
}

// processStack handles a single due reconcile schedule.
func (c *Controller) processStack(ctx context.Context, schedule *ReconcileSchedule) {
	log := c.logger.With("stack_id", schedule.StackID)

	// 1. Check stack is still active.
	stk, err := c.stackSvc.Get(ctx, schedule.OrgID, schedule.StackID)
	if err != nil {
		log.Error("reconcile: get stack", "err", err)
		c.recordFailure(ctx, schedule.StackID)
		return
	}
	if stk.DeletedAt != nil {
		log.Info("reconcile: stack deleted, disabling schedule")
		c.repo.DisableSchedule(ctx, c.db.Pool, schedule.StackID)
		return
	}

	// 2. Skip if there's already an active run for this stack.
	active, err := c.runSvc.HasActiveRun(ctx, schedule.StackID)
	if err != nil {
		log.Error("reconcile: check active run", "err", err)
		c.recordFailure(ctx, schedule.StackID)
		return
	}
	if active {
		log.Debug("reconcile: active run in progress, skipping")
		return
	}

	// 3. Create drift-detect run.
	run, err := c.runSvc.Create(ctx, run.CreateRunInput{
		OrgID:       schedule.OrgID,
		StackID:     schedule.StackID,
		SpaceID:     stk.SpaceID,
		RunType:     run.RunTypeDriftDetect,
		TriggerType: run.TriggerSchedule,
		TriggeredBy: nil, // system-initiated
	})
	if err != nil {
		log.Error("reconcile: failed to create drift-detect run", "err", err)
		c.recordFailure(ctx, schedule.StackID)
		return
	}

	log.Info("reconcile: drift-detect run created", "run_id", run.ID)
	c.repo.ResetFailures(ctx, c.db.Pool, schedule.StackID)
}

func (c *Controller) recordFailure(ctx context.Context, stackID uuid.UUID) {
	if err := c.repo.RecordFailure(ctx, c.db.Pool, stackID); err != nil {
		c.logger.Error("reconcile: record failure", "stack_id", stackID, "err", err)
	}
}

// sleepWithJitter sleeps for the given duration plus up to 50% random jitter.
// Returns immediately if ctx is cancelled.
func sleepWithJitter(ctx context.Context, d time.Duration) {
	jitter := time.Duration(rand.Float64() * float64(d) * 0.5)
	timer := time.NewTimer(d + jitter)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
