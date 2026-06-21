package worker

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/stratum/internal/platform/db"
)

// Repository provides worker and pool persistence. It is stateless.
type Repository struct{}

// NewRepository returns a ready worker repository.
func NewRepository() *Repository { return &Repository{} }

// ─── Scans ──────────────────────────────────────────────────────────────────

func scanPool(row pgx.Row) (*WorkerPool, error) {
	p := &WorkerPool{}
	err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.PoolType, &p.TokenHash,
		&p.MaxConcurrency, &p.Labels, &p.CreatedAt, &p.DeletedAt)
	return p, err
}

func scanPools(rows pgx.Rows) ([]*WorkerPool, error) {
	var out []*WorkerPool
	for rows.Next() {
		p := &WorkerPool{}
		err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.PoolType, &p.TokenHash,
			&p.MaxConcurrency, &p.Labels, &p.CreatedAt, &p.DeletedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanWorker(row pgx.Row) (*Worker, error) {
	w := &Worker{}
	err := row.Scan(&w.ID, &w.PoolID, &w.OrgID, &w.Hostname, &w.Version,
		&w.Capabilities, &w.Status, &w.TokenHash, &w.LastHeartbeat,
		&w.CurrentRunID, &w.RegisteredAt)
	return w, err
}

func scanWorkers(rows pgx.Rows) ([]*Worker, error) {
	var out []*Worker
	for rows.Next() {
		w := &Worker{}
		err := rows.Scan(&w.ID, &w.PoolID, &w.OrgID, &w.Hostname, &w.Version,
			&w.Capabilities, &w.Status, &w.TokenHash, &w.LastHeartbeat,
			&w.CurrentRunID, &w.RegisteredAt)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

const poolColumns = `id, org_id, name, pool_type, token_hash, max_concurrency, labels, created_at, deleted_at`
const workerColumns = `id, pool_id, org_id, hostname, version, capabilities, status, token_hash, last_heartbeat, current_run_id, registered_at`

// ─── Pools ──────────────────────────────────────────────────────────────────

// CreatePool inserts a new worker pool.
func (r *Repository) CreatePool(ctx context.Context, q db.DBTX, pool *WorkerPool) error {
	const sql = `INSERT INTO worker_pools (id, org_id, name, pool_type, token_hash, max_concurrency, labels, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + poolColumns
	created, err := scanPool(q.QueryRow(ctx, sql,
		pool.ID, pool.OrgID, pool.Name, pool.PoolType, pool.TokenHash,
		pool.MaxConcurrency, pool.Labels, pool.CreatedAt))
	if err != nil {
		return err
	}
	*pool = *created
	return nil
}

// GetPool fetches a pool by ID. Returns ErrPoolNotFound if missing.
func (r *Repository) GetPool(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*WorkerPool, error) {
	const sql = `SELECT ` + poolColumns + ` FROM worker_pools WHERE id = $1 AND deleted_at IS NULL`
	p, err := scanPool(pool.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPoolNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListPools returns all non-deleted pools for an org.
func (r *Repository) ListPools(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) ([]*WorkerPool, error) {
	const sql = `SELECT ` + poolColumns + ` FROM worker_pools WHERE org_id = $1 AND deleted_at IS NULL ORDER BY created_at`
	rows, err := pool.Query(ctx, sql, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPools(rows)
}

// DeletePool soft-deletes a pool.
func (r *Repository) DeletePool(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `UPDATE worker_pools SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPoolNotFound
	}
	return nil
}

// UpdatePoolToken updates the token_hash for a pool.
func (r *Repository) UpdatePoolToken(ctx context.Context, q db.DBTX, id uuid.UUID, tokenHash string) error {
	tag, err := q.Exec(ctx, `UPDATE worker_pools SET token_hash = $2 WHERE id = $1 AND deleted_at IS NULL`, id, tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPoolNotFound
	}
	return nil
}

// PoolByName returns a pool by org ID and name (for uniqueness check).
func (r *Repository) PoolByName(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, name string) (*WorkerPool, error) {
	const sql = `SELECT ` + poolColumns + ` FROM worker_pools WHERE org_id = $1 AND name = $2 AND deleted_at IS NULL`
	p, err := scanPool(pool.QueryRow(ctx, sql, orgID, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPoolNotFound
		}
		return nil, err
	}
	return p, nil
}

// PoolByTokenHash returns a pool by its token_hash (for worker authentication
// during registration, before a worker record exists).
func (r *Repository) PoolByTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (*WorkerPool, error) {
	const sql = `SELECT ` + poolColumns + ` FROM worker_pools WHERE token_hash = $1 AND deleted_at IS NULL`
	p, err := scanPool(pool.QueryRow(ctx, sql, tokenHash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPoolNotFound
		}
		return nil, err
	}
	return p, nil
}

// ─── Workers ────────────────────────────────────────────────────────────────

// CreateWorker inserts a new worker record.
func (r *Repository) CreateWorker(ctx context.Context, q db.DBTX, w *Worker) error {
	const sql = `INSERT INTO workers (id, pool_id, org_id, hostname, version, capabilities, status, token_hash, last_heartbeat, current_run_id, registered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING ` + workerColumns
	created, err := scanWorker(q.QueryRow(ctx, sql,
		w.ID, w.PoolID, w.OrgID, w.Hostname, w.Version,
		w.Capabilities, w.Status, w.TokenHash, w.LastHeartbeat,
		w.CurrentRunID, w.RegisteredAt))
	if err != nil {
		return err
	}
	*w = *created
	return nil
}

// GetWorkerByID fetches a worker by ID.
func (r *Repository) GetWorkerByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*Worker, error) {
	const sql = `SELECT ` + workerColumns + ` FROM workers WHERE id = $1`
	w, err := scanWorker(pool.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}
	return w, nil
}

// GetWorkerByTokenHash fetches a worker by its token HMAC.
func (r *Repository) GetWorkerByTokenHash(ctx context.Context, pool *pgxpool.Pool, hash string) (*Worker, error) {
	const sql = `SELECT ` + workerColumns + ` FROM workers WHERE token_hash = $1`
	w, err := scanWorker(pool.QueryRow(ctx, sql, hash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorkerNotFound
		}
		return nil, err
	}
	return w, nil
}

// ListActiveWorkers returns all non-deregistered workers for a pool.
func (r *Repository) ListActiveWorkers(ctx context.Context, pool *pgxpool.Pool, poolID uuid.UUID) ([]*Worker, error) {
	const sql = `SELECT ` + workerColumns + ` FROM workers WHERE pool_id = $1 AND status != 'DEREGISTERED' ORDER BY registered_at`
	rows, err := pool.Query(ctx, sql, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkers(rows)
}

// UpdateWorkerHeartbeat updates a worker's status, last_heartbeat, and current_run_id.
func (r *Repository) UpdateWorkerHeartbeat(ctx context.Context, q db.DBTX, id uuid.UUID, status WorkerStatus, currentRunID *uuid.UUID) error {
	tag, err := q.Exec(ctx,
		`UPDATE workers SET status = $2, last_heartbeat = now(), current_run_id = $3 WHERE id = $1`,
		id, string(status), currentRunID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}
	return nil
}

// DeregisterWorker marks a worker as deregistered.
func (r *Repository) DeregisterWorker(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	tag, err := q.Exec(ctx,
		`UPDATE workers SET status = 'DEREGISTERED', current_run_id = NULL WHERE id = $1`,
		id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}
	return nil
}

// ReactivateWorker re-activates a deregistered worker, updating its metadata
// and clearing any stale run state. Used when a worker restarts and reconnects
// with the same pool token.
func (r *Repository) ReactivateWorker(ctx context.Context, q db.DBTX, w *Worker) error {
	const sql = `UPDATE workers SET
		status = $2, hostname = $3, version = $4, capabilities = $5,
		last_heartbeat = $6, current_run_id = NULL
		WHERE id = $1`
	tag, err := q.Exec(ctx, sql,
		w.ID, string(w.Status), w.Hostname, w.Version,
		w.Capabilities, w.LastHeartbeat)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWorkerNotFound
	}
	return nil
}

// ─── Jobs ───────────────────────────────────────────────────────────────────

// GetRunJob fetches the AVAILABLE job for a run, locking it for the given worker.
func (r *Repository) GetRunJob(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID) (*Job, error) {
	const sql = `SELECT j.id, j.run_id, COALESCE(j.pool_id, '00000000-0000-0000-0000-000000000000'), 
		r.run_type, r.stack_id, COALESCE(r.config_version, 'opentofu'), COALESCE(r.config_version, '1.6.0'),
		r.org_id, j.status, j.attempt, j.created_at
		FROM run_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.run_id = $1 AND j.status = 'AVAILABLE'
		LIMIT 1`
	job := &Job{}
	err := pool.QueryRow(ctx, sql, runID).Scan(
		&job.ID, &job.RunID, &job.PoolID, &job.RunType, &job.StackID,
		&job.IACTool, &job.IACVersion, &job.OrgID, &job.Status, &job.Attempt, &job.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoJobAvailable
		}
		return nil, err
	}
	return job, nil
}
