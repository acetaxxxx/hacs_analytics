# SQLite schema

SQLite belongs to the external Go service. The database is not the Home Assistant Recorder database and is not read or written by the Python integration. The service opens SQLite with WAL mode, a busy timeout, foreign keys enabled, and transactions around each ingest batch.

## Principles

- Store timestamps as UTC integer epoch milliseconds consistently; retain local timezone only in configuration/report metadata.
- Store the stable `event_id` as a unique key for idempotent ingestion.
- Keep raw data for 30 days and daily rollups/reports for 2 years.
- Keep report inputs bounded by querying rollups and selected intervals, not by dumping raw events into Gemini.
- Add `schema_version` to durable JSON payloads and use explicit migrations before opening a newer schema.

## Tables

```sql
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at_ms INTEGER NOT NULL
);

CREATE TABLE service_config (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at_ms INTEGER NOT NULL
);

CREATE TABLE entity_profiles (
  entity_id TEXT PRIMARY KEY,
  profile_kind TEXT NOT NULL,
  profile_version INTEGER NOT NULL,
  config_json TEXT NOT NULL,
  updated_at_ms INTEGER NOT NULL
);

CREATE TABLE events (
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

CREATE INDEX events_entity_time ON events(entity_id, observed_at_ms);
CREATE INDEX events_time ON events(observed_at_ms);

CREATE TABLE state_intervals (
  interval_id INTEGER PRIMARY KEY,
  entity_id TEXT NOT NULL,
  started_at_ms INTEGER NOT NULL,
  ended_at_ms INTEGER,
  state TEXT NOT NULL,
  profile_version INTEGER NOT NULL,
  UNIQUE(entity_id, started_at_ms)
);

CREATE INDEX intervals_entity_time ON state_intervals(entity_id, started_at_ms);

CREATE TABLE heartbeat_intervals (
  interval_id INTEGER PRIMARY KEY,
  source_instance TEXT NOT NULL,
  started_at_ms INTEGER NOT NULL,
  ended_at_ms INTEGER,
  status TEXT NOT NULL CHECK (status IN ('healthy', 'data_gap')),
  UNIQUE(source_instance, started_at_ms)
);

CREATE TABLE daily_rollups (
  report_date TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  profile_version INTEGER NOT NULL,
  rollup_json TEXT NOT NULL,
  computed_at_ms INTEGER NOT NULL,
  PRIMARY KEY(report_date, entity_id, profile_version)
);

CREATE TABLE report_runs (
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
  lease_expires_at_ms INTEGER,
  config_snapshot_json TEXT NOT NULL
);

CREATE TABLE llm_attempts (
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
);
```

`events.metadata_json` contains only the safe metadata allowed by the profile. It must not contain arbitrary HA attributes by default. `report_runs.report_json` is the complete structured report; the Home Assistant integration should cache only what its entity representation needs.

## Retention

A scheduled maintenance job deletes events and closed intervals older than 30 days, then vacuums only when explicitly configured because frequent vacuuming causes unnecessary I/O. Rollups, report runs, and attempts older than 2 years are pruned. Retention is based on UTC timestamps for raw records and local `report_date` for daily records.

## Concurrency and recovery

Only the Go process writes this database. Ingest and report jobs use separate contexts and short transactions. A report run is claimed with a conditional update, so a restart can resume a pending attempt without duplicate ownership. An `ai_running` run with an expired lease returns to `ai_retry_scheduled` on startup.
