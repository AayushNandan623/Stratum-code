// Package agent implements the worker agent that connects to the Stratum
// control plane, polls for jobs, and executes them via the configured executor.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/worker"
)

// Client is an HTTP client for the Stratum control plane internal worker API.
type Client struct {
	baseURL    string
	token      string // Bearer token sent as Authorization header
	httpClient *http.Client
}

// SetToken sets the bearer token for subsequent requests.
func (c *Client) SetToken(token string) {
	c.token = token
}

// NewClient creates a Client pointing at the given control plane base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// ─── Types matching the internal API JSON contracts ─────────────────────────

type registerRequest struct {
	PoolID       string   `json:"pool_id"`
	Hostname     string   `json:"hostname"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

type registerResponse struct {
	WorkerID            string `json:"worker_id"`
	HeartbeatIntervalS  int    `json:"heartbeat_interval_s"`
}

type heartbeatRequest struct {
	Status       string  `json:"status"`
	CurrentRunID *string `json:"current_run_id,omitempty"`
}

type heartbeatResponse struct {
	CancelRunID *string `json:"cancel_run_id,omitempty"`
}

type jobResponse struct {
	JobID      string `json:"job_id"`
	RunID      string `json:"run_id"`
	RunType    string `json:"run_type"`
	StackID    string `json:"stack_id"`
	IACTool    string `json:"iac_tool"`
	IACVersion string `json:"iac_version"`
}

type eventRequest struct {
	EventType  string          `json:"event_type"`
	ActorID    *string         `json:"actor_id,omitempty"`
	ActorType  string          `json:"actor_type,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type logsRequest struct {
	Lines []logLine `json:"lines"`
}

type logLine struct {
	Line       string    `json:"line"`
	Source     string    `json:"source"`
	OccurredAt time.Time `json:"occurred_at"`
}

type claimSecretsRequest struct {
	WorkerID string `json:"worker_id"`
}

type secretValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type claimSecretsResponse struct {
	Secrets []secretValue `json:"secrets"`
}

// ─── Registration ───────────────────────────────────────────────────────────

// Register sends a POST /api/v1/internal/workers/register request.
func (c *Client) Register(ctx context.Context, poolID, hostname, version string, capabilities []string) (uuid.UUID, int, error) {
	body := registerRequest{
		PoolID:       poolID,
		Hostname:     hostname,
		Version:      version,
		Capabilities: capabilities,
	}
	var resp registerResponse
	if err := c.post(ctx, "/api/v1/internal/workers/register", body, &resp); err != nil {
		return uuid.Nil, 0, err
	}
	workerID, err := uuid.Parse(resp.WorkerID)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("parse worker_id: %w", err)
	}
	if resp.HeartbeatIntervalS <= 0 {
		resp.HeartbeatIntervalS = 15
	}
	return workerID, resp.HeartbeatIntervalS, nil
}

// ─── Heartbeat ──────────────────────────────────────────────────────────────

// HeartbeatResponse carries the control plane response including optional
// cancellation signal.
type HeartbeatResp struct {
	CancelRunID *uuid.UUID
}

// Heartbeat sends a POST /api/v1/internal/workers/{id}/heartbeat request.
func (c *Client) Heartbeat(ctx context.Context, workerID uuid.UUID, status string, currentRunID *uuid.UUID) (*HeartbeatResp, error) {
	var runIDStr *string
	if currentRunID != nil {
		s := currentRunID.String()
		runIDStr = &s
	}
	body := heartbeatRequest{
		Status:       status,
		CurrentRunID: runIDStr,
	}
	var resp heartbeatResponse
	if err := c.post(ctx, fmt.Sprintf("/api/v1/internal/workers/%s/heartbeat", workerID), body, &resp); err != nil {
		return nil, err
	}
	result := &HeartbeatResp{}
	if resp.CancelRunID != nil {
		id, err := uuid.Parse(*resp.CancelRunID)
		if err == nil {
			result.CancelRunID = &id
		}
	}
	return result, nil
}

// ─── Job polling ────────────────────────────────────────────────────────────

