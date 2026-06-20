// State repository: persistence for state version metadata. State blobs are
// stored in object storage in Phase 3; this repository tracks only the
// metadata rows.
package state

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yourorg/stratum/internal/platform/db"
)

// Repository provides state version persistence. It is stateless.
type Repository struct{}

// NewRepository returns a ready state repository.
func NewRepository() *Repository { return &Repository{} }

func scanVersion(row scanner) (*StateVersion, error) {
	v := &StateVersion{}
	err := row.Scan(&v.ID, &v.StackID, &v.Serial, &v.SHA256, &v.SizeBytes, &v.StorageURI, &v.CreatedAt)
	if err != nil {
		return nil, err
	}
	return v, nil
}

type scanner interface {
	Scan(dest ...any) error
}

const versionColumns = `id, stack_id, serial, sha256, size_bytes, storage_uri, created_at`

// GetLatest returns the highest-serial state version for a stack.
func (r *Repository) GetLatest(ctx context.Context, q db.DBTX, orgID, stackID uuid.UUID) (*StateVersion, error) {
	const sql = `SELECT ` + versionColumns + ` FROM state_versions
		WHERE org_id = $1 AND stack_id = $2 ORDER BY serial DESC LIMIT 1`
	v, err := scanVersion(q.QueryRow(ctx, sql, orgID, stackID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrStateNotFound
		}
		return nil, err
	}
	return v, nil
}

// ListVersions returns all state versions for a stack, newest first.
func (r *Repository) ListVersions(ctx context.Context, q db.DBTX, orgID, stackID uuid.UUID) ([]*StateVersion, error) {
	const sql = `SELECT ` + versionColumns + ` FROM state_versions
		WHERE org_id = $1 AND stack_id = $2 ORDER BY serial DESC`
	rows, err := q.Query(ctx, sql, orgID, stackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StateVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// InsertVersion records a new state version metadata row.
func (r *Repository) InsertVersion(
	ctx context.Context, q db.DBTX,
	orgID, stackID uuid.UUID, serial int, sha256 string, size int64, storageURI string,
) (*StateVersion, error) {
	const sql = `INSERT INTO state_versions (org_id, stack_id, serial, sha256, size_bytes, storage_uri)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING ` + versionColumns
	return scanVersion(q.QueryRow(ctx, sql, orgID, stackID, serial, sha256, size, storageURI))
}
