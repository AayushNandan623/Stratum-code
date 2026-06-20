// Package vcs implements the Version Control System integration bounded
// context: webhook signature validation, push event parsing, and connection
// management. It does not import the iam context.
package vcs

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// VCSProvider identifies a source-control host.
type VCSProvider string

const (
	ProviderGitHub    VCSProvider = "github"
	ProviderGitLab    VCSProvider = "gitlab"
	ProviderBitbucket VCSProvider = "bitbucket"
)

// VCSConnection is a configured link to a source-control host.
type VCSConnection struct {
	ID          uuid.UUID   `json:"id"`
	OrgID       uuid.UUID   `json:"org_id"`
	Provider    VCSProvider `json:"provider"`
	Name        string      `json:"name"`
	BaseURL     string      `json:"base_url"`
	APITokenRef *uuid.UUID  `json:"api_token_ref,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	DeletedAt   *time.Time  `json:"deleted_at,omitempty"`
}

// PushEvent is the normalized result of parsing a provider push webhook.
type PushEvent struct {
	Provider    VCSProvider `json:"provider"`
	RepoURL     string      `json:"repo_url"`
	Branch      string      `json:"branch"`
	CommitSHA   string      `json:"commit_sha"`
	CommitMsg   string      `json:"commit_msg"`
	PusherEmail string      `json:"pusher_email"`
	IsPR        bool        `json:"is_pr"`
	PRNumber    int         `json:"pr_number"`
}

// PRStatusInput updates a commit/PR status on the provider.
type PRStatusInput struct {
	ConnectionID uuid.UUID
	Repo         string
	CommitSHA    string
	State        string // pending | success | failure | error
	Description  string
	TargetURL    string
}

// CreateConnectionInput creates a VCS connection.
type CreateConnectionInput struct {
	OrgID    uuid.UUID
	Provider VCSProvider
	Name     string
	BaseURL  string
}

// VCSService is the boundary contract for the vcs context.
type VCSService interface {
	CreateConnection(ctx context.Context, input CreateConnectionInput) (*VCSConnection, error)
	GetConnection(ctx context.Context, id uuid.UUID) (*VCSConnection, error)
	ListConnections(ctx context.Context, orgID uuid.UUID) ([]*VCSConnection, error)
	DeleteConnection(ctx context.Context, id uuid.UUID) error

	// Source operations — called by worker agent via control plane (Phase 3).
	GetSourceArchive(ctx context.Context, stackID uuid.UUID, ref string) (io.ReadCloser, error)

	// PR status updates — called by run service on state transitions.
	UpdatePRStatus(ctx context.Context, input PRStatusInput) error

	// Webhook validation and parsing.
	ValidateWebhookSignature(ctx context.Context, body []byte, signature string) error
	ParsePushEvent(ctx context.Context, provider VCSProvider, body []byte) (*PushEvent, error)
}

// Sentinel domain errors.
var (
	ErrConnectionNotFound = domainerr.New("VCS_CONNECTION_NOT_FOUND", 404, "vcs connection not found")
	ErrInvalidSignature   = domainerr.New("VCS_SIGNATURE_INVALID", 401, "webhook signature verification failed")
	ErrInvalidPayload     = domainerr.New("VCS_PAYLOAD_INVALID", 422, "could not parse webhook payload")
)
