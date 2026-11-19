#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

"""Build and verify the persistent README showcase asset bundle.

The capture job is read-only; the publish job is the only writer. This module
is the shared, deterministic gate between them: it turns real journey output
into a versioned manifest and then fails closed unless every declared image is
present, structurally valid, byte-identical to the manifest, fresh relative to
its source commit, and free of credential material.

Only the Python standard library is used so both jobs can run it without a
dependency install step.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import struct
import sys
import zlib
from datetime import datetime, timedelta, timezone
from pathlib import Path

SCHEMA_VERSION = 1
KIND = "carbonpanel-showcase-assets"
ASSET_DIR = "assets/showcase"
PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"

# Isolated review credentials and token shapes must never appear in a public
# asset. These are structural patterns, not the credential values themselves.
SECRET_PATTERNS = (
    re.compile(rb"Review-Panel-2026"),
    re.compile(rb"PANEL_REVIEW_PASSWORD\s*="),
    re.compile(rb"Authorization\s*:\s*Bearer", re.I),
    re.compile(rb"\bBearer\s+[A-Za-z0-9._-]{20,}"),
    re.compile(rb"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})"),
    re.compile(rb"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
)

MIN_IMAGE_BYTES = 4 * 1024
MAX_IMAGE_BYTES = 24 * 1024 * 1024
MIN_DIMENSION = 320
MAX_DIMENSION = 8192


class AssetError(Exception):
    """A fail-closed validation error."""


def canonical_json(value: object) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def inspect_png(path: Path) -> tuple[int, int, int]:
    """Validate PNG structure (signature, chunk CRCs, IEND) and return dims.

    A file that merely ends in .png is not evidence. Verifying every chunk CRC
    and a terminating IEND rejects truncated, fake or partially written files.
    """
    data = path.read_bytes()
    if len(data) < 33 or not data.startswith(PNG_SIGNATURE):
        raise AssetError(f"{path.name}: not a PNG (bad or missing signature)")
    offset = 8
    width = height = 0
    saw_ihdr = saw_idat = saw_iend = False
    while offset < len(data):
        if offset + 12 > len(data):
            raise AssetError(f"{path.name}: truncated PNG chunk header")
        (length,) = struct.unpack(">I", data[offset : offset + 4])
        chunk_type = data[offset + 4 : offset + 8]
        if length > MAX_IMAGE_BYTES or offset + 12 + length > len(data):
            raise AssetError(f"{path.name}: truncated or oversized PNG chunk")
        body = data[offset + 8 : offset + 8 + length]
        (crc,) = struct.unpack(">I", data[offset + 8 + length : offset + 12 + length])
        if zlib.crc32(chunk_type + body) & 0xFFFFFFFF != crc:
            raise AssetError(f"{path.name}: PNG chunk CRC mismatch ({chunk_type!r})")
        if chunk_type == b"IHDR":
            if saw_ihdr or length != 13:
                raise AssetError(f"{path.name}: invalid IHDR")
            width, height = struct.unpack(">II", body[:8])
            saw_ihdr = True
        elif chunk_type == b"IDAT":
            saw_idat = True
        elif chunk_type == b"IEND":
            saw_iend = True
            if offset + 12 + length != len(data):
                raise AssetError(f"{path.name}: bytes after PNG IEND")
            break
        offset += 12 + length
    if not (saw_ihdr and saw_idat and saw_iend):
        raise AssetError(f"{path.name}: PNG missing IHDR/IDAT/IEND")
    if not (MIN_DIMENSION <= width <= MAX_DIMENSION and MIN_DIMENSION <= height <= MAX_DIMENSION):
        raise AssetError(f"{path.name}: implausible PNG dimensions {width}x{height}")
    if not (MIN_IMAGE_BYTES <= len(data) <= MAX_IMAGE_BYTES):
        raise AssetError(f"{path.name}: implausible PNG size {len(data)} bytes")
    return width, height, len(data)


def scan_secrets(data: bytes, label: str) -> None:
    for pattern in SECRET_PATTERNS:
        if pattern.search(data):
            raise AssetError(f"{label}: contains credential-like material")


def parse_features(value: str | None) -> set[str]:
    return {item for item in (value or "").split(",") if item}


def expected_images(plan_path: Path, features: set[str] | None = None) -> list[dict[str, str]]:
    plan = json.loads(plan_path.read_text(encoding="utf-8"))
    enabled = features if features is not None else set()
    images = []
    for step in plan.get("steps", []):
        name = step.get("screenshot")
        if not name:
            continue
        # A feature-tagged screenshot is only required when that feature was
        # explicitly selected for the run; an untagged core shot is always
        # required. This is the publish-side feature gate.
        feature = step.get("feature")
        if feature and feature not in enabled:
            continue
        images.append(
            {
                "name": name,
                "alt": step.get("alt") or step.get("name") or name,
                "focus": step.get("focus") or step.get("name") or "",
                # Core screenshots are mandatory; feature-tagged screenshots
                # are best-effort until a README link makes them canonical.
                "required": bool(step.get("required", not bool(feature))),
            }
        )
    if not images:
        raise AssetError(f"{plan_path}: plan declares no screenshots")
    names = [image["name"] for image in images]
    if len(names) != len(set(names)):
        raise AssetError(f"{plan_path}: duplicate screenshot names")
    return images


def content_digest(manifest: dict) -> str:
    """Identity of the *content*, excluding run/source/time metadata.

    Repeated runs that capture identical images must be a no-op even though
    their run id and timestamp differ. Alt text and focus are included so a
    provenance or accessibility correction still publishes a new manifest.
    """
    content = {"images": manifest["images"]}
    return sha256_bytes(canonical_json(content).encode("utf-8"))


def write_atomic(path: Path, data: bytes) -> None:
    temp = path.with_name(path.name + ".tmp")
    temp.write_bytes(data)
    os.replace(temp, path)


def build(args: argparse.Namespace) -> None:
    images_dir = Path(args.images_dir)
    publish_dir = Path(args.publish_dir)
    publish_dir.mkdir(parents=True, exist_ok=True)
    declared = expected_images(Path(args.plan), parse_features(getattr(args, "features", None)))

    images = []
    for spec in declared:
        source = images_dir / f"{spec['name']}.png"
        if not source.is_file():
            if spec["required"]:
                raise AssetError(f"missing required screenshot {source}")
            sys.stderr.write(f"optional screenshot {source.name} absent; skipping\n")
            continue
        width, height, size = inspect_png(source)
        scan_secrets(source.read_bytes(), source.name)
        images.append(
            {
                "name": spec["name"],
                "path": f"{ASSET_DIR}/{spec['name']}.png",
                "sha256": sha256_file(source),
                "bytes": size,
                "width": width,
                "height": height,
                "alt": spec["alt"],
                "focus": spec["focus"],
                "required": spec["required"],
            }
        )

    seed_hash = sha256_file(Path(args.seed_script))
    seed_text = Path(args.seed_script).read_text(encoding="utf-8")
    if "PANEL_REVIEW_USER" not in seed_text or "Register" not in seed_text:
        raise AssetError("seed script does not describe an isolated seeded installation")

    manifest = {
        "kind": KIND,
        "schemaVersion": SCHEMA_VERSION,
        "source": {
            "sha": args.source_sha,
            "ref": args.source_ref,
            "repository": args.repository,
            "runId": args.run_id,
            "runUrl": args.run_url,
            "capturedAt": args.captured_at,
        },
        "version": {
            "appVersion": args.app_version,
            "driverVersion": args.driver_version,
            "browserVersion": args.browser_version,
        },
        "provenance": {
            "installation": "isolated disposable seeded review stack (scripts/review/devstack.sh)",
            "dataOrigin": "seeded fixture account, node, server and modpacks; no production data",
            "seedScript": {"path": args.seed_script, "sha256": seed_hash},
            "capture": "unretouched Chromium crops of the packaged application",
            "credentials": "isolated review credentials, never published",
            "mockups": False,
        },
        "images": images,
        "features": sorted(parse_features(getattr(args, "features", None))),
    }
    manifest["contentDigest"] = content_digest(manifest)

    for image in images:
        source = images_dir / f"{image['name']}.png"
        write_atomic(publish_dir / f"{image['name']}.png", source.read_bytes())
    manifest_bytes = (canonical_json(manifest) + "\n").encode("utf-8")
    write_atomic(publish_dir / "manifest.json", manifest_bytes)
    sys.stdout.write(f"built manifest for {len(images)} images ({manifest['contentDigest']})\n")


def load_manifest(path: Path) -> dict:
    try:
        manifest = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise AssetError(f"cannot read manifest: {exc}") from exc
    if manifest.get("kind") != KIND or manifest.get("schemaVersion") != SCHEMA_VERSION:
        raise AssetError("unsupported manifest kind or schemaVersion")
    return manifest


def validate(args: argparse.Namespace) -> None:
    publish_dir = Path(args.publish_dir)
    manifest = load_manifest(publish_dir / "manifest.json")
    source = manifest.get("source", {})
    if source.get("sha") != args.expected_source_sha:
        raise AssetError(
            f"stale source: manifest {source.get('sha')} != expected {args.expected_source_sha}"
        )
    if not source.get("repository") or not source.get("runUrl"):
        raise AssetError("manifest lacks repository or run provenance")
    try:
        captured = datetime.fromisoformat(source["capturedAt"].replace("Z", "+00:00"))
    except (KeyError, ValueError) as exc:
        raise AssetError(f"invalid capturedAt: {exc}") from exc
    if captured.tzinfo is None:
        raise AssetError("capturedAt must include a timezone")
    now = datetime.now(timezone.utc)
    if captured > now + timedelta(minutes=5) or now - captured > timedelta(hours=args.max_age_hours):
        raise AssetError("capturedAt is in the future or too old to publish")

    provenance = manifest.get("provenance", {})
    for key in ("installation", "dataOrigin", "seedScript", "capture", "credentials"):
        if not provenance.get(key):
            raise AssetError(f"manifest lacks provenance.{key}")
    if provenance.get("mockups") is not False:
        raise AssetError("manifest must assert mockups=false")
    if not manifest.get("version", {}).get("appVersion"):
        raise AssetError("manifest lacks app version")

    images = manifest.get("images")
    if not isinstance(images, list) or not images:
        raise AssetError("manifest has no images")
    names = [image.get("name") for image in images]
    if len(names) != len(set(names)):
        raise AssetError("manifest has duplicate image names")
    declared = {
        spec["name"]: spec
        for spec in expected_images(Path(args.plan), parse_features(getattr(args, "features", None)))
    }
    unexpected = sorted(set(names) - set(declared))
    if unexpected:
        raise AssetError(f"manifest contains images absent from the plan: {unexpected}")
    for name, spec in declared.items():
        if spec["required"] and name not in names:
            raise AssetError(f"required screenshot missing from manifest: {name}")
    for image in images:
        if image.get("required") is not declared[image["name"]]["required"]:
            raise AssetError(f"{image['name']}: required flag differs from the plan")

    for image in images:
        path = publish_dir / f"{image['name']}.png"
        if not path.is_file():
            raise AssetError(f"missing published image {path}")
        width, height, size = inspect_png(path)
        data = path.read_bytes()
        scan_secrets(data, path.name)
        if sha256_bytes(data) != image.get("sha256"):
            raise AssetError(f"{path.name}: SHA256 does not match manifest")
        if image.get("path") != f"{ASSET_DIR}/{image['name']}.png":
            raise AssetError(f"{path.name}: bad published path")
        if (width, height, size) != (image.get("width"), image.get("height"), image.get("bytes")):
            raise AssetError(f"{path.name}: dimensions/size do not match manifest")
        if not image.get("alt"):
            raise AssetError(f"{path.name}: missing alt text")

    # No unexpected files: a stale or injected payload must not be published.
    allowed = {f"{name}.png" for name in names} | {"manifest.json"}
    extras = sorted(p.name for p in publish_dir.iterdir() if p.is_file() and p.name not in allowed)
    if extras:
        raise AssetError(f"unexpected files in publish bundle: {extras}")
    scan_secrets((publish_dir / "manifest.json").read_bytes(), "manifest.json")

    expected_digest = content_digest(manifest)
    if manifest.get("contentDigest") != expected_digest:
        raise AssetError("manifest contentDigest mismatch")
    sys.stdout.write(f"validated {len(images)} images; contentDigest={expected_digest}\n")
    sys.stdout.write(f"content-digest={expected_digest}\n")


def noop(args: argparse.Namespace) -> None:
    current = Path(args.current_manifest)
    if not current.is_file():
        sys.stdout.write("changed=true\n")
        return
    old = load_manifest(current).get("contentDigest")
    new = load_manifest(Path(args.new_manifest)).get("contentDigest")
    changed = "false" if old and old == new else "true"
    sys.stdout.write(f"changed={changed}\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    build_cmd = sub.add_parser("build")
    for flag in (
        "images-dir", "publish-dir", "plan", "seed-script", "source-sha",
        "source-ref", "repository", "run-id", "run-url", "captured-at",
        "app-version", "driver-version", "browser-version",
    ):
        build_cmd.add_argument("--" + flag, required=True)
    build_cmd.add_argument("--features", default="")

    validate_cmd = sub.add_parser("validate")
    validate_cmd.add_argument("--publish-dir", required=True)
    validate_cmd.add_argument("--plan", required=True)
    validate_cmd.add_argument("--expected-source-sha", required=True)
    validate_cmd.add_argument("--features", default="")
    validate_cmd.add_argument("--max-age-hours", type=int, default=12)

    noop_cmd = sub.add_parser("noop")
    noop_cmd.add_argument("--current-manifest", required=True)
    noop_cmd.add_argument("--new-manifest", required=True)

    args = parser.parse_args()
    try:
        if args.command == "build":
            build(args)
        elif args.command == "validate":
            validate(args)
        else:
            noop(args)
    except AssetError as exc:
        sys.stderr.write(f"showcase assets: {exc}\n")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
