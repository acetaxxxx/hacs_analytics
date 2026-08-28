"""Daily rollup manager for Home Daily Report."""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from datetime import date, datetime, timedelta
import fnmatch
import json
import logging
import math
from typing import Any
import uuid

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import (
    ATTR_DEVICE_CLASS,
    ATTR_FRIENDLY_NAME,
    EVENT_STATE_CHANGED,
    STATE_UNAVAILABLE,
    STATE_UNKNOWN,
)
from homeassistant.core import Event, HomeAssistant, State, callback
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.event import (
    async_call_later,
    async_track_time_change,
    async_track_time_interval,
)
from homeassistant.helpers.storage import Store
import homeassistant.util.dt as dt_util

from .const import (
    CONF_AI_TASK_ENTITY_ID,
    CONF_ENABLE_AI_SUMMARY,
    CONF_GEMINI_MODEL,
    CONF_EXCLUDED_ENTITY_GLOBS,
    CONF_INCLUDED_DEVICE_IDS,
    CONF_INCLUDED_DOMAINS,
    CONF_INCLUDED_ENTITY_IDS,
    CONF_MAX_DAYS,
    CONF_NOTIFY_SERVICE,
    CONF_PROFILE_OVERRIDES,
    CONF_REPORT_TIME,
    DEFAULT_ENABLE_AI_SUMMARY,
    DEFAULT_INCLUDED_DOMAINS,
    DEFAULT_MAX_DAYS,
    DEFAULT_NOTIFY_SERVICE,
    DEFAULT_REPORT_TIME,
    CONF_SIDECAR_URL,
    DOMAIN,
    EVENT_REPORT_READY,
    NAME,
    STORE_KEY,
    STORE_VERSION,
)
from .sidecar import SidecarClient, SidecarError

_LOGGER = logging.getLogger(__name__)

UNKNOWN_STATES = {STATE_UNKNOWN, STATE_UNAVAILABLE}
SENSITIVE_ATTRIBUTE_NAMES = {
    "access_token",
    "api_key",
    "latitude",
    "longitude",
    "media_content_id",
    "password",
    "secret",
    "token",
}
NUMERIC_JUMP_THRESHOLDS = {
    "temperature": 3.0,
    "humidity": 10.0,
    "battery": 20.0,
}


