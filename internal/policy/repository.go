package policy

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
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Repository provides policy persistence. It is stateless.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

// ─── Column constants ───────────────────────────────────────────────────────

const policyColumns = `id, org_id, name, description, rego_text,
	enabled, created_at, updated_at, deleted_at`

// policies with enforcement (added by 015 migration — COALESCE handles missing)
const policyColumnsWithEnforcement = `id, org_id, name, description, rego_text,
	enabled, COALESCE(enforcement, 'SOFT_WARN'), created_at, updated_at, deleted_at`

const policySetColumns = `id, org_id, name, created_at`
const policySetBindingColumns = `id, policy_set_id, resource_type, resource_id, created_at`

// ─── Scans ──────────────────────────────────────────────────────────────────

func scanPolicy(row pgx.Row) (*Policy, error) {
	p := &Policy{}
	var desc, deletedAt *string
	var enforcement string
	err := row.Scan(
		&p.ID, &p.OrgID, &p.Name, &desc, &p.RegoSource,
		&p.Enabled, &enforcement,
		&p.CreatedAt, &p.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	if desc != nil {
		p.Description = desc
	}
	if deletedAt != nil {
		t, _ := time.Parse(time.RFC3339Nano, *deletedAt)
		p.DeletedAt = &t
	}
	p.Enforcement = EnforcementLevel(enforcement)
	// Map DB column name rego_text → Go field RegoSource
	return p, nil
}

func scanPolicies(rows pgx.Rows) ([]*Policy, error) {
	var out []*Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPolicySet(row pgx.Row) (*PolicySet, error) {
	ps := &PolicySet{}
	err := row.Scan(&ps.ID, &ps.OrgID, &ps.Name, &ps.CreatedAt)
	return ps, err
}

func scanPolicySets(rows pgx.Rows) ([]*PolicySet, error) {
	var out []*PolicySet
	for rows.Next() {
		ps := &PolicySet{}
		if err := rows.Scan(&ps.ID, &ps.OrgID, &ps.Name, &ps.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}

func scanPolicySetBinding(row pgx.Row) (*PolicySetBinding, error) {
	b := &PolicySetBinding{}
	err := row.Scan(&b.ID, &b.PolicySetID, &b.ResourceType, &b.ResourceID, &b.CreatedAt)
	return b, err
}

func scanPolicySetBindings(rows pgx.Rows) ([]*PolicySetBinding, error) {
	var out []*PolicySetBinding
	for rows.Next() {
		b := &PolicySetBinding{}
		if err := rows.Scan(&b.ID, &b.PolicySetID, &b.ResourceType, &b.ResourceID, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ─── Policies ───────────────────────────────────────────────────────────────

func (r *Repository) Create(ctx context.Context, q db.DBTX, p *Policy) error {
	const sql = `INSERT INTO policies (id, org_id, name, description, rego_text, enabled, enforcement, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + policyColumnsWithEnforcement
	row := q.QueryRow(ctx, sql,
		p.ID, p.OrgID, p.Name, p.Description, p.RegoSource,
		p.Enabled, string(p.Enforcement),
		p.CreatedAt, p.UpdatedAt)
	created, err := scanPolicy(row)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPolicyNameConflict
		}
		return err
	}
	*p = *created
	return nil
}

func (r *Repository) GetByID(ctx context.Context, q db.DBTX, id uuid.UUID) (*Policy, error) {
	const sql = `SELECT ` + policyColumnsWithEnforcement + ` FROM policies WHERE id = $1 AND deleted_at IS NULL`
	p, err := scanPolicy(q.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPolicyNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *Repository) ListByOrg(ctx context.Context, q db.DBTX, orgID uuid.UUID) ([]*Policy, error) {
	const sql = `SELECT ` + policyColumnsWithEnforcement + ` FROM policies
		WHERE org_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC`
	rows, err := q.Query(ctx, sql, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func (r *Repository) Update(ctx context.Context, q db.DBTX, id uuid.UUID, input UpdatePolicyInput) (*Policy, error) {
	// Build dynamic update — only set fields that are non-nil.
	sets := []string{}
	args := []any{}
	idx := 1

	if input.Name != nil {
		sets = append(sets, "name = $"+strconv.Itoa(idx))
		args = append(args, *input.Name)
		idx++
	}
	if input.Description != nil {
		sets = append(sets, "description = $"+strconv.Itoa(idx))
		args = append(args, *input.Description)
		idx++
	}
	if input.Enabled != nil {
		sets = append(sets, "enabled = $"+strconv.Itoa(idx))
		args = append(args, *input.Enabled)
		idx++
	}
	if input.Enforcement != nil {
		sets = append(sets, "enforcement = $"+strconv.Itoa(idx))
		args = append(args, string(*input.Enforcement))
		idx++
	}
	if len(sets) == 0 {
		// No-op: just fetch and return
		return r.GetByID(ctx, q, id)
	}

	sets = append(sets, "updated_at = now()")
	query := `UPDATE policies SET ` + strings.Join(sets, ", ") +
		` WHERE id = $` + strconv.Itoa(idx) + ` AND deleted_at IS NULL
		RETURNING ` + policyColumnsWithEnforcement
	args = append(args, id)

	row := q.QueryRow(ctx, query, args...)
	p, err := scanPolicy(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPolicyNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *Repository) UpdateSource(ctx context.Context, q db.DBTX, id uuid.UUID, source string) error {
	const sql = `UPDATE policies SET rego_text = $2, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`
	tag, err := q.Exec(ctx, sql, id, source)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPolicyNotFound
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	const sql = `UPDATE policies SET deleted_at = now(), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`
	tag, err := q.Exec(ctx, sql, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPolicyNotFound
	}
	return nil
}

// ─── Policy sets ────────────────────────────────────────────────────────────

func (r *Repository) CreatePolicySet(ctx context.Context, q db.DBTX, ps *PolicySet) error {
	const sql = `INSERT INTO policy_sets (id, org_id, name, created_at)
		VALUES ($1, $2, $3, $4) RETURNING ` + policySetColumns
	row := q.QueryRow(ctx, sql, ps.ID, ps.OrgID, ps.Name, ps.CreatedAt)
	created, err := scanPolicySet(row)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPolicySetNameConflict
		}
		return err
	}
	*ps = *created
	return nil
}

func (r *Repository) AddToSet(ctx context.Context, q db.DBTX, setID, policyID uuid.UUID) error {
	const sql = `INSERT INTO policy_set_members (policy_set_id, policy_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`
	_, err := q.Exec(ctx, sql, setID, policyID)
	return err
}

func (r *Repository) RemoveFromSet(ctx context.Context, q db.DBTX, setID, policyID uuid.UUID) error {
	const sql = `DELETE FROM policy_set_members WHERE policy_set_id = $1 AND policy_id = $2`
	tag, err := q.Exec(ctx, sql, setID, policyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPolicySetMemberNotFound
	}
	return nil
}

func (r *Repository) ListPolicySetMembers(ctx context.Context, q db.DBTX, setID uuid.UUID) ([]*PolicySetMember, error) {
	const sql = `SELECT policy_set_id, policy_id FROM policy_set_members WHERE policy_set_id = $1`
	rows, err := q.Query(ctx, sql, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PolicySetMember
	for rows.Next() {
		m := &PolicySetMember{}
		if err := rows.Scan(&m.PolicySetID, &m.PolicyID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ─── Policy set bindings ────────────────────────────────────────────────────

func (r *Repository) BindSet(ctx context.Context, q db.DBTX, setID uuid.UUID, resourceType string, resourceID uuid.UUID) (*PolicySetBinding, error) {
	const sql = `INSERT INTO policy_set_bindings (id, policy_set_id, resource_type, resource_id, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING ` + policySetBindingColumns
	b := &PolicySetBinding{ID: uuid.New(), CreatedAt: time.Now()}
	row := q.QueryRow(ctx, sql, b.ID, setID, resourceType, resourceID, b.CreatedAt)
	created, err := scanPolicySetBinding(row)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *Repository) UnbindSet(ctx context.Context, q db.DBTX, bindingID uuid.UUID) error {
	const sql = `DELETE FROM policy_set_bindings WHERE id = $1`
	tag, err := q.Exec(ctx, sql, bindingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBindingNotFound
	}
	return nil
}

// GetBindingsForStack returns all policy set bindings that apply to a given
// stack: org-level (org_id directly on bindings), space-level, and stack-level.
func (r *Repository) GetBindingsForStack(ctx context.Context, q db.DBTX, orgID uuid.UUID, spaceID *uuid.UUID, stackID uuid.UUID) ([]*PolicySetBinding, error) {
	// ORG-level: bindings where resource_type = 'ORG' AND resource_id = orgID
	// SPACE-level: bindings where resource_type = 'SPACE' AND resource_id = spaceID
	// STACK-level: bindings where resource_type = 'STACK' AND resource_id = stackID
	args := []any{"ORG", orgID, "STACK", stackID}
	query := `SELECT ` + policySetBindingColumns + ` FROM policy_set_bindings
		WHERE (resource_type = $1 AND resource_id = $2)
		   OR (resource_type = $3 AND resource_id = $4)`
	idx := 5
	if spaceID != nil {
		query += ` OR (resource_type = $` + strconv.Itoa(idx) + ` AND resource_id = $` + strconv.Itoa(idx+1) + `)`
		args = append(args, "SPACE", *spaceID)
	}
	query += ` ORDER BY resource_type`
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicySetBindings(rows)
}

// GetPoliciesInSet returns all policies that are members of a given set,
// including enforcement-level data.
func (r *Repository) GetPoliciesInSet(ctx context.Context, q db.DBTX, setID uuid.UUID) ([]*Policy, error) {
	const sql = `SELECT p.` + policyColumnsWithEnforcement + `
		FROM policies p
		JOIN policy_set_members m ON m.policy_id = p.id
		WHERE m.policy_set_id = $1 AND p.deleted_at IS NULL`
	rows, err := q.Query(ctx, sql, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

// ─── Sentinel errors ────────────────────────────────────────────────────────

var (
	ErrPolicyNotFound         = domainerr.New("POLICY_NOT_FOUND", 404, "policy not found")
	ErrPolicyNameConflict     = domainerr.New("POLICY_NAME_CONFLICT", 409, "policy name already exists in this org")
	ErrPolicySetNotFound      = domainerr.New("POLICY_SET_NOT_FOUND", 404, "policy set not found")
	ErrPolicySetNameConflict  = domainerr.New("POLICY_SET_NAME_CONFLICT", 409, "policy set name already exists in this org")
	ErrPolicySetMemberNotFound = domainerr.New("POLICY_SET_MEMBER_NOT_FOUND", 404, "policy set member not found")
	ErrBindingNotFound        = domainerr.New("BINDING_NOT_FOUND", 404, "policy set binding not found")
)

// ─── Helpers ────────────────────────────────────────────────────────────────
// (strconv.Itoa and strings.Join used in dynamic queries)
