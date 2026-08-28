// Package store owns the sidecar's SQLite connection and schema lifecycle.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// DriverName is the database/sql driver used by the sidecar.
	DriverName = "sqlite"
	// CurrentSchemaVersion is the latest schema this binary can open.
	CurrentSchemaVersion = 2

	busyTimeout = 5000
)

// IngestEvent represents one state change or snapshot observation.
type IngestEvent struct {
	EventID        string         `json:"event_id"`
	ObservedAt     time.Time      `json:"observed_at"`
	EntityID       string         `json:"entity_id"`
	Kind           string         `json:"kind"`
	OldState       *string        `json:"old_state"`
	NewState       string         `json:"new_state"`
	NumericValue   *float64       `json:"numeric_value"`
	Unit           *string        `json:"unit"`
	Metadata       map[string]any `json:"metadata"`
	ProfileVersion int            `json:"profile_version"`
}

// EventBatch represents a batch of events sent by Home Assistant.
type EventBatch struct {
	SourceInstance string        `json:"source_instance"`
	SentAt         time.Time     `json:"sent_at"`
	Events         []IngestEvent `json:"events"`
}

// IngestResult is the response returned after committing an event batch.
type IngestResult struct {
	RequestID  string `json:"request_id"`
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
	Ignored    int    `json:"ignored"`
}

// Heartbeat represents a liveness heartbeat payload from Home Assistant.
type Heartbeat struct {
	SourceInstance string    `json:"source_instance"`
	ObservedAt     time.Time `json:"observed_at"`
}

// HeartbeatResult is the response returned for a heartbeat.
type HeartbeatResult struct {
	Status      string `json:"status"`
	GapDetected bool   `json:"gap_detected"`
}

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
	case 2:
		statement = `
			CREATE TABLE IF NOT EXISTS service_config (
				key TEXT PRIMARY KEY,
				value_json TEXT NOT NULL,
				updated_at_ms INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS entity_profiles (
				entity_id TEXT PRIMARY KEY,
				profile_kind TEXT NOT NULL,
				profile_version INTEGER NOT NULL,
				config_json TEXT NOT NULL,
				updated_at_ms INTEGER NOT NULL
			);

			CREATE TABLE IF NOT EXISTS events (
				event_id TEXT PRIMARY KEY,
				observed_at_ms INTEGER NOT NULL,
				entity_id TEXT NOT NULL,
				kind TEXT NOT NULL CHECK (kind IN ('state_change', 'snapshot')),
				old_state TEXT,
				new_state TEXT NOT NULL,
				numeric_value REAL,
				unit TEXT,
				metadata_json TEXT NOT NULL,
				profile_version INTEGER NOT NULL,
				source_instance TEXT NOT NULL,
				received_at_ms INTEGER NOT NULL
			);

			CREATE INDEX IF NOT EXISTS events_entity_time ON events(entity_id, observed_at_ms);
			CREATE INDEX IF NOT EXISTS events_time ON events(observed_at_ms);

			CREATE TABLE IF NOT EXISTS state_intervals (
				interval_id INTEGER PRIMARY KEY,
				entity_id TEXT NOT NULL,
				started_at_ms INTEGER NOT NULL,
				ended_at_ms INTEGER,
				state TEXT NOT NULL,
				profile_version INTEGER NOT NULL,
				UNIQUE(entity_id, started_at_ms)
			);

			CREATE INDEX IF NOT EXISTS intervals_entity_time ON state_intervals(entity_id, started_at_ms);

			CREATE TABLE IF NOT EXISTS heartbeat_intervals (
				interval_id INTEGER PRIMARY KEY,
				source_instance TEXT NOT NULL,
				started_at_ms INTEGER NOT NULL,
				ended_at_ms INTEGER,
				status TEXT NOT NULL CHECK (status IN ('healthy', 'data_gap')),
				UNIQUE(source_instance, started_at_ms)
			);

			CREATE TABLE IF NOT EXISTS daily_rollups (
				report_date TEXT NOT NULL,
				entity_id TEXT NOT NULL,
				profile_version INTEGER NOT NULL,
				rollup_json TEXT NOT NULL,
				computed_at_ms INTEGER NOT NULL,
				PRIMARY KEY(report_date, entity_id, profile_version)
			);

			CREATE TABLE IF NOT EXISTS report_runs (
				report_date TEXT PRIMARY KEY,
				requested_at_ms INTEGER NOT NULL,
				started_at_ms INTEGER,
				completed_at_ms INTEGER,
				status TEXT NOT NULL,
				attempt INTEGER NOT NULL DEFAULT 0,
				next_attempt_at_ms INTEGER,
				error_code TEXT,
				deterministic_json TEXT,
				report_json TEXT,
				report_schema_version INTEGER,
				config_snapshot_json TEXT NOT NULL
			);

			CREATE TABLE IF NOT EXISTS llm_attempts (
				attempt_id INTEGER PRIMARY KEY,
				report_date TEXT NOT NULL REFERENCES report_runs(report_date),
				attempt INTEGER NOT NULL,
				provider TEXT NOT NULL,
				model TEXT NOT NULL,
				request_id TEXT NOT NULL,
				started_at_ms INTEGER NOT NULL,
				ended_at_ms INTEGER,
				status TEXT NOT NULL,
				input_hash TEXT,
				usage_json TEXT,
				error_code TEXT,
				UNIQUE(report_date, attempt)
			);`
	default:
		return fmt.Errorf("unknown schema migration %d", version)
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	return nil
}

