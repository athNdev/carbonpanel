#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Build-only packaged stdio smoke for the MCP OCI image.

Builds the MCP image and the disposable mock fixture locally (no push/pull),
runs the packaged container on an isolated Docker network against the owned
fixture, and drives a real initialize/tools.list/scoped read-only tools/call
through the stdio protocol. This mirrors the publish workflow's packaged smoke
without registry writes or a live panel.
"""

from __future__ import annotations

import argparse
import json
import shlex
import shutil
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent


def run(cmd: list[str], *, check: bool = True, capture: bool = False, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    print("+ " + " ".join(shlex.quote(c) for c in cmd), flush=True)
    return subprocess.run(
        cmd,
        cwd=str(cwd or ROOT),
        check=check,
        text=True,
        capture_output=capture,
    )


def docker_available() -> tuple[bool, str]:
    if shutil.which("docker") is None:
        return False, "docker CLI not found"
    probe = subprocess.run(
        ["docker", "info", "--format", "{{.ServerVersion}}"],
        cwd=str(ROOT),
        text=True,
        capture_output=True,
    )
    if probe.returncode != 0:
        detail = (probe.stderr or probe.stdout or "docker daemon unavailable").strip()
        return False, detail
    return True, (probe.stdout or "unknown").strip()


def git_value(args: list[str], default: str) -> str:
    proc = subprocess.run(["git", *args], cwd=str(ROOT), text=True, capture_output=True)
    if proc.returncode != 0:
        return default
    return (proc.stdout or "").strip() or default


def cleanup(*, fixture: str | None, network: str | None) -> list[str]:
    steps: list[str] = []
    if fixture:
        cp = run(["docker", "rm", "-f", fixture], check=False, capture=True)
        steps.append(f"docker rm -f {fixture}: rc={cp.returncode}")
    if network:
        cp = run(["docker", "network", "rm", network], check=False, capture=True)
        steps.append(f"docker network rm {network}: rc={cp.returncode}")
    return steps


def wait_ready(fixture: str, timeout_s: int) -> bool:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        probe = subprocess.run(
            [
                "docker", "exec", fixture, "python", "-c",
                "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/ready', timeout=2)",
            ],
            cwd=str(ROOT),
            text=True,
            capture_output=True,
        )
        if probe.returncode == 0:
            return True
        time.sleep(1)
    return False


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image-tag", default="", help="Local tag for the built MCP image")
    parser.add_argument("--mock-tag", default="", help="Local tag for the built mock fixture image")
    parser.add_argument("--network", default="", help="Isolated Docker network name")
    parser.add_argument("--fixture-name", default="", help="Fixture container name")
    parser.add_argument("--platform", default="", help="Docker platform to build (default: host)")
    parser.add_argument("--evidence", type=Path, default=Path("mcp-build-only-packaged-smoke.json"))
    parser.add_argument("--fixture-log", type=Path, default=Path("mcp-build-only-fixture.log"))
    parser.add_argument("--ready-timeout", type=int, default=30)
    parser.add_argument("--skip-build", action="store_true", help="Reuse existing local images")
    parser.add_argument("--dry-run", action="store_true", help="Print planned actions and exit without Docker")
    args = parser.parse_args(argv)

    run_id = uuid.uuid4().hex[:12]
    image_tag = args.image_tag or f"carbonpanel-mcp:build-smoke-{run_id}"
    mock_tag = args.mock_tag or f"carbonpanel-mcp-mock:build-smoke-{run_id}"
    network = args.network or f"mcp-build-smoke-{run_id}"
    fixture_name = args.fixture_name or f"mcp-fixture-{run_id}"
    commit = git_value(["rev-parse", "HEAD"], "unknown")
    build_date = git_value(["show", "-s", "--format=%cI", "HEAD"], "unknown")
    version = git_value(["describe", "--tags", "--always", "--dirty"], "build-smoke")

    report: dict[str, Any] = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "mode": "build-only-packaged-smoke",
        "image_tag": image_tag,
        "mock_tag": mock_tag,
        "network": network,
        "fixture_container": fixture_name,
        "commit": commit,
        "limits": (
            "Local build-only smoke on an isolated Docker network with an owned "
            "disposable mock fixture. No GHCR push/pull, no live panel, no real "
            "credentials, no user data."
        ),
    }

    build_mcp_cmd = ["docker", "build", "-f", "docker/Dockerfile.mcp", "-t", image_tag]
    if args.platform:
        build_mcp_cmd += ["--platform", args.platform]
    build_mcp_cmd += [
        "--build-arg", f"MCP_ARTIFACT_VERSION={version}",
        "--build-arg", f"MCP_ARTIFACT_COMMIT={commit}",
        "--build-arg", f"MCP_ARTIFACT_BUILDDATE={build_date}",
        ".",
    ]
    build_mock_cmd = ["docker", "build", "-f", "docker/Dockerfile.mcp-mock", "-t", mock_tag, "."]
    run_fixture_cmd = [
        "docker", "run", "-d", "--name", fixture_name,
        "--network", network, "--network-alias", "panel-fixture", mock_tag,
    ]
    smoke_cmd = [
        sys.executable, "scripts/mcp_stdio_smoke.py",
        "--expect-tool", "list_servers",
        "--call-tool", "list_servers",
        "--url", "http://panel-fixture:8080",
        "--evidence", str(args.evidence),
        "--",
        "docker", "run", "-i", "--rm", "--network", network,
        "-e", "CARBONPANEL_URL", "-e", "CARBONPANEL_TOKEN",
        "-e", "CARBONPANEL_API_TOKEN", "-e", "CARBONPANEL_MCP_TRANSPORT",
        image_tag,
    ]
    report["planned_commands"] = {
        "build_mock": build_mock_cmd,
        "build_mcp": build_mcp_cmd,
        "create_network": ["docker", "network", "create", network],
        "run_fixture": run_fixture_cmd,
        "stdio_smoke": smoke_cmd,
    }

    if args.dry_run:
        report["status"] = "dry-run"
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0

    ok, detail = docker_available()
    report["docker"] = {"available": ok, "detail": detail}
    if not ok:
        report["status"] = "blocked"
        report["error"] = "Docker daemon unavailable for build-only packaged smoke"
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2, sort_keys=True))
        return 2

    cleanup_steps: list[str] = []
    fixture_started = False
    network_created = False
    try:
        if not args.skip_build:
            run(build_mock_cmd)
            run(build_mcp_cmd)
        run(["docker", "network", "create", network])
        network_created = True
        run(run_fixture_cmd)
        fixture_started = True
        if not wait_ready(fixture_name, args.ready_timeout):
            raise RuntimeError("fixture never became ready")
        cp = subprocess.run(smoke_cmd, cwd=str(ROOT), text=True)
        report["stdio_smoke_exit_code"] = cp.returncode
        if cp.returncode != 0:
            raise RuntimeError(f"stdio smoke failed with exit code {cp.returncode}")
        logs = subprocess.run(["docker", "logs", fixture_name], cwd=str(ROOT), text=True, capture_output=True)
        fixture_log_text = (logs.stdout or "") + (logs.stderr or "")
        args.fixture_log.parent.mkdir(parents=True, exist_ok=True)
        args.fixture_log.write_text(fixture_log_text, encoding="utf-8", errors="replace")
        for required in ("GetAuthStatus", "GetCurrentUser", "ListServers"):
            if required not in fixture_log_text:
                raise RuntimeError(f"fixture log missing expected request marker: {required}")
        report["fixture_log"] = str(args.fixture_log)
        report["fixture_markers"] = ["GetAuthStatus", "GetCurrentUser", "ListServers"]
        report["status"] = "ok"
    except Exception as error:  # noqa: BLE001 - report every failure honestly
        report["status"] = "failed"
        report["error"] = str(error)
    finally:
        cleanup_steps = cleanup(
            fixture=fixture_name if fixture_started else None,
            network=network if network_created else None,
        )
        report["cleanup"] = cleanup_steps

    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report.get("status") == "ok" else 1


if __name__ == "__main__":
    raise SystemExit(main())
