#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Drive a real stdio MCP handshake against a binary or an OCI image.

Reads newline-delimited JSON-RPC from stdout only; all diagnostics from the
child are captured from stderr. This does not contact a panel: it verifies
that the artifact starts, speaks MCP over stdio, lists tools and exits cleanly
on stdin EOF. Read-only tool calls against a fixture belong in the runbook.
"""

from __future__ import annotations

import argparse
import json
import os
import queue
import subprocess
import sys
import threading
from pathlib import Path
from typing import Any


def _reader(stream: Any, lines: "queue.Queue[str]") -> None:
    for line in stream:
        lines.put(line)
    lines.put(None)  # type: ignore[arg-type]


def _stderr_reader(stream: Any, chunks: list[str]) -> None:
    for line in stream:
        chunks.append(line)


def run_smoke(command: list[str], env: dict[str, str], expect_tool: str,
              timeout: float = 20.0) -> dict[str, Any]:
    if not command:
        raise ValueError("a command is required")

    process = subprocess.Popen(
        command,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        bufsize=1,
    )
    assert process.stdin is not None
    assert process.stdout is not None
    assert process.stderr is not None

    stdout_lines: "queue.Queue[str]" = queue.Queue()
    stderr_chunks: list[str] = []
    thread = threading.Thread(target=_reader, args=(process.stdout, stdout_lines), daemon=True)
    thread.start()
    stderr_thread = threading.Thread(target=_stderr_reader, args=(process.stderr, stderr_chunks), daemon=True)
    stderr_thread.start()

    def send(payload: dict[str, Any]) -> None:
        process.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
        process.stdin.flush()

    def response(request_id: int) -> dict[str, Any]:
        while True:
            line = stdout_lines.get(timeout=timeout)
            if line is None:
                raise RuntimeError("stdio closed before a JSON-RPC response arrived")
            if not line.strip():
                continue
            message = json.loads(line)
            if message.get("id") == request_id:
                return message

    report: dict[str, Any] = {"command": command, "expect_tool": expect_tool}
    try:
        send({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "carbon-panel-mcp-smoke", "version": "1.0.0"},
            },
        })
        init = response(1)
        if "result" not in init:
            raise RuntimeError(f"initialize did not return a result: {init}")
        report["initialize"] = {
            "protocolVersion": init["result"].get("protocolVersion"),
            "serverInfo": init["result"].get("serverInfo"),
        }

        send({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        listed = response(2)
        tools = listed.get("result", {}).get("tools", [])
        if not isinstance(tools, list):
            raise RuntimeError("tools/list did not return a tools array")
        names = sorted(tool.get("name", "") for tool in tools if isinstance(tool, dict))
        report["tool_names"] = names
        if expect_tool and expect_tool not in names:
            raise RuntimeError(f"expected tool {expect_tool!r} missing from tools/list: {names}")

        process.stdin.close()
        code = process.wait(timeout=timeout)
        report["exit_code"] = code
        if code != 0:
            raise RuntimeError(f"stdio server exited with {code}")

        stderr_thread.join(timeout=timeout)
        report["stderr_bytes"] = sum(len(chunk.encode("utf-8", errors="replace")) for chunk in stderr_chunks)
        report["status"] = "ok"
    except Exception as error:  # noqa: BLE001 - report every failure honestly
        report["status"] = "failed"
        report["error"] = str(error)
        try:
            process.kill()
        except OSError:
            pass
        process.wait(timeout=timeout)
        return report
    finally:
        thread.join(timeout=1)
        stderr_thread.join(timeout=1)
        for stream in (process.stdin, process.stdout, process.stderr):
            try:
                stream.close()
            except OSError:
                pass

    return report


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expect-tool", default="")
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--url", default="http://127.0.0.1:1")
    parser.add_argument("--token", default="stdio-smoke-placeholder")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    command = args.command[1:] if args.command and args.command[0] == "--" else args.command

    env = dict(os.environ)
    env["CARBONPANEL_URL"] = args.url
    env["CARBONPANEL_TOKEN"] = args.token
    env["CARBONPANEL_MCP_TRANSPORT"] = "stdio"

    try:
        report = run_smoke(command, env, args.expect_tool)
    except (ValueError, subprocess.SubprocessError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    rendered = json.dumps(report, indent=2, sort_keys=True)
    print(rendered)
    if args.evidence:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(rendered + "\n", encoding="utf-8")
    return 0 if report.get("status") == "ok" else 1


if __name__ == "__main__":
    raise SystemExit(main())
