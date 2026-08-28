package rollup

import (
	"testing"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/profile"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

func TestComputeLocalDayAndSnapshotSemantics(t *testing.T) {
	location, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	old := "off"
	t0 := time.Date(2026, 8, 28, 0, 0, 0, 0, location).UTC()
	value0, value1 := 20.0, 22.5
	events := []store.IngestEvent{
		{EventID: "a", ObservedAt: t0.Add(time.Hour), EntityID: "sensor.temp", Kind: "snapshot", NewState: "20", NumericValue: &value0, ProfileVersion: 1},
		{EventID: "b", ObservedAt: t0.Add(2 * time.Hour), EntityID: "sensor.temp", Kind: "state_change", OldState: &old, NewState: "22.5", NumericValue: &value1, ProfileVersion: 1},
	}
	got := Compute(events, location, time.Date(2026, 8, 28, 12, 0, 0, 0, location), map[string]profile.Profile{"sensor.temp": {Kind: profile.Numeric, Version: 1}})
	row := got.Entities["sensor.temp"]
	if got.ReportDate != "2026-08-28" || row.Changes != 1 || row.Numeric == nil || row.Numeric.Count != 2 {
		t.Fatalf("unexpected rollup: %+v", got)
	}
}

func TestComputeDoesNotTreatGenericNumericStateAsMetric(t *testing.T) {
	value := 42.0
	got := Compute([]store.IngestEvent{{
		EventID:        "generic",
		ObservedAt:     time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		EntityID:       "sensor.opaque",
		Kind:           "snapshot",
		NewState:       "42",
		NumericValue:   &value,
		ProfileVersion: 1,
	}}, time.UTC, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), map[string]profile.Profile{
		"sensor.opaque": {Kind: profile.Generic, Version: 1},
	})
	if row := got.Entities["sensor.opaque"]; row.Numeric != nil {
		t.Fatalf("generic numeric state created a metric summary: %+v", row.Numeric)
	}
}

func TestComputeWithIntervalsCarriesSparseStateAcrossDay(t *testing.T) {
	location, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC)
	got := ComputeWithIntervals(nil, []store.StateInterval{{
		EntityID: "binary_sensor.front_door", StartedAt: started, State: "off", ProfileVersion: 1,
	}}, location, time.Date(2026, 8, 28, 12, 0, 0, 0, location), map[string]profile.Profile{
		"binary_sensor.front_door": {Kind: profile.Binary, Version: 1},
	})
	row := got.Entities["binary_sensor.front_door"]
	if row.StateSeconds["off"] != 24*60*60 {
		t.Fatalf("sparse state duration = %d, want %d", row.StateSeconds["off"], 24*60*60)
	}
}
