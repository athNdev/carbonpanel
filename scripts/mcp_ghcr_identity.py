#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Verify a pinned MCP OCI image by digest on BOTH platforms.

Every observation is anchored to the manifest-list digest, never a mutable
tag. For each platform child we read the image CONFIG labels (not index
annotations) and require the pinned build revision, the MCP artifact version
and the approved ownership label. Any missing/unreadable field refuses.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import release  # noqa: E402

MCP_LABEL = "io.modelcontextprotocol.server.name"


def platform_labels(image: str, child: str) -> dict[str, str]:
    raw = release.inspect_runner(
        "docker", "buildx", "imagetools", "inspect", f"{image}@{child}",
        "--format", "{{json .Image}}",
    )
    document = json.loads(raw)
    labels = (document.get("config") or {}).get("Labels") or {}
    if not isinstance(labels, dict):
        return {}
    return {str(key): str(value) for key, value in labels.items()}


def verify(image: str, digest: str, revision: str, version: str,
           expected_name: str) -> tuple[bool, str, dict[str, object]]:
    if not release.valid_digest(digest):
        return False, "invalid manifest digest", {}
    index_digest, platforms = release.manifest_platforms(image, anchor_digest=digest)
    if index_digest and index_digest != digest:
        return False, "anchored index digest does not match the observed image", {}
    missing = sorted(platform for platform in release.REQUIRED_PLATFORMS if platform not in platforms)
    if missing:
        return False, "missing platforms: " + ", ".join(f"{os}/{arch}" for os, arch in missing), {}

    evidence: dict[str, object] = {"digest": digest, "platforms": {}}
    for platform, child in sorted(platforms.items()):
        key = f"{platform[0]}/{platform[1]}"
        try:
            labels = platform_labels(image, child)
        except (release.RegistryError, ValueError, TypeError, OSError,
                subprocess.CalledProcessError) as error:
            return False, f"cannot read {key} config labels: {error}", evidence
        evidence["platforms"][key] = {"digest": child, "labels": labels}
        if labels.get(MCP_LABEL) != expected_name:
            return False, f"{key} ownership label mismatch", evidence
        if labels.get("org.opencontainers.image.revision") != revision:
            return False, f"{key} config revision does not match the pinned build", evidence
        if labels.get("org.opencontainers.image.version") != version:
            return False, f"{key} config version does not match the pinned artifact", evidence
    return True, "both platform configs match the pinned build and artifact version", evidence


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--digest", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--expected-name", default="io.github.athNdev/carbon-panel")
    args = parser.parse_args(argv)
    ok, reason, evidence = verify(
        args.image, args.digest, args.revision, args.version, args.expected_name
    )
    values = {"verified": str(ok).lower(), "reason": reason}
    print(json.dumps({"verified": ok, "reason": reason, "evidence": evidence},
                     indent=2, sort_keys=True))
    release.write_output(values)
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
