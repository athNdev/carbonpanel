# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Disposable stdio smoke for the smoke driver itself.

The product MCP binary is owned by the core lane and is not available here.
This test starts an owned mock stdio MCP server (read-only tool list matching
the v1.0.40 stable set), drives it with scripts/mcp_stdio_smoke.py and asserts
the driver's success and failure paths. It proves the smoke procedure works;
it is NOT evidence that the product image or a real panel was exercised.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import unittest
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import mcp_stdio_smoke  # noqa: E402

MOCK_SERVER = r'''
import json, os, sys
read_only = ["list_servers", "get_server", "list_files",
             "get_file", "list_backups"]
if os.environ.get("MCP_FIXTURE_NOISE"):
    print("diagnostic accidentally written to stdout", flush=True)
for line in sys.stdin:
    try:
        msg = json.loads(line)
    except Exception:
        continue
    method = msg.get("method")
    if method == "initialize":
        out = {"jsonrpc":"2.0","id":msg.get("id"),"result":{
            "protocolVersion":"2025-06-18",
            "serverInfo":{"name":"fixture-mcp","version":"v0.0.0-fixture"}}}
    elif method == "tools/list":
        out = {"jsonrpc":"2.0","id":msg.get("id"),"result":{
            "tools":[{"name":name,"description":"fixture read-only tool"} for name in read_only]}}
    elif method == "tools/call":
        out = {"jsonrpc":"2.0","id":msg.get("id"),"result":{
            "content":[{"type":"text","text":"fixture read-only call ok"}],
            "isError":False}}
    else:
        continue
    print(json.dumps(out, separators=(",", ":")), flush=True)
'''


def fixture_command(extra_env: dict[str, str] | None = None) -> tuple[list[str], dict[str, str]]:
    env = dict(os.environ)
    env.update(extra_env or {})
    return [sys.executable, "-c", MOCK_SERVER], env


class StdioSmokeDriverTests(unittest.TestCase):
    def test_real_stdio_exchange_with_read_only_fixture(self):
        command, env = fixture_command()
        report = mcp_stdio_smoke.run_smoke(command, env, expect_tool="list_servers")
        self.assertEqual(report.get("status"), "ok", report)
        self.assertIn("list_servers", report.get("tool_names", []))
        self.assertEqual(report.get("exit_code"), 0)
        self.assertNotIn("list_players", report.get("tool_names", []))

    def test_missing_new_capability_tool_fails_honestly(self):
        command, env = fixture_command()
        report = mcp_stdio_smoke.run_smoke(command, env, expect_tool="list_players")
        self.assertEqual(report.get("status"), "failed")
        self.assertIn("missing from tools/list", report.get("error", ""))

    def test_stdout_noise_is_detected(self):
        command, env = fixture_command({"MCP_FIXTURE_NOISE": "1"})
        report = mcp_stdio_smoke.run_smoke(command, env, expect_tool="list_servers")
        self.assertEqual(report.get("status"), "failed")

    def test_scoped_readonly_tool_call(self):
        command, env = fixture_command()
        report = mcp_stdio_smoke.run_smoke(
            command, env, expect_tool="list_servers",
            call_tool="list_servers", tool_arguments={})
        self.assertEqual(report.get("status"), "ok", report)
        self.assertEqual(report["tool_call"]["name"], "list_servers")
        self.assertFalse(report["tool_call"]["isError"])
        self.assertIn("read-only call ok", " ".join(report["tool_call"]["content_preview"]))


def evidence_payload() -> dict[str, Any]:
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(StdioSmokeDriverTests)
    result = unittest.TextTestRunner(verbosity=0).run(suite)
    command, _env = fixture_command()
    report = mcp_stdio_smoke.run_smoke(
        command, dict(os.environ), expect_tool="list_servers",
        call_tool="list_servers", tool_arguments={})
    return {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "fixture": "owned in-process mock stdio MCP server; no product binary, image or panel",
        "status": "ok" if result.wasSuccessful() else "failed",
        "checks": {
            "tests_run": result.testsRun,
            "failures": [str(test) for test, _ in result.failures],
            "errors": [str(test) for test, _ in result.errors],
            "handshake_report": report,
            "cleanup": "mock process received stdin EOF and exited; subprocess killed on failure",
        },
        "limits": "Validates the stdio smoke driver/procedure only. No GHCR image was built or "
                  "pushed, no product MCP binary exists in this worktree, and no live panel was contacted.",
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path)
    args = parser.parse_args(argv)
    payload = evidence_payload()
    rendered = json.dumps(payload, indent=2, sort_keys=True)
    print(rendered)
    if args.evidence:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(rendered + "\n", encoding="utf-8")
    return 0 if payload["status"] == "ok" else 1


if __name__ == "__main__":
    raise SystemExit(main())
