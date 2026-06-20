# Phase 6: Event Bus and NATS Integration

## Scope

**IN scope:**
- NATS JetStream setup: stream and consumer definitions in code
- `internal/events/` package: EventBus interface, NATS implementation, in-memory implementation
- Transactional outbox: `outbox_messages` table, relay goroutine
- Replace in-memory WebSocket hub with NATS-backed fan-out (multi-instance safe)
- Audit log archiver consumer (NATS → `audit_log` table)
- Notification router consumer (NATS → Slack webhook stub)
- Event-driven reconcile trigger (stack.applied → immediate drift-detect if configured)
- Run event fan-out to NATS on every state transition
- Stack event fan-out (stack.created, stack.updated, stack.drifted, stack.applied)

**OUT of scope:**
- NATS-based job dispatch replacing PostgreSQL long-poll (evaluate after Phase 6 — optional optimization)
- Full event replay API (post-Phase 6 feature)
- Temporal integration (post-Phase 6)
- Email notifications (post-Phase 6 — Slack stub is sufficient)

---

## Prerequisites

Phase 5 complete and validated. All run lifecycle, policy, and drift detection working end-to-end. In-memory WebSocket hub in place from Phase 2.

---

## Files to Create / Modify

```
internal/events/
  types.go          Event types, domain event structs
  bus.go            EventBus interface
  nats.go           NATS JetStream EventBus implementation
  inmemory.go       In-memory EventBus (already exists from Phase 2 — retain for testing)
  outbox.go         OutboxRelay goroutine
  consumers/
    audit.go        Audit log archiver consumer
    notify.go       Notification router consumer
    reconcile.go    Event-driven reconcile trigger consumer

internal/api/ws/
  hub.go            MODIFY: replace in-memory pub/sub with NATS subscriber per connection

internal/platform/db/
  outbox.go         DB helpers for outbox table (insert, claim, mark delivered)

internal/run/
  service.go        MODIFY: write to outbox on every run event append
  eventstore.go     MODIFY: wrap event append in transaction with outbox write

internal/stack/
  service.go        MODIFY: write to outbox on stack create/update/delete

internal/reconcile/
  service.go        MODIFY: write to outbox on drift.detected, drift.resolved

migrations/
  013_init_outbox.sql
  014_init_audit_log.sql

cmd/stratum-server/
  main.go           MODIFY: start NATS connection, outbox relay, consumers on startup
```

---

## DB Schema Additions

```sql
-- Migration: 013_init_outbox.sql

CREATE TABLE outbox_messages (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  subject       VARCHAR(255) NOT NULL,  -- NATS subject
  payload       JSONB NOT NULL,
  status        VARCHAR(20) NOT NULL DEFAULT 'PENDING',  -- PENDING | DELIVERED | FAILED
  attempt       INT NOT NULL DEFAULT 0,
  max_attempts  INT NOT NULL DEFAULT 5,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deliver_after TIMESTAMPTZ NOT NULL DEFAULT now(),  -- for delayed delivery
  delivered_at  TIMESTAMPTZ
);

CREATE INDEX idx_outbox_pending ON outbox_messages (deliver_after)
  WHERE status = 'PENDING';

-- Migration: 014_init_audit_log.sql

CREATE TABLE audit_log (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL,
  actor_id      UUID,
  actor_type    VARCHAR(20) NOT NULL,  -- USER | API_KEY | WORKER | SYSTEM
  action        VARCHAR(128) NOT NULL, -- "run.applied", "stack.created", "policy.evaluated", etc.
  resource_type VARCHAR(64) NOT NULL,
  resource_id   UUID,
  metadata      JSONB NOT NULL DEFAULT '{}',
  ip_address    INET,
  occurred_at   TIMESTAMPTZ NOT NULL,
  inserted_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_org_time ON audit_log (org_id, occurred_at DESC);
CREATE INDEX idx_audit_log_resource ON audit_log (resource_type, resource_id, occurred_at DESC);
-- No updates or deletes. Partition by org_id HASH(8) in Phase 7+ when volume requires.
```

---

## EventBus Interface

