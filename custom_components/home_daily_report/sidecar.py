"""HTTP client for the optional Homekeeper analytics sidecar."""

from __future__ import annotations

import asyncio
from typing import Any
import uuid

import aiohttp

from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .const import CONF_SIDECAR_TIMEOUT, CONF_SIDECAR_TOKEN, CONF_SIDECAR_URL

CONTRACT_MAJOR = "1"
HEALTH_PATH = "/api/v1/health"
INGEST_EVENTS_PATH = "/api/v1/ingest/events"
INGEST_HEARTBEAT_PATH = "/api/v1/ingest/heartbeat"


class SidecarError(Exception):
    """A sidecar request failed without exposing credentials."""

    def __init__(self, code: str, message: str) -> None:
        """Initialize the sidecar error."""
        super().__init__(message)
        self.code = code


class SidecarClient:
    """Call the sidecar REST API using HA's shared HTTP session."""

    def __init__(self, hass: HomeAssistant, options: dict[str, Any]) -> None:
        """Initialize the client from config entry options."""
        self._session = async_get_clientsession(hass)
        self._base_url = str(options.get(CONF_SIDECAR_URL, "")).rstrip("/")
        self._token = str(options.get(CONF_SIDECAR_TOKEN, ""))
        self._timeout = float(options.get(CONF_SIDECAR_TIMEOUT, 10))

    @property
    def configured(self) -> bool:
        """Return whether a sidecar URL has been configured."""
        return bool(self._base_url)

    def _build_headers(self) -> dict[str, str]:
        """Build request headers with auth and contract version."""
        headers = {
            "X-Request-ID": uuid.uuid4().hex,
            "X-Homekeeper-Contract-Version": CONTRACT_MAJOR,
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        return headers

    async def async_health(self) -> dict[str, Any]:
        """Return sidecar health or raise a safe, classified error."""
        if not self.configured:
            raise SidecarError("not_configured", "sidecar URL is not configured")

        headers = self._build_headers()
        try:
            async with self._session.get(
                f"{self._base_url}{HEALTH_PATH}",
                headers=headers,
                timeout=aiohttp.ClientTimeout(total=self._timeout),
            ) as response:
                try:
                    payload = await response.json(content_type=None)
                except (TypeError, ValueError) as err:
                    raise SidecarError(
                        "invalid_response", "sidecar returned invalid JSON"
                    ) from err
                if response.status != 200:
                    raise SidecarError(
                        "not_ready", f"sidecar health returned HTTP {response.status}"
                    )
                if not isinstance(payload, dict):
                    raise SidecarError("invalid_response", "sidecar health is not an object")
                return payload
        except SidecarError:
            raise
        except asyncio.TimeoutError as err:
            raise SidecarError("timeout", "sidecar health request timed out") from err
        except aiohttp.ClientError as err:
            raise SidecarError("connection", "sidecar health request failed") from err

    async def async_ingest_events(self, batch: dict[str, Any]) -> dict[str, Any]:
        """Send an event batch to the sidecar."""
        if not self.configured:
            raise SidecarError("not_configured", "sidecar URL is not configured")

        headers = self._build_headers()
        try:
            async with self._session.post(
                f"{self._base_url}{INGEST_EVENTS_PATH}",
                json=batch,
                headers=headers,
                timeout=aiohttp.ClientTimeout(total=self._timeout),
            ) as response:
                try:
                    payload = await response.json(content_type=None)
                except (TypeError, ValueError) as err:
                    raise SidecarError(
                        "invalid_response", "sidecar returned invalid JSON"
                    ) from err

                if response.status == 200:
                    if not isinstance(payload, dict):
                        raise SidecarError("invalid_response", "ingest result is not an object")
                    return payload

                code = "invalid_response"
                msg = f"sidecar returned HTTP {response.status}"
                if isinstance(payload, dict):
                    code = payload.get("code", code)
                    msg = payload.get("message", msg)

                if response.status == 400:
                    raise SidecarError(code or "invalid_payload", msg)
                if response.status == 401:
                    raise SidecarError("unauthorized", msg)
                if response.status == 409:
                    raise SidecarError("unsupported_contract", msg)
                if response.status == 413:
                    raise SidecarError("body_too_large", msg)
                if response.status == 429:
                    raise SidecarError("busy", msg)
                if response.status in (500, 503):
                    raise SidecarError("unavailable", msg)
                raise SidecarError(code, msg)
        except SidecarError:
            raise
        except asyncio.TimeoutError as err:
            raise SidecarError("timeout", "sidecar ingest events request timed out") from err
        except aiohttp.ClientError as err:
            raise SidecarError("connection", "sidecar ingest events request failed") from err

    async def async_ingest_heartbeat(self, heartbeat: dict[str, Any]) -> dict[str, Any]:
        """Send a heartbeat to the sidecar."""
        if not self.configured:
            raise SidecarError("not_configured", "sidecar URL is not configured")

        headers = self._build_headers()
        try:
            async with self._session.post(
                f"{self._base_url}{INGEST_HEARTBEAT_PATH}",
                json=heartbeat,
                headers=headers,
                timeout=aiohttp.ClientTimeout(total=self._timeout),
            ) as response:
                try:
                    payload = await response.json(content_type=None)
                except (TypeError, ValueError) as err:
                    raise SidecarError(
                        "invalid_response", "sidecar returned invalid JSON"
                    ) from err

                if response.status == 200:
                    if not isinstance(payload, dict):
                        raise SidecarError("invalid_response", "heartbeat result is not an object")
                    return payload

                code = "invalid_response"
                msg = f"sidecar returned HTTP {response.status}"
                if isinstance(payload, dict):
                    code = payload.get("code", code)
                    msg = payload.get("message", msg)

                if response.status == 400:
                    raise SidecarError(code or "invalid_payload", msg)
                if response.status == 401:
                    raise SidecarError("unauthorized", msg)
                if response.status == 409:
                    raise SidecarError("unsupported_contract", msg)
                if response.status in (500, 503):
                    raise SidecarError("unavailable", msg)
                raise SidecarError(code, msg)
        except SidecarError:
            raise
        except asyncio.TimeoutError as err:
            raise SidecarError("timeout", "sidecar heartbeat request timed out") from err
        except aiohttp.ClientError as err:
            raise SidecarError("connection", "sidecar heartbeat request failed") from err

