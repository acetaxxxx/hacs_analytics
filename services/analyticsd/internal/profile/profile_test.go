package profile

import "testing"

func TestResolveExclusionOverrideAndUnitCompatibility(t *testing.T) {
	if _, ok := Resolve(Entity{EntityID: "sensor.temperature", Domain: "sensor", DeviceClass: "temperature", Unit: "°C"}, true, nil); ok {
		t.Fatal("excluded entity was resolved")
	}
	override := Override{Kind: Energy, Version: 3, NumericThreshold: 12}
	resolved, ok := Resolve(Entity{EntityID: "sensor.temperature", Domain: "sensor", DeviceClass: "temperature", Unit: "°C"}, false, &override)
	if !ok || resolved.Kind != Energy || resolved.Version != 3 || resolved.NumericThreshold != 12 {
		t.Fatalf("override resolution = %+v, ok=%v", resolved, ok)
	}
	resolved, ok = Resolve(Entity{EntityID: "sensor.temperature", Domain: "sensor", DeviceClass: "temperature", Unit: "W"}, false, nil)
	if !ok || resolved.Kind != Generic {
		t.Fatalf("unit-incompatible resolution = %+v, ok=%v", resolved, ok)
	}
	resolved, ok = Resolve(Entity{EntityID: "sensor.battery_level", Domain: "sensor", Unit: "W"}, false, nil)
	if !ok || resolved.Kind != Generic {
		t.Fatalf("battery identity with incompatible unit = %+v, ok=%v", resolved, ok)
	}
}
