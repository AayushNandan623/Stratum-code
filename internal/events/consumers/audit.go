package consumers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/yourorg/stratum/internal/events"
)

// AuditArchiver is a durable pull consumer that reads audit events from NATS
// and inserts them into the audit_log table. It uses ON CONFLICT DO NOTHING
// for idempotent deduplication.
type AuditArchiver struct {
	db     *pgxpool.Pool
	js     nats.JetStreamContext
	logger *slog.Logger
}

// NewAuditArchiver creates a ready AuditArchiver.
func NewAuditArchiver(db *pgxpool.Pool, js nats.JetStreamContext, logger *slog.Logger) *AuditArchiver {
	return &AuditArchiver{db: db, js: js, logger: logger}
}

// Start begins consuming from the STRATUM_AUDIT stream. It blocks until ctx is
// cancelled.
func (a *AuditArchiver) Start(ctx context.Context) error {
	sub, err := a.js.PullSubscribe("stratum.audit.>", "audit-archiver",
		nats.BindStream("STRATUM_AUDIT"),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return err
	}

	a.logger.Info("audit archiver started")
	for {
		msgs, err := sub.Fetch(50, nats.MaxWait(5*time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			a.logger.Error("audit fetch error", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		for _, msg := range msgs {
			if err := a.archive(ctx, msg); err != nil {
				a.logger.Error("archive error", "err", err)
				msg.Nak() // retry
			} else {
				msg.Ack()
			}
		}
	}
}

func (a *AuditArchiver) archive(ctx context.Context, msg *nats.Msg) error {
	var event events.AuditEventMessage
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return err
	}

	_, err := a.db.Exec(ctx, `
		INSERT INTO audit_log (id, org_id, actor_id, actor_type, action, resource_type, resource_id, metadata, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO NOTHING
	`, event.ID, event.OrgID, event.ActorID, event.ActorType,
		event.Action, event.ResourceType, event.ResourceID, event.Metadata, event.OccurredAt)
	if err != nil {
		return err
	}
	return nil
}
