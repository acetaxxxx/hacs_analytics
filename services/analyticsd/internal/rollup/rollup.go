// Package rollup converts retained observations into local-calendar facts.
package rollup

import (
	"sort"
	"strings"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/profile"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

// NumericSummary contains bounded numeric statistics for one entity/day.
type NumericSummary struct {
	Count    int     `json:"count"`
	Sum      float64 `json:"sum"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Last     float64 `json:"last"`
	MaxDelta float64 `json:"max_delta"`
}

// EntityRollup is deliberately JSON-friendly so it can be stored as one
// versioned value and passed to the report builder without raw event details.
type EntityRollup struct {
	EntityID         string           `json:"entity_id"`
	ProfileKind      profile.Kind     `json:"profile_kind"`
	ProfileVersion   int              `json:"profile_version"`
	NumericThreshold float64          `json:"numeric_threshold,omitempty"`
	DeviceClass      string           `json:"device_class,omitempty"`
	Unit             string           `json:"unit,omitempty"`
	Observations     int              `json:"observations"`
	Changes          int              `json:"changes"`
	UnknownCount     int              `json:"unknown_count"`
	UnavailableCount int              `json:"unavailable_count"`
	StateCounts      map[string]int   `json:"state_counts"`
	StateSeconds     map[string]int64 `json:"state_seconds"`
	Numeric          *NumericSummary  `json:"numeric,omitempty"`
	FirstSeen        time.Time        `json:"first_seen"`
	LastSeen         time.Time        `json:"last_seen"`
}

// DailyRollup is the bounded input to deterministic analysis.
type DailyRollup struct {
	SchemaVersion int                     `json:"schema_version"`
	ReportDate    string                  `json:"report_date"`
	WindowStart   time.Time               `json:"window_start"`
	WindowEnd     time.Time               `json:"window_end"`
	Timezone      string                  `json:"timezone"`
	Entities      map[string]EntityRollup `json:"entities"`
	EventCount    int                     `json:"event_count"`
	DataGaps      []string                `json:"data_gaps,omitempty"`
}

// Compute aggregates events whose observed timestamps fall in the local day.
// Events outside the day are ignored; callers can pass the retained 30-day
// query and reuse this function for recomputation.
func Compute(events []store.IngestEvent, location *time.Location, day time.Time, profiles map[string]profile.Profile) DailyRollup {
	return ComputeWithIntervals(events, nil, location, day, profiles)
}

// ComputeWithIntervals computes the same rollup while using persisted state
// intervals for accurate duration accounting when an entity did not change
// during the report day.
func ComputeWithIntervals(events []store.IngestEvent, intervals []store.StateInterval, location *time.Location, day time.Time, profiles map[string]profile.Profile) DailyRollup {
	if location == nil {
		location = time.UTC
	}
	localDay := day.In(location)
	start := time.Date(localDay.Year(), localDay.Month(), localDay.Day(), 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)
	result := DailyRollup{SchemaVersion: 1, ReportDate: start.Format("2006-01-02"), WindowStart: start.UTC(), WindowEnd: end.UTC(), Timezone: location.String(), Entities: map[string]EntityRollup{}}
	filtered := make([]store.IngestEvent, 0, len(events))
	for _, event := range events {
		if !event.ObservedAt.Before(start.UTC()) && event.ObservedAt.Before(end.UTC()) {
			filtered = append(filtered, event)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].EntityID == filtered[j].EntityID {
			return filtered[i].ObservedAt.Before(filtered[j].ObservedAt)
		}
		return filtered[i].EntityID < filtered[j].EntityID
	})
	result.EventCount = len(filtered)
	for _, event := range filtered {
		resolved := profiles[event.EntityID]
		if resolved.Kind == "" {
			resolved = profile.Profile{Kind: profile.Generic, Version: 1}
		}
		row := result.Entities[event.EntityID]
		if row.EntityID == "" {
			row = EntityRollup{EntityID: event.EntityID, ProfileKind: resolved.Kind, ProfileVersion: resolved.Version, NumericThreshold: resolved.NumericThreshold, Unit: value(event.Unit), StateCounts: map[string]int{}, StateSeconds: map[string]int64{}}
			if event.NumericValue != nil && isNumericProfile(resolved.Kind) {
				row.Numeric = &NumericSummary{Min: *event.NumericValue, Max: *event.NumericValue}
			}
		}
		if row.DeviceClass == "" && event.Metadata != nil {
			if deviceClass, ok := event.Metadata["device_class"].(string); ok {
				row.DeviceClass = deviceClass
			}
		}
		if row.FirstSeen.IsZero() || event.ObservedAt.Before(row.FirstSeen) {
			row.FirstSeen = event.ObservedAt
		}
		if event.ObservedAt.After(row.LastSeen) {
			row.LastSeen = event.ObservedAt
		}
		row.Observations++
		row.StateCounts[event.NewState]++
		switch strings.ToLower(event.NewState) {
		case "unknown":
			row.UnknownCount++
		case "unavailable":
			row.UnavailableCount++
		}
		if event.Kind == "state_change" && event.OldState != nil && *event.OldState != event.NewState {
			row.Changes++
		}
		if event.NumericValue != nil && isNumericProfile(resolved.Kind) {
			if row.Numeric == nil {
				row.Numeric = &NumericSummary{Min: *event.NumericValue, Max: *event.NumericValue}
			}
			row.Numeric.Count++
			row.Numeric.Sum += *event.NumericValue
			row.Numeric.Min = min(row.Numeric.Min, *event.NumericValue)
			row.Numeric.Max = max(row.Numeric.Max, *event.NumericValue)
			if row.Numeric.Count > 1 {
				delta := abs(*event.NumericValue - row.Numeric.Last)
				row.Numeric.MaxDelta = max(row.Numeric.MaxDelta, delta)
			}
			row.Numeric.Last = *event.NumericValue
		}
		result.Entities[event.EntityID] = row
	}
	// Durations are computed from the ordered samples and bounded by the day.
	for entityID, row := range result.Entities {
		entityEvents := make([]store.IngestEvent, 0)
		for _, event := range filtered {
			if event.EntityID == entityID {
				entityEvents = append(entityEvents, event)
			}
		}
		for i, event := range entityEvents {
			endAt := end.UTC()
			if i+1 < len(entityEvents) && entityEvents[i+1].ObservedAt.Before(endAt) {
				endAt = entityEvents[i+1].ObservedAt
			}
			seconds := int64(endAt.Sub(event.ObservedAt).Seconds())
			if seconds > 0 && seconds <= 26*60*60 {
				row.StateSeconds[event.NewState] += seconds
			}
		}
		result.Entities[entityID] = row
	}
	if intervals != nil {
		for entityID, row := range result.Entities {
			row.StateSeconds = map[string]int64{}
			result.Entities[entityID] = row
		}
		for _, interval := range intervals {
			intervalStart := interval.StartedAt
			if intervalStart.Before(start.UTC()) {
				intervalStart = start.UTC()
			}
			intervalEnd := end.UTC()
			if interval.EndedAt != nil && interval.EndedAt.Before(intervalEnd) {
				intervalEnd = *interval.EndedAt
			}
			if !intervalEnd.After(intervalStart) {
				continue
			}
			row := result.Entities[interval.EntityID]
			if row.EntityID == "" {
				resolved := profiles[interval.EntityID]
				if resolved.Kind == "" {
					resolved = profile.Profile{Kind: profile.Generic, Version: interval.ProfileVersion}
				}
				row = EntityRollup{EntityID: interval.EntityID, ProfileKind: resolved.Kind, ProfileVersion: resolved.Version, NumericThreshold: resolved.NumericThreshold, StateCounts: map[string]int{}, StateSeconds: map[string]int64{}}
			}
			row.StateSeconds[interval.State] += int64(intervalEnd.Sub(intervalStart).Seconds())
			result.Entities[interval.EntityID] = row
		}
	}
	return result
}

func isNumericProfile(kind profile.Kind) bool {
	return kind == profile.Numeric || kind == profile.Battery || kind == profile.Energy || kind == profile.Climate
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
