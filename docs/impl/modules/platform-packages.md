# Platform Package Reference

Reference specification for `internal/platform/` packages.
These are implemented in Phase 0 and used by every other context.

---

## `internal/platform/config/config.go`

```go
// Package config loads all application configuration from environment variables.
// No config files, no viper. Pure os.Getenv with validation and defaults.
package config

import (
    "fmt"
    "os"
    "strconv"
    "time"
)

type Config struct {
    // Server
    HTTPPort    int    // STRATUM_HTTP_PORT (default: 8080)
    Env         string // STRATUM_ENV: development | production

    // Database
    DBURL       string // STRATUM_DB_URL (required)
    DBMaxConns  int    // STRATUM_DB_MAX_CONNS (default: 25)

    // Auth
    JWTSecret         string        // STRATUM_JWT_SECRET (required)
    JWTAccessExpiry   time.Duration // STRATUM_JWT_ACCESS_EXPIRY (default: 15m)
    JWTRefreshExpiry  time.Duration // STRATUM_JWT_REFRESH_EXPIRY (default: 720h)
    WorkerHMACSecret  string        // STRATUM_WORKER_HMAC_SECRET (required in prod)

    // Encryption
    EncryptionKey string // STRATUM_ENCRYPTION_KEY: 64-char hex (32 bytes)

    // NATS (optional in Phase 0-5, required in Phase 6)
    NATSUrl     string // STRATUM_NATS_URL (default: "" = disabled)

    // Logging
    LogLevel    string // STRATUM_LOG_LEVEL: debug|info|warn|error (default: info)

    // Notifications (optional)
    SlackWebhookURL string // STRATUM_SLACK_WEBHOOK_URL (empty = disabled)

    // Scheduler
    SchedulerTickInterval time.Duration // STRATUM_SCHEDULER_TICK_INTERVAL (default: 5s)

    // Reconciler
    ReconcilerPoolSize int // STRATUM_RECONCILER_POOL_SIZE (default: 5)

    // Worker (for hosted pool, if running workers in-process)
    WorkerPoolSize int // STRATUM_HOSTED_WORKER_POOL_SIZE (default: 0 = disabled)
}

func Load() (*Config, error) {
    cfg := &Config{}

    // Required fields
    cfg.DBURL = requireEnv("STRATUM_DB_URL")
    cfg.JWTSecret = requireEnv("STRATUM_JWT_SECRET")

    // Optional with defaults
    cfg.HTTPPort = envInt("STRATUM_HTTP_PORT", 8080)
    cfg.Env = envString("STRATUM_ENV", "development")
    cfg.DBMaxConns = envInt("STRATUM_DB_MAX_CONNS", 25)
    cfg.LogLevel = envString("STRATUM_LOG_LEVEL", "info")
    cfg.SchedulerTickInterval = envDuration("STRATUM_SCHEDULER_TICK_INTERVAL", 5*time.Second)
    cfg.ReconcilerPoolSize = envInt("STRATUM_RECONCILER_POOL_SIZE", 5)

    cfg.JWTAccessExpiry = envDuration("STRATUM_JWT_ACCESS_EXPIRY", 15*time.Minute)
    cfg.JWTRefreshExpiry = envDuration("STRATUM_JWT_REFRESH_EXPIRY", 30*24*time.Hour)

    // Optional
    cfg.EncryptionKey = os.Getenv("STRATUM_ENCRYPTION_KEY")
    cfg.WorkerHMACSecret = os.Getenv("STRATUM_WORKER_HMAC_SECRET")
    cfg.NATSUrl = os.Getenv("STRATUM_NATS_URL")
    cfg.SlackWebhookURL = os.Getenv("STRATUM_SLACK_WEBHOOK_URL")

    if err := cfg.validate(); err != nil {
        return nil, err
    }
    return cfg, nil
}

func (c *Config) validate() error {
    if c.Env == "production" {
        if c.EncryptionKey == "" {
            return fmt.Errorf("STRATUM_ENCRYPTION_KEY is required in production")
        }
        if len(c.EncryptionKey) != 64 {
            return fmt.Errorf("STRATUM_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
        }
        if c.WorkerHMACSecret == "" {
            return fmt.Errorf("STRATUM_WORKER_HMAC_SECRET is required in production")
        }
    }
    if c.HTTPPort < 1 || c.HTTPPort > 65535 {
        return fmt.Errorf("STRATUM_HTTP_PORT must be 1-65535")
    }
    return nil
}

// IsDevelopment returns true if running in development mode.
// Development mode: relaxed validation, verbose logging, no TLS required.
func (c *Config) IsDevelopment() bool { return c.Env == "development" }
```

