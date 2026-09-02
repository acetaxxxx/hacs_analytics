"""Constants for Home Daily Report."""

from homeassistant.const import Platform

DOMAIN = "home_daily_report"
NAME = "Home Daily Report"

PLATFORMS = [Platform.SENSOR, Platform.BUTTON]

CONF_INCLUDED_DOMAINS = "included_domains"
CONF_INCLUDED_DEVICE_IDS = "included_device_ids"
CONF_INCLUDED_ENTITY_IDS = "included_entity_ids"
CONF_EXCLUDED_DEVICE_IDS = "excluded_device_ids"
CONF_EXCLUDED_ENTITY_IDS = "excluded_entity_ids"
CONF_EXCLUDED_ENTITY_GLOBS = "excluded_entity_globs"
CONF_REPORT_TIME = "report_time"
CONF_MAX_DAYS = "max_days"
CONF_ENABLE_AI_SUMMARY = "enable_ai_summary"
CONF_AI_TASK_ENTITY_ID = "ai_task_entity_id"
CONF_NOTIFY_SERVICE = "notify_service"
CONF_NOTIFY_TARGETS = "notify_targets"
CONF_SIDECAR_URL = "sidecar_url"
CONF_SIDECAR_TOKEN = "sidecar_token"
CONF_SIDECAR_TIMEOUT = "sidecar_timeout"
CONF_GEMINI_MODEL = "gemini_model"
CONF_PROFILE_OVERRIDES = "profile_overrides"

DEFAULT_INCLUDED_DOMAINS = [
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
DEFAULT_REPORT_TIME = "08:00"
DEFAULT_MAX_DAYS = 35
DEFAULT_ENABLE_AI_SUMMARY = True
DEFAULT_NOTIFY_SERVICE = "persistent_notification.create"
DEFAULT_SIDECAR_URL = ""
DEFAULT_SIDECAR_TIMEOUT = 10
DEFAULT_GEMINI_MODEL = "gemini-2.5-flash"

STORE_VERSION = 1
STORE_KEY = f"{DOMAIN}.rollups"

EVENT_REPORT_READY = f"{DOMAIN}_report_ready"

SERVICE_GENERATE_REPORT = "generate_report"

ATTR_DATE = "date"
ATTR_USE_AI = "use_ai"
ATTR_NOTIFY = "notify"

STATE_PROBLEM = "problem"
STATE_OK = "ok"
