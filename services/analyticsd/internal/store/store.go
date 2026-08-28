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
	CurrentSchemaVersion = 3

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

// StateInterval is a persisted state span used to account for time between
// sparse state-change events, including a span that started before a report
// day.
type StateInterval struct {
	EntityID       string
	StartedAt      time.Time
	EndedAt        *time.Time
	State          string
	ProfileVersion int
}

// ReportJob is the storage-neutral status returned by the report API.
type ReportJob struct {
	ReportDate    string  `json:"report_date"`
	Status        string  `json:"status"`
	Attempt       int     `json:"attempt"`
	NextAttemptAt *string `json:"next_attempt_at"`
	ResultURL     *string `json:"result_url"`
	ErrorCode     *string `json:"error_code"`
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
	case 3:
		statement = `
			ALTER TABLE report_runs ADD COLUMN lease_expires_at_ms INTEGER;`
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
			if err := updateStateInterval(ctx, tx, event); err != nil {
				return IngestResult{}, err
			}
		} else {
			result.Duplicates++
		}
	}

	if err := tx.Commit(); err != nil {
		return IngestResult{}, fmt.Errorf("commit ingest events: %w", err)
	}

	return result, nil
}

func updateStateInterval(ctx context.Context, tx *sql.Tx, event IngestEvent) error {
	var (
		intervalID int64
		startedAt  int64
		state      string
	)
	err := tx.QueryRowContext(ctx, `SELECT interval_id, started_at_ms, state FROM state_intervals WHERE entity_id = ? AND ended_at_ms IS NULL ORDER BY started_at_ms DESC LIMIT 1`, event.EntityID).Scan(&intervalID, &startedAt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		_, insertErr := tx.ExecContext(ctx, `INSERT OR IGNORE INTO state_intervals(entity_id, started_at_ms, state, profile_version) VALUES (?, ?, ?, ?)`, event.EntityID, event.ObservedAt.UnixMilli(), event.NewState, maxProfileVersion(event.ProfileVersion))
		if insertErr != nil {
			return fmt.Errorf("insert state interval: %w", insertErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("query state interval: %w", err)
	}
	observedAt := event.ObservedAt.UnixMilli()
	if observedAt <= startedAt {
		return nil
	}
	if event.Kind != "state_change" || state == event.NewState {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE state_intervals SET ended_at_ms = ? WHERE interval_id = ? AND ended_at_ms IS NULL`, observedAt, intervalID); err != nil {
		return fmt.Errorf("close state interval: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO state_intervals(entity_id, started_at_ms, state, profile_version) VALUES (?, ?, ?, ?)`, event.EntityID, observedAt, event.NewState, maxProfileVersion(event.ProfileVersion)); err != nil {
		return fmt.Errorf("insert changed state interval: %w", err)
	}
	return nil
}

func maxProfileVersion(value int) int {
	if value < 1 {
		return 1
	}
	return value
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
	// A zero-length initial/recovery healthy interval already owns its start
	// timestamp. Keep the interval key unique while preserving the gap boundary
	// to millisecond precision; the one millisecond adjustment is immaterial to
	// report durations and avoids a UNIQUE constraint collision.
	gapStart := lastObserved
	if gapStart <= prevStart {
		gapStart = prevStart + 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO heartbeat_intervals (source_instance, started_at_ms, ended_at_ms, status)
		VALUES (?, ?, ?, 'data_gap')
	`, heartbeat.SourceInstance, gapStart, observedAtMs); err != nil {
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

// QueryEvents returns normalized events in the half-open UTC interval [start,
// end). Results are ordered for deterministic rollup and report generation.
func (s *Store) QueryEvents(ctx context.Context, start, end time.Time) ([]IngestEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, observed_at_ms, entity_id, kind, old_state,
		       new_state, numeric_value, unit, metadata_json, profile_version
		FROM events
		WHERE observed_at_ms >= ? AND observed_at_ms < ?
		ORDER BY observed_at_ms ASC, event_id ASC`, start.UTC().UnixMilli(), end.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []IngestEvent
	for rows.Next() {
		var (
			event      IngestEvent
			observedMs int64
			oldState   sql.NullString
			numeric    sql.NullFloat64
			unit       sql.NullString
			metadata   string
		)
		if err := rows.Scan(&event.EventID, &observedMs, &event.EntityID, &event.Kind, &oldState, &event.NewState, &numeric, &unit, &metadata, &event.ProfileVersion); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.ObservedAt = time.UnixMilli(observedMs).UTC()
		if oldState.Valid {
			event.OldState = &oldState.String
		}
		if numeric.Valid {
			n := numeric.Float64
			event.NumericValue = &n
		}
		if unit.Valid {
			event.Unit = &unit.String
		}
		if metadata != "" && metadata != "{}" {
			if err := json.Unmarshal([]byte(metadata), &event.Metadata); err != nil {
				return nil, fmt.Errorf("decode event metadata: %w", err)
			}
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// QueryStateIntervals returns intervals overlapping the half-open UTC window
// [start, end). The result is bounded for predictable report memory use.
func (s *Store) QueryStateIntervals(ctx context.Context, start, end time.Time) ([]StateInterval, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT entity_id, started_at_ms, ended_at_ms, state, profile_version
		FROM state_intervals
		WHERE started_at_ms < ? AND (ended_at_ms IS NULL OR ended_at_ms > ?)
		ORDER BY started_at_ms ASC, interval_id ASC
		LIMIT 50000`, end.UTC().UnixMilli(), start.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query state intervals: %w", err)
	}
	defer rows.Close()

	intervals := make([]StateInterval, 0)
	for rows.Next() {
		var (
			interval  StateInterval
			startedMs int64
			endedMs   sql.NullInt64
		)
		if err := rows.Scan(&interval.EntityID, &startedMs, &endedMs, &interval.State, &interval.ProfileVersion); err != nil {
			return nil, fmt.Errorf("scan state interval: %w", err)
		}
		interval.StartedAt = time.UnixMilli(startedMs).UTC()
		if endedMs.Valid {
			ended := time.UnixMilli(endedMs.Int64).UTC()
			interval.EndedAt = &ended
		}
		intervals = append(intervals, interval)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate state intervals: %w", err)
	}
	return intervals, nil
}

// QueryHeartbeatGaps returns safe, bounded gap labels that can lower report
// confidence without exposing source credentials or payloads.
func (s *Store) QueryHeartbeatGaps(ctx context.Context, start, end time.Time) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT source_instance, started_at_ms, ended_at_ms FROM heartbeat_intervals WHERE status='data_gap' AND started_at_ms < ? AND (ended_at_ms IS NULL OR ended_at_ms > ?) ORDER BY started_at_ms ASC LIMIT 128`, end.UTC().UnixMilli(), start.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query heartbeat gaps: %w", err)
	}
	defer rows.Close()
	var gaps []string
	for rows.Next() {
		var source string
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&source, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan heartbeat gap: %w", err)
		}
		gaps = append(gaps, fmt.Sprintf("%s:%s", source, time.UnixMilli(started).UTC().Format(time.RFC3339)))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate heartbeat gaps: %w", err)
	}
	return gaps, nil
}

// SaveDailyRollup stores a complete versioned rollup atomically.
func (s *Store) SaveDailyRollup(ctx context.Context, reportDate string, entityID string, profileVersion int, rollup any) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	if reportDate == "" || entityID == "" || profileVersion < 1 {
		return errors.New("report date, entity id, and profile version are required")
	}
	payload, err := json.Marshal(rollup)
	if err != nil {
		return fmt.Errorf("marshal daily rollup: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO daily_rollups(report_date, entity_id, profile_version, rollup_json, computed_at_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(report_date, entity_id, profile_version) DO UPDATE SET
		  rollup_json=excluded.rollup_json, computed_at_ms=excluded.computed_at_ms`,
		reportDate, entityID, profileVersion, string(payload), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("save daily rollup: %w", err)
	}
	return nil
}

// PurgeRetention removes only bounded raw data and old derived records. The
// operation is intentionally explicit so it can run on a low-write schedule.
func (s *Store) PurgeRetention(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	rawBefore := now.UTC().Add(-30 * 24 * time.Hour).UnixMilli()
	rollupBefore := now.UTC().AddDate(-2, 0, 0).Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retention: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		query string
		arg   any
	}{
		{`DELETE FROM events WHERE observed_at_ms < ?`, rawBefore},
		{`DELETE FROM state_intervals WHERE ended_at_ms IS NOT NULL AND ended_at_ms < ?`, rawBefore},
		{`DELETE FROM heartbeat_intervals WHERE ended_at_ms IS NOT NULL AND ended_at_ms < ?`, rawBefore},
		{`DELETE FROM daily_rollups WHERE report_date < ?`, rollupBefore},
		// llm_attempts references report_runs, so remove child provenance first.
		{`DELETE FROM llm_attempts WHERE report_date < ?`, rollupBefore},
		{`DELETE FROM report_runs WHERE report_date < ?`, rollupBefore},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.arg); err != nil {
			return fmt.Errorf("retention query: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit retention: %w", err)
	}
	return nil
}

// RequestReport creates or returns the idempotent report job for a local date.
func (s *Store) RequestReport(ctx context.Context, reportDate, configSnapshot string) (ReportJob, error) {
	if s == nil || s.db == nil {
		return ReportJob{}, errors.New("database is not open")
	}
	if _, err := time.Parse("2006-01-02", reportDate); err != nil {
		return ReportJob{}, errors.New("report date must use YYYY-MM-DD")
	}
	if configSnapshot == "" {
		configSnapshot = "{}"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO report_runs(report_date, requested_at_ms, status, attempt, config_snapshot_json)
		VALUES (?, ?, 'requested', 0, ?)
		ON CONFLICT(report_date) DO NOTHING`, reportDate, time.Now().UnixMilli(), configSnapshot)
	if err != nil {
		return ReportJob{}, fmt.Errorf("request report: %w", err)
	}
	return s.GetReportJob(ctx, reportDate)
}

// GetReportJob returns one report job without exposing secret configuration.
func (s *Store) GetReportJob(ctx context.Context, reportDate string) (ReportJob, error) {
	if s == nil || s.db == nil {
		return ReportJob{}, errors.New("database is not open")
	}
	var job ReportJob
	var nextAttemptMs sql.NullInt64
	var errorCode, reportJSON sql.NullString
	var reportSchema sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT report_date, status, attempt, next_attempt_at_ms, error_code, report_json, report_schema_version
		FROM report_runs WHERE report_date = ?`, reportDate).Scan(&job.ReportDate, &job.Status, &job.Attempt, &nextAttemptMs, &errorCode, &reportJSON, &reportSchema)
	if errors.Is(err, sql.ErrNoRows) {
		return ReportJob{}, fmt.Errorf("report %q not found", reportDate)
	}
	if err != nil {
		return ReportJob{}, fmt.Errorf("get report job: %w", err)
	}
	if nextAttemptMs.Valid {
		value := time.UnixMilli(nextAttemptMs.Int64).UTC().Format(time.RFC3339)
		job.NextAttemptAt = &value
	}
	if errorCode.Valid {
		job.ErrorCode = &errorCode.String
	}
	if reportJSON.Valid && reportSchema.Valid {
		result := "/api/v1/reports/" + reportDate + "/result"
		job.ResultURL = &result
	}
	return job, nil
}

// GetReportConfig returns the non-secret configuration snapshot captured when
// a report date was first requested.
func (s *Store) GetReportConfig(ctx context.Context, reportDate string) ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	var config string
	if err := s.db.QueryRowContext(ctx, `SELECT config_snapshot_json FROM report_runs WHERE report_date = ?`, reportDate).Scan(&config); err != nil {
		return nil, fmt.Errorf("get report config: %w", err)
	}
	return []byte(config), nil
}

// GetReportResult returns the stored structured report JSON.
func (s *Store) GetReportResult(ctx context.Context, reportDate string) ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	var result sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT report_json FROM report_runs WHERE report_date = ?`, reportDate).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("report %q not found", reportDate)
	}
	if err != nil {
		return nil, fmt.Errorf("get report result: %w", err)
	}
	if !result.Valid || result.String == "" {
		return nil, errors.New("report result is not ready")
	}
	return []byte(result.String), nil
}

// SaveReportResult stores a validated structured report without overwriting a
// completed result during an ordinary retry or polling request.
func (s *Store) SaveReportResult(ctx context.Context, reportDate string, payload []byte, status string) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	if len(payload) == 0 {
		return errors.New("report payload is empty")
	}
	if status == "" {
		status = "completed"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE report_runs SET status = ?, completed_at_ms = ?, report_json = ?, report_schema_version = 1, lease_expires_at_ms = NULL
		WHERE report_date = ? AND status <> 'completed'`, status, time.Now().UnixMilli(), string(payload), reportDate)
	if err != nil {
		return fmt.Errorf("save report result: %w", err)
	}
	return nil
}

// PendingReportDates returns report jobs that still need deterministic/AI
// processing. The result is bounded so a damaged database cannot create an
// unbounded worker loop.
func (s *Store) PendingReportDates(ctx context.Context, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database is not open")
	}
	if limit <= 0 || limit > 32 {
		limit = 32
	}
	rows, err := s.db.QueryContext(ctx, `SELECT report_date FROM report_runs WHERE status IN ('requested', 'ai_pending') OR (status = 'ai_retry_scheduled' AND (next_attempt_at_ms IS NULL OR next_attempt_at_ms <= ?)) ORDER BY requested_at_ms ASC LIMIT ?`, time.Now().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("query pending reports: %w", err)
	}
	defer rows.Close()
	var dates []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan pending report: %w", err)
		}
		dates = append(dates, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending reports: %w", err)
	}
	return dates, nil
}

// ScheduleReportRetry records a bounded retry time and safe error code.
func (s *Store) ScheduleReportRetry(ctx context.Context, reportDate, errorCode string, next time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE report_runs SET status='ai_retry_scheduled', next_attempt_at_ms=?, error_code=?, lease_expires_at_ms=NULL WHERE report_date=? AND status <> 'completed'`, next.UTC().UnixMilli(), errorCode, reportDate)
	if err != nil {
		return fmt.Errorf("schedule report retry: %w", err)
	}
	return nil
}

// ClaimReport marks a pending job as collecting. A conditional update makes
// report requests idempotent if a worker restart races with another worker.
func (s *Store) ClaimReport(ctx context.Context, reportDate string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("database is not open")
	}
	now := time.Now()
	result, err := s.db.ExecContext(ctx, `UPDATE report_runs SET status='collecting', started_at_ms=?, attempt=attempt+1, lease_expires_at_ms=? WHERE report_date=? AND status IN ('requested','ai_pending','ai_retry_scheduled')`, now.UnixMilli(), now.Add(10*time.Minute).UnixMilli(), reportDate)
	if err != nil {
		return false, fmt.Errorf("claim report: %w", err)
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// SaveDeterministicReport persists the rule-engine result before Gemini is
// called, so a provider failure never loses explainable local evidence.
func (s *Store) SaveDeterministicReport(ctx context.Context, reportDate string, payload []byte) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	if len(payload) == 0 {
		return errors.New("deterministic report payload is empty")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE report_runs
		SET deterministic_json = ?, status = 'ai_pending'
		WHERE report_date = ? AND status <> 'completed'`, string(payload), reportDate)
	if err != nil {
		return fmt.Errorf("save deterministic report: %w", err)
	}
	return nil
}

// MarkAIStarted transitions a claimed report into its provider lease.
func (s *Store) MarkAIStarted(ctx context.Context, reportDate string) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE report_runs
		SET status = 'ai_running', lease_expires_at_ms = ?
		WHERE report_date = ? AND status = 'ai_pending'`, time.Now().Add(10*time.Minute).UnixMilli(), reportDate)
	if err != nil {
		return fmt.Errorf("mark AI started: %w", err)
	}
	return nil
}

// LLMAttempt is safe provenance metadata for one provider call.
type LLMAttempt struct {
	ReportDate string
	Attempt    int
	Provider   string
	Model      string
	RequestID  string
	StartedAt  time.Time
	EndedAt    *time.Time
	Status     string
	InputHash  string
	UsageJSON  *string
	ErrorCode  *string
}

// SaveLLMAttempt records provider provenance without storing the prompt or
// response body. InputHash is a digest of the bounded prompt.
func (s *Store) SaveLLMAttempt(ctx context.Context, attempt LLMAttempt) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	if attempt.ReportDate == "" || attempt.Attempt < 1 || attempt.Provider == "" || attempt.Model == "" || attempt.RequestID == "" {
		return errors.New("incomplete LLM attempt")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_attempts(report_date, attempt, provider, model, request_id, started_at_ms, ended_at_ms, status, input_hash, usage_json, error_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(report_date, attempt) DO UPDATE SET
		  provider=excluded.provider, model=excluded.model, request_id=excluded.request_id,
		  started_at_ms=excluded.started_at_ms, ended_at_ms=excluded.ended_at_ms,
		  status=excluded.status, input_hash=excluded.input_hash, usage_json=excluded.usage_json,
		  error_code=excluded.error_code`,
		attempt.ReportDate, attempt.Attempt, attempt.Provider, attempt.Model, attempt.RequestID,
		attempt.StartedAt.UnixMilli(), nullableTimeMillis(attempt.EndedAt), attempt.Status,
		attempt.InputHash, attempt.UsageJSON, attempt.ErrorCode)
	if err != nil {
		return fmt.Errorf("save LLM attempt: %w", err)
	}
	return nil
}

func nullableTimeMillis(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

// RecoverExpiredReports makes interrupted jobs eligible again after restart.
// Completed reports are never touched.
func (s *Store) RecoverExpiredReports(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("database is not open")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE report_runs
		SET status='ai_retry_scheduled', next_attempt_at_ms=?, error_code='worker_restarted', lease_expires_at_ms=NULL
		WHERE status IN ('collecting', 'ai_pending', 'ai_running')
		  AND lease_expires_at_ms IS NOT NULL AND lease_expires_at_ms <= ?`, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return fmt.Errorf("recover expired reports: %w", err)
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