// IngestEvents validates uniqueness transactionally and persists raw events.
func (s *Store) IngestEvents(ctx context.Context, requestID string, batch EventBatch) (IngestResult, error) {
	if s == nil || s.db == nil {
		return IngestResult{}, errors.New("database is not open")
	}

	result := IngestResult{
		RequestID: requestID,
	}
	if len(batch.Events) == 0 {
		return result, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestResult{}, fmt.Errorf("begin ingest tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (
			event_id,
			observed_at_ms,
			entity_id,
			kind,
			old_state,
			new_state,
			numeric_value,
			unit,
			metadata_json,
			profile_version,
			source_instance,
			received_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO NOTHING
	`)
	if err != nil {
		return IngestResult{}, fmt.Errorf("prepare insert event: %w", err)
	}
	defer stmt.Close()

	nowMs := time.Now().UnixMilli()

	for _, event := range batch.Events {
		profileVersion := event.ProfileVersion
		if profileVersion <= 0 {
			profileVersion = 1
		}
		var metaJSON []byte
		if event.Metadata != nil {
			metaJSON, err = json.Marshal(event.Metadata)
			if err != nil {
				metaJSON = []byte("{}")
			}
		} else {
			metaJSON = []byte("{}")
		}

		res, err := stmt.ExecContext(
			ctx,
			event.EventID,
			event.ObservedAt.UnixMilli(),
			event.EntityID,
			event.Kind,
			event.OldState,
			event.NewState,
			event.NumericValue,
			event.Unit,
			string(metaJSON),
			profileVersion,
			batch.SourceInstance,
			nowMs,
		)
		if err != nil {
			return IngestResult{}, fmt.Errorf("insert event %q: %w", event.EventID, err)
		}

		rows, err := res.RowsAffected()
		if err != nil {
			return IngestResult{}, fmt.Errorf("read rows affected for %q: %w", event.EventID, err)
		}
		if rows > 0 {
			result.Accepted++
		} else {
			result.Duplicates++
		}
	}

	if err := tx.Commit(); err != nil {
		return IngestResult{}, fmt.Errorf("commit ingest events: %w", err)
	}

	return result, nil
}

// IngestHeartbeat updates heartbeat intervals and records data gaps.
func (s *Store) IngestHeartbeat(ctx context.Context, heartbeat Heartbeat, tolerance time.Duration) (HeartbeatResult, error) {
	if s == nil || s.db == nil {
		return HeartbeatResult{}, errors.New("database is not open")
	}

	if tolerance <= 0 {
		tolerance = 90 * time.Second
	}
	toleranceMs := tolerance.Milliseconds()
	observedAtMs := heartbeat.ObservedAt.UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HeartbeatResult{}, fmt.Errorf("begin heartbeat tx: %w", err)
	}
	defer tx.Rollback()

	var (
		prevID     int64
		prevStart  int64
		prevEnd    sql.NullInt64
		prevStatus string
	)

	err = tx.QueryRowContext(ctx, `
		SELECT interval_id, started_at_ms, ended_at_ms, status
		FROM heartbeat_intervals
		WHERE source_instance = ?
		ORDER BY started_at_ms DESC
		LIMIT 1
	`, heartbeat.SourceInstance).Scan(&prevID, &prevStart, &prevEnd, &prevStatus)

	if errors.Is(err, sql.ErrNoRows) {
		// First heartbeat ever recorded for this source instance
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO heartbeat_intervals (source_instance, started_at_ms, ended_at_ms, status)
			VALUES (?, ?, ?, 'healthy')
		`, heartbeat.SourceInstance, observedAtMs, observedAtMs); err != nil {
			return HeartbeatResult{}, fmt.Errorf("insert initial heartbeat: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return HeartbeatResult{}, fmt.Errorf("commit initial heartbeat: %w", err)
		}
		return HeartbeatResult{Status: "healthy", GapDetected: false}, nil
	} else if err != nil {
		return HeartbeatResult{}, fmt.Errorf("query previous heartbeat: %w", err)
	}

	lastObserved := prevStart
	if prevEnd.Valid && prevEnd.Int64 > lastObserved {
		lastObserved = prevEnd.Int64
	}

	if observedAtMs <= lastObserved {
		// Duplicate or out-of-order heartbeat; preserve existing interval
		if err := tx.Commit(); err != nil {
			return HeartbeatResult{}, fmt.Errorf("commit duplicate heartbeat: %w", err)
		}
		return HeartbeatResult{Status: prevStatus, GapDetected: prevStatus == "data_gap"}, nil
	}

	delta := observedAtMs - lastObserved
	if delta <= toleranceMs {
		if prevStatus == "healthy" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE heartbeat_intervals
				SET ended_at_ms = ?
				WHERE interval_id = ?
			`, observedAtMs, prevID); err != nil {
				return HeartbeatResult{}, fmt.Errorf("update healthy heartbeat: %w", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO heartbeat_intervals (source_instance, started_at_ms, ended_at_ms, status)
				VALUES (?, ?, ?, 'healthy')
			`, heartbeat.SourceInstance, observedAtMs, observedAtMs); err != nil {
				return HeartbeatResult{}, fmt.Errorf("insert healthy heartbeat after gap: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return HeartbeatResult{}, fmt.Errorf("commit healthy heartbeat: %w", err)
		}
		return HeartbeatResult{Status: "healthy", GapDetected: false}, nil
	}

	// Gap detected
	if !prevEnd.Valid || prevEnd.Int64 != lastObserved {
		if _, err := tx.ExecContext(ctx, `
			UPDATE heartbeat_intervals
			SET ended_at_ms = ?
			WHERE interval_id = ?
		`, lastObserved, prevID); err != nil {
			return HeartbeatResult{}, fmt.Errorf("close prev heartbeat before gap: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO heartbeat_intervals (source_instance, started_at_ms, ended_at_ms, status)
		VALUES (?, ?, ?, 'data_gap')
	`, heartbeat.SourceInstance, lastObserved, observedAtMs); err != nil {
		return HeartbeatResult{}, fmt.Errorf("insert data gap heartbeat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO heartbeat_intervals (source_instance, started_at_ms, ended_at_ms, status)
		VALUES (?, ?, ?, 'healthy')
	`, heartbeat.SourceInstance, observedAtMs, observedAtMs); err != nil {
		return HeartbeatResult{}, fmt.Errorf("insert healthy heartbeat following gap: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return HeartbeatResult{}, fmt.Errorf("commit data gap heartbeat: %w", err)
	}
	return HeartbeatResult{Status: "data_gap", GapDetected: true}, nil
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
