// Package worker owns the low-frequency report processing loop.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/anomaly"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/gemini"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/profile"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/report"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/rollup"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

// ReportWorker processes at most a small number of jobs per tick to stay
// suitable for the old Windows host and its approximately 1 GB RAM budget.
type ReportWorker struct {
	Store    *store.Store
	LLM      report.LLMClient
	Location *time.Location
	Model    string
	Interval time.Duration
}

const (
	maxReportAttempts = 5 // initial attempt plus four scheduled retries
	llmTimeout        = 2 * time.Minute
)

func (w *ReportWorker) Run(ctx context.Context) error {
	if w == nil || w.Store == nil {
		return errors.New("report worker store is required")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if err := w.Store.RecoverExpiredReports(ctx, time.Now()); err != nil {
		return err
	}
	if err := w.ProcessPending(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.ProcessPending(ctx); err != nil {
				return err
			}
		}
	}
}

func (w *ReportWorker) ProcessPending(ctx context.Context) error {
	if w == nil || w.Store == nil {
		return errors.New("report worker store is required")
	}
	dates, err := w.Store.PendingReportDates(ctx, 4)
	if err != nil {
		return err
	}
	for _, reportDate := range dates {
		if err := w.processDate(ctx, reportDate); err != nil {
			return err
		}
	}
	return nil
}

