// Package db (migrate.go) wraps golang-migrate to apply forward-only schema
// migrations. Migrations are run as an explicit operator action (via the
// `migrate` CLI or this helper); the server does not auto-migrate on startup.
package db

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	// Driver registrations: the postgres driver backs the database side and the
	// file driver backs the on-disk migration source. Both register via init.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// RunMigrations applies all pending up migrations from sourcePath (a local
// directory path) against dbURL. A nil error means the database is at the
// latest version, including the case where no migrations were pending.
func RunMigrations(dbURL, sourcePath string) error {
	m, err := migrate.New("file://"+sourcePath, dbURL)
	if err != nil {
		return fmt.Errorf("db.RunMigrations: create migrate instance: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("db.RunMigrations: apply: %w", err)
	}
	return nil
}
