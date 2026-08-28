package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
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
	assertTableExists(t, store.DB(), "events")
	assertTableExists(t, store.DB(), "heartbeat_intervals")
	assertTableExists(t, store.DB(), "state_intervals")
	assertTableExists(t, store.DB(), "daily_rollups")
	assertTableExists(t, store.DB(), "report_runs")
	assertTableExists(t, store.DB(), "llm_attempts")
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

func TestIngestEventsIdempotencyAndBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	ctx := context.Background()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	numVal := 22.5
	unit := "°C"
	oldState := "21.0"
	batch := EventBatch{
		SourceInstance: "ha-main",
		SentAt:         time.Now().UTC(),
		Events: []IngestEvent{
			{
				EventID:        "evt-001",
				ObservedAt:     time.Now().UTC(),
				EntityID:       "sensor.living_room_temperature",
				Kind:           "state_change",
				OldState:       &oldState,
				NewState:       "22.5",
				NumericValue:   &numVal,
				Unit:           &unit,
				Metadata:       map[string]any{"friendly_name": "Living Room Temp"},
				ProfileVersion: 1,
			},
			{
				EventID:        "evt-002",
				ObservedAt:     time.Now().UTC(),
				EntityID:       "binary_sensor.front_door",
				Kind:           "state_change",
				NewState:       "on",
				Metadata:       map[string]any{"device_class": "door"},
				ProfileVersion: 1,
			},
		},
	}

	// First ingestion: 2 accepted, 0 duplicate
	res1, err := st.IngestEvents(ctx, "req-1", batch)
	if err != nil {
		t.Fatalf("IngestEvents() error = %v", err)
	}
	if res1.Accepted != 2 || res1.Duplicates != 0 {
		t.Fatalf("res1 got accepted=%d, duplicates=%d; want 2, 0", res1.Accepted, res1.Duplicates)
	}

	// Re-ingest same batch: 0 accepted, 2 duplicates (idempotent)
	res2, err := st.IngestEvents(ctx, "req-2", batch)
	if err != nil {
		t.Fatalf("IngestEvents() error = %v", err)
	}
	if res2.Accepted != 0 || res2.Duplicates != 2 {
		t.Fatalf("res2 got accepted=%d, duplicates=%d; want 0, 2", res2.Accepted, res2.Duplicates)
	}

	// Ingest batch with 1 duplicate and 1 new
	batch2 := EventBatch{
		SourceInstance: "ha-main",
		SentAt:         time.Now().UTC(),
		Events: []IngestEvent{
			batch.Events[0],
			{
				EventID:        "evt-003",
				ObservedAt:     time.Now().UTC(),
				EntityID:       "light.kitchen",
				Kind:           "snapshot",
				NewState:       "off",
				ProfileVersion: 1,
			},
		},
	}
	res3, err := st.IngestEvents(ctx, "req-3", batch2)
	if err != nil {
		t.Fatalf("IngestEvents() error = %v", err)
	}
	if res3.Accepted != 1 || res3.Duplicates != 1 {
		t.Fatalf("res3 got accepted=%d, duplicates=%d; want 1, 1", res3.Accepted, res3.Duplicates)
	}
}

func TestIngestHeartbeatHealthyAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat.db")
	ctx := context.Background()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tolerance := 90 * time.Second

	// 1. First heartbeat
	res1, err := st.IngestHeartbeat(ctx, Heartbeat{
		SourceInstance: "ha-1",
		ObservedAt:     t0,
	}, tolerance)
	if err != nil {
		t.Fatalf("Heartbeat 1 error: %v", err)
	}
	if res1.Status != "healthy" || res1.GapDetected {
		t.Fatalf("Heartbeat 1 got %+v, want healthy, gap_detected=false", res1)
	}

	// 2. Next heartbeat 60s later (within 90s tolerance)
	t1 := t0.Add(60 * time.Second)
	res2, err := st.IngestHeartbeat(ctx, Heartbeat{
		SourceInstance: "ha-1",
		ObservedAt:     t1,
	}, tolerance)
	if err != nil {
		t.Fatalf("Heartbeat 2 error: %v", err)
	}
	if res2.Status != "healthy" || res2.GapDetected {
		t.Fatalf("Heartbeat 2 got %+v, want healthy, gap_detected=false", res2)
	}

	// 3. Heartbeat 10 minutes later (gap detected)
	t2 := t1.Add(10 * time.Minute)
	res3, err := st.IngestHeartbeat(ctx, Heartbeat{
		SourceInstance: "ha-1",
		ObservedAt:     t2,
	}, tolerance)
	if err != nil {
		t.Fatalf("Heartbeat 3 error: %v", err)
	}
	if res3.Status != "data_gap" || !res3.GapDetected {
		t.Fatalf("Heartbeat 3 got %+v, want data_gap, gap_detected=true", res3)
	}

	// 4. Heartbeat 60s after gap recovery (healthy continuation)
	t3 := t2.Add(60 * time.Second)
	res4, err := st.IngestHeartbeat(ctx, Heartbeat{
		SourceInstance: "ha-1",
		ObservedAt:     t3,
	}, tolerance)
	if err != nil {
		t.Fatalf("Heartbeat 4 error: %v", err)
	}
	if res4.Status != "healthy" || res4.GapDetected {
		t.Fatalf("Heartbeat 4 got %+v, want healthy, gap_detected=false", res4)
	}

	// Verify intervals count: should have 2 healthy intervals and 1 data_gap interval
	rows, err := st.DB().Query("SELECT status, started_at_ms, ended_at_ms FROM heartbeat_intervals WHERE source_instance = 'ha-1' ORDER BY started_at_ms ASC")
	if err != nil {
		t.Fatalf("query heartbeat_intervals: %v", err)
	}
	defer rows.Close()

	type intervalRow struct {
		status  string
		started int64
		ended   int64
	}
	var intervals []intervalRow
	for rows.Next() {
		var r intervalRow
		if err := rows.Scan(&r.status, &r.started, &r.ended); err != nil {
			t.Fatalf("scan interval: %v", err)
		}
		intervals = append(intervals, r)
	}
	if len(intervals) != 3 {
		t.Fatalf("intervals count = %d, want 3; %+v", len(intervals), intervals)
	}
	if intervals[0].status != "healthy" || intervals[1].status != "data_gap" || intervals[2].status != "healthy" {
		t.Fatalf("unexpected interval statuses: %+v", intervals)
	}
}

