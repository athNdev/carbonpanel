#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Synchronize the Docker Hub repository page with the project README."""
import json
import os
from pathlib import Path
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen


REPOSITORY = os.environ.get("DOCKER_HUB_REPOSITORY", "atharvnaphade/carbonpanel")
USERNAME = os.environ.get("DOCKER_HUB_USERNAME") or os.environ.get("DOCKERHUB_USERNAME")
PASSWORD = os.environ.get("DOCKER_HUB_PASSWORD") or os.environ.get("DOCKERHUB_TOKEN")


def request_json(url, method="GET", payload=None, headers=None):
    body = json.dumps(payload).encode() if payload is not None else None
    request = Request(url, data=body, method=method, headers=headers or {})
    try:
        with urlopen(request) as response:
            return json.load(response)
    except HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"Docker Hub API request failed ({error.code}): {detail}") from error


def main():
    if not USERNAME or not PASSWORD:
        raise RuntimeError("Docker Hub credentials are required to update the repository description")

    readme = Path("README.md").read_text(encoding="utf-8")
    auth = request_json(
        "https://hub.docker.com/v2/auth/token",
        method="POST",
        payload={"identifier": USERNAME, "secret": PASSWORD},
        headers={"Content-Type": "application/json"},
    )
    token = auth.get("access_token")
    if not token:
        raise RuntimeError("Docker Hub authentication response did not contain a token")

    request_json(
        f"https://hub.docker.com/v2/repositories/{REPOSITORY}/",
        method="PATCH",
        payload={
            "description": "Self-hosted Minecraft server and modpack management with Docker.",
            "full_description": readme,
        },
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    print(f"Updated Docker Hub description for {REPOSITORY}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError) as error:
        print(f"error: {error}", file=sys.stderr)
        sys.exit(1)
