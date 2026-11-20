# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Disposable fixture test for the copyable MCP onboarding instructions.

Binds an owned mock panel to 127.0.0.1 on an ephemeral port, exercises the
documented health and read-only paths, checks the README prompt's networking
and data-preservation wording, then shuts the fixture down. No live panel,
shared service, Docker daemon, world or database is contacted.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
import threading
import time
import unittest
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urljoin

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
GUIDE = ROOT / "docs/carbon-panel/src/content/docs/guides/mcp-server.mdx"
MUTATING = ("Create", "Update", "Delete", "Start", "Stop", "Restart", "Recreate", "Kick", "Ban")


def readme_section() -> str:
    text = README.read_text(encoding="utf-8")
    start = text.index("## AI assistant integration (MCP)")
    end = text.index("## Documentation and help", start)
    return text[start:end]


def fenced_blocks(section: str, language: str) -> list[str]:
    return re.findall(r"```" + re.escape(language) + r"\n(.*?)```", section, re.DOTALL)


class MockPanel:
    def __init__(self) -> None:
        state = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args: Any) -> None:
                return

            def _reply(self, status: int, body: dict[str, Any]) -> None:
                payload = json.dumps(body).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_GET(self) -> None:  # noqa: N802
                state.requests.append(("GET", self.path))
                if self.path == "/healthz":
                    self._reply(200, {"status": "ok", "uptime": "fixture"})
                elif self.path == "/readyz":
                    self._reply(200, {"status": "ready"})
                elif self.path == "/health/detail":
                    self._reply(200, {"layers": [
                        {"name": "process", "status": "healthy"},
                        {"name": "database", "status": "healthy"},
                        {"name": "game", "status": "unknown"},
                    ]})
                else:
                    self._reply(404, {"error": "not found"})

            def do_POST(self) -> None:  # noqa: N802
                state.requests.append(("POST", self.path))
                if self.path.endswith("ServerService/ListServers"):
                    self._reply(200, {"servers": []})
                else:
                    self._reply(404, {"error": "unimplemented on fixture"})

        self.requests: list[tuple[str, str]] = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.port = self.server.server_address[1]

    def __enter__(self) -> "MockPanel":
        self.thread.start()
        return self

    def __exit__(self, *_args: Any) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    @staticmethod
    def _open(opener: Any, target: Any) -> Any:
        # Windows can abort a just-established loopback connection; retry only
        # transport races, never HTTP status errors.
        transient = (ConnectionAbortedError, ConnectionResetError, ConnectionRefusedError, TimeoutError)
        for attempt in range(5):
            try:
                return opener.open(target, timeout=5)
            except transient:
                if attempt == 4:
                    raise
                time.sleep(0.05 * (attempt + 1))
        raise RuntimeError("unreachable")

    def get(self, path: str) -> tuple[int, dict[str, Any]]:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with self._open(opener, f"http://127.0.0.1:{self.port}{path}") as response:
            return response.status, json.loads(response.read().decode("utf-8"))

    def list_servers(self) -> tuple[int, dict[str, Any]]:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        request = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/carbonpanel.v1.ServerService/ListServers",
            data=b"{}",
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with self._open(opener, request) as response:
            return response.status, json.loads(response.read().decode("utf-8"))


