// Package db establishes SQLite database connections, applies sequential schema migrations,
// and implements repository adapters for document and authentication entities.
//
// Key architectural principle: Persistence interfaces (document.Repository, auth.Repo) are declared
// exclusively by the consuming domain packages. Package db simply satisfies those abstractions.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver registration with database/sql
)

// Open initializes a thread-safe connection pool (*sql.DB) for SQLite.
// A single pool should be instantiated during startup and shared across the application lifespan.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	pool.SetMaxOpenConns(25)                  // Cap concurrent active connections
	pool.SetMaxIdleConns(25)                  // Retain idle pool to avoid repeated connection handshakes
	pool.SetConnMaxLifetime(30 * time.Minute) // Periodically refresh connections to prevent stale handles

	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ping); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Migrate executes all *.sql scripts discovered in the target directory in lexicographical order
// (001_init.sql, 002_auth.sql, etc.).
//
// Schema definitions utilize idempotent DDL constructs ("CREATE TABLE IF NOT EXISTS"),
// ensuring repeated executions produce predictable results.
func Migrate(ctx context.Context, pool *sql.DB, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("db: failed to glob migration scripts: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("db: no migration scripts discovered in %q", dir)
	}
	sort.Strings(files) // Numerical prefix naming guarantees correct chronological sequencing

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("db: failed reading migration %s: %w", filepath.Base(f), err)
		}
		if _, err := pool.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("db: execution failure in migration %s: %w", filepath.Base(f), err)
		}
	}
	return nil
}
