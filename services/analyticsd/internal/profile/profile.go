// Package profile contains the deterministic entity profile rules.
package profile

import (
	"strings"
	"time"
)

// Kind identifies the interpretation that is safe for an entity.
type Kind string

const (
	Numeric         Kind = "numeric"
	Binary          Kind = "binary"
	Battery         Kind = "battery"
	Energy          Kind = "energy"
	Climate         Kind = "climate"
	ContactSecurity Kind = "contact_security"
	LightMedia      Kind = "light_media"
	Generic         Kind = "generic"
)

// Entity describes only metadata received at the collection boundary.
type Entity struct {
	EntityID    string
	Domain      string
	DeviceClass string
	Unit        string
}

// Override changes the defaults for one entity. A zero value means no override.
type Override struct {
	Kind             Kind
	Version          int
	SnapshotInterval time.Duration
	NumericThreshold float64
	AllowedStates    []string
	SafeMetadata     []string
}

// Profile is the resolved, immutable profile used by aggregation.
type Profile struct {
	Kind             Kind
	Version          int
	SnapshotInterval time.Duration
	NumericThreshold float64
	AllowedStates    []string
	SafeMetadata     []string
	Unit             string
}

// Resolve applies exclusion before an explicit entity override, then chooses a
// domain/device-class profile. The returned generic profile never invents
// numeric meaning for an unsupported entity.
func Resolve(entity Entity, excluded bool, override *Override) (Profile, bool) {
	if excluded {
		return Profile{}, false
	}
	if override != nil && override.Kind != "" {
		return fromOverride(entity, *override), true
	}

	domain := strings.ToLower(entity.Domain)
	deviceClass := strings.ToLower(entity.DeviceClass)
	kind := Generic
	switch {
	case domain == "sensor" && (deviceClass == "battery" || strings.Contains(strings.ToLower(entity.EntityID), "battery")) && compatibleUnit("battery", entity.Unit):
		kind = Battery
	case domain == "sensor" && (deviceClass == "energy" || deviceClass == "power") && compatibleUnit(deviceClass, entity.Unit):
		kind = Energy
	case domain == "sensor" && (deviceClass == "temperature" || deviceClass == "humidity") && compatibleUnit(deviceClass, entity.Unit):
		kind = Climate
	case domain == "sensor" && (deviceClass == "battery" || deviceClass == "energy" || deviceClass == "power" || deviceClass == "temperature" || deviceClass == "humidity" || (deviceClass == "" && strings.Contains(strings.ToLower(entity.EntityID), "battery"))):
		// A known semantic class with an incompatible unit is safer as generic
		// than silently treating the value as an unrelated numeric metric.
		kind = Generic
	case domain == "binary_sensor" && (deviceClass == "door" || deviceClass == "window" || deviceClass == "lock" || deviceClass == "safety" || deviceClass == "smoke" || deviceClass == "gas" || deviceClass == "moisture"):
		kind = ContactSecurity
	case domain == "binary_sensor":
		kind = Binary
	case domain == "light" || domain == "media_player":
		kind = LightMedia
	case domain == "sensor" && entity.Unit != "":
		kind = Numeric
	}

	return Profile{
		Kind:             kind,
		Version:          1,
		SnapshotInterval: snapshotInterval(kind),
		NumericThreshold: defaultThreshold(kind),
		Unit:             entity.Unit,
	}, true
}

func compatibleUnit(deviceClass, unit string) bool {
	unit = strings.ToLower(strings.TrimSpace(unit))
	if unit == "" {
		return true
	}
	switch strings.ToLower(deviceClass) {
	case "battery":
		return unit == "%"
	case "temperature":
		return unit == "°c" || unit == "°f" || unit == "k" || unit == "c" || unit == "f"
	case "humidity":
		return unit == "%"
	case "energy":
		return unit == "wh" || unit == "kwh" || unit == "mwh"
	case "power":
		return unit == "w" || unit == "kw" || unit == "mw"
	default:
		return true
	}
}

func fromOverride(entity Entity, override Override) Profile {
	version := override.Version
	if version < 1 {
		version = 1
	}
	interval := override.SnapshotInterval
	if interval <= 0 && (override.Kind == Numeric || override.Kind == Battery || override.Kind == Energy || override.Kind == Climate) {
		interval = 5 * time.Minute
	}
	threshold := override.NumericThreshold
	if threshold <= 0 {
		threshold = defaultThreshold(override.Kind)
	}
	return Profile{Kind: override.Kind, Version: version, SnapshotInterval: interval, NumericThreshold: threshold, AllowedStates: override.AllowedStates, SafeMetadata: override.SafeMetadata, Unit: entity.Unit}
}

func snapshotInterval(kind Kind) time.Duration {
	switch kind {
	case Numeric, Battery, Energy, Climate:
		return 5 * time.Minute
	default:
		return 0
	}
}

func defaultThreshold(kind Kind) float64 {
	switch kind {
	case Battery:
		return 20
	case Climate:
		return 3
	case Energy:
		return 5
	default:
		return 0
	}
}
