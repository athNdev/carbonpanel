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
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import release  # noqa: E402


def decide(image: str, version: str, expected_digest: str) -> dict[str, str]:
    reference = f"{image}:{version}"
    if expected_digest and not release.valid_digest(expected_digest):
        return {"action": "refuse", "state": "invalid", "digest": "",
                "reason": "expected digest is not a valid sha256"}

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
    result = decide(args.image, args.version, args.expected_digest)
    print(json.dumps(result, indent=2, sort_keys=True))
    release.write_output(result)
    return 0 if result["action"] in {"publish", "resume"} else 1


if __name__ == "__main__":
    raise SystemExit(main())
