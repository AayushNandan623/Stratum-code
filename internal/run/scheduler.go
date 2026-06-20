package run

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/clock"
	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/stack"
)

// Scheduler is the periodic goroutine that drives eligible PENDING runs to
// QUEUED, enforces DAG ordering, and recycles expired job claims. It never
// calls the stack context directly; all stack access goes through
// stack.StackService.
type Scheduler struct {
	db       *db.DB
	runRepo  *Repository
	runSvc   RunService
	stackSvc stack.StackService
	clock    clock.Clock
	interval time.Duration
	logger   *slog.Logger
}

// NewScheduler constructs a Scheduler. Call Start to begin the tick loop.
func NewScheduler(
	database *db.DB,
	runRepo *Repository,
	runSvc RunService,
	stackSvc stack.StackService,
	clk clock.Clock,
	interval time.Duration,
	logger *slog.Logger,
) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Scheduler{
		db:       database,
		runRepo:  runRepo,
		runSvc:   runSvc,
		stackSvc: stackSvc,
		clock:    clk,
		interval: interval,
		logger:   logger,
	}
}

// Start begins the scheduler tick loop. It returns when ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.logger.Info("scheduler starting", "interval", s.interval)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.tick(ctx)
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		}
	}
}

// tick runs one cycle of the scheduler loop.
func (s *Scheduler) tick(ctx context.Context) {
	// 1. Load all PENDING runs.
	pendingRuns, err := s.runRepo.ListByState(ctx, s.db.Pool, StatePending)
	if err != nil {
		s.logger.Error("scheduler: list pending runs", "error", err)
		return
	}
	// 2. Check DAG eligibility and enqueue.
	for _, run := range pendingRuns {
		if s.isBlocked(ctx, run) {
			s.logger.Debug("scheduler: run blocked by DAG", "run_id", run.ID, "stack_id", run.StackID)
			continue
		}
		if s.hasOtherActiveRun(ctx, run) {
			s.logger.Debug("scheduler: stack has other active run", "run_id", run.ID, "stack_id", run.StackID)
			continue
		}
		s.enqueue(ctx, run)
	}
	// 3. Handle timed-out job claims.
	s.requeueTimedOutJobs(ctx)
	// 4. Self-heal: create missing jobs for QUEUED runs (e.g. after a crash
	//    between Transition and CreateJob in a previous tick).
	s.ensureQueuedRunsHaveJobs(ctx)
}

// isBlocked returns true if the run's stack has upstream dependencies that are
// not yet satisfied (non-ACTIVE status or active runs in progress).
func (s *Scheduler) isBlocked(ctx context.Context, run *Run) bool {
	deps, err := s.stackSvc.GetDependencies(ctx, run.OrgID, run.StackID)
	if err != nil {
		s.logger.Error("scheduler: get dependencies", "run_id", run.ID, "stack_id", run.StackID, "error", err)
		return true // conservative: block on error
	}
	for _, dep := range deps {
		upstream, err := s.stackSvc.Get(ctx, run.OrgID, dep.DependsOnID)
		if err != nil {
			s.logger.Error("scheduler: get upstream stack", "stack_id", dep.DependsOnID, "error", err)
			return true
		}
		if upstream.Status != stack.StatusActive {
			s.logger.Debug("scheduler: upstream not active",
				"run_id", run.ID, "upstream", dep.DependsOnID, "status", upstream.Status)
			return true
		}
		active, err := s.runSvc.HasActiveRun(ctx, dep.DependsOnID)
		if err != nil {
			s.logger.Error("scheduler: check active run upstream", "stack_id", dep.DependsOnID, "error", err)
			return true
		}
		if active {
			s.logger.Debug("scheduler: upstream has active run", "run_id", run.ID, "upstream", dep.DependsOnID)
			return true
		}
		// Block if the upstream has never had a successful run.
		applied, err := s.runRepo.HasAppliedRun(ctx, s.db.Pool, dep.DependsOnID)
		if err != nil {
			s.logger.Error("scheduler: check applied run upstream", "stack_id", dep.DependsOnID, "error", err)
			return true
		}
		if !applied {
			s.logger.Debug("scheduler: upstream never applied", "run_id", run.ID, "upstream", dep.DependsOnID)
			return true
		}
	}
	return false
}

// hasOtherActiveRun returns true if the stack currently has a run in a
// non-terminal state OTHER than the run being considered. This prevents
// queueing a second run on a stack that already has an active run, without
// blocking the run itself.
func (s *Scheduler) hasOtherActiveRun(ctx context.Context, run *Run) bool {
	active, err := s.runRepo.HasActiveRunExcluding(ctx, s.db.Pool, run.StackID, run.ID)
	if err != nil {
		s.logger.Error("scheduler: has other active run", "stack_id", run.StackID, "error", err)
		return true // conservative: block on error
	}
	return active
}

