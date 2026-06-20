// Package iam implements the Identity and Access Management bounded context.
// It owns organizations, users, API keys, JWT sessions, and role-based access
// control. The Identity type it defines is the canonical authenticated
// principal carried through the request context by the auth middleware.
package iam

import (
	"context"
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// IdentityType classifies the kind of principal behind a request.
type IdentityType string

const (
	IdentityUser   IdentityType = "USER"
	IdentityAPIKey IdentityType = "API_KEY"
	IdentityWorker IdentityType = "WORKER"
	IdentitySystem IdentityType = "SYSTEM"
)

// SubjectType values stored on role_bindings.
const (
	SubjectUser   = "USER"
	SubjectAPIKey = "API_KEY"
)

// ResourceType values for role binding scopes.
const (
	ResourceOrg   = "ORG"
	ResourceSpace = "SPACE"
	ResourceStack = "STACK"
)

// Role constants used by RBAC middleware and GrantRole calls.
const (
	RoleAdmin       = "admin"
	RoleStackReader = "stack:reader"
	RoleStackWriter = "stack:writer"
)

// Organization is a tenant in the control plane.
type Organization struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Slug      string     `json:"slug"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// User is a human principal belonging to an organization. PasswordHash is never
// serialized to clients.
type User struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        uuid.UUID  `json:"org_id"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// APIKey is a machine credential. KeyHash holds the HMAC of the raw key; the
// raw key is returned only once at creation. KeyHash is never serialized.
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	UserID     *uuid.UUID `json:"user_id,omitempty"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// RoleBinding grants a role to a subject, optionally scoped to a resource.
type RoleBinding struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        uuid.UUID  `json:"org_id"`
	SubjectType  string     `json:"subject_type"`
	SubjectID    uuid.UUID  `json:"subject_id"`
	Role         string     `json:"role"`
	ResourceType *string    `json:"resource_type,omitempty"`
	ResourceID   *uuid.UUID `json:"resource_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Identity is the authenticated principal placed on the request context by the
// auth middleware. Raw holds the original credential for audit logging.
type Identity struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Type  IdentityType
	Roles []string
	Raw   string
}

// HasRole reports whether the identity holds the given role.
func (i Identity) HasRole(role string) bool {
	for _, r := range i.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Resource is the target of a permission check.
type Resource struct {
	Type string
	ID   uuid.UUID
}

// Session is the result of a successful login or token refresh.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	User         *User  `json:"user"`
}

// CreateOrgInput creates an organization and bootstraps its first admin user.
type CreateOrgInput struct {
	Name          string
	Slug          string
	AdminEmail    string
	AdminPassword string
}

// CreateUserInput registers a new user within an organization.
type CreateUserInput struct {
	OrgID    uuid.UUID
	Email    string
	Password string
}

// CreateAPIKeyInput mints a new API key. Roles become role bindings scoped to
// the organization.
type CreateAPIKeyInput struct {
	OrgID     uuid.UUID
	UserID    *uuid.UUID
	Name      string
	Roles     []string
	ExpiresAt *time.Time
}

// GrantRoleInput binds a role to a subject.
type GrantRoleInput struct {
	OrgID        uuid.UUID
	SubjectType  string
	SubjectID    uuid.UUID
	Role         string
	ResourceType string
	ResourceID   *uuid.UUID
}

// IAMService is the boundary contract for the IAM context.
type IAMService interface {
	// Auth
	Login(ctx context.Context, email, password string) (*Session, error)
	RefreshSession(ctx context.Context, refreshToken string) (*Session, error)
	Logout(ctx context.Context, refreshToken string) error
	ValidateAPIKey(ctx context.Context, rawKey string) (*Identity, error)
	ValidateJWT(ctx context.Context, token string) (*Identity, error)

	// Org + User management
	CreateOrg(ctx context.Context, input CreateOrgInput) (*Organization, error)
	GetOrg(ctx context.Context, id uuid.UUID) (*Organization, error)
	CreateUser(ctx context.Context, input CreateUserInput) (*User, error)
	GetUser(ctx context.Context, id uuid.UUID) (*User, error)

	// API Keys
	CreateAPIKey(ctx context.Context, input CreateAPIKeyInput) (*APIKey, string, error)
	RevokeAPIKey(ctx context.Context, id uuid.UUID) error

	// RBAC
	GrantRole(ctx context.Context, input GrantRoleInput) error
	RevokeRole(ctx context.Context, bindingID uuid.UUID) error
	CheckPermission(ctx context.Context, subject Identity, action string, resource Resource) (bool, error)
	GetRoleBindings(ctx context.Context, subjectID uuid.UUID) ([]*RoleBinding, error)
}

// Sentinel domain errors. These carry HTTP statuses so the API layer can map
// them without type-switching.
var (
	ErrOrgNotFound        = domainerr.New("ORG_NOT_FOUND", 404, "organization not found")
	ErrUserNotFound       = domainerr.New("USER_NOT_FOUND", 404, "user not found")
	ErrInvalidCredentials = domainerr.New("INVALID_CREDENTIALS", 401, "invalid email or password")
	ErrAPIKeyNotFound     = domainerr.New("API_KEY_NOT_FOUND", 404, "api key not found")
	ErrTokenRevoked       = domainerr.New("TOKEN_REVOKED", 401, "token has been revoked")
	ErrInvalidToken       = domainerr.New("INVALID_TOKEN", 401, "invalid or expired token")
	ErrEmailExists        = domainerr.New("EMAIL_EXISTS", 409, "email already registered in this org")
	ErrSlugExists         = domainerr.New("SLUG_EXISTS", 409, "org slug already taken")
)

type identityKey struct{}

// WithIdentity returns a copy of ctx that carries the given identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the identity stored in ctx, if any.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