func (w *ReportWorker) processDate(ctx context.Context, reportDate string) error {
	claimed, err := w.Store.ClaimReport(ctx, reportDate)
	if err != nil || !claimed {
		return err
	}
	job, err := w.Store.GetReportJob(ctx, reportDate)
	if err != nil {
		return err
	}
	model := w.model()
	useAI := true
	if snapshot, configErr := w.Store.GetReportConfig(ctx, reportDate); configErr == nil {
		var config struct {
			Model string `json:"model"`
			UseAI *bool  `json:"use_ai"`
		}
		if json.Unmarshal(snapshot, &config) == nil && strings.HasPrefix(config.Model, "gemini-") {
			model = config.Model
		}
		if config.UseAI != nil {
			useAI = *config.UseAI
		}
	}
	day, err := time.ParseInLocation("2006-01-02", reportDate, w.location())
	if err != nil {
		return w.saveFailure(ctx, reportDate, "invalid_report_date")
	}
	start := time.Date(day.Year(), day.Month(), day.Day()-14, 0, 0, 0, 0, w.location())
	end := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, w.location())
	events, err := w.Store.QueryEvents(ctx, start, end)
	if err != nil {
		return w.saveFailure(ctx, reportDate, "query_failed")
	}
	intervals, err := w.Store.QueryStateIntervals(ctx, start, end)
	if err != nil {
		return w.saveFailure(ctx, reportDate, "interval_query_failed")
	}
	profiles := inferProfiles(events)
	today := rollup.ComputeWithIntervals(events, intervals, w.location(), day, profiles)
	if gaps, gapErr := w.Store.QueryHeartbeatGaps(ctx, today.WindowStart, today.WindowEnd); gapErr == nil {
		today.DataGaps = gaps
	}
	for entityID, row := range today.Entities {
		if err := w.Store.SaveDailyRollup(ctx, today.ReportDate, entityID, row.ProfileVersion, row); err != nil {
			return w.saveFailure(ctx, reportDate, "rollup_save_failed")
		}
	}
	history := make([]rollup.DailyRollup, 0, 14)
	for offset := 1; offset <= 14; offset++ {
		history = append(history, rollup.ComputeWithIntervals(events, intervals, w.location(), day.AddDate(0, 0, -offset), profiles))
	}
	findings := anomaly.Analyze(today, history)
	deterministic := report.BuildDeterministic(today, findings, model)
	deterministicPayload, err := json.Marshal(deterministic)
	if err != nil {
		return w.saveFailure(ctx, reportDate, "deterministic_encode_failed")
	}
	if err := w.Store.SaveDeterministicReport(ctx, reportDate, deterministicPayload); err != nil {
		return err
	}
	if !useAI {
		deterministic.AIStatus.Status = "ai_failed"
		deterministic.AIStatus.ErrorCode = stringPointer("ai_disabled")
		payload, marshalErr := json.Marshal(deterministic)
		if marshalErr != nil {
			return marshalErr
		}
		return w.Store.SaveReportResult(ctx, reportDate, payload, "ai_failed")
	}
	if w.LLM == nil {
		return w.saveFailureReport(ctx, reportDate, deterministic, "gemini_not_configured")
	}
	prompt, err := report.Prompt(deterministic)
	if err != nil {
		return w.saveFailureReport(ctx, reportDate, deterministic, "prompt_invalid")
	}
	if err := w.Store.MarkAIStarted(ctx, reportDate); err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	requestID := fmt.Sprintf("report-%s-%d", reportDate, job.Attempt)
	llmContext, cancel := context.WithTimeout(ctx, llmTimeout)
	payload, err := w.LLM.Generate(llmContext, prompt)
	cancel()
	endedAt := time.Now().UTC()
	if err != nil {
		code := errorCode(err)
		attemptStatus := "failed"
		var providerErr *gemini.Error
		transient := errors.Is(err, context.DeadlineExceeded)
		if errors.As(err, &providerErr) {
			transient = providerErr.Code == gemini.Transient || providerErr.Code == gemini.RateLimited
		}
		if transient && job.Attempt < maxReportAttempts {
			attemptStatus = "retry_scheduled"
			if attemptErr := w.saveLLMAttempt(ctx, reportDate, job.Attempt, model, requestID, startedAt, endedAt, attemptStatus, prompt, code); attemptErr != nil {
				return attemptErr
			}
			return w.Store.ScheduleReportRetry(ctx, reportDate, code, retryAt(job.Attempt))
		}
		if attemptErr := w.saveLLMAttempt(ctx, reportDate, job.Attempt, model, requestID, startedAt, endedAt, attemptStatus, prompt, code); attemptErr != nil {
			return attemptErr
		}
		return w.saveFailureReport(ctx, reportDate, deterministic, code)
	}
	validated, err := report.Validate(payload)
	if err != nil || validated.ReportDate != reportDate || report.ValidateEvidenceReferences(validated, deterministic.Evidence) != nil || report.ValidatePreservesDeterministicFindings(validated, deterministic) != nil {
		if attemptErr := w.saveLLMAttempt(ctx, reportDate, job.Attempt, model, requestID, startedAt, endedAt, "schema_invalid", prompt, "ai_schema_invalid"); attemptErr != nil {
			return attemptErr
		}
		return w.saveFailureReport(ctx, reportDate, deterministic, "ai_schema_invalid")
	}
	validated = report.RestoreEntityIDs(validated, deterministic)
	payload, err = json.Marshal(validated)
	if err != nil {
		return w.saveFailureReport(ctx, reportDate, deterministic, "ai_schema_invalid")
	}
	if err := w.saveLLMAttempt(ctx, reportDate, job.Attempt, model, requestID, startedAt, endedAt, "completed", prompt, ""); err != nil {
		return err
	}
	return w.Store.SaveReportResult(ctx, reportDate, payload, "completed")
}

func (w *ReportWorker) saveLLMAttempt(ctx context.Context, reportDate string, attempt int, model, requestID string, startedAt, endedAt time.Time, status, prompt, code string) error {
	digest := sha256.Sum256([]byte(prompt))
	var errorCode *string
	if code != "" {
		errorCode = stringPointer(code)
	}
	return w.Store.SaveLLMAttempt(ctx, store.LLMAttempt{
		ReportDate: reportDate,
		Attempt:    attempt,
		Provider:   "gemini",
		Model:      model,
		RequestID:  requestID,
		StartedAt:  startedAt,
		EndedAt:    &endedAt,
		Status:     status,
		InputHash:  hex.EncodeToString(digest[:]),
		ErrorCode:  errorCode,
	})
}

