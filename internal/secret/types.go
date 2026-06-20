// Package secret implements the Secret bounded context. It owns its own
// encryption (AES-256-GCM with per-org HKDF-derived keys) and never leaks
// plaintext values into logs, errors, or API responses. It does not import the
// iam context; the caller's orgID is passed in for tenancy scoping.
package secret

import (
	"context"
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// SecretScope bounds where a secret applies.
type SecretScope string

const (
	ScopeOrg   SecretScope = "ORG"
	ScopeSpace SecretScope = "SPACE"
	ScopeStack SecretScope = "STACK"
)

// Secret is the persisted record. Ciphertext holds nonce||sealed bytes; the
// plaintext is never stored.
type Secret struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ScopeType  SecretScope
	ScopeID    uuid.UUID
	Key        string
	Ciphertext []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// SecretMeta is the safe-to-return view of a secret: name and metadata only.
type SecretMeta struct {
	Name        string      `json:"name"`
	Scope       SecretScope `json:"scope"`
	ScopeID     uuid.UUID   `json:"scope_id"`
	Sensitive   bool        `json:"sensitive"`
	UpdatedAt   time.Time   `json:"updated_at"`
	MaskedValue string      `json:"masked_value"`
}

// SecretValue carries a decrypted plaintext value, used only at dispatch time.
type SecretValue struct {
	Name  string
	Value string
}

// SetSecretInput creates or updates a secret. Value is plaintext and is
// encrypted before persistence.
type SetSecretInput struct {
	OrgID   uuid.UUID
	Scope   SecretScope
	ScopeID uuid.UUID
	Name    string
	Value   string
}

// SecretService is the boundary contract for the secret context.
type SecretService interface {
	Set(ctx context.Context, input SetSecretInput) error
	Delete(ctx context.Context, orgID, stackID uuid.UUID, name string) error
	List(ctx context.Context, orgID, stackID uuid.UUID) ([]*SecretMeta, error)

	// One-time value claim — called by worker at dispatch time (Phase 2+).
	ClaimValues(ctx context.Context, runID, workerID uuid.UUID) ([]*SecretValue, error)
	GetEffectiveSecrets(ctx context.Context, orgID, stackID uuid.UUID) ([]*SecretMeta, error)
}

// Sentinel domain errors. None include plaintext values.
var (
	ErrSecretNotFound = domainerr.New("SECRET_NOT_FOUND", 404, "secret not found")
	ErrInvalidKey     = domainerr.New("SECRET_KEY_INVALID", 500, "encryption key misconfigured")
)
