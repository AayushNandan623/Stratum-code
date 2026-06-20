// IAM repository: persistence for organizations, users, API keys, and role
// bindings. Every method accepts a db.DBTX so the same code path runs against
// either the connection pool or an in-flight transaction.
package iam

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// Repository provides IAM persistence. It is stateless; the db.DBTX argument
// selects the connection or transaction to use.
type Repository struct{}

// NewRepository returns a ready IAM repository.
func NewRepository() *Repository { return &Repository{} }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ── Organizations ───────────────────────────────────────────────────────────

// CreateOrg inserts a new organization and returns it.
func (r *Repository) CreateOrg(ctx context.Context, q db.DBTX, name, slug string) (*Organization, error) {
	const sql = `INSERT INTO organizations (name, slug) VALUES ($1, $2)
		RETURNING id, name, slug, created_at, updated_at, deleted_at`
	o := &Organization{}
	err := q.QueryRow(ctx, sql, name, slug).Scan(
		&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugExists
		}
		return nil, err
	}
	return o, nil
}

// GetOrg fetches a non-deleted organization by ID.
func (r *Repository) GetOrg(ctx context.Context, q db.DBTX, id uuid.UUID) (*Organization, error) {
	const sql = `SELECT id, name, slug, created_at, updated_at, deleted_at
		FROM organizations WHERE id = $1 AND deleted_at IS NULL`
	o := &Organization{}
	err := q.QueryRow(ctx, sql, id).Scan(
		&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOrgNotFound
		}
		return nil, err
	}
	return o, nil
}

// ── Users ───────────────────────────────────────────────────────────────────

// CreateUser inserts a user with a pre-hashed password.
func (r *Repository) CreateUser(ctx context.Context, q db.DBTX, orgID uuid.UUID, email, passwordHash string) (*User, error) {
	const sql = `INSERT INTO users (org_id, email, password_hash) VALUES ($1, $2, $3)
		RETURNING id, org_id, email, password_hash, created_at, updated_at, deleted_at`
	u := &User{}
	err := q.QueryRow(ctx, sql, orgID, email, passwordHash).Scan(
		&u.ID, &u.OrgID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailExists
		}
		return nil, err
	}
	return u, nil
}

