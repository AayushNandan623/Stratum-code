// IAM admin operations: organization and user management, API key minting, and
// role-based access control. These are split from service.go to keep each file
// focused and under the line limit.
package iam

import (
	"context"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// CreateOrg creates an organization and bootstraps its first admin user with an
// admin role binding, all in one transaction so login works immediately.
func (s *service) CreateOrg(ctx context.Context, in CreateOrgInput) (*Organization, error) {
	if in.Name == "" || in.Slug == "" || in.AdminEmail == "" || in.AdminPassword == "" {
		return nil, domainerr.ErrValidation
	}
	var org *Organization
	err := s.db.InTx(ctx, func(q db.DBTX) error {
		o, err := s.repo.CreateOrg(ctx, q, in.Name, in.Slug)
		if err != nil {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(in.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		admin, err := s.repo.CreateUser(ctx, q, o.ID, in.AdminEmail, string(hash))
		if err != nil {
			return err
		}
		orgID := o.ID
		if _, err := s.repo.CreateRoleBinding(ctx, q, GrantRoleInput{
			OrgID:        o.ID,
			SubjectType:  SubjectUser,
			SubjectID:    admin.ID,
			Role:         RoleAdmin,
			ResourceType: ResourceOrg,
			ResourceID:   &orgID,
		}); err != nil {
			return err
		}
		org = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return org, nil
}

// GetOrg returns an organization by ID.
func (s *service) GetOrg(ctx context.Context, id uuid.UUID) (*Organization, error) {
	return s.repo.GetOrg(ctx, s.db.Pool, id)
}

// CreateUser registers a user with a bcrypt-hashed password.
func (s *service) CreateUser(ctx context.Context, in CreateUserInput) (*User, error) {
	if in.OrgID == uuid.Nil || in.Email == "" || in.Password == "" {
		return nil, domainerr.ErrValidation
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return s.repo.CreateUser(ctx, s.db.Pool, in.OrgID, in.Email, string(hash))
}

// GetUser returns a user by ID.
func (s *service) GetUser(ctx context.Context, id uuid.UUID) (*User, error) {
	return s.repo.GetUserByID(ctx, s.db.Pool, id)
}

// CreateAPIKey mints a new API key, stores its hash, grants the requested roles
// as org-scoped bindings, and returns the plaintext key exactly once.
func (s *service) CreateAPIKey(ctx context.Context, in CreateAPIKeyInput) (*APIKey, string, error) {
	if in.OrgID == uuid.Nil || in.Name == "" {
		return nil, "", domainerr.ErrValidation
	}
	secret, err := generateKeySecret()
	if err != nil {
		return nil, "", err
	}
	raw := apiKeyPrefix + secret
	hash := hashAPIKey(raw, s.jwtSecret)
	scopes := in.Roles
	if scopes == nil {
		scopes = []string{}
	}
	var key *APIKey
	err = s.db.InTx(ctx, func(q db.DBTX) error {
		k, err := s.repo.CreateAPIKey(ctx, q, in.OrgID, in.UserID, in.Name, hash, scopes, in.ExpiresAt)
		if err != nil {
			return err
		}
		orgID := in.OrgID
		for _, role := range in.Roles {
			if _, err := s.repo.CreateRoleBinding(ctx, q, GrantRoleInput{
				OrgID:        in.OrgID,
				SubjectType:  SubjectAPIKey,
				SubjectID:    k.ID,
				Role:         role,
				ResourceType: ResourceOrg,
				ResourceID:   &orgID,
			}); err != nil {
				return err
			}
		}
		key = k
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return key, raw, nil
}

// RevokeAPIKey deletes an API key by ID.
func (s *service) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteAPIKey(ctx, s.db.Pool, id)
}

// GrantRole binds a role to a subject.
func (s *service) GrantRole(ctx context.Context, in GrantRoleInput) error {
	_, err := s.repo.CreateRoleBinding(ctx, s.db.Pool, in)
	return err
}

// RevokeRole removes a role binding by ID.
func (s *service) RevokeRole(ctx context.Context, bindingID uuid.UUID) error {
	return s.repo.DeleteRoleBinding(ctx, s.db.Pool, bindingID)
}

// GetRoleBindings returns all role bindings for a subject.
func (s *service) GetRoleBindings(ctx context.Context, subjectID uuid.UUID) ([]*RoleBinding, error) {
	return s.repo.ListRoleBindingsBySubject(ctx, s.db.Pool, subjectID)
}

// CheckPermission reports whether a subject may perform an action on a
// resource. Admins are always allowed; otherwise the subject must hold a role
// matching the action within the same organization. Handlers additionally
// enforce tenancy by scoping queries to the identity's org.
func (s *service) CheckPermission(_ context.Context, subject Identity, action string, _ Resource) (bool, error) {
	if subject.HasRole(RoleAdmin) {
		return true, nil
	}
	if subject.HasRole(action) {
		return true, nil
	}
	return false, nil
}
