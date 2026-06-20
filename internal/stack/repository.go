// Stack repository: persistence for stacks, variables, and dependencies. All
// methods accept a db.DBTX so they run against the pool or a transaction.
package stack

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yourorg/stratum/internal/platform/db"
)

// Repository provides stack persistence. It is stateless.
type Repository struct{}

// NewRepository returns a ready stack repository.
func NewRepository() *Repository { return &Repository{} }

type scanner interface {
	Scan(dest ...any) error
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// scanStack maps a row to a Stack. reconcile_interval is read as seconds.
func scanStack(row scanner) (*Stack, error) {
	s := &Stack{}
	var reconcileSecs int64
	err := row.Scan(
		&s.ID, &s.OrgID, &s.SpaceID, &s.Name, &s.Status,
		&s.VCSRepo, &s.VCSBranch, &s.WorkingDir, &s.IACTool, &s.IACVersion,
		&s.WorkerPoolID, &s.AutoApply, &reconcileSecs, &s.DriftMode,
		&s.CreatedAt, &s.UpdatedAt, &s.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	s.ReconcileInterval = time.Duration(reconcileSecs) * time.Second
	return s, nil
}

const stackColumns = `id, org_id, space_id, name, status,
	COALESCE(vcs_repo, ''), COALESCE(vcs_branch, ''), COALESCE(working_dir, ''),
	COALESCE(iac_tool, ''), COALESCE(iac_version, ''), worker_pool_id, auto_apply,
	EXTRACT(EPOCH FROM reconcile_interval)::bigint, COALESCE(drift_mode, ''),
	created_at, updated_at, deleted_at`

// Create inserts a new stack and returns it.
func (r *Repository) Create(ctx context.Context, q db.DBTX, in CreateStackInput) (*Stack, error) {
	const sql = `INSERT INTO stacks
		(org_id, space_id, name, status, vcs_repo, vcs_branch, working_dir,
		 iac_tool, iac_version, worker_pool_id, auto_apply, reconcile_interval, drift_mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, make_interval(secs => $12), $13)
		RETURNING ` + stackColumns
	row := q.QueryRow(ctx, sql,
		in.OrgID, in.SpaceID, in.Name, StatusActive, in.VCSRepo, in.VCSBranch, in.WorkingDir,
		in.IACTool, in.IACVersion, in.WorkerPoolID, in.AutoApply, float64(in.ReconcileInterval.Seconds()), string(in.DriftMode))
	s, err := scanStack(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateStack
		}
		return nil, err
	}
	return s, nil
}

// GetByID fetches a non-deleted stack scoped to orgID.
func (r *Repository) GetByID(ctx context.Context, q db.DBTX, orgID, id uuid.UUID) (*Stack, error) {
	const sql = `SELECT ` + stackColumns + ` FROM stacks
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`
	s, err := scanStack(q.QueryRow(ctx, sql, id, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStackNotFound
		}
		return nil, err
	}
	return s, nil
}

// ListByOrg returns a page of stacks for an org and the total count.
func (r *Repository) ListByOrg(ctx context.Context, q db.DBTX, orgID uuid.UUID, page Pagination) ([]*Stack, int, error) {
	if page.Size <= 0 {
		page.Size = 50
	}
	if page.Page <= 0 {
		page.Page = 1
	}
	offset := (page.Page - 1) * page.Size
	const sql = `SELECT ` + stackColumns + ` FROM stacks
		WHERE org_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	rows, err := q.Query(ctx, sql, orgID, page.Size, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Stack
	for rows.Next() {
		s, err := scanStack(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM stacks WHERE org_id = $1 AND deleted_at IS NULL`, orgID).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Update patches a stack's mutable fields. Nil fields are left unchanged.
func (r *Repository) Update(ctx context.Context, q db.DBTX, orgID, id uuid.UUID, in UpdateStackInput) (*Stack, error) {
	sets := make([]string, 0, 9)
	args := make([]any, 0, 9)
	idx := 1
	add := func(col string, val any) {
		sets = append(sets, col+" = $"+strconv.Itoa(idx))
		args = append(args, val)
		idx++
	}
	if in.VCSRepo != nil {
		add("vcs_repo", *in.VCSRepo)
	}
	if in.VCSBranch != nil {
		add("vcs_branch", *in.VCSBranch)
	}
	if in.WorkingDir != nil {
		add("working_dir", *in.WorkingDir)
	}
	if in.IACTool != nil {
		add("iac_tool", *in.IACTool)
	}
	if in.IACVersion != nil {
		add("iac_version", *in.IACVersion)
	}
	if in.AutoApply != nil {
		add("auto_apply", *in.AutoApply)
	}
	if in.ReconcileInterval != nil {
		add("reconcile_interval", makeInterval(*in.ReconcileInterval))
	}
	if in.DriftMode != nil {
		add("drift_mode", string(*in.DriftMode))
	}
	if in.Status != nil {
		add("status", string(*in.Status))
	}
	if len(sets) == 0 {
		return r.GetByID(ctx, q, orgID, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id, orgID)
	query := `UPDATE stacks SET ` + strings.Join(sets, ", ") +
		` WHERE id = $` + strconv.Itoa(idx) + ` AND org_id = $` + strconv.Itoa(idx+1) +
		` AND deleted_at IS NULL RETURNING ` + stackColumns
	s, err := scanStack(q.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStackNotFound
		}
		return nil, err
	}
	return s, nil
}

// makeInterval renders a duration as a PostgreSQL interval literal.
func makeInterval(d time.Duration) string {
	return "make_interval(secs => " + strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + ")"
}

// SoftDelete marks a stack deleted.
func (r *Repository) SoftDelete(ctx context.Context, q db.DBTX, orgID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `UPDATE stacks SET deleted_at = now(), status = $3 WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		id, orgID, string(StatusDestroyed))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStackNotFound
	}
	return nil
}

// SetStatus updates a stack's status.
func (r *Repository) SetStatus(ctx context.Context, q db.DBTX, orgID, id uuid.UUID, status StackStatus) error {
	tag, err := q.Exec(ctx, `UPDATE stacks SET status = $3, updated_at = now() WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		id, orgID, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStackNotFound
	}
	return nil
}