class CopyablePromptTests(unittest.TestCase):
    def test_prompt_covers_all_required_instructions(self):
        section = readme_section()
        prompts = fenced_blocks(section, "text")
        self.assertTrue(prompts, "README must contain a copyable text prompt")
        prompt = " ".join(prompts[0].split())
        required = [
            "io.github.athNdev/carbon-panel",
            "Stop if the listing or anonymous pull is unavailable",
            "Preserve all existing services, worlds and databases",
            "Ask before changing the installed panel version or channel",
            "servers:read", "files:read", "backups:read",
            "reachable from the MCP container",
            "http://carbon-panel:8080",
            "no panel data mounts or Docker socket mounts",
            "error.kind=capability_unavailable",
            "minimum_panel_build",
            "/healthz", "/readyz", "/health/detail",
            "tools/list", "list_servers", "Stable", "sha256",
            "authentication-enabled", "CARBONPANEL_TOKEN", "dp_",
        ]
        for phrase in required:
            self.assertIn(phrase, prompt, f"copyable prompt is missing: {phrase}")

    def test_two_reachable_url_cases_are_distinct(self):
        configs = [json.loads(block) for block in fenced_blocks(GUIDE.read_text(encoding="utf-8"), "json")]
        configs = [config for config in configs if "mcpServers" in config]
        self.assertGreaterEqual(len(configs), 2)
        host = next(c for c in configs if c["mcpServers"]["carbon-panel"]["command"] != "docker")
        container = next(c for c in configs if c["mcpServers"]["carbon-panel"]["command"] == "docker")
        self.assertEqual(
            host["mcpServers"]["carbon-panel"]["env"]["CARBONPANEL_URL"],
            "http://127.0.0.1:8080",
        )
        self.assertEqual(
            container["mcpServers"]["carbon-panel"]["env"]["CARBONPANEL_URL"],
            "http://carbon-panel:8080",
        )
        self.assertIn("--network", container["mcpServers"]["carbon-panel"]["args"])
        self.assertIn("sha256:<64_HEX_DIGEST>", json.dumps(container))
        self.assertEqual(host["mcpServers"]["carbon-panel"]["args"], [])
        for config in configs:
            self.assertNotIn("http://localhost:8080", json.dumps(config))

    def test_compose_configuration_forwards_token_by_environment_name(self):
        blocks = fenced_blocks(GUIDE.read_text(encoding="utf-8"), "json")
        config = next(json.loads(block)["mcpServers"]["carbon-panel"] for block in blocks if '"docker"' in block)
        self.assertEqual(config["args"][:3], ["run", "-i", "--rm"])
        for variable in ("CARBONPANEL_URL", "CARBONPANEL_TOKEN"):
            index = config["args"].index(variable)
            self.assertEqual(config["args"][index - 1], "-e")
        self.assertNotIn(config["env"]["CARBONPANEL_TOKEN"], config["args"])

    def test_guide_links_resolve_inside_each_docs_route(self):
        guide = GUIDE.read_text(encoding="utf-8")
        for label, destination in (
            ("Docker Compose", "getting-started/docker-compose/"),
            ("Prebuilt binaries", "getting-started/prebuilt-binaries/"),
            ("Documentation version", "versions/"),
        ):
            link = re.search(rf"\[{label}\]\(([^)]+)\)", guide)
            self.assertIsNotNone(link, f"{label} link is missing")
            for root in ("/docs/", "/docs/versions/1.0.40/", "/docs/nightly/"):
                with self.subTest(label=label, root=root):
                    self.assertEqual(urljoin(f"{root}guides/mcp-server/", link[1]), root + destination)

    def test_disposable_mock_panel_fixture(self):
        with MockPanel() as panel:
            self.assertEqual(panel.get("/healthz")[0], 200)
            self.assertEqual(panel.get("/readyz")[0], 200)
            status, detail = panel.get("/health/detail")
            self.assertEqual(status, 200)
            states = {layer["name"]: layer["status"] for layer in detail["layers"]}
            self.assertEqual(states["game"], "unknown")
            self.assertEqual(panel.list_servers(), (200, {"servers": []}))
            self.assertFalse(
                [path for _method, path in panel.requests if any(word in path for word in MUTATING)],
                "fixture observed a mutating request",
            )
        self.assertFalse(panel.thread.is_alive(), "owned fixture thread must stop after cleanup")


def evidence_payload() -> dict[str, Any]:
    section = readme_section()
    result: dict[str, Any] = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "fixture": "owned mock panel on 127.0.0.1 ephemeral port; no Docker daemon, live panel or user data",
        "readme_section_sha256": hashlib.sha256(section.encode("utf-8")).hexdigest(),
        "checks": {},
    }
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(CopyablePromptTests)
    output = unittest.TextTestRunner(verbosity=0).run(suite)
    result["checks"] = {
        "tests_run": output.testsRun,
        "failures": [str(test) for test, _ in output.failures],
        "errors": [str(test) for test, _ in output.errors],
        "passed": output.wasSuccessful(),
        "cleanup": "MockPanel.__exit__ shut down and closed the owned HTTP server",
        "limits": "This suite validates the configuration examples and exercises health and a read-only "
                  "request against an HTTP mock. Packaged stdio is exercised by the distribution smoke.",
    }
    result["status"] = "ok" if output.wasSuccessful() else "failed"
    return result


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
