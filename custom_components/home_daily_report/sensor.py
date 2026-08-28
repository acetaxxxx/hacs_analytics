"""Sensors for Home Daily Report."""

from __future__ import annotations

from typing import Any

from homeassistant.components.sensor import SensorEntity, SensorStateClass
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import DOMAIN, NAME
from .manager import HomeDailyReportManager


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    """Set up sensors from a config entry."""
    manager: HomeDailyReportManager = hass.data[DOMAIN][entry.entry_id]
    async_add_entities(
        [
            LastReportSensor(entry, manager),
            AnomalyCountSensor(entry, manager),
            TrackedEntityCountSensor(entry, manager),
            SidecarStatusSensor(entry, manager),
        ]
    )


class HomeDailyReportSensor(SensorEntity):
    """Base class for Home Daily Report sensors."""

    _attr_has_entity_name = True

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the sensor."""
        self.entry = entry
        self.manager = manager
        self._attr_device_info = {
            "identifiers": {(DOMAIN, entry.entry_id)},
            "name": NAME,
        }

    async def async_added_to_hass(self) -> None:
        """Subscribe to manager updates."""
        self.async_on_remove(
            self.manager.async_add_listener(self.async_write_ha_state)
        )


class LastReportSensor(HomeDailyReportSensor):
    """Expose the latest generated report."""

    _attr_name = "Last Report"
    _attr_icon = "mdi:file-chart-outline"

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the sensor."""
        super().__init__(entry, manager)
        self._attr_unique_id = f"{entry.entry_id}_last_report"

    @property
    def native_value(self) -> str | None:
        """Return the report date."""
        report = self.manager.last_report
        if not report:
            return None
        return report.get("report_date", report.get("date"))

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Return report attributes."""
        report = self.manager.last_report
        if not report:
            return {}
        return {
            "summary": report.get("summary"),
            "anomaly_count": len(report.get("anomalies", [])),
            "risk_count": len(report.get("risks", [])),
            "confidence": report.get("confidence"),
            "data_quality": report.get("data_quality"),
            "ai_status": report.get("ai_status", report.get("ai")),
            "status": report.get("status"),
        }


class AnomalyCountSensor(HomeDailyReportSensor):
    """Expose the anomaly count from the latest report."""

    _attr_name = "Anomaly Count"
    _attr_icon = "mdi:alert-circle-outline"
    _attr_native_unit_of_measurement = "issues"
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the sensor."""
        super().__init__(entry, manager)
        self._attr_unique_id = f"{entry.entry_id}_anomaly_count"

    @property
    def native_value(self) -> int:
        """Return anomaly count."""
        report = self.manager.last_report
        if not report:
            return 0
        return len(report.get("anomalies", [])) + len(report.get("risks", []))


class TrackedEntityCountSensor(HomeDailyReportSensor):
    """Expose the number of currently tracked entities."""

    _attr_name = "Tracked Entities"
    _attr_icon = "mdi:counter"
    _attr_native_unit_of_measurement = "entities"
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the sensor."""
        super().__init__(entry, manager)
        self._attr_unique_id = f"{entry.entry_id}_tracked_entities"

    @property
    def native_value(self) -> int:
        """Return tracked entity count."""
        return self.manager.tracked_entity_count


class SidecarStatusSensor(HomeDailyReportSensor):
    """Expose whether the external analytics sidecar is reachable."""

    _attr_name = "Sidecar Status"
    _attr_icon = "mdi:server-network"

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the sensor."""
        super().__init__(entry, manager)
        self._attr_unique_id = f"{entry.entry_id}_sidecar_status"

    @property
    def native_value(self) -> str:
        """Return healthy, degraded, or unconfigured."""
        return self.manager.sidecar_status

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Return the last safe sidecar error code."""
        return {"error": self.manager.sidecar_error}
