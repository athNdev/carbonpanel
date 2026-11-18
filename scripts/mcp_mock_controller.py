#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Owned disposable Connect controller mock for MCP stdio smokes.

Responds to the auth-readiness calls (`GetAuthStatus`, `GetCurrentUser`) and one
read-only `ListServers` RPC with minimal protobuf messages. No live panel, real
credential, world or database is involved.
"""

from __future__ import annotations

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


class OwnedController:
    def __init__(self) -> None:
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args: Any) -> None:
                return

            def do_POST(self) -> None:  # noqa: N802
                length = int(self.headers.get("Content-Length", "0"))
                self.rfile.read(length)
                outer.requests.append(self.path)
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

        self.requests: list[str] = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

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
