"""Config flow for Home Daily Report."""

from __future__ import annotations

import json
import math
import re
from typing import Any
from urllib.parse import urlsplit

import voluptuous as vol

from homeassistant import config_entries
from homeassistant.core import callback
from homeassistant.helpers import selector

from .const import (
    CONF_GEMINI_MODEL,
    CONF_EXCLUDED_DEVICE_IDS,
    CONF_EXCLUDED_ENTITY_IDS,
    CONF_PROFILE_OVERRIDES,
    CONF_EXCLUDED_ENTITY_GLOBS,
    CONF_INCLUDED_DEVICE_IDS,
    CONF_INCLUDED_DOMAINS,
    CONF_INCLUDED_ENTITY_IDS,
    CONF_MAX_DAYS,
    CONF_NOTIFY_SERVICE,
    CONF_REPORT_TIME,
    CONF_SIDECAR_TIMEOUT,
    CONF_SIDECAR_TOKEN,
    CONF_SIDECAR_URL,
    DEFAULT_GEMINI_MODEL,
    DEFAULT_INCLUDED_DOMAINS,
    DEFAULT_MAX_DAYS,
    DEFAULT_NOTIFY_SERVICE,
    DEFAULT_REPORT_TIME,
    DEFAULT_SIDECAR_TIMEOUT,
    DEFAULT_SIDECAR_URL,
    DOMAIN,
    NAME,
)

TIME_RE = re.compile(r"^([01]\d|2[0-3]):([0-5]\d)$")
ENTITY_ID_RE = re.compile(r"^[a-z0-9_]+\.[a-z0-9_]+$")
SERVICE_RE = re.compile(r"^[a-z0-9_]+\.[a-z0-9_]+$")

DOMAIN_OPTIONS = [
    "sensor",
    "binary_sensor",
    "cover",
    "light",
    "switch",
    "climate",
    "lock",
    "select",
    "input_boolean",
    "input_select",
    "button",
    "number",
    "device_tracker",
    "person",
    "alarm_control_panel",
    "automation",
    "fan",
    "humidifier",
    "media_player",
    "remote",
    "scene",
    "script",
    "vacuum",
    "water_heater",
    "weather",
]

def _csv_to_list(value: str, *, lowercase: bool = True) -> list[str]:
    """Convert a comma-separated string to a normalized list."""
    parts = [part.strip() for part in value.split(",") if part.strip()]
    if lowercase:
        return [part.lower() for part in parts]
    return parts


def _list_to_csv(value: list[str] | tuple[str, ...] | None) -> str:
    """Convert a list to a comma-separated string."""
    if not value:
        return ""
    return ",".join(value)


def _normalize_list(value: Any, *, lowercase: bool = True) -> list[str]:
    """Normalize selector or CSV output to a list."""
    if not value:
        return []
    if isinstance(value, str):
        return _csv_to_list(value, lowercase=lowercase)
    items = [str(item).strip() for item in value if str(item).strip()]
    if lowercase:
        return [item.lower() for item in items]
    return items


def _validate_report_time(value: str) -> str:
    """Validate a HH:MM time string."""
    if not TIME_RE.fullmatch(value) or value not in {"08:00", "12:00", "18:00", "22:00"}:
        raise vol.Invalid("Report time must use HH:MM format")
    return value


def _validate_gemini_model(value: str) -> str:
    """Allow only Gemini model identifiers, never an arbitrary endpoint."""
    value = value.strip()
    if not value.startswith("gemini-") or len(value) <= len("gemini-") or len(value) > 128:
        raise vol.Invalid("Gemini model must be a supported gemini-* model")
    return value


def _validate_notify_service(value: str) -> str:
    """Accept the user's existing HA notify service, including Telegram."""
    value = value.strip().lower()
    if not SERVICE_RE.fullmatch(value):
        raise vol.Invalid("Notification service must use domain.service format")
    return value


def _validate_sidecar_url(value: str) -> str:
    """Validate the required external sidecar base URL."""
    if not value:
        raise vol.Invalid("Sidecar URL is required")
    try:
        parsed = urlsplit(value)
    except ValueError as err:
        raise vol.Invalid("Sidecar URL must be an HTTP(S) URL") from err
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.netloc
        or any(char.isspace() for char in value)
    ):
        raise vol.Invalid("Sidecar URL must be an HTTP(S) URL")
    return value.rstrip("/")


def _validate_sidecar_token(value: str) -> str:
    """Require a non-trivial shared secret for protected ingest endpoints."""
    value = value.strip()
    if len(value) < 8 or len(value) > 256:
        raise vol.Invalid("Sidecar token must be between 8 and 256 characters")
    return value


