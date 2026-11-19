#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Stamp and verify the exact product version in generated OpenAPI copies.

The proto declares an intentionally independent API schema version (currently
the literal ``2.x`` placeholder) and the wire protocol stays ``v1``. A release
candidate must not ship that placeholder: the bundled and website schema copies
carry the authoritative normalized product version instead. The proto and the
``carbonpanel.v1`` protocol identity are never rewritten.
"""
import argparse
import json
from pathlib import Path
import re
import sys

SEMVER = re.compile(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?\Z")
PLACEHOLDER = re.compile(r"(?:\d+\.x|\d+\.\*|todo|tbd|placeholder|unset|unknown)\Z", re.I)
VERSION_LINE = re.compile(r"^  version:\s*(.*?)\s*(?:\r?\n)?\Z")


def _info_version(text):
    inside = False
    index = None
    for position, line in enumerate(text.splitlines(keepends=True)):
        if line.startswith("info:"):
            if inside:
                raise ValueError("OpenAPI document has multiple info blocks")
            inside = True
            continue
        if inside and line.strip() and not line[0].isspace():
            inside = False
        match = VERSION_LINE.match(line) if inside else None
        if match:
            if index is not None:
                raise ValueError("OpenAPI info has multiple version fields")
            index = (position, match.group(1))
    if index is None:
        raise ValueError("OpenAPI info.version is missing")
    return index


def verify_protocol_v1(text):
    """The independent protocol identity must remain v1."""
    if "carbonpanel.v1." not in text:
        raise ValueError("OpenAPI does not describe the carbonpanel.v1 protocol")
    if "/carbonpanel.v2." in text:
        raise ValueError("OpenAPI protocol identity is not v1")


def stamp_text(text, version):
    """Return text with info.version set to the exact normalized version."""
    if not SEMVER.fullmatch(version):
        raise ValueError(f"Not an exact normalized product version: {version!r}")
    position, current = _info_version(text)
    if current == version:
        return text
    if not PLACEHOLDER.fullmatch(current):
        raise ValueError(
            f"Refusing to overwrite a non-placeholder info.version {current!r}; "
            f"expected {version!r}")
    lines = text.splitlines(keepends=True)
    ending = "\n" if lines[position].endswith("\n") else ""
    lines[position] = f"  version: {version}{ending}"
    result = "".join(lines)
    verify_protocol_v1(result)
    return result


def verify_text(text, version):
    """Fail closed unless the schema already carries the exact version."""
    if not SEMVER.fullmatch(version):
        raise ValueError(f"Not an exact normalized product version: {version!r}")
    _, current = _info_version(text)
    if current != version:
        raise ValueError(f"OpenAPI info.version {current!r} is not {version!r}")
    if PLACEHOLDER.fullmatch(current):
        raise ValueError(f"OpenAPI info.version is still a placeholder: {current!r}")
    verify_protocol_v1(text)


def stamp(path, version):
    source = Path(path)
    updated = stamp_text(source.read_text(encoding="utf-8"), version)
    if updated != source.read_text(encoding="utf-8"):
        source.write_text(updated, encoding="utf-8")
    verify_text(source.read_text(encoding="utf-8"), version)
    print(f"{source}: info.version stamped to {version}")


def verify(path, version):
    verify_text(Path(path).read_text(encoding="utf-8"), version)
    print(f"{path}: info.version verified as {version}")


def verify_ui(directory, version):
    """Verify the AUTHORITATIVE runtime UI version, not an incidental string.

    Vite writes build/version.json from the same getVersion() that defines the
    app's __APP_VERSION__ constant. A version substring in an unrelated asset,
    schema or metadata file must not satisfy this check.
    """
    if not SEMVER.fullmatch(version):
        raise ValueError(f"Not an exact normalized product version: {version!r}")
    metadata = Path(directory) / "version.json"
    if not metadata.is_file():
        raise ValueError(f"UI build has no authoritative version.json: {metadata}")
    try:
        document = json.loads(metadata.read_text(encoding="utf-8"))
    except (ValueError, OSError) as error:
        raise ValueError(f"UI version.json is invalid: {error}") from error
    actual = document.get("version") if isinstance(document, dict) else None
    if actual != version:
        raise ValueError(f"UI runtime version {actual!r} is not {version!r}")
    print(f"UI runtime version.json under {directory}: {version} verified")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("stamp", "verify", "verify-ui"):
        command = commands.add_parser(name)
        command.add_argument("path")
        command.add_argument("version")
    args = parser.parse_args()
    if args.command == "stamp":
        stamp(args.path, args.version)
    elif args.command == "verify":
        verify(args.path, args.version)
    elif args.command == "verify-ui":
        verify_ui(args.path, args.version)


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