```go
// internal/events/bus.go

type EventBus interface {
    // Publish sends an event to a subject.
    // In production: writes to outbox table (transactional).
    // In tests: delivers synchronously to in-memory subscribers.
    Publish(ctx context.Context, subject string, payload any) error

    // PublishTx publishes within an existing database transaction.
    // The message is durably written to outbox within the transaction.
    // Delivery to NATS happens asynchronously via the OutboxRelay.
    PublishTx(ctx context.Context, tx pgx.Tx, subject string, payload any) error

    // Subscribe registers a handler for a subject pattern.
    // Used internally for consumers (audit, notify, reconcile).
    Subscribe(ctx context.Context, subject string, handler MessageHandler) error

    Close() error
}

type MessageHandler func(ctx context.Context, msg *Message) error

type Message struct {
    ID        string          // NATS message ID (for deduplication)
    Subject   string
    Payload   json.RawMessage
    Timestamp time.Time
}
```

---

## NATS JetStream Setup

```go
// internal/events/nats.go

// Streams defined at startup (idempotent — safe to re-run)
var streams = []nats.StreamConfig{
    {
        Name:       "STRATUM_RUNS",
        Subjects:   []string{"stratum.runs.>"},
        Retention:  nats.LimitsPolicy,
        MaxAge:     30 * 24 * time.Hour,
        Storage:    nats.FileStorage,
        Replicas:   1, // increase to 3 for production cluster
        Duplicates: 2 * time.Minute, // dedup window
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
```

**Subject naming convention:**
```
stratum.runs.events.{run_id}          — run lifecycle events (per-run)
stratum.runs.logs.{run_id}            — log chunks (per-run, not stored in event store)
stratum.stacks.events.{stack_id}      — stack lifecycle events
stratum.stacks.drifted.{org_id}       — drift notifications (for notification router)
stratum.audit.{org_id}                — all audit events for archiver
stratum.reconcile.trigger.{stack_id}  — immediate reconcile requests
```

---

## Outbox Pattern — Transactional Dual Write

The core invariant: **an event is never lost between DB write and NATS delivery**.

```go
// internal/events/outbox.go

// InsertOutboxMessage writes to outbox within a DB transaction.
// Called from domain services that own transactions.
func InsertOutboxMessage(ctx context.Context, tx pgx.Tx, subject string, payload any) error {
    data, err := json.Marshal(payload)
    if err != nil { return err }
    _, err = tx.Exec(ctx, `
        INSERT INTO outbox_messages (subject, payload)
        VALUES ($1, $2)
    `, subject, data)
    return err
}

// OutboxRelay reads pending messages and publishes to NATS.
type OutboxRelay struct {
    db     *pgxpool.Pool
    js     nats.JetStreamContext
    tick   time.Duration  // default: 500ms
    batch  int            // messages per flush: default 50
    logger *slog.Logger
}

func (r *OutboxRelay) Start(ctx context.Context) {
    ticker := time.NewTicker(r.tick)
    for {
        select {
        case <-ticker.C:
            if err := r.flush(ctx); err != nil {
                r.logger.Error("outbox flush error", "err", err)
            }
        case <-ctx.Done():
            // Final flush on shutdown
            flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            r.flush(flushCtx)
            return
        }
    }
}

func (r *OutboxRelay) flush(ctx context.Context) error {
    // Claim batch of pending messages atomically
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
    if err != nil { return err }
    defer rows.Close()

    var msgs []outboxMsg
    for rows.Next() {
        var m outboxMsg
        rows.Scan(&m.ID, &m.Subject, &m.Payload, &m.Attempt)
        msgs = append(msgs, m)
    }

    for _, m := range msgs {
        pubAck, err := r.js.Publish(m.Subject, m.Payload,
            nats.MsgId(m.ID.String()), // NATS dedup key
        )
        if err != nil || pubAck == nil {
            // Mark for retry with backoff
            r.db.Exec(ctx, `
                UPDATE outbox_messages
                SET status = 'PENDING',
                    deliver_after = now() + ($1 * interval '1 second')
                WHERE id = $2
            `, min(30*m.Attempt, 300), m.ID) // 30s, 60s, 90s... up to 300s
            continue
        }
        // Mark delivered
        r.db.Exec(ctx, `
            UPDATE outbox_messages
            SET status = 'DELIVERED', delivered_at = now()
            WHERE id = $1
        `, m.ID)
    }
    return nil
}
```

---

## Modifying Domain Services to Use Outbox

Every domain event emission changes from direct in-memory publish to transactional outbox write:

```go
// BEFORE (Phase 2-5 — in-memory, not durable):
// s.events.Publish(ctx, "run.applied", payload)

// AFTER (Phase 6 — outbox within the run transition transaction):
func (s *runService) AppendEvent(ctx context.Context, runID uuid.UUID, input RunEventInput) error {
    return s.db.WithTx(ctx, func(tx pgx.Tx) error {
        // 1. Insert run event
        seq, err := s.repo.InsertEvent(ctx, tx, runID, input)
        if err != nil { return err }

        // 2. Update run current_state
        if err := s.repo.UpdateState(ctx, tx, runID, input.DerivedState()); err != nil {
            return err
        }

        // 3. Insert outbox message — same transaction
        subject := fmt.Sprintf("stratum.runs.events.%s", runID)
        return events.InsertOutboxMessage(ctx, tx, subject, RunEventMessage{
            RunID:     runID,
            Seq:       seq,
            EventType: input.Type,
            Payload:   input.Payload,
            OccurredAt: input.OccurredAt,
        })
    })
}
```

This is the key transaction boundary: run_events row + outbox row written atomically. NATS delivery is decoupled and handled by the relay.

---

## WebSocket Hub — NATS-Backed

Replace the in-memory hub from Phase 2:

```go
// internal/api/ws/hub.go

type NATSHub struct {
    js     nats.JetStreamContext
    logger *slog.Logger
}

// SubscribeToRun subscribes a WebSocket client to a run's event stream.
// The client receives:
//   1. Historical events (from run_events table, via API) on connect — handled by caller
//   2. Future events via NATS push subscription
func (h *NATSHub) SubscribeToRun(ctx context.Context, runID uuid.UUID, send func([]byte)) (func(), error) {
    subject := fmt.Sprintf("stratum.runs.events.%s", runID)

    sub, err := h.js.Subscribe(subject, func(msg *nats.Msg) {
        msg.Ack()
        send(msg.Data)
    },
        nats.DeliverNew(),          // only future messages — history sent on connect
        nats.AckExplicit(),
        nats.MaxDeliver(1),
        nats.InactiveThreshold(5*time.Minute),
    )
    if err != nil { return nil, err }

    // Return cleanup func
    return func() { sub.Unsubscribe() }, nil
}
```

**Multi-instance correctness:** Each API instance creates its own NATS subscription per connected WebSocket client. NATS delivers the message to all subscribers across all instances — every connected client receives the event regardless of which API instance they connected to.

---

## Audit Log Archiver Consumer

