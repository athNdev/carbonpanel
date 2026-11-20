#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Contract tests for the build-only packaged MCP smoke seam.

Validates the reusable script's dry-run plan and its honest blocked/failure
reporting when Docker is unavailable. No Docker daemon, image build, live panel
or real credential is required.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "mcp_build_only_packaged_smoke.py"


def run_script(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        cwd=str(ROOT),
        text=True,
        capture_output=True,
        check=False,
    )


class BuildOnlyPackagedSmokeScriptTests(unittest.TestCase):
    def test_dry_run_emits_packaged_smoke_plan(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            evidence = Path(tmp) / "dry.json"
            proc = run_script("--dry-run", "--evidence", str(evidence))
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            report = json.loads(proc.stdout)
            self.assertEqual(report["status"], "dry-run")
            self.assertEqual(report["mode"], "build-only-packaged-smoke")
            planned = report["planned_commands"]
            self.assertEqual(planned["build_mock"][:4], ["docker", "build", "-f", "docker/Dockerfile.mcp-mock"])
            self.assertEqual(planned["build_mcp"][:4], ["docker", "build", "-f", "docker/Dockerfile.mcp"])
            self.assertIn("--network-alias", planned["run_fixture"])
            self.assertIn("panel-fixture", planned["run_fixture"])
            smoke = planned["stdio_smoke"]
            self.assertIn("scripts/mcp_stdio_smoke.py", smoke)
            self.assertIn("--call-tool", smoke)
            self.assertIn("list_servers", smoke)
            self.assertIn("http://panel-fixture:8080", smoke)
            self.assertFalse(evidence.exists(), "dry-run must not write evidence")


if __name__ == "__main__":
    unittest.main()
