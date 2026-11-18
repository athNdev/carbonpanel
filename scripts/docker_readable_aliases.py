#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Deterministic, human-readable immutable UTC build aliases for Docker Hub.

The aliases are ADDITIONS at exact existing manifest digests. They never move a
channel alias, never rebuild an image, and never touch the original sha-* refs.
"""
import argparse
from collections import defaultdict
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
from scripts import release

DOCKER_HUB_IMAGE = "atharvnaphade/carbonpanel"
ALIAS_PREFIX = "build-"
SHORT_LEN = 12  # provenance metadata only; never part of a user-facing alias
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
SHA_TAG_RE = re.compile(r"sha-([0-9a-f]{40})")
REQUIRED_PLATFORMS = frozenset({("linux", "amd64"), ("linux", "arm64")})
# These are channel/release aliases and are never read as inputs or written.
CHANNEL_TAGS = frozenset({"latest", "edge", "nightly", "1.0", "1.0.40"})


def utc_second(value):
    """Return (YYYYMMDDTHHMMSSZ, datetime) for an ISO-8601 UTC timestamp."""
    if not isinstance(value, str) or not value.strip():
        raise ValueError("timestamp is required")
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        moment = datetime.fromisoformat(text)
    except ValueError as error:
        raise ValueError("invalid timestamp: " + value) from error
    if moment.tzinfo is None:
        raise ValueError("timestamp must carry an explicit UTC offset")
    moment = moment.astimezone(timezone.utc)
    return moment.strftime("%Y%m%dT%H%M%SZ"), moment


def publication_timestamp(entry):
    """Return (datetime, source) using tag_last_pushed when available.

    This is the Hub tag-push/publication time, NOT a verified image build time.
    Re-pushing a tag can change it. `last_updated` is only a fallback.
    """
    value = entry.get("tag_last_pushed") or entry.get("observed_last_updated")
    source = "tag_last_pushed" if entry.get("tag_last_pushed") else "observed_last_updated"
    _, moment = utc_second(value)
    return moment, source


def readable_alias(moment, suffix=None):
    """Pure human-readable UTC alias: build-YYYY-MM-DD-HHMMSS[-N]."""
    name = moment.strftime("build-%Y-%m-%d-%H%M%S")
    return name if suffix is None else f"{name}-{suffix}"


def commit_from_sha_tag(tag):
    """Extract the 40-hex commit id from a sha-<commit> tag."""
    found = SHA_TAG_RE.fullmatch(tag or "")
    if not found:
        raise ValueError("expected a sha-<40-hex-commit> tag: " + str(tag))
    return found.group(1)


def base_alias(stamp):
    """Pure readable build-YYYY-MM-DD-HHMMSS from a YYYYMMDDTHHMMSSZ stamp."""
    if not re.fullmatch(r"\d{8}T\d{6}Z", stamp):
        raise ValueError("invalid UTC second stamp: " + str(stamp))
    return f"build-{stamp[0:4]}-{stamp[4:6]}-{stamp[6:8]}-{stamp[9:15]}"


def normalize_platforms(platforms):
    """Accept 'linux/amd64' strings or [os, arch] pairs; return a set of pairs."""
    result = set()
    for item in platforms or []:
        if isinstance(item, str):
            parts = item.split("/", 1)
            if len(parts) != 2:
                raise ValueError("invalid platform: " + item)
            result.add((parts[0], parts[1]))
        else:
            result.add(tuple(item))
    return result


def validate_entries(entries):
    """Validate raw inventory entries. Never accepts a channel alias as input."""
    if not isinstance(entries, list) or not entries:
        raise ValueError("a non-empty entries list is required")
    for entry in entries:
        tag = entry.get("tag", "")
        if tag in CHANNEL_TAGS:
            raise ValueError("channel aliases cannot be inputs: " + tag)
        commit = entry.get("commit_id") or commit_from_sha_tag(tag)
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise ValueError("invalid commit id for " + tag)
        digest = entry.get("digest") or entry.get("manifest_list_digest", "")
        if not DIGEST_RE.fullmatch(digest):
            raise ValueError("invalid manifest digest for " + tag)
        publication_timestamp(entry)
        platforms = normalize_platforms(entry.get("platforms", []))
        if not REQUIRED_PLATFORMS <= platforms:
            raise ValueError(tag + " must expose linux/amd64 and linux/arm64")


def build_alias_map(entries, repository=DOCKER_HUB_IMAGE):
    """Map each distinct source digest to one readable immutable alias.

    Collision rules:
      1. Several sha tags for one digest produce ONE alias; canonical is the
         earliest publication timestamp (tie-break: tag name ascending), other
         sha tags are retained.
      2. Distinct digests sharing a readable base alias get stable numeric
         suffixes (-2, -3, ...) ordered by digest hex; no SHA fragment appears
         in any user-facing alias.
    """
    validate_entries(entries)
    by_digest = defaultdict(list)
    for entry in entries:
        item = dict(entry)
        item["commit_id"] = item.get("commit_id") or commit_from_sha_tag(item["tag"])
        item["digest"] = item.get("digest") or item["manifest_list_digest"]
        by_digest[item["digest"]].append(item)

    aliases = []
    for digest, group in by_digest.items():
        ordered = sorted(group, key=lambda item: (
            publication_timestamp(item)[0], item["tag"]))
        canonical = ordered[0]
        moment, source = publication_timestamp(canonical)
        base = readable_alias(moment)
        aliases.append({
            "digest": digest,
            "canonical_tag": canonical["tag"],
            "commit_id": canonical["commit_id"],
            "short_sha": canonical["commit_id"][:SHORT_LEN],
            "platforms": sorted(REQUIRED_PLATFORMS),
            "tag_last_pushed": canonical.get("tag_last_pushed"),
            "observed_last_updated": canonical.get("observed_last_updated"),
            "timestamp_source": source,
            "timestamp_utc": moment.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "retained_sha_tags": sorted(item["tag"] for item in ordered[1:]),
            "base_alias": base,
            "final_alias": base,
            "collision_rank": 1,
            "suffix_reason": None,
        })

    by_base = defaultdict(list)
    for alias in aliases:
        by_base[alias["base_alias"]].append(alias)
    for base, group in by_base.items():
        if len(group) < 2:
            continue
        ordered = sorted(group, key=lambda item: item["digest"])
        for rank, alias in enumerate(ordered, 1):
            alias["final_alias"] = base if rank == 1 else f"{base}-{rank}"
            alias["collision_rank"] = rank
            alias["suffix_reason"] = "same publication second across distinct digests"

    aliases.sort(key=lambda item: (item["timestamp_utc"], item["final_alias"], item["digest"]))
    for index, alias in enumerate(aliases, 1):
        alias["seq"] = index
    return {
        "schema_version": "1.0",
        "repository": repository,
        "alias_scheme": {
            "pattern": "build-YYYY-MM-DD-HHMMSS[-N]",
            "timestamp_source": "Hub tag_last_pushed when present, otherwise observed last_updated; this is tag publication time, NOT a verified image build time",
            "collision_suffix": "stable -2, -3, ... ordered by manifest digest hex when distinct digests share a readable base alias",
            "provenance": "commit id and short sha stay in the mapping and the retained original sha-<40hex> ref, never in a user-facing alias",
            "immutability": "new alias at the exact existing manifest digest; sha refs and channel aliases are untouched",
        },
        "aliases": aliases,
        "counts": {
            "source_tags": len(entries),
            "distinct_digests": len(aliases),
            "aliases": len(aliases),
            "digest_collision_groups": sum(1 for group in by_digest.values() if len(group) > 1),
            "same_second_collision_groups": sum(
                1 for group in by_base.values() if len(group) > 1),
        },
    }


def load_entries(path):
    """Read raw sha-tag entries from an inventory or mapping JSON file."""
    document = json.loads(Path(path).read_text(encoding="utf-8"))
    entries = document.get("original_sha_tags") or document.get("entries") or document
    if not isinstance(entries, list):
        raise ValueError("no sha-tag entries found in " + str(path))
    return entries, document


def verify_mapping(mapping, entries=None):
    """Recompute a mapping and require exact equality of alias identities."""
    if entries is None:
        entries = mapping.get("original_sha_tags")
        if not entries:
            entries, _ = load_entries(_mapping_input_path(mapping))
    expected = build_alias_map(entries)
    actual = [(item["final_alias"], item["digest"]) for item in mapping["aliases"]]
    wanted = [(item["final_alias"], item["digest"]) for item in expected["aliases"]]
    if actual != wanted:
        raise ValueError("mapping does not match deterministic recomputation")
    return True


def _mapping_input_path(mapping):
    source = mapping.get("source_inventory")
    if not source:
        raise ValueError("mapping needs source_inventory for verification")
    return source


def alias_publication_plan(mapping, observer=None):
    """Strict preflight for every alias. Returns (plan, problems).

    Problems are collected before any write so a conflict or unknown lookup
    refuses with ZERO registry writes.
    """
    plan, problems = [], []
    observer = observer or (lambda ref, strict=False: release.observe_reference(ref, strict=strict))
    for item in mapping["aliases"]:
        commit = item["commit_id"]
        source_ref = f"{DOCKER_HUB_IMAGE}:sha-{commit}"
        target_ref = f"{DOCKER_HUB_IMAGE}:{item['final_alias']}"
        if item["final_alias"] in CHANNEL_TAGS:
            problems.append("refusing to write a channel alias: " + item["final_alias"])
            continue
        source = observer(source_ref, strict=True)
        if source[0] != "present":
            problems.append(f"source {source_ref} is {source[0]}")
            continue
        if source[1]["digest"] != item["digest"]:
            problems.append(f"source {source_ref} digest conflicts with the mapping")
            continue
        if not REQUIRED_PLATFORMS <= source[1]["platforms"]:
            problems.append(f"source {source_ref} lacks both Linux architectures")
            continue
        target = observer(target_ref, strict=True)
        if target[0] == "present":
            if target[1]["digest"] != item["digest"]:
                problems.append(f"alias {target_ref} exists with a conflicting digest")
                continue
            action = "reuse"
        elif target[0] == "unknown":
            problems.append(f"alias {target_ref} lookup is unknown")
            continue
        else:
            action = "create"
        plan.append({"alias": item["final_alias"], "target_ref": target_ref,
                     "digest": item["digest"], "action": action})
    return plan, problems


def apply_aliases(mapping, observer=None, runner=None, apply=False):
    """Preflight all aliases, then add only missing ones by exact digest."""
    plan, problems = alias_publication_plan(mapping, observer)
    if problems:
        raise ValueError("Alias publication refused: " + "; ".join(problems))
    runner = runner or (lambda *args, **kwargs: subprocess.run(args, check=True, **kwargs))
    if not apply:
        return plan
    for item in plan:
        if item["action"] == "reuse":
            continue
        # imagetools create only adds a tag to an existing digest: no rebuild,
        # no platform change, no layer push and no channel alias move.
        source = f"{DOCKER_HUB_IMAGE}@{item['digest']}"
        runner("docker", "buildx", "imagetools", "create",
               "--tag", item["target_ref"], source)
    return plan


def write_output(values):
    if release and hasattr(release, "write_output"):
        release.write_output(values)


def publication_gate(event, confirm, ref, ci_verified):
    """Reuse the existing trusted-main developer gate for alias additions."""
    return release.developer_channel_decision(
        event, confirm, ref=ref, ci_verified=ci_verified)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    mapper = commands.add_parser("map")
    mapper.add_argument("--input", required=True)
    mapper.add_argument("--output")
    checker = commands.add_parser("validate")
    checker.add_argument("--input", required=True)
    publisher = commands.add_parser("apply")
    publisher.add_argument("--mapping", required=True)
    publisher.add_argument("--dry-run", action="store_true")
    gate = commands.add_parser("gate")
    gate.add_argument("--event", required=True)
    gate.add_argument("--confirm", default="")
    gate.add_argument("--ref", default="")
    gate.add_argument("--ci-verified", default="")
    args = parser.parse_args(argv if argv is not None else sys.argv[1:])

    if args.command == "map":
        entries, _ = load_entries(args.input)
        mapping = build_alias_map(entries)
        text = json.dumps(mapping, indent=2) + "\n"
        if args.output:
            Path(args.output).write_text(text, encoding="utf-8")
        print(text, end="")
        return mapping
    if args.command == "validate":
        mapping = json.loads(Path(args.input).read_text(encoding="utf-8"))
        verify_mapping(mapping)
        print(json.dumps({"valid": True, "aliases": len(mapping["aliases"])}, indent=2))
        return True
    if args.command == "apply":
        mapping = json.loads(Path(args.mapping).read_text(encoding="utf-8"))
        verify_mapping(mapping)
        plan = apply_aliases(mapping, apply=not args.dry_run)
        values = {"planned": len(plan),
                  "create": sum(1 for item in plan if item["action"] == "create"),
                  "reuse": sum(1 for item in plan if item["action"] == "reuse")}
        write_output(values)
        print(json.dumps(values, indent=2))
        return plan
    if args.command == "gate":
        allowed, reason = publication_gate(
            args.event, args.confirm, args.ref, args.ci_verified)
        # A refusal is a successful dry-run decision, not a job failure: the
        # apply step still runs with --dry-run and performs no registry write.
        values = {"publish": str(allowed).lower(), "reason": reason}
        write_output(values)
        print(json.dumps(values, indent=2))
        return allowed
    return None


if __name__ == "__main__":
    main()
