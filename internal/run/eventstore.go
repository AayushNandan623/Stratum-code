package run

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/stratum/internal/platform/db"
)

// EventStore persists and retrieves run events. It is stateless and accepts a
// DBTX so callers can participate in transactions.
type EventStore struct{}

// NewEventStore returns a ready EventStore.
func NewEventStore() *EventStore {
	return &EventStore{}
}

// AppendEventInput contains the fields needed to insert a run event.
type AppendEventInput struct {
	RunID      uuid.UUID
	OrgID      uuid.UUID
	EventType  string
	ActorID    *uuid.UUID
	ActorType  string
	Payload    json.RawMessage
	OccurredAt time.Time
}

// Append inserts a new run event with an automatically derived seq number. It
// must be called within a transaction that holds a FOR UPDATE lock on the run
// row to guarantee monotonic seq ordering.
func (es *EventStore) Append(ctx context.Context, q db.DBTX, input AppendEventInput) (*RunEvent, error) {
	var nextSeq int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = $1`,
		input.RunID).Scan(&nextSeq)
	if err != nil {
		return nil, err
	}
	if input.Payload == nil {
		input.Payload = json.RawMessage(`{}`)
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}

	ev := &RunEvent{
		ID:         uuid.New(),
		RunID:      input.RunID,
		OrgID:      input.OrgID,
		Seq:        nextSeq,
		EventType:  input.EventType,
		ActorID:    input.ActorID,
		ActorType:  input.ActorType,
		Payload:    input.Payload,
		OccurredAt: input.OccurredAt,
		InsertedAt: time.Now(),
	}

	const sql = `INSERT INTO run_events
		(id, run_id, org_id, seq, event_type, actor_id, actor_type, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	_, err = q.Exec(ctx, sql,
		ev.ID, ev.RunID, ev.OrgID, ev.Seq, ev.EventType,
		ev.ActorID, ev.ActorType, ev.Payload, ev.OccurredAt)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

// GetTimeline returns all events for a run ordered by seq.
func (es *EventStore) GetTimeline(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) ([]*RunEvent, error) {
	const sql = `SELECT id, run_id, org_id, seq, event_type, actor_id, actor_type, payload, occurred_at, inserted_at
		FROM run_events WHERE run_id = $1 ORDER BY seq`
	rows, err := pool.Query(ctx, sql, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*RunEvent
	for rows.Next() {
		ev := &RunEvent{}
		err := rows.Scan(
			&ev.ID, &ev.RunID, &ev.OrgID, &ev.Seq, &ev.EventType,
			&ev.ActorID, &ev.ActorType, &ev.Payload, &ev.OccurredAt, &ev.InsertedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetTimelineTx is like GetTimeline but accepts a DBTX for transactional reads.
func (es *EventStore) GetTimelineTx(ctx context.Context, q db.DBTX, runID uuid.UUID) ([]*RunEvent, error) {
	const sql = `SELECT id, run_id, org_id, seq, event_type, actor_id, actor_type, payload, occurred_at, inserted_at
		FROM run_events WHERE run_id = $1 ORDER BY seq`
	rows, err := q.Query(ctx, sql, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*RunEvent
	for rows.Next() {
		ev := &RunEvent{}
		err := rows.Scan(
			&ev.ID, &ev.RunID, &ev.OrgID, &ev.Seq, &ev.EventType,
			&ev.ActorID, &ev.ActorType, &ev.Payload, &ev.OccurredAt, &ev.InsertedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

