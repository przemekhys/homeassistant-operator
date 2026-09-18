"""Zero-configuration config flow used by the community repository E2E test."""

from homeassistant import config_entries

from . import DOMAIN


class ExampleIntegrationConfigFlow(config_entries.ConfigFlow, domain=DOMAIN):
    """Create one fixture config entry without user input."""

    VERSION = 1

    async def async_step_user(self, user_input=None):
        """Create the fixture entry."""
        await self.async_set_unique_id(DOMAIN)
        self._abort_if_unique_id_configured()
        return self.async_create_entry(title="Example Integration", data={})
