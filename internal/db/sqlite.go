// Package db establishes SQLite database connections, applies sequential schema migrations,
// and implements repository adapters for document and authentication entities.
//
// Key architectural principle: Persistence interfaces (document.Repository, auth.Repo) are declared
// exclusively by the consuming domain packages. Package db simply satisfies those abstractions.
package db

import (
	"context"
	"database/sql"
	"errors"
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
	// SQLite allows only one writer at a time. With many pooled connections, concurrent
	// writes pile up on busy_timeout and tail latency spikes. A single connection
	// serializes all access, which is more than enough for this workload (small DB,
	// read path is cached) and removes SQLITE_BUSY entirely.
	// ponytail: single conn; add a separate read-only pool if read throughput ever matters.
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	pool.SetConnMaxLifetime(30 * time.Minute) // Periodically refresh to prevent stale handles

	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.PingContext(ping); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Migrate applies every *.sql script in dir, in lexicographical order (001_init.sql,
// 002_auth.sql, ...), exactly once. Applied filenames are recorded in a
// schema_migrations table and skipped on subsequent runs, so a script may safely
// contain non-idempotent statements (ALTER TABLE, data backfills). Each script plus
// its bookkeeping row commits in a single transaction: a failure rolls back cleanly
// and the script is retried on the next start.
func Migrate(ctx context.Context, pool *sql.DB, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("db: failed to glob migration scripts: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("db: no migration scripts discovered in %q", dir)
	}
	sort.Strings(files) // Numerical prefix naming guarantees correct chronological sequencing

	if _, err := pool.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`,
	); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	for _, f := range files {
		name := filepath.Base(f)

		var one int
		err := pool.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE name = ?`, name).Scan(&one)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: check migration %s: %w", name, err)
		}

		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("db: failed reading migration %s: %w", name, err)
		}

		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("db: begin migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: execution failure in migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().Unix(),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("db: commit migration %s: %w", name, err)
		}
	}
	return nil
}
