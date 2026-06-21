package events

import (
	"context"

	"github.com/yourorg/stratum/internal/platform/db"
)

// EventBus is the central interface for publishing and subscribing to domain
// events. In production the implementation writes to the outbox table within
// the caller's database transaction (PublishTx) and hands off delivery to
// NATS via the OutboxRelay. In tests an in-memory implementation delivers
// synchronously to subscribers.
type EventBus interface {
	// Publish sends an event to a subject immediately. In production this
	// writes directly to NATS (for consumers that don't need transactional
	// durability). For transactional dual-write use PublishTx.
	Publish(ctx context.Context, subject string, payload any) error

	// PublishTx writes an outbox row within an existing database transaction.
	// The message is durably stored and delivered asynchronously via OutboxRelay.
	PublishTx(ctx context.Context, q db.DBTX, subject string, payload any) error

	// Subscribe registers a handler for the given subject pattern. When the
	// bus receives a matching message it invokes the handler with the decoded
	// message. Used internally for consumers (audit, notify, reconcile).
	Subscribe(ctx context.Context, subject string, handler MessageHandler) error

	// Close releases all resources held by the bus (NATS connection, etc.).
	Close() error
}