def _normalize_profile_overrides(value: Any) -> dict[str, dict[str, Any]]:
    """Parse optional per-entity profile settings without arbitrary data."""
    if not value:
        return {}
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except json.JSONDecodeError as err:
            raise vol.Invalid("Profile overrides must be valid JSON") from err
    if not isinstance(value, dict):
        raise vol.Invalid("Profile overrides must be a JSON object")
    allowed = {
        "numeric",
        "binary",
        "battery",
        "energy",
        "climate",
        "contact_security",
        "light_media",
        "generic",
    }
    result: dict[str, dict[str, Any]] = {}
    for entity_id, config in value.items():
        if not isinstance(entity_id, str) or not ENTITY_ID_RE.fullmatch(entity_id.lower()) or not isinstance(config, dict):
            raise vol.Invalid("Each profile override must map an entity ID to an object")
        kind = str(config.get("profile", config.get("profile_kind", "generic"))).strip().lower()
        if kind not in allowed:
            raise vol.Invalid("Unknown profile override")
        override: dict[str, Any] = {"profile_kind": kind}
        try:
            version = int(config.get("profile_version", 1))
        except (TypeError, ValueError) as err:
            raise vol.Invalid("Profile version must be an integer") from err
        if version < 1:
            raise vol.Invalid("Profile version must be positive")
        override["profile_version"] = version
        if "snapshot_interval" in config:
            try:
                interval = int(config["snapshot_interval"])
            except (TypeError, ValueError) as err:
                raise vol.Invalid("Snapshot interval must be an integer") from err
            if interval < 0 or interval > 86400:
                raise vol.Invalid("Snapshot interval must be between 0 and 86400 seconds")
            override["snapshot_interval"] = interval
        if "numeric_threshold" in config:
            try:
                threshold = float(config["numeric_threshold"])
            except (TypeError, ValueError) as err:
                raise vol.Invalid("Numeric threshold must be a number") from err
            if not math.isfinite(threshold) or threshold < 0 or threshold > 1e15:
                raise vol.Invalid("Numeric threshold must be between 0 and 1e15")
            override["numeric_threshold"] = threshold
        for key in ("allowed_states", "safe_metadata"):
            if key not in config:
                continue
            values = config[key]
            if not isinstance(values, list) or len(values) > 32 or any(
                not isinstance(item, str) or not item.strip() or len(item) > 128
                for item in values
            ):
                raise vol.Invalid(f"{key} must be a list of at most 32 strings")
            override[key] = [item.strip() for item in values]
        result[entity_id.lower()] = override
    return result


def _normalize_user_input(user_input: dict[str, Any]) -> dict[str, Any]:
    """Normalize config flow user input."""
    return {
        CONF_INCLUDED_DOMAINS: _normalize_list(user_input[CONF_INCLUDED_DOMAINS]),
        CONF_INCLUDED_DEVICE_IDS: _normalize_list(
            user_input.get(CONF_INCLUDED_DEVICE_IDS), lowercase=False
        ),
        CONF_INCLUDED_ENTITY_IDS: _normalize_list(
            user_input.get(CONF_INCLUDED_ENTITY_IDS)
        ),
        CONF_EXCLUDED_DEVICE_IDS: _normalize_list(
            user_input.get(CONF_EXCLUDED_DEVICE_IDS), lowercase=False
        ),
        CONF_EXCLUDED_ENTITY_IDS: _normalize_list(
            user_input.get(CONF_EXCLUDED_ENTITY_IDS)
        ),
        CONF_EXCLUDED_ENTITY_GLOBS: _csv_to_list(
            user_input.get(CONF_EXCLUDED_ENTITY_GLOBS, "")
        ),
        CONF_REPORT_TIME: _validate_report_time(
            str(user_input[CONF_REPORT_TIME]).strip()
        ),
        CONF_MAX_DAYS: user_input[CONF_MAX_DAYS],
        CONF_NOTIFY_SERVICE: _validate_notify_service(user_input[CONF_NOTIFY_SERVICE]),
        CONF_SIDECAR_URL: _validate_sidecar_url(
            str(user_input.get(CONF_SIDECAR_URL, DEFAULT_SIDECAR_URL)).strip()
        ),
        CONF_SIDECAR_TOKEN: _validate_sidecar_token(
            str(user_input.get(CONF_SIDECAR_TOKEN, ""))
        ),
        CONF_SIDECAR_TIMEOUT: user_input.get(
            CONF_SIDECAR_TIMEOUT, DEFAULT_SIDECAR_TIMEOUT
        ),
        CONF_GEMINI_MODEL: _validate_gemini_model(
            str(user_input.get(CONF_GEMINI_MODEL, DEFAULT_GEMINI_MODEL))
        ),
        CONF_PROFILE_OVERRIDES: _normalize_profile_overrides(
            user_input.get(CONF_PROFILE_OVERRIDES, {})
        ),
    }


