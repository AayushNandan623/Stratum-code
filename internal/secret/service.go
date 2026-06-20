// Secret service: encrypts plaintext on write and exposes metadata-only views
// on read. Plaintext values never appear in logs, errors, or responses.
package secret

import (
	"context"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

type service struct {
	repo   *Repository
	crypto *Crypto
	db     *db.DB
}

// NewService constructs a SecretService backed by database and crypto.
func NewService(database *db.DB, c *Crypto) SecretService {
	return &service{repo: NewRepository(), crypto: c, db: database}
}

var _ SecretService = (*service)(nil)

// Set encrypts the value and upserts the secret.
func (s *service) Set(ctx context.Context, in SetSecretInput) error {
	if in.OrgID == uuid.Nil || in.ScopeID == uuid.Nil || in.Name == "" {
		return domainerr.ErrValidation
	}
	if in.Scope == "" {
		in.Scope = ScopeStack
	}
	ciphertext, err := s.crypto.Encrypt(in.OrgID, in.Name, []byte(in.Value))
	if err != nil {
		return err
	}
	return s.repo.Upsert(ctx, s.db.Pool, in.OrgID, in.Scope, in.ScopeID, in.Name, ciphertext)
}

// Delete removes a stack-scoped secret.
func (s *service) Delete(ctx context.Context, orgID, stackID uuid.UUID, name string) error {
	if name == "" {
		return domainerr.ErrValidation
	}
	return s.repo.Delete(ctx, s.db.Pool, orgID, stackID, name)
}

// List returns metadata for all stack-scoped secrets. Values are masked.
func (s *service) List(ctx context.Context, orgID, stackID uuid.UUID) ([]*SecretMeta, error) {
	secrets, err := s.repo.ListByStack(ctx, s.db.Pool, orgID, stackID)
	if err != nil {
		return nil, err
	}
	out := make([]*SecretMeta, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, &SecretMeta{
			Name:        sec.Key,
			Scope:       sec.ScopeType,
			ScopeID:     sec.ScopeID,
			Sensitive:   true,
			UpdatedAt:   sec.UpdatedAt,
			MaskedValue: "***",
		})
	}
	return out, nil
}

// GetEffectiveSecrets returns the secrets effective for a stack. For Phase 1
// this is the stack-scoped set; org/space inheritance arrives later.
func (s *service) GetEffectiveSecrets(ctx context.Context, orgID, stackID uuid.UUID) ([]*SecretMeta, error) {
	return s.List(ctx, orgID, stackID)
}

// ClaimValues is a stub until the run/worker contexts land in Phase 2.
func (s *service) ClaimValues(_ context.Context, _, _ uuid.UUID) ([]*SecretValue, error) {
	return nil, nil
}
