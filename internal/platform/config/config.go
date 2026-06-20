// Package config loads application configuration from environment variables.
// There are no config files in production; every setting is sourced from the
// process environment so deployments can be configured identically across
// local, container, and orchestration environments.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration required to start the control plane. Fields
// are populated by Load from the STRATUM_* environment variables.
type Config struct {
	// DBURL is the PostgreSQL connection string.
	DBURL string
	// HTTPPort is the port the HTTP server listens on.
	HTTPPort string
	// LogLevel controls slog verbosity: debug, info, warn, or error.
	LogLevel string
	// Env is the deployment environment: development or production.
	Env string
	// EncryptionKey is the hex-encoded 32-byte key used for secret encryption.
	EncryptionKey string
	// JWTSecret is the HMAC key used to sign and verify JWT session tokens.
	JWTSecret string
	// WebhookSecret is the shared secret used to validate VCS webhook
	// signatures. Phase 1 uses a single global secret; per-connection secrets
	// arrive with the connection schema extension in a later phase.
	WebhookSecret string
}

// Load reads configuration from the environment, applies defaults, and
// validates required values. It returns an error if a required variable is
// missing or a value is invalid.
func Load() (Config, error) {
	cfg := Config{
		DBURL:         os.Getenv("STRATUM_DB_URL"),
		HTTPPort:      getenvDefault("STRATUM_HTTP_PORT", "8080"),
		LogLevel:      strings.ToLower(getenvDefault("STRATUM_LOG_LEVEL", "info")),
		Env:           strings.ToLower(getenvDefault("STRATUM_ENV", "development")),
		EncryptionKey: os.Getenv("STRATUM_ENCRYPTION_KEY"),
		JWTSecret:     os.Getenv("STRATUM_JWT_SECRET"),
		WebhookSecret: getenvDefault("STRATUM_WEBHOOK_SECRET", os.Getenv("STRATUM_WORKER_HMAC_SECRET")),
	}

	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// IsProduction reports whether the process is running in production.
func (c Config) IsProduction() bool {
	return c.Env == "production"
}

func (c Config) validate() error {
	if c.DBURL == "" {
		return fmt.Errorf("config: STRATUM_DB_URL is required")
	}
	switch c.Env {
	case "development", "production":
	default:
		return fmt.Errorf("config: STRATUM_ENV must be development or production, got %q", c.Env)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: STRATUM_LOG_LEVEL must be debug, info, warn, or error, got %q", c.LogLevel)
	}
	if _, err := strconv.Atoi(c.HTTPPort); err != nil {
		return fmt.Errorf("config: STRATUM_HTTP_PORT must be numeric, got %q", c.HTTPPort)
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("config: STRATUM_JWT_SECRET is required")
	}
	if c.WebhookSecret == "" {
		return fmt.Errorf("config: STRATUM_WEBHOOK_SECRET (or STRATUM_WORKER_HMAC_SECRET) is required")
	}
	return nil
}

// getenvDefault returns the value of the named environment variable, or
// fallback when it is unset or empty.
func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
