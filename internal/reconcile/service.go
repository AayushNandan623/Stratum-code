package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/events"
	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/stack"
)

// ReconcileService is the boundary contract for the reconcile context.
type ReconcileService interface {
	// Schedule management
	GetSchedule(ctx context.Context, stackID uuid.UUID) (*ReconcileSchedule, error)
	UpdateSchedule(ctx context.Context, stackID uuid.UUID, input UpdateScheduleInput) (*ReconcileSchedule, error)
	EnableSchedule(ctx context.Context, stackID uuid.UUID) error
	DisableSchedule(ctx context.Context, stackID uuid.UUID) error

	// Manual trigger
	TriggerNow(ctx context.Context, stackID uuid.UUID, actorID uuid.UUID) (*run.Run, error)

	// Drift record management
	GetDriftRecord(ctx context.Context, id uuid.UUID) (*DriftRecord, error)
	ListDriftRecords(ctx context.Context, filter DriftFilter) ([]*DriftRecord, int, error)
	IgnoreDrift(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

	// Called by run context callbacks
	ProcessDriftResult(ctx context.Context, runID uuid.UUID, planOutput *run.PlanOutput) error
	ResolveDrift(ctx context.Context, stackID uuid.UUID) error
}

type service struct {
	repo     *Repository
	db       *db.DB
	runSvc   run.RunService
	stackSvc stack.StackService
	logger   *slog.Logger
}

var _ ReconcileService = (*service)(nil)

// NewService constructs a ReconcileService.
func NewService(database *db.DB, runSvc run.RunService, stackSvc stack.StackService, logger *slog.Logger) ReconcileService {
	return &service{
		repo:     NewRepository(),
		db:       database,
		runSvc:   runSvc,
		stackSvc: stackSvc,
		logger:   logger,
	}
}

// ─── Schedule management ────────────────────────────────────────────────────

func (s *service) GetSchedule(ctx context.Context, stackID uuid.UUID) (*ReconcileSchedule, error) {
	return s.repo.GetSchedule(ctx, s.db.Pool, stackID)
}

func (s *service) UpdateSchedule(ctx context.Context, stackID uuid.UUID, input UpdateScheduleInput) (*ReconcileSchedule, error) {
	return s.repo.UpdateSchedule(ctx, s.db.Pool, stackID, input)
}

func (s *service) EnableSchedule(ctx context.Context, stackID uuid.UUID) error {
	return s.repo.EnableSchedule(ctx, s.db.Pool, stackID)
}

func (s *service) DisableSchedule(ctx context.Context, stackID uuid.UUID) error {
	return s.repo.DisableSchedule(ctx, s.db.Pool, stackID)
}

// ─── Manual trigger ─────────────────────────────────────────────────────────

func (s *service) TriggerNow(ctx context.Context, stackID uuid.UUID, actorID uuid.UUID) (*run.Run, error) {
	// Look up the schedule to get orgID, or look up the stack.
	schedule, err := s.repo.GetSchedule(ctx, s.db.Pool, stackID)
	if err != nil {
		return nil, err
	}
	ra, err := s.runSvc.Create(ctx, run.CreateRunInput{
		OrgID:       schedule.OrgID,
		StackID:     stackID,
		RunType:     run.RunTypeDriftDetect,
		TriggerType: run.TriggerManual,
		TriggeredBy: &actorID,
	})
	if err != nil {
		return nil, err
	}
	return ra, nil
}

// ─── Drift record management ───────────────────────────────────────────────

func (s *service) GetDriftRecord(ctx context.Context, id uuid.UUID) (*DriftRecord, error) {
	return s.repo.GetDriftRecord(ctx, s.db.Pool, id)
}

func (s *service) ListDriftRecords(ctx context.Context, filter DriftFilter) ([]*DriftRecord, int, error) {
	return s.repo.ListDriftRecords(ctx, s.db.Pool, filter)
}

func (s *service) IgnoreDrift(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error {
	rec, err := s.repo.GetDriftRecord(ctx, s.db.Pool, id)
	if err != nil {
		return err
	}
	if rec.IgnoredBy != nil {
		return nil // already ignored
	}
	if err := s.repo.IgnoreDriftRecord(ctx, s.db.Pool, id, actorID); err != nil {
		return err
	}
	// Return stack to ACTIVE if no other open drift records
	openRecords, err := s.repo.ListOpenDriftRecords(ctx, s.db.Pool, rec.StackID)
	if err != nil || len(openRecords) == 0 {
		s.stackSvc.SetStatus(ctx, rec.OrgID, rec.StackID, stack.StatusActive)
	}
	return nil
}

// ─── Drift result processing ────────────────────────────────────────────────

func (s *service) ProcessDriftResult(ctx context.Context, runID uuid.UUID, planOutput *run.PlanOutput) error {
	ra, err := s.runSvc.Get(ctx, runID)
	if err != nil {
		return err
	}

	hasDrift := planOutput != nil && planOutput.HasChanges
	if !hasDrift {
		// No drift — ensure stack is ACTIVE.
		s.stackSvc.SetStatus(ctx, ra.OrgID, ra.StackID, stack.StatusActive)
		s.repo.UpdateLastDriftAt(ctx, s.db.Pool, ra.StackID, nil)
		return nil
	}

	// Drift detected — create drift record.
	changes := make([]ResourceChange, len(planOutput.Resources))
	for i, rc := range planOutput.Resources {
		changes[i] = ResourceChange{Address: rc.Address, Actions: rc.Actions}
	}
	summary := buildDriftSummary(changes, planOutput.Added, planOutput.Changed, planOutput.Removed)
	now := time.Now()
	driftID := uuid.New()
	driftRec := &DriftRecord{
		ID:            driftID,
		StackID:       ra.StackID,
		OrgID:         ra.OrgID,
		TriggerRunID:  ra.ID,
		Status:        DriftStatusDetected,
		ResourceCount: len(planOutput.Resources),
		DriftSummary:  summary,
		DetectedAt:    now,
	}

	// Wrap drift record creation and outbox writes in a single transaction.
	err = s.db.InTx(ctx, func(q db.DBTX) error {
		if err := s.repo.CreateDriftRecord(ctx, q, driftRec); err != nil {
			return err
		}
		s.repo.UpdateLastDriftAt(ctx, q, ra.StackID, &driftRec.DetectedAt)

		// Look up stack for the name.
		stk, stkErr := s.stackSvc.Get(ctx, ra.OrgID, ra.StackID)
		if stkErr != nil {
			s.logger.Warn("drift: get stack for name", "stack_id", ra.StackID, "err", stkErr)
		}
		stackName := ""
		if stk != nil {
			stackName = stk.Name
		}

		// Phase 6: write drift.detected event to outbox.
		driftMsg := events.DriftEventMessage{
			EventID:       driftID,
			StackID:       ra.StackID,
			OrgID:         ra.OrgID,
			StackName:     stackName,
			DriftID:       driftID,
			ResourceCount: len(planOutput.Resources),
			DriftSummary:  summary,
			Status:        "drift.detected",
			DetectedAt:    now,
			OccurredAt:    now,
		}
		if err := events.InsertOutboxMessage(ctx, q,
			fmt.Sprintf("stratum.stacks.drifted.%s", ra.OrgID), driftMsg); err != nil {
			return err
		}
		// Also publish to stack events subject.
		if err := events.InsertOutboxMessage(ctx, q,
			fmt.Sprintf("stratum.stacks.events.%s", ra.StackID), driftMsg); err != nil {
			return err
		}
		// Write audit event.
		auditMsg := events.AuditEventMessage{
			ID:           driftID,
			OrgID:        ra.OrgID,
			ActorType:    "SYSTEM",
			Action:       "drift.detected",
			ResourceType: "stack",
			ResourceID:   &ra.StackID,
			Metadata:     summary,
			OccurredAt:   now,
		}
		if err := events.InsertOutboxMessage(ctx, q,
			fmt.Sprintf("stratum.audit.%s", ra.OrgID), auditMsg); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Handle remediation mode (outside the transaction — creates runs).
	s.stackSvc.SetStatus(ctx, ra.OrgID, ra.StackID, stack.StatusDrifted)

	schedule, err := s.repo.GetSchedule(ctx, s.db.Pool, ra.StackID)
	if err != nil {
		s.logger.Warn("no schedule found for drift result, skipping remediation",
			"stack_id", ra.StackID, "err", err)
		return nil
	}
	return s.triggerRemediation(ctx, ra.StackID, ra.OrgID, driftID, schedule.DriftMode)
}

func (s *service) ResolveDrift(ctx context.Context, stackID uuid.UUID) error {
	openRecords, err := s.repo.ListOpenDriftRecords(ctx, s.db.Pool, stackID)
	if err != nil {
		return err
	}
	if len(openRecords) == 0 {
		return nil
	}

	// Wrap resolution and outbox write in a transaction.
	now := time.Now()
	err = s.db.InTx(ctx, func(q db.DBTX) error {
		for _, rec := range openRecords {
			if err := s.repo.ResolveDriftRecord(ctx, q, rec.ID); err != nil {
				s.logger.Error("resolve drift record", "drift_id", rec.ID, "error", err)
				continue
			}
			// Phase 6: write drift.resolved event.
			driftMsg := events.DriftEventMessage{
				EventID:    uuid.New(),
				StackID:    rec.StackID,
				OrgID:      rec.OrgID,
				DriftID:    rec.ID,
				Status:     "drift.resolved",
				OccurredAt: now,
			}
			if err := events.InsertOutboxMessage(ctx, q,
				fmt.Sprintf("stratum.stacks.drifted.%s", rec.OrgID), driftMsg); err != nil {
				return err
			}
			// Write audit event.
			auditMsg := events.AuditEventMessage{
				ID:           uuid.New(),
				OrgID:        rec.OrgID,
				ActorType:    "SYSTEM",
				Action:       "drift.resolved",
				ResourceType: "stack",
				ResourceID:   &rec.StackID,
				Metadata:     json.RawMessage(`{}`),
				OccurredAt:   now,
			}
			if err := events.InsertOutboxMessage(ctx, q,
				fmt.Sprintf("stratum.audit.%s", rec.OrgID), auditMsg); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Need orgID — get it from the first record.
	orgID := openRecords[0].OrgID
	s.stackSvc.SetStatus(ctx, orgID, stackID, stack.StatusActive)
	return nil
}

// ─── Remediation ────────────────────────────────────────────────────────────

func (s *service) triggerRemediation(ctx context.Context, stackID, orgID uuid.UUID, driftID uuid.UUID, mode DriftMode) error {
	switch mode {
	case DriftModeNone, DriftModeNotify:
		return nil // record only; notification handled by event subscriber (Phase 6)
	case DriftModeAutoPlan:
		ra, err := s.runSvc.Create(ctx, run.CreateRunInput{
			OrgID:       orgID,
			StackID:     stackID,
			RunType:     run.RunTypePlan,
			TriggerType: run.TriggerDrift,
		})
		if err != nil {
			return err
		}
		return s.repo.SetRemediationRun(ctx, s.db.Pool, driftID, ra.ID)
	case DriftModeAutoApply:
		ra, err := s.runSvc.Create(ctx, run.CreateRunInput{
			OrgID:       orgID,
			StackID:     stackID,
			RunType:     run.RunTypeApply,
			TriggerType: run.TriggerDrift,
		})
		if err != nil {
			return err
		}
		return s.repo.SetRemediationRun(ctx, s.db.Pool, driftID, ra.ID)
	}
	return nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// Ensure json import is used for buildDriftSummary.
var _ = json.Marshal
