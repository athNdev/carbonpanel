#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline checks for the bounded OSS-246 fault-seam fixture helpers."""
import os
import tempfile
import unittest
from pathlib import Path

from scripts import oss246_fault_seam_rehearsal as f


class ShimChecks(unittest.TestCase):
    @unittest.skipIf(os.name == "nt", "POSIX PATH shims are executed by the WSL/Linux rehearsal")
    def test_shim_fails_tools_and_records_invocation(self):
        with tempfile.TemporaryDirectory() as tmp:
            workspace = Path(tmp)
            env, marker = f.make_shim(workspace, "unit")
            self.assertTrue(env["PATH"].startswith(str(workspace / "shim-unit")))
            for name in ("sqlite3", "python3", "docker"):
                path = workspace / "shim-unit" / name
                self.assertTrue(path.is_file())
                self.assertTrue(os.access(path, os.X_OK))
            # Run the shimmed sqlite3 directly and confirm refusal + record.
            code, _ = f.run(["sqlite3", "select 1"], env=env, cwd=workspace)
            self.assertNotEqual(code, 0)
            self.assertIn("sqlite3", marker.read_text(encoding="utf-8"))

    def test_fixture_hashes_detect_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "carbon-panel.db").write_bytes(b"old-db")
            (root / "world").mkdir()
            (root / "world" / "level.dat").write_bytes(b"level")
            first = f.fixture_hashes(root)
            self.assertEqual(len(first), 2)
            (root / "world" / "level.dat").write_bytes(b"changed")
            self.assertNotEqual(f.fixture_hashes(root), first)


if __name__ == "__main__":
    unittest.main()
