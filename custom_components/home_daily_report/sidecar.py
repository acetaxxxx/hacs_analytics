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


class SidecarError(Exception):
    """A sidecar request failed without exposing credentials."""

    def __init__(self, code: str, message: str) -> None:
        """Initialize the sidecar error."""
        super().__init__(message)
        self.code = code


class SidecarClient:
    """Call the sidecar health endpoint using HA's shared HTTP session."""

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

    async def async_health(self) -> dict[str, Any]:
        """Return sidecar health or raise a safe, classified error."""
        if not self.configured:
            raise SidecarError("not_configured", "sidecar URL is not configured")

        headers = {
            "X-Request-ID": uuid.uuid4().hex,
            "X-Homekeeper-Contract-Version": CONTRACT_MAJOR,
        }
        # Health is intentionally public for container probes, but retaining
        # the token here makes the client ready for protected API calls in
        # later tickets without ever logging or returning it.
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"

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