// enqueue transitions a run from PENDING to QUEUED and creates an AVAILABLE
// job row so workers can pick it up (Phase 3+).
func (s *Scheduler) enqueue(ctx context.Context, run *Run) {
	if err := s.runSvc.Transition(ctx, run.ID, StateQueued, nil); err != nil {
		s.logger.Error("scheduler: transition to queued", "run_id", run.ID, "error", err)
		return
	}
	job := &RunJob{
		ID:        uuid.New(),
		RunID:     run.ID,
		Status:    "AVAILABLE",
		Attempt:   0,
		CreatedAt: s.clock.Now(),
	}
	if err := s.runRepo.CreateJob(ctx, s.db.Pool, job); err != nil {
		s.logger.Error("scheduler: create job", "run_id", run.ID, "error", err)
	}
}

// requeueTimedOutJobs finds CLAIMED jobs past their expires_at and either
// re-queues them (up to 3 attempts) or marks the run as FAILED. Runs in
// APPLYING state are always failed (mid-apply worker crash is unsafe).
func (s *Scheduler) requeueTimedOutJobs(ctx context.Context) {
	jobs, err := s.runRepo.ListTimedOutJobs(ctx, s.db.Pool)
	if err != nil {
		s.logger.Error("scheduler: list timed out jobs", "error", err)
		return
	}
	for _, job := range jobs {
		run, err := s.runRepo.GetByID(ctx, s.db.Pool, job.RunID)
		if err != nil {
			s.logger.Error("scheduler: get run for timed out job", "job_id", job.ID, "error", err)
			continue
		}
		if run.CurrentState == StateApplying {
			s.logger.Warn("scheduler: worker timeout during apply — marking failed",
				"run_id", run.ID, "job_id", job.ID)
			if err := s.runSvc.Transition(ctx, run.ID, StateFailed, map[string]string{
				"reason": "worker_timeout_during_apply",
			}); err != nil {
				s.logger.Error("scheduler: transition to failed (apply timeout)", "run_id", run.ID, "error", err)
			}
			continue
		}
		job.Attempt++
		if job.Attempt >= 3 {
			s.logger.Warn("scheduler: max attempts exceeded", "run_id", run.ID, "job_id", job.ID, "attempt", job.Attempt)
			if err := s.runSvc.Transition(ctx, run.ID, StateFailed, map[string]any{
				"reason":  "max_attempts_exceeded",
				"attempt": job.Attempt,
			}); err != nil {
				s.logger.Error("scheduler: transition to failed (max attempts)", "run_id", run.ID, "error", err)
			}
			continue
		}
		if err := s.runRepo.RequeueJob(ctx, s.db.Pool, job); err != nil {
			s.logger.Error("scheduler: requeue job", "job_id", job.ID, "error", err)
			continue
		}
		if err := s.runSvc.Transition(ctx, run.ID, StateQueued, map[string]any{
			"reason":  "worker_timeout_requeue",
			"attempt": job.Attempt,
		}); err != nil {
			s.logger.Error("scheduler: transition to queued (requeue)", "run_id", run.ID, "error", err)
		}
	}
}

// ensureQueuedRunsHaveJobs creates missing AVAILABLE jobs for QUEUED runs that
// don't have one. This self-heals cases where a server crash between the state
// transition and job creation left the run in QUEUED with no job.
func (s *Scheduler) ensureQueuedRunsHaveJobs(ctx context.Context) {
	const sql = `SELECT ` + runColumns + ` FROM runs r
		WHERE r.current_state = 'QUEUED'
		AND NOT EXISTS (SELECT 1 FROM run_jobs j WHERE j.run_id = r.id AND j.status = 'AVAILABLE')`
	rows, err := s.db.Pool.Query(ctx, sql)
	if err != nil {
		s.logger.Error("scheduler: find queued runs without jobs", "error", err)
		return
	}
	defer rows.Close()
	runs, err := scanRuns(rows)
	if err != nil {
		s.logger.Error("scheduler: scan queued runs without jobs", "error", err)
		return
	}
	for _, r := range runs {
		job := &RunJob{
			ID:        uuid.New(),
			RunID:     r.ID,
			Status:    "AVAILABLE",
			Attempt:   0,
			CreatedAt: s.clock.Now(),
		}
		if err := s.runRepo.CreateJob(ctx, s.db.Pool, job); err != nil {
			s.logger.Error("scheduler: create missing job", "run_id", r.ID, "error", err)
		} else {
			s.logger.Info("scheduler: created missing job for queued run", "run_id", r.ID)
		}
	}
}
