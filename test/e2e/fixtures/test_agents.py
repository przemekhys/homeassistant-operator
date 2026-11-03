"""Offline tests for fixture storage only; real HA compatibility is tested by E2E."""

import importlib.util
import json
from dataclasses import asdict, dataclass
from pathlib import Path
import sys
import tempfile
import types
import unittest


class BackupAgentError(Exception):
    pass


class BackupNotFound(BackupAgentError):
    pass


@dataclass
class AgentBackup:
    backup_id: str
    protected: bool = True
    size: int = 0

    def as_dict(self):
        return asdict(self)

    @classmethod
    def from_dict(cls, data):
        return cls(**data)


for name in ("homeassistant", "homeassistant.components", "homeassistant.components.backup",
             "homeassistant.components.backup.agent", "homeassistant.components.backup.models"):
    sys.modules[name] = types.ModuleType(name)
sys.modules["homeassistant.components.backup.agent"].BackupAgent = object
models = sys.modules["homeassistant.components.backup.models"]
models.AgentBackup, models.BackupAgentError, models.BackupNotFound = AgentBackup, BackupAgentError, BackupNotFound
spec = importlib.util.spec_from_file_location("fixture_agents", Path(__file__).parent / "backup_test/backup.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class FixtureTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.state = {"fail_b": False}
        self.a = module.FixtureAgent("agent-a", self.temp.name, self.state)
        self.b = module.FixtureAgent("agent-b", self.temp.name, self.state)

    async def upload(self, agent, protected=True):
        async def chunks():
            for value in (b"opaque native ", b"archive bytes"):
                yield value

        async def open_stream():
            return chunks()

        await agent.async_upload_backup(open_stream=open_stream, backup=AgentBackup("archive", protected),
                                       on_progress=lambda **kwargs: None)

    async def test_round_trip_preserves_native_bytes_and_separates_agents(self):
        await self.upload(self.a)
        self.assertEqual(len(await self.a.async_list_backups()), 1)
        self.assertEqual(await self.b.async_list_backups(), [])
        stream = await self.a.async_download_backup("archive")
        self.assertEqual(b"".join([chunk async for chunk in stream]), b"opaque native archive bytes")
        self.assertEqual(self.a.root, Path(self.temp.name) / "agent-a")
        metadata = await self.a.async_get_backup("archive")
        self.assertTrue(metadata.protected)
        await self.a.async_delete_backup("archive")
        with self.assertRaises(BackupNotFound):
            await self.a.async_get_backup("archive")

    async def test_failure_does_not_write_or_fallback(self):
        self.state["fail_b"] = True
        with self.assertRaises(BackupAgentError):
            await self.upload(self.b)
        self.assertEqual(await self.a.async_list_backups(), [])
        self.assertEqual(await self.b.async_list_backups(), [])
        self.assertFalse(self.b.root.exists())

    async def test_unprotected_and_unsafe_ids_are_rejected(self):
        with self.assertRaises(BackupAgentError):
            await self.upload(self.a, protected=False)
        with self.assertRaises(BackupNotFound):
            await self.a.async_get_backup("../other")

    async def test_interrupted_upload_is_not_published(self):
        async def chunks():
            yield b"incomplete native stream"
            raise OSError("injected stream interruption")

        async def open_stream():
            return chunks()

        with self.assertRaises(OSError):
            await self.a.async_upload_backup(
                open_stream=open_stream, backup=AgentBackup("archive"),
                on_progress=lambda **kwargs: None,
            )
        self.assertEqual(await self.a.async_list_backups(), [])
        with self.assertRaises(BackupNotFound):
            await self.a.async_get_backup("archive")

    async def test_corrupt_metadata_identity_is_rejected(self):
        await self.upload(self.a)
        path = self.a.root / "archive.json"
        data = json.loads(path.read_text())
        data["backup_id"] = "other"
        path.write_text(json.dumps(data))
        with self.assertRaises(BackupAgentError):
            await self.a.async_get_backup("archive")


if __name__ == "__main__":
    unittest.main()
