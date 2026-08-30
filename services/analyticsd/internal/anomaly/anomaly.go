// Package anomaly produces explainable findings from profile-aware rollups.
package anomaly

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/profile"
	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/rollup"
)

// Finding is a deterministic, evidence-linked report item.
type Finding struct {
	ID          string   `json:"id"`
	Level       string   `json:"level,omitempty"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	EntityIDs   []string `json:"entity_ids,omitempty"`
	EvidenceIDs []string `json:"evidence_ids"`
	Confidence  string   `json:"confidence"`
	Source      string   `json:"source"`
}

// Result is deterministic output before any language-model enrichment.
type Result struct {
	Anomalies   []Finding `json:"anomalies"`
	Risks       []Finding `json:"risks"`
	Suggestions []Finding `json:"suggestions"`
	Evidence    []string  `json:"evidence"`
	Coverage    float64   `json:"coverage"`
	Confidence  string    `json:"confidence"`
	Limitations []string  `json:"limitations"`
}

// Analyze runs safety rules even when history has not reached the baseline.
// History should contain up to the previous 14 local days.
func Analyze(today rollup.DailyRollup, history []rollup.DailyRollup) Result {
	baselineDays := 0
	for _, day := range history {
		if day.EventCount > 0 || len(day.Entities) > 0 {
			baselineDays++
		}
	}
	result := Result{Confidence: "insufficient_baseline", Coverage: coverage(today)}
	if baselineDays >= 14 {
		result.Confidence = "medium"
	} else {
		result.Limitations = append(result.Limitations, fmt.Sprintf("僅有 %d/14 天基線資料，統計判斷較保守。", baselineDays))
	}
	if len(today.DataGaps) > 0 {
		result.Limitations = append(result.Limitations, fmt.Sprintf("偵測到 %d 段資料缺口，缺口期間不推論為正常狀態。", len(today.DataGaps)))
		if result.Confidence == "medium" {
			result.Confidence = "low"
		}
	}
	for entityID, entity := range today.Entities {
		confidence := result.Confidence
		if confidence == "medium" && result.Coverage < 0.8 {
			confidence = "low"
		}
		for _, rule := range rulesFor(entity) {
			if !rule.match(entity) {
				continue
			}
			safeID := stableID(entityID)
			evidenceID := fmt.Sprintf("ev-%s-%s", safeID, rule.id)
			finding := Finding{ID: fmt.Sprintf("finding-%s-%s", safeID, rule.id), Level: rule.level, Title: rule.title, Explanation: rule.explanation(entity), EntityIDs: []string{entityID}, EvidenceIDs: []string{evidenceID}, Confidence: confidence, Source: "deterministic"}
			result.Evidence = append(result.Evidence, evidenceID)
			if rule.level == "high" {
				result.Risks = append(result.Risks, finding)
			} else {
				result.Anomalies = append(result.Anomalies, finding)
			}
		}
		batteryThreshold := entity.NumericThreshold
		if batteryThreshold <= 0 {
			batteryThreshold = 20
		}
		if entity.ProfileKind == profile.Battery && entity.Numeric != nil && entity.Numeric.Last <= batteryThreshold {
			evidenceID := "ev-" + stableID(entityID) + "-battery"
			result.Evidence = append(result.Evidence, evidenceID)
			result.Suggestions = append(result.Suggestions, Finding{ID: "suggestion-" + stableID(entityID) + "-battery", Title: "電池電量偏低", Explanation: "建議檢查或安排更換此裝置的電池。", EntityIDs: []string{entityID}, EvidenceIDs: []string{evidenceID}, Confidence: confidence, Source: "deterministic"})
		}
		if baselineDays >= 14 && entity.Numeric != nil && entity.Numeric.Count > 0 {
			if baseline := baselineAverage(history, entityID); baseline > 0 && math.Abs(entity.Numeric.Last-baseline) > math.Max(3, baseline*0.5) {
				evidenceID := fmt.Sprintf("ev-%s-baseline-drift", stableID(entityID))
				result.Evidence = append(result.Evidence, evidenceID)
				result.Anomalies = append(result.Anomalies, Finding{ID: "finding-" + stableID(entityID) + "-baseline-drift", Level: "medium", Title: "數值偏離近期基線", Explanation: fmt.Sprintf("目前最後數值 %.2f，14 天基線平均約 %.2f。", entity.Numeric.Last, baseline), EntityIDs: []string{entityID}, EvidenceIDs: []string{evidenceID}, Confidence: confidence, Source: "deterministic"})
			}
		}
	}
	return result
}

func baselineAverage(history []rollup.DailyRollup, entityID string) float64 {
	var sum float64
	var count int
	for _, day := range history {
		row, ok := day.Entities[entityID]
		if !ok || row.Numeric == nil || row.Numeric.Count == 0 {
			continue
		}
		sum += row.Numeric.Sum
		count += row.Numeric.Count
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// Cooldown suppresses repeated high-risk notifications without suppressing
// the finding in the stored daily report.
type Cooldown struct{ until map[string]time.Time }

func NewCooldown() *Cooldown { return &Cooldown{until: map[string]time.Time{}} }

func (c *Cooldown) Allow(finding Finding, now time.Time, duration time.Duration) bool {
	if c == nil {
		return true
	}
	if c.until == nil {
		c.until = map[string]time.Time{}
	}
	if next, ok := c.until[finding.ID]; ok && now.Before(next) {
		return false
	}
	if duration > 0 {
		c.until[finding.ID] = now.Add(duration)
	}
	return true
}

type rule struct {
	id          string
	level       string
	title       string
	match       func(rollup.EntityRollup) bool
	explanation func(rollup.EntityRollup) string
}

func rulesFor(entity rollup.EntityRollup) []rule {
	common := []rule{{id: "unavailable", level: "medium", title: "裝置長時間無法使用", match: func(row rollup.EntityRollup) bool {
		return row.UnavailableCount >= 3 || row.StateSeconds["unavailable"] >= 30*60
	}, explanation: func(row rollup.EntityRollup) string {
		return fmt.Sprintf("此裝置在報告日有 %d 次 unavailable 觀測，累計約 %d 分鐘。", row.UnavailableCount, row.StateSeconds["unavailable"]/60)
	}}}
	if entity.ProfileKind == profile.ContactSecurity || entity.ProfileKind == profile.Binary {
		common = append(common, rule{id: "rapid-cycling", level: "low", title: "狀態切換頻繁", match: func(row rollup.EntityRollup) bool { return row.Changes >= 20 }, explanation: func(row rollup.EntityRollup) string {
			return fmt.Sprintf("報告日共記錄 %d 次狀態切換，值得確認是否為預期行為。", row.Changes)
		}})
	}
	if entity.ProfileKind == profile.ContactSecurity {
		common = append(common, rule{id: "security-state", level: "high", title: "安全相關狀態需要留意", match: func(row rollup.EntityRollup) bool {
			return strings.Contains(row.DeviceClass, "smoke") || strings.Contains(row.DeviceClass, "gas") || strings.Contains(row.DeviceClass, "moisture") || row.StateCounts["on"] > 0 || row.StateCounts["open"] > 0
		}, explanation: func(rollup.EntityRollup) string {
			return "安全相關 entity 曾出現需要人工確認的狀態；系統不會自動執行任何動作。"
		}})
	}
	if entity.ProfileKind == profile.Climate {
		threshold := entity.NumericThreshold
		if threshold <= 0 {
			threshold = 3
		}
		common = append(common, rule{id: "temperature-jump", level: "medium", title: "環境數值變化較大", match: func(row rollup.EntityRollup) bool { return row.Numeric != nil && row.Numeric.MaxDelta >= threshold }, explanation: func(row rollup.EntityRollup) string {
			return fmt.Sprintf("數值最大變化約 %.2f，請結合實際環境確認。", row.Numeric.MaxDelta)
		}})
	}
	if entity.ProfileKind == profile.Energy && entity.NumericThreshold > 0 {
		common = append(common, rule{
			id: "energy-jump", level: "medium", title: "能源數值變化較大",
			match: func(row rollup.EntityRollup) bool {
				return row.Numeric != nil && row.Numeric.MaxDelta >= row.NumericThreshold
			},
			explanation: func(row rollup.EntityRollup) string {
				return fmt.Sprintf("能源數值最大變化約 %.2f，已達個別 entity 閾值 %.2f。", row.Numeric.MaxDelta, row.NumericThreshold)
			},
		})
	}
	return common
}

func coverage(today rollup.DailyRollup) float64 {
	if today.WindowEnd.Before(today.WindowStart) || today.WindowEnd.Equal(today.WindowStart) || today.EventCount == 0 {
		return 0
	}
	// This is an observation-coverage proxy until heartbeat intervals are joined.
	value := math.Min(1, float64(today.EventCount)/100)
	if len(today.DataGaps) > 0 {
		value *= math.Max(0, 1-float64(len(today.DataGaps))*0.1)
	}
	return value
}

func sanitize(value string) string {
	value = strings.NewReplacer(".", "-", "/", "-", " ", "-").Replace(value)
	if len(value) > 80 {
		return value[:80]
	}
	return value
}

func stableID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}
