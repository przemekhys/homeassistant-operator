"""Local E2E-only backup-agent fixture."""

from __future__ import annotations

from homeassistant.core import HomeAssistant
from homeassistant.helpers.typing import ConfigType


async def async_setup(_: HomeAssistant, __: ConfigType) -> bool:
    """Load the fixture when the E2E configuration explicitly enables it."""
    return True
