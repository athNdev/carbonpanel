#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Owned disposable Connect controller mock for MCP stdio smokes.

Responds to the auth-readiness calls (`GetAuthStatus`, `GetCurrentUser`) and one
read-only `ListServers` RPC with minimal protobuf messages. No live panel, real
credential, world or database is involved.

In-process mode is used by host-side tests. Standalone mode (`--port`) is used
inside a disposable container on an isolated Docker network so the packaged MCP
container reaches the fixture by service DNS rather than runner loopback.
"""

from __future__ import annotations

import argparse
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

# Connect's Go client uses protobuf binary by default. Minimal valid responses:
# GetAuthStatusResponse{local_auth_enabled:true},
# GetCurrentUserResponse{user:{id,username,is_active:true}}, empty ListServersResponse.
_USER = (bytes.fromhex("0a09") + b"u-fixture"
         + bytes.fromhex("120d") + b"fixture-admin"
         + bytes.fromhex("2801"))
RESPONSES: dict[str, bytes] = {
    "/carbonpanel.v1.AuthService/GetAuthStatus": bytes.fromhex("0801"),
    "/carbonpanel.v1.AuthService/GetCurrentUser": bytes.fromhex("0a1c") + _USER,
    "/carbonpanel.v1.ServerService/ListServers": b"",
}
REQUESTS: list[str] = []


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args: Any) -> None:
        return

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/ready":
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        REQUESTS.append(self.path)
        # `docker logs` of the fixture container proves the MCP container
        # reached it by service DNS on the isolated test network.
        print(f"fixture request {self.path}", flush=True)
        body = RESPONSES.get(self.path)
        if body is None:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/proto")
        self.send_header("Connect-Protocol-Version", "1")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class OwnedController:
    def __init__(self) -> None:
        REQUESTS.clear()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def requests(self) -> list[str]:
        return list(REQUESTS)

    def __enter__(self) -> "OwnedController":
        self.thread.start()
        return self

    def __exit__(self, *_args: Any) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


def evidence_summary(controller: OwnedController) -> str:
    return json.dumps({"fixture_requests": controller.requests,
                       "cleanup": "owned HTTP server shut down"})


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    ThreadingHTTPServer(("0.0.0.0", args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
