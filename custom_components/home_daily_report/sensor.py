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
        return report.get("date")

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Return report attributes."""
        report = self.manager.last_report
        if not report:
            return {}
        return {
            "summary": report.get("summary"),
            "top_changers": report.get("top_changers"),
            "availability": report.get("availability"),
            "numeric_highlights": report.get("numeric_highlights"),
            "anomalies": report.get("anomalies"),
            "trends": report.get("trends"),
            "trend_deltas": report.get("trend_deltas"),
            "ai": report.get("ai"),
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
        return self.manager.anomaly_count


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
