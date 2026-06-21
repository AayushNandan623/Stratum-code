// Package events provides the event bus abstraction, NATS JetStream
// implementation, transactional outbox, and event-driven consumers for
// the Stratum control plane.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Message is the envelope that flows through the event bus.
type Message struct {
	ID        string          // NATS MsgId for deduplication
	Subject   string          // NATS subject
	Payload   json.RawMessage // serialised event body
	Timestamp time.Time
}

// MessageHandler processes a single event message.
type MessageHandler func(ctx context.Context, msg *Message) error

// ─── Subject constants ──────────────────────────────────────────────────────

const (
	SubjectRunEvents    = "stratum.runs.events.%s"    // format with run_id
	SubjectRunLogs      = "stratum.runs.logs.%s"      // format with run_id
	SubjectStackEvents  = "stratum.stacks.events.%s"  // format with stack_id
	SubjectStackDrifted = "stratum.stacks.drifted.%s" // format with org_id
	SubjectAudit        = "stratum.audit.%s"          // format with org_id
	SubjectReconcile    = "stratum.reconcile.trigger.%s" // format with stack_id
)

// ─── Domain event payloads ──────────────────────────────────────────────────

// RunEventMessage is published to stratum.runs.events.{run_id}.
type RunEventMessage struct {
	EventID    uuid.UUID       `json:"event_id"`
	RunID      uuid.UUID       `json:"run_id"`
	OrgID      uuid.UUID       `json:"org_id"`
	StackID    uuid.UUID       `json:"stack_id"`
	Seq        int64           `json:"seq"`
	EventType  string          `json:"event_type"`
	ActorID    *uuid.UUID      `json:"actor_id,omitempty"`
	ActorType  string          `json:"actor_type,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// StackEventMessage is published to stratum.stacks.events.{stack_id}.
type StackEventMessage struct {
	EventID      uuid.UUID       `json:"event_id"`
	StackID      uuid.UUID       `json:"stack_id"`
	OrgID        uuid.UUID       `json:"org_id"`
	EventType    string          `json:"event_type"` // stack.created, stack.updated, stack.config_updated, stack.deleted
	ActorID      *uuid.UUID      `json:"actor_id,omitempty"`
	ActorType    string          `json:"actor_type,omitempty"`
	Previous     json.RawMessage `json:"previous,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

// DriftEventMessage is published to stratum.stacks.drifted.{org_id}.
type DriftEventMessage struct {
	EventID       uuid.UUID       `json:"event_id"`
	StackID       uuid.UUID       `json:"stack_id"`
	OrgID         uuid.UUID       `json:"org_id"`
	StackName     string          `json:"stack_name"`
	DriftID       uuid.UUID       `json:"drift_id"`
	ResourceCount int             `json:"resource_count"`
	DriftSummary  json.RawMessage `json:"drift_summary"`
	Status        string          `json:"status"` // drift.detected or drift.resolved
	DetectedAt    time.Time       `json:"detected_at"`
	OccurredAt    time.Time       `json:"occurred_at"`
}

// AuditEventMessage is published to stratum.audit.{org_id}.
type AuditEventMessage struct {
	ID           uuid.UUID       `json:"id"`
	OrgID        uuid.UUID       `json:"org_id"`
	ActorID      *uuid.UUID      `json:"actor_id,omitempty"`
	ActorType    string          `json:"actor_type"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   *uuid.UUID      `json:"resource_id,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	OccurredAt   time.Time       `json:"occurred_at"`
}
