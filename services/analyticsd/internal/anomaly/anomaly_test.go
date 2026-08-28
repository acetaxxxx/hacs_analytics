package anomaly

import (
	"testing"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/profile"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/rollup"
)

func TestAnalyzeRunsSafetyRuleDuringColdStart(t *testing.T) {
	today := rollup.DailyRollup{WindowStart: time.Now().Add(-24 * time.Hour), WindowEnd: time.Now(), EventCount: 20, Entities: map[string]rollup.EntityRollup{"binary_sensor.smoke": {EntityID: "binary_sensor.smoke", ProfileKind: profile.ContactSecurity, DeviceClass: "smoke", StateCounts: map[string]int{"on": 1}, StateSeconds: map[string]int64{}}}}
	result := Analyze(today, nil)
	if result.Confidence != "insufficient_baseline" || len(result.Risks) != 1 || len(result.Evidence) != 1 {
		t.Fatalf("unexpected cold-start result: %+v", result)
	}
}

func TestCooldownSuppressesOnlyRepeatedNotification(t *testing.T) {
	cooldown := NewCooldown()
	finding := Finding{ID: "risk-1"}
	now := time.Now()
	if !cooldown.Allow(finding, now, time.Hour) {
		t.Fatal("first finding was suppressed")
	}
	if cooldown.Allow(finding, now.Add(time.Minute), time.Hour) {
		t.Fatal("repeated finding was not suppressed")
	}
	if !cooldown.Allow(finding, now.Add(2*time.Hour), time.Hour) {
		t.Fatal("expired finding remained suppressed")
	}
}

func TestAnalyzeFlagsLongUnavailableInterval(t *testing.T) {
	today := rollup.DailyRollup{
		WindowStart: time.Now().Add(-24 * time.Hour), WindowEnd: time.Now(), EventCount: 1,
		Entities: map[string]rollup.EntityRollup{
			"sensor.router": {
				EntityID: "sensor.router", ProfileKind: profile.Generic,
				StateCounts: map[string]int{}, StateSeconds: map[string]int64{"unavailable": 45 * 60},
			},
		},
	}
	result := Analyze(today, nil)
	if len(result.Anomalies) != 1 || result.Anomalies[0].ID == "" {
		t.Fatalf("long unavailable interval was not flagged: %+v", result)
	}
}