// GetUserByEmail fetches a non-deleted user by email within an org.
func (r *Repository) GetUserByEmail(ctx context.Context, q db.DBTX, orgID uuid.UUID, email string) (*User, error) {
	const sql = `SELECT id, org_id, email, password_hash, created_at, updated_at, deleted_at
		FROM users WHERE org_id = $1 AND email = $2 AND deleted_at IS NULL`
	u := &User{}
	err := q.QueryRow(ctx, sql, orgID, email).Scan(
		&u.ID, &u.OrgID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// GetUserByEmailGlobal fetches a non-deleted user by email across all orgs.
// Login identifies users by email alone, so the first match is returned.
func (r *Repository) GetUserByEmailGlobal(ctx context.Context, q db.DBTX, email string) (*User, error) {
	const sql = `SELECT id, org_id, email, password_hash, created_at, updated_at, deleted_at
		FROM users WHERE email = $1 AND deleted_at IS NULL LIMIT 1`
	u := &User{}
	err := q.QueryRow(ctx, sql, email).Scan(
		&u.ID, &u.OrgID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// GetUserByID fetches a non-deleted user by ID.
func (r *Repository) GetUserByID(ctx context.Context, q db.DBTX, id uuid.UUID) (*User, error) {
	const sql = `SELECT id, org_id, email, password_hash, created_at, updated_at, deleted_at
		FROM users WHERE id = $1 AND deleted_at IS NULL`
	u := &User{}
	err := q.QueryRow(ctx, sql, id).Scan(
		&u.ID, &u.OrgID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return u, nil
}

// ── API Keys ────────────────────────────────────────────────────────────────

// CreateAPIKey inserts an API key with a pre-computed hash.
func (r *Repository) CreateAPIKey(
	ctx context.Context, q db.DBTX,
	orgID uuid.UUID, userID *uuid.UUID, name, keyHash string,
	scopes []string, expiresAt *time.Time,
) (*APIKey, error) {
	const sql = `INSERT INTO api_keys (org_id, user_id, name, key_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, org_id, user_id, name, key_hash, scopes, expires_at, last_used_at, created_at`
	k := &APIKey{}
	err := q.QueryRow(ctx, sql, orgID, userID, name, keyHash, scopes, expiresAt).Scan(
		&k.ID, &k.OrgID, &k.UserID, &k.Name, &k.KeyHash, &k.Scopes,
		&k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	return k, nil
}

// GetAPIKeyByHash loads a non-expired API key by its hash.
func (r *Repository) GetAPIKeyByHash(ctx context.Context, q db.DBTX, hash string) (*APIKey, error) {
	const sql = `SELECT id, org_id, user_id, name, key_hash, scopes, expires_at, last_used_at, created_at
		FROM api_keys
		WHERE key_hash = $1 AND (expires_at IS NULL OR expires_at > now())`
	k := &APIKey{}
	err := q.QueryRow(ctx, sql, hash).Scan(
		&k.ID, &k.OrgID, &k.UserID, &k.Name, &k.KeyHash, &k.Scopes,
		&k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	return k, nil
}

// DeleteAPIKey removes an API key by ID.
func (r *Repository) DeleteAPIKey(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	const sql = `DELETE FROM api_keys WHERE id = $1`
	tag, err := q.Exec(ctx, sql, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// TouchAPIKeyLastUsed records the current time as last_used_at.
func (r *Repository) TouchAPIKeyLastUsed(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	const sql = `UPDATE api_keys SET last_used_at = now() WHERE id = $1`
	_, err := q.Exec(ctx, sql, id)
	return err
}

// ── Role Bindings ───────────────────────────────────────────────────────────

// CreateRoleBinding inserts a role binding and returns it.
func (r *Repository) CreateRoleBinding(ctx context.Context, q db.DBTX, in GrantRoleInput) (*RoleBinding, error) {
	const sql = `INSERT INTO role_bindings (org_id, subject_type, subject_id, role, resource_type, resource_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, org_id, subject_type, subject_id, role, resource_type, resource_id, created_at`
	rb := &RoleBinding{}
	var rt *string
	if in.ResourceType != "" {
		rt = &in.ResourceType
	}
	err := q.QueryRow(ctx, sql, in.OrgID, in.SubjectType, in.SubjectID, in.Role, rt, in.ResourceID).Scan(
		&rb.ID, &rb.OrgID, &rb.SubjectType, &rb.SubjectID, &rb.Role,
		&rb.ResourceType, &rb.ResourceID, &rb.CreatedAt)
	if err != nil {
		return nil, err
	}
	return rb, nil
}

// ListRoleBindingsBySubject returns all role bindings for a subject.
func (r *Repository) ListRoleBindingsBySubject(ctx context.Context, q db.DBTX, subjectID uuid.UUID) ([]*RoleBinding, error) {
	const sql = `SELECT id, org_id, subject_type, subject_id, role, resource_type, resource_id, created_at
		FROM role_bindings WHERE subject_id = $1`
	rows, err := q.Query(ctx, sql, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RoleBinding
	for rows.Next() {
		rb := &RoleBinding{}
		if err := rows.Scan(
			&rb.ID, &rb.OrgID, &rb.SubjectType, &rb.SubjectID, &rb.Role,
			&rb.ResourceType, &rb.ResourceID, &rb.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rb)
	}
	return out, rows.Err()
}

// DeleteRoleBinding removes a role binding by ID.
func (r *Repository) DeleteRoleBinding(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	const sql = `DELETE FROM role_bindings WHERE id = $1`
	tag, err := q.Exec(ctx, sql, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domainerr.ErrNotFound
	}
	return nil
}
