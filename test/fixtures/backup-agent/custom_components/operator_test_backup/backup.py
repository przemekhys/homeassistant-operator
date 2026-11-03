"""A deterministic, local-only backup agent used by the E2E suite.

This component intentionally has no external dependencies or credentials.  It
allows the suite to exercise a destination unknown to the operator's defaults.
"""

DOMAIN = "operator_test_backup"
AGENT_ID = "operator_test_backup.fixture"


async def async_setup_backup(hass):
    """Expose fixture state for the E2E harness without installing a provider."""
    hass.data.setdefault(DOMAIN, {"agent_id": AGENT_ID, "outcome": "success"})
    return True
