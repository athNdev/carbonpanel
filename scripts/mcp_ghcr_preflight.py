#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Strict immutable-tag preflight for the MCP OCI artifact.

Reuses scripts/release.py observation/classification so publication refuses on
ambiguous, transient, unauthorized or generic 404 results. Only a definitive
manifest-unknown for the QUERIED reference is treated as absent. A requested
resume digest that cannot be observed never falls back to a new push.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import release  # noqa: E402


def github_package_state(image: str) -> tuple[str, str]:
    """Read-only package existence via the GitHub Packages API.

    A raw registry manifest lookup for an uncreated GHCR package returns 401
    anonymously and 403 with a pull token (empty body), which is NOT a
    definitive absence. The authenticated Packages API returns a definitive
    404 'Package not found.' instead. Anything else is unknown and refuses.
    """
    parts = image.split("/")
    if len(parts) != 3 or not parts[1] or not parts[2]:
        return "unknown", "cannot derive owner/package from image"
    path = f"/users/{parts[1]}/packages/container/{parts[2]}/versions"
    try:
        result = subprocess.run(
            ["gh", "api", path, "--include"],
            capture_output=True, text=True, check=False,
        )
    except OSError as error:
        return "unknown", f"cannot read GitHub Packages API: {error}"
    match = re.search(r"(?m)^HTTP/[\d.]+\s+(\d{3})", result.stdout or "")
    if not match:
        return "unknown", "GitHub Packages API returned no HTTP status"
    status = int(match.group(1))
    if status == 404 and "Package not found" in result.stdout:
        return "absent", "definitive 404 Package not found from GitHub Packages API"
    if status == 200:
        return "present", "package exists in GitHub Packages API"
    return "unknown", f"ambiguous GitHub Packages API status {status}"


def decide(image: str, version: str, expected_digest: str,
           package_state: str | None = None) -> dict[str, str]:
    reference = f"{image}:{version}"
    if expected_digest and not release.valid_digest(expected_digest):
        return {"action": "refuse", "state": "invalid", "digest": "",
                "reason": "expected digest is not a valid sha256"}

    if package_state is None:
        package_state, detail = github_package_state(image)
    else:
        detail = "caller-supplied package state"
    if package_state == "unknown":
        return {"action": "refuse", "state": "unknown", "digest": "",
                "reason": "package existence is unknown: " + detail}
    if package_state == "absent":
        if expected_digest:
            return {"action": "refuse", "state": "absent", "digest": "",
                    "reason": "requested resume digest cannot exist: package is absent"}
        return {"action": "publish", "state": "absent", "digest": "",
                "reason": "definitive first-tag absence from " + detail}

    state, info = release.observe_reference(reference, strict=True)
    if state == "unknown":
        return {"action": "refuse", "state": "unknown", "digest": "",
                "reason": f"registry state for {reference} is unknown; no write"}
    if state == "present":
        observed = (info or {}).get("digest", "")
        if not release.valid_digest(observed):
            return {"action": "refuse", "state": "present", "digest": "",
                    "reason": "tag exists but digest could not be parsed; no write"}
        if not expected_digest:
            return {"action": "refuse", "state": "present", "digest": observed,
                    "reason": "immutable tag already published; choose a new version"}
        if observed != expected_digest:
            return {"action": "refuse", "state": "present", "digest": observed,
                    "reason": "existing tag digest conflicts with the verified expected digest"}
        return {"action": "resume", "state": "present", "digest": observed,
                "reason": "verified identical digest; reuse with no destructive move"}

    # Strictly absent for this reference only.
    if expected_digest:
        return {"action": "refuse", "state": "absent", "digest": "",
                "reason": "requested resume digest was not found; no new push"}
    return {"action": "publish", "state": "absent", "digest": "",
            "reason": "immutable tag is absent under strict observation"}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--expected-digest", default="")
    args = parser.parse_args(argv)
    package_state, _detail = github_package_state(args.image)
    result = decide(args.image, args.version, args.expected_digest, package_state)
    result["package_state"] = package_state
    print(json.dumps(result, indent=2, sort_keys=True))
    release.write_output(result)
    return 0 if result["action"] in {"publish", "resume"} else 1


if __name__ == "__main__":
    raise SystemExit(main())
