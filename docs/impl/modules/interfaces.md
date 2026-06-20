# Module Interface Contracts

This document is the canonical reference for all cross-context service interfaces.
These interfaces are the boundaries between bounded contexts.
Import rules: contexts may import these interfaces; they may NOT import each other's concrete types.

---

## IAM Context

```go
// internal/iam/service.go

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
    CreateAPIKey(ctx context.Context, input CreateAPIKeyInput) (*APIKey, string, error) // string = plaintext key (once)
    RevokeAPIKey(ctx context.Context, id uuid.UUID) error

    // RBAC
    GrantRole(ctx context.Context, input GrantRoleInput) error
    RevokeRole(ctx context.Context, bindingID uuid.UUID) error
    CheckPermission(ctx context.Context, subject Identity, action string, resource Resource) (bool, error)
    GetRoleBindings(ctx context.Context, subjectID uuid.UUID) ([]*RoleBinding, error)
}

type Identity struct {
    ID       uuid.UUID
    OrgID    uuid.UUID
    Type     IdentityType  // USER | API_KEY | WORKER | SYSTEM
    Roles    []string
    Raw      string        // original token (for audit)
}

type Resource struct {
    Type string    // ORG | SPACE | STACK
    ID   uuid.UUID
}
```

---

## Stack Context

```go
// internal/stack/service.go

type StackService interface {
    Create(ctx context.Context, input CreateStackInput) (*Stack, error)
    Get(ctx context.Context, id uuid.UUID) (*Stack, error)
    GetByOrgID(ctx context.Context, orgID uuid.UUID, page Pagination) ([]*Stack, int, error)
    Update(ctx context.Context, id uuid.UUID, input UpdateStackInput) (*Stack, error)
    Delete(ctx context.Context, id uuid.UUID) error
    SetStatus(ctx context.Context, id uuid.UUID, status StackStatus) error

    // Variables
    SetVariable(ctx context.Context, stackID uuid.UUID, input VariableInput) error
    DeleteVariable(ctx context.Context, stackID uuid.UUID, key string) error
    ListVariables(ctx context.Context, stackID uuid.UUID) ([]*Variable, error)

    // Dependency graph
    AddDependency(ctx context.Context, stackID, dependsOnID uuid.UUID) error
    RemoveDependency(ctx context.Context, stackID, dependsOnID uuid.UUID) error
    GetDependencies(ctx context.Context, stackID uuid.UUID) ([]*Dependency, error)
    GetDependents(ctx context.Context, stackID uuid.UUID) ([]*Dependency, error)
    IsDAGCyclePresent(ctx context.Context, orgID uuid.UUID, proposedEdge DependencyEdge) (bool, error)

    // Used by scheduler for DAG-aware dispatch
    HasActiveRun(ctx context.Context, stackID uuid.UUID) (bool, error)
    GetUpstreamStatus(ctx context.Context, stackID uuid.UUID) ([]UpstreamStatus, error)
}

type Stack struct {
    ID               uuid.UUID
    OrgID            uuid.UUID
    SpaceID          *uuid.UUID
    Name             string
    Status           StackStatus
    VCSRepo          string
    VCSBranch        string
    WorkingDir       string
    IACTool          string
    IACVersion       string
    WorkerPoolID     *uuid.UUID
    AutoApply        bool
    ReconcileInterval time.Duration
    DriftMode        DriftMode
    CreatedAt        time.Time
    UpdatedAt        time.Time
    DeletedAt        *time.Time
}

type StackStatus string
const (
    StatusActive    StackStatus = "ACTIVE"
    StatusDrifted   StackStatus = "DRIFTED"
    StatusLocked    StackStatus = "LOCKED"
    StatusDestroyed StackStatus = "DESTROYED"
)
```

---

## Run Context