func (w *ReportWorker) saveFailure(ctx context.Context, reportDate, code string) error {
	day, _ := time.ParseInLocation("2006-01-02", reportDate, w.location())
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, w.location())
	return w.saveFailureReport(ctx, reportDate, report.Report{SchemaVersion: 1, ReportDate: reportDate, Window: report.Window{Start: start.UTC(), End: start.AddDate(0, 0, 1).UTC(), Timezone: w.location().String()}, Summary: "本次報告尚未完成。", DataQuality: report.DataQuality{Coverage: 0, DataGaps: []string{}, Limitations: []string{"AI 報告暫時無法完成。"}}, Confidence: "low", Anomalies: []report.Finding{}, Risks: []report.Finding{}, Patterns: []report.Finding{}, Suggestions: []report.Finding{}, Evidence: []string{}, AIStatus: report.AIStatus{Status: "ai_failed", Provider: "gemini", Model: w.model(), ErrorCode: stringPointer(code)}}, code)
}

func (w *ReportWorker) saveFailureReport(ctx context.Context, reportDate string, value report.Report, code string) error {
	if value.SchemaVersion == 0 {
		value.SchemaVersion = 1
	}
	if value.AIStatus.Provider == "" {
		value.AIStatus.Provider = "gemini"
	}
	if value.AIStatus.Model == "" {
		value.AIStatus.Model = w.model()
	}
	value.AIStatus.Status = "ai_failed"
	value.AIStatus.ErrorCode = stringPointer(code)
	if value.DataQuality.DataGaps == nil {
		value.DataQuality.DataGaps = []string{}
	}
	if value.DataQuality.Limitations == nil {
		value.DataQuality.Limitations = []string{"AI 報告暫時無法完成，以上結果不應視為完整分析。"}
	}
	if value.Anomalies == nil {
		value.Anomalies = []report.Finding{}
	}
	if value.Risks == nil {
		value.Risks = []report.Finding{}
	}
	if value.Patterns == nil {
		value.Patterns = []report.Finding{}
	}
	if value.Suggestions == nil {
		value.Suggestions = []report.Finding{}
	}
	if value.Evidence == nil {
		value.Evidence = []string{}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.Store.SaveReportResult(ctx, reportDate, payload, "ai_failed")
}

func inferProfiles(events []store.IngestEvent) map[string]profile.Profile {
	profiles := make(map[string]profile.Profile)
	for _, event := range events {
		domain := "generic"
		if dot := strings.IndexByte(event.EntityID, '.'); dot > 0 {
			domain = event.EntityID[:dot]
		}
		deviceClass, _ := event.Metadata["device_class"].(string)
		overrideKind, _ := event.Metadata["profile_kind"].(string)
		unit := ""
		if event.Unit != nil {
			unit = *event.Unit
		}
		resolved, _ := profile.Resolve(profile.Entity{EntityID: event.EntityID, Domain: domain, DeviceClass: deviceClass, Unit: unit}, false, nil)
		if overrideKind != "" {
			resolved.Kind = profile.Kind(overrideKind)
			if event.ProfileVersion >= 1 {
				resolved.Version = event.ProfileVersion
			} else if overrideVersion, ok := event.Metadata["profile_version"].(float64); ok && overrideVersion >= 1 {
				resolved.Version = int(overrideVersion)
			}
		}
		if threshold, ok := event.Metadata["numeric_threshold"].(float64); ok && threshold >= 0 {
			resolved.NumericThreshold = threshold
		}
		// QueryEvents is ordered by observed time. Keep the latest profile
		// metadata so an options change recomputes the retained raw window with
		// the new interpretation while older stored rollups remain versioned.
		profiles[event.EntityID] = resolved
	}
	return profiles
}

func (w *ReportWorker) location() *time.Location {
	if w.Location != nil {
		return w.Location
	}
	return time.UTC
}
func (w *ReportWorker) model() string {
	if strings.TrimSpace(w.Model) != "" {
		return w.Model
	}
	return gemini.DefaultModel
}
func retryAt(attempt int) time.Time {
	delays := []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 24 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(delays) {
		attempt = len(delays)
	}
	return time.Now().Add(delays[attempt-1])
}
func errorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return string(gemini.Transient)
	}
	var providerErr *gemini.Error
	if errors.As(err, &providerErr) {
		return string(providerErr.Code)
	}
	if err == nil {
		return "unknown"
	}
	return fmt.Sprintf("%T", err)
}
func stringPointer(value string) *string { return &value }
