#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Scoped read-only stdio smoke for the ACTUAL carbonpanel-mcp binary.

Starts an owned mock Connect controller (scripts/mcp_mock_controller.py) on
127.0.0.1:0, then drives a real initialize, tools/list and one read-only
list_servers call through the compiled binary. The binary must verify auth
readiness before it serves stdio.

This is not a Docker image smoke: no daemon is available in the authoring
worktree, so the merged container entrypoint remains an outstanding gate.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import mcp_stdio_smoke  # noqa: E402
from mcp_mock_controller import OwnedController  # noqa: E402


def run(binary: Path, evidence: Path | None = None) -> dict[str, Any]:
    with OwnedController() as controller:
        env = dict(os.environ)
        env["CARBONPANEL_URL"] = controller.url
        env["CARBONPANEL_API_TOKEN"] = "dp_fixture_scoped_readonly"
        env["CARBONPANEL_TOKEN"] = env["CARBONPANEL_API_TOKEN"]
        report = mcp_stdio_smoke.run_smoke(
            [str(binary)], env, expect_tool="list_servers",
            call_tool="list_servers", tool_arguments={},
        )
        report["fixture_requests"] = controller.requests
        report["fixture_cleanup"] = "owned HTTP server shut down and closed"
    report["binary"] = str(binary)
    report["limits"] = (
        "Actual compiled binary with an owned Connect mock; no Docker image, "
        "no live panel, no user data, no real credentials."
    )
    rendered = json.dumps(report, indent=2, sort_keys=True)
    if evidence:
        evidence.parent.mkdir(parents=True, exist_ok=True)
        evidence.write_text(rendered + "\n", encoding="utf-8")
    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--evidence", type=Path)
    args = parser.parse_args(argv)
    if not args.binary.is_file():
        print(f"error: binary not found: {args.binary}", file=sys.stderr)
        return 1
    report = run(args.binary, args.evidence)
    print(json.dumps({
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "status": report.get("status"),
        "tool_call": report.get("tool_call"),
        "fixture_requests": report.get("fixture_requests"),
        "limits": report.get("limits"),
    }, indent=2, sort_keys=True))
    return 0 if report.get("status") == "ok" else 1


if __name__ == "__main__":
    raise SystemExit(main())