class HomeDailyReportManager:
    """Collect state changes and build compact daily reports."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        """Initialize the manager."""
        self.hass = hass
        self.entry = entry
        self._store: Store[dict[str, Any]] = Store(hass, STORE_VERSION, STORE_KEY)
        self._data: dict[str, Any] = {"days": {}, "last_report": None}
        self._unsubscribers: list[Callable[[], None]] = []
        self._listeners: list[Callable[[], None]] = []
        self._save_unsub: Callable[[], None] | None = None
        self._flush_unsub: Callable[[], None] | None = None
        self._snapshot_unsub: Callable[[], None] | None = None
        self._pending_events: list[dict[str, Any]] = []
        self._entity_registry: er.EntityRegistry | None = None
        self._sidecar_client: SidecarClient | None = None
        self._sidecar_configured = bool(str(self.options.get(CONF_SIDECAR_URL, "")).strip())
        self._sidecar_probe_task: asyncio.Task[None] | None = None
        self._sidecar_heartbeat_task: asyncio.Task[None] | None = None
        self._sidecar_stop = asyncio.Event()
        self._sidecar_status = "unconfigured"
        self._sidecar_error: str | None = None
        self._last_snapshot_at: dict[str, datetime] = {}
        self._risk_cooldowns: dict[str, datetime] = {}
        self._flush_lock = asyncio.Lock()
        self._flush_tasks: set[asyncio.Task[None]] = set()
        self._report_tasks: set[asyncio.Task[None]] = set()
        self._report_poll_tasks: dict[str, asyncio.Task[None]] = {}

    @property
    def options(self) -> dict[str, Any]:
        """Return merged config entry data and options."""
        return {**self.entry.data, **self.entry.options}

    @property
    def last_report(self) -> dict[str, Any] | None:
        """Return the last generated report."""
        return self._data.get("last_report")

    @property
    def tracked_entity_count(self) -> int:
        """Return current matching entity count."""
        return sum(
            1
            for state in self.hass.states.async_all()
            if self._should_track_entity(state.entity_id)
        )

    @property
    def anomaly_count(self) -> int:
        """Return anomaly count from the last report."""
        report = self.last_report
        if not report:
            return 0
        return len(report.get("anomalies", [])) + len(report.get("risks", []))

    @property
    def sidecar_status(self) -> str:
        """Return sidecar connectivity status for the status sensor."""
        return self._sidecar_status

    @property
    def sidecar_error(self) -> str | None:
        """Return the last safe sidecar error code, if any."""
        return self._sidecar_error

    async def async_load(self) -> None:
        """Load persisted rollups."""
        if not self._sidecar_configured:
            stored = await self._store.async_load()
            if stored:
                self._data = stored
        self._data.setdefault("days", {})
        self._data.setdefault("last_report", None)
        self._prune_days()

    async def async_start(self) -> None:
        """Start collecting data."""
        self._entity_registry = er.async_get(self.hass)
        self._sidecar_client = SidecarClient(self.hass, self.options)
        if self._sidecar_client.configured:
            self._sidecar_status = "degraded"
            self._sidecar_probe_task = self.hass.async_create_task(
                self._async_sidecar_probe_loop()
            )
            self._sidecar_heartbeat_task = self.hass.async_create_task(
                self._async_heartbeat_loop()
            )
        self._unsubscribers.append(
            self.hass.bus.async_listen(EVENT_STATE_CHANGED, self._handle_state_changed)
        )
        self._schedule_daily_report()
        self._record_current_snapshot()
        self._snapshot_unsub = async_track_time_interval(
            self.hass, self._handle_periodic_snapshot, timedelta(minutes=5)
        )
        if not self._sidecar_configured:
            self._schedule_save()
        self._notify_listeners()

    async def async_unload(self) -> None:
        """Stop collecting data and persist the latest rollup."""
        if self._sidecar_probe_task is not None:
            self._sidecar_stop.set()
            self._sidecar_probe_task.cancel()
            try:
                await self._sidecar_probe_task
            except asyncio.CancelledError:
                pass
            self._sidecar_probe_task = None
        if self._sidecar_heartbeat_task is not None:
            self._sidecar_stop.set()
            self._sidecar_heartbeat_task.cancel()
            try:
                await self._sidecar_heartbeat_task
            except asyncio.CancelledError:
                pass
            self._sidecar_heartbeat_task = None
        for unsub in self._unsubscribers:
            unsub()
        self._unsubscribers.clear()
        if self._save_unsub is not None:
            self._save_unsub()
            self._save_unsub = None
        if self._flush_unsub is not None:
            self._flush_unsub()
            self._flush_unsub = None
        if self._snapshot_unsub is not None:
            self._snapshot_unsub()
            self._snapshot_unsub = None
        for task in tuple(self._flush_tasks):
            task.cancel()
        if self._flush_tasks:
            await asyncio.gather(*self._flush_tasks, return_exceptions=True)
        for task in tuple(self._report_tasks):
            task.cancel()
        if self._report_tasks:
            await asyncio.gather(*self._report_tasks, return_exceptions=True)
        self._report_poll_tasks.clear()
        self._pending_events.clear()
        if not self._sidecar_configured:
            await self.async_save()

    async def _async_sidecar_probe_loop(self) -> None:
        """Probe the sidecar without making HA setup depend on it."""
        while not self._sidecar_stop.is_set():
            await self._async_probe_sidecar()
            try:
                await asyncio.wait_for(self._sidecar_stop.wait(), timeout=60)
            except asyncio.TimeoutError:
                continue

    async def _async_probe_sidecar(self) -> None:
        """Update sidecar status from one best-effort health request."""
        if self._sidecar_client is None or not self._sidecar_client.configured:
            return
        try:
            health = await self._sidecar_client.async_health()
        except SidecarError as err:
            self._set_sidecar_status("degraded", err.code)
            return

        if health.get("status") == "ok" and health.get("database") == "ready":
            self._set_sidecar_status("healthy", None)
        else:
            self._set_sidecar_status("degraded", "not_ready")

    async def _async_heartbeat_loop(self) -> None:
        """Send periodic heartbeats to the sidecar every 60 seconds."""
        while not self._sidecar_stop.is_set():
            await self._async_send_heartbeat()
            try:
                await asyncio.wait_for(self._sidecar_stop.wait(), timeout=60)
            except asyncio.TimeoutError:
                continue

    async def _async_send_heartbeat(self) -> None:
        """Send one heartbeat to the sidecar."""
        if self._sidecar_client is None or not self._sidecar_client.configured:
            return
        source_instance = getattr(self.entry, "entry_id", "homeassistant") or "homeassistant"
        payload = {
            "source_instance": source_instance,
            "observed_at": dt_util.utcnow().isoformat(),
        }
        try:
            await self._sidecar_client.async_ingest_heartbeat(payload)
            self._set_sidecar_status("healthy", None)
        except SidecarError as err:
            self._set_sidecar_status("degraded", err.code)
        except Exception as err:
            _LOGGER.debug("Error sending sidecar heartbeat: %s", err)
            self._set_sidecar_status("degraded", "connection")

    def _set_sidecar_status(self, status: str, error: str | None) -> None:
        """Update sidecar state and refresh status entities when it changes."""
        if self._sidecar_status == status and self._sidecar_error == error:
            return
        self._sidecar_status = status
        self._sidecar_error = error
        self._notify_listeners()

    def _profile_override(self, entity_id: str) -> dict[str, Any]:
        """Return the small allowlisted profile metadata for one entity."""
        overrides = self._option(CONF_PROFILE_OVERRIDES, {})
        if not isinstance(overrides, dict):
            return {}
        value = overrides.get(entity_id.lower())
        if not isinstance(value, dict):
            return {}
        return {
            key: value[key]
            for key in (
                "profile_kind",
                "profile_version",
                "snapshot_interval",
                "numeric_threshold",
                "allowed_states",
                "safe_metadata",
            )
            if key in value
        }

    def async_add_listener(self, listener: Callable[[], None]) -> Callable[[], None]:
        """Subscribe to manager updates."""
        self._listeners.append(listener)

        def _remove_listener() -> None:
            if listener in self._listeners:
                self._listeners.remove(listener)

        return _remove_listener

    async def async_save(self) -> None:
        """Persist rollups."""
        if self._sidecar_configured:
            return
        self._prune_days()
        await self._store.async_save(self._data)

    async def async_generate_report(
        self,
        report_date: str | None = None,
        use_ai: bool = True,
        notify: bool = True,
    ) -> dict[str, Any]:
        """Generate a daily report through the external Gemini sidecar."""
        target_date = report_date or self._default_report_date()
        if self._sidecar_client is not None and self._sidecar_client.configured:
            return await self._async_generate_sidecar_report(
                target_date,
                notify,
                use_ai and self._option(CONF_ENABLE_AI_SUMMARY, DEFAULT_ENABLE_AI_SUMMARY),
            )
        report = self._build_report(target_date)

        if use_ai and self._option(CONF_ENABLE_AI_SUMMARY, DEFAULT_ENABLE_AI_SUMMARY):
            report["ai"] = await self._async_generate_ai_summary(report)

        self._data["last_report"] = report
        await self.async_save()
        self._notify_listeners()
        self.hass.bus.async_fire(EVENT_REPORT_READY, {"report": report})

        if notify:
            await self._async_notify(report)

        return report

    async def _async_generate_sidecar_report(
        self, target_date: str, notify: bool, use_ai: bool = True
    ) -> dict[str, Any]:
        """Request and bounded-poll one idempotent sidecar report."""
        assert self._sidecar_client is not None
        model = str(self._option(CONF_GEMINI_MODEL, "gemini-2.5-flash"))
        try:
            await self._sidecar_client.async_request_report(target_date, model, use_ai)
        except SidecarError as err:
            self._set_sidecar_status("degraded", err.code)
            return {
                "report_date": target_date,
                "status": "sidecar_unavailable",
                "error_code": err.code,
            }

        # The sidecar worker is intentionally low-frequency on the old
        # Windows host. Keep the HA request bounded, but allow enough time
        # for its 30-second worker tick plus one Gemini call.
        for _ in range(90):
            try:
                result = await self._sidecar_client.async_get_report_result(target_date)
            except SidecarError as err:
                self._set_sidecar_status("degraded", err.code)
                await asyncio.sleep(2)
                continue
            if result is not None:
                await self._accept_sidecar_report(result, notify)
                return result
            await asyncio.sleep(2)
        self._schedule_report_poll(target_date, notify)
        return {"report_date": target_date, "status": "pending"}

    async def _accept_sidecar_report(
        self, result: dict[str, Any], notify: bool
    ) -> None:
        """Publish one completed sidecar result to HA and notifications."""
        self._set_sidecar_status("healthy", None)
        self._data["last_report"] = result
        if not self._sidecar_configured:
            await self.async_save()
        self._notify_listeners()
        self.hass.bus.async_fire(EVENT_REPORT_READY, {"report": result})
        if notify:
            await self._async_notify(result)

    def _schedule_report_poll(self, report_date: str, notify: bool) -> None:
        """Continue polling a pending report across delayed worker retries."""
        current = self._report_poll_tasks.get(report_date)
        if current is not None and not current.done():
            return
        task = self.hass.async_create_task(
            self._async_poll_report_until_ready(report_date, notify)
        )
        self._report_poll_tasks[report_date] = task
        self._report_tasks.add(task)

        def _done(completed: asyncio.Task[None]) -> None:
            self._report_tasks.discard(completed)
            if self._report_poll_tasks.get(report_date) is completed:
                self._report_poll_tasks.pop(report_date, None)

        task.add_done_callback(_done)

    async def _async_poll_report_until_ready(
        self, report_date: str, notify: bool
    ) -> None:
        """Poll for up to 26 hours so scheduled Gemini retries can finish."""
        assert self._sidecar_client is not None
        for _ in range(26 * 60):
            await asyncio.sleep(60)
            try:
                result = await self._sidecar_client.async_get_report_result(report_date)
            except SidecarError as err:
                self._set_sidecar_status("degraded", err.code)
                continue
            if result is not None:
                await self._accept_sidecar_report(result, notify)
                return

    @callback
    def _handle_state_changed(self, event: Event) -> None:
        """Handle a Home Assistant state change event."""
        entity_id: str = event.data["entity_id"]
        if not self._should_track_entity(entity_id):
            return

        old_state: State | None = event.data.get("old_state")
        new_state: State | None = event.data.get("new_state")
        if new_state is None:
            return

        self._record_state(new_state, old_state=old_state, is_snapshot=False)
        if old_state is not None and old_state.state != new_state.state:
            self._schedule_immediate_risk(new_state)
        if not self._sidecar_configured:
            self._schedule_save()
        self._notify_listeners()

    def _record_current_snapshot(self) -> None:
        """Record the current state of all matching entities without transitions."""
        for state in self.hass.states.async_all():
            if self._should_track_entity(state.entity_id):
                self._record_state(state, old_state=None, is_snapshot=True)

    @callback
    def _handle_periodic_snapshot(self, _now: datetime) -> None:
        """Sample numeric entities periodically for slow-changing sensors."""
        now = dt_util.utcnow()
        for state in self.hass.states.async_all():
            if not self._should_track_entity(state.entity_id) or _as_float(state.state) is None:
                continue
            override = self._profile_override(state.entity_id)
            interval = override.get("snapshot_interval", 300)
            try:
                interval_seconds = int(interval)
            except (TypeError, ValueError):
                interval_seconds = 300
            if interval_seconds <= 0:
                interval_seconds = 300
            last_snapshot = self._last_snapshot_at.get(state.entity_id)
            if last_snapshot is not None and now - last_snapshot < timedelta(seconds=interval_seconds):
                continue
            self._record_state(state, old_state=None, is_snapshot=True)
        if not self._sidecar_configured:
            self._schedule_save()

    def _record_state(
        self,
        new_state: State,
        old_state: State | None,
        is_snapshot: bool,
    ) -> None:
        """Record one state sample into today's rollup."""
        if self._sidecar_configured:
            self._queue_sidecar_state(new_state, old_state, is_snapshot)
            return
        day = self._get_day(self._today())
        entity = self._get_entity_rollup(day, new_state)

        now_iso = dt_util.utcnow().isoformat()
        entity["last_seen"] = now_iso
        entity["name"] = new_state.attributes.get(ATTR_FRIENDLY_NAME, new_state.name)
        entity["device_class"] = new_state.attributes.get(ATTR_DEVICE_CLASS)

        if entity.get("first_seen") is None:
            entity["first_seen"] = now_iso

        entity["observations"] += 1

        state_value = new_state.state
        if state_value == STATE_UNAVAILABLE:
            entity["unavailable_count"] += 1
        if state_value == STATE_UNKNOWN:
            entity["unknown_count"] += 1

        if old_state is not None and old_state.state != state_value:
            entity["changes"] += 1
            transition_key = f"{old_state.state}=>{state_value}"
            transitions = entity["transitions"]
            transitions[transition_key] = transitions.get(transition_key, 0) + 1

        numeric_value = _as_float(state_value)
        if numeric_value is not None:
            self._record_numeric_sample(entity, new_state, numeric_value)

        if not is_snapshot:
            day["change_events"] += 1
        day["updated_at"] = now_iso

    def _queue_sidecar_state(
        self, new_state: State, old_state: State | None, is_snapshot: bool
    ) -> None:
        """Create a redacted sidecar observation without local persistence."""
        if self._sidecar_client is None or not self._sidecar_client.configured:
            return
        now_iso = dt_util.utcnow().isoformat()
        numeric_value = _as_float(new_state.state)
        metadata: dict[str, Any] = {}
        device_class = new_state.attributes.get(ATTR_DEVICE_CLASS)
        if device_class is not None:
            metadata["device_class"] = str(device_class)
        friendly_name = new_state.attributes.get(ATTR_FRIENDLY_NAME, new_state.name)
        if friendly_name is not None:
            metadata["friendly_name"] = str(friendly_name)
        override = self._profile_override(new_state.entity_id)
        if override:
            metadata.update({
                key: override[key]
                for key in ("profile_kind", "profile_version", "snapshot_interval", "numeric_threshold")
                if key in override
            })
            for key in override.get("safe_metadata", [])[:24]:
                value = _safe_attribute_value(key, new_state.attributes.get(key))
                if value is not None:
                    metadata[key] = value
        profile_version = int(override.get("profile_version", 1)) if override else 1
        if is_snapshot:
            self._last_snapshot_at[new_state.entity_id] = dt_util.utcnow()
        self._queue_sidecar_event({
            "event_id": uuid.uuid4().hex,
            "observed_at": now_iso,
            "entity_id": new_state.entity_id,
            "kind": "snapshot" if is_snapshot or old_state is None or old_state.state == new_state.state else "state_change",
            "old_state": old_state.state if old_state is not None else None,
            "new_state": new_state.state,
            "numeric_value": numeric_value,
            "unit": str(new_state.attributes["unit_of_measurement"]) if new_state.attributes.get("unit_of_measurement") is not None else None,
            "metadata": metadata,
            "profile_version": profile_version,
        })

    def _queue_sidecar_event(self, event_dto: dict[str, Any]) -> None:
        """Queue an event for batch delivery to the sidecar."""
        if len(self._pending_events) >= 500:
            self._pending_events.pop(0)
        self._pending_events.append(event_dto)

        if len(self._pending_events) >= 100:
            if self._flush_unsub is not None:
                self._flush_unsub()
                self._flush_unsub = None
            self._create_flush_task()
        elif self._flush_unsub is None:
            self._flush_unsub = async_call_later(self.hass, 30, self._handle_flush_later)

    @callback
    def _handle_flush_later(self, _now: datetime) -> None:
        """Flush queued events after debounce delay."""
        self._flush_unsub = None
        self._create_flush_task()

    def _create_flush_task(self, retry_attempt: int = 0) -> None:
        """Track a flush task so reload/unload cannot leave it running."""
        task = self.hass.async_create_task(self._async_flush_events(retry_attempt))
        self._flush_tasks.add(task)
        task.add_done_callback(self._flush_tasks.discard)

    async def _async_flush_events(self, retry_attempt: int = 0) -> None:
        """Flush up to 100 queued events to the sidecar."""
        async with self._flush_lock:
            if not self._pending_events or self._sidecar_client is None or not self._sidecar_client.configured:
                return

            batch_events = self._pending_events[:100]
            self._pending_events = self._pending_events[100:]

            source_instance = getattr(self.entry, "entry_id", "homeassistant") or "homeassistant"
            batch = {
                "source_instance": source_instance,
                "sent_at": dt_util.utcnow().isoformat(),
                "events": batch_events,
            }

            try:
                await self._sidecar_client.async_ingest_events(batch)
                self._set_sidecar_status("healthy", None)
            except SidecarError as err:
                self._set_sidecar_status("degraded", err.code)
                if retry_attempt < 3 and err.code in {
                    "timeout",
                    "connection",
                    "busy",
                    "unavailable",
                }:
                    self._pending_events = (batch_events + self._pending_events)[:500]
                    self._schedule_sidecar_retry(retry_attempt + 1)
                    return
            except Exception as err:
                _LOGGER.debug("Error flushing sidecar events: %s", err)
                self._set_sidecar_status("degraded", "connection")
                if retry_attempt < 3:
                    self._pending_events = (batch_events + self._pending_events)[:500]
                    self._schedule_sidecar_retry(retry_attempt + 1)
                    return

            if len(self._pending_events) >= 100:
                self._create_flush_task()
            elif self._pending_events and self._flush_unsub is None:
                self._flush_unsub = async_call_later(self.hass, 30, self._handle_flush_later)

    def _schedule_sidecar_retry(self, retry_attempt: int) -> None:
        """Retry a failed in-memory batch a bounded number of times."""
        if self._flush_unsub is not None:
            self._flush_unsub()
        delay = (5, 15, 60)[min(retry_attempt - 1, 2)]

        @callback
        def _retry(_now: datetime) -> None:
            self._flush_unsub = None
            self._create_flush_task(retry_attempt)

        self._flush_unsub = async_call_later(self.hass, delay, _retry)

    def _record_numeric_sample(
        self,
        entity: dict[str, Any],
        state: State,
        value: float,
    ) -> None:
        """Record numeric sensor statistics."""
        numeric = entity["numeric"]
        numeric["count"] += 1
        numeric["sum"] += value
        numeric["min"] = value if numeric["min"] is None else min(numeric["min"], value)
        numeric["max"] = value if numeric["max"] is None else max(numeric["max"], value)

        last = numeric.get("last")
        if last is not None:
            delta = abs(value - last)
            numeric["max_delta"] = max(numeric.get("max_delta", 0.0), delta)
            device_class = state.attributes.get(ATTR_DEVICE_CLASS)
            threshold = NUMERIC_JUMP_THRESHOLDS.get(str(device_class))
            if threshold is not None and delta >= threshold:
                numeric["large_jump_count"] += 1

        numeric["last"] = value

    def _build_report(self, target_date: str) -> dict[str, Any]:
        """Build a report payload for a date."""
        day = self._data["days"].get(target_date, self._empty_day(target_date))
        entities = day.get("entities", {})

        top_changers = sorted(
            (
                {
                    "entity_id": entity_id,
                    "name": entity.get("name"),
                    "changes": entity.get("changes", 0),
                    "top_transitions": _top_items(entity.get("transitions", {}), 5),
                }
                for entity_id, entity in entities.items()
                if entity.get("changes", 0) > 0
            ),
            key=lambda item: item["changes"],
            reverse=True,
        )[:10]

        unavailable = sorted(
            (
                {
                    "entity_id": entity_id,
                    "name": entity.get("name"),
                    "unavailable_count": entity.get("unavailable_count", 0),
                    "unknown_count": entity.get("unknown_count", 0),
                }
                for entity_id, entity in entities.items()
                if entity.get("unavailable_count", 0) > 0
                or entity.get("unknown_count", 0) > 0
            ),
            key=lambda item: item["unavailable_count"] + item["unknown_count"],
            reverse=True,
        )

        numeric = self._numeric_highlights(entities)
        anomalies = self._detect_anomalies(entities)
        total_changes = sum(entity.get("changes", 0) for entity in entities.values())

        report = {
            "date": target_date,
            "generated_at": dt_util.utcnow().isoformat(),
            "summary": {
                "tracked_entities": len(entities),
                "state_changes": total_changes,
                "entities_with_changes": len(top_changers),
                "entities_with_availability_issues": len(unavailable),
                "anomalies": len(anomalies),
            },
            "top_changers": top_changers,
            "availability": unavailable[:20],
            "numeric_highlights": numeric,
            "anomalies": anomalies,
            "trends": {
                "7d": self._window_summary(target_date, 7),
                "14d": self._window_summary(target_date, 14),
                "28d": self._window_summary(target_date, 28),
            },
        }
        report["trend_deltas"] = {
            "7d_vs_previous_7d": self._window_delta(target_date, 7),
            "14d_vs_previous_14d": self._window_delta(target_date, 14),
        }
        return report

    async def _async_generate_ai_summary(self, report: dict[str, Any]) -> dict[str, Any]:
        """Generate a legacy local report through Home Assistant AI Task."""
        service_data: dict[str, Any] = {
            "task_name": f"home_daily_report_{report['date']}",
            "instructions": (
                "You are a Home Assistant daily-report assistant. Analyze the "
                "following compact rollup JSON and respond in Traditional Chinese. "
                "Focus on useful household observations, possible issues, and "
                "small improvement suggestions. Avoid inventing device behavior "
                "that is not present in the data.\n\n"
                f"{json.dumps(report, ensure_ascii=False)}"
            ),
            "structure": {
                "title": {
                    "description": "Short report title in Traditional Chinese",
                    "required": True,
                    "selector": {"text": {}},
                },
                "summary": {
                    "description": "Concise daily summary in Traditional Chinese",
                    "required": True,
                    "selector": {"text": {}},
                },
                "alerts": {
                    "description": "Important alerts or empty text",
                    "required": True,
                    "selector": {"text": {}},
                },
                "suggestions": {
                    "description": "Practical improvement suggestions",
                    "required": True,
                    "selector": {"text": {}},
                },
            },
        }

        target = None
        ai_task_entity_id = self._option(CONF_AI_TASK_ENTITY_ID, "")
        if ai_task_entity_id:
            target = {"entity_id": ai_task_entity_id}

        try:
            response = await self.hass.services.async_call(
                "ai_task",
                "generate_data",
                service_data,
                blocking=True,
                return_response=True,
                target=target,
            )
        except Exception as err:  # noqa: BLE001
            _LOGGER.warning("AI Task report generation failed: %s", err)
            return {"ok": False, "error": str(err)}

        return {"ok": True, "response": response}

    async def _async_notify(self, report: dict[str, Any]) -> None:
        """Send a notification for the generated report."""
        service_name = self._option(CONF_NOTIFY_SERVICE, DEFAULT_NOTIFY_SERVICE)
        if "." not in service_name:
            _LOGGER.warning("Invalid notify service: %s", service_name)
            return

        domain, service = service_name.split(".", 1)
        message = self._notification_message(report)
        report_date = report.get("date", report.get("report_date", self._today()))
        chunks = _split_message(message)
        for index, chunk in enumerate(chunks, start=1):
            service_data: dict[str, Any] = {
                "title": "Home Daily Report",
                "message": chunk,
            }
            if service_name == DEFAULT_NOTIFY_SERVICE:
                suffix = f"_{index}" if len(chunks) > 1 else ""
                service_data["notification_id"] = f"{DOMAIN}_{report_date}{suffix}"

            try:
                await self.hass.services.async_call(
                    domain,
                    service,
                    service_data,
                    blocking=False,
                )
            except Exception as err:  # noqa: BLE001
                _LOGGER.warning("Failed to send report notification: %s", err)

    def _schedule_immediate_risk(self, state: State) -> None:
        """Notify Telegram about a high-risk state with a local cooldown."""
        device_class = str(state.attributes.get(ATTR_DEVICE_CLASS, "")).lower()
        state_value = state.state.lower()
        if device_class not in {"smoke", "gas", "moisture", "leak", "door", "window", "lock", "safety"}:
            return
        if state_value not in {"on", "open", "detected", "triggered", "unlocked"}:
            return
        key = f"{state.entity_id}:{state_value}"
        now = dt_util.utcnow()
        if now < self._risk_cooldowns.get(key, datetime.min.replace(tzinfo=now.tzinfo)):
            return
        self._risk_cooldowns[key] = now + timedelta(hours=1)
        self.hass.async_create_task(self._async_notify_immediate_risk(state))

    async def _async_notify_immediate_risk(self, state: State) -> None:
        """Send an advisory high-risk notification without executing actions."""
        name = state.attributes.get(ATTR_FRIENDLY_NAME, state.name)
        await self._async_notify({
            "report_date": self._today(),
            "summary": f"高風險狀態提醒：{name} 目前為 {state.state}。請人工確認；系統不會自動執行任何動作。",
            "risks": [{"title": f"{name} 狀態需要確認"}],
            "suggestions": [],
            "data_quality": {"coverage": 1, "data_gaps": []},
        })

    def _notification_message(self, report: dict[str, Any]) -> str:
        """Build a human-readable notification message."""
        if isinstance(report.get("summary"), str) and "report_date" in report:
            lines = [report.get("summary", "")]
            quality = report.get("data_quality") or {}
            if quality:
                lines.append(
                    f"資料涵蓋率：{quality.get('coverage', 0):.0%}；"
                    f"資料缺口：{len(quality.get('data_gaps', []))}"
                )
            risks = report.get("risks", [])
            if risks:
                lines.append("風險：" + "、".join(str(item.get("title", "")) for item in risks))
            suggestions = report.get("suggestions", [])
            if suggestions:
                lines.append("建議：" + "、".join(str(item.get("title", "")) for item in suggestions))
            return "\n\n".join(line for line in lines if line)
        ai = report.get("ai", {})
        if ai.get("ok") and isinstance(ai.get("response"), dict):
            data = ai["response"].get("data")
            if isinstance(data, dict):
                parts = [
                    data.get("summary"),
                    data.get("alerts"),
                    data.get("suggestions"),
                ]
                message = "\n\n".join(part for part in parts if part)
                if message:
                    return message

        summary = report["summary"]
        return (
            f"Date: {report['date']}\n"
            f"Tracked entities: {summary['tracked_entities']}\n"
            f"State changes: {summary['state_changes']}\n"
            f"Availability issues: {summary['entities_with_availability_issues']}\n"
            f"Anomalies: {summary['anomalies']}"
        )

    def _numeric_highlights(self, entities: dict[str, Any]) -> list[dict[str, Any]]:
        """Return numeric entity highlights."""
        rows: list[dict[str, Any]] = []
        for entity_id, entity in entities.items():
            numeric = entity.get("numeric", {})
            count = numeric.get("count", 0)
            if count <= 0:
                continue
            rows.append(
                {
                    "entity_id": entity_id,
                    "name": entity.get("name"),
                    "device_class": entity.get("device_class"),
                    "count": count,
                    "min": _round(numeric["min"]),
                    "max": _round(numeric["max"]),
                    "avg": _round(numeric["sum"] / count),
                    "max_delta": _round(numeric.get("max_delta", 0.0)),
                    "large_jump_count": numeric.get("large_jump_count", 0),
                }
            )
        rows.sort(
            key=lambda item: (item["large_jump_count"], item["max_delta"]),
            reverse=True,
        )
        return rows[:20]

    def _detect_anomalies(self, entities: dict[str, Any]) -> list[dict[str, Any]]:
        """Detect simple anomalies from one daily rollup."""
        anomalies: list[dict[str, Any]] = []
        for entity_id, entity in entities.items():
            if entity.get("unavailable_count", 0) >= 3:
                anomalies.append(
                    {
                        "type": "availability",
                        "entity_id": entity_id,
                        "name": entity.get("name"),
                        "detail": "Entity was unavailable multiple times.",
                        "count": entity["unavailable_count"],
                    }
                )
            if entity.get("changes", 0) >= 50:
                anomalies.append(
                    {
                        "type": "frequent_changes",
                        "entity_id": entity_id,
                        "name": entity.get("name"),
                        "detail": "Entity changed state unusually often.",
                        "count": entity["changes"],
                    }
                )
            numeric = entity.get("numeric", {})
            if numeric.get("large_jump_count", 0) > 0:
                anomalies.append(
                    {
                        "type": "numeric_jump",
                        "entity_id": entity_id,
                        "name": entity.get("name"),
                        "device_class": entity.get("device_class"),
                        "detail": "Numeric sensor had one or more large jumps.",
                        "count": numeric["large_jump_count"],
                        "max_delta": _round(numeric.get("max_delta", 0.0)),
                    }
                )
        return anomalies[:30]

    def _window_summary(self, end_date: str, days: int) -> dict[str, Any]:
        """Build a compact trend window summary."""
        dates = _date_range(end_date, days)
        day_rows = [
            self._data["days"][day_key]
            for day_key in dates
            if day_key in self._data["days"]
        ]
        if not day_rows:
            return {"days_with_data": 0}

        state_changes = 0
        availability_issues = 0
        numeric_by_class: dict[str, dict[str, float]] = {}

        for day in day_rows:
            for entity in day.get("entities", {}).values():
                state_changes += entity.get("changes", 0)
                availability_issues += entity.get("unavailable_count", 0)
                numeric = entity.get("numeric", {})
                count = numeric.get("count", 0)
                device_class = str(entity.get("device_class") or "numeric")
                if count > 0:
                    row = numeric_by_class.setdefault(
                        device_class, {"sum": 0.0, "count": 0.0}
                    )
                    row["sum"] += numeric["sum"]
                    row["count"] += count

        numeric_avg = {
            device_class: _round(row["sum"] / row["count"])
            for device_class, row in numeric_by_class.items()
            if row["count"] > 0
        }

        return {
            "days_with_data": len(day_rows),
            "state_changes": state_changes,
            "avg_state_changes_per_day": _round(state_changes / len(day_rows)),
            "availability_issues": availability_issues,
            "numeric_avg_by_device_class": numeric_avg,
        }

    def _window_delta(self, end_date: str, days: int) -> dict[str, Any]:
        """Compare a trend window with the previous matching window."""
        current = self._window_summary(end_date, days)
        previous_end = (
            datetime.fromisoformat(end_date).date() - timedelta(days=days)
        ).isoformat()
        previous = self._window_summary(previous_end, days)

        if not current.get("days_with_data") or not previous.get("days_with_data"):
            return {"available": False}

        return {
            "available": True,
            "state_changes_delta": current["state_changes"]
            - previous["state_changes"],
            "availability_issues_delta": current["availability_issues"]
            - previous["availability_issues"],
        }

    def _schedule_daily_report(self) -> None:
        """Schedule the configured daily report time."""
        hour, minute = self._report_time_parts()
        self._unsubscribers.append(
            async_track_time_change(
                self.hass,
                self._handle_daily_report_time,
                hour=hour,
                minute=minute,
                second=0,
            )
        )

    @callback
    def _handle_daily_report_time(self, _now: datetime) -> None:
        """Generate the scheduled report."""
        task = self.hass.async_create_task(self.async_generate_report())
        self._report_tasks.add(task)
        task.add_done_callback(self._report_tasks.discard)

    def _schedule_save(self) -> None:
        """Debounce storage writes."""
        if self._save_unsub is not None:
            return
        self._save_unsub = async_call_later(self.hass, 30, self._handle_save_later)

    @callback
    def _handle_save_later(self, _now: datetime) -> None:
        """Persist after debounce delay."""
        self._save_unsub = None
        self.hass.async_create_task(self.async_save())

    def _should_track_entity(self, entity_id: str) -> bool:
        """Return true if an entity should be tracked."""
        for pattern in self._option(CONF_EXCLUDED_ENTITY_GLOBS, []):
            if fnmatch.fnmatch(entity_id, pattern):
                return False

        included_entity_ids = self._option(CONF_INCLUDED_ENTITY_IDS, [])
        if entity_id in included_entity_ids:
            return True

        domain = entity_id.split(".", 1)[0]
        included_domains = self._option(CONF_INCLUDED_DOMAINS, DEFAULT_INCLUDED_DOMAINS)
        if domain not in included_domains:
            return False

        included_device_ids = self._option(CONF_INCLUDED_DEVICE_IDS, [])
        if included_device_ids and not self._entity_belongs_to_selected_device(
            entity_id, included_device_ids
        ):
            return False

        return True

    def _entity_belongs_to_selected_device(
        self, entity_id: str, included_device_ids: list[str]
    ) -> bool:
        """Return true if an entity belongs to a selected device."""
        registry = self._entity_registry or er.async_get(self.hass)
        registry_entry = registry.async_get(entity_id)
        return bool(
            registry_entry
            and registry_entry.device_id
            and registry_entry.device_id in included_device_ids
        )

    def _get_day(self, day_key: str) -> dict[str, Any]:
        """Return a day rollup, creating it if needed."""
        days = self._data.setdefault("days", {})
        if day_key not in days:
            days[day_key] = self._empty_day(day_key)
        return days[day_key]

    def _empty_day(self, day_key: str) -> dict[str, Any]:
        """Create an empty day rollup."""
        now_iso = dt_util.utcnow().isoformat()
        return {
            "date": day_key,
            "started_at": now_iso,
            "updated_at": now_iso,
            "change_events": 0,
            "entities": {},
        }

    def _get_entity_rollup(
        self, day: dict[str, Any], state: State
    ) -> dict[str, Any]:
        """Return an entity rollup, creating it if needed."""
        entities = day.setdefault("entities", {})
        entity_id = state.entity_id
        if entity_id not in entities:
            entities[entity_id] = {
                "domain": entity_id.split(".", 1)[0],
                "name": state.attributes.get(ATTR_FRIENDLY_NAME, state.name),
                "device_class": state.attributes.get(ATTR_DEVICE_CLASS),
                "first_seen": None,
                "last_seen": None,
                "observations": 0,
                "changes": 0,
                "transitions": {},
                "unavailable_count": 0,
                "unknown_count": 0,
                "numeric": {
                    "count": 0,
                    "sum": 0.0,
                    "min": None,
                    "max": None,
                    "last": None,
                    "max_delta": 0.0,
                    "large_jump_count": 0,
                },
            }
        return entities[entity_id]

    def _prune_days(self) -> None:
        """Prune old rollups."""
        max_days = self._option(CONF_MAX_DAYS, DEFAULT_MAX_DAYS)
        days = self._data.setdefault("days", {})
        keep = set(sorted(days)[-max_days:])
        for day_key in list(days):
            if day_key not in keep:
                days.pop(day_key, None)

    def _report_time_parts(self) -> tuple[int, int]:
        """Return configured report time as hour/minute."""
        report_time = self._option(CONF_REPORT_TIME, DEFAULT_REPORT_TIME)
        hour, minute = report_time.split(":", 1)
        return int(hour), int(minute)

    def _option(self, key: str, default: Any) -> Any:
        """Return one option value."""
        return self.options.get(key, default)

    def _today(self) -> str:
        """Return the local date string for today."""
        return dt_util.now().date().isoformat()

    def _default_report_date(self) -> str:
        """Return the default report date."""
        return (dt_util.now().date() - timedelta(days=1)).isoformat()

    def _notify_listeners(self) -> None:
        """Notify HA entities backed by this manager."""
        for listener in list(self._listeners):
            listener()


