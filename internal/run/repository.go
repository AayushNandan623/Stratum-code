package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// Repository provides run persistence. It is stateless.
type Repository struct{}

// NewRepository returns a ready run repository.
func NewRepository() *Repository { return &Repository{} }

// ─── Scans ──────────────────────────────────────────────────────────────────

func scanRun(row pgx.Row) (*Run, error) {
	r := &Run{}
	err := row.Scan(
		&r.ID, &r.OrgID, &r.StackID, &r.SpaceID,
		&r.RunType, &r.CurrentState, &r.TriggerType,
		&r.TriggeredBy, &r.ConfigVersion,
		&r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

func scanRuns(rows pgx.Rows) ([]*Run, error) {
	var out []*Run
	for rows.Next() {
		r := &Run{}
		err := rows.Scan(
			&r.ID, &r.OrgID, &r.StackID, &r.SpaceID,
			&r.RunType, &r.CurrentState, &r.TriggerType,
			&r.TriggeredBy, &r.ConfigVersion,
			&r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanJob(row pgx.Row) (*RunJob, error) {
	j := &RunJob{}
	err := row.Scan(
		&j.ID, &j.RunID, &j.PoolID, &j.Status,
		&j.ClaimedBy, &j.ClaimedAt, &j.ExpiresAt,
		&j.Attempt, &j.CreatedAt,
	)
	return j, err
}

func scanJobs(rows pgx.Rows) ([]*RunJob, error) {
	var out []*RunJob
	for rows.Next() {
		j := &RunJob{}
		err := rows.Scan(
			&j.ID, &j.RunID, &j.PoolID, &j.Status,
			&j.ClaimedBy, &j.ClaimedAt, &j.ExpiresAt,
			&j.Attempt, &j.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

const runColumns = `id, org_id, stack_id, space_id,
	run_type, current_state, trigger_type,
	triggered_by, COALESCE(config_version, ''),
	created_at, updated_at`

const jobColumns = `id, run_id, pool_id, status,
	claimed_by, claimed_at, expires_at,
	attempt, created_at`

// ─── Runs ───────────────────────────────────────────────────────────────────

// Create inserts a new run and returns it.
func (r *Repository) Create(ctx context.Context, q db.DBTX, in *Run) error {
	const sql = `INSERT INTO runs
		(id, org_id, stack_id, space_id, run_type, current_state, trigger_type, triggered_by, config_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING ` + runColumns
	row := q.QueryRow(ctx, sql,
		in.ID, in.OrgID, in.StackID, in.SpaceID,
		in.RunType, in.CurrentState, in.TriggerType,
		in.TriggeredBy, in.ConfigVersion,
		in.CreatedAt, in.UpdatedAt)
	created, err := scanRun(row)
	if err != nil {
		return err
	}
	*in = *created
	return nil
}

// GetByID fetches a run by its ID. Returns ErrRunNotFound if missing.
func (r *Repository) GetByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*Run, error) {
	const sql = `SELECT ` + runColumns + ` FROM runs WHERE id = $1`
	run, err := scanRun(pool.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return run, nil
}

// GetByIDTx fetches a run by ID within a transaction.
func (r *Repository) GetByIDTx(ctx context.Context, q db.DBTX, id uuid.UUID) (*Run, error) {
	const sql = `SELECT ` + runColumns + ` FROM runs WHERE id = $1`
	run, err := scanRun(q.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return run, nil
}

// LockRun fetches a run and locks the row for update. Must be called within a
// transaction.
func (r *Repository) LockRun(ctx context.Context, q db.DBTX, id uuid.UUID) (*Run, error) {
	const sql = `SELECT ` + runColumns + ` FROM runs WHERE id = $1 FOR UPDATE`
	run, err := scanRun(q.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	return run, nil
}

// UpdateState sets the current_state and updated_at for a run.
func (r *Repository) UpdateState(ctx context.Context, q db.DBTX, id uuid.UUID, state RunState) error {
	tag, err := q.Exec(ctx,
		`UPDATE runs SET current_state = $2, updated_at = now() WHERE id = $1`,
		id, string(state))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

// ListByState returns all runs with the given state.
func (r *Repository) ListByState(ctx context.Context, pool *pgxpool.Pool, state RunState) ([]*Run, error) {
	const sql = `SELECT ` + runColumns + ` FROM runs WHERE current_state = $1 ORDER BY created_at`
	rows, err := pool.Query(ctx, sql, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// ListByStateTx is like ListByState but accepts a DBTX.
func (r *Repository) ListByStateTx(ctx context.Context, q db.DBTX, state RunState) ([]*Run, error) {
	const sql = `SELECT ` + runColumns + ` FROM runs WHERE current_state = $1 ORDER BY created_at`
	rows, err := q.Query(ctx, sql, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuns(rows)
}

// List returns a paginated, filtered list of runs and the total count.
func (r *Repository) List(ctx context.Context, pool *pgxpool.Pool, filter RunFilter) ([]*Run, int, error) {
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
		where = append(where, "stack_id = $"+strconv.Itoa(idx))
		args = append(args, *filter.StackID)
		idx++
	}
	if len(filter.States) > 0 {
		placeholders := make([]string, len(filter.States))
		for i, s := range filter.States {
			placeholders[i] = "$" + strconv.Itoa(idx)
			args = append(args, string(s))
			idx++
		}
		where = append(where, "current_state IN ("+strings.Join(placeholders, ",")+")")
	}
	if filter.TriggerType != nil {
		where = append(where, "trigger_type = $"+strconv.Itoa(idx))
		args = append(args, string(*filter.TriggerType))
		idx++
	}

	whereClause := strings.Join(where, " AND ")

	var total int
	err := pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE "+whereClause, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	args = append(args, filter.Size, offset)
	query := `SELECT ` + runColumns + ` FROM runs WHERE ` + whereClause +
		` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(idx) + ` OFFSET $` + strconv.Itoa(idx+1)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanRuns(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// HasActiveRun returns true if any run for the stack is in a non-terminal state.
func (r *Repository) HasActiveRun(ctx context.Context, pool *pgxpool.Pool, stackID uuid.UUID) (bool, error) {
	nonTerminal := []string{
		string(StatePending), string(StateQueued), string(StateAssigned),
		string(StatePlanning), string(StatePlanned), string(StateAwaitingApproval),
		string(StateApplying),
	}
	const sql = `SELECT EXISTS (
		SELECT 1 FROM runs WHERE stack_id = $1 AND current_state = ANY($2)
	)`
	var exists bool
	err := pool.QueryRow(ctx, sql, stackID, nonTerminal).Scan(&exists)
	return exists, err
}

// HasActiveRunExcluding returns true if any run for the stack OTHER than
// excludeID is in a non-terminal state. Used by the scheduler to prevent
// queueing a second run on a stack that already has an active run, without
// blocking the run being considered.
func (r *Repository) HasActiveRunExcluding(ctx context.Context, pool *pgxpool.Pool, stackID, excludeID uuid.UUID) (bool, error) {
	nonTerminal := []string{
		string(StatePending), string(StateQueued), string(StateAssigned),
		string(StatePlanning), string(StatePlanned), string(StateAwaitingApproval),
		string(StateApplying),
	}
	const sql = `SELECT EXISTS (
		SELECT 1 FROM runs WHERE stack_id = $1 AND id != $2 AND current_state = ANY($3)
	)`
	var exists bool
	err := pool.QueryRow(ctx, sql, stackID, excludeID, nonTerminal).Scan(&exists)
	return exists, err
}

// HasAppliedRun returns true if the stack has at least one run in APPLIED
// state. Used by the scheduler to check whether an upstream dependency has
// ever been successfully deployed.
func (r *Repository) HasAppliedRun(ctx context.Context, pool *pgxpool.Pool, stackID uuid.UUID) (bool, error) {
	const sql = `SELECT EXISTS (
		SELECT 1 FROM runs WHERE stack_id = $1 AND current_state = $2
	)`
	var exists bool
	err := pool.QueryRow(ctx, sql, stackID, string(StateApplied)).Scan(&exists)
	return exists, err
}

// ─── Run jobs ───────────────────────────────────────────────────────────────

// CreateJob inserts a new run_job row.
func (r *Repository) CreateJob(ctx context.Context, q db.DBTX, job *RunJob) error {
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	if job.Status == "" {
		job.Status = "AVAILABLE"
	}
	if job.ExpiresAt.IsZero() {
		job.ExpiresAt = time.Now().Add(60 * time.Second)
	}
	const sql = `INSERT INTO run_jobs (id, run_id, pool_id, status, claimed_by, claimed_at, expires_at, attempt, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + jobColumns
	row := q.QueryRow(ctx, sql,
		job.ID, job.RunID, job.PoolID, job.Status,
		job.ClaimedBy, job.ClaimedAt, job.ExpiresAt,
		job.Attempt, job.CreatedAt)
	created, err := scanJob(row)
	if err != nil {
		return err
	}
	*job = *created
	return nil
}

// ClaimJob atomically claims an AVAILABLE job using SELECT FOR UPDATE SKIP
// LOCKED and returns the claimed job. Returns ErrNoJobAvailable if none found.
func (r *Repository) ClaimJob(ctx context.Context, q db.DBTX, workerID uuid.UUID, timeout time.Duration) (*RunJob, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	const sql = `UPDATE run_jobs SET
		status = 'CLAIMED',
		claimed_by = $1,
		claimed_at = NOW(),
		expires_at = NOW() + $2::interval
		WHERE id = (
			SELECT id FROM run_jobs
			WHERE status = 'AVAILABLE'
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING ` + jobColumns
	job, err := scanJob(q.QueryRow(ctx, sql, workerID, fmt.Sprintf("%.0f seconds", timeout.Seconds())))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoJobAvailable
		}
		return nil, err
	}
	return job, nil
}

// ListTimedOutJobs returns jobs that are past their expires_at (aka CLAIMED
// but never completed).
func (r *Repository) ListTimedOutJobs(ctx context.Context, pool *pgxpool.Pool) ([]*RunJob, error) {
	const sql = `SELECT ` + jobColumns + ` FROM run_jobs
		WHERE status = 'CLAIMED' AND expires_at < NOW()
		ORDER BY expires_at`
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// RequeueJob resets a job to AVAILABLE with an incremented attempt count and
// fresh expiry.
func (r *Repository) RequeueJob(ctx context.Context, q db.DBTX, job *RunJob) error {
	job.Status = "AVAILABLE"
	job.ClaimedBy = nil
	job.ClaimedAt = nil
	job.ExpiresAt = time.Now().Add(60 * time.Second)
	const sql = `UPDATE run_jobs SET
		status = 'AVAILABLE',
		claimed_by = NULL,
		claimed_at = NULL,
		expires_at = $2,
		attempt = $3
		WHERE id = $1
		RETURNING ` + jobColumns
	row := q.QueryRow(ctx, sql, job.ID, job.ExpiresAt, job.Attempt)
	updated, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunJobNotFound
		}
		return err
	}
	*job = *updated
	return nil
}

// ─── Run logs ───────────────────────────────────────────────────────────────

// InsertLogLines inserts a batch of log lines with sequential seq numbers
// starting from the next available value. Returns the number of lines inserted.
func (r *Repository) InsertLogLines(ctx context.Context, q db.DBTX, runID uuid.UUID, lines []LogLine) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	var startSeq int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM run_logs WHERE run_id = $1`,
		runID).Scan(&startSeq)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for i, line := range lines {
		seq := startSeq + int64(i)
		oc := line.OccurredAt
		if oc.IsZero() {
			oc = now
		}
		const sql = `INSERT INTO run_logs (id, run_id, seq, line, source, occurred_at, inserted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`
		_, err := q.Exec(ctx, sql,
			uuid.New(), runID, seq, line.Line, line.Source, oc, now)
		if err != nil {
			return i, err
		}
	}
	return len(lines), nil
}

// ListLogLines returns a paginated page of log lines ordered by seq.
func (r *Repository) ListLogLines(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, page Pagination) ([]*LogLine, int, error) {
	if page.Size <= 0 {
		page.Size = 100
	}
	if page.Page <= 0 {
		page.Page = 1
	}
	offset := (page.Page - 1) * page.Size

	var total int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM run_logs WHERE run_id = $1`, runID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	const sql = `SELECT line, source, occurred_at FROM run_logs
		WHERE run_id = $1 ORDER BY seq LIMIT $2 OFFSET $3`
	rows, err := pool.Query(ctx, sql, runID, page.Size, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*LogLine
	for rows.Next() {
		ll := &LogLine{}
		if err := rows.Scan(&ll.Line, &ll.Source, &ll.OccurredAt); err != nil {
			return nil, 0, err
		}
		out = append(out, ll)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ─── Plan outputs ───────────────────────────────────────────────────────────

// StorePlanOutput saves or updates the plan output JSONB for a run.
func (r *Repository) StorePlanOutput(ctx context.Context, q db.DBTX, runID uuid.UUID, output *PlanOutput) error {
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	const sql = `UPDATE runs SET plan_output = $2, updated_at = now() WHERE id = $1`
	tag, err := q.Exec(ctx, sql, runID, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

// GetPlanOutput retrieves the stored plan output for a run.
func (r *Repository) GetPlanOutput(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) (*PlanOutput, error) {
	const sql = `SELECT plan_output FROM runs WHERE id = $1`
	var raw []byte
	err := pool.QueryRow(ctx, sql, runID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var out PlanOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ─── Sentinel errors ────────────────────────────────────────────────────────

var (
	ErrRunNotFound     = domainerr.New("RUN_NOT_FOUND", 404, "run not found")
	ErrRunJobNotFound  = domainerr.New("RUN_JOB_NOT_FOUND", 404, "run job not found")
	ErrNoJobAvailable  = domainerr.New("NO_JOB_AVAILABLE", 204, "no available job")
)
