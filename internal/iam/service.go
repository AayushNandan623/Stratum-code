// IAM service: authentication (login, JWT sessions, API key validation) and
// the principal loading logic that backs the auth middleware. Org/user/API key
// management lives in admin.go.
package iam

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/yourorg/stratum/internal/platform/db"
)

const (
	apiKeyPrefix = "stratum_" // distinguishes API keys from JWTs in the auth header
	accessTTL    = 15 * time.Minute
	refreshTTL   = 7 * 24 * time.Hour
)

// service implements IAMService. Revoked refresh-token JTIs are tracked in
// memory; on restart the set clears and outstanding refresh tokens remain
// valid until their natural expiry (acceptable for Phase 1).
type service struct {
	repo      *Repository
	db        *db.DB
	jwtSecret []byte
	revoked   sync.Map // map[string]struct{} of revoked refresh JTIs
}

// NewService constructs an IAMService backed by database and signed with
// jwtSecret.
func NewService(database *db.DB, jwtSecret string) IAMService {
	return &service{
		repo:      NewRepository(),
		db:        database,
		jwtSecret: []byte(jwtSecret),
	}
}

var _ IAMService = (*service)(nil)

type claims struct {
	UserID string `json:"sub"`
	OrgID  string `json:"org_id"`
	Type   string `json:"type"`
	jwt.RegisteredClaims
}

// signToken issues a signed HS256 JWT for the given principal and returns the
// token string and its JTI.
func (s *service) signToken(userID, orgID uuid.UUID, tokenType string, ttl time.Duration) (string, string, error) {
	now := time.Now()
	jti := uuid.NewString()
	c := claims{
		UserID: userID.String(),
		OrgID:  orgID.String(),
		Type:   tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(s.jwtSecret)
	if err != nil {
		return "", "", fmt.Errorf("iam: sign token: %w", err)
	}
	return signed, jti, nil
}

// parseToken verifies the signature and expiry of a JWT and returns its claims.
func (s *service) parseToken(tokenStr string) (*claims, error) {
	c := &claims{}
	_, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("iam: unexpected signing method %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, ErrInvalidToken
	}
	return c, nil
}

// hashAPIKey returns the deterministic HMAC-SHA256 hex digest of a raw API key,
// keyed with the JWT secret. Used for both storage and lookup.
func hashAPIKey(raw string, secret []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// generateKeySecret returns 32 random bytes as URL-safe base64.
func generateKeySecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("iam: generate key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// loadRoles returns the distinct roles granted to a subject.
func (s *service) loadRoles(ctx context.Context, subjectID uuid.UUID) []string {
	bindings, err := s.repo.ListRoleBindingsBySubject(ctx, s.db.Pool, subjectID)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var roles []string
	for _, b := range bindings {
		if _, ok := seen[b.Role]; ok {
			continue
		}
		seen[b.Role] = struct{}{}
		roles = append(roles, b.Role)
	}
	return roles
}

// Login verifies credentials and issues an access + refresh token pair.
func (s *service) Login(ctx context.Context, email, password string) (*Session, error) {
	user, err := s.repo.GetUserByEmailGlobal(ctx, s.db.Pool, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return s.issueSession(ctx, user)
}

// issueSession mints a fresh access + refresh token pair for a user.
func (s *service) issueSession(_ context.Context, user *User) (*Session, error) {
	access, _, err := s.signToken(user.ID, user.OrgID, "access", accessTTL)
	if err != nil {
		return nil, err
	}
	refresh, _, err := s.signToken(user.ID, user.OrgID, "refresh", refreshTTL)
	if err != nil {
		return nil, err
	}
	return &Session{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTTL.Seconds()),
		User:         user,
	}, nil
}

// RefreshSession validates a refresh token, rotates it, and returns a new pair.
func (s *service) RefreshSession(ctx context.Context, refreshToken string) (*Session, error) {
	c, err := s.parseToken(refreshToken)
	if err != nil {
		return nil, err
	}
	if c.Type != "refresh" {
		return nil, ErrInvalidToken
	}
	if _, revoked := s.revoked.Load(c.ID); revoked {
		return nil, ErrTokenRevoked
	}
	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	user, err := s.repo.GetUserByID(ctx, s.db.Pool, userID)
	if err != nil {
		return nil, err
	}
	access, _, err := s.signToken(userID, orgID, "access", accessTTL)
	if err != nil {
		return nil, err
	}
	newRefresh, _, err := s.signToken(userID, orgID, "refresh", refreshTTL)
	if err != nil {
		return nil, err
	}
	// Rotate: invalidate the presented refresh token.
	s.revoked.Store(c.ID, struct{}{})
	return &Session{
		AccessToken:  access,
		RefreshToken: newRefresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTTL.Seconds()),
		User:         user,
	}, nil
}

// Logout revokes a refresh token by its JTI. It is idempotent.
func (s *service) Logout(_ context.Context, refreshToken string) error {
	c, err := s.parseToken(refreshToken)
	if err != nil || c.Type != "refresh" {
		return nil
	}
	s.revoked.Store(c.ID, struct{}{})
	return nil
}

// ValidateAPIKey hashes the raw key, looks it up, and returns the identity.
func (s *service) ValidateAPIKey(ctx context.Context, rawKey string) (*Identity, error) {
	hash := hashAPIKey(rawKey, s.jwtSecret)
	key, err := s.repo.GetAPIKeyByHash(ctx, s.db.Pool, hash)
	if err != nil {
		return nil, ErrInvalidToken
	}
	_ = s.repo.TouchAPIKeyLastUsed(ctx, s.db.Pool, key.ID) // best-effort
	return &Identity{
		ID:    key.ID,
		OrgID: key.OrgID,
		Type:  IdentityAPIKey,
		Roles: s.loadRoles(ctx, key.ID),
		Raw:   rawKey,
	}, nil
}

// ValidateJWT verifies an access token and returns the identity.
func (s *service) ValidateJWT(ctx context.Context, token string) (*Identity, error) {
	c, err := s.parseToken(token)
	if err != nil {
		return nil, err
	}
	if c.Type != "access" {
		return nil, ErrInvalidToken
	}
	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	orgID, err := uuid.Parse(c.OrgID)
	if err != nil {
		return nil, ErrInvalidToken
	}
	return &Identity{
		ID:    userID,
		OrgID: orgID,
		Type:  IdentityUser,
		Roles: s.loadRoles(ctx, userID),
		Raw:   token,
	}, nil
}
