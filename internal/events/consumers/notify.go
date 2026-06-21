package consumers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nats-io/nats.go"

	"github.com/yourorg/stratum/internal/events"
)

// NotificationRouter subscribes to drift notifications and run outcome events
// and sends formatted messages to a Slack webhook. When STRATUM_SLACK_WEBHOOK_URL
// is unset it logs notifications to stdout.
type NotificationRouter struct {
	js       nats.JetStreamContext
	slackURL string // empty = disabled
	logger   *slog.Logger
}

// NewNotificationRouter creates a ready NotificationRouter.
func NewNotificationRouter(js nats.JetStreamContext, slackURL string, logger *slog.Logger) *NotificationRouter {
	return &NotificationRouter{js: js, slackURL: slackURL, logger: logger}
}

// Start begins consuming from the relevant subjects. Blocks until ctx is
// cancelled.
func (n *NotificationRouter) Start(ctx context.Context) error {
	// Subscribe to drift notifications.
	driftSub, err := n.js.Subscribe("stratum.stacks.drifted.>", func(msg *nats.Msg) {
		n.handleDriftMessage(ctx, msg)
	}, nats.DeliverNew(), nats.AckExplicit())
	if err != nil {
		return fmt.Errorf("notify subscribe drift: %w", err)
	}
	defer driftSub.Unsubscribe()

	// Subscribe to run outcome events (run.failed, run.applied).
	runSub, err := n.js.Subscribe("stratum.runs.events.>", func(msg *nats.Msg) {
		n.handleRunMessage(ctx, msg)
	}, nats.DeliverNew(), nats.AckExplicit())
	if err != nil {
		return fmt.Errorf("notify subscribe runs: %w", err)
	}
	defer runSub.Unsubscribe()

	n.logger.Info("notification router started")
	<-ctx.Done()
	return nil
}

func (n *NotificationRouter) handleRunMessage(ctx context.Context, msg *nats.Msg) {
	var event events.RunEventMessage
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		n.logger.Error("notify: unmarshal run event", "error", err)
		msg.Nak()
		return
	}

	var text string
	switch event.EventType {
	case "run.failed":
		text = fmt.Sprintf(":red_circle: Run *%s* failed", event.RunID)
	case "run.applied":
		text = fmt.Sprintf(":white_check_mark: Run *%s* applied successfully", event.RunID)
	default:
		msg.Ack()
		return // not an event we notify on
	}

	if err := n.sendSlack(ctx, text); err != nil {
		n.logger.Error("notify: send slack", "error", err)
		msg.Nak()
		return
	}
	msg.Ack()
}

func (n *NotificationRouter) handleDriftMessage(ctx context.Context, msg *nats.Msg) {
	var event events.DriftEventMessage
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		n.logger.Error("notify: unmarshal drift event", "error", err)
		msg.Nak()
		return
	}

	var text string
	switch event.Status {
	case "drift.detected":
		text = fmt.Sprintf(":warning: Drift detected on stack *%s*: %d resources changed",
			event.StackName, event.ResourceCount)
	case "drift.resolved":
		text = fmt.Sprintf(":white_check_mark: Drift resolved on stack *%s*", event.StackName)
	default:
		msg.Ack()
		return
	}

	if err := n.sendSlack(ctx, text); err != nil {
		n.logger.Error("notify: send slack", "error", err)
		msg.Nak()
		return
	}
	msg.Ack()
}

func (n *NotificationRouter) sendSlack(ctx context.Context, text string) error {
	if n.slackURL == "" {
		n.logger.Info("notification (slack disabled)", "text", text)
		return nil
	}

	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("marshal slack body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.slackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}
