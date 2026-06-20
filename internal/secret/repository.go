// Secret repository: persistence for encrypted secrets. The List query selects
// metadata columns only; ciphertext is never loaded for listing.
package secret

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yourorg/stratum/internal/platform/db"
)

// Repository provides secret persistence. It is stateless.
type Repository struct{}

// NewRepository returns a ready secret repository.
func NewRepository() *Repository { return &Repository{} }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// Upsert inserts a secret or updates its ciphertext, clearing any prior soft
// delete. The UNIQUE (scope_type, scope_id, key) constraint drives the upsert.
func (r *Repository) Upsert(
	ctx context.Context, q db.DBTX,
	orgID uuid.UUID, scopeType SecretScope, scopeID uuid.UUID, key string, ciphertext []byte,
) error {
	const sql = `INSERT INTO secrets (org_id, scope_type, scope_id, key, ciphertext)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (scope_type, scope_id, key) DO UPDATE
		SET ciphertext = EXCLUDED.ciphertext, updated_at = now(), deleted_at = NULL`
	_, err := q.Exec(ctx, sql, orgID, string(scopeType), scopeID, key, ciphertext)
	return err
}

// Delete soft-deletes a stack-scoped secret. Returns ErrSecretNotFound if absent.
func (r *Repository) Delete(ctx context.Context, q db.DBTX, orgID, stackID uuid.UUID, key string) error {
	const sql = `UPDATE secrets SET deleted_at = now()
		WHERE org_id = $1 AND scope_type = 'STACK' AND scope_id = $2 AND key = $3 AND deleted_at IS NULL`
	tag, err := q.Exec(ctx, sql, orgID, stackID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSecretNotFound
	}
	return nil
}

// ListByStack returns metadata for all non-deleted stack-scoped secrets.
// Ciphertext is intentionally not selected.
func (r *Repository) ListByStack(ctx context.Context, q db.DBTX, orgID, stackID uuid.UUID) ([]*Secret, error) {
	const sql = `SELECT id, org_id, scope_type, scope_id, key, updated_at
		FROM secrets
		WHERE org_id = $1 AND scope_type = 'STACK' AND scope_id = $2 AND deleted_at IS NULL
		ORDER BY key`
	rows, err := q.Query(ctx, sql, orgID, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Secret
	for rows.Next() {
		s := &Secret{}
		if err := rows.Scan(&s.ID, &s.OrgID, &s.ScopeType, &s.ScopeID, &s.Key, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get loads a single secret including its ciphertext, for internal decryption.
func (r *Repository) Get(ctx context.Context, q db.DBTX, orgID, scopeID uuid.UUID, key string) (*Secret, error) {
	const sql = `SELECT id, org_id, scope_type, scope_id, key, ciphertext, updated_at
		FROM secrets
		WHERE org_id = $1 AND scope_type = 'STACK' AND scope_id = $2 AND key = $3 AND deleted_at IS NULL`
	s := &Secret{}
	err := q.QueryRow(ctx, sql, orgID, scopeID, key).Scan(
		&s.ID, &s.OrgID, &s.ScopeType, &s.ScopeID, &s.Key, &s.Ciphertext, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSecretNotFound
		}
		return nil, err
	}
	return s, nil
}
