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
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "mcp_build_only_packaged_smoke.py"
sys.path.insert(0, str(SCRIPT.parent))

import mcp_build_only_packaged_smoke as smoke  # noqa: E402


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
            self.assertIn("--internal", planned["create_network"])
            self.assertIn("--network-alias", planned["run_fixture"])
            self.assertIn("panel-fixture", planned["run_fixture"])
            smoke = planned["stdio_smoke"]
            self.assertIn("scripts/mcp_stdio_smoke.py", smoke)
            self.assertIn("--call-tool", smoke)
            self.assertIn("list_servers", smoke)
            self.assertIn("http://panel-fixture:8080", smoke)
            self.assertFalse(evidence.exists(), "dry-run must not write evidence")

    def test_report_keeps_stdio_evidence_and_uses_immutable_local_image(self) -> None:
        result, report, commands, stdio_command = self.run_fixture()
        self.assertEqual(result, 0)
        self.assertEqual(report["status"], "ok")
        self.assertEqual(report["stdio_smoke"]["tool_names"], ["list_servers"])
        self.assertFalse(report["stdio_smoke"]["tool_call"]["isError"])
        self.assertEqual(stdio_command[-1], report["image_id"])
        self.assertEqual(
            [command[-1] for command in commands if command[:3] == ["docker", "image", "rm"]],
            [report["mock_tag"], report["image_tag"]],
        )
        self.assertTrue(report["cleanup_ok"])

    def test_cleanup_failure_keeps_evidence_and_preserves_reused_images(self) -> None:
        result, report, commands, _ = self.run_fixture(
            "--skip-build", "--image-tag", "test:mcp", "--mock-tag", "test:mock", cleanup_code=1,
        )
        self.assertEqual(result, 1)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["stdio_smoke"]["status"], "ok")
        self.assertEqual(report["error"], "Docker smoke cleanup failed")
        self.assertFalse(report["cleanup_ok"])
        self.assertFalse(any(command[:3] == ["docker", "image", "rm"] for command in commands))
        self.assertFalse(any(command[:2] == ["docker", "build"] for command in commands))

    def run_fixture(self, *args: str, cleanup_code: int = 0) -> tuple[int, dict, list[list[str]], list[str]]:
        smoke_report = {
            "status": "ok",
            "tool_names": ["list_servers"],
            "tool_call": {"isError": False},
        }

        def docker(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
            if command[:3] == ["docker", "image", "inspect"]:
                return subprocess.CompletedProcess(command, 0, stdout="sha256:" + "a" * 64, stderr="")
            code = cleanup_code if command[:3] == ["docker", "network", "rm"] else 0
            return subprocess.CompletedProcess(command, code, stdout="", stderr="")

        def subprocess_command(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
            if command[:2] == ["docker", "logs"]:
                return subprocess.CompletedProcess(
                    command, 0, stdout="GetAuthStatus\nGetCurrentUser\nListServers\n", stderr="",
                )
            return subprocess.CompletedProcess(command, 0, stdout=json.dumps(smoke_report), stderr="")

        with tempfile.TemporaryDirectory() as tmp:
            evidence = Path(tmp) / "evidence.json"
            with (
                patch.object(smoke, "docker_available", return_value=(True, "test")),
                patch.object(smoke, "git_value", return_value="fixture"),
                patch.object(smoke, "wait_ready", return_value=True),
                patch.object(smoke, "run", side_effect=docker) as docker_run,
                patch.object(smoke.subprocess, "run", side_effect=subprocess_command) as process_run,
                patch("builtins.print"),
            ):
                result = smoke.main([
                    "--evidence", str(evidence), "--fixture-log", str(Path(tmp) / "fixture.log"), *args,
                ])
            report = json.loads(evidence.read_text(encoding="utf-8"))
            docker_commands = [call.args[0] for call in docker_run.call_args_list]
            stdio_command = process_run.call_args_list[0].args[0]
            return result, report, docker_commands, stdio_command


if __name__ == "__main__":
    unittest.main()
