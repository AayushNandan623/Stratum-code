package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// Repository provides reconcile persistence. It is stateless.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ─── Scans ──────────────────────────────────────────────────────────────────

func scanSchedule(row pgx.Row) (*ReconcileSchedule, error) {
	s := &ReconcileSchedule{}
	var intervalSecs float64
	err := row.Scan(
		&s.StackID, &s.OrgID, &s.Enabled, &intervalSecs,
		&s.DriftMode, &s.NextCheckAt, &s.LastCheckAt,
		&s.LastDriftAt, &s.ConsecutiveFailures, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	s.ReconcileInterval = Duration(time.Duration(intervalSecs) * time.Second)
	return s, nil
}

func scanSchedules(rows pgx.Rows) ([]*ReconcileSchedule, error) {
	var out []*ReconcileSchedule
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanDriftRecord(row pgx.Row) (*DriftRecord, error) {
	r := &DriftRecord{}
	err := row.Scan(
		&r.ID, &r.StackID, &r.OrgID, &r.TriggerRunID,
		&r.Status, &r.ResourceCount, &r.DriftSummary,
		&r.RemediationRunID, &r.DetectedAt, &r.ResolvedAt,
		&r.IgnoredAt, &r.IgnoredBy,
	)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func scanDriftRecords(rows pgx.Rows) ([]*DriftRecord, error) {
	var out []*DriftRecord
	for rows.Next() {
		r, err := scanDriftRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─── Column constants ───────────────────────────────────────────────────────

const scheduleColumns = `stack_id, org_id, enabled,
	EXTRACT(EPOCH FROM reconcile_interval)::float8,
	drift_mode, next_check_at, last_check_at,
	last_drift_at, consecutive_failures, updated_at`

const driftColumns = `id, stack_id, org_id, trigger_run_id,
	status, resource_count, drift_summary,
	remediation_run_id, detected_at, resolved_at,
	ignored_at, ignored_by`

// ─── Schedule queries ───────────────────────────────────────────────────────

func (r *Repository) GetSchedule(ctx context.Context, q db.DBTX, stackID uuid.UUID) (*ReconcileSchedule, error) {
	const sql = `SELECT ` + scheduleColumns + ` FROM reconcile_schedules WHERE stack_id = $1`
	s, err := scanSchedule(q.QueryRow(ctx, sql, stackID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrScheduleNotFound
		}
		return nil, err
	}
	return s, nil
}

func (r *Repository) UpsertSchedule(ctx context.Context, q db.DBTX, stackID, orgID uuid.UUID, interval time.Duration, firstCheckDelay time.Duration) error {
	const sql = `INSERT INTO reconcile_schedules (stack_id, org_id, reconcile_interval, next_check_at)
		VALUES ($1, $2, make_interval(secs => $3), now() + make_interval(secs => $4))
		ON CONFLICT (stack_id) DO NOTHING`
	_, err := q.Exec(ctx, sql, stackID, orgID, interval.Seconds(), firstCheckDelay.Seconds())
	return err
}

func (r *Repository) UpdateSchedule(ctx context.Context, q db.DBTX, stackID uuid.UUID, input UpdateScheduleInput) (*ReconcileSchedule, error) {
	sets := []string{}
	args := []any{}
	idx := 1

	if input.Enabled != nil {
		sets = append(sets, "enabled = $"+itoa(idx))
		args = append(args, *input.Enabled)
		idx++
	}
	if input.ReconcileInterval != nil {
		sets = append(sets, "reconcile_interval = make_interval(secs => $"+itoa(idx)+")")
		args = append(args, input.ReconcileInterval.Seconds())
		idx++
	}
	if input.DriftMode != nil {
		sets = append(sets, "drift_mode = $"+itoa(idx))
		args = append(args, string(*input.DriftMode))
		idx++
	}
	if len(sets) == 0 {
		return r.GetSchedule(ctx, q, stackID)
	}
	sets = append(sets, "updated_at = now()")
	query := `UPDATE reconcile_schedules SET ` + join(sets, ", ") +
		` WHERE stack_id = $` + itoa(idx) + ` RETURNING ` + scheduleColumns
	args = append(args, stackID)

	s, err := scanSchedule(q.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrScheduleNotFound
		}
		return nil, err
	}
	return s, nil
}

func (r *Repository) EnableSchedule(ctx context.Context, q db.DBTX, stackID uuid.UUID) error {
	const sql = `UPDATE reconcile_schedules SET enabled = true, updated_at = now() WHERE stack_id = $1`
	tag, err := q.Exec(ctx, sql, stackID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

func (r *Repository) DisableSchedule(ctx context.Context, q db.DBTX, stackID uuid.UUID) error {
	const sql = `UPDATE reconcile_schedules SET enabled = false, updated_at = now() WHERE stack_id = $1`
	tag, err := q.Exec(ctx, sql, stackID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// ClaimNextDue atomically claims the next due reconcile schedule using SKIP
// LOCKED. It advances next_check_at BEFORE the check runs to prevent runaway
// retry loops on crash. Returns nil if no schedule is due.
func (r *Repository) ClaimNextDue(ctx context.Context, q db.DBTX) (*ReconcileSchedule, error) {
	const sql = `WITH claimed AS (
		SELECT stack_id FROM reconcile_schedules
		WHERE enabled = true AND next_check_at <= now()
		ORDER BY next_check_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE reconcile_schedules
	SET next_check_at = now() + reconcile_interval,
		last_check_at = now()
	WHERE stack_id = (SELECT stack_id FROM claimed)
	RETURNING ` + scheduleColumns

	s, err := scanSchedule(q.QueryRow(ctx, sql))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

func (r *Repository) RecordFailure(ctx context.Context, q db.DBTX, stackID uuid.UUID) error {
	// Exponential backoff: next_check_at = now() + interval * 2^min(failures, 5)
	// Capped at 32x original interval.
	_, err := q.Exec(ctx, `
		UPDATE reconcile_schedules
		SET consecutive_failures = consecutive_failures + 1,
			next_check_at = now() + (reconcile_interval * POWER(2, LEAST(consecutive_failures + 1, 5))),
			updated_at = now()
		WHERE stack_id = $1
	`, stackID)
	return err
}

func (r *Repository) ResetFailures(ctx context.Context, q db.DBTX, stackID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE reconcile_schedules
		SET consecutive_failures = 0, updated_at = now()
		WHERE stack_id = $1
	`, stackID)
	return err
}

func (r *Repository) UpdateLastDriftAt(ctx context.Context, q db.DBTX, stackID uuid.UUID, t *time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE reconcile_schedules SET last_drift_at = $2, updated_at = now()
		WHERE stack_id = $1
	`, stackID, t)
	return err
}

// ─── Drift record queries ───────────────────────────────────────────────────

func (r *Repository) CreateDriftRecord(ctx context.Context, q db.DBTX, rec *DriftRecord) error {
	const sql = `INSERT INTO drift_records
		(id, stack_id, org_id, trigger_run_id, status, resource_count, drift_summary, detected_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + driftColumns
	row := q.QueryRow(ctx, sql,
		rec.ID, rec.StackID, rec.OrgID, rec.TriggerRunID,
		string(rec.Status), rec.ResourceCount, rec.DriftSummary, rec.DetectedAt)
	created, err := scanDriftRecord(row)
	if err != nil {
		return err
	}
	*rec = *created
	return nil
}

func (r *Repository) GetDriftRecord(ctx context.Context, q db.DBTX, id uuid.UUID) (*DriftRecord, error) {
	const sql = `SELECT ` + driftColumns + ` FROM drift_records WHERE id = $1`
	rec, err := scanDriftRecord(q.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDriftRecordNotFound
		}
		return nil, err
	}
	return rec, nil
}

func (r *Repository) ListDriftRecords(ctx context.Context, q db.DBTX, filter DriftFilter) ([]*DriftRecord, int, error) {
	if filter.Size <= 0 {
		filter.Size = 50
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	offset := (filter.Page - 1) * filter.Size

	where := []string{"org_id = $1"}
	args := []any{filter.OrgID}
	idx := 2

	if filter.StackID != nil {
		where = append(where, "stack_id = $"+itoa(idx))
		args = append(args, *filter.StackID)
		idx++
	}
	if filter.Status != nil {
		where = append(where, "status = $"+itoa(idx))
		args = append(args, string(*filter.Status))
		idx++
	}
	whereClause := join(where, " AND ")

	var total int
	err := q.QueryRow(ctx, "SELECT count(*) FROM drift_records WHERE "+whereClause, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + driftColumns + ` FROM drift_records WHERE ` + whereClause +
		` ORDER BY detected_at DESC LIMIT $` + itoa(idx) + ` OFFSET $` + itoa(idx+1)
	args = append(args, filter.Size, offset)
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanDriftRecords(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *Repository) ListOpenDriftRecords(ctx context.Context, q db.DBTX, stackID uuid.UUID) ([]*DriftRecord, error) {
	const sql = `SELECT ` + driftColumns + ` FROM drift_records
		WHERE stack_id = $1 AND status IN ('DETECTED', 'REMEDIATING')
		ORDER BY detected_at DESC`
	rows, err := q.Query(ctx, sql, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDriftRecords(rows)
}

func (r *Repository) SetRemediationRun(ctx context.Context, q db.DBTX, driftID, runID uuid.UUID) error {
	const sql = `UPDATE drift_records
		SET status = 'REMEDIATING', remediation_run_id = $2
		WHERE id = $1`
	_, err := q.Exec(ctx, sql, driftID, runID)
	return err
}

func (r *Repository) ResolveDriftRecord(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	const sql = `UPDATE drift_records
		SET status = 'RESOLVED', resolved_at = now()
		WHERE id = $1 AND status IN ('DETECTED', 'REMEDIATING')`
	tag, err := q.Exec(ctx, sql, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDriftRecordNotFound
	}
	return nil
}

func (r *Repository) IgnoreDriftRecord(ctx context.Context, q db.DBTX, id uuid.UUID, actorID uuid.UUID) error {
	const sql = `UPDATE drift_records
		SET status = 'IGNORED', ignored_at = now(), ignored_by = $2
		WHERE id = $1 AND status IN ('DETECTED', 'REMEDIATING')`
	tag, err := q.Exec(ctx, sql, id, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDriftRecordNotFound
	}
	return nil
}

// ─── Sentinel errors ────────────────────────────────────────────────────────

var (
	ErrScheduleNotFound    = domainerr.New("SCHEDULE_NOT_FOUND", 404, "reconcile schedule not found")
	ErrDriftRecordNotFound = domainerr.New("DRIFT_RECORD_NOT_FOUND", 404, "drift record not found")
)

// ─── Helpers ────────────────────────────────────────────────────────────────

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0' + n/10)) + string(rune('0' + n%10))
}

func join(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}

// buildDriftSummary creates a JSON summary from a plan output.
func buildDriftSummary(changes []ResourceChange, added, changed, removed int) json.RawMessage {
	type change struct {
		Address string   `json:"address"`
		Actions []string `json:"actions"`
	}
	cs := make([]change, len(changes))
	for i, c := range changes {
		cs[i] = change{Address: c.Address, Actions: c.Actions}
	}
	m := map[string]any{
		"resource_changes": cs,
		"total_added":      added,
		"total_changed":    changed,
		"total_removed":    removed,
	}
	b, _ := json.Marshal(m)
	return b
}

// ResourceChange is a local copy of run.ResourceChange to avoid import cycles.
type ResourceChange struct {
	Address string
	Actions []string
}
