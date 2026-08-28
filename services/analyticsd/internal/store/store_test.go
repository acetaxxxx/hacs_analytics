package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrationAndSQLiteSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.db")
	ctx := context.Background()

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	assertSchemaVersion(t, store.DB(), CurrentSchemaVersion)
	assertTableExists(t, store.DB(), "service_metadata")
	assertPragma(t, store.DB(), "foreign_keys", "1")
	assertPragma(t, store.DB(), "journal_mode", "wal")

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	assertSchemaVersion(t, store.DB(), CurrentSchemaVersion)
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open(DriverName, path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at_ms INTEGER NOT NULL
		);
		INSERT INTO schema_migrations(version, applied_at_ms) VALUES (999, 0);
	`); err != nil {
		db.Close()
		t.Fatalf("seed future schema: %v", err)
	}
	db.Close()

	store, err := Open(context.Background(), path)
	if err == nil {
		store.Close()
		t.Fatal("Open() succeeded for a newer schema")
	}
}

func assertSchemaVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&got); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var got string
	if err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&got); err != nil {
		t.Fatalf("table %q missing: %v", table, err)
	}
}

func assertPragma(t *testing.T, db *sql.DB, pragma, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow("PRAGMA "+pragma).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}
