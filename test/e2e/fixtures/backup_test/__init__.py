"""Test-only agents; not shipped with or installed by the production operator."""


async def async_setup(hass, config):
    state = hass.data.setdefault("backup_test", {"fail_b": False})

    async def set_failure(call):
        enabled = call.data.get("enabled")
        if type(enabled) is not bool:
            raise ValueError("fixture failure switch requires a boolean")
        state["fail_b"] = enabled

    hass.services.async_register("backup_test", "set_failure", set_failure)
    return True
