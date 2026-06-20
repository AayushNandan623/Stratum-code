package run

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// EventPublisher is the interface for publishing run events to subscribers
// (WebSocket hub in Phase 2, NATS in Phase 6). Defined here to avoid importing
// the API layer from the run context.
type EventPublisher interface {
	PublishRunEvent(runID string, data []byte)
}

// RunService is the boundary contract for the run context.
type RunService interface {
	Create(ctx context.Context, input CreateRunInput) (*Run, error)
	Get(ctx context.Context, id uuid.UUID) (*Run, error)
	List(ctx context.Context, filter RunFilter) ([]*Run, int, error)
	Cancel(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
	Approve(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
	Discard(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

	// State transitions (called by scheduler and workers)
	Transition(ctx context.Context, id uuid.UUID, to RunState, meta any) error
	HasActiveRun(ctx context.Context, stackID uuid.UUID) (bool, error)

	// Event store
	AppendEvent(ctx context.Context, runID uuid.UUID, event RunEventInput) error
	GetTimeline(ctx context.Context, runID uuid.UUID) ([]*RunEvent, error)

	// Logs
	AppendLogs(ctx context.Context, runID uuid.UUID, lines []LogLine) error
	GetLogs(ctx context.Context, runID uuid.UUID, page Pagination) ([]*LogLine, int, error)
}

type service struct {
	repo      *Repository
	events    *EventStore
	sm        *StateMachine
	db        *db.DB
	hub       EventPublisher // optional — nil is safe
	logger    *slog.Logger
}

var _ RunService = (*service)(nil)

// NewService constructs a RunService. hub is optional; pass nil for no event
// broadcasting (e.g. in tests).
func NewService(database *db.DB, hub EventPublisher, logger *slog.Logger) RunService {
	return &service{
		repo:   NewRepository(),
		events: NewEventStore(),
		sm:     NewStateMachine(),
		db:     database,
		hub:    hub,
		logger: logger,
	}
}

// ─── Create ─────────────────────────────────────────────────────────────────

func (s *service) Create(ctx context.Context, input CreateRunInput) (*Run, error) {
	if input.OrgID == uuid.Nil || input.StackID == uuid.Nil {
		return nil, fmt.Errorf("%w: org_id and stack_id are required", domainerr.ErrValidation)
	}
	if input.RunType == "" {
		input.RunType = RunTypePlan
	}
	if input.TriggerType == "" {
		input.TriggerType = TriggerManual
	}
	now := time.Now()
	run := &Run{
		ID:            uuid.New(),
		OrgID:         input.OrgID,
		StackID:       input.StackID,
		SpaceID:       input.SpaceID,
		RunType:       input.RunType,
		CurrentState:  StatePending,
		TriggerType:   input.TriggerType,
		TriggeredBy:   input.TriggeredBy,
		ConfigVersion: input.ConfigVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	err := s.db.InTx(ctx, func(q db.DBTX) error {
		if err := s.repo.Create(ctx, q, run); err != nil {
			return err
		}
		ev, err := s.events.Append(ctx, q, AppendEventInput{
			RunID:      run.ID,
			OrgID:      run.OrgID,
			EventType:  "run.created",
			ActorID:    input.TriggeredBy,
			ActorType:  actorType(input.TriggeredBy),
			Payload:    marshalMeta(map[string]string{"run_type": string(input.RunType), "trigger_type": string(input.TriggerType)}),
			OccurredAt: now,
		})
		if err != nil {
			return err
		}
		s.broadcastEvent(run.ID.String(), ev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// ─── Get ────────────────────────────────────────────────────────────────────

func (s *service) Get(ctx context.Context, id uuid.UUID) (*Run, error) {
	return s.repo.GetByID(ctx, s.db.Pool, id)
}

// ─── List ───────────────────────────────────────────────────────────────────

func (s *service) List(ctx context.Context, filter RunFilter) ([]*Run, int, error) {
	return s.repo.List(ctx, s.db.Pool, filter)
}

// ─── Cancel ─────────────────────────────────────────────────────────────────

func (s *service) Cancel(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error {
	payload, _ := json.Marshal(map[string]string{"actor_id": actorID.String()})
	return s.transitionWithActor(ctx, id, StateCancelled, actorID, "user", payload)
}

// ─── Approve ────────────────────────────────────────────────────────────────

func (s *service) Approve(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error {
	// TODO(phase-4): Policy evaluation gate. Phase 4 replaces this with a call
	// to policySvc.Evaluate() and transitions to StatePolicyRejected on deny.
	// For now, always allow.
	payload, _ := json.Marshal(map[string]string{"actor_id": actorID.String(), "source": "approval"})
	return s.transitionWithActor(ctx, id, StateApplying, actorID, "user", payload)
}

// ─── Discard ────────────────────────────────────────────────────────────────

func (s *service) Discard(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error {
	payload, _ := json.Marshal(map[string]string{"actor_id": actorID.String()})
	return s.transitionWithActor(ctx, id, StateDiscarded, actorID, "user", payload)
}

// ─── Transition ─────────────────────────────────────────────────────────────

func (s *service) Transition(ctx context.Context, id uuid.UUID, to RunState, meta any) error {
	return s.db.InTx(ctx, func(q db.DBTX) error {
		run, err := s.repo.LockRun(ctx, q, id)
		if err != nil {
			return err
		}
		if err := s.sm.Transition(run.CurrentState, to); err != nil {
			return err
		}
		eventType := stateToEventType(to)
		payload := marshalMeta(meta)
		ev, err := s.events.Append(ctx, q, AppendEventInput{
			RunID:      id,
			OrgID:      run.OrgID,
			EventType:  eventType,
			Payload:    payload,
			OccurredAt: time.Now(),
		})
		if err != nil {
			return err
		}
		if err := s.repo.UpdateState(ctx, q, id, to); err != nil {
			return err
		}
		s.broadcastEvent(id.String(), ev)
		return nil
	})
}

// transitionWithActor is like Transition but appends an actor reference to the
// event. It's used for user-initiated transitions (Cancel, Approve, Discard).
func (s *service) transitionWithActor(ctx context.Context, id uuid.UUID, to RunState, actorID uuid.UUID, actorType string, payload json.RawMessage) error {
	return s.db.InTx(ctx, func(q db.DBTX) error {
		run, err := s.repo.LockRun(ctx, q, id)
		if err != nil {
			return err
		}
		if err := s.sm.Transition(run.CurrentState, to); err != nil {
			return err
		}
		eventType := stateToEventType(to)
		ev, err := s.events.Append(ctx, q, AppendEventInput{
			RunID:      id,
			OrgID:      run.OrgID,
			EventType:  eventType,
			ActorID:    &actorID,
			ActorType:  actorType,
			Payload:    payload,
			OccurredAt: time.Now(),
		})
		if err != nil {
			return err
		}
		if err := s.repo.UpdateState(ctx, q, id, to); err != nil {
			return err
		}
		s.broadcastEvent(id.String(), ev)
		return nil
	})
}

// ─── HasActiveRun ───────────────────────────────────────────────────────────

func (s *service) HasActiveRun(ctx context.Context, stackID uuid.UUID) (bool, error) {
	return s.repo.HasActiveRun(ctx, s.db.Pool, stackID)
}

// ─── Event store ────────────────────────────────────────────────────────────

func (s *service) AppendEvent(ctx context.Context, runID uuid.UUID, input RunEventInput) error {
	return s.db.InTx(ctx, func(q db.DBTX) error {
		run, err := s.repo.LockRun(ctx, q, runID)
		if err != nil {
			return err
		}
		ev, err := s.events.Append(ctx, q, AppendEventInput{
			RunID:      runID,
			OrgID:      run.OrgID,
			EventType:  input.EventType,
			ActorID:    input.ActorID,
			ActorType:  input.ActorType,
			Payload:    input.Payload,
			OccurredAt: input.OccurredAt,
		})
		if err != nil {
			return err
		}
		s.broadcastEvent(runID.String(), ev)
		return nil
	})
}

func (s *service) GetTimeline(ctx context.Context, runID uuid.UUID) ([]*RunEvent, error) {
	return s.events.GetTimeline(ctx, s.db.Pool, runID)
}

// ─── Logs ───────────────────────────────────────────────────────────────────

func (s *service) AppendLogs(ctx context.Context, runID uuid.UUID, lines []LogLine) error {
	// For simplicity, we use the pool directly (no transaction needed for
	// log ingestion — best-effort is acceptable).
	_, err := s.repo.InsertLogLines(ctx, s.db.Pool, runID, lines)
	return err
}

func (s *service) GetLogs(ctx context.Context, runID uuid.UUID, page Pagination) ([]*LogLine, int, error) {
	return s.repo.ListLogLines(ctx, s.db.Pool, runID, page)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// stateToEventType maps a state to the event type string stored in run_events.
func stateToEventType(s RunState) string {
	switch s {
	case StatePending:
		return "run.created"
	case StateQueued:
		return "run.queued"
	case StateAssigned:
		return "run.assigned"
	case StatePlanning:
		return "run.planning_started"
	case StatePlanned:
		return "run.planned"
	case StateAwaitingApproval:
		return "run.awaiting_approval"
	case StateApplying:
		return "run.applying_started"
	case StateApplied:
		return "run.applied"
	case StateFailed:
		return "run.failed"
	case StateCancelled:
		return "run.cancelled"
	case StateDiscarded:
		return "run.discarded"
	case StatePolicyRejected:
		return "run.policy_rejected"
	default:
		return "run." + string(s)
	}
}

// actorType returns the actor type string based on whether an actor ID is set.
func actorType(actorID *uuid.UUID) string {
	if actorID != nil {
		return "user"
	}
	return "system"
}

// marshalMeta converts arbitrary metadata to JSON. Returns "{}" on failure.
func marshalMeta(meta any) json.RawMessage {
	if meta == nil {
		return json.RawMessage(`{}`)
	}
	switch v := meta.(type) {
	case json.RawMessage:
		return v
	case []byte:
		return json.RawMessage(v)
	default:
		b, err := json.Marshal(meta)
		if err != nil {
			return json.RawMessage(`{"marshal_error":"` + err.Error() + `"}`)
		}
		return json.RawMessage(b)
	}
}

func (s *service) broadcastEvent(runID string, ev *RunEvent) {
	if s.hub == nil {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		s.logger.Error("failed to marshal run event for broadcast", "run_id", runID, "error", err)
		return
	}
	s.hub.PublishRunEvent(runID, data)
}
