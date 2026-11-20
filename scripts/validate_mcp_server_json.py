#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Validate the checked-in official MCP Registry manifest for Carbon Panel.

This is a local, offline schema-required-field check. It is deliberately
stricter than "valid JSON": it refuses moving versions, requires an immutable
OCI digest, requires stdio transport, requires the token to be marked secret,
and can check that the OCI ownership label matches the registry name. It does
not submit anything to the registry.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_MANIFEST = ROOT / "docker" / "mcp.server.json"
DEFAULT_DOCKERFILE = ROOT / "docker" / "Dockerfile.mcp"

EXPECTED_SCHEMA = "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json"
EXPECTED_NAME = "io.github.athNdev/carbon-panel"
EXPECTED_IMAGE = "ghcr.io/athndev/carbonpanel-mcp"
EXPECTED_REPO_URL = "https://github.com/athNdev/carbonpanel"
EXPECTED_REPO_ID = "1369132627"
OFFICIAL_REGISTRY_METADATA = "io.modelcontextprotocol.registry/official"
PLACEHOLDER_DIGEST = "sha256:" + "0" * 64

NAME_RE = re.compile(r"^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+\Z")
SEMVER_RE = re.compile(r"^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?\Z")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}\Z")
IDENTIFIER_RE = re.compile(
    rf"^{re.escape(EXPECTED_IMAGE)}:([^@\s]+)@(sha256:[0-9a-f]{{64}})\Z"
)
LABEL_RE = re.compile(r'io\.modelcontextprotocol\.server\.name="([^"]+)"')
MOVING_REFS = {"latest", "main", "master", "edge", "dev", "head"}
RANGE_PREFIXES = ("^", "~", ">=", "<=", "=", "v>")


def _version_errors(value: Any, where: str) -> list[str]:
    errors: list[str] = []
    if not isinstance(value, str) or not value:
        return [f"{where}: version must be a non-empty string"]
    lowered = value.strip().lower()
    if lowered in MOVING_REFS or lowered.endswith("@latest"):
        errors.append(f"{where}: moving reference {value!r} is forbidden")
    if value.startswith(RANGE_PREFIXES) or re.search(r"(x|\*|>=|<=|\^|~)", value):
        errors.append(f"{where}: version range or wildcard {value!r} is forbidden")
    if not SEMVER_RE.match(value):
        errors.append(f"{where}: {value!r} is not a pinned SemVer-like version")
    return errors


def validate(
    doc: Any,
    *,
    dockerfile_label: str | None = None,
    require_real_digest: bool = False,
) -> tuple[list[str], list[str]]:
    """Return (errors, warnings) for a parsed server.json document."""
    errors: list[str] = []
    warnings: list[str] = []

    if not isinstance(doc, dict):
        return ["server.json must be a JSON object"], warnings

    if doc.get("$schema") != EXPECTED_SCHEMA:
        errors.append(f"$schema must be {EXPECTED_SCHEMA}")
    for field in ("name", "description", "version", "repository"):
        if field not in doc:
            errors.append(f"missing required root field: {field}")

    name = doc.get("name")
    if not isinstance(name, str) or not NAME_RE.match(name):
        errors.append("name must match ^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$")
    elif name != EXPECTED_NAME:
        errors.append(f"name must be {EXPECTED_NAME!r} for this repository")

    description = doc.get("description")
    if not isinstance(description, str) or not (1 <= len(description) <= 100):
        errors.append("description must be a string of 1 to 100 characters")

    errors.extend(_version_errors(doc.get("version"), "version"))

    repository = doc.get("repository")
    if isinstance(repository, dict):
        if repository.get("url") != EXPECTED_REPO_URL:
            errors.append(f"repository.url must be {EXPECTED_REPO_URL!r}")
        if repository.get("source") != "github":
            errors.append("repository.source must be 'github'")
        if str(repository.get("id", "")) != EXPECTED_REPO_ID:
            errors.append(f"repository.id must be the stable GitHub id {EXPECTED_REPO_ID}")
    elif "repository" in doc:
        errors.append("repository must be an object")

    packages = doc.get("packages")
    if not isinstance(packages, list) or len(packages) != 1:
        errors.append("packages must contain exactly one OCI entry")
    else:
        for index, package in enumerate(packages):
            where = f"packages[{index}]"
            if not isinstance(package, dict):
                errors.append(f"{where}: must be an object")
                continue
            if package.get("registryType") != "oci":
                errors.append(f"{where}: registryType must be 'oci'")
            for field in ("registryBaseUrl", "version", "fileSha256"):
                if field in package:
                    errors.append(f"{where}: OCI packages forbid {field!r} outside the identifier")

            identifier = package.get("identifier")
            if not isinstance(identifier, str):
                errors.append(f"{where}: identifier must be a string")
            else:
                match = IDENTIFIER_RE.match(identifier)
                if not match:
                    errors.append(
                        f"{where}: identifier must be {EXPECTED_IMAGE}:<tag>@sha256:<64 hex>"
                    )
                else:
                    tag, digest = match.groups()
                    if tag.lower() in MOVING_REFS or not SEMVER_RE.match(tag):
                        errors.append(f"{where}: identifier tag must be pinned, got {tag!r}")
                    elif tag != doc.get("version"):
                        errors.append(f"{where}: identifier tag must equal the server version")
                    if digest == PLACEHOLDER_DIGEST:
                        message = f"{where}: digest is still the all-zero placeholder"
                        if require_real_digest:
                            errors.append(message)
                        else:
                            warnings.append(message)

            transport = package.get("transport")
            if not isinstance(transport, dict) or transport.get("type") != "stdio":
                errors.append(f"{where}: transport.type must be 'stdio'")
            if package.get("runtimeHint") != "docker":
                errors.append(f"{where}: runtimeHint must be 'docker'")
            if not isinstance(package.get("runtimeArguments"), list):
                errors.append(f"{where}: runtimeArguments must be a list")

            variables = package.get("environmentVariables")
            if not isinstance(variables, list):
                errors.append(f"{where}: environmentVariables must be a list")
            else:
                names = {
                    item.get("name")
                    for item in variables
                    if isinstance(item, dict)
                }
                for required in ("CARBONPANEL_URL", "CARBONPANEL_TOKEN"):
                    if required not in names:
                        errors.append(f"{where}: environmentVariables must define {required}")
                for item in variables:
                    if isinstance(item, dict) and item.get("name") == "CARBONPANEL_TOKEN":
                        if item.get("isSecret") is not True:
                            errors.append(f"{where}: CARBONPANEL_TOKEN must have isSecret=true")

    if dockerfile_label is not None and dockerfile_label != EXPECTED_NAME:
        errors.append(
            f"OCI label {dockerfile_label!r} does not match registry name {EXPECTED_NAME!r}"
        )

    return errors, warnings