```go
// internal/run/service.go

type RunService interface {
    Create(ctx context.Context, input CreateRunInput) (*Run, error)
    Get(ctx context.Context, id uuid.UUID) (*Run, error)
    List(ctx context.Context, filter RunFilter) ([]*Run, int, error)
    Cancel(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
    Approve(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
    Discard(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

    // State transitions (called by scheduler and workers)
    Transition(ctx context.Context, id uuid.UUID, to RunState, meta any) error
    HasActiveRun(ctx context.Context, stackID uuid.UUID) (bool, error)

    // Event store
    AppendEvent(ctx context.Context, runID uuid.UUID, event RunEventInput) error
    GetTimeline(ctx context.Context, runID uuid.UUID) ([]*RunEvent, error)
    GetPlanOutput(ctx context.Context, runID uuid.UUID) (*PlanOutput, error)
    StorePlanOutput(ctx context.Context, runID uuid.UUID, output *PlanOutput) error

    // Logs
    AppendLogs(ctx context.Context, runID uuid.UUID, lines []LogLine) error
    GetLogs(ctx context.Context, runID uuid.UUID, page Pagination) ([]*LogLine, int, error)
}

type Run struct {
    ID            uuid.UUID
    OrgID         uuid.UUID
    StackID       uuid.UUID
    SpaceID       *uuid.UUID
    RunType       RunType
    CurrentState  RunState
    TriggerType   TriggerType
    TriggeredBy   *uuid.UUID
    ConfigVersion string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

type RunType   string
type RunState  string
type TriggerType string

const (
    RunTypePlan        RunType = "plan"
    RunTypeApply       RunType = "apply"
    RunTypeDestroy     RunType = "destroy"
    RunTypeDriftDetect RunType = "drift_detect"
)

const (
    StatePending          RunState = "PENDING"
    StateQueued           RunState = "QUEUED"
    StateAssigned         RunState = "ASSIGNED"
    StatePlanning         RunState = "PLANNING"
    StatePlanned          RunState = "PLANNED"
    StateAwaitingApproval RunState = "AWAITING_APPROVAL"
    StateApplying         RunState = "APPLYING"
    StateApplied          RunState = "APPLIED"
    StateFailed           RunState = "FAILED"
    StateCancelled        RunState = "CANCELLED"
    StateDiscarded        RunState = "DISCARDED"
    StatePolicyRejected   RunState = "POLICY_REJECTED"
)
```

---

## Worker Context

```go
// internal/worker/service.go

type WorkerService interface {
    // Pool management
    CreatePool(ctx context.Context, input CreatePoolInput) (*WorkerPool, string, error)
    GetPool(ctx context.Context, id uuid.UUID) (*WorkerPool, error)
    ListPools(ctx context.Context, orgID uuid.UUID) ([]*WorkerPool, error)
    DeletePool(ctx context.Context, id uuid.UUID) error
    RotatePoolToken(ctx context.Context, id uuid.UUID) (string, error)

    // Worker lifecycle
    Register(ctx context.Context, input RegisterWorkerInput) (*Worker, error)
    Heartbeat(ctx context.Context, id uuid.UUID, status WorkerStatus) (*HeartbeatResponse, error)
    Deregister(ctx context.Context, id uuid.UUID) error
    GetByTokenHash(ctx context.Context, hash string) (*Worker, error)

    // Job dispatch
    ClaimJob(ctx context.Context, workerID uuid.UUID, timeout time.Duration) (*Job, error)
    CompleteJob(ctx context.Context, jobID uuid.UUID, success bool) error
    ListActiveWorkers(ctx context.Context, poolID uuid.UUID) ([]*Worker, error)
}

type HeartbeatResponse struct {
    CancelRunID *uuid.UUID  // if set, worker must cancel this run
}
```

---

## Policy Context

```go
// internal/policy/service.go

type PolicyService interface {
    // Policy management
    Create(ctx context.Context, input CreatePolicyInput) (*Policy, error)
    Get(ctx context.Context, id uuid.UUID) (*Policy, error)
    List(ctx context.Context, orgID uuid.UUID) ([]*Policy, error)
    Update(ctx context.Context, id uuid.UUID, input UpdatePolicyInput) (*Policy, error)
    UpdateSource(ctx context.Context, id uuid.UUID, source string) error
    Delete(ctx context.Context, id uuid.UUID) error

    // Policy sets
    CreatePolicySet(ctx context.Context, input CreatePolicySetInput) (*PolicySet, error)
    AddToSet(ctx context.Context, setID, policyID uuid.UUID) error
    RemoveFromSet(ctx context.Context, setID, policyID uuid.UUID) error
    BindSet(ctx context.Context, setID uuid.UUID, resource Resource) error
    UnbindSet(ctx context.Context, bindingID uuid.UUID) error

    // Evaluation — called by run scheduler
    Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error)

    // Dry-run evaluation for UI/API consumers
    DryRun(ctx context.Context, input DryRunInput) (*EvaluationResult, error)
}
```

---

## State Context

```go
// internal/state/service.go

type StateService interface {
    // State file operations (Terraform HTTP backend protocol)
    GetState(ctx context.Context, stackID uuid.UUID) (*StateVersion, error)
    StoreState(ctx context.Context, stackID uuid.UUID, data []byte, md5 string) (*StateVersion, error)
    ListVersions(ctx context.Context, stackID uuid.UUID) ([]*StateVersion, error)
    GetVersion(ctx context.Context, versionID uuid.UUID) ([]byte, error)

    // Locking (Terraform HTTP backend lock protocol)
    AcquireLock(ctx context.Context, stackID uuid.UUID, lock LockRequest) error
    ReleaseLock(ctx context.Context, stackID uuid.UUID, lockID string) error
    GetLock(ctx context.Context, stackID uuid.UUID) (*Lock, error)
}

type StateVersion struct {
    ID        uuid.UUID
    StackID   uuid.UUID
    Version   int
    MD5       string
    Size      int64
    StorageURL string  // S3 URL or internal reference
    CreatedAt time.Time
}

type LockRequest struct {
    LockID  string
    Who     string
    Info    string
    Version string
    Created time.Time
}
```

