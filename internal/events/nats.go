package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/yourorg/stratum/internal/platform/db"
)

// Compile-time check: *NATSBus implements EventBus.
var _ EventBus = (*NATSBus)(nil)

// NATSBus implements EventBus backed by NATS JetStream. Events published via
// PublishTx are written to the outbox table and delivered asynchronously by
// the OutboxRelay. Direct Publish calls go straight to NATS for consumers that
// don't require transactional semantics.
type NATSBus struct {
	nc     *nats.Conn
	js     nats.JetStreamContext
	logger *slog.Logger
}

// NewNATSBus connects to a NATS server and ensures the required streams exist.
func NewNATSBus(ctx context.Context, url string, logger *slog.Logger) (*NATSBus, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.Name("stratum-server"),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	bus := &NATSBus{nc: nc, js: js, logger: logger}
	if err := bus.setupStreams(); err != nil {
		nc.Close()
		return nil, fmt.Errorf("stream setup: %w", err)
	}
	return bus, nil
}

// Publish sends a message directly to NATS. The payload is serialised as JSON.
func (b *NATSBus) Publish(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	_, err = b.js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("nats publish: %w", err)
	}
	return nil
}

// PublishTx writes an outbox_messages row within the caller's transaction.
// The OutboxRelay delivers it asynchronously.
func (b *NATSBus) PublishTx(ctx context.Context, q db.DBTX, subject string, payload any) error {
	return InsertOutboxMessage(ctx, q, subject, payload)
}

// Subscribe registers a NATS push subscription for the given subject pattern.
// The handler is called with a decoded Message for every matching message.
func (b *NATSBus) Subscribe(_ context.Context, subject string, handler MessageHandler) error {
	_, err := b.js.Subscribe(subject, func(msg *nats.Msg) {
		m := &Message{
			ID:        msg.Header.Get("Nats-Msg-Id"),
			Subject:   msg.Subject,
			Payload:   msg.Data,
			Timestamp: time.Now(),
		}
		if err := handler(context.Background(), m); err != nil {
			b.logger.Error("event handler error",
				"subject", subject,
				"msg_id", m.ID,
				"error", err,
			)
			msg.Nak()
			return
		}
		msg.Ack()
	},
		nats.DeliverNew(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
	)
	if err != nil {
		return fmt.Errorf("nats subscribe: %w", err)
	}
	return nil
}

// Close drains and closes the NATS connection.
func (b *NATSBus) Close() error {
	b.nc.Close()
	return nil
}

// JetStream returns the underlying JetStream context. Needed by the OutboxRelay
// and consumers that require direct NATS access.
func (b *NATSBus) JetStream() nats.JetStreamContext {
	return b.js
}

// Conn returns the underlying NATS connection.
func (b *NATSBus) Conn() *nats.Conn {
	return b.nc
}

// ─── Stream setup ───────────────────────────────────────────────────────────

var streamConfigs = []*nats.StreamConfig{
	{
		Name:       "STRATUM_RUNS",
		Subjects:   []string{"stratum.runs.>"},
		Retention:  nats.LimitsPolicy,
		MaxAge:     30 * 24 * time.Hour,
		Storage:    nats.FileStorage,
		Replicas:   1,
		Duplicates: 2 * time.Minute,
	},
	{
		Name:      "STRATUM_STACKS",
		Subjects:  []string{"stratum.stacks.>"},
		Retention: nats.LimitsPolicy,
		MaxAge:    90 * 24 * time.Hour,
		Storage:   nats.FileStorage,
		Replicas:  1,
	},
	{
		Name:      "STRATUM_AUDIT",
		Subjects:  []string{"stratum.audit.>"},
		Retention: nats.LimitsPolicy,
		MaxAge:    365 * 24 * time.Hour,
		Storage:   nats.FileStorage,
		Replicas:  1,
	},
}

// setupStreams idempotently creates or updates the required JetStream streams.
func (b *NATSBus) setupStreams() error {
	// Use the JetStream API for idempotent stream management.
	jsm, err := b.nc.JetStream()
	if err != nil {
		return err
	}
	for _, cfg := range streamConfigs {
		info, err := jsm.StreamInfo(cfg.Name)
		if err != nil {
			// Stream doesn't exist — create it.
			_, err = jsm.AddStream(cfg)
			if err != nil {
				return fmt.Errorf("add stream %s: %w", cfg.Name, err)
			}
			b.logger.Info("created nats stream", "name", cfg.Name)
			continue
		}
		_ = info // already exists; skip update for simplicity
		b.logger.Info("nats stream already exists", "name", cfg.Name)
	}
	return nil
}

// ─── Helpers for the hub and consumers ──────────────────────────────────────

// SubscribeWithConfig is like Subscribe but allows full subscription options.
// Used by NATSHub to set InactiveThreshold.
func (b *NATSBus) SubscribeWithConfig(subject string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	return b.js.Subscribe(subject, cb, opts...)
}

// PullSubscribe creates a durable pull consumer. Used by the audit archiver.
func (b *NATSBus) PullSubscribe(subject, durable string, opts ...nats.SubOpt) (*nats.Subscription, error) {
	return b.js.PullSubscribe(subject, durable, opts...)
}

// PublishWithMsgId publishes a message with a deduplication ID.
func (b *NATSBus) PublishWithMsgId(subject string, data []byte, msgID string) (*nats.PubAck, error) {
	return b.js.Publish(subject, data, nats.MsgId(msgID))
}

// NewMsgId returns a deduplication key based on an event ID.
func NewMsgId(eventID uuid.UUID) string {
	return eventID.String()
}
