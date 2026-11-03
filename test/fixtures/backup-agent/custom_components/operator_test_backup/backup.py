"""A deterministic, local-only backup agent used by the E2E suite."""

from __future__ import annotations

from collections.abc import AsyncIterator, Callable, Coroutine
from typing import Any

from homeassistant.components.backup import (
    AgentBackup,
    BackupAgent,
    BackupAgentError,
    BackupNotFound,
)
from homeassistant.core import HomeAssistant

DOMAIN = "operator_test_backup"
AGENT_ID = "operator_test_backup.fixture"


async def async_get_backup_agents(_: HomeAssistant) -> list[BackupAgent]:
    """Return an agent whose upload outcome is controlled by the fixture."""
    return [OperatorTestBackupAgent()]


class OperatorTestBackupAgent(BackupAgent):
    """Keep uploaded backups in memory and optionally report a controlled error."""

    domain = DOMAIN

    def __init__(self) -> None:
        """Initialize the fixture agent."""
        super().__init__()
        self.name = self.unique_id = AGENT_ID
        self.fail_upload = False
        self._backups: dict[str, AgentBackup] = {}

    async def async_upload_backup(
        self,
        *,
        open_stream: Callable[[], Coroutine[Any, Any, AsyncIterator[bytes]]],
        backup: AgentBackup,
        **_: Any,
    ) -> None:
        """Record a backup or report the configured deterministic failure."""
        if self.fail_upload:
            raise BackupAgentError("fixture upload failure")
        self._backups[backup.backup_id] = backup

    async def async_download_backup(
        self, backup_id: str, **_: Any
    ) -> AsyncIterator[bytes]:
        """Return deterministic bytes for an uploaded fixture backup."""
        if backup_id not in self._backups:
            raise BackupNotFound(backup_id)

        async def bytes_iterator() -> AsyncIterator[bytes]:
            yield b"operator-test-backup"

        return bytes_iterator()

    async def async_list_backups(self, **_: Any) -> list[AgentBackup]:
        """Return backups retained by this process-local fixture."""
        return list(self._backups.values())

    async def async_delete_backup(self, backup_id: str, **_: Any) -> None:
        """Delete one retained fixture backup."""
        self._backups.pop(backup_id, None)

    async def async_get_backup(self, backup_id: str, **_: Any) -> AgentBackup:
        """Return metadata for one fixture backup."""
        try:
            return self._backups[backup_id]
        except KeyError as err:
            raise BackupNotFound(backup_id) from err
