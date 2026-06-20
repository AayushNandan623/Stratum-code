# Phase 1: Stack Management

## Scope

**IN scope:**
- IAM context: orgs, users, API key auth, JWT sessions, RBAC middleware
- Stack context: CRUD, variable management, dependency graph, space management
- Secret context: create/update/delete secrets, encryption at rest
- State context: state file upload/download, version history, locking API stubs
- VCS context: webhook receiver (GitHub), HMAC validation, push event parsing
- REST API endpoints for all of the above
- Authentication middleware (API key + JWT)

**OUT of scope:**
- Run creation/execution (Phase 2)
- Worker management (Phase 3)
- Policy evaluation (Phase 4)
- Reconciler (Phase 5)
- Approval gates (Phase 2)

---

## Prerequisites

Phase 0 complete. Migrations 001-010 applied. Platform packages exist.

---

## API Endpoints to Implement

### Organizations
```
POST   /api/v1/orgs                          Create org
GET    /api/v1/orgs/{org_id}                 Get org
```

### Auth
```
POST   /api/v1/auth/login                    Email + password → JWT + refresh token
POST   /api/v1/auth/refresh                  Refresh token → new JWT
POST   /api/v1/auth/logout                   Invalidate refresh token
POST   /api/v1/orgs/{org_id}/api-keys        Create API key (returns key once)
DELETE /api/v1/orgs/{org_id}/api-keys/{id}   Revoke API key
```

### Stacks
```
POST   /api/v1/orgs/{org_id}/stacks          Create stack
GET    /api/v1/orgs/{org_id}/stacks          List stacks (paginated)
GET    /api/v1/stacks/{stack_id}             Get stack
PATCH  /api/v1/stacks/{stack_id}             Update stack config
DELETE /api/v1/stacks/{stack_id}             Soft delete stack
POST   /api/v1/stacks/{stack_id}/dependencies Add dependency edge
DELETE /api/v1/stacks/{stack_id}/dependencies/{dep_id} Remove dependency
GET    /api/v1/stacks/{stack_id}/dependencies Get dependency graph (adjacency list)
```

### Variables
```
PUT    /api/v1/stacks/{stack_id}/variables/{key}   Create or update variable
DELETE /api/v1/stacks/{stack_id}/variables/{key}   Delete variable
GET    /api/v1/stacks/{stack_id}/variables         List variables (values masked for sensitive)
```

### Secrets
```
PUT    /api/v1/stacks/{stack_id}/secrets/{name}    Create or update secret
DELETE /api/v1/stacks/{stack_id}/secrets/{name}    Delete secret
GET    /api/v1/stacks/{stack_id}/secrets           List secrets (names + metadata only, no values)
```

### State
```
GET    /api/v1/stacks/{stack_id}/state             Get current state metadata
GET    /api/v1/stacks/{stack_id}/state/versions    List state versions
POST   /api/v1/stacks/{stack_id}/state/lock        Acquire state lock
DELETE /api/v1/stacks/{stack_id}/state/lock        Release state lock
```
*(State upload/download by workers is implemented as part of Phase 3 worker protocol)*

### VCS Webhooks
```
POST   /api/v1/webhooks/github    Receive GitHub push events
POST   /api/v1/webhooks/gitlab    Receive GitLab push events
```

---

## IAM Implementation Notes

### Authentication Flow
```
API Key auth:
  1. Extract Bearer token from Authorization header
  2. Compute HMAC-SHA256 of token
  3. SELECT * FROM api_keys WHERE key_hash = $hash AND (expires_at IS NULL OR expires_at > now())
  4. If found: load org_id, load role_bindings for this key
  5. Set authenticated identity on request context

JWT auth:
  1. Parse and verify JWT signature (HS256, secret from STRATUM_JWT_SECRET env var)
  2. Validate exp claim
  3. Extract user_id and org_id from claims
  4. Load role_bindings for user
```

### RBAC Enforcement
```go
// Middleware check for each handler
func RequireRole(role string, resourceType string) Middleware {
    return func(next http.Handler) http.Handler {
        // Get identity from context
        // Check role_bindings for matching role + resource scope
        // 403 if no matching binding
    }
}
```

---

## Stack Dependency Graph — Cycle Detection

Use DFS with coloring (WHITE/GRAY/BLACK):

```
On POST /stacks/{id}/dependencies body: { depends_on_id: "..." }:
  1. Load existing adjacency list for the org
  2. Temporarily add the proposed edge
  3. Run DFS from the dependent stack
  4. If we visit a GRAY node: cycle detected → 409 Conflict
  5. If DFS completes cleanly: persist the edge
```

The graph is small (hundreds of stacks per org at most). In-memory DFS per write is correct and fast.

---

## Secret Encryption

```
Encryption key: AES-256 key = HKDF(STRATUM_ENCRYPTION_KEY, salt=org_id, info="secrets")
  → Each org gets a derived key, so compromising one org's key does not affect others

Per secret:
  nonce = 12 random bytes (crypto/rand)
  ciphertext = AES-256-GCM Seal(plaintext, nonce, org-derived-key, additionalData=secret_name)
  stored: base64(nonce || ciphertext)

Decryption:
  decode base64
  split nonce (first 12 bytes) and ciphertext (rest)
  AES-256-GCM Open(ciphertext, nonce, org-derived-key, additionalData=secret_name)
```

Key rotation (Phase 3): New derived key from new master key; re-encrypt all secrets in a background job.

---

## VCS Webhook Validation

```
GitHub:
  1. Read X-Hub-Signature-256 header
  2. Compute HMAC-SHA256(body, vcs_connection.webhook_secret)
  3. Constant-time compare
  4. If mismatch → 401

On valid push event:
  1. Parse repository URL + branch from payload
  2. SELECT * FROM stacks WHERE vcs_repo = $repo AND vcs_branch = $branch AND org matching VCS connection
  3. For each matching stack: publish internal event stack.vcs_push{stack_id, commit_sha, pusher}
  4. Return 200 immediately (event processing is async)
```

---

## Validation Criteria

After Phase 1:
1. Can create org + user + login → receive JWT
2. Can create a stack with variables and secrets
3. Can add stack dependencies; cycle detection returns 409 on circular dep
4. GitHub webhook push event triggers `stack.vcs_push` log line (stack lookup)
5. Secret values are encrypted in DB (verify via direct SQL: `SELECT value FROM secrets` shows ciphertext, not plaintext)
6. RBAC: API key with `stack:reader` role cannot PATCH a stack (returns 403)
