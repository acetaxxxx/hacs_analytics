// Package store owns the sidecar's SQLite connection and schema lifecycle.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// DriverName is the database/sql driver used by the sidecar.
	DriverName = "sqlite"
	// CurrentSchemaVersion is the latest schema this binary can open.
	CurrentSchemaVersion = 1

	busyTimeout = 5000
)

// Store is the sidecar's database handle. Only the sidecar process writes it.
type Store struct {
	db *sql.DB
}

// Open opens path, applies SQLite safety settings, and runs migrations before
// returning. A successful return means the database is ready for HTTP traffic.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}

	db, err := sql.Open(DriverName, path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer keeps migration and the small household workload
	// predictable while WAL still permits readers during short writes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) configure(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	// These pragmas are connection-local with SQLite. MaxOpenConns=1 makes
	// applying them once sufficient for this service.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout),
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure sqlite (%s): %w", pragma, err)
		}
	}
	return nil
}

// Migrate applies every schema migration that has not yet been recorded.
// Migration one intentionally contains only service metadata; analytics tables
// are introduced by later tickets.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at_ms INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	var applied int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if applied > CurrentSchemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			applied,
			CurrentSchemaVersion,
		)
	}

	for version := applied + 1; version <= CurrentSchemaVersion; version++ {
		if err := applyMigration(ctx, tx, version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at_ms) VALUES (?, ?)",
			version,
			time.Now().UnixMilli(),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func applyMigration(ctx context.Context, tx *sql.Tx, version int) error {
	var statement string
	switch version {
	case 1:
		statement = `
			CREATE TABLE IF NOT EXISTS service_metadata (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL,
				updated_at_ms INTEGER NOT NULL
			)`
	default:
		return fmt.Errorf("unknown schema migration %d", version)
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	return nil
}

// Ping verifies that the database is reachable and has a usable connection.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	return s.db.PingContext(ctx)
}

// DB exposes the handle to later storage adapters and tests. Callers must not
// change SQLite connection settings or close the returned handle directly.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
