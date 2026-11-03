"""Local E2E-only backup-agent fixture."""

from .backup import async_setup_backup


async def async_setup(hass, config):
    """Register the fixture only when copied into an E2E Home Assistant config."""
    return await async_setup_backup(hass)
