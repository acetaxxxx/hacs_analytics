"""Buttons for Home Daily Report."""

from __future__ import annotations

from homeassistant.components.button import ButtonEntity
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
    """Set up buttons from a config entry."""
    manager: HomeDailyReportManager = hass.data[DOMAIN][entry.entry_id]
    async_add_entities([GenerateReportButton(entry, manager)])


class GenerateReportButton(ButtonEntity):
    """Generate a report on demand."""

    _attr_has_entity_name = True
    _attr_name = "Generate Report"
    _attr_icon = "mdi:robot-outline"

    def __init__(self, entry: ConfigEntry, manager: HomeDailyReportManager) -> None:
        """Initialize the button."""
        self.entry = entry
        self.manager = manager
        self._attr_unique_id = f"{entry.entry_id}_generate_report"
        self._attr_device_info = {
            "identifiers": {(DOMAIN, entry.entry_id)},
            "name": NAME,
        }

    async def async_press(self) -> None:
        """Generate the report."""
        await self.manager.async_generate_report(use_ai=True, notify=True)
