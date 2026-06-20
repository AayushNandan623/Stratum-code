// Webhook handlers: receive VCS push events, validate signatures, and trigger
// stack lookups. Event processing is logged synchronously for Phase 1; async
// dispatch via the event bus arrives in a later phase.
package handlers

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/yourorg/stratum/internal/api/httpjson"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
	"github.com/yourorg/stratum/internal/stack"
	"github.com/yourorg/stratum/internal/vcs"
)

// WebhooksHandler exposes VCS webhook endpoints.
type WebhooksHandler struct {
	vcsSvc   vcs.VCSService
	stackSvc stack.StackService
	logger   *slog.Logger
}

// NewWebhooksHandler constructs a WebhooksHandler.
func NewWebhooksHandler(vcsSvc vcs.VCSService, stackSvc stack.StackService, logger *slog.Logger) *WebhooksHandler {
	return &WebhooksHandler{vcsSvc: vcsSvc, stackSvc: stackSvc, logger: logger}
}

// GitHub handles POST /api/v1/webhooks/github. Validates the HMAC signature,
// parses the push event, and logs a stack.vcs_push entry for each matching
// stack. Returns 401 on a forged signature.
func (h *WebhooksHandler) GitHub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	if err := h.vcsSvc.ValidateWebhookSignature(r.Context(), body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	event, err := h.vcsSvc.ParsePushEvent(r.Context(), vcs.ProviderGitHub, body)
	if err != nil {
		httpjson.WriteError(w, err)
		return
	}
	h.dispatchPush(r, event)
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// GitLab handles POST /api/v1/webhooks/gitlab. Validates the signature; full
// push parsing arrives with the GitLab provider client in a later phase.
func (h *WebhooksHandler) GitLab(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpjson.WriteError(w, domainerr.ErrValidation)
		return
	}
	if err := h.vcsSvc.ValidateWebhookSignature(r.Context(), body, r.Header.Get("X-Gitlab-Signature-256")); err != nil {
		httpjson.WriteError(w, err)
		return
	}
	httpjson.WriteJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

// dispatchPush looks up stacks matching the pushed repo and branch and logs a
// stack.vcs_push entry for each.
func (h *WebhooksHandler) dispatchPush(r *http.Request, event *vcs.PushEvent) {
	if event == nil {
		return
	}
	stacks, err := h.stackSvc.ListByVCS(r.Context(), event.RepoURL, event.Branch)
	if err != nil {
		h.logger.Error("vcs webhook stack lookup failed",
			"error", err, "repo", event.RepoURL, "branch", event.Branch)
		return
	}
	for _, s := range stacks {
		h.logger.Info("stack.vcs_push",
			"stack_id", s.ID,
			"org_id", s.OrgID,
			"repo", event.RepoURL,
			"branch", event.Branch,
			"commit_sha", event.CommitSHA,
			"pusher", event.PusherEmail,
		)
	}
}
