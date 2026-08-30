// Package report builds the stable, provider-neutral daily report contract.
package report

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/anomaly"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/rollup"
)

// Finding is the public report finding shape. Text is generated in Traditional
// Chinese, while keys and enum values remain stable English contract values.
type Finding = anomaly.Finding

// Report is the complete schema-v1 daily report returned to Home Assistant.
type Report struct {
	SchemaVersion int         `json:"schema_version"`
	ReportDate    string      `json:"report_date"`
	Window        Window      `json:"window"`
	Summary       string      `json:"summary"`
	DataQuality   DataQuality `json:"data_quality"`
	Anomalies     []Finding   `json:"anomalies"`
	Risks         []Finding   `json:"risks"`
	Patterns      []Finding   `json:"patterns"`
	Suggestions   []Finding   `json:"suggestions"`
	Confidence    string      `json:"confidence"`
	Evidence      []string    `json:"evidence"`
	AIStatus      AIStatus    `json:"ai_status"`
}

type Window struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Timezone string    `json:"timezone"`
}

type DataQuality struct {
	Coverage    float64  `json:"coverage"`
	DataGaps    []string `json:"data_gaps"`
	Limitations []string `json:"limitations"`
}

type AIStatus struct {
	Status    string  `json:"status"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Attempt   int     `json:"attempt,omitempty"`
	ErrorCode *string `json:"error_code"`
}

// LLMClient is intentionally tiny so report tests can use a fake response.
type LLMClient interface {
	Generate(context.Context, string) ([]byte, error)
}

// Validate decodes the schema-v1 report and applies the cross-field evidence
// checks that JSON Schema cannot express on its own.
func Validate(payload []byte) (Report, error) {
	if len(payload) == 0 || len(payload) > 256*1024 {
		return Report{}, fmt.Errorf("report payload is empty or exceeds 256 KiB")
	}
	var result Report
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Report{}, fmt.Errorf("decode report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Report{}, fmt.Errorf("report must contain exactly one JSON value")
	}
	parsedDate, dateErr := time.Parse("2006-01-02", result.ReportDate)
	if result.SchemaVersion != 1 || dateErr != nil || parsedDate.Format("2006-01-02") != result.ReportDate || result.Window.Timezone == "" || result.Window.Start.IsZero() || result.Window.End.IsZero() || !result.Window.End.After(result.Window.Start) {
		return Report{}, fmt.Errorf("invalid report identity")
	}
	if len(result.Summary) > 12000 || result.DataQuality.DataGaps == nil || result.DataQuality.Limitations == nil || result.Anomalies == nil || result.Risks == nil || result.Patterns == nil || result.Suggestions == nil || result.Evidence == nil {
		return Report{}, fmt.Errorf("report has missing or oversized required fields")
	}
	if result.Confidence != "high" && result.Confidence != "medium" && result.Confidence != "low" && result.Confidence != "insufficient_baseline" {
		return Report{}, fmt.Errorf("invalid confidence")
	}
	if result.AIStatus.Provider != "gemini" || result.AIStatus.Model == "" {
		return Report{}, fmt.Errorf("invalid ai status")
	}
	if result.AIStatus.Status != "completed" && result.AIStatus.Status != "ai_pending" && result.AIStatus.Status != "ai_failed" {
		return Report{}, fmt.Errorf("invalid ai status enum")
	}
	if result.DataQuality.Coverage < 0 || result.DataQuality.Coverage > 1 {
		return Report{}, fmt.Errorf("invalid coverage")
	}
	evidence := make(map[string]bool, len(result.Evidence))
	for _, id := range result.Evidence {
		if id == "" || len(id) > 128 {
			return Report{}, fmt.Errorf("empty evidence id")
		}
		evidence[id] = true
	}
	for _, finding := range append(append(append([]Finding{}, result.Anomalies...), result.Risks...), append(result.Patterns, result.Suggestions...)...) {
		if finding.ID == "" || len(finding.ID) > 128 || finding.Title == "" || len(finding.Title) > 1000 || finding.Explanation == "" || len(finding.Explanation) > 6000 || finding.Confidence == "" || len(finding.EvidenceIDs) == 0 {
			return Report{}, fmt.Errorf("finding %q is incomplete", finding.ID)
		}
		for _, id := range finding.EvidenceIDs {
			if id == "" || len(id) > 128 || !evidence[id] {
				return Report{}, fmt.Errorf("finding %q references unknown evidence %q", finding.ID, id)
			}
		}
		if finding.Level != "" && finding.Level != "high" && finding.Level != "medium" && finding.Level != "low" {
			return Report{}, fmt.Errorf("finding %q has invalid level", finding.ID)
		}
		if finding.Source != "" && finding.Source != "deterministic" && finding.Source != "ai_enriched" && finding.Source != "ai" {
			return Report{}, fmt.Errorf("finding %q has invalid source", finding.ID)
		}
	}
	return result, nil
}

// ValidateEvidenceReferences ensures Gemini only rephrases deterministic
// evidence; it cannot smuggle a new unsupported finding into the report.
func ValidateEvidenceReferences(value Report, allowed []string) error {
	known := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		known[id] = true
	}
	for _, id := range value.Evidence {
		if !known[id] {
			return fmt.Errorf("AI returned unknown evidence %q", id)
		}
	}
	for _, findings := range [][]Finding{value.Anomalies, value.Risks, value.Patterns, value.Suggestions} {
		for _, finding := range findings {
			for _, id := range finding.EvidenceIDs {
				if !known[id] {
					return fmt.Errorf("finding %q returned unknown evidence %q", finding.ID, id)
				}
			}
		}
	}
	return nil
}

// ValidatePreservesDeterministicFindings prevents an AI response from hiding
// a rule-engine finding while still allowing it to improve the wording.
func ValidatePreservesDeterministicFindings(value, baseline Report) error {
	sections := []struct {
		name string
		got  []Finding
		want []Finding
	}{
		{"anomalies", value.Anomalies, baseline.Anomalies},
		{"risks", value.Risks, baseline.Risks},
		{"patterns", value.Patterns, baseline.Patterns},
		{"suggestions", value.Suggestions, baseline.Suggestions},
	}
	for _, section := range sections {
		ids := make(map[string]struct{}, len(section.got))
		for _, finding := range section.got {
			ids[finding.ID] = struct{}{}
		}
		for _, finding := range section.want {
			if _, ok := ids[finding.ID]; !ok {
				return fmt.Errorf("AI removed deterministic %s finding %q", section.name, finding.ID)
			}
		}
	}
	return nil
}

// RestoreEntityIDs restores local entity IDs after validation. Gemini sees
// stable pseudonyms, but Home Assistant users need the report to point to the
// actual entity they can inspect. Only IDs present in deterministic output
// are eligible for restoration.
func RestoreEntityIDs(value, baseline Report) Report {
	pseudonyms := make(map[string]string)
	knownFindings := make(map[string][]string)
	for _, findings := range [][]Finding{baseline.Anomalies, baseline.Risks, baseline.Patterns, baseline.Suggestions} {
		for _, finding := range findings {
			knownFindings[finding.ID] = append([]string(nil), finding.EntityIDs...)
			for _, entityID := range finding.EntityIDs {
				pseudonyms[pseudonym(entityID)] = entityID
			}
		}
	}
	for _, findings := range []*[]Finding{&value.Anomalies, &value.Risks, &value.Patterns, &value.Suggestions} {
		for index := range *findings {
			finding := &(*findings)[index]
			if localIDs, ok := knownFindings[finding.ID]; ok {
				finding.EntityIDs = append([]string(nil), localIDs...)
				continue
			}
			for entityIndex, entityID := range finding.EntityIDs {
				if localID, ok := pseudonyms[entityID]; ok {
					finding.EntityIDs[entityIndex] = localID
				}
			}
		}
	}
	return value
}

// Prompt creates a bounded instruction with no raw event data or secret.
func Prompt(deterministic Report) (string, error) {
	deterministic = redactForAI(deterministic)
	deterministic.AIStatus.Status = "completed"
	encoded, err := json.Marshal(deterministic)
	if err != nil {
		return "", fmt.Errorf("marshal report prompt: %w", err)
	}
	if len(encoded) > 256*1024 {
		return "", fmt.Errorf("report prompt exceeds 256 KiB")
	}
	return strings.Join([]string{
		"你是家庭資料管家。請只根據下列 deterministic evidence 產生完整 JSON report。",
		"所有人類可讀文字使用繁體中文；JSON keys 與 enum 使用既定英文。",
		"不得新增沒有 evidence_ids 的風險，不得提出或執行 Home Assistant service/action。",
		"只回傳 JSON，不要 markdown code fence。",
		string(encoded),
	}, "\n\n"), nil
}

func redactForAI(value Report) Report {
	// Report contains slices; clone through JSON before rewriting IDs so the
	// persisted deterministic report remains useful to the local API.
	if payload, err := json.Marshal(value); err == nil {
		var cloned Report
		if json.Unmarshal(payload, &cloned) == nil {
			value = cloned
		}
	}
	for _, findings := range []*[]Finding{&value.Anomalies, &value.Risks, &value.Patterns, &value.Suggestions} {
		for index := range *findings {
			finding := &(*findings)[index]
			for entityIndex, entityID := range finding.EntityIDs {
				finding.EntityIDs[entityIndex] = pseudonym(entityID)
			}
		}
	}
	for index, evidenceID := range value.Evidence {
		value.Evidence[index] = redactEvidenceID(evidenceID)
	}
	for _, findings := range [][]Finding{value.Anomalies, value.Risks, value.Patterns, value.Suggestions} {
		for index := range findings {
			for evidenceIndex, evidenceID := range findings[index].EvidenceIDs {
				findings[index].EvidenceIDs[evidenceIndex] = redactEvidenceID(evidenceID)
			}
		}
	}
	return value
}

func pseudonym(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "entity-" + hex.EncodeToString(digest[:6])
}

func redactEvidenceID(value string) string {
	// Evidence IDs are already generated from a truncated SHA-256 digest by
	// the deterministic rule engine. Preserve them so the AI can cite them.
	return value
}

// BuildDeterministic creates a report before Gemini enrichment. It remains
// useful during cold start and when the external API is unavailable.
func BuildDeterministic(today rollup.DailyRollup, findings anomaly.Result, model string) Report {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	dataGaps := today.DataGaps
	if dataGaps == nil {
		dataGaps = []string{}
	}
	limitations := findings.Limitations
	if limitations == nil {
		limitations = []string{}
	}
	anomalies := findings.Anomalies
	if anomalies == nil {
		anomalies = []Finding{}
	}
	risks := findings.Risks
	if risks == nil {
		risks = []Finding{}
	}
	suggestions := findings.Suggestions
	if suggestions == nil {
		suggestions = []Finding{}
	}
	evidence := findings.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	return Report{
		SchemaVersion: 1,
		ReportDate:    today.ReportDate,
		Window:        Window{Start: today.WindowStart, End: today.WindowEnd, Timezone: today.Timezone},
		Summary:       fmt.Sprintf("報告日共收到 %d 筆觀測，涵蓋 %d 個 entity；請將風險與異常搭配原始 HA 狀態人工確認。", today.EventCount, len(today.Entities)),
		DataQuality:   DataQuality{Coverage: findings.Coverage, DataGaps: dataGaps, Limitations: limitations},
		Anomalies:     anomalies,
		Risks:         risks,
		Patterns:      []Finding{},
		Suggestions:   suggestions,
		Confidence:    findings.Confidence,
		Evidence:      evidence,
		AIStatus:      AIStatus{Status: "ai_pending", Provider: "gemini", Model: model, ErrorCode: nil},
	}
}