func TestIngestHeartbeatDetectsGapAfterOnlyOneHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "initial-gap.db")
	ctx := context.Background()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer st.Close()

	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	if _, err := st.IngestHeartbeat(ctx, Heartbeat{SourceInstance: "ha-1", ObservedAt: t0}, 90*time.Second); err != nil {
		t.Fatalf("initial heartbeat error: %v", err)
	}
	result, err := st.IngestHeartbeat(ctx, Heartbeat{SourceInstance: "ha-1", ObservedAt: t0.Add(10 * time.Minute)}, 90*time.Second)
	if err != nil {
		t.Fatalf("gap heartbeat error: %v", err)
	}
	if result.Status != "data_gap" || !result.GapDetected {
		t.Fatalf("gap heartbeat result = %+v, want data_gap", result)
	}

	var count int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM heartbeat_intervals WHERE source_instance = ?", "ha-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("heartbeat interval count = %d, want 3", count)
	}
}

func TestReportLifecyclePersistsDeterministicAndLLMProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.db")
	ctx := context.Background()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	job, err := st.RequestReport(ctx, "2026-08-28", `{"model":"gemini-2.5-flash","use_ai":true}`)
	if err != nil || job.Status != "requested" {
		t.Fatalf("RequestReport() = %+v, error = %v", job, err)
	}
	claimed, err := st.ClaimReport(ctx, "2026-08-28")
	if err != nil || !claimed {
		t.Fatalf("ClaimReport() claimed=%v, error=%v", claimed, err)
	}
	if err := st.SaveDeterministicReport(ctx, "2026-08-28", []byte(`{"schema_version":1}`)); err != nil {
		t.Fatalf("SaveDeterministicReport() error = %v", err)
	}
	if err := st.MarkAIStarted(ctx, "2026-08-28"); err != nil {
		t.Fatalf("MarkAIStarted() error = %v", err)
	}
	ended := time.Now().UTC()
	if err := st.SaveLLMAttempt(ctx, LLMAttempt{
		ReportDate: "2026-08-28", Attempt: 1, Provider: "gemini", Model: "gemini-2.5-flash",
		RequestID: "report-1", StartedAt: ended.Add(-time.Second), EndedAt: &ended,
		Status: "completed", InputHash: "hash",
	}); err != nil {
		t.Fatalf("SaveLLMAttempt() error = %v", err)
	}
	if err := st.SaveReportResult(ctx, "2026-08-28", []byte(`{"schema_version":1}`), "completed"); err != nil {
		t.Fatalf("SaveReportResult() error = %v", err)
	}
	var deterministic, attempts int
	if err := st.DB().QueryRow("SELECT deterministic_json IS NOT NULL FROM report_runs WHERE report_date = ?", "2026-08-28").Scan(&deterministic); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM llm_attempts WHERE report_date = ?", "2026-08-28").Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if deterministic != 1 || attempts != 1 {
		t.Fatalf("persisted deterministic=%d attempts=%d, want 1 and 1", deterministic, attempts)
	}
}

func TestRecoverExpiredReportLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	ctx := context.Background()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()
	if _, err := st.RequestReport(ctx, "2026-08-28", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimReport(ctx, "2026-08-28"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec("UPDATE report_runs SET lease_expires_at_ms = ? WHERE report_date = ?", time.Now().Add(-time.Minute).UnixMilli(), "2026-08-28"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverExpiredReports(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	job, err := st.GetReportJob(ctx, "2026-08-28")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "ai_retry_scheduled" || job.ErrorCode == nil || *job.ErrorCode != "worker_restarted" {
		t.Fatalf("recovered job = %+v", job)
	}
}

func TestPurgeRetentionRemovesReportProvenanceBeforeParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retention.db")
	ctx := context.Background()
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	oldDate := "2024-01-01"
	if _, err := st.RequestReport(ctx, oldDate, "{}"); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	if err := st.SaveLLMAttempt(ctx, LLMAttempt{
		ReportDate: oldDate, Attempt: 1, Provider: "gemini", Model: "gemini-2.5-flash",
		RequestID: "old-report", StartedAt: started, Status: "failed", InputHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeRetention(ctx, started); err != nil {
		t.Fatalf("PurgeRetention() error = %v", err)
	}
	var reports, attempts int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM report_runs WHERE report_date = ?", oldDate).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM llm_attempts WHERE report_date = ?", oldDate).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if reports != 0 || attempts != 0 {
		t.Fatalf("old report rows remain: reports=%d attempts=%d", reports, attempts)
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
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}
