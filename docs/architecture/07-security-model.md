# Security Model

## Multi-Tenancy Boundary

The top-level isolation unit is the **Organization**. All resources (stacks, runs, workers, secrets, policies) belong to an organization. Cross-organization access is impossible by design — every database query includes `WHERE org_id = $authenticated_org_id`.

There is no super-admin API key with cross-org access in the data plane. Administrative operations (org creation, billing) use a separate internal admin interface not exposed to the public API.

---

## Identity Model

### User Authentication
- Email + password (hashed with bcrypt, cost=12)
- OIDC/OAuth2 (GitHub, Google, SAML for enterprise — Phase 3)
- Sessions via short-lived JWT (15 min) + refresh token (30 days, stored in httpOnly cookie)

### API Key Authentication
- Format: `str_live_xxxx...xxxx` (32 bytes base58-encoded random)
- Stored as HMAC-SHA256 hash — plain key never persisted after creation
- Each key has: name, org_id, creator_id, created_at, last_used_at, expiry (optional), scopes
- Keys can be scoped to specific resources (e.g., read-only on a single stack)

### Worker Authentication
- Worker pool tokens: `wpt_xxxx...xxxx` — issued once, stored as hash
- Worker tokens authenticate against the control plane API for: job polling, event posting, log streaming
- Worker tokens cannot access any API endpoint outside the worker-specific paths
- Each token is associated with exactly one pool — workers inherit pool permissions

---

## RBAC Model

Roles are defined at the organization level and bound to users for specific resource scopes.

### Built-in Roles

| Role | Scope | Capabilities |
|------|-------|-------------|
| `org:owner` | Org | Full access including billing, member management |
| `org:admin` | Org | All technical resources, no billing |
| `stack:writer` | Stack or space | Create/trigger runs, manage variables |
| `stack:reader` | Stack or space | View runs, logs, plan outputs |
| `policy:admin` | Org | Create/update/delete policies |
| `worker:admin` | Org | Manage worker pools and tokens |
| `run:approver` | Stack or space | Approve/reject runs awaiting approval gate |

### Spaces (Resource Groups)
Stacks are organized into **Spaces** (logical groups). Role bindings can target a space, granting access to all stacks within it. This avoids needing per-stack bindings in large organizations.

### Enforcement Points
1. **API middleware** — Every request validated against RBAC before reaching domain handler
2. **Row-level filtering** — Queries always include `org_id` and scope filters derived from the caller's bindings
3. **Policy engine** — OPA policies can reference the caller's identity and role bindings for fine-grained enforcement

```
Table: role_bindings
────────────────────────────────────────────────────────
id              UUID
org_id          UUID
subject_type    ENUM    USER | API_KEY | GROUP
subject_id      UUID
role            VARCHAR
resource_type   ENUM    ORG | SPACE | STACK (null = org-wide)
resource_id     UUID    nullable
created_at      TIMESTAMPTZ
created_by      UUID
```

---

## Secret Management

### Storage
- Secrets are encrypted at rest using AES-256-GCM
- Encryption key is a 256-bit key derived from a KMS-provided root key (AWS KMS / GCP KMS / HashiCorp Vault in Phase 3; local key in Phase 0-1)
- Each secret value is encrypted with a unique IV — same value encrypted twice produces different ciphertext
- Key rotation: re-encrypts all secrets with new derived key, old key retained for decryption until rotation complete

### Access Control
- Secrets are scoped to: org, space, or stack
- Scoped secrets are inherited: stack inherits org + space secrets, with stack-level secrets overriding
- Only users with `stack:writer` or above can create/update secrets
- Secret *values* are never returned by the API after creation — only metadata (name, last updated, masked value)

### Injection
- At run dispatch time, the worker calls the control plane's one-time claim endpoint
- The claim endpoint: verifies the worker token is assigned to the run, returns plaintext values, marks the claim as consumed (idempotent — same values returned on retry within 60s window)
- Values are passed to the Docker container via environment variables — never written to disk
- The worker zeroes the values from its memory after container creation

---

## Policy Engine (OPA)

### Policy Evaluation Points

| Gate | When | Effect |
|------|------|--------|
| `plan.pre_queue` | Before a plan run is queued | Can block run creation |
| `apply.pre_execute` | After plan, before apply | Can block apply (hard-fail or soft-warn) |
| `approval.evaluate` | When an approval is submitted | Can reject an approval |
| `stack.modify` | Before stack config is updated | Can block configuration changes |

### Policy Bundle Structure
```
policies/
  built-in/
    no-public-buckets.rego
    require-tags.rego
    max-resource-count.rego
  org-custom/      (stored in DB, loaded at runtime)
```

### Policy Input Document
OPA receives a structured input document per evaluation:

```json
{
  "run": {
    "id": "r-xxx",
    "type": "apply",
    "stack_id": "s-xxx"
  },
  "stack": {
    "name": "prod-vpc",
    "space_id": "sp-xxx",
    "labels": { "env": "production" }
  },
  "plan": {
    "resource_changes": [
      { "address": "aws_s3_bucket.data", "actions": ["create"], "after": {...} }
    ],
    "total_changes": 3
  },
  "actor": {
    "id": "u-xxx",
    "roles": ["stack:writer"],
    "groups": ["platform-team"]
  },
  "organization": {
    "id": "o-xxx"
  }
}
```

### Policy Verdicts
```json
{
  "allow": true | false,
  "severity": "HARD_FAIL" | "SOFT_WARN",
  "violations": [
    { "policy": "no-public-buckets", "message": "S3 bucket acl=public-read is not allowed", "resource": "aws_s3_bucket.data" }
  ]
}
```

`HARD_FAIL` → run transitions to `POLICY_REJECTED`.
`SOFT_WARN` → run proceeds with warnings recorded in run events.

---

## Network Security

### Control Plane
- TLS 1.3 required on all external endpoints
- CORS restricted to known UI origins
- Rate limiting per API key and per IP (token bucket, 1000 req/min default)
- Request size limits (10MB for log chunk uploads, 1MB for all other endpoints)

### Worker Communication
- Workers connect outbound only (no inbound)
- Long-poll HTTP for job claims (30s timeout, then reconnect)
- All worker API endpoints require valid worker token
- Worker tokens are pool-scoped — cannot access other pools' jobs

### Execution Containers
- No host network access by default (`--network none`)
- Outbound internet access allowed only if stack declares it (required for provider API calls)
- Mounted secrets are environment variables — never written to container filesystem
- Containers run as non-root user

---

## Threat Model Summary

| Threat | Mitigation |
|--------|-----------|
| Compromised worker token | Token scoped to one pool, rotatable, expires |
| Secret exfiltration via logs | Log scanner strips known secret patterns before storage |
| Policy bypass via payload manipulation | OPA input is constructed server-side, never client-supplied |
| Cross-tenant data access | org_id filter on every query; no cross-org joins |
| Replay attacks on worker events | Run-scoped worker tokens; event sequence monotonically increasing |
| Denial of service via run flooding | Per-org run rate limits; pool concurrency limits |
| Malicious IaC code execution | Execution containers are isolated with no host access |
