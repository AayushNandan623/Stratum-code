package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/yourorg/stratum/internal/platform/db"
)

// InsertOutboxMessage writes an outbox_messages row within an existing database
// transaction. This is the core of the transactional outbox pattern: the event
// insert and the domain write happen atomically.
func InsertOutboxMessage(ctx context.Context, q db.DBTX, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO outbox_messages (subject, payload)
		VALUES ($1, $2)
	`, subject, data)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

// ─── OutboxRelay ────────────────────────────────────────────────────────────

// OutboxRelay polls the outbox_messages table for PENDING rows, claims them
// atomically with SKIP LOCKED, publishes to NATS, and marks them DELIVERED.
// It runs in a single goroutine (no parallelism — ordering is best-effort).
type OutboxRelay struct {
	db     *pgxpool.Pool
	js     nats.JetStreamContext
	tick   time.Duration // poll interval (default 500ms)
	batch  int           // messages per flush (default 50)
	logger *slog.Logger
}

// outboxMsg is a scanned row from outbox_messages.
type outboxMsg struct {
	ID      uuid.UUID
	Subject string
	Payload []byte
	Attempt int
}

// NewOutboxRelay creates a ready OutboxRelay.
func NewOutboxRelay(db *pgxpool.Pool, js nats.JetStreamContext, logger *slog.Logger) *OutboxRelay {
	return &OutboxRelay{
		db:     db,
		js:     js,
		tick:   500 * time.Millisecond,
		batch:  50,
		logger: logger,
	}
}

// WithTick sets the poll interval.
func (r *OutboxRelay) WithTick(d time.Duration) *OutboxRelay {
	r.tick = d
	return r
}

// WithBatch sets the batch size.
func (r *OutboxRelay) WithBatch(n int) *OutboxRelay {
	r.batch = n
	return r
}

// Start begins the relay loop. It blocks until ctx is cancelled, then performs
// a final flush before returning.
func (r *OutboxRelay) Start(ctx context.Context) {
	r.logger.Info("outbox relay starting",
		"tick", r.tick,
		"batch", r.batch,
	)
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := r.flush(ctx); err != nil {
				r.logger.Error("outbox flush error", "err", err)
			}
		case <-ctx.Done():
			r.logger.Info("outbox relay shutting down, final flush")
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.flush(flushCtx); err != nil {
				r.logger.Error("outbox final flush error", "err", err)
			}
			return
		}
	}
}

// flush claims a batch of PENDING messages and publishes each to NATS.
// On publish failure the message is marked for retry with exponential backoff:
// 30s, 60s, 90s, ... capped at 300s (5 minutes).
func (r *OutboxRelay) flush(ctx context.Context) error {
	rows, err := r.db.Query(ctx, `
		WITH claimed AS (
			SELECT id, subject, payload, attempt
			FROM outbox_messages
			WHERE status = 'PENDING'
			  AND deliver_after <= now()
			  AND attempt < max_attempts
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox_messages m
		SET status = 'IN_FLIGHT', attempt = attempt + 1
		FROM claimed
		WHERE m.id = claimed.id
		RETURNING m.id, m.subject, m.payload, m.attempt
	`, r.batch)
	if err != nil {
		return fmt.Errorf("claim outbox batch: %w", err)
	}
	defer rows.Close()

	var msgs []outboxMsg
	for rows.Next() {
		var m outboxMsg
		if err := rows.Scan(&m.ID, &m.Subject, &m.Payload, &m.Attempt); err != nil {
			return fmt.Errorf("scan outbox row: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil // nothing to deliver
	}

	for _, m := range msgs {
		pubAck, err := r.js.Publish(m.Subject, m.Payload, nats.MsgId(m.ID.String()))
		if err != nil || pubAck == nil {
			r.logger.Error("outbox publish failed",
				"msg_id", m.ID,
				"subject", m.Subject,
				"attempt", m.Attempt,
				"error", err,
			)
			// Exponential backoff: 30s, 60s, 90s, ... capped at 300s.
			backoff := int(math.Min(float64(30*m.Attempt), 300))
			_, updErr := r.db.Exec(ctx, `
				UPDATE outbox_messages
				SET status = 'PENDING',
				    deliver_after = now() + ($1 * interval '1 second')
				WHERE id = $2
			`, backoff, m.ID)
			if updErr != nil {
				r.logger.Error("outbox set retry failed",
					"msg_id", m.ID,
					"error", updErr,
				)
			}
			continue
		}
		// Mark delivered.
		_, updErr := r.db.Exec(ctx, `
			UPDATE outbox_messages
			SET status = 'DELIVERED', delivered_at = now()
			WHERE id = $1
		`, m.ID)
		if updErr != nil {
			r.logger.Error("outbox mark delivered failed",
				"msg_id", m.ID,
				"error", updErr,
			)
		}
		r.logger.Debug("outbox message delivered",
			"msg_id", m.ID,
			"subject", m.Subject,
		)
	}
	return nil
}
