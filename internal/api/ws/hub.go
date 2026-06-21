// Package ws provides NATS-backed WebSocket event streaming. Each WebSocket
// client subscribes to its run's event subject via an ephemeral NATS push
// subscription, receiving only future events. Historical events are served
// through the REST timeline endpoint at connect time.
package ws

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// NATSHub fans out run events to WebSocket clients via per-client NATS push
// subscriptions. It is multi-instance safe: every connected client receives
// events regardless of which API instance they connected to.
type NATSHub struct {
	js     nats.JetStreamContext
	logger *slog.Logger
}

// NewNATSHub returns a ready NATSHub.
func NewNATSHub(js nats.JetStreamContext) *NATSHub {
	return &NATSHub{js: js, logger: slog.Default()}
}

// WithLogger sets the logger.
func (h *NATSHub) WithLogger(logger *slog.Logger) *NATSHub {
	h.logger = logger
	return h
}

// SubscribeToRun creates an ephemeral NATS push subscription for the given
// run_id. It delivers only future messages (DeliverNew) with explicit
// acknowledgements. Returns an unsubscribe function. The send callback is
// invoked for each received message.
func (h *NATSHub) SubscribeToRun(ctx context.Context, runID uuid.UUID, send func([]byte)) (func(), error) {
	subject := fmt.Sprintf("stratum.runs.events.%s", runID)

	sub, err := h.js.Subscribe(subject, func(msg *nats.Msg) {
		msg.Ack()
		send(msg.Data)
	},
		nats.DeliverNew(),       // only future messages — history is REST-served
		nats.AckExplicit(),       // at-least-once delivery
		nats.MaxDeliver(1),      // no retry for WebSocket delivery
		nats.InactiveThreshold(5*time.Minute), // auto-cleanup idle subs
	)
	if err != nil {
		return nil, fmt.Errorf("nats subscribe: %w", err)
	}

	cleanup := func() {
		if err := sub.Unsubscribe(); err != nil {
			h.logger.Error("nats unsubscribe error", "subject", subject, "error", err)
		}
	}
	return cleanup, nil
}

// Subscribe implements the old Hub.Subscribe signature for backward
// compatibility with the EventStream handler. It returns a channel that
// receives messages published to the topic. Topic is the run ID as string.
func (h *NATSHub) Subscribe(topic string) (<-chan []byte, func()) {
	runID, err := uuid.Parse(topic)
	if err != nil {
		// Return closed channel on parse failure.
		ch := make(chan []byte)
		close(ch)
		return ch, func() {}
	}

	ch := make(chan []byte, 64)
	cleanup, err := h.SubscribeToRun(context.Background(), runID, func(data []byte) {
		select {
		case ch <- data:
		default:
			// Slow consumer — drop message.
		}
	})
	if err != nil {
		h.logger.Error("nats subscribe backward compat", "run_id", topic, "error", err)
		close(ch)
		return ch, func() {}
	}

	unsub := func() {
		cleanup()
		close(ch)
	}
	return ch, unsub
}

// In-memory subscriber map for local publishing (maintained for backward compat).
type memorySub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan []byte
}

// Publish sends msg to every local subscriber of topic. Slow or blocked
// subscribers are dropped (non-blocking send). In the NATS-backed hub this
// is a no-op for real publish; kept for backward compat with the EventPublisher
// interface used by the run context.
func (h *NATSHub) Publish(topic string, msg []byte) {
	// In NATS mode, publishing happens through the outbox -> NATS path.
	// This method is kept for the EventPublisher interface but is a no-op;
	// real event distribution occurs via NATS subscriptions.
}

// PublishRunEvent satisfies the run.EventPublisher interface. In NATS mode
// this is a no-op — events are delivered via NATS subscriptions.
func (h *NATSHub) PublishRunEvent(runID string, data []byte) {
	// No-op: events flow through NATS, not through local channels.
}
