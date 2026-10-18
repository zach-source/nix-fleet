"""Startup guards; real pinned-image application is checked during release."""

import hashlib
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

PATCHES = Path(__file__).resolve().parents[1] / "patches"
spec = importlib.util.spec_from_file_location(
    "hotfix", PATCHES / "hotfix-sparse-mla-barrier.py"
)
hotfix = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hotfix)


class StartupGuards(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        files = {
            name: (
                hashlib.sha256(b"old").hexdigest(),
                hashlib.sha256(b"fixed").hexdigest(),
            )
            for name in hotfix.FILES
        }
        for name in files:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"old")
        for item in [
            patch.object(hotfix, "ROOT", self.root),
            patch.object(hotfix, "FILES", files),
            patch.object(hotfix, "PATCH", PATCHES / "flashinfer-4732.patch"),
            patch.dict(hotfix.os.environ, {"FLASHINFER_WORKSPACE_BASE": hotfix.CACHE}),
        ]:
            item.start()
            self.addCleanup(item.stop)

    def apply_patch(self, command, **kwargs):
        self.assertEqual(
            command,
            [
                "patch",
                "--batch",
                "--forward",
                "--fuzz=0",
                "-p1",
                "-i",
                str(hotfix.PATCH),
            ],
        )
        self.assertEqual(kwargs, {"cwd": self.root, "check": True})
        for name in hotfix.FILES:
            (self.root / name).write_bytes(b"fixed")

    def test_apply_and_repeat_without_reapplying(self):
        with patch.object(
            hotfix.subprocess, "run", side_effect=self.apply_patch
        ) as run:
            hotfix.apply()
            hotfix.apply()
        self.assertEqual(run.call_count, 1)

    def test_changed_header_stops_before_any_write(self):
        (self.root / next(iter(hotfix.FILES))).write_bytes(b"unknown")
        with (
            patch.object(hotfix.subprocess, "run") as run,
            self.assertRaisesRegex(RuntimeError, "headers differ"),
        ):
            hotfix.apply()
        run.assert_not_called()

    def test_original_cache_is_rejected(self):
        with (
            patch.dict(
                hotfix.os.environ,
                {"FLASHINFER_WORKSPACE_BASE": "/cache/huggingface/flashinfer"},
            ),
            self.assertRaisesRegex(RuntimeError, "isolated kernel cache"),
        ):
            hotfix.apply()

    def test_changed_patch_stops_before_write(self):
        wrong = self.root / "changed.patch"
        wrong.write_bytes(b"unknown")
        with (
            patch.object(hotfix, "PATCH", wrong),
            patch.object(hotfix.subprocess, "run") as run,
            self.assertRaisesRegex(RuntimeError, "upstream patch changed"),
        ):
            hotfix.apply()
        run.assert_not_called()

    def test_patch_failure_propagates(self):
        with (
            patch.object(
                hotfix.subprocess,
                "run",
                side_effect=subprocess.CalledProcessError(1, "patch"),
            ),
            self.assertRaises(subprocess.CalledProcessError),
        ):
            hotfix.apply()

    def test_unexpected_patch_output_cannot_start_model(self):
        with (
            patch.object(hotfix.subprocess, "run"),
            self.assertRaisesRegex(RuntimeError, "patched header verification"),
        ):
            hotfix.apply()


if __name__ == "__main__":
    unittest.main()