def _schema(defaults: dict[str, Any]) -> vol.Schema:
    """Build the config form schema."""
    return vol.Schema(
        {
            vol.Required(
                CONF_INCLUDED_DOMAINS,
                default=defaults.get(CONF_INCLUDED_DOMAINS, DEFAULT_INCLUDED_DOMAINS),
            ): selector.SelectSelector(
                selector.SelectSelectorConfig(
                    options=DOMAIN_OPTIONS,
                    multiple=True,
                    mode=selector.SelectSelectorMode.DROPDOWN,
                )
            ),
            vol.Optional(
                CONF_INCLUDED_DEVICE_IDS,
                default=defaults.get(CONF_INCLUDED_DEVICE_IDS, []),
            ): selector.DeviceSelector(
                selector.DeviceSelectorConfig(multiple=True)
            ),
            vol.Optional(
                CONF_INCLUDED_ENTITY_IDS,
                default=defaults.get(CONF_INCLUDED_ENTITY_IDS, []),
            ): selector.EntitySelector(
                selector.EntitySelectorConfig(multiple=True)
            ),
            vol.Optional(
                CONF_EXCLUDED_DEVICE_IDS,
                default=defaults.get(CONF_EXCLUDED_DEVICE_IDS, []),
            ): selector.DeviceSelector(
                selector.DeviceSelectorConfig(multiple=True)
            ),
            vol.Optional(
                CONF_EXCLUDED_ENTITY_IDS,
                default=defaults.get(CONF_EXCLUDED_ENTITY_IDS, []),
            ): selector.EntitySelector(
                selector.EntitySelectorConfig(multiple=True)
            ),
            vol.Optional(
                CONF_EXCLUDED_ENTITY_GLOBS,
                default=_list_to_csv(defaults.get(CONF_EXCLUDED_ENTITY_GLOBS, [])),
            ): str,
            vol.Required(
                CONF_REPORT_TIME,
                default=defaults.get(CONF_REPORT_TIME, DEFAULT_REPORT_TIME),
            ): selector.SelectSelector(
                selector.SelectSelectorConfig(
                    options=["08:00", "12:00", "18:00", "22:00"],
                    mode=selector.SelectSelectorMode.DROPDOWN,
                )
            ),
            vol.Required(
                CONF_MAX_DAYS,
                default=defaults.get(CONF_MAX_DAYS, DEFAULT_MAX_DAYS),
            ): vol.All(vol.Coerce(int), vol.Range(min=7, max=120)),
            # Keep text fields serializable for Home Assistant's config-flow
            # response; strict custom validation runs on submit below.
            vol.Required(
                CONF_NOTIFY_SERVICE,
                default=defaults.get(CONF_NOTIFY_SERVICE, DEFAULT_NOTIFY_SERVICE),
            ): str,
            vol.Required(
                CONF_SIDECAR_URL,
                default=defaults.get(CONF_SIDECAR_URL, DEFAULT_SIDECAR_URL),
            ): str,
            vol.Required(
                CONF_SIDECAR_TOKEN,
                default=defaults.get(CONF_SIDECAR_TOKEN, ""),
            ): str,
            vol.Required(
                CONF_SIDECAR_TIMEOUT,
                default=defaults.get(
                    CONF_SIDECAR_TIMEOUT, DEFAULT_SIDECAR_TIMEOUT
                ),
            ): vol.All(vol.Coerce(int), vol.Range(min=1, max=120)),
            vol.Required(
                CONF_GEMINI_MODEL,
                default=defaults.get(CONF_GEMINI_MODEL, DEFAULT_GEMINI_MODEL),
            ): str,
            vol.Optional(
                CONF_PROFILE_OVERRIDES,
                default=json.dumps(defaults.get(CONF_PROFILE_OVERRIDES, {})),
            ): str,
        }
    )


class HomeDailyReportConfigFlow(config_entries.ConfigFlow, domain=DOMAIN):
    """Handle a Home Daily Report config flow."""

    VERSION = 1

    @staticmethod
    @callback
    def async_get_options_flow(
        config_entry: config_entries.ConfigEntry,
    ) -> HomeDailyReportOptionsFlow:
        """Create the options flow."""
        return HomeDailyReportOptionsFlow(config_entry)

    async def async_step_user(
        self, user_input: dict[str, Any] | None = None
    ) -> config_entries.ConfigFlowResult:
        """Handle the initial step."""
        errors: dict[str, str] = {}

        if user_input is not None:
            try:
                data = _normalize_user_input(user_input)
            except vol.Invalid:
                errors["base"] = "invalid_config"
            else:
                await self.async_set_unique_id(DOMAIN)
                self._abort_if_unique_id_configured()
                return self.async_create_entry(title=NAME, data=data)

        return self.async_show_form(
            step_id="user",
            data_schema=_schema({}),
            errors=errors,
        )


class HomeDailyReportOptionsFlow(config_entries.OptionsFlow):
    """Handle Home Daily Report options."""

    def __init__(self, config_entry: config_entries.ConfigEntry) -> None:
        """Initialize options flow."""
        self.config_entry = config_entry

    async def async_step_init(
        self, user_input: dict[str, Any] | None = None
    ) -> config_entries.ConfigFlowResult:
        """Manage integration options."""
        errors: dict[str, str] = {}

        if user_input is not None:
            try:
                options = _normalize_user_input(user_input)
            except vol.Invalid:
                errors["base"] = "invalid_config"
            else:
                return self.async_create_entry(title="", data=options)

        defaults = {**self.config_entry.data, **self.config_entry.options}
        return self.async_show_form(
            step_id="init",
            data_schema=_schema(defaults),
            errors=errors,
        )