---

## `internal/platform/db/db.go`

```go
// Package db provides PostgreSQL connection management and transaction helpers.
package db

import (
    "context"
    "fmt"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx.
// All repository methods accept DBTX to support both transactional and non-transactional use.
type DBTX interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Connect creates a pgxpool connection pool.
func Connect(ctx context.Context, connStr string, maxConns int) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(connStr)
    if err != nil {
        return nil, fmt.Errorf("db.Connect: parse config: %w", err)
    }
    cfg.MaxConns = int32(maxConns)
    cfg.MinConns = 2

    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil {
        return nil, fmt.Errorf("db.Connect: new pool: %w", err)
    }
    if err := pool.Ping(ctx); err != nil {
        return nil, fmt.Errorf("db.Connect: ping: %w", err)
    }
    return pool, nil
}

// WithTx executes fn within a PostgreSQL transaction.
// Automatically commits on success or rolls back on error or panic.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
    tx, err := pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("db.WithTx: begin: %w", err)
    }
    defer func() {
        if p := recover(); p != nil {
            tx.Rollback(ctx)
            panic(p)
        }
    }()
    if err := fn(tx); err != nil {
        tx.Rollback(ctx)
        return err
    }
    return tx.Commit(ctx)
}

// IsNotFound returns true if err is a pgx "no rows" error.
func IsNotFound(err error) bool {
    return err == pgx.ErrNoRows
}

// IsUniqueViolation returns true if err is a PostgreSQL unique constraint violation.
func IsUniqueViolation(err error) bool {
    if pgErr, ok := err.(*pgconn.PgError); ok {
        return pgErr.Code == "23505"
    }
    return false
}

// IsForeignKeyViolation returns true if err is a PostgreSQL FK violation.
func IsForeignKeyViolation(err error) bool {
    if pgErr, ok := err.(*pgconn.PgError); ok {
        return pgErr.Code == "23503"
    }
    return false
}
```

---

## `internal/platform/errors/errors.go`

