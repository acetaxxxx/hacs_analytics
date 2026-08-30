package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/anomaly"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/rollup"
)

func TestDeterministicReportValidatesAndReferencesEvidence(t *testing.T) {
	today := rollup.DailyRollup{SchemaVersion: 1, ReportDate: "2026-08-28", WindowStart: time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC), WindowEnd: time.Date(2026, 8, 28, 16, 0, 0, 0, time.UTC), Timezone: "Asia/Taipei", Entities: map[string]rollup.EntityRollup{}, EventCount: 1}
	findings := anomaly.Result{Confidence: "insufficient_baseline", Coverage: 1, Anomalies: []anomaly.Finding{}, Risks: []anomaly.Finding{}, Suggestions: []anomaly.Finding{}, Evidence: []string{}}
	value := BuildDeterministic(today, findings, "gemini-2.5-flash")
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(payload); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsTrailingJSON(t *testing.T) {
	today := rollup.DailyRollup{SchemaVersion: 1, ReportDate: "2026-08-28", WindowStart: time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC), WindowEnd: time.Date(2026, 8, 28, 16, 0, 0, 0, time.UTC), Timezone: "Asia/Taipei", Entities: map[string]rollup.EntityRollup{}, EventCount: 1}
	value := BuildDeterministic(today, anomaly.Result{Confidence: "insufficient_baseline", Coverage: 1, Anomalies: []anomaly.Finding{}, Risks: []anomaly.Finding{}, Suggestions: []anomaly.Finding{}, Evidence: []string{}}, "gemini-2.5-flash")
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(append(payload, []byte(`{"unexpected":true}`)...)); err == nil {
		t.Fatal("Validate() accepted trailing JSON")
	}
}

func TestAIReportMustPreserveDeterministicFinding(t *testing.T) {
	finding := anomaly.Finding{
		ID: "finding-1", Title: "原始風險", Explanation: "請人工確認。", EvidenceIDs: []string{"ev-1"}, Confidence: "low", Source: "deterministic",
	}
	baseline := Report{Anomalies: []Finding{finding}}
	missing := Report{Anomalies: []Finding{}}
	if err := ValidatePreservesDeterministicFindings(missing, baseline); err == nil {
		t.Fatal("AI report without deterministic finding was accepted")
	}
	kept := Report{Anomalies: []Finding{finding}}
	if err := ValidatePreservesDeterministicFindings(kept, baseline); err != nil {
		t.Fatalf("preserved finding was rejected: %v", err)
	}
}