---

## Secret Context

```go
// internal/secret/service.go

type SecretService interface {
    // Secret management
    Set(ctx context.Context, input SetSecretInput) error  // create or update
    Delete(ctx context.Context, stackID uuid.UUID, name string) error
    List(ctx context.Context, stackID uuid.UUID) ([]*SecretMeta, error)  // metadata only, no values

    // One-time value claim — called by worker at dispatch time
    // Returns plaintext values. Marks claim as consumed.
    ClaimValues(ctx context.Context, runID uuid.UUID, workerID uuid.UUID) ([]*SecretValue, error)
    GetEffectiveSecrets(ctx context.Context, stackID uuid.UUID) ([]*SecretMeta, error)
}

type SecretMeta struct {
    Name        string
    Scope       SecretScope  // ORG | SPACE | STACK
    Sensitive   bool
    UpdatedAt   time.Time
    MaskedValue string  // "***" or first 3 chars + "..."
}

type SecretValue struct {
    Name  string
    Value string  // plaintext — zeroed after use
}
```

---

## Reconcile Context

```go
// internal/reconcile/service.go

type ReconcileService interface {
    // Schedule management
    GetSchedule(ctx context.Context, stackID uuid.UUID) (*ReconcileSchedule, error)
    UpdateSchedule(ctx context.Context, stackID uuid.UUID, input UpdateScheduleInput) error
    EnableSchedule(ctx context.Context, stackID uuid.UUID) error
    DisableSchedule(ctx context.Context, stackID uuid.UUID) error

    // Manual trigger
    TriggerNow(ctx context.Context, stackID uuid.UUID, actorID uuid.UUID) (*Run, error)

    // Drift record management
    GetDriftRecord(ctx context.Context, id uuid.UUID) (*DriftRecord, error)
    ListDriftRecords(ctx context.Context, filter DriftFilter) ([]*DriftRecord, int, error)
    IgnoreDrift(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

    // Called by run context callbacks
    ProcessDriftResult(ctx context.Context, runID uuid.UUID, output *PlanOutput) error
    ResolveDrift(ctx context.Context, stackID uuid.UUID) error
}
```

---

## Events Context

```go
// internal/events/bus.go

type EventBus interface {
    Publish(ctx context.Context, subject string, payload any) error
    PublishTx(ctx context.Context, tx pgx.Tx, subject string, payload any) error
    Subscribe(ctx context.Context, subject string, handler MessageHandler) error
    Close() error
}

// Canonical subject constants — import this package for type-safe subjects
const (
    SubjectRunEvents    = "stratum.runs.events.%s"     // format with run_id
    SubjectRunLogs      = "stratum.runs.logs.%s"       // format with run_id
    SubjectStackEvents  = "stratum.stacks.events.%s"   // format with stack_id
    SubjectStackDrifted = "stratum.stacks.drifted.%s"  // format with org_id
    SubjectAudit        = "stratum.audit.%s"           // format with org_id
    SubjectReconcile    = "stratum.reconcile.trigger.%s" // format with stack_id
)
```

---

## VCS Context

```go
// internal/vcs/service.go

type VCSService interface {
    // Connection management
    CreateConnection(ctx context.Context, input CreateConnectionInput) (*VCSConnection, error)
    GetConnection(ctx context.Context, id uuid.UUID) (*VCSConnection, error)
    ListConnections(ctx context.Context, orgID uuid.UUID) ([]*VCSConnection, error)
    DeleteConnection(ctx context.Context, id uuid.UUID) error

    // Source operations — called by worker agent via control plane
    GetSourceArchive(ctx context.Context, stackID uuid.UUID, ref string) (io.ReadCloser, error)

    // PR status updates — called by run service on state transitions
    UpdatePRStatus(ctx context.Context, input PRStatusInput) error

    // Webhook validation — called by webhook receiver
    ValidateWebhookSignature(ctx context.Context, connectionID uuid.UUID, body []byte, signature string) error
    ParsePushEvent(ctx context.Context, provider VCSProvider, body []byte) (*PushEvent, error)
}

type PushEvent struct {
    Provider    VCSProvider
    RepoURL     string
    Branch      string
    CommitSHA   string
    CommitMsg   string
    PusherEmail string
    IsPR        bool
    PRNumber    int
}
```