```go
// Package errors defines domain error types used across all contexts.
package errors

import (
    "fmt"
    "net/http"
)

// DomainError is a typed error with an HTTP status code and a machine-readable code.
// All service-layer errors should be DomainError or wrap DomainError.
type DomainError struct {
    Code       string // machine-readable: "STACK_NOT_FOUND", "RUN_ALREADY_CANCELLED"
    Message    string // human-readable description
    HTTPStatus int    // HTTP status code for API responses
    Cause      error  // wrapped underlying error (optional)
}

func (e *DomainError) Error() string {
    if e.Cause != nil {
        return fmt.Sprintf("%s: %v", e.Message, e.Cause)
    }
    return e.Message
}

func (e *DomainError) Unwrap() error { return e.Cause }

// Standard domain errors — add to this list as new contexts are implemented.
var (
    ErrNotFound = func(resource string) *DomainError {
        return &DomainError{
            Code:       resource + "_NOT_FOUND",
            Message:    resource + " not found",
            HTTPStatus: http.StatusNotFound,
        }
    }

    ErrForbidden = &DomainError{
        Code: "FORBIDDEN", Message: "insufficient permissions", HTTPStatus: http.StatusForbidden,
    }

    ErrUnauthorized = &DomainError{
        Code: "UNAUTHORIZED", Message: "authentication required", HTTPStatus: http.StatusUnauthorized,
    }

    ErrConflict = func(msg string) *DomainError {
        return &DomainError{Code: "CONFLICT", Message: msg, HTTPStatus: http.StatusConflict}
    }

    ErrValidation = func(msg string) *DomainError {
        return &DomainError{Code: "VALIDATION_ERROR", Message: msg, HTTPStatus: http.StatusUnprocessableEntity}
    }

    // Run-specific
    ErrRunInvalidTransition = func(from, to string) *DomainError {
        return &DomainError{
            Code:       "INVALID_STATE_TRANSITION",
            Message:    fmt.Sprintf("cannot transition run from %s to %s", from, to),
            HTTPStatus: http.StatusConflict,
        }
    }

    ErrStackLocked = &DomainError{
        Code: "STACK_LOCKED", Message: "stack has an active run in progress", HTTPStatus: http.StatusConflict,
    }

    ErrCyclicDependency = &DomainError{
        Code: "CYCLIC_DEPENDENCY", Message: "dependency would create a cycle", HTTPStatus: http.StatusUnprocessableEntity,
    }

    // Secret-specific
    ErrSecretClaimExpired = &DomainError{
        Code: "SECRET_CLAIM_EXPIRED", Message: "secret claim token has expired", HTTPStatus: http.StatusGone,
    }
)

// IsDomainError returns true if err is or wraps a *DomainError.
func IsDomainError(err error) (*DomainError, bool) {
    var de *DomainError
    if errors.As(err, &de) {
        return de, true
    }
    return nil, false
}
```

---

## `internal/platform/clock/clock.go`

```go
// Package clock provides a testable time abstraction.
// All domain code uses clock.Clock rather than time.Now() directly.
// This allows tests to control time without sleeping.
package clock

import "time"

type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    Sleep(d time.Duration)
}

// Real is the production Clock implementation using actual system time.
type Real struct{}

func (Real) Now() time.Time                  { return time.Now() }
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }
func (Real) Sleep(d time.Duration)           { time.Sleep(d) }

// Fake is a controllable Clock for testing.
type Fake struct {
    current time.Time
}

func NewFake(t time.Time) *Fake      { return &Fake{current: t} }
func (f *Fake) Now() time.Time       { return f.current }
func (f *Fake) Since(t time.Time) time.Duration { return f.current.Sub(t) }
func (f *Fake) Sleep(d time.Duration) { f.current = f.current.Add(d) }
func (f *Fake) Advance(d time.Duration) { f.current = f.current.Add(d) }
func (f *Fake) Set(t time.Time)         { f.current = t }
```

---

## `internal/platform/logger/logger.go`

```go
// Package logger sets up the application-wide structured logger.
// Uses stdlib slog — no external logging library.
package logger

import (
    "log/slog"
    "os"
)

// Setup initialises the global slog logger based on the log level string.
// Call this once at startup before any domain code runs.
func Setup(level string) *slog.Logger {
    var l slog.Level
    switch level {
    case "debug":
        l = slog.LevelDebug
    case "warn":
        l = slog.LevelWarn
    case "error":
        l = slog.LevelError
    default:
        l = slog.LevelInfo
    }

    handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level:     l,
        AddSource: l == slog.LevelDebug, // include file:line only in debug
    })

    logger := slog.New(handler)
    slog.SetDefault(logger) // set as global default
    return logger
}

// FromContext extracts a logger from context, or returns the default logger.
// Domain code should prefer log.FromContext(ctx) over slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
        return l
    }
    return slog.Default()
}

// WithContext returns a new context with the logger attached.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
    return context.WithValue(ctx, loggerKey{}, logger)
}

type loggerKey struct{}
```
