// Package db provides the PostgreSQL connection pool and transaction helpers
// used by every bounded context's repository. All database access in the
// codebase goes through this package; contexts never import database/sql or
// pgx directly.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is the minimal interface satisfied by both *pgxpool.Pool and pgx.Tx.
// Repositories accept a DBTX so the same code path runs against either a plain
// connection or an in-flight transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Compile-time assertions that the pool and transaction types satisfy DBTX.
var (
	_ DBTX = (*pgxpool.Pool)(nil)
	_ DBTX = (pgx.Tx)(nil)
)

// DB wraps a pgx connection pool and is the entry point for all persistence.
type DB struct {
	// Pool is the underlying connection pool. Repositories hold a reference to
	// it for non-transactional queries.
	Pool *pgxpool.Pool
}

// New creates a connection pool against dbURL, pings it to verify
// connectivity, and returns a ready DB. The caller must call Close on shutdown.
func New(ctx context.Context, dbURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("db.New: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db.New: ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases all connections in the pool. It is safe to call multiple
// times.
func (d *DB) Close() {
	if d != nil && d.Pool != nil {
		d.Pool.Close()
	}
}

// InTx runs fn within a single database transaction. The transaction is
// committed if fn returns nil and rolled back otherwise. The rolled-back
// transaction error is intentionally ignored when committing succeeds.
func (d *DB) InTx(ctx context.Context, fn func(DBTX) error) error {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("db.InTx: begin: %w", err)
	}
	defer func() {
		// Rollback is a no-op after a successful commit; ignore its error.
		_ = tx.Rollback(ctx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db.InTx: commit: %w", err)
	}
	return nil
}
