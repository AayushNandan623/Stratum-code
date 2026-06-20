// GitHub provider integration: push event parsing and commit-status posting.
// The HTTP client for live API calls arrives in a later phase; UpdatePRStatus is
// a stub for now.
package vcs

import (
	"encoding/json"
	"strings"
)

type githubPushPayload struct {
	Ref         string `json:"ref"`
	After       string `json:"after"`
	Repository  struct {
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
		SSHURL   string `json:"ssh_url"`
	} `json:"repository"`
	HeadCommit struct {
		Message string `json:"message"`
	} `json:"head_commit"`
	Pusher struct {
		Email string `json:"email"`
	} `json:"pusher"`
}

// ParseGitHubPush extracts a normalized PushEvent from a GitHub push webhook
// body.
func ParseGitHubPush(body []byte) (*PushEvent, error) {
	var p githubPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, ErrInvalidPayload
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	repoURL := p.Repository.CloneURL
	if repoURL == "" {
		repoURL = p.Repository.HTMLURL
	}
	return &PushEvent{
		Provider:    ProviderGitHub,
		RepoURL:     repoURL,
		Branch:      branch,
		CommitSHA:   p.After,
		CommitMsg:   p.HeadCommit.Message,
		PusherEmail: p.Pusher.Email,
		IsPR:        false,
	}, nil
}

// postGitHubCommitStatus updates a commit status on GitHub. Stub until the
// provider HTTP client is implemented in a later phase.
func postGitHubCommitStatus(_ PRStatusInput) error {
	return nil
}
