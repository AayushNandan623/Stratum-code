// VCS service: connection management, webhook signature validation (global
// secret), and push event parsing dispatch. Provider HTTP clients for source
// archives and live PR status updates arrive in a later phase.
package vcs

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

type service struct {
	db            *db.DB
	webhookSecret string
	logger        *slog.Logger
}

// NewService constructs a VCSService backed by database. webhookSecret is the
// global shared secret used to validate inbound webhook signatures.
func NewService(database *db.DB, webhookSecret string, logger *slog.Logger) VCSService {
	return &service{db: database, webhookSecret: webhookSecret, logger: logger}
}

var _ VCSService = (*service)(nil)

const connColumns = `id, org_id, provider, name, base_url, api_token_ref, created_at, updated_at, deleted_at`

func scanConn(row scanner) (*VCSConnection, error) {
	c := &VCSConnection{}
	err := row.Scan(&c.ID, &c.OrgID, &c.Provider, &c.Name, &c.BaseURL, &c.APITokenRef, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// CreateConnection inserts a new VCS connection.
func (s *service) CreateConnection(ctx context.Context, in CreateConnectionInput) (*VCSConnection, error) {
	if in.OrgID == uuid.Nil || in.Name == "" || in.Provider == "" {
		return nil, domainerr.ErrValidation
	}
	const sql = `INSERT INTO vcs_connections (org_id, provider, name, base_url)
		VALUES ($1, $2, $3, $4) RETURNING ` + connColumns
	return scanConn(s.db.Pool.QueryRow(ctx, sql, in.OrgID, string(in.Provider), in.Name, in.BaseURL))
}

// GetConnection fetches a non-deleted connection by ID.
func (s *service) GetConnection(ctx context.Context, id uuid.UUID) (*VCSConnection, error) {
	const sql = `SELECT ` + connColumns + ` FROM vcs_connections WHERE id = $1 AND deleted_at IS NULL`
	c, err := scanConn(s.db.Pool.QueryRow(ctx, sql, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	return c, nil
}

// ListConnections returns all connections for an org.
func (s *service) ListConnections(ctx context.Context, orgID uuid.UUID) ([]*VCSConnection, error) {
	const sql = `SELECT ` + connColumns + ` FROM vcs_connections WHERE org_id = $1 AND deleted_at IS NULL ORDER BY created_at`
	rows, err := s.db.Pool.Query(ctx, sql, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*VCSConnection
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConnection soft-deletes a connection by ID.
func (s *service) DeleteConnection(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `UPDATE vcs_connections SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConnectionNotFound
	}
	return nil
}

// GetSourceArchive is a stub until the provider HTTP client lands in Phase 3.
func (s *service) GetSourceArchive(_ context.Context, _ uuid.UUID, _ string) (io.ReadCloser, error) {
	return nil, errors.New("vcs: source archive not implemented until Phase 3")
}

// UpdatePRStatus posts a commit status. Stub until the provider HTTP client
// lands in a later phase.
func (s *service) UpdatePRStatus(_ context.Context, input PRStatusInput) error {
	return postGitHubCommitStatus(input)
}

// ValidateWebhookSignature verifies the inbound signature against the global
// webhook secret using constant-time HMAC comparison.
func (s *service) ValidateWebhookSignature(_ context.Context, body []byte, signature string) error {
	return ValidateSignature(body, s.webhookSecret, signature)
}

// ParsePushEvent dispatches to the provider-specific parser.
func (s *service) ParsePushEvent(_ context.Context, provider VCSProvider, body []byte) (*PushEvent, error) {
	switch provider {
	case ProviderGitHub:
		return ParseGitHubPush(body)
	default:
		return nil, ErrInvalidPayload
	}
}