```go
// internal/events/consumers/audit.go

type AuditArchiver struct {
    db     *pgxpool.Pool
    js     nats.JetStreamContext
    logger *slog.Logger
}

func (a *AuditArchiver) Start(ctx context.Context) error {
    // Durable pull consumer — survives restarts, processes each message once
    sub, err := a.js.PullSubscribe("stratum.audit.>", "audit-archiver",
        nats.BindStream("STRATUM_AUDIT"),
        nats.AckExplicit(),
        nats.MaxDeliver(3),
    )
    if err != nil { return err }

    for {
        msgs, err := sub.Fetch(50, nats.MaxWait(5*time.Second))
        if errors.Is(err, nats.ErrTimeout) { continue }
        if err != nil {
            if ctx.Err() != nil { return nil }
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
    var event AuditEvent
    if err := json.Unmarshal(msg.Data, &event); err != nil { return err }

    _, err := a.db.Exec(ctx, `
        INSERT INTO audit_log (id, org_id, actor_id, actor_type, action, resource_type, resource_id, metadata, occurred_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (id) DO NOTHING  -- idempotent: NATS at-least-once
    `, event.ID, event.OrgID, event.ActorID, event.ActorType,
       event.Action, event.ResourceType, event.ResourceID, event.Metadata, event.OccurredAt)
    return err
}
```

---

## Notification Router Consumer (Slack Stub)

```go
// internal/events/consumers/notify.go

// Subscribes to: stratum.stacks.drifted.> and stratum.runs.events.> (for run.failed, run.applied)
// Phase 6: logs notifications to stdout and optionally POSTs to a configured Slack webhook URL

type NotificationRouter struct {
    js          nats.JetStreamContext
    slackURL    string  // STRATUM_SLACK_WEBHOOK_URL env var (empty = disabled)
    logger      *slog.Logger
}

func (n *NotificationRouter) handleRunFailed(ctx context.Context, event RunEvent) error {
    msg := fmt.Sprintf(":red_circle: Run *%s* failed on stack *%s*", event.RunID, event.StackName)
    return n.sendSlack(ctx, msg)
}

func (n *NotificationRouter) handleDriftDetected(ctx context.Context, event DriftEvent) error {
    msg := fmt.Sprintf(":warning: Drift detected on stack *%s*: %d resources changed",
        event.StackName, event.ResourceCount)
    return n.sendSlack(ctx, msg)
}

func (n *NotificationRouter) sendSlack(ctx context.Context, text string) error {
    if n.slackURL == "" {
        n.logger.Info("notification (slack disabled)", "text", text)
        return nil
    }
    body, _ := json.Marshal(map[string]string{"text": text})
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost, n.slackURL, bytes.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    resp, err := http.DefaultClient.Do(req)
    if err != nil { return err }
    defer resp.Body.Close()
    if resp.StatusCode != 200 { return fmt.Errorf("slack webhook returned %d", resp.StatusCode) }
    return nil
}
```

---

## Event-Driven Reconcile Trigger

When a stack is updated (new variable, config change), immediately queue a drift-detect rather than waiting for the schedule:

```go
// internal/events/consumers/reconcile.go

type ReconcileTriggerConsumer struct {
    reconcileSvc reconcile.ReconcileService
    js           nats.JetStreamContext
}

// Subscribes to: stratum.stacks.events.>
// On stack.config_updated: call reconcileSvc.TriggerNow()

func (c *ReconcileTriggerConsumer) handle(ctx context.Context, msg *nats.Msg) error {
    var event StackEvent
    json.Unmarshal(msg.Data, &event)
    if event.Type != "stack.config_updated" { return nil }
    _, err := c.reconcileSvc.TriggerNow(ctx, event.StackID, uuid.Nil) // system trigger
    return err
}
```

---

## Startup Wiring

```go
// cmd/stratum-server/main.go additions for Phase 6

// Connect NATS
nc, err := nats.Connect(cfg.NATSUrl,
    nats.RetryOnFailedConnect(true),
    nats.MaxReconnects(-1), // reconnect forever
    nats.ReconnectWait(2*time.Second),
)
js, _ := nc.JetStream()

// Ensure streams exist
setupStreams(js)

// Start outbox relay
relay := &events.OutboxRelay{db: pool, js: js, tick: 500*time.Millisecond, batch: 50}
go relay.Start(ctx)

// Start consumers
auditArchiver := &consumers.AuditArchiver{db: pool, js: js}
go auditArchiver.Start(ctx)

notifyRouter := &consumers.NotificationRouter{js: js, slackURL: cfg.SlackWebhookURL}
go notifyRouter.Start(ctx)

reconcileTrigger := &consumers.ReconcileTriggerConsumer{reconcileSvc: reconcileSvc, js: js}
go reconcileTrigger.Start(ctx)

// Replace in-memory hub with NATS hub
wsHub := &ws.NATSHub{js: js}
```

---

## Configuration Additions

```
STRATUM_NATS_URL              nats://localhost:4222  (required from Phase 6)
STRATUM_SLACK_WEBHOOK_URL     https://hooks.slack.com/... (optional)
STRATUM_OUTBOX_TICK_MS        500  (default)
STRATUM_OUTBOX_BATCH_SIZE     50   (default)
```

---

## Validation Criteria

After Phase 6:

1. **Outbox durability:** Stop NATS. Create a run, let it transition through states. Verify outbox_messages table has PENDING entries. Restart NATS. Within 2s, entries are delivered and marked DELIVERED.

2. **Multi-instance WebSocket:** Start two stratum-server instances. Connect WebSocket to instance 1 for run_id R. Create a run event via instance 2's API. Verify WebSocket on instance 1 receives the event.

3. **Audit log:** Create a stack, create a run, apply it. Query audit_log: verify entries for stack.created, run.created, run.applied. All from NATS consumer, not inline writes.

4. **Drift notification:** Configure STRATUM_SLACK_WEBHOOK_URL to a test endpoint. Trigger drift. Verify Slack message is received with correct stack name and resource count.

5. **Event-driven reconcile:** Update a stack variable. Without waiting for the schedule interval, verify a drift-detect run is created within 10 seconds (event-driven trigger).

6. **At-least-once deduplication:** Manually insert a duplicate outbox message with the same NATS MsgId. Verify audit_log receives only one entry (ON CONFLICT DO NOTHING).
