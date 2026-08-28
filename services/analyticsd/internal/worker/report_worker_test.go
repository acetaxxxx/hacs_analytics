package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/report"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

type fakeLLM struct {
	response []byte
	called   int
}

func (f *fakeLLM) Generate(context.Context, string) ([]byte, error) {
	f.called++
	return f.response, nil
}

func TestProcessPendingBuildsAndStoresStructuredReport(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.RequestReport(ctx, "2026-08-28", `{"model":"gemini-2.5-flash","use_ai":true}`); err != nil {
		t.Fatal(err)
	}
	response := []byte(`{
		"schema_version":1,
		"report_date":"2026-08-28",
		"window":{"start":"2026-08-28T00:00:00Z","end":"2026-08-29T00:00:00Z","timezone":"UTC"},
		"summary":"今日沒有需要特別注意的狀況。",
		"data_quality":{"coverage":0.5,"data_gaps":[],"limitations":[]},
		"anomalies":[],"risks":[],"patterns":[],"suggestions":[],
		"confidence":"insufficient_baseline","evidence":[],
		"ai_status":{"status":"completed","provider":"gemini","model":"gemini-2.5-flash","error_code":null}
	}`)
	llm := &fakeLLM{response: response}
	w := &ReportWorker{Store: st, LLM: llm, Location: time.UTC, Model: "gemini-2.5-flash"}
	if err := w.ProcessPending(ctx); err != nil {
		t.Fatalf("ProcessPending() error = %v", err)
	}
	if llm.called != 1 {
		t.Fatalf("fake Gemini calls = %d, want 1", llm.called)
	}
	payload, err := st.GetReportResult(ctx, "2026-08-28")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := report.Validate(payload); err != nil {
		t.Fatalf("stored report failed validation: %v", err)
	}
	var status string
	if err := st.DB().QueryRow("SELECT status FROM report_runs WHERE report_date = ?", "2026-08-28").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("report status = %q, want completed", status)
	}
}
