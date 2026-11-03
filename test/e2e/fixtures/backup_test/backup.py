"""Native HA BackupAgent fixtures with opaque, local, disposable storage.

HA supplies the encrypted archive. This fixture only copies its byte stream;
it neither creates/opens tar archives nor implements encryption or restore.
"""

import asyncio
import json
from dataclasses import replace
from pathlib import Path
import re

from homeassistant.components.backup.agent import BackupAgent
from homeassistant.components.backup.models import AgentBackup, BackupAgentError, BackupNotFound


class FixtureAgent(BackupAgent):
    domain = "backup_test"

    def __init__(self, unique_id, root, state):
        self.unique_id = unique_id
        self.name = f"Local fixture {unique_id}"
        self.root = Path(root) / unique_id
        self.state = state

    def _paths(self, backup_id):
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", backup_id):
            raise BackupNotFound("fixture archive is unavailable")
        return self.root / f"{backup_id}.tar", self.root / f"{backup_id}.json"

    async def async_upload_backup(self, *, open_stream, backup, on_progress, **kwargs):
        if not backup.protected:
            raise BackupAgentError("fixture requires a protected archive")
        if self.unique_id == "agent-b" and self.state["fail_b"]:
            raise BackupAgentError("injected fixture write failure")
        archive, metadata = self._paths(backup.backup_id)
        await asyncio.to_thread(self.root.mkdir, parents=True, exist_ok=True)
        pending = archive.with_suffix(".part")
        output = await asyncio.to_thread(pending.open, "wb")
        size = 0
        try:
            async for chunk in await open_stream():
                await asyncio.to_thread(output.write, chunk)
                size += len(chunk)
                on_progress(bytes_uploaded=size)
        finally:
            await asyncio.to_thread(output.close)
        await asyncio.to_thread(pending.replace, archive)
        document = json.dumps(replace(backup, size=size).as_dict())
        pending_metadata = metadata.with_suffix(".json.part")
        await asyncio.to_thread(pending_metadata.write_text, document, encoding="utf-8")
        await asyncio.to_thread(pending_metadata.replace, metadata)

    async def async_get_backup(self, backup_id, **kwargs):
        archive, metadata = self._paths(backup_id)
        try:
            document = await asyncio.to_thread(metadata.read_text, encoding="utf-8")
            if not await asyncio.to_thread(archive.is_file):
                raise FileNotFoundError
            result = AgentBackup.from_dict(json.loads(document))
            if result.backup_id != backup_id:
                raise BackupAgentError("fixture metadata identity mismatch")
            return result
        except FileNotFoundError:
            raise BackupNotFound("fixture archive is unavailable") from None

    async def async_list_backups(self, **kwargs):
        paths = await asyncio.to_thread(lambda: sorted(self.root.glob("*.json")))
        return [await self.async_get_backup(path.stem) for path in paths]

    async def async_download_backup(self, backup_id, **kwargs):
        await self.async_get_backup(backup_id)
        archive, _ = self._paths(backup_id)
        source = await asyncio.to_thread(archive.open, "rb")

        async def chunks():
            try:
                while data := await asyncio.to_thread(source.read, 65536):
                    yield data
            finally:
                await asyncio.to_thread(source.close)

        return chunks()

    async def async_delete_backup(self, backup_id, **kwargs):
        await self.async_get_backup(backup_id)
        archive, metadata = self._paths(backup_id)
        await asyncio.to_thread(metadata.unlink)
        await asyncio.to_thread(archive.unlink)


async def async_get_backup_agents(hass, **kwargs):
    state = hass.data.setdefault("backup_test", {"fail_b": False})
    return [FixtureAgent(name, "/test-backups", state) for name in ("agent-a", "agent-b")]
