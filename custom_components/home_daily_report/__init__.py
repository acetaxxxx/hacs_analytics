"""Home Daily Report integration."""

from __future__ import annotations

import logging
from typing import Any

import voluptuous as vol

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant, ServiceCall
from homeassistant.helpers import config_validation as cv

from .const import (
    ATTR_DATE,
    ATTR_NOTIFY,
    ATTR_USE_AI,
    DOMAIN,
    PLATFORMS,
    SERVICE_GENERATE_REPORT,
)
from .manager import HomeDailyReportManager

_LOGGER = logging.getLogger(__name__)

GENERATE_REPORT_SCHEMA = vol.Schema(
    {
        vol.Optional(ATTR_DATE): str,
        vol.Optional(ATTR_USE_AI, default=True): cv.boolean,
        vol.Optional(ATTR_NOTIFY, default=True): cv.boolean,
    }
)


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    """Set up Home Daily Report from a config entry."""
    hass.data.setdefault(DOMAIN, {})

    manager = HomeDailyReportManager(hass, entry)
    await manager.async_load()
    await manager.async_start()

    hass.data[DOMAIN][entry.entry_id] = manager
    entry.runtime_data = manager

    await _async_register_services(hass)
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)

    entry.async_on_unload(entry.add_update_listener(_async_update_listener))
    return True


async def async_unload_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    """Unload a config entry."""
    unload_ok = await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
    manager: HomeDailyReportManager | None = hass.data[DOMAIN].pop(
        entry.entry_id, None
    )

    if manager is not None:
        await manager.async_unload()

    if not hass.data[DOMAIN]:
        hass.data.pop(DOMAIN)

    return unload_ok


async def _async_update_listener(hass: HomeAssistant, entry: ConfigEntry) -> None:
    """Reload the entry when options change."""
    await hass.config_entries.async_reload(entry.entry_id)


async def _async_register_services(hass: HomeAssistant) -> None:
    """Register integration services once."""
    marker = "_services_registered"
    if hass.data[DOMAIN].get(marker):
        return

    async def _handle_generate_report(call: ServiceCall) -> None:
        manager = _get_manager(hass)
        report_date = call.data.get(ATTR_DATE)
        use_ai = call.data[ATTR_USE_AI]
        notify = call.data[ATTR_NOTIFY]

        await manager.async_generate_report(
            report_date=report_date,
            use_ai=use_ai,
            notify=notify,
        )

    hass.services.async_register(
        DOMAIN,
        SERVICE_GENERATE_REPORT,
        _handle_generate_report,
        schema=GENERATE_REPORT_SCHEMA,
    )
    hass.data[DOMAIN][marker] = True


def _get_manager(hass: HomeAssistant) -> HomeDailyReportManager:
    """Return the configured manager."""
    for key, value in hass.data.get(DOMAIN, {}).items():
        if key.startswith("_"):
            continue
        return value
    raise RuntimeError("Home Daily Report is not configured")
