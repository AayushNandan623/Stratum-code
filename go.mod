module github.com/yourorg/stratum

go 1.22

require (
	// HTTP routing
	github.com/go-chi/chi/v5 v5.0.12

	// PostgreSQL driver
	github.com/jackc/pgx/v5 v5.5.5

	// UUID
	github.com/google/uuid v1.6.0

	// JWT
	github.com/golang-jwt/jwt/v5 v5.2.1

	// OPA (policy engine) — embedded in-process
	github.com/open-policy-agent/opa v0.63.0

	// NATS JetStream (Phase 6+)
	github.com/nats-io/nats.go v1.34.1

	// Docker SDK (worker executor)
	github.com/docker/docker v26.1.0+incompatible
	github.com/docker/distribution v2.8.3+incompatible
	github.com/opencontainers/image-spec v1.1.0

	// OpenTelemetry
	go.opentelemetry.io/otel v1.25.0
	go.opentelemetry.io/otel/exporters/prometheus v0.47.0
	go.opentelemetry.io/otel/metric v1.25.0
	go.opentelemetry.io/otel/sdk v1.25.0
	go.opentelemetry.io/otel/trace v1.25.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.50.0

	// Prometheus metrics exposition
	github.com/prometheus/client_golang v1.19.0

	// Structured logging
	// stdlib slog used directly — no external logging dependency

	// WebSocket
	github.com/coder/websocket v1.8.11

	// Configuration (env-var based, no viper)
	// stdlib os.Getenv used directly — no external config dependency

	// Database migrations
	github.com/golang-migrate/migrate/v4 v4.17.1

	// Crypto helpers
	// stdlib crypto/aes, crypto/cipher, crypto/rand — no external crypto dependency

	// Rate limiting
	golang.org/x/time v0.5.0
)

require (
	// Indirect dependencies (selected highlights)
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20231201235250-de7065d787b7 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/sys v0.18.0 // indirect
	golang.org/x/net v0.23.0 // indirect
)

// NOTE: Replace directives for local development forks go here.
// Example: replace github.com/yourorg/stratum => ../stratum