// ListByVCS returns non-deleted stacks matching a repo URL and branch.
func (r *Repository) ListByVCS(ctx context.Context, q db.DBTX, repo, branch string) ([]*Stack, error) {
	const sql = `SELECT ` + stackColumns + ` FROM stacks
		WHERE vcs_repo = $1 AND vcs_branch = $2 AND deleted_at IS NULL`
	rows, err := q.Query(ctx, sql, repo, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Stack
	for rows.Next() {
		s, err := scanStack(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── Variables ───────────────────────────────────────────────────────────────

// SetVariable upserts a stack variable.
func (r *Repository) SetVariable(ctx context.Context, q db.DBTX, stackID uuid.UUID, in VariableInput) error {
	if in.Category == "" {
		in.Category = "terraform"
	}
	const sql = `INSERT INTO stack_variables (stack_id, key, value, sensitive, category)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (stack_id, key) DO UPDATE
		SET value = EXCLUDED.value, sensitive = EXCLUDED.sensitive, category = EXCLUDED.category, updated_at = now()`
	_, err := q.Exec(ctx, sql, stackID, in.Key, in.Value, in.Sensitive, in.Category)
	return err
}

// DeleteVariable removes a variable by key.
func (r *Repository) DeleteVariable(ctx context.Context, q db.DBTX, stackID uuid.UUID, key string) error {
	tag, err := q.Exec(ctx, `DELETE FROM stack_variables WHERE stack_id = $1 AND key = $2`, stackID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVariableNotFound
	}
	return nil
}

// ListVariables returns all variables for a stack.
func (r *Repository) ListVariables(ctx context.Context, q db.DBTX, stackID uuid.UUID) ([]*Variable, error) {
	const sql = `SELECT id, stack_id, key, value, sensitive, category, created_at, updated_at
		FROM stack_variables WHERE stack_id = $1 ORDER BY key`
	rows, err := q.Query(ctx, sql, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Variable
	for rows.Next() {
		v := &Variable{}
		if err := rows.Scan(&v.ID, &v.StackID, &v.Key, &v.Value, &v.Sensitive, &v.Category, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── Dependencies ────────────────────────────────────────────────────────────

// AddDependency inserts an edge. Returns ErrDependencyExists if already present.
func (r *Repository) AddDependency(ctx context.Context, q db.DBTX, stackID, dependsOnID uuid.UUID) error {
	tag, err := q.Exec(ctx, `INSERT INTO stack_dependencies (stack_id, depends_on_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		stackID, dependsOnID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDependencyExists
	}
	return nil
}

// RemoveDependency deletes an edge. Returns ErrDependencyNotFound if absent.
func (r *Repository) RemoveDependency(ctx context.Context, q db.DBTX, stackID, dependsOnID uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM stack_dependencies WHERE stack_id = $1 AND depends_on_id = $2`, stackID, dependsOnID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDependencyNotFound
	}
	return nil
}

// ListDependencies returns outgoing edges (what stackID depends on).
func (r *Repository) ListDependencies(ctx context.Context, q db.DBTX, stackID uuid.UUID) ([]*Dependency, error) {
	rows, err := q.Query(ctx, `SELECT stack_id, depends_on_id FROM stack_dependencies WHERE stack_id = $1`, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeps(rows)
}

// ListDependents returns incoming edges (what depends on stackID).
func (r *Repository) ListDependents(ctx context.Context, q db.DBTX, stackID uuid.UUID) ([]*Dependency, error) {
	rows, err := q.Query(ctx, `SELECT stack_id, depends_on_id FROM stack_dependencies WHERE depends_on_id = $1`, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeps(rows)
}

// ListAllDependenciesByOrg returns every edge among an org's non-deleted stacks.
func (r *Repository) ListAllDependenciesByOrg(ctx context.Context, q db.DBTX, orgID uuid.UUID) ([]*Dependency, error) {
	const sql = `SELECT sd.stack_id, sd.depends_on_id
		FROM stack_dependencies sd
		JOIN stacks s ON s.id = sd.stack_id
		WHERE s.org_id = $1 AND s.deleted_at IS NULL`
	rows, err := q.Query(ctx, sql, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeps(rows)
}

func scanDeps(rows pgx.Rows) ([]*Dependency, error) {
	var out []*Dependency
	for rows.Next() {
		d := &Dependency{}
		if err := rows.Scan(&d.StackID, &d.DependsOnID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