def read_dockerfile_label(path: Path) -> str | None:
    text = path.read_text(encoding="utf-8")
    match = LABEL_RE.search(text)
    return match.group(1) if match else None


def verify_listing(response: Any, expected: dict) -> list[str]:
    """Compare the exact-version Registry response with the validated manifest."""
    if not isinstance(response, dict) or not isinstance(response.get("server"), dict):
        return ["Registry response must contain a server object"]
    server = response["server"]
    errors, _ = validate(server, require_real_digest=True)
    if server.get("version") != expected["version"]:
        errors.append("Registry server version does not match the submitted version")
    if not errors and server["packages"][0]["identifier"] != expected["packages"][0]["identifier"]:
        errors.append("Registry OCI identifier does not match the submitted image digest")
    metadata = response.get("_meta")
    official = metadata.get(OFFICIAL_REGISTRY_METADATA) if isinstance(metadata, dict) else None
    if not isinstance(official, dict) or official.get("status") != "active":
        errors.append("Registry server status must be active")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--listing", type=Path, help="read-only exact-version Registry response to compare")
    parser.add_argument(
        "--require-real-digest",
        action="store_true",
        help="fail when the identifier still uses the all-zero placeholder digest",
    )
    parser.add_argument(
        "--dockerfile",
        type=Path,
        default=DEFAULT_DOCKERFILE,
        help="Dockerfile whose OCI ownership label must match the registry name",
    )
    args = parser.parse_args(argv)

    try:
        doc = json.loads(args.file.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        print(f"error: cannot read {args.file}: {error}", file=sys.stderr)
        return 1

    label = None
    if args.dockerfile.is_file():
        try:
            label = read_dockerfile_label(args.dockerfile)
        except OSError as error:
            print(f"error: cannot read {args.dockerfile}: {error}", file=sys.stderr)
            return 1
        if label is None:
            print(f"error: no OCI ownership label in {args.dockerfile}", file=sys.stderr)
            return 1

    errors, warnings = validate(
        doc, dockerfile_label=label, require_real_digest=args.require_real_digest or args.listing is not None
    )
    if args.listing is not None and not errors:
        try:
            listing = json.loads(args.listing.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            print(f"error: cannot read {args.listing}: {error}", file=sys.stderr)
            return 1
        errors.extend(verify_listing(listing, doc))
    for warning in warnings:
        print(f"WARNING: {warning}")
    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    if errors:
        return 1
    if args.listing is not None:
        print(f"OK: {args.listing} confirms the active listing for the submitted OCI package")
    else:
        print(f"OK: {args.file} satisfies the local MCP manifest contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
