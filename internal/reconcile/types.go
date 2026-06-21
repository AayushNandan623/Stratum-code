// Package reconcile implements drift detection and reconciliation for stacks.
// It provides schedule management, drift detection via drift-detect runs, and
// remediation mode handling (NONE, NOTIFY, AUTO_PLAN, AUTO_APPLY).
package reconcile

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ─── Domain types ───────────────────────────────────────────────────────────

// DriftMode controls how the system responds to detected drift.
type DriftMode string

const (
	DriftModeNone     DriftMode = "NONE"
	DriftModeNotify   DriftMode = "NOTIFY"
	DriftModeAutoPlan DriftMode = "AUTO_PLAN"
	DriftModeAutoApply DriftMode = "AUTO_APPLY"
)

// DriftStatus is the lifecycle state of a drift record.
type DriftStatus string

const (
	DriftStatusDetected    DriftStatus = "DETECTED"
	DriftStatusRemediating DriftStatus = "REMEDIATING"
	DriftStatusResolved    DriftStatus = "RESOLVED"
	DriftStatusIgnored     DriftStatus = "IGNORED"
)

// ReconcileSchedule is the per-stack schedule for drift detection.
type ReconcileSchedule struct {
	StackID            uuid.UUID  `json:"stack_id"`
	OrgID              uuid.UUID  `json:"org_id"`
	Enabled            bool       `json:"enabled"`
	ReconcileInterval  Duration   `json:"reconcile_interval"`
	DriftMode          DriftMode  `json:"drift_mode"`
	NextCheckAt        time.Time  `json:"next_check_at"`
	LastCheckAt        *time.Time `json:"last_check_at,omitempty"`
	LastDriftAt        *time.Time `json:"last_drift_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// DriftRecord tracks a single drift detection event and its resolution.
type DriftRecord struct {
	ID               uuid.UUID       `json:"id"`
	StackID          uuid.UUID       `json:"stack_id"`
	OrgID            uuid.UUID       `json:"org_id"`
	TriggerRunID     uuid.UUID       `json:"trigger_run_id"`
	Status           DriftStatus     `json:"status"`
	ResourceCount    int             `json:"resource_count"`
	DriftSummary     json.RawMessage `json:"drift_summary"`
	RemediationRunID *uuid.UUID      `json:"remediation_run_id,omitempty"`
	DetectedAt       time.Time       `json:"detected_at"`
	ResolvedAt       *time.Time      `json:"resolved_at,omitempty"`
	IgnoredAt        *time.Time      `json:"ignored_at,omitempty"`
	IgnoredBy        *uuid.UUID      `json:"ignored_by,omitempty"`
}

// ─── Input types ────────────────────────────────────────────────────────────

// UpdateScheduleInput holds optional fields for patching a reconcile schedule.
type UpdateScheduleInput struct {
	Enabled           *bool
	ReconcileInterval *Duration
	DriftMode         *DriftMode
}

// DriftFilter specifies filter criteria for listing drift records.
type DriftFilter struct {
	StackID *uuid.UUID
	OrgID   uuid.UUID
	Status  *DriftStatus
	Page    int
	Size    int
}

// Duration is a JSON-compatible duration that serializes as seconds.
// The DB stores INTERVAL values; we scan them as seconds and convert.
type Duration time.Duration

func (d Duration) Seconds() float64 { return time.Duration(d).Seconds() }

// MarshalJSON serializes Duration as a number of seconds.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).Seconds())
}

// UnmarshalJSON deserializes Duration from a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return err
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}
