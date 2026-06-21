package consumers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/yourorg/stratum/internal/reconcile"
)

// stackEvent is a minimal decode target for stack events.
type stackEvent struct {
	StackID   uuid.UUID `json:"stack_id"`
	OrgID     uuid.UUID `json:"org_id"`
	EventType string    `json:"event_type"`
}

// ReconcileTriggerConsumer listens for stack.config_updated events and
// triggers an immediate drift-detect run. This gives near-instant reconciliation
// after a variable change or VCS push rather than waiting for the schedule.
type ReconcileTriggerConsumer struct {
	reconcileSvc reconcile.ReconcileService
	js           nats.JetStreamContext
	logger       *slog.Logger
}

// NewReconcileTriggerConsumer creates a ready ReconcileTriggerConsumer.
func NewReconcileTriggerConsumer(reconcileSvc reconcile.ReconcileService, js nats.JetStreamContext, logger *slog.Logger) *ReconcileTriggerConsumer {
	return &ReconcileTriggerConsumer{
		reconcileSvc: reconcileSvc,
		js:           js,
		logger:       logger,
	}
}

// Start begins consuming from stratum.stacks.events.>. Blocks until ctx is
// cancelled.
func (c *ReconcileTriggerConsumer) Start(ctx context.Context) error {
	_, err := c.js.Subscribe("stratum.stacks.events.>", func(msg *nats.Msg) {
		if err := c.handle(ctx, msg); err != nil {
			c.logger.Error("reconcile trigger: handle error", "error", err)
			msg.Nak()
			return
		}
		msg.Ack()
	},
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		return err
	}

	c.logger.Info("reconcile trigger consumer started")
	<-ctx.Done()
	return nil
}

func (c *ReconcileTriggerConsumer) handle(ctx context.Context, msg *nats.Msg) error {
	var ev stackEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		return err
	}
	if ev.EventType != "stack.config_updated" {
		return nil // not an event we care about
	}

	c.logger.Info("reconcile trigger: config updated, triggering drift detect",
		"stack_id", ev.StackID,
	)
	_, err := c.reconcileSvc.TriggerNow(ctx, ev.StackID, uuid.Nil) // system trigger (uuid.Nil = no actor)
	return err
}