// PollJob sends a GET /api/v1/internal/workers/{id}/jobs request.
// Returns nil, ErrNoJob when no job is available.
func (c *Client) PollJob(ctx context.Context, workerID uuid.UUID, timeout time.Duration) (*worker.Job, error) {
	url := fmt.Sprintf("%s/api/v1/internal/workers/%s/jobs?timeout=%d",
		c.baseURL, workerID, int(timeout.Seconds()))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, ErrNoJob
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("poll job: status=%d body=%s", resp.StatusCode, string(body))
	}

	var jobResp jobResponse
	if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
		return nil, fmt.Errorf("decode job: %w", err)
	}
	jobID, _ := uuid.Parse(jobResp.JobID)
	runID, _ := uuid.Parse(jobResp.RunID)
	stackID, _ := uuid.Parse(jobResp.StackID)

	return &worker.Job{
		ID:         jobID,
		RunID:      runID,
		StackID:    stackID,
		RunType:    jobResp.RunType,
		IACTool:    jobResp.IACTool,
		IACVersion: jobResp.IACVersion,
		Status:     "CLAIMED",
	}, nil
}

// ─── Deregister ─────────────────────────────────────────────────────────────

// Deregister sends a DELETE /api/v1/internal/workers/{id} request.
func (c *Client) Deregister(ctx context.Context, workerID uuid.UUID) error {
	return c.delete(ctx, fmt.Sprintf("/api/v1/internal/workers/%s", workerID))
}

// ─── Events ─────────────────────────────────────────────────────────────────

// AppendEvent sends a POST /api/v1/internal/runs/{id}/events request.
func (c *Client) AppendEvent(ctx context.Context, runID uuid.UUID, eventType string, payload json.RawMessage, occurredAt time.Time) error {
	body := eventRequest{
		EventType:  eventType,
		Payload:    payload,
		OccurredAt: occurredAt,
	}
	return c.post(ctx, fmt.Sprintf("/api/v1/internal/runs/%s/events", runID), body, nil)
}

// ─── Logs ───────────────────────────────────────────────────────────────────

// AppendLogs sends a POST /api/v1/internal/runs/{id}/logs request.
func (c *Client) AppendLogs(ctx context.Context, runID uuid.UUID, lines []worker.LogLine) error {
	reqLines := make([]logLine, len(lines))
	for i, l := range lines {
		reqLines[i] = logLine{Line: l.Line, Source: l.Source, OccurredAt: l.OccurredAt}
	}
	body := logsRequest{Lines: reqLines}
	return c.post(ctx, fmt.Sprintf("/api/v1/internal/runs/%s/logs", runID), body, nil)
}

// ─── Source archive ─────────────────────────────────────────────────────────

// GetSourceArchive sends a GET /api/v1/internal/runs/{id}/source-archive request.
// Returns the archive as a reader. Caller must close it.
func (c *Client) GetSourceArchive(ctx context.Context, runID uuid.UUID) (io.ReadCloser, error) {
	url := fmt.Sprintf("%s/api/v1/internal/runs/%s/source-archive", c.baseURL, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("get source archive: status=%d", resp.StatusCode)
	}
	return resp.Body, nil
}

// ─── Secrets ────────────────────────────────────────────────────────────────

// ClaimSecrets sends a POST /api/v1/internal/runs/{id}/secrets/claim request.
func (c *Client) ClaimSecrets(ctx context.Context, runID, workerID uuid.UUID) ([]worker.EnvVar, error) {
	body := claimSecretsRequest{WorkerID: workerID.String()}
	var resp claimSecretsResponse
	if err := c.post(ctx, fmt.Sprintf("/api/v1/internal/runs/%s/secrets/claim", runID), body, &resp); err != nil {
		return nil, err
	}
	env := make([]worker.EnvVar, len(resp.Secrets))
	for i, s := range resp.Secrets {
		env[i] = worker.EnvVar{Name: s.Name, Value: s.Value}
	}
	return env, nil
}

// ─── HTTP helpers ───────────────────────────────────────────────────────────

func (c *Client) post(ctx context.Context, path string, body any, dest any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post %s: status=%d body=%s", path, resp.StatusCode, string(bodyBytes))
	}
	if dest != nil {
		return json.NewDecoder(resp.Body).Decode(dest)
	}
	return nil
}

func (c *Client) delete(ctx context.Context, path string) error {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete %s: status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return nil
}

// ─── Error sentinels ────────────────────────────────────────────────────────

var (
	ErrNoJob = errors.New("no job available")
)