def _as_float(value: str) -> float | None:
    """Convert a state string to a finite float."""
    if value in UNKNOWN_STATES:
        return None
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    if not math.isfinite(number):
        return None
    return number


def _safe_attribute_value(name: str, value: Any) -> str | float | int | bool | None:
    """Return one explicitly opted-in scalar attribute, excluding secrets."""
    if not isinstance(name, str) or name.lower() in SENSITIVE_ATTRIBUTE_NAMES:
        return None
    if isinstance(value, float) and not math.isfinite(value):
        return None
    if isinstance(value, (str, int, float, bool)) and not isinstance(value, complex):
        if isinstance(value, str) and len(value) > 4096:
            return value[:4096]
        return value
    return None


def _round(value: float | int | None, digits: int = 2) -> float | None:
    """Round a number for JSON output."""
    if value is None:
        return None
    return round(float(value), digits)


def _top_items(items: dict[str, int], limit: int) -> list[dict[str, Any]]:
    """Return top dict items as JSON-friendly rows."""
    return [
        {"value": key, "count": value}
        for key, value in sorted(items.items(), key=lambda item: item[1], reverse=True)[
            :limit
        ]
    ]


def _date_range(end_date: str, days: int) -> list[str]:
    """Return date strings ending at end_date."""
    end = date.fromisoformat(end_date)
    return [
        (end - timedelta(days=offset)).isoformat()
        for offset in range(days - 1, -1, -1)
    ]


def _split_message(message: str, max_length: int = 4000) -> list[str]:
    """Split long notification text without silently dropping any content."""
    if len(message) <= max_length:
        return [message]
    chunks: list[str] = []
    current = ""
    for line in message.splitlines(keepends=True):
        if len(line) > max_length:
            if current:
                chunks.append(current)
                current = ""
            for start in range(0, len(line), max_length):
                chunks.append(line[start : start + max_length])
            continue
        if current and len(current) + len(line) > max_length:
            chunks.append(current)
            current = ""
        current += line
    if current:
        chunks.append(current)
    return chunks or [message]
