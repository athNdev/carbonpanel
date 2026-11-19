#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Build, inspect and publish versioned Carbon Panel release bundles."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from datetime import datetime, timezone
import zipfile

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))
from scripts.openapi_version import verify_protocol_v1, verify_text

ROOT = _ROOT
IMAGE = "atharvnaphade/carbonpanel"
TARGETS = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"),
           ("darwin", "arm64"), ("windows", "amd64"))
SEMVER = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")


def version(value):
    match = SEMVER.fullmatch(value)
    if not match or any(part.isdigit() and len(part) > 1 and part[0] == "0"
                        for part in (match[4] or "").split(".")):
        raise ValueError("Use vMAJOR.MINOR.PATCH or a SemVer prerelease, such as v3.0.0-alpha.1")
    return value


def run(*args, **kwargs):
    kwargs.setdefault("cwd", ROOT)
    return subprocess.check_output(args, text=True, **kwargs).strip()


def write_output(values):
    """Emit step outputs only when running inside Actions."""
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
            output.writelines(f"{key}={value}\n" for key, value in values.items())


def archive_name(tag, goos, goarch):
    return f"carbon-panel_{version(tag)}_{goos}_{goarch}" + (".zip" if goos == "windows" else ".tar.gz")


DOCKER_HUB_IMAGE = "atharvnaphade/carbonpanel"
VERIFY_TAG = re.compile(r"v0\.0\.0-verify\.\d+")
MODULE_TAG = re.compile(
    r"mod-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")
# Rehearsal, test and documentation namespaces are never publishable versions.
RESERVED_PRERELEASE = frozenset(
    {"verify", "verification", "test", "testing", "dryrun", "dry-run", "docs", "doc"})

# --- Carbon Panel product release tag domain -------------------------------
# Product tags are SEPARATE from plain v* Go/module tags and from archive
# filenames. The Git tag carries the `carbonpanel-v` prefix; the user-facing
# SemVer and the immutable image tag are MAJOR.MINOR.PATCH with the same
# optional alpha/beta/rc suffix. Keep both fields instead of guessing one from
# the other. See docs/releases.md, docs/release-history.md and ADR 0033.
PRODUCT_PREFIX = "carbonpanel-v"
PRODUCT_PRERELEASE = frozenset({"alpha", "beta", "rc"})
PRODUCT_TAG = re.compile(
    r"carbonpanel-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-(alpha|beta|rc)\.(0|[1-9][0-9]*))?")

# The only verified historical stable release in athNdev/carbonpanel. It is
# recognised for monotonic promotion and rollback, never republished or
# retargeted. The next planned stable is the MAJOR 2.0.0 (see UPGRADE NOTES in
# docs/releases.md): three intentional operator changes break an unchanged
# v1.0.40 config, so a compatible MINOR would be dishonest. The 1.0 alias stays
# at 1.0.40 today; a future 1.0.x maintenance patch is not permanently banned
# and may move that alias only through the normal explicit promotion path.
LEGACY_STABLE_TAG = "v1.0.40"
LEGACY_STABLE_COMMIT = "f88748be6a216b187e675bbb1f110df07e544148"
LEGACY_STABLE_DIGEST = (
    "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001")
ROLLBACK_MINOR = "1.0"
ROLLBACK_VERSION = LEGACY_STABLE_TAG

# Inherited tag names are historical refs, never panel publication candidates.
# v1.0.38 and v1.0.39 are source/module history; v3.0.0-alpha.1 is a GHCR-only
# legacy preview; v3.0.0-beta.4 is a cross-repository provenance collision;
# v3.0.0-beta.5-.8 are upstream-only discopanel prereleases.
SOURCE_ONLY_TAGS = frozenset({"v1.0.38", "v1.0.39"})
LEGACY_PREVIEW_TAG = "v3.0.0-alpha.1"
NAME_COLLISION_TAG = "v3.0.0-beta.4"
UPSTREAM_ONLY_TAGS = frozenset({
    "v3.0.0-beta.5", "v3.0.0-beta.6", "v3.0.0-beta.7", "v3.0.0-beta.8"})

# The canonical annotated alias planned at the legacy stable commit is a
# HISTORICAL reference. It is valid for provenance, recovery and rollback, but
# it must NEVER start a new release attempt: that would republish 1.0.40 under
# a second name. This is enforced in publication_candidate_decision() before
# any immutable registry preflight runs.
HISTORICAL_PRODUCT_ALIAS = "carbonpanel-v1.0.40"

# Product tags may not reuse a legacy preview/collision version number, even
# with a different Git tag. These are reserved historical identities.
RESERVED_LEGACY_SEMVERS = frozenset({
    "1.0.40", "3.0.0-alpha.1", "3.0.0-beta.4",
    "3.0.0-beta.5", "3.0.0-beta.6", "3.0.0-beta.7", "3.0.0-beta.8"})

# The MAJOR boundary for the next stable. Kept as data so docs, tests and the
# release history catalog cannot drift from the policy.
NEXT_STABLE_GIT_TAG = "carbonpanel-v2.0.0"
NEXT_STABLE_SEMVER = "2.0.0"
NEXT_STABLE_MINOR = "2.0"


def product_release(value):
    """Parse a canonical product tag into its separate Git/SemVer fields."""
    match = PRODUCT_TAG.fullmatch(value or "")
    if not match:
        raise ValueError(
            "Use carbonpanel-vMAJOR.MINOR.PATCH[-alpha.N|-beta.N|-rc.N] for a product tag")
    core = ".".join(match.group(1, 2, 3))
    prerelease = match.group(4)
    semver = core + ("-" + prerelease + "." + match.group(5) if prerelease else "")
    return {
        "git_tag": value,
        "version": "v" + semver,          # internal form for existing tooling
        "semver": semver,                 # user-facing / immutable image tag
        "prerelease": bool(prerelease),
        "channel": "prerelease" if prerelease else "stable",
        "minor": ".".join(core.split(".")[:2]),
    }


def product_release_or_none(value):
    try:
        return product_release(value)
    except (ValueError, TypeError):
        return None


def resolve_panel_version(value):
    """Resolve an input ref to the internal vMAJOR.MINOR.PATCH form.

    Only product tags, the verified legacy stable and internal v-forms (used by
    rehearsals, fixtures and historical staging) are accepted. Publication is
    gated separately by publication_allowed(), which accepts product tags only.
    """
    product = product_release_or_none(value)
    if product:
        return product["version"]
    if value == LEGACY_STABLE_TAG:
        return value
    return version(value)


def display_version(value):
    """The one user-facing/build SemVer form, with no leading `v`.

    Product tags expose their SemVer field; rehearsal, verification and plain
    v* staging inputs strip the single leading `v`. This is the SAME form as the
    plan `semver` output used for APP_VERSION, UI embedding and OCI labels, so
    package() searches the UI for exactly the string the frontend embedded.
    Archive filenames and BUILD-INFO keep their internal v-form separately.
    """
    product = product_release_or_none(value)
    if product:
        return product["semver"]
    if isinstance(value, str) and value.startswith("v"):
        return value[1:]
    return value


def version_from_release_tag(name):
    """Map a published release tag to an internal panel version, or None.

    Inherited v*, module and documentation tags are not panel releases. A
    product tag and the verified legacy stable are the only recognised inputs.
    """
    product = product_release_or_none(name)
    if product:
        return product["version"]
    return name if name == LEGACY_STABLE_TAG else None


def rollback_line():
    """The immutable 1.0 rollback reference; never advanced by a new release."""
    return {"minor": ROLLBACK_MINOR, "version": ROLLBACK_VERSION,
            "semver": ROLLBACK_VERSION[1:], "commit": LEGACY_STABLE_COMMIT,
            "digest": LEGACY_STABLE_DIGEST}


def rollback_line_guard(value):
    """Resolve a version for promotion without imposing a permanent ban.

    The 1.0 alias currently points at 1.0.40, but that is a fact about today's
    published state, not a product decision to forbid future 1.0.x maintenance.
    A later 1.0.x patch may move the 1.0 alias through the ordinary explicit,
    monotonic promotion path. Historical-only refs are still refused at the
    publication boundary by publication_candidate_decision().
    """
    return resolve_panel_version(value)


HISTORY_PATH = ROOT / "docs/release-history.json"
HISTORY_CATEGORIES = frozenset({
    "upstream-identical", "upstream-name-collision", "carbonpanel-published-stable",
    "carbonpanel-source-only", "carbonpanel-legacy-preview", "historical-component",
    "unknown",
})
HISTORY_PROVENANCE = frozenset({
    "upstream-identical", "upstream-name-collision", "origin-only", "unknown",
})


def load_release_history():
    return json.loads(HISTORY_PATH.read_text(encoding="utf-8"))


def verify_release_history(document=None):
    """Validate the checked-in retrospective catalog without network access."""
    doc = document if document is not None else load_release_history()
    problems = []
    tags = doc.get("tags", [])
    if len(tags) != 84 or len({item["name"] for item in tags}) != 84:
        problems.append("expected 84 unique tag records")
    categories = {}
    provenance = {}
    for item in tags:
        if item.get("classification") not in HISTORY_CATEGORIES:
            problems.append("unknown classification for " + item.get("name", "?"))
        if item.get("provenance") not in HISTORY_PROVENANCE:
            problems.append("unknown provenance for " + item.get("name", "?"))
        if item.get("domain") not in {"panel-release", "go-module", "module-image",
                                      "docs", "inherited", "product-history",
                                      "unknown"}:
            problems.append("unknown domain for " + item.get("name", "?"))
        if item.get("publishable") is not False:
            problems.append("historical tag marked publishable: " + item.get("name", "?"))
        categories[item.get("classification")] = categories.get(
            item.get("classification"), 0) + 1
        provenance[item.get("provenance")] = provenance.get(
            item.get("provenance"), 0) + 1
    # Counts follow the independent provenance audit (migration-provenance-audit.md
    # section 5): origin/rm are historical components, not upstream-identical
    # product history. 57 + 22 + 1 + 1 + 2 + 1 = 84 origin tags.
    expected_categories = {
        "upstream-identical": 57, "historical-component": 22,
        "upstream-name-collision": 1, "carbonpanel-published-stable": 1,
        "carbonpanel-source-only": 2, "carbonpanel-legacy-preview": 1,
    }
    if categories != expected_categories:
        problems.append("category counts differ from the first-party inventory: "
                        + repr(categories))
    expected_provenance = {"upstream-identical": 79,
                           "upstream-name-collision": 1, "origin-only": 4}
    if provenance != expected_provenance:
        problems.append("provenance counts differ from the first-party inventory: "
                        + repr(provenance))
    stable = doc.get("canonical_stable", {})
    if (stable.get("git_tag"), stable.get("version"), stable.get("commit"),
            stable.get("image_digest")) != (
            LEGACY_STABLE_TAG, LEGACY_STABLE_TAG, LEGACY_STABLE_COMMIT,
            LEGACY_STABLE_DIGEST):
        problems.append("canonical stable identity does not match v1.0.40")
    rollback = doc.get("rollback_line", {})
    if (rollback.get("minor"), rollback.get("version")) != (ROLLBACK_MINOR, ROLLBACK_VERSION):
        problems.append("rollback line is not the frozen 1.0.40")
    if doc.get("build_aliases", {}).get("count") != 19:
        problems.append("expected the 19 preserved Docker Hub build aliases")
    if doc.get("invented_registry_alias") is not False:
        problems.append("no historical registry alias may be invented")
    # The MAJOR boundary is data, not prose drift.
    nxt = doc.get("next_stable", {})
    if (nxt.get("git_tag"), nxt.get("semver"), nxt.get("minor_alias"),
            nxt.get("boundary")) != (
            NEXT_STABLE_GIT_TAG, NEXT_STABLE_SEMVER, NEXT_STABLE_MINOR, "major"):
        problems.append("planned next stable must be carbonpanel-v2.0.0 (MAJOR)")
    alias = doc.get("historical_alias", {})
    if (alias.get("git_tag") != HISTORICAL_PRODUCT_ALIAS
            or alias.get("commit") != LEGACY_STABLE_COMMIT
            or alias.get("classification") != "historical-only"
            or alias.get("publishable") is not False
            or alias.get("duplicate_release") is not False):
        problems.append("historical carbonpanel-v1.0.40 alias is not historical-only")
    upstream = {item.get("name") for item in doc.get("upstream_only_tags", [])}
    if upstream != UPSTREAM_ONLY_TAGS:
        problems.append("the four upstream-only discopanel beta tags must be recorded")
    if stable.get("release_asset_count") != 8:
        problems.append("the v1.0.40 release has 8 verified assets")
    if stable.get("manifest_asset") != "release-manifest.json":
        problems.append("release-manifest.json is the commit/digest anchor")
    if not doc.get("unknown_boundaries"):
        problems.append("UNKNOWN provenance boundaries must be recorded explicitly")
    return (not problems), problems


def ghcr_repository(repository=None):
    """GHCR namespace is the lowercased GitHub owner/repo. It is never a Docker
    Hub namespace mapping and never contains a credential."""
    return (repository or os.environ.get("GITHUB_REPOSITORY", "athNdev/carbonpanel")).lower()


def github_image(tag, repository=None):
    return "ghcr.io/" + ghcr_repository(repository) + ":" + version(tag)[1:]


def module_version(value):
    """Modules use a separate version domain: mod-vMAJOR.MINOR.PATCH."""
    match = MODULE_TAG.fullmatch(value)
    if not match or any(part.isdigit() and len(part) > 1 and part[0] == "0"
                        for part in (match[4] or "").split(".")):
        raise ValueError("Use mod-vMAJOR.MINOR.PATCH for module image versions")
    return value


def is_verification_tag(tag):
    """True for the reserved rehearsal/test namespaces, which never publish."""
    if VERIFY_TAG.fullmatch(tag):
        return True
    match = SEMVER.fullmatch(tag)
    return bool(match and match[4] and
                any(part.lower() in RESERVED_PRERELEASE for part in match[4].split(".")))


def ref_channel(ref_name, ref_type="tag"):
    """Classify a ref into its durable release domain."""
    if ref_type != "tag":
        return "development"
    if ref_name.startswith("mod-"):
        return "module"
    if ref_name.startswith("docs"):
        return "documentation"
    if is_verification_tag(ref_name):
        return "verification"
    product = product_release_or_none(ref_name)
    if product:
        return product["channel"]
    # Plain v* refs are the Go/legacy application domain. They are preserved
    # as historical refs but never infer a new panel release.
    if ref_name.startswith("v"):
        return "legacy-version"
    return "other"


def publication_candidate_decision(version_value):
    """Classify a ref BEFORE any preflight or registry read.

    Returns (ok, reason). Only a canonical, non-historical product tag is a
    new-release candidate. This is deliberately earlier than immutable digest
    preflight so an inherited or historical tag can never be used to start a
    release attempt, even by a dispatch that sets publish=true.
    """
    if is_verification_tag(version_value):
        return False, "verification and test tags are never published"
    value = version_value or ""
    if value == HISTORICAL_PRODUCT_ALIAS:
        return (False, "carbonpanel-v1.0.40 is a HISTORICAL-ONLY alias of the "
                "verified v1.0.40 release; it cannot start a new release")
    if value in SOURCE_ONLY_TAGS:
        return False, f"{value} is source/module history only; it is not a panel release"
    if value == LEGACY_PREVIEW_TAG:
        return (False, f"{value} is the GHCR-only legacy preview; its version "
                "number is reserved and cannot be reused for a product release")
    if value == NAME_COLLISION_TAG:
        return (False, f"{value} is an upstream name-collision tag retained as "
                "history; it cannot start a panel release")
    if value in UPSTREAM_ONLY_TAGS:
        return False, f"{value} is an upstream-only discopanel prerelease, not a panel release"
    product = product_release_or_none(value)
    if product is None:
        if value.startswith("v"):
            return False, f"{value} is an inherited/plain v* tag; product tags use the carbonpanel-v prefix"
        return False, "only carbonpanel-vMAJOR.MINOR.PATCH product tags publish panel releases"
    if product["semver"] in RESERVED_LEGACY_SEMVERS:
        return (False, f"{value} reuses reserved legacy version {product['semver']}; "
                "choose a new version")
    return True, "canonical, non-historical product tag"


def publication_allowed(event_name, publish_requested, version_value):
    """The single auditable publication boundary.

    Only an explicit operator `workflow_dispatch` that requests publication may
    write to a registry or GitHub release. Branch pushes, product/legacy tag
    pushes, pull requests, workflow_run and every other automatic event only
    stage, build and verify. Publication additionally requires a canonical,
    non-historical `carbonpanel-vMAJOR.MINOR.PATCH[-alpha.N|-beta.N|-rc.N]` tag:
    inherited v* tags, the historical 1.0 alias, source-only versions, reserved
    legacy previews/collisions and archive filenames are refused even with the
    opt-in, and before any immutable registry preflight.
    """
    if event_name != "workflow_dispatch":
        return False, f"{event_name} events stage only; publication needs an explicit workflow_dispatch"
    if str(publish_requested).strip().lower() != "true":
        return False, "publication needs publish=true on workflow_dispatch"
    ok, reason = publication_candidate_decision(version_value)
    if not ok:
        return False, reason
    return True, "explicit workflow_dispatch publish opt-in for a canonical product tag"


def publication_ref_decision(event_name, ref, ci_verified):
    """A panel publication requires a trusted default-branch ref and real CI."""
    if event_name != "workflow_dispatch":
        return False, f"{event_name} is a build-only event"
    if ref != TRUSTED_DEVELOPER_REF:
        return False, "publication requires the trusted refs/heads/main dispatch ref"
    if str(ci_verified).strip().lower() != "true":
        return False, "exact main CI must be verified before publication"
    return True, "trusted main dispatch with verified exact-commit CI"


def promotion_plan(version_value):
    """Ordered tag destinations. The immutable full version always precedes a
    moving alias; prereleases publish no stable alias at all. The current 1.0
    rollback line is 1.0.40; a future 1.0.x patch may move `1.0` only through
    ordinary explicit monotonic promotion.
    """
    tag = rollback_line_guard(version_value)
    plain = tag[1:]
    prerelease = "-" in tag
    plan = [{"tag": plain, "mutable": False,
             "channel": "prerelease" if prerelease else "stable", "step": 1}]
    if not prerelease:
        major_minor = ".".join(plain.split("-", 1)[0].split(".")[:2])
        plan.append({"tag": major_minor, "mutable": True, "channel": "stable", "step": 2})
        plan.append({"tag": "latest", "mutable": True, "channel": "stable", "step": 3})
    return plan


def module_promotion_plan(value):
    """Module images have their own immutable version domain; `latest` is a
    documented moving alias that moves only under explicit promotion."""
    plain = module_version(value)[len("mod-"):]
    return [{"tag": plain, "mutable": False, "channel": "module", "step": 1},
            {"tag": "latest", "mutable": True, "channel": "module", "step": 2}]


def missing_publish_credentials(environ, registries=("docker_hub", "ghcr")):
    """Presence-only credential check; secret values are never read into output."""
    missing = []
    if "docker_hub" in registries:
        user = bool(environ.get("DOCKER_HUB_USERNAME") or environ.get("DOCKERHUB_USERNAME"))
        token = bool(environ.get("DOCKER_HUB_PASSWORD") or environ.get("DOCKERHUB_TOKEN"))
        if not (user and token):
            missing.append("docker_hub")
    if "ghcr" in registries and not environ.get("GITHUB_TOKEN"):
        missing.append("ghcr")
    return sorted(missing)


# --- Registry publication state machine ------------------------------------
# The workflow never guesses registry state. Every observation is classified as
# absent, present or unknown, and any unknown/conflicting/mixed state refuses.

REQUIRED_PLATFORMS = frozenset({("linux", "amd64"), ("linux", "arm64")})
IMMUTABLE_PUBLISH = "publish"
IMMUTABLE_RESUME = "resume"
IMMUTABLE_REFUSE = "refuse"


class RegistryError(Exception):
    """Registry state could not be determined; publication must refuse."""


def valid_digest(value):
    return bool(value and re.fullmatch(r"sha256:[0-9a-f]{64}", value))


def inspect_runner(*args):
    result = subprocess.run(args, text=True, capture_output=True, cwd=ROOT)
    if result.returncode:
        raise subprocess.CalledProcessError(result.returncode, args,
                                            output=result.stdout, stderr=result.stderr)
    return result.stdout


def parse_inspect(text):
    """Parse `docker buildx imagetools inspect` output into a digest/platform set."""
    found = re.search(r"(?m)^Digest:\s+(sha256:[0-9a-f]{64})\s*$", text)
    if not found:
        raise RegistryError("Image inspect output has no digest")
    platforms = {("linux", arch) for arch in re.findall(
        r"(?m)^\s+Platform:\s+linux/(amd64|arm64)\b", text)}
    return {"digest": found.group(1), "platforms": platforms}


# Only an unambiguous registry "manifest unknown" is a definitive absence.
# Generic 404/"not found" strings also occur on auth, network and transient
# failures, so publication paths (strict=True) refuse those as unknown.
DEFINITIVE_MISSING = ("manifest unknown", "no such manifest", "unknown manifest")
# Real read-only probe (2026-11-18):
#   ERROR: docker.io/atharvnaphade/carbonpanel:<tag>: not found
DEFINITIVE_NOT_FOUND = re.compile(r"(?m)^\s*error:.*:\s*not found\s*$")
AMBIGUOUS_FAILURE = ("unauthorized", "denied", "forbidden", "connection",
                    "timeout", "temporar", "server error", "unavailable")


def classify_lookup_failure(error, strict=False, reference=None):
    """Classify a failed image lookup as 'absent' or 'unknown'.

    Strict classification is scoped to the QUERIED reference: a not-found line
    for another image, or a generic/transient error, is unknown. Non-strict
    callers keep the historical broad matching.
    """
    message = (str(getattr(error, "stderr", "") or "")
               + " " + str(getattr(error, "output", "") or "")).lower()
    if strict:
        if any(token in message for token in AMBIGUOUS_FAILURE):
            return "unknown"
        definitive = any(token in message for token in DEFINITIVE_MISSING)
        if not definitive:
            if reference:
                pattern = (r"(?m)^\s*error:\s*(?:docker\.io/)?"
                           + re.escape(reference.lower()) + r":\s*not found\s*$")
                definitive = bool(re.search(pattern, message))
            else:
                definitive = bool(DEFINITIVE_NOT_FOUND.search(message))
        return "absent" if definitive else "unknown"
    if any(token in message for token in
           ("not found", "no such", "does not exist", "manifest unknown", "404")):
        return "absent"
    return "unknown"


def observe_reference(reference, runner=None, strict=False):
    """Return (state, info): 'absent', 'present' or 'unknown'.

    Publication paths pass strict=True; an ambiguous lookup is unknown and
    fails closed rather than being treated as a missing tag.
    """
    try:
        text = (runner or inspect_runner)(
            "docker", "buildx", "imagetools", "inspect", reference)
    except subprocess.CalledProcessError as error:
        return classify_lookup_failure(error, strict, reference), None
    except OSError as error:
        return classify_lookup_failure(error, strict, reference), None
    try:
        return "present", parse_inspect(text)
    except RegistryError:
        return "unknown", None


def manifest_platforms(reference, runner=None, anchor_digest=None):
    """Read the real Buildx index: (index_digest, {(os, arch): child_digest}).

    When anchor_digest is supplied the index is fetched from `reference@digest`,
    never from the mutable tag, so a retag between observation and evidence
    cannot change what is verified. Attestation descriptors are ignored.
    """
    target = f"{reference}@{anchor_digest}" if anchor_digest else reference
    try:
        raw = (runner or inspect_runner)(
            "docker", "buildx", "imagetools", "inspect", target,
            "--format", "{{json .Manifest}}")
        document = json.loads(raw)
    except (subprocess.CalledProcessError, OSError, ValueError, TypeError):
        return "", {}
    platforms = {}
    for item in document.get("manifests", []):
        platform = item.get("platform") or {}
        key = (platform.get("os"), platform.get("architecture"))
        if key in REQUIRED_PLATFORMS and item.get("digest"):
            platforms[key] = item["digest"]
    return document.get("digest", ""), platforms


def platform_manifest_revisions(reference, runner=None, anchor_digest=None):
    """Read build-label revisions from BOTH target platform image configs.

    Both the index and each child config are fetched by digest when anchored,
    never from a mutable tag. Returns (index_digest,
    {(os, arch): {"digest": child_digest, "revision": label}}).
    Missing or unreadable data yields empty maps so callers fail closed.
    """
    index_digest, platforms = manifest_platforms(reference, runner, anchor_digest)
    result = {}
    for key, child in platforms.items():
        try:
            raw = (runner or inspect_runner)(
                "docker", "buildx", "imagetools", "inspect",
                f"{reference}@{child}", "--format", "{{json .Image}}")
            image = json.loads(raw)
        except (subprocess.CalledProcessError, OSError, ValueError, TypeError):
            continue
        labels = (image.get("config") or {}).get("Labels") or {}
        revision = (labels.get("org.opencontainers.image.revision")
                    or labels.get("revision") or "")
        result[key] = {"digest": child, "revision": revision}
    return index_digest, result


def verify_sha_identity(commit, digest, observation, revisions, index_digest=None):
    """Source-based identity check without CI: index digest, both platform
    child digests and their config.Labels revision must match the commit.
    An observed index digest is honoured and a mismatch refuses.
    """
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or not valid_digest(digest):
        return False, "invalid commit or digest"
    if index_digest and index_digest != digest:
        return False, "anchored index digest does not match the observed image digest"
    try:
        verify_tag_digest(observation, digest)
    except ValueError as error:
        return False, "immutable image verification failed: " + str(error)
    for platform in sorted(REQUIRED_PLATFORMS):
        value = revisions.get(platform)
        if not isinstance(value, dict) or not valid_digest(value.get("digest")):
            return False, f"missing {platform[0]}/{platform[1]} platform manifest digest"
        if value.get("revision") != commit:
            return False, f"config revision on {platform[0]}/{platform[1]} does not match the commit"
    return True, "sha identity verified from platform configs"


def trusted_pinned_evidence(commit, digest, ci_verified, observation, revisions,
                            index_digest=None):
    """Verify independent pinned-build evidence before moving a shared alias.

    Requires exact-commit CI confirmation, the observed index digest, both
    target platform child digests and their config.Labels revisions equal to
    the pinned commit. Descriptor annotations are never treated as evidence.
    """
    if not ci_verified or str(ci_verified).strip().lower() != "true":
        return False, "exact successful CI for the pinned commit is required"
    return verify_sha_identity(commit, digest, observation, revisions, index_digest)


def immutable_publication_decision(observations, expected_digest=None, provenance=None):
    """Decide the immutable-version action across every intended registry.

    observations: {registry: (state, info)} from observe_reference().
    expected_digest: operator-supplied verified digest for an identical resume.
    provenance: (ok, reason) from resume_provenance for the exact commit/version.
    Returns (action, reason). Unknown errors, conflicts, mixed states and
    unverified provenance all refuse.
    """
    states = {state for state, _ in observations.values()}
    if "unknown" in states:
        return IMMUTABLE_REFUSE, "registry state is unknown; refusing"
    if states == {"absent"}:
        if expected_digest:
            return IMMUTABLE_REFUSE, "no registry has the expected digest; cannot resume"
        return IMMUTABLE_PUBLISH, "immutable version is absent in every registry"
    if states != {"present"}:
        return IMMUTABLE_REFUSE, "partial publication state; operator intervention required"
    if not valid_digest(expected_digest):
        return IMMUTABLE_REFUSE, "version tag already exists; supply a verified digest to resume"
    if not provenance or not provenance[0]:
        detail = provenance[1] if provenance else "not supplied"
        return IMMUTABLE_REFUSE, "resume requires verified provenance: " + detail
    for name, (_, info) in observations.items():
        if info["digest"] != expected_digest:
            return IMMUTABLE_REFUSE, f"{name} digest differs from the expected digest"
        if not REQUIRED_PLATFORMS <= info["platforms"]:
            return IMMUTABLE_REFUSE, f"{name} lacks both Linux architectures"
    return IMMUTABLE_RESUME, "identical verified digest in every registry; resume"


def verify_tag_digest(observation, expected_digest):
    """Verify the expected tag→digest mapping and both architectures."""
    state, info = observation
    if state != "present":
        raise ValueError("Version tag is absent after publication")
    if not valid_digest(expected_digest):
        raise ValueError("Missing or invalid expected digest")
    if info["digest"] != expected_digest:
        raise ValueError("Version tag digest differs from the published image")
    if not REQUIRED_PLATFORMS <= info["platforms"]:
        raise ValueError("Version tag lacks both Linux architectures")
    return True


def semver_key(tag):
    """Full SemVer precedence key, including prerelease ordering."""
    match = SEMVER.fullmatch(version(tag))
    core = tuple(int(match.group(index)) for index in (1, 2, 3))
    prerelease = match.group(4)
    if prerelease is None:
        return core + (1, ())
    identifiers = tuple((0, int(part), "") if part.isdigit() else (1, 0, part)
                        for part in prerelease.split("."))
    return core + (0, identifiers)


def highest_stable(versions):
    stable = [value for value in versions if "-" not in value]
    return max(stable, key=semver_key) if stable else None


def alias_promotion_decision(candidate, published_versions):
    """Moving aliases advance only to a version not older than the highest
    published stable version. Prereleases promote nothing; an unavailable
    published state omits automated promotion instead of guessing.
    """
    tag = version(candidate)
    if "-" in tag:
        return None, "prereleases do not move stable aliases"
    if published_versions is None:
        return None, "published version state is unavailable; skip automated alias promotion"
    current = highest_stable(published_versions)
    if current and semver_key(tag) < semver_key(current):
        return None, f"{tag} is older than published {current}; alias promotion refused"
    return True, f"{tag} is not older than published {current or 'none'}"


def github_latest_decision(tag, published_versions):
    """GitHub 'latest' follows the SAME product-only verified stable ordering as
    registry promotion. A prerelease never promotes; an older maintenance
    release must not displace a newer verified stable; and an undeterminable
    published state fails closed by leaving `latest` unchanged.
    """
    internal = version(tag)
    if "-" in internal:
        return False, "prereleases never become GitHub latest"
    if published_versions is None:
        return False, "published stable state is unavailable; do not move GitHub latest"
    current = highest_stable(published_versions)
    if current and semver_key(internal) <= semver_key(current):
        return (False, f"{internal} is not newer than verified stable {current}; "
                "GitHub latest stays unchanged")
    return True, f"{internal} is newer than verified stable {current or 'none'}"


def verified_previous_release_tag(releases_json=None):
    """Actual GitHub tag name of the highest VERIFIED stable product/legacy
    release, for an explicit compare base. Inherited v* tags are filtered out by
    version_from_release_tag; no generic tag-name sorting is used.
    """
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        candidates = []
        for page in data:
            for item in page:
                if item.get("draft") or item.get("prerelease"):
                    continue
                name = item.get("tag_name", "")
                if version_from_release_tag(name):
                    candidates.append(name)
        if not candidates:
            return None
        return max(candidates, key=lambda name: semver_key(
            version_from_release_tag(name)))
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError):
        return None


def migration_guide_notes(repository, git_tag):
    """User-facing UPGRADE NOTES block for the first MAJOR product stable."""
    guide = f"https://github.com/{repository}/blob/{git_tag}/docs/releases.md#upgrade-notes-from-v1040"
    history = f"https://github.com/{repository}/blob/{git_tag}/docs/release-history.md#upgrade-notes--read-before-upgrading-from-v1040"
    return (
        "## UPGRADE NOTES — read before upgrading from v1.0.40\n\n"
        f"**User-facing migration guide: {guide}** (full provenance and evidence: {history}).\n\n"
        "This MAJOR release has three prerequisites; complete all three before upgrading:\n\n"
        "1. **OIDC raw-group role mapping and explicit admin linking.** v1.0.40's "
        "`role_mapping: {}` example granted raw IdP group names directly. Raw claims are no "
        "longer granted by default and privileged names are always refused, so add an explicit "
        "`auth.oidc.role_mapping` entry for every retained role (including admin). There is no "
        "implicit first-user admin: keep a local administrator and explicitly link each admin "
        "OIDC subject.\n"
        "2. **PROXY protocol trust.** PROXY v2 headers are trusted only from "
        "`proxy.trusted_proxy_cidrs` (default `[]` trusts nobody). Add every load balancer source "
        "CIDR and confirm real client IPs are preserved after upgrade.\n"
        "3. **Backup/restore drill.** Back up the database (db + wal + shm), configuration, "
        "worlds and backups, then rehearse the documented copy-back restore on disposable "
        "copies before upgrading a live installation.\n\n"
    )


def published_stable_versions(releases_json=None):
    """Stable SemVer release tags, or None when the state cannot be established."""
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        tags = []
        for page in data:
            for release in page:
                if release.get("draft") or release.get("prerelease"):
                    continue
                # Inherited/unclassified tags never count as published panel
                # versions; only product tags and the verified legacy stable do.
                candidate = version_from_release_tag(release.get("tag_name", ""))
                if candidate:
                    tags.append(candidate)
        return tags
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError):
        return None


def published_module_versions(tags=None):
    """Validated module versions, or None when the state cannot be established."""
    try:
        tags = tags if tags is not None else run("git", "tag", "--list", "mod-v*").splitlines()
        versions = []
        for tag in tags:
            try:
                versions.append(module_version(tag))
            except ValueError:
                continue
        return versions
    except subprocess.CalledProcessError:
        return None


def release_provenance(tag, commit, expected_digest, releases_json=None, git_tag=None):
    """Prove from a verified release manifest that an existing image digest is
    the build of this exact version and source commit. Returns (ok, reason,
    manifest). Unknown or mismatched provenance always fails closed.
    """
    release_tag = git_tag or tag
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        for page in data:
            for item in page:
                if item.get("tag_name") != release_tag:
                    continue
                if item.get("draft"):
                    return False, "release is still a draft; not a durable record", None
                assets = {asset["name"]: asset for asset in item.get("assets", [])}
                manifest_asset = assets.get("release-manifest.json")
                if not manifest_asset:
                    return False, "release has no release-manifest.json asset", None
                raw = github("api", "/repos/{owner}/{repo}/releases/assets/"
                             + str(manifest_asset["id"]),
                             "-H", "Accept: application/octet-stream")
                manifest = json.loads(raw)
                if manifest.get("version") != tag:
                    return False, "release manifest version mismatch", manifest
                if manifest.get("commit") != commit:
                    return False, "release manifest source commit mismatch", manifest
                if manifest.get("revision", commit) != commit:
                    return False, "release manifest revision mismatch", manifest
                if manifest.get("image_digest") != expected_digest:
                    return False, "release manifest image digest mismatch", manifest
                return True, "verified release provenance", manifest
        return False, "no release provenance for this version", None
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError, KeyError):
        return False, "release provenance unavailable; fail closed", None


def durable_release_records(releases_json=None):
    """Map verified published image digests to their release versions. Used to
    interpret an observed alias digest; an empty map means unknown, so an alias
    move must refuse rather than guess.
    """
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        records = {}
        for page in data:
            for item in page:
                if item.get("draft") or item.get("prerelease"):
                    continue
                assets = {asset["name"]: asset for asset in item.get("assets", [])}
                manifest_asset = assets.get("release-manifest.json")
                if not manifest_asset:
                    continue
                raw = github("api", "/repos/{owner}/{repo}/releases/assets/"
                             + str(manifest_asset["id"]),
                             "-H", "Accept: application/octet-stream")
                manifest = json.loads(raw)
                digest = manifest.get("image_digest")
                panel_version = version_from_release_tag(item.get("tag_name", ""))
                if valid_digest(digest) and panel_version:
                    records[digest] = panel_version
        return records
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError, KeyError):
        return {}


def release_record_decision(tag, commit, digest, releases_json=None, git_tag=None):
    """Decide how to reach a durable release record before any alias moves.
    Returns (action, reason) with action publish / verified / refuse.

    An unpublished draft is a resumable record: the publisher reuses its stable
    release ID, re-uploads the verified assets and then exposes it. Only a
    published record with matching provenance is 'verified'; a conflicting or
    unknown state refuses.
    """
    release_tag = git_tag or tag
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        matching = [item for page in data for item in page
                    if item.get("tag_name") == release_tag]
        if not matching:
            return "publish", "no release record for this version"
        if any(item.get("draft") for item in matching):
            return "publish", "unpublished draft is resumable; assets are re-verified before exposure"
        ok, reason, _ = release_provenance(tag, commit, digest, data, git_tag)
        return ("verified", reason) if ok else ("refuse", reason)
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError, KeyError):
        return "refuse", "release record state is unknown"


def alias_move_decision(candidate, candidate_digest, observations, digest_versions):
    """Fail-closed check before moving a moving alias.

    observations: {registry: (state, info)} of the current alias in each
    registry. digest_versions: digest→version from durable release records.
    Equal registry refs alone do not authorize a move, and an unknown version
    for an observed digest refuses instead of relying on release history alone.
    """
    tag = version(candidate)
    if "-" in tag:
        return False, "prereleases do not move stable aliases"
    if not valid_digest(candidate_digest):
        return False, "missing or invalid verified digest"
    for name, (state, info) in observations.items():
        if state == "unknown":
            return False, f"{name} alias state is unknown"
        if state == "absent":
            continue
        if not REQUIRED_PLATFORMS <= info["platforms"]:
            return False, f"{name} alias lacks both Linux architectures"
        current = info["digest"]
        if current == candidate_digest:
            continue  # already at the verified image; idempotent, no overwrite
        previous = digest_versions.get(current)
        if not previous:
            return False, f"{name} alias digest has no durable version provenance"
        if semver_key(tag) < semver_key(previous):
            return False, f"would move {name} alias backwards from {previous}"
    return True, "alias may advance from the verified digest"


def resume_provenance(tag, commit, expected_digest, releases_json=None, git_tag=None):
    """RESUME needs the exact tag identity plus verified release provenance,
    not merely equal tag digests and matching platform counts."""
    release_tag = git_tag or tag
    if not valid_digest(expected_digest):
        return False, "a verified expected digest is required to resume"
    try:
        local = run("git", "tag", "--list", release_tag)
        if local and run("git", "rev-parse", f"{release_tag}^{{commit}}") != commit:
            return False, "version tag points at a different commit"
    except subprocess.CalledProcessError:
        return False, "version tag identity cannot be determined"
    return release_provenance(tag, commit, expected_digest, releases_json, release_tag)[:2]


TRUSTED_DEVELOPER_REF = "refs/heads/main"


def developer_channel_decision(event_name, opt_in=False, run_event="", conclusion="",
                               ref="", ci_verified=False):
    """Decide whether a developer-channel side effect may occur.

    An ordinary verified main merge builds and verifies only. Publishing the
    Docker Hub `edge`/`sha-*` images or deploying Pages requires an explicit
    operator opt-in on workflow_dispatch FROM THE TRUSTED DEFAULT BRANCH, and
    `ci_verified` must come from exact_main_ci_verified() for the checked-out
    commit, never from a checkbox. Tags, feature/blank refs, PRs, dry runs and
    failed or non-push CI events never publish regardless of any input.
    """
    if not ref or ref.startswith("refs/tags/"):
        return False, "a trusted branch ref is required; tags and blank refs stage only"
    if event_name == "workflow_dispatch":
        if ref != TRUSTED_DEVELOPER_REF:
            return False, "developer-channel opt-in is only valid on the default branch"
        if str(opt_in).strip().lower() != "true":
            return False, "dispatch without opt-in is a dry run"
        if str(ci_verified).strip().lower() != "true":
            return False, "green main CI must be confirmed before developer publication"
        return True, "explicit trusted-branch opt-in with confirmed CI"
    if event_name == "workflow_run":
        if ref != TRUSTED_DEVELOPER_REF:
            return False, "non-default-branch CI stages only"
        if run_event == "push" and conclusion == "success":
            return False, "verified main merge stages only; publication needs an explicit opt-in"
        return False, "workflow_run from a non-push or failed CI stages only"
    if event_name == "push":
        return False, "branch push stages only"
    if event_name == "pull_request":
        return False, "pull requests stage only"
    return False, f"{event_name} stages only"


NIGHTLY_ALIAS = "nightly"


def nightly_channel_decision(event_name, opt_in=False, run_event="", conclusion="",
                             ref="", ci_verified=False):
    """Trusted-main dispatch writes require an explicit opt-in and a read-only
    exact-commit main CI verification. Every other event — main push, CI
    workflow_run, PR, tag — stages only. Native schedules use the separate,
    default-off nightly_schedule_decision() path below.
    """
    allowed, reason = developer_channel_decision(
        event_name, opt_in, run_event, conclusion, ref, ci_verified)
    if not allowed:
        return False, "nightly " + reason
    return True, "nightly opt-in with verified exact main CI"


def nightly_schedule_decision(event_name, ref="", ci_verified=False,
                              pin_stable=False, operator_setting=False):
    """Native Actions schedule gate for the nightly-only development alias.

    A scheduled run verifies by default. It may publish only when all of the
    following hold: this is a native `schedule` event, the ref is the trusted
    default branch, exact main CI is green for the pinned commit, the main tip
    was stable across reads (it did not move mid-run), and a separately
    reviewed, default-off operator setting is enabled. `push`, `pull_request`,
    `workflow_run`, `workflow_dispatch` and tag events never take this path;
    the stable/latest/edge aliases and release records are not touched here.
    """
    if event_name != "schedule":
        return False, "scheduled nightly path is schedule-only"
    if not ref or ref != TRUSTED_DEVELOPER_REF:
        return False, "scheduled nightly requires the trusted default branch"
    if str(ci_verified).strip().lower() != "true":
        return False, "scheduled nightly requires exact main CI verification"
    if str(pin_stable).strip().lower() != "true":
        return False, "scheduled nightly requires a stable main pin"
    if str(operator_setting).strip().lower() != "true":
        return False, "scheduled nightly is verify-only until the operator setting is enabled"
    return True, "scheduled trusted-main nightly authorized"


def nightly_immutable_reference(commit):
    """Nightly reuses the existing per-commit immutable image identity."""
    return f"{DOCKER_HUB_IMAGE}:sha-{commit}"


def sha_tag_decision(commit, observation, expected_digest=None, evidence=None,
                     revisions=None, ci_verified=False, index_digest=None):
    """Pre-write gate for the SHARED sha-<commit> immutable identity.

    Both edge and nightly writers share this tag. A present tag is reused only
    when its digest, both platforms and the OCI revision on both platform
    manifests match independently trusted pinned-build evidence for the exact
    commit. A bare expected digest (a second copy of the observed digest) is not
    evidence. Conflicts and unknown lookups refuse with zero writes; an absent
    tag may be built only when no expected digest was supplied.
    """
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        return "refuse", "", "the pinned commit must be a full 40-hex SHA"
    state, info = observation
    if state == "unknown":
        return "refuse", "", "sha tag lookup is unknown; refusing before any write"
    if state == "absent":
        if expected_digest:
            return "refuse", "", "an expected digest was supplied but the sha tag is missing"
        return "push", "", "sha tag is absent; a new immutable image may be built"
    if not expected_digest or not valid_digest(expected_digest):
        return "refuse", "", "sha tag already exists; a verified expected digest is required to reuse it"
    if info["digest"] != expected_digest:
        return "refuse", "", "sha tag digest conflicts with the verified expected digest"
    if evidence and evidence.get("digest") != expected_digest:
        return "refuse", "", "expected digest is not backed by the pinned build evidence"
    ok, reason = trusted_pinned_evidence(
        commit, expected_digest, ci_verified, observation, revisions or {},
        index_digest)
    if not ok:
        return "refuse", "", reason
    return "reuse", info["digest"], "sha tag is verified by pinned build evidence; reuse it"


def commit_is_ancestor(older, newer):
    """Read-only local git ancestry check for the previously promoted commit."""
    try:
        subprocess.run(["git", "merge-base", "--is-ancestor", older, newer],
                       check=True, cwd=ROOT, stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
        return True
    except (subprocess.CalledProcessError, OSError):
        return False


def nightly_identity_binding(previous_commit, previous_digest, observer=None,
                             revisions=None, index_digest=None):
    """Bind a previous-commit claim to the LIVE sha-<previous_commit> identity.

    Uses independent source evidence for the PREVIOUS image: its own live sha
    index digest and both platform config revisions. It never reuses the
    candidate's CI result or evidence digest. Returns a digest→commit map only
    for the fully verified previous identity; otherwise {} so promotion refuses.
    """
    if not previous_commit or not previous_digest or not valid_digest(previous_digest):
        return {}
    state, info = (observer or observe_reference)(
        nightly_immutable_reference(previous_commit), strict=True)
    if state != "present" or info["digest"] != previous_digest:
        return {}
    ok, _ = verify_sha_identity(
        previous_commit, previous_digest, (state, info), revisions or {},
        index_digest)
    if not ok:
        return {}
    return {previous_digest: previous_commit}


def nightly_alias_identity(alias_observation, revisions=None, index_digest=None):
    """Read the commit pinned by the LIVE nightly alias from its own
    digest-addressed platform configs. This is an identity READ, not an
    authorization decision: it lets an unattended run derive the previous
    binding without an operator-supplied commit string. Both required
    platforms must agree on a full 40-hex revision, or no commit is returned.
    """
    state, info = alias_observation
    if state != "present" or not valid_digest(info["digest"]):
        return "", ""
    digest = info["digest"]
    if revisions is None:
        index_digest, revisions = platform_manifest_revisions(
            f"{DOCKER_HUB_IMAGE}:{NIGHTLY_ALIAS}", anchor_digest=digest)
    if index_digest not in (None, "") and index_digest != digest:
        return "", digest
    if not REQUIRED_PLATFORMS <= set(revisions):
        return "", digest
    commits = set()
    for platform in sorted(REQUIRED_PLATFORMS):
        value = revisions.get(platform)
        revision = value.get("revision") if isinstance(value, dict) else None
        if not revision or not re.fullmatch(r"[0-9a-f]{40}", revision):
            return "", digest
        commits.add(revision)
    if len(commits) != 1:
        return "", digest
    return commits.pop(), digest


def nightly_promotion_decision(commit, digest, immutable_observation,
                               alias_observation, previous_commit=None, ancestor=None,
                               identity_binding=None, ci_verified=False,
                               revisions=None, evidence=None, index_digest=None):
    """Fail-closed check before moving the nightly alias.

    The immutable sha-<commit> image must already be verified (digest + both
    platforms). An alias already at that digest is idempotent. Moving over a
    different digest needs the observed alias digest bound to a verified sha
    reference (identity_binding) that agrees with the operator's previous
    commit and is an ancestor of the candidate. An unbound/stale identity
    refuses; the operator string is never trusted on its own.
    """
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        return False, "the pinned commit must be a full 40-hex SHA"
    if not valid_digest(digest):
        return False, "missing or invalid verified digest"
    if evidence and evidence.get("digest") != digest:
        return False, "expected digest is not backed by pinned build evidence"
    ok, reason = trusted_pinned_evidence(
        commit, digest, ci_verified, immutable_observation, revisions or {},
        index_digest)
    if not ok:
        return False, reason
    state, info = alias_observation
    if state == "unknown":
        return False, "nightly alias state is unknown"
    if state == "present":
        if info["digest"] == digest:
            return True, "nightly already at the verified digest; no move"
        if not REQUIRED_PLATFORMS <= info["platforms"]:
            return False, "nightly alias lacks both Linux architectures"
        bound = (identity_binding or {}).get(info["digest"])
        if not bound:
            return False, "nightly alias digest has no verified sha identity binding"
        if previous_commit and bound != previous_commit:
            return False, "operator previous commit does not match the observed nightly identity"
        if bound == commit:
            return False, "digest conflict for the same pinned commit"
        checker = ancestor or commit_is_ancestor
        if not checker(bound, commit):
            return False, "nightly would move backwards or from an unrelated commit"
    return True, "nightly may move to the verified digest"


def exact_main_ci_verified(commit, runs_json=None):
    """Read-only: the exact commit must be an ancestor of main AND have a
    successful, completed push CI run for that SHA. No operator checkbox can
    substitute for this check.
    """
    if not commit or not re.fullmatch(r"[0-9a-f]{7,40}", commit):
        return False, "missing or invalid commit"
    try:
        subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"],
                       check=True, cwd=ROOT, stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError:
        return False, "commit is not an ancestor of main"
    except OSError:
        return False, "git unavailable; cannot verify commit ancestry"
    try:
        runs = (runs_json if runs_json is not None else json.loads(github(
            "run", "list", "--workflow", "ci.yml", "--event", "push",
            "--branch", "main", "--commit", commit,
            "--json", "status,conclusion", "--limit", "1")))
    except (subprocess.CalledProcessError, ValueError, TypeError):
        return False, "CI API unavailable; fail closed"
    if not runs or runs[0] != {"conclusion": "success", "status": "completed"}:
        return False, "no successful completed push CI for this exact commit"
    return True, "exact main CI verified"


def indexable_urls(event_name, before_sha="", changed_paths=()):
    """Dry-run model of landing.yml's indexable-changes step.

    Only a real push diff yields URLs. A dispatch deploy has no before SHA and
    therefore yields none, so the deferred IndexNow job has nothing to notify.
    """
    if event_name != "push" or not before_sha or set(before_sha) == {"0"}:
        return []
    found = set()
    docs_prefix = "docs/carbon-panel/src/content/docs/"
    for path in changed_paths:
        if path == "landing/index.html":
            found.add("https://carbonpanel.dev/")
        elif path == "landing/llms.txt":
            found.add("https://carbonpanel.dev/llms.txt")
        elif path.startswith("docs/design-system/"):
            found.add("https://carbonpanel.dev/design-system/")
        elif path.startswith(docs_prefix) and path.endswith((".md", ".mdx")):
            route = path[len(docs_prefix):].rsplit(".", 1)[0]
            if route == "index":
                route = ""
            elif route.endswith("/index"):
                route = route[:-len("/index")]
            base = "https://carbonpanel.dev/docs/"
            found.add(base if not route else base + route + "/")
    return sorted(found)


def github(*args):
    return run("gh", *args)


def get_release(tag):
    # GitHub's by-tag REST endpoint omits unpublished drafts. The CLI resolves
    # drafts too and supplies their stable, authenticated release-by-ID URL.
    api_url = github("release", "view", tag, "--json", "apiUrl", "--jq", ".apiUrl")
    return json.loads(github("api", api_url))


def guard_publishing(tag, commit, git_tag=None):
    """Refuse any non-product tag and require verified exact-main CI.

    `tag` is the internal v-form; `git_tag` is the canonical product ref that is
    actually created and published. Inherited, source-only, preview and
    collision tags can never start a new panel release.
    """
    release_tag = git_tag or tag
    ok, reason = publication_candidate_decision(release_tag)
    if not ok:
        raise ValueError("Publication refused: " + reason)
    try:
        subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    except subprocess.CalledProcessError as error:
        raise ValueError("Publishing requires an exact main-ancestor commit") from error
    runs = json.loads(github("run", "list", "--workflow", "ci.yml", "--event", "push",
                              "--branch", "main", "--commit", commit,
                              "--json", "status,conclusion", "--limit", "1"))
    if not runs or runs[0] != {"conclusion": "success", "status": "completed"}:
        raise ValueError("Publishing requires successful main CI for this exact commit")
    tags = run("git", "tag", "--list", release_tag)
    if tags and run("git", "rev-parse", f"{release_tag}^{{commit}}") != commit:
        raise ValueError("The version tag already points at a different commit")
    releases = json.loads(github("api", "--paginate", "--slurp", "/repos/{owner}/{repo}/releases?per_page=100"))
    if any(release["tag_name"] == release_tag and not release["draft"]
           for page in releases for release in page):
        raise ValueError("A published release is immutable; choose a new version")


def plan(args):
    raw = args.version
    tag = resolve_panel_version(raw)
    product = product_release_or_none(raw)
    commit = run("git", "rev-parse", "HEAD")
    epoch = int(run("git", "show", "-s", "--format=%ct", "HEAD"))
    allowed, reason = publication_allowed(args.event, args.publish_input, raw)
    if allowed:
        guard_publishing(tag, commit, raw)
    values = {"version": raw, "internal_version": tag,
              "semver": product["semver"] if product else tag[1:],
              "git_tag": raw, "product": str(bool(product)).lower(),
              "commit": commit, "epoch": str(epoch),
              "date": datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
              "prerelease": str("-" in tag).lower(), "channel": ref_channel(raw),
              "publish": str(allowed).lower(), "publication_reason": reason}
    print(json.dumps(values, indent=2))
    write_output(values)


def write_archive(destination, members, epoch):
    """Stable order, timestamps, ownership and permissions; no workstation paths."""
    if destination.suffix == ".zip":
        date = datetime.fromtimestamp(max(epoch, 315532800), timezone.utc).timetuple()[:6]
        with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            for name, data, mode in sorted(members):
                entry = zipfile.ZipInfo(name, date)
                entry.create_system = 3
                entry.external_attr = (0o100000 | mode) << 16
                archive.writestr(entry, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    else:
        with destination.open("wb") as output, gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                for name, data, mode in sorted(members):
                    entry = tarfile.TarInfo(name)
                    entry.size, entry.mode, entry.mtime = len(data), mode, epoch
                    archive.addfile(entry, io.BytesIO(data))


def read_archive(path):
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            if len(set(names)) != len(names):
                raise ValueError("Duplicate archive member")
            members = {name: archive.read(name) for name in names}
    else:
        with tarfile.open(path) as archive:
            entries = archive.getmembers()
            if any(not item.isfile() for item in entries) or len({item.name for item in entries}) != len(entries):
                raise ValueError("Only unique regular files are allowed in release bundles")
            members = {item.name: archive.extractfile(item).read() for item in entries}
    if any("/" in name or "\\" in name or name in ("", ".", "..") for name in members):
        raise ValueError("Release bundles must contain flat, named files")
    return members


def package(args):
    tag = resolve_panel_version(args.version)
    display = display_version(args.version)
    product = product_release_or_none(args.version)
    target = (args.goos, args.goarch)
    if target not in TARGETS:
        raise ValueError(f"Unsupported platform: {target}")
    commit = run("git", "rev-parse", "HEAD")
    if subprocess.run(["git", "diff", "--quiet", "HEAD", "--"], cwd=ROOT).returncode:
        raise ValueError("Release builds require a clean tracked tree")
    frontend = ROOT / "web/carbon-panel/build"
    if not (frontend / "index.html").is_file() or not (frontend / "_app").is_dir():
        raise ValueError("Build the complete, versioned frontend before packaging")
    if not any(display.encode() in asset.read_bytes() or tag.encode() in asset.read_bytes()
               for asset in (frontend / "_app").rglob("*.js")):
        raise ValueError("The embedded web UI does not contain the release version")
    # The handler serves the schema embedded at build/schemav1.yaml. Derive it
    # from the SAME stamped input used for the bundled and website copies so a
    # running packaged panel reports the exact product version.
    stamped_schema = ROOT / ".website-build/release-schema/openapi.yaml"
    if not stamped_schema.is_file():
        raise ValueError("Missing stamped release OpenAPI input")
    verify_text(stamped_schema.read_text(encoding="utf-8"), display)
    verify_protocol_v1(stamped_schema.read_text(encoding="utf-8"))
    shutil.copyfile(stamped_schema, frontend / "schemav1.yaml")
    epoch = int(args.epoch)
    date = datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    info = {"version": tag, "commit": commit, "built": date,
            "os": args.goos, "arch": args.goarch, "go": run("go", "version")}
    if product:
        # Git tag, displayed SemVer and archive filename are separate fields.
        info.update({"git_tag": product["git_tag"], "semver": product["semver"]})
    environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": args.goos, "GOARCH": args.goarch}
    flags = f"-s -w -X github.com/athNdev/carbon-panel/internal/buildinfo.Version={display} -X github.com/athNdev/carbon-panel/internal/buildinfo.Commit={commit} -X github.com/athNdev/carbon-panel/internal/buildinfo.Date={date}"
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    members = []
    with tempfile.TemporaryDirectory(prefix="carbon-release-") as temporary:
        for program in ("carbon-panel", "carbon-storage"):
            name = program + (".exe" if args.goos == "windows" else "")
            binary = Path(temporary) / name
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags,
                            "-o", str(binary), f"./cmd/{program}"], cwd=ROOT, env=environment, check=True)
            # Each release target runs on a matching native runner. This checks
            # the actual shipped CLI without configuration, Docker or data I/O.
            reported = run(str(binary), "--version", cwd=temporary)
            if not reported.startswith(f"{program} {display} ") or commit not in reported:
                raise ValueError(f"Incorrect embedded version: {reported}")
            print(reported, flush=True)
            members.append((name, binary.read_bytes(), 0o755))
    for name in ("config.example.yaml", "LICENSE", "LICENSE.MPL-2.0"):
        members.append((name, (ROOT / name).read_bytes(), 0o644))
    members.append(("README.md", (ROOT / "docs/release-bundle.md").read_bytes(), 0o644))
    members.append(("BUILD-INFO.json", (json.dumps(info, indent=2) + "\n").encode(), 0o644))
    destination = output / archive_name(tag, *target)
    write_archive(destination, members, epoch)
    # Verify that packaging preserved both executables and all metadata.
    if read_archive(destination) != {name: data for name, data, _ in members}:
        raise ValueError("Archive did not preserve bundle contents")
    print(destination)


def sha256(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def collect(args):
    tag = resolve_panel_version(args.version)
    product = product_release_or_none(args.version)
    source, output = Path(args.source), Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    image = output.parent / "image"
    image.mkdir(parents=True, exist_ok=True)
    manifest = {"version": tag, "commit": args.commit, "image": IMAGE + ":" + tag[1:],
                "github_image": github_image(tag), "artifacts": []}
    if product:
        manifest.update({"git_tag": product["git_tag"], "semver": product["semver"]})
    for goos, goarch in TARGETS:
        name = archive_name(tag, goos, goarch)
        path = source / name
        members = read_archive(path)
        info = json.loads(members["BUILD-INFO.json"])
        if (info["version"], info["commit"], info["os"], info["arch"]) != (tag, args.commit, goos, goarch):
            raise ValueError(f"Mismatched release input: {name}")
        suffix = ".exe" if goos == "windows" else ""
        required = {"carbon-panel" + suffix, "carbon-storage" + suffix, "BUILD-INFO.json",
                    "config.example.yaml", "LICENSE", "LICENSE.MPL-2.0", "README.md"}
        if set(members) != required:
            raise ValueError(f"Incomplete or unexpected bundle: {name}")
        shutil.copyfile(path, output / name)
        manifest["artifacts"].append({"name": name, "sha256": sha256(path), "os": goos, "arch": goarch})
        if goos == "linux":
            platform = image / f"linux_{goarch}"
            platform.mkdir(parents=True, exist_ok=True)
            for program in ("carbon-panel", "carbon-storage"):
                (platform / program).write_bytes(members[program])
                (platform / program).chmod(0o755)
    shutil.copyfile(ROOT / "config.example.yaml", image / "config.example.yaml")
    # The bundled schema must carry the exact product version, never the proto's
    # independent 2.x placeholder. CLI/UI use the same normalized version.
    semver = product["semver"] if product else tag[1:]
    release_schema = ROOT / ".website-build/release-schema/openapi.yaml"
    verify_text(release_schema.read_text(encoding="utf-8"), semver)
    verify_protocol_v1(release_schema.read_text(encoding="utf-8"))
    shutil.copyfile(release_schema, output / "openapi.yaml")
    (output / "release-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    finalize(argparse.Namespace(output=str(output), digest=None))


def finalize(args):
    output = Path(args.output)
    path = output / "release-manifest.json"
    manifest = json.loads(path.read_text(encoding="utf-8"))
    if args.digest:
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.digest):
            raise ValueError("Missing or invalid image digest")
        previous = manifest.get("image_digest")
        if previous and previous != args.digest:
            raise ValueError("A different image digest is already recorded for this version")
        manifest["image_digest"] = args.digest
        path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    checksums = "".join(f"{sha256(file)}  {file.name}\n" for file in sorted(output.iterdir())
                        if file.is_file() and file.name != "checksums.txt")
    (output / "checksums.txt").write_text(checksums, encoding="utf-8")


def verify_image(args):
    manifest = json.loads(Path(args.manifest).read_text(encoding="utf-8"))
    platforms = {(item.get("platform", {}).get("os"), item.get("platform", {}).get("architecture"))
                 for item in manifest.get("manifests", [])}
    platforms.discard(("unknown", "unknown"))  # BuildKit provenance descriptors
    if platforms != {("linux", "amd64"), ("linux", "arm64")}:
        raise ValueError(f"Published image must contain both Linux architectures: {platforms}")


def preflight(args):
    """Fail closed when a publication is requested without registry access.
    Reports presence only; no secret value is ever read or printed."""
    registries = (tuple(r.strip() for r in args.registries.split(","))
                  if args.registries else ("docker_hub", "ghcr"))
    missing = missing_publish_credentials(os.environ, registries)
    print(json.dumps({"registries": list(registries), "missing": missing}))
    if missing:
        raise ValueError("Missing publish credentials for: " + ", ".join(missing))


def verify_registries(args):
    """Both registries must serve a complete multi-arch index. A partial result
    is a hard failure so no GitHub release can be exposed half-published."""
    results = {}
    for name, path in (("docker_hub", args.docker_manifest), ("ghcr", args.github_manifest)):
        verify_image(argparse.Namespace(manifest=path))
        results[name] = "verified"
    print(json.dumps(results, indent=2))
    return results


def image_tags(args):
    """Emit tag names for display/staging only. Promotion order is NOT enforced
    by this list; the publish jobs push the immutable tag before any alias."""
    plan = (module_promotion_plan(args.version) if args.module
            else promotion_plan(resolve_panel_version(args.version)))
    if args.immutable_only:
        plan = plan[:1]
    for step in plan:
        print(step["tag"])
    return plan


def immutable_references(tag):
    """Both registries' immutable full-version references for a panel tag."""
    return {"docker_hub": f"{DOCKER_HUB_IMAGE}:{tag[1:]}", "ghcr": github_image(tag)}


def preflight_immutable(args):
    """Read-only preflight of the immutable version tag in every registry."""
    tag = resolve_panel_version(args.version)
    if args.expected_digest and not valid_digest(args.expected_digest):
        raise ValueError("Invalid expected digest")
    observations = {name: observe_reference(ref)
                    for name, ref in immutable_references(tag).items()}
    provenance = (resume_provenance(tag, args.commit, args.expected_digest,
                                    git_tag=args.version)
                  if args.commit else None)
    action, reason = immutable_publication_decision(
        observations, args.expected_digest, provenance)
    summary = {name: {"state": state, "digest": info["digest"] if info else ""}
               for name, (state, info) in observations.items()}
    values = {"action": action, "reason": reason,
              "observations": json.dumps(summary, sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == IMMUTABLE_REFUSE:
        raise ValueError("Refusing immutable publication: " + reason)
    return action


def verify_immutable(args):
    """Verify expected tag→digest and both architectures in every registry."""
    tag = resolve_panel_version(args.version)
    results = {}
    for name, ref in immutable_references(tag).items():
        observation = observe_reference(ref)
        verify_tag_digest(observation, args.digest)
        results[name] = {
            "digest": observation[1]["digest"],
            "platforms": sorted(f"{os_}/{arch}" for os_, arch in observation[1]["platforms"]),
        }
    values = {"verified": "true", "digest": args.digest,
              "registries": json.dumps(results, sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)


def alias_promotion(args):
    """Decide which moving aliases, if any, may be promoted from a verified digest."""
    tag = resolve_panel_version(args.version)
    allowed, reason = alias_promotion_decision(tag, published_stable_versions())
    # A durable release record must exist before any alias can move.
    if allowed:
        verified, detail, _ = release_provenance(
            tag, args.commit, args.digest, git_tag=args.version)
        if not verified:
            allowed, reason = None, "durable release record unavailable: " + detail
    plain = tag[1:]
    minor = ".".join(plain.split("-", 1)[0].split(".")[:2])
    values = {"minor": minor if allowed else "", "latest": "true" if allowed else "false",
              "reason": reason, "digest": args.digest}
    print(json.dumps(values, indent=2))
    write_output(values)


def promote_alias(args):
    """Move one moving alias to an already-verified digest, then re-verify it.

    The durable release record is checked first, then the actual alias digest in
    every registry. Unknown state or an untraceable current digest refuses;
    an alias already at the verified digest is left untouched (no overwrite).
    """
    tag = resolve_panel_version(args.version)
    if not valid_digest(args.digest):
        raise ValueError("Missing or invalid verified digest")
    minor = ".".join(tag[1:].split("-", 1)[0].split(".")[:2])
    if args.alias not in (minor, "latest"):
        raise ValueError("Unsupported moving alias: " + args.alias)
    verified, detail, _ = release_provenance(
        tag, args.commit, args.digest, git_tag=args.version)
    if not verified:
        raise ValueError("Durable release record refused alias promotion: " + detail)
    refs = {"docker_hub": f"{DOCKER_HUB_IMAGE}:{args.alias}",
            "ghcr": f"ghcr.io/{ghcr_repository()}:{args.alias}"}
    observations = {name: observe_reference(ref) for name, ref in refs.items()}
    if any(state == "unknown" for state, _ in observations.values()):
        raise ValueError("Alias promotion refused: registry alias state is unknown")
    allowed, reason = alias_move_decision(
        tag, args.digest, observations, durable_release_records())
    if not allowed:
        raise ValueError("Alias promotion refused: " + reason)
    for name, ref in refs.items():
        state, info = observations[name]
        if state == "present" and info["digest"] == args.digest:
            continue  # idempotent retry: no re-upload and no overwrite
        source = ref.rsplit(":", 1)[0] + "@" + args.digest
        subprocess.run(["docker", "buildx", "imagetools", "create", "--tag", ref, source],
                       check=True, cwd=ROOT)
        verify_tag_digest(observe_reference(ref), args.digest)
    print(json.dumps({"alias": args.alias, "digest": args.digest,
                      "moved": True, "reason": reason}, indent=2))


def developer_channel_gate(args):
    """Emit the single publish decision used by cd.yml and landing.yml."""
    publish, reason = developer_channel_decision(
        args.event, args.opt_in, args.run_event, args.conclusion, args.ref, args.ci_verified)
    values = {"publish": str(publish).lower(), "reason": reason}
    print(json.dumps(values, indent=2))
    write_output(values)
    return publish


def verify_main_ci(args):
    """Emit the read-only exact-main-CI decision used by opt-in writes."""
    verified, reason = exact_main_ci_verified(args.commit)
    values = {"verified": str(verified).lower(), "reason": reason,
              "commit": args.commit}
    print(json.dumps(values, indent=2))
    write_output(values)
    return verified


def product_version_info(args):
    """Parse a product tag into its separate Git, SemVer and channel fields."""
    product = product_release(args.version)
    values = {**product, "display_version": product["semver"],
              "internal_version": product["version"]}
    print(json.dumps(values, indent=2))
    write_output(values)
    return values


def verify_publication_ref(args):
    """Trusted-ref plus exact-main-CI gate for an explicit publication."""
    ci_verified, ci_reason = exact_main_ci_verified(args.commit)
    allowed, reason = publication_ref_decision(
        args.event, args.ref, str(ci_verified).lower())
    values = {"publish": str(allowed).lower(), "reason": reason,
              "ci_verified": str(ci_verified).lower(), "ci_reason": ci_reason,
              "commit": args.commit, "ref": args.ref}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not allowed:
        raise ValueError("Publication ref gate refused: " + reason)
    return allowed


def verify_release_history_cli(args):
    """Validate the checked-in 84-tag retrospective catalog."""
    ok, problems = verify_release_history(None)
    values = {"verified": str(ok).lower(), "problems": json.dumps(problems)}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not ok:
        raise ValueError("Release history validation failed: " + "; ".join(problems))
    return ok


def indexable_urls_cli(args):
    """Emit JSON URLs for the deferred IndexNow dry-run fixture and workflow."""
    urls = indexable_urls(args.event, args.before, args.paths.splitlines())
    print(json.dumps(urls, separators=(",", ":")))
    return urls


def nightly_gate(args):
    """Emit the single nightly publication decision used by cd.yml."""
    allowed, reason = nightly_channel_decision(
        args.event, args.opt_in, args.run_event, args.conclusion,
        args.ref, args.ci_verified)
    values = {"publish": str(allowed).lower(), "reason": reason,
              "immutable_ref": nightly_immutable_reference(args.commit)}
    print(json.dumps(values, indent=2))
    write_output(values)
    return allowed


def nightly_schedule_gate(args):
    """Emit the nightly-only decision for the native Actions schedule path.

    This never authorizes a stable/latest/edge write; it only records whether
    the trusted-main nightly alias may move, and is verify-only by default.
    """
    allowed, reason = nightly_schedule_decision(
        args.event, args.ref, args.ci_verified, args.pin_stable,
        args.operator_setting)
    values = {"publish": str(allowed).lower(), "reason": reason,
              "immutable_ref": nightly_immutable_reference(args.commit),
              "commit": args.commit, "event": args.event}
    print(json.dumps(values, indent=2))
    write_output(values)
    return allowed


def nightly_schedule_sha_gate(args):
    """Read-only sha identity gate for the unattended schedule path.

    `push` only when the shared immutable sha tag is absent; `reuse` only with
    independently verified pinned evidence; `skip` (zero writes) when the tag
    is present-but-unverified or its lookup is unknown. A present-but-verified
    tag for the pinned commit is reused, so a repeat schedule is a no-op rather
    than a failed run.
    """
    reference = nightly_immutable_reference(args.commit)
    revisions = {}
    observation = observe_reference(reference, strict=True)
    state, info = observation
    if state == "unknown":
        action, digest, reason = "skip", "", "sha tag lookup is unknown; skipping"
    elif state == "absent":
        action, digest, reason = "push", "", "sha tag is absent; an immutable image may be built"
    else:
        digest = info["digest"]
        index_digest, revisions = platform_manifest_revisions(
            reference, anchor_digest=digest)
        ok, detail = trusted_pinned_evidence(
            args.commit, digest, args.ci_verified, observation, revisions,
            index_digest)
        if ok:
            action, reason = "reuse", "sha tag verified by pinned evidence; reuse"
        else:
            action, digest, reason = "skip", "", "sha tag present but unverified: " + detail
    values = {"action": action, "digest": digest, "reason": reason,
              "reference": reference,
              "platform_revisions": json.dumps(
                  {f"{os_}/{arch}": value
                   for (os_, arch), value in revisions.items()},
                  sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)
    return action


def json_argument(value, default=None):
    """Parse an optional JSON workflow input; empty means the default."""
    if not value:
        return default
    try:
        return json.loads(value)
    except (ValueError, TypeError) as error:
        raise ValueError("Invalid JSON workflow input") from error


def sha_tag_gate(args):
    """Read-only pre-write gate for the shared sha-<commit> image identity."""
    reference = nightly_immutable_reference(args.commit)
    observation = observe_reference(reference, strict=True)
    anchor = observation[1]["digest"] if observation[0] == "present" else None
    index_digest, revisions = platform_manifest_revisions(
        reference, anchor_digest=anchor)
    evidence = json_argument(args.pinned_evidence)
    action, digest, reason = sha_tag_decision(
        args.commit, observation, args.expected_digest, evidence,
        revisions, args.ci_verified, index_digest)
    if action == "reuse":
        ok, detail = verify_sha_identity(
            args.commit, digest, observation, revisions, index_digest)
        if not ok:
            action, reason = "refuse", detail
    values = {"action": action, "digest": digest, "reason": reason,
              "reference": reference,
              "platform_revisions": json.dumps(
                  {f"{os_}/{arch}": value for (os_, arch), value in revisions.items()},
                  sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse":
        raise ValueError("Shared sha tag gate refused: " + reason)
    return action


def dev_alias_promote(args, alias):
    """Shared, digest-based promotion of a development alias (edge or nightly).

    Both aliases reuse the verified sha-<commit> image and the same pinned-build
    evidence. Without --apply this is a read-only dry run.
    """
    reference = nightly_immutable_reference(args.commit)
    immutable = observe_reference(reference, strict=True)
    alias_observation = observe_reference(
        f"{DOCKER_HUB_IMAGE}:{alias}", strict=True)
    evidence = json_argument(args.pinned_evidence)
    # An unattended schedule has no operator-supplied previous binding: derive
    # it from the LIVE alias's own digest-addressed configs, then verify that
    # identity independently against its sha-<commit> reference below. Manual
    # dispatch keeps the explicit operator binding and is never auto-derived.
    previous_commit, previous_digest = args.previous_commit, args.previous_digest
    if (getattr(args, "auto_bind_previous", False)
            and (not previous_commit or not previous_digest)):
        previous_commit, previous_digest = nightly_alias_identity(alias_observation)
    # Independent evidence: the previous identity is verified from its OWN
    # digest-addressed platform configs, never from the candidate's CI result
    # or the candidate's evidence digest.
    previous_revisions, previous_index = {}, ""
    if previous_commit and previous_digest:
        previous_index, previous_revisions = platform_manifest_revisions(
            nightly_immutable_reference(previous_commit),
            anchor_digest=previous_digest)
    binding = nightly_identity_binding(
        previous_commit, previous_digest,
        revisions=previous_revisions, index_digest=previous_index)
    candidate_anchor = immutable[1]["digest"] if immutable[0] == "present" else None
    candidate_index, candidate_revisions = platform_manifest_revisions(
        reference, anchor_digest=candidate_anchor)
    allowed, reason = nightly_promotion_decision(
        args.commit, args.digest, immutable, alias_observation,
        previous_commit, identity_binding=binding,
        ci_verified=args.ci_verified, revisions=candidate_revisions,
        evidence=evidence, index_digest=candidate_index)
    values = {"alias": alias, "allowed": str(allowed).lower(), "reason": reason,
              "digest": args.digest, "immutable_state": immutable[0],
              "alias_state": alias_observation[0],
              "previous_commit": previous_commit, "previous_digest": previous_digest,
              "identity_bound": str(bool(binding)).lower()}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not allowed:
        raise ValueError(f"{alias} promotion refused: " + reason)
    if args.apply:
        if alias_observation[0] == "present" and alias_observation[1]["digest"] == args.digest:
            print(f"{alias} already verified; no overwrite")
            return True
        target = f"{DOCKER_HUB_IMAGE}:{alias}"
        source = f"{DOCKER_HUB_IMAGE}@{args.digest}"
        subprocess.run(["docker", "buildx", "imagetools", "create",
                        "--tag", target, source], check=True, cwd=ROOT)
        verify_tag_digest(observe_reference(target, strict=True), args.digest)
        print(f"{alias} promoted and verified")
    return True


def nightly_promote(args):
    return dev_alias_promote(args, NIGHTLY_ALIAS)


def edge_promote(args):
    """Explicitly promote edge from the verified immutable digest."""
    return dev_alias_promote(args, "edge")


def release_record(args):
    """Decide whether a durable release record exists before alias promotion."""
    action, reason = release_record_decision(
        resolve_panel_version(args.version), args.commit, args.digest,
        git_tag=args.version)
    values = {"action": action, "reason": reason}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse":
        raise ValueError("Release record refused: " + reason)
    return action


def verify_release_record(args):
    """Re-verify the durable record immediately before moving an alias."""
    ok, reason, _ = release_provenance(
        resolve_panel_version(args.version), args.commit, args.digest,
        git_tag=args.version)
    values = {"verified": str(ok).lower(), "reason": reason}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not ok:
        raise ValueError("Durable release record verification failed: " + reason)


def verify_reference(args):
    """Verify a single registry reference→digest mapping and both arches."""
    observation = observe_reference(args.reference)
    verify_tag_digest(observation, args.digest)
    print(json.dumps({"reference": args.reference, "digest": args.digest,
                      "verified": True}, indent=2))


def module_version_info(args):
    """Share exact validated module SemVer and ref format with the workflow."""
    tag = module_version(args.version)
    plain = tag[len("mod-"):]
    prerelease = "-" in plain
    values = {"version": plain, "tag": tag, "ref": "refs/tags/" + tag,
              "prerelease": str(prerelease).lower(), "alias": "" if prerelease else "latest"}
    print(json.dumps(values, indent=2))
    write_output(values)


def module_promotion(args):
    """Decide whether the module `latest` alias may move from a verified digest."""
    tag = module_version(args.version)
    published = (published_module_versions(args.published.split())
                 if args.published is not None else published_module_versions())
    if published is not None:
        published = [value[len("mod-"):] for value in published]
    plain = tag[len("mod-"):]
    allowed, reason = alias_promotion_decision(plain, published)
    values = {"version": plain, "promote_latest": "true" if allowed else "false",
              "reason": reason, "digest": args.digest}
    print(json.dumps(values, indent=2))
    write_output(values)


def publish(args):
    tag = resolve_panel_version(args.version)
    git_tag = args.version
    guard_publishing(tag, args.commit, git_tag)
    output = Path(args.output)
    manifest = json.loads((output / "release-manifest.json").read_text(encoding="utf-8"))
    if manifest["version"] != tag or manifest["commit"] != args.commit or not manifest.get("image_digest"):
        raise ValueError("Release artifacts and published image must match the release commit")
    # FINAL MANIFEST VALIDATION: a canonical product release must carry both
    # identity fields explicitly. A missing git_tag is NOT accepted as a default,
    # and semver must equal the product tag's displayed version exactly.
    product = product_release_or_none(git_tag)
    if manifest.get("git_tag") != git_tag:
        raise ValueError("Release manifest git_tag is missing or does not match the requested product tag")
    if product is None or manifest.get("semver") != product["semver"]:
        raise ValueError("Release manifest semver is missing or does not match the requested product tag")
    expected = {archive_name(tag, *target) for target in TARGETS} | {"openapi.yaml", "release-manifest.json"}
    lines = (output / "checksums.txt").read_text(encoding="utf-8").splitlines()
    recorded = dict((line.split("  ", 1)[1], line.split("  ", 1)[0]) for line in lines)
    if set(recorded) != expected or any(sha256(output / name) != digest for name, digest in recorded.items()):
        raise ValueError("Release files do not match their checksums")
    if not run("git", "tag", "--list", git_tag):
        github("api", "repos/{owner}/{repo}/git/refs", "-f", f"ref=refs/tags/{git_tag}", "-f", f"sha={args.commit}")
    # GitHub latest uses the same product-only verified stable ordering as the
    # registry alias promotion. An older maintenance release never moves it.
    published = published_stable_versions()
    latest, latest_reason = github_latest_decision(tag, published)
    previous_tag = verified_previous_release_tag()
    generated_args = ["api", "repos/{owner}/{repo}/releases/generate-notes",
                      "-f", f"tag_name={git_tag}", "-f", f"target_commitish={args.commit}"]
    if previous_tag:
        # Explicit VERIFIED compare base for the first MAJOR release; never
        # inferred from generic v* tag-name sorting.
        generated_args += ["-f", f"previous_tag_name={previous_tag}"]
    generated = json.loads(github(*generated_args))
    repository = os.environ.get("GITHUB_REPOSITORY") or github("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
    channel = "This is an alpha/prerelease build for early testing.\n\n" if "-alpha." in tag else (
        "This is a prerelease build.\n\n" if "-" in tag else "")
    upgrade = (migration_guide_notes(repository, git_tag)
               if product and not product["prerelease"]
               and int(product["semver"].split(".")[0]) >= 2 else "")
    body = (f"{channel}{upgrade}## Downloads\n\nChoose the archive for your operating system and CPU. Each includes the panel with its web UI, the storage helper, configuration example and licenses.\n\n"
            f"Verify downloads against **checksums.txt**. **release-manifest.json** records the exact source commit and Docker image digest. Back up your database and configuration before upgrading; preserve server worlds and existing data.\n\n"
            f"Container images (Linux amd64 and arm64):\n\n- Docker Hub: `{manifest['image']}`\n- GitHub Packages: `{manifest['github_image']}`\n\n"
            f"[Installation and verification](https://github.com/{repository}/blob/{git_tag}/docs/releases.md)\n\n" + generated["body"])
    with tempfile.TemporaryDirectory(prefix="carbon-release-notes-") as temporary:
        notes = Path(temporary) / "notes.md"
        notes.write_text(body, encoding="utf-8")
        # Only drafts are reusable; the guard above refuses published versions.
        exists = subprocess.run(["gh", "release", "view", git_tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
        if not exists:
            github("release", "create", git_tag, "--verify-tag", "--draft",
                   "--title", f"Carbon Panel {display_version(args.version)}", "--notes-file", str(notes))
        github("release", "edit", git_tag, "--notes-file", str(notes))
        github("release", "upload", git_tag, *(str(output / name) for name in sorted(expected | {"checksums.txt"})), "--clobber")
        uploaded = get_release(git_tag)
        remote = {asset["name"]: asset.get("digest") for asset in uploaded["assets"]}
        complete = expected | {"checksums.txt"}
        if not uploaded["draft"] or set(remote) != complete or any(
                remote[name] != "sha256:" + sha256(output / name) for name in complete):
            raise ValueError("Uploaded draft assets do not match the verified local release")
    github("release", "edit", git_tag, "--draft=false",
           f"--prerelease={str('-' in tag).lower()}", f"--latest={str(latest).lower()}")
    print(json.dumps({"git_tag": git_tag, "semver": product["semver"] if product else tag[1:],
                      "github_latest": str(latest).lower(), "reason": latest_reason,
                      "previous_tag": previous_tag or ""}, indent=2))


# --- OSS-413 channel promotion gates ----------------------------------------
# A channel move is allowed only from an EXACT source revision pinned read-only
# from trusted main, after a QUALIFIED stage: exact-commit main CI plus a
# verified multiarch digest in BOTH maintained registries (OCI) or a validated
# schema_version 3 contract plus exact site build (docs). Every move is
# digest-anchored and bound to the previously verified digest so a retry or a
# partial failure can converge or roll back without ever overwriting an
# immutable version tag. See ADR 0046 and docs/releases.md.

CHANNEL_CONTRACT_SCHEMA = 3
CHANNEL_NAMES = ("stable", "nightly")
CHANNEL_SELECTOR_LABELS = ("Stable", "Nightly")
LEGACY_DOC_SNAPSHOTS = ("1.0.39", "3.0.0-alpha.1")
CHANNEL_REGISTRIES = ("docker_hub", "ghcr")
ROLLING_ALIAS = "rolling"  # legacy constant; no redundant rolling channel is created

# The verified stable baseline is the ONLY source from which `latest` and the
# `stable` alias may move. It is frozen historical provenance, not a candidate.
STABLE_CHANNEL_BASELINE = {
    "commit": LEGACY_STABLE_COMMIT,
    "digest": LEGACY_STABLE_DIGEST,
    "tag": "latest",
    "stable_alias": "stable",
}
# A VERIFIED CURRENT Docker Hub nightly baseline. It is NOT a mandatory first
# publication revision. It may seed nightly only through a recorded, bounded
# bootstrap that also qualifies in GHCR and whose docs marker matches; otherwise
# the first nightly promotion carries the first qualified current-main revision.
NIGHTLY_HUB_BASELINE = {
    "commit": "d8597d4e953522d2eb2fa6d4764965ae8e58686f",
    "digest": ("sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce"
               "5e96d00fa1c64239c965a9302d"),
}
CHANNEL_MOVABLE = ("stable", "nightly", ROLLING_ALIAS, "docs")


def valid_commit(value):
    """A canonical, full-length commit SHA (never a short id or a ref name)."""
    return bool(value and re.fullmatch(r"[0-9a-f]{40}", value))


def validate_channel_contract(document):
    """Fail-closed validation of the docs schema_version 3 channel contract.

    Rejects a wrong schema, extra/missing channels, non-40-hex commits, missing
    or mismatched digests/revisions, a stable image that is not pinned to the
    verified stable digest, version terminology in the public selector, and any
    of the two removed legacy DOC snapshots. Source tags and historical records
    in docs/release-history.json are unaffected; this validates only the public
    channel contract artifact.
    """
    problems = []
    if not isinstance(document, dict):
        return False, ["channel contract must be a JSON object"]
    if document.get("schema_version") != CHANNEL_CONTRACT_SCHEMA:
        problems.append("schema_version must be 3")
    repository = document.get("repository", "")
    if not isinstance(repository, str) or not repository.startswith("https://github.com/"):
        problems.append("repository must be an https GitHub URL")
    if not isinstance(document.get("catalog"), str) or not document["catalog"]:
        problems.append("catalog path is required")
    channels = document.get("channels")
    if not isinstance(channels, dict) or set(channels) != set(CHANNEL_NAMES):
        problems.append("channels must contain exactly stable and nightly")
        return False, problems
    blob = json.dumps(channels, sort_keys=True)
    for stale in LEGACY_DOC_SNAPSHOTS:
        if stale in blob:
            problems.append(f"legacy doc snapshot {stale} must not appear in channels")
    for name in CHANNEL_NAMES:
        entry = channels[name]
        if not isinstance(entry, dict):
            problems.append(f"{name} channel must be an object")
            continue
        if entry.get("display") not in CHANNEL_SELECTOR_LABELS:
            problems.append(f"{name} display must be Stable or Nightly")
        commit = entry.get("commit")
        if not valid_commit(commit):
            problems.append(f"{name} commit must be a full 40-hex SHA")
        image = entry.get("image")
        if not isinstance(image, dict):
            problems.append(f"{name} image is required")
            continue
        if not valid_digest(image.get("digest")):
            problems.append(f"{name} image digest must be a valid sha256 digest")
        if image.get("revision") != commit:
            problems.append(f"{name} image revision must equal its commit")
        for registry in CHANNEL_REGISTRIES:
            ref = image.get(registry, "")
            if not isinstance(ref, str) or ":" not in ref:
                problems.append(f"{name} image must name a {registry} reference")
        for key in ("commit_url", "pinned_link", "pinned_url"):
            if key in entry and entry[key] != f"{repository}/commit/{commit}":
                problems.append(f"{name}.{key} must be the full-SHA commit URL")
    stable = channels["stable"]
    simage = stable.get("image", {}) if isinstance(stable, dict) else {}
    if simage.get("tag") != STABLE_CHANNEL_BASELINE["tag"]:
        problems.append("stable image tag must be latest")
    if simage.get("stable_alias") != STABLE_CHANNEL_BASELINE["stable_alias"]:
        problems.append("stable must define the stable alias")
    if simage.get("digest") != STABLE_CHANNEL_BASELINE["digest"]:
        problems.append("stable digest must be the verified stable baseline")
    if stable.get("commit") != STABLE_CHANNEL_BASELINE["commit"]:
        problems.append("stable commit must be the verified stable baseline")
    nightly = channels["nightly"]
    nimage = nightly.get("image", {}) if isinstance(nightly, dict) else {}
    if nightly.get("tag") is not None:
        problems.append("nightly must not carry a release/version tag")
    if nimage.get("tag") != "nightly":
        problems.append("nightly image tag must be nightly")
    if not valid_digest(nimage.get("digest")):
        problems.append("nightly digest must be a verified sha256 digest")
    return (not problems), problems


def channel_oci_parity(observations, digest, commit, registries=CHANNEL_REGISTRIES):
    """Both maintained registries must serve the SAME verified multiarch digest
    and OCI revision. A missing, unknown, single-arch or mismatched registry
    fails the qualified stage with zero writes.
    """
    for registry in registries:
        observation = observations.get(registry)
        if not observation:
            return False, f"{registry} observation is missing"
        state, info = observation
        if state == "unknown":
            return False, f"{registry} state is unknown"
        if state != "present":
            return False, f"{registry} immutable image is absent"
        if info.get("digest") != digest:
            return False, f"{registry} digest differs from the verified digest"
        if not REQUIRED_PLATFORMS <= set(info.get("platforms", ())):
            return False, f"{registry} lacks both required Linux architectures"
        if info.get("revision") != commit:
            return False, f"{registry} OCI revision does not match the pinned commit"
    return True, "both registries serve the same verified multiarch digest and revision"


def channel_source_gate(event, ref, commit, ci_verified, contract=None,
                        contract_valid=False, docs_validated=False):
    """Exact source revision pinned read-only from trusted main + qualified stage.

    A tag ref, a short SHA or an unverified CI result never qualifies. When a
    contract is supplied (the docs lane) it must pass schema_version 3
    validation and the exact site build validation must also be green; stale
    docs can never mask a channel candidate.
    """
    if ref != TRUSTED_DEVELOPER_REF:
        return False, "channel promotion requires the trusted refs/heads/main source pin"
    if not valid_commit(commit):
        return False, "the pinned source revision must be a full 40-hex SHA"
    if str(ci_verified).strip().lower() != "true":
        return False, "exact-commit main CI must be verified"
    if contract is not None:
        if not contract_valid:
            ok, _ = validate_channel_contract(contract)
            if not ok:
                return False, "channel contract schema_version 3 validation failed"
        if str(docs_validated).strip().lower() != "true":
            return False, "exact site build validation must pass before docs promotion"
        pinned = {entry.get("commit")
                  for entry in (contract.get("channels") or {}).values()
                  if isinstance(entry, dict)}
        if commit not in pinned:
            return False, "the built docs contract does not pin the candidate source revision"
    return True, f"{event} source pin qualified from trusted main"


def channel_move_decision(channel, candidate_commit, candidate_digest,
                          immutable_observations=None, alias_observations=None,
                          previous=None, ancestry=None, bootstrap=False,
                          qualification=None):
    """Return (action, reason, target_digest) for one channel move.

    action is publish (including an idempotent convergence of a partial move),
    noop (alias already at the verified digest), rollback (preserve the previous
    verified digest after a failed candidate) or refuse. No action ever
    overwrites an immutable version tag or deletes a tag.
    """
    if channel not in CHANNEL_MOVABLE:
        return "refuse", "unknown channel", ""
    if not valid_commit(candidate_commit):
        return "refuse", "candidate must be a full 40-hex commit", ""
    if not valid_digest(candidate_digest):
        return "refuse", "candidate digest must be a verified sha256 digest", ""
    qualification = qualification or {}
    if str(qualification.get("ci_verified", False)).strip().lower() != "true":
        return "refuse", "exact-commit main CI is not verified", ""

    # The planned 2.0.0 stable is deliberately NOT publishable by a channel move.
    tag = str(qualification.get("tag", ""))
    if tag in (NEXT_STABLE_GIT_TAG, "v" + NEXT_STABLE_SEMVER, NEXT_STABLE_SEMVER):
        return "refuse", "the planned 2.0.0 stable is not a channel publication", ""

    # Stable is frozen to the verified baseline; no arbitrary/legacy tag moves it.
    if channel == "stable":
        if (candidate_commit != STABLE_CHANNEL_BASELINE["commit"]
                or candidate_digest != STABLE_CHANNEL_BASELINE["digest"]):
            return "refuse", "stable aliases may only move from the verified stable digest", ""

    # Docs qualify on the contract + exact site build, not on registry aliases.
    if channel == "docs":
        if not qualification.get("contract_valid") or not qualification.get("site_validated"):
            return ("refuse",
                    "docs promotion needs a validated schema_version 3 contract and exact site build",
                    "")
        contract = qualification.get("contract") or {}
        commits = {entry.get("commit")
                   for entry in (contract.get("channels") or {}).values()
                   if isinstance(entry, dict)}
        if candidate_commit not in commits:
            return "refuse", "the docs candidate commit is not pinned by the channel contract", ""
        return _channel_alias_transition(
            channel, candidate_commit, candidate_digest,
            alias_observations or {}, previous, ancestry)

    # OCI channels qualify only after BOTH registries serve the same digest.
    parity_ok, parity_reason = channel_oci_parity(
        immutable_observations or {}, candidate_digest, candidate_commit)
    if not parity_ok:
        if previous and previous.get("bound") and valid_digest(previous.get("digest", "")):
            return ("rollback",
                    "candidate failed dual-registry parity (" + parity_reason
                    + "); preserving previous verified digest",
                    previous["digest"])
        return "refuse", "candidate failed dual-registry parity: " + parity_reason, ""

    # A verified Hub-only baseline may seed nightly ONLY via a recorded bounded
    # bootstrap that is already mirrored/qualified in GHCR (parity above) and
    # whose docs marker carries the same revision (contract validation upstream).
    if (channel == "nightly"
            and (candidate_commit, candidate_digest)
            == (NIGHTLY_HUB_BASELINE["commit"], NIGHTLY_HUB_BASELINE["digest"])
            and not bootstrap):
        return ("refuse",
                "the verified Hub-only nightly baseline is not a first-publish revision; "
                "promote a qualified current-main commit", "")

    return _channel_alias_transition(
        channel, candidate_commit, candidate_digest,
        alias_observations or {}, previous, ancestry)


def _channel_alias_transition(channel, candidate_commit, candidate_digest,
                              alias_observations, previous, ancestry):
    """Digest-anchored alias state machine shared by OCI and docs moves."""
    present = {}
    for registry, observation in alias_observations.items():
        state, info = observation
        if state == "unknown":
            return "refuse", f"{registry} alias state is unknown", ""
        if state == "present":
            present[registry] = info.get("digest", "")
    if not present:
        return "publish", "first publish: no verified alias holds a digest", candidate_digest
    if set(present.values()) == {candidate_digest}:
        return "noop", "alias already at the verified digest; no overwrite", candidate_digest

    # A partial move leaves one registry at the candidate and the other at the
    # previous digest. Converge only when the other digest is verifiably bound.
    other = {digest for digest in present.values() if digest != candidate_digest}
    if len(other) != 1:
        return "refuse", "alias digests conflict across registries", ""
    bound_digest = other.pop()
    if not previous or not previous.get("bound") or previous.get("digest") != bound_digest:
        return "refuse", "alias digest has no verified previous binding", bound_digest
    previous_commit = previous.get("commit", "")
    if not valid_commit(previous_commit) or previous_commit == candidate_commit:
        return "refuse", "previous binding is not a distinct full-SHA commit", bound_digest
    checker = ancestry or commit_is_ancestor
    if not checker(previous_commit, candidate_commit):
        return ("refuse",
                "stale/out-of-order revision: candidate is not a descendant of the bound previous commit",
                bound_digest)
    return "publish", "advance from the verified previous digest", candidate_digest


def channel_rollback_decision(channel, previous_commit, previous_digest,
                              previous_binding_verified, observations=None):
    """Explicit rollback to the previous verified digest after a partial failure.

    Refuses unless the previous identity was independently verified; an alias
    already at that digest is a noop so rollback is idempotent and safe to retry.
    """
    if channel not in CHANNEL_MOVABLE:
        return "refuse", "unknown channel", ""
    if not valid_commit(previous_commit) or not valid_digest(previous_digest):
        return "refuse", "rollback needs a full-SHA commit and verified digest", ""
    if not previous_binding_verified:
        return "refuse", "previous digest identity is not independently verified", ""
    for observation in (observations or {}).values():
        state, info = observation
        if state == "unknown":
            return "refuse", "alias state is unknown; refusing rollback", ""
        if state == "present" and info.get("digest") != previous_digest:
            if info.get("digest") and info.get("digest") != previous_digest:
                return "refuse", "alias holds an unbound digest; refusing blind rollback", ""
    return "rollback", "restore the previous verified digest", previous_digest


# --- OSS-413 qualified channel transaction ----------------------------------
# A channel is NOT promoted by moving an OCI alias. One transaction stages both
# immutable registries AND the exact docs artifact, confirms a PUBLIC candidate
# marker before any alias move, then deploys final docs and confirms both
# registries and the live marker agree. A receipt preserves the verified state
# so retry reuses the staged digest and partial failure rolls back or reconciles.

CHANNEL_PUBLISH_GROUP = "pages-deploy"  # the ONE shared non-cancelling lock
TX_STAGE_IMMUTABLE = "stage_immutable_both_registries"
TX_STAGE_DOCS = "stage_exact_docs_artifact"
TX_DEPLOY_CANDIDATE = "deploy_candidate_docs"
TX_VERIFY_CANDIDATE = "verify_public_candidate_marker"
TX_MOVE_ALIASES = "move_both_registry_aliases"
TX_DEPLOY_FINAL = "deploy_final_channel_docs"
TX_VERIFY_FINAL = "verify_final_aliases_and_docs"
TX_WRITE_RECEIPT = "record_receipt"
TX_RECONCILE = "reconcile_partial_failure"
TX_ROLLBACK = "rollback_to_previous_receipt"

TX_PUBLISH_STEPS = (TX_STAGE_IMMUTABLE, TX_STAGE_DOCS, TX_DEPLOY_CANDIDATE,
                    TX_VERIFY_CANDIDATE, TX_MOVE_ALIASES, TX_DEPLOY_FINAL,
                    TX_VERIFY_FINAL, TX_WRITE_RECEIPT)


def channel_receipt(channel, commit, digest, docs_hash, docs_commit, docs_digest,
                    registries, before, marker_urls, run_ids, seq, timestamp):
    """Build a complete, durable promotion receipt (no mutable main file)."""
    return {
        "channel": channel, "revision": commit, "digest": digest,
        "docs_artifact_hash": docs_hash, "docs_commit": docs_commit,
        "docs_digest": docs_digest, "registries": dict(registries),
        "before": dict(before), "marker_urls": dict(marker_urls),
        "run_ids": dict(run_ids), "seq": int(seq), "timestamp": timestamp,
        "verified": True,
    }


def channel_transaction_decision(candidate, previous=None, live=None,
                                 event="workflow_run", ancestry=None):
    """Decide one qualified channel transaction.

    Returns (action, reason, receipt, steps). action is publish, noop,
    reconcile, rollback or refuse. `previous` is the preserved verified receipt;
    `live` is the observed registry-alias and docs-marker state; `candidate`
    carries the qualified-stage evidence. A manual dispatch can only re-publish
    an ALREADY-ACTIVE exact source from the receipt, never originate a move.
    """
    channel = candidate.get("channel")
    if channel not in ("nightly", "stable"):
        return "refuse", "unknown channel", previous or {}, []
    commit, digest = candidate.get("commit"), candidate.get("digest")
    if not valid_commit(commit) or not valid_digest(digest):
        return "refuse", "candidate needs a full-SHA revision and verified digest", previous or {}, []
    if candidate.get("source_ref") != TRUSTED_DEVELOPER_REF:
        return "refuse", "exact source revision must be pinned from trusted main", previous or {}, []
    if str(candidate.get("ci_verified", False)).strip().lower() != "true":
        return "refuse", "exact-commit main CI is not verified", previous or {}, []
    if channel == "stable" and (
            commit != STABLE_CHANNEL_BASELINE["commit"]
            or digest != STABLE_CHANNEL_BASELINE["digest"]):
        return ("refuse",
                "stable channel moves only from the verified stable digest", previous or {}, [])

    previous = previous or {}
    live = live or {}
    seq = int(candidate.get("seq", 0))
    prev_seq = int(previous.get("seq", 0))

    # Stale/out-of-order candidates are refused before any stage or write.
    if previous.get("verified") and seq:
        if seq < prev_seq or (seq == prev_seq and
                              (candidate.get("commit") != previous.get("revision")
                               or candidate.get("digest") != previous.get("digest"))):
            return ("refuse",
                    "older/out-of-order candidate rejected against the active receipt",
                    previous, [])
        if seq > prev_seq and previous.get("revision"):
            checker = ancestry or commit_is_ancestor
            if not checker(previous["revision"], commit):
                return ("refuse", "candidate revision is not a descendant of the active receipt",
                        previous, [])

    # Manual/site-only deploy may publish only an already-active exact source.
    if event == "workflow_dispatch" and not (
            previous.get("verified")
            and previous.get("revision") == commit
            and previous.get("digest") == digest):
        return ("refuse",
                "manual/site-only deploy may publish only an already-active exact channel source from the receipt",
                previous, [])

    qualified = all((candidate.get("immutable_ok"), candidate.get("contract_valid"),
                     candidate.get("site_validated")))
    marker_ok = (candidate.get("docs_commit") == commit
                 and candidate.get("docs_digest") == digest)
    if not qualified or not marker_ok:
        # Losing qualification preserves the prior verified receipt and rolls
        # both surfaces back to it rather than improving a broken candidate.
        if previous.get("verified") and valid_digest(previous.get("digest", "")):
            return ("rollback",
                    "candidate is not fully qualified; preserving the prior verified receipt",
                    previous, (TX_ROLLBACK, TX_DEPLOY_FINAL, TX_VERIFY_FINAL))
        return "refuse", "candidate is not fully qualified (docs marker must match)", previous, []

    registries = candidate.get("registries") or {}
    if set(registries) != set(CHANNEL_REGISTRIES) or any(
            value != digest for value in registries.values()):
        if previous.get("verified"):
            return ("rollback", "staged digests do not agree in both registries",
                    previous, (TX_ROLLBACK, TX_DEPLOY_FINAL, TX_VERIFY_FINAL))
        return "refuse", "staged digests do not agree in both registries", previous, []

    if not candidate.get("candidate_marker_verified"):
        if previous.get("verified"):
            return ("rollback",
                    "public candidate marker not confirmed; aliases stay at the prior receipt",
                    previous, (TX_ROLLBACK, TX_DEPLOY_FINAL, TX_VERIFY_FINAL))
        return "refuse", "public candidate marker must be confirmed before any alias move", previous, []

    alias_digests = {k: v for k, v in (live.get("registries") or {}).items()
                     if v and k in CHANNEL_REGISTRIES}
    live_docs = (live.get("docs_commit"), live.get("docs_digest"))
    fully_live = (set(alias_digests.values()) == {digest}
                  and live_docs == (commit, digest))
    if fully_live:
        return "noop", "both registries and the live docs marker already match the candidate", previous, []

    alias_values = set(alias_digests.values())
    docs_values = {value for value in live_docs if value}
    mixed = ((digest in alias_values and any(v != digest for v in alias_values))
             or (digest in docs_values and any(v != digest for v in docs_values)))
    if mixed and previous.get("verified"):
        # A PARTIAL move (one surface already at the candidate) converges to the
        # qualified staged candidate; a fully-previous state is a normal advance.
        return "reconcile", "partial state converges to the verified staged candidate", previous, (
            TX_RECONCILE, TX_DEPLOY_CANDIDATE, TX_VERIFY_CANDIDATE,
            TX_MOVE_ALIASES, TX_DEPLOY_FINAL, TX_VERIFY_FINAL, TX_WRITE_RECEIPT)

    receipt = channel_receipt(
        channel, commit, digest, candidate.get("docs_hash", ""), commit, digest,
        registries, live.get("registries") or {}, candidate.get("marker_urls") or {},
        candidate.get("run_ids") or {}, seq or prev_seq + 1,
        candidate.get("timestamp", ""))
    return "publish", "qualified transaction staged and verified on both surfaces", receipt, TX_PUBLISH_STEPS


def channel_active_receipt(channel, alias_observations, docs_commit, docs_digest,
                           seq=0, timestamp=""):
    """Reconstruct the currently active receipt from LIVE published surfaces.

    Used by a manual/site-only dispatch so it can republish only the exact source
    already active in both registries and the public docs marker. A mixed or
    unknown alias state yields {} and the dispatch refuses.
    """
    registries = {}
    for name, (state, info) in alias_observations.items():
        if name not in CHANNEL_REGISTRIES:
            continue
        if state != "present" or not info or not valid_digest(info.get("digest", "")):
            return {}
        registries[name] = info["digest"]
    if set(registries) != set(CHANNEL_REGISTRIES) or len(set(registries.values())) != 1:
        return {}
    if not valid_commit(docs_commit) or not valid_digest(docs_digest):
        return {}
    digest = registries["docker_hub"]
    return channel_receipt(channel, docs_commit, digest, "", docs_commit, docs_digest,
                           registries, registries, {}, {}, seq, timestamp)


def channel_active_receipt_cli(args):
    """CLI: emit the live active receipt (read-only) for a manual dispatch."""
    receipt = channel_active_receipt(
        args.channel, _observation_map(args.aliases), args.docs_commit,
        args.docs_digest, args.seq, args.timestamp)
    values = {"receipt": json.dumps(receipt, sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not receipt:
        raise ValueError("No single verified active channel receipt could be reconstructed")
    return receipt


def channel_transaction_cli(args):
    """CLI: emit the qualified-transaction decision (offline, no writes)."""
    candidate = json_argument(args.candidate, default={}) or {}
    previous = json_argument(args.previous, default={}) or {}
    live = json_argument(args.live, default={}) or {}
    action, reason, receipt, steps = channel_transaction_decision(
        candidate, previous, live, args.event)
    values = {"action": action, "reason": reason,
              "receipt": json.dumps(receipt, sort_keys=True),
              "steps": json.dumps(list(steps))}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse":
        raise ValueError("Channel transaction refused: " + reason)
    return action


def validate_channel_contract_cli(args):
    """CLI: validate a generated channel contract artifact (offline)."""
    document = json.loads(Path(args.path).read_text(encoding="utf-8"))
    ok, problems = validate_channel_contract(document)
    values = {"verified": str(ok).lower(), "problems": json.dumps(problems)}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not ok:
        raise ValueError("Channel contract validation failed: " + "; ".join(problems))
    return ok


def channel_source_gate_cli(args):
    """CLI: emit the exact-source + qualified-stage decision for a channel move."""
    contract = json_argument(args.contract) if args.contract else None
    allowed, reason = channel_source_gate(
        args.event, args.ref, args.commit, args.ci_verified, contract,
        args.contract_validated == "true", args.docs_validated)
    values = {"qualified": str(allowed).lower(), "reason": reason,
              "commit": args.commit, "event": args.event}
    print(json.dumps(values, indent=2))
    write_output(values)
    if not allowed:
        raise ValueError("Channel source gate refused: " + reason)
    return allowed


def channel_promote_cli(args):
    """CLI: decide (and with --apply, perform) one digest-anchored channel move."""
    immutable = _observation_map(args.immutable)
    aliases = _observation_map(args.aliases)
    previous = None
    if args.previous_commit or args.previous_digest:
        previous = {"commit": args.previous_commit, "digest": args.previous_digest,
                    "bound": args.previous_binding == "true"}
    qualification = {
        "ci_verified": args.ci_verified,
        "contract_valid": args.contract_validated == "true",
        "site_validated": args.docs_validated,
        "tag": args.tag,
    }
    if args.contract:
        qualification["contract"] = json_argument(args.contract)
    action, reason, target = channel_move_decision(
        args.channel, args.commit, args.digest, immutable, aliases, previous,
        bootstrap=args.bootstrap, qualification=qualification)
    values = {"channel": args.channel, "action": action, "reason": reason,
              "target_digest": target, "previous_digest": args.previous_digest,
              "commit": args.commit}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse":
        raise ValueError("Channel promotion refused: " + reason)
    if args.apply and action in ("publish", "rollback", "noop"):
        _apply_channel_alias(args.channel, target)
    return action


def _observation_map(raw):
    """Parse a JSON map of registry -> [state, info] observations."""
    document = json_argument(raw, default={}) or {}
    result = {}
    for registry, value in document.items():
        state, info = value
        if state == "present":
            info = dict(info)
            info["platforms"] = {tuple(item) for item in info.get("platforms", ())}
        result[registry] = (state, info)
    return result


def channel_observe(reference):
    """Read-only observation of one reference in BOTH maintained registries.

    Emits the digest-anchored observation map consumed by channel-promote. The
    OCI revision is derived from agreeing, digest-addressed platform configs;
    an ambiguous or mixed revision stays empty and fails the move closed.
    """
    observations = {}
    for name, registry_ref in (("docker_hub", f"{DOCKER_HUB_IMAGE}:{reference}"),
                               ("ghcr", f"ghcr.io/{ghcr_repository()}:{reference}")):
        observation = observe_reference(registry_ref, strict=True)
        state, info = observation
        info = dict(info) if info else {}
        if state == "present":
            index_digest, revisions = platform_manifest_revisions(
                registry_ref, anchor_digest=info["digest"])
            commit, _ = nightly_alias_identity((state, info), revisions, index_digest)
            info["revision"] = commit
        info["platforms"] = sorted(tuple(platform) for platform in info.get("platforms", ()))
        observations[name] = [state, info]
    return observations


def channel_observe_cli(args):
    """CLI: emit both registries' observations for a reference (read-only)."""
    observations = channel_observe(args.reference)
    values = {"reference": args.reference,
              "observations": json.dumps(observations, sort_keys=True)}
    print(json.dumps(values, indent=2))
    write_output(values)
    return observations


def promote_stable_alias_cli(args):
    """CLI: promote only the `stable` alias from the verified stable digest.

    Never touches `latest` (owned by the product release lane), an immutable
    version tag or a Git tag. No tag is deleted and no release is created.
    """
    immutable = _observation_map(args.immutable)
    aliases = _observation_map(args.aliases)
    qualification = {"ci_verified": args.ci_verified, "tag": args.tag}
    action, reason, target = channel_move_decision(
        "stable", STABLE_CHANNEL_BASELINE["commit"], STABLE_CHANNEL_BASELINE["digest"],
        immutable, aliases, qualification=qualification)
    values = {"channel": "stable", "action": action, "reason": reason,
              "target_digest": target}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse" and not args.soft:
        raise ValueError("Stable alias promotion refused: " + reason)
    if action == "refuse" and args.soft:
        print("::notice::stable alias not promoted: " + reason)
        return action
    if args.apply and action in ("publish", "rollback"):
        for reference in (f"{DOCKER_HUB_IMAGE}:stable",
                          f"ghcr.io/{ghcr_repository()}:stable"):
            state, info = observe_reference(reference, strict=True)
            if state == "present" and info.get("digest") == target:
                continue
            source = reference.rsplit(":", 1)[0] + "@" + target
            subprocess.run(["docker", "buildx", "imagetools", "create",
                            "--tag", reference, source], check=True, cwd=ROOT)
            verify_tag_digest(observe_reference(reference, strict=True), target)


def _apply_channel_alias(channel, digest):
    """Move a moving alias at an already verified digest in BOTH registries.

    Immutable version tags are never touched and no tag is deleted. An alias is
    skipped when it already holds the target digest (idempotent retry).
    """
    if not valid_digest(digest):
        raise ValueError("Missing or invalid verified digest for alias move")
    alias = "nightly" if channel == "nightly" else (
        "latest" if channel == "stable" else channel)
    for reference in (f"{DOCKER_HUB_IMAGE}:{alias}",
                      f"ghcr.io/{ghcr_repository()}:{alias}"):
        state, info = observe_reference(reference, strict=True)
        if state == "present" and info.get("digest") == digest:
            continue
        source = reference.rsplit(":", 1)[0] + "@" + digest
        subprocess.run(["docker", "buildx", "imagetools", "create",
                        "--tag", reference, source], check=True, cwd=ROOT)
        verify_tag_digest(observe_reference(reference, strict=True), digest)
    # The stable alias is promoted alongside latest only from the verified digest.
    if channel == "stable":
        for reference in (f"{DOCKER_HUB_IMAGE}:stable",
                          f"ghcr.io/{ghcr_repository()}:stable"):
            state, info = observe_reference(reference, strict=True)
            if state == "present" and info.get("digest") == digest:
                continue
            source = reference.rsplit(":", 1)[0] + "@" + digest
            subprocess.run(["docker", "buildx", "imagetools", "create",
                            "--tag", reference, source], check=True, cwd=ROOT)
            verify_tag_digest(observe_reference(reference, strict=True), digest)


def build_parser():
    """Parser exposed so workflow job arguments can be fixture-parsed."""
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    planner = commands.add_parser("plan")
    planner.add_argument("--version", required=True)
    planner.add_argument("--event", required=True)
    planner.add_argument("--publish-input", default="false")
    builder = commands.add_parser("package")
    for name in ("version", "goos", "goarch", "epoch", "output"):
        builder.add_argument("--" + name, required=True)
    collector = commands.add_parser("collect")
    for name in ("version", "commit", "source", "output"):
        collector.add_argument("--" + name, required=True)
    finisher = commands.add_parser("finalize")
    finisher.add_argument("--output", required=True)
    finisher.add_argument("--digest")
    image_checker = commands.add_parser("verify-image")
    image_checker.add_argument("--manifest", required=True)
    registry_checker = commands.add_parser("verify-registries")
    registry_checker.add_argument("--docker-manifest", required=True)
    registry_checker.add_argument("--github-manifest", required=True)
    preflight_cmd = commands.add_parser("preflight")
    preflight_cmd.add_argument("--registries")
    image_tagger = commands.add_parser("image-tags")
    image_tagger.add_argument("--version", required=True)
    image_tagger.add_argument("--module", action="store_true")
    image_tagger.add_argument("--immutable-only", action="store_true")
    immutable_preflight = commands.add_parser("preflight-immutable")
    immutable_preflight.add_argument("--version", required=True)
    immutable_preflight.add_argument("--commit", required=True)
    immutable_preflight.add_argument("--expected-digest")
    immutable_verify = commands.add_parser("verify-immutable")
    immutable_verify.add_argument("--version", required=True)
    immutable_verify.add_argument("--digest", required=True)
    alias_planner = commands.add_parser("alias-promotion")
    alias_planner.add_argument("--version", required=True)
    alias_planner.add_argument("--commit", required=True)
    alias_planner.add_argument("--digest", required=True)
    alias_mover = commands.add_parser("promote-alias")
    alias_mover.add_argument("--version", required=True)
    alias_mover.add_argument("--commit", required=True)
    alias_mover.add_argument("--alias", required=True)
    alias_mover.add_argument("--digest", required=True)
    record_checker = commands.add_parser("release-record")
    for name in ("version", "commit", "digest"):
        record_checker.add_argument("--" + name, required=True)
    record_verifier = commands.add_parser("verify-release-record")
    for name in ("version", "commit", "digest"):
        record_verifier.add_argument("--" + name, required=True)
    developer_gate = commands.add_parser("developer-channel-gate")
    developer_gate.add_argument("--event", required=True)
    developer_gate.add_argument("--opt-in", default="")
    developer_gate.add_argument("--run-event", default="")
    developer_gate.add_argument("--conclusion", default="")
    developer_gate.add_argument("--ref", default="")
    developer_gate.add_argument("--ci-verified", default="")
    nightly_gate_cmd = commands.add_parser("nightly-gate")
    for name in ("event", "commit", "ref"):
        nightly_gate_cmd.add_argument("--" + name, required=True)
    for name in ("opt-in", "run-event", "conclusion", "ci-verified"):
        nightly_gate_cmd.add_argument("--" + name, default="")
    sha_gate_cmd = commands.add_parser("sha-tag-gate")
    sha_gate_cmd.add_argument("--commit", required=True)
    sha_gate_cmd.add_argument("--expected-digest", default="")
    sha_gate_cmd.add_argument("--ci-verified", default="")
    sha_gate_cmd.add_argument("--pinned-evidence", default="")
    promote_cmds = {}
    for command_name in ("nightly-promote", "edge-promote"):
        promote_cmd = commands.add_parser(command_name)
        promote_cmds[command_name] = promote_cmd
        promote_cmd.add_argument("--commit", required=True)
        promote_cmd.add_argument("--digest", required=True)
        promote_cmd.add_argument("--previous-commit", default="")
        promote_cmd.add_argument("--previous-digest", default="")
        promote_cmd.add_argument("--ci-verified", default="")
        promote_cmd.add_argument("--pinned-evidence", default="")
        promote_cmd.add_argument("--apply", action="store_true")
    promote_cmds["nightly-promote"].add_argument(
        "--auto-bind-previous", action="store_true")
    schedule_gate = commands.add_parser("nightly-schedule-gate")
    for name in ("event", "commit", "ref"):
        schedule_gate.add_argument("--" + name, required=True)
    for name in ("ci-verified", "pin-stable", "operator-setting"):
        schedule_gate.add_argument("--" + name, default="")
    schedule_sha = commands.add_parser("nightly-schedule-sha-gate")
    schedule_sha.add_argument("--commit", required=True)
    schedule_sha.add_argument("--ci-verified", default="")
    module_resolver = commands.add_parser("module-version")
    module_resolver.add_argument("--version", required=True)
    ci_checker = commands.add_parser("verify-main-ci")
    ci_checker.add_argument("--commit", required=True)
    product_resolver = commands.add_parser("product-version")
    product_resolver.add_argument("--version", required=True)
    ref_checker = commands.add_parser("verify-publication-ref")
    ref_checker.add_argument("--commit", required=True)
    ref_checker.add_argument("--ref", required=True)
    ref_checker.add_argument("--event", default="workflow_dispatch")
    commands.add_parser("verify-release-history")
    indexable = commands.add_parser("indexable-urls")
    indexable.add_argument("--event", required=True)
    indexable.add_argument("--before", default="")
    indexable.add_argument("--paths", default="")
    module_planner = commands.add_parser("module-promotion")
    module_planner.add_argument("--version", required=True)
    module_planner.add_argument("--digest", default="")
    module_planner.add_argument("--published")
    reference_verifier = commands.add_parser("verify-reference")
    reference_verifier.add_argument("--reference", required=True)
    reference_verifier.add_argument("--digest", required=True)
    publisher = commands.add_parser("publish")
    for name in ("version", "commit", "output"):
        publisher.add_argument("--" + name, required=True)
    contract_checker = commands.add_parser("validate-channel-contract")
    contract_checker.add_argument("--path", required=True)
    source_gate = commands.add_parser("channel-source-gate")
    for name in ("event", "ref", "commit", "ci-verified"):
        source_gate.add_argument("--" + name, required=True)
    source_gate.add_argument("--contract", default="")
    source_gate.add_argument("--contract-validated", default="false")
    source_gate.add_argument("--docs-validated", default="false")
    promote = commands.add_parser("channel-promote")
    for name in ("channel", "commit", "digest"):
        promote.add_argument("--" + name, required=True)
    promote.add_argument("--ci-verified", default="false")
    promote.add_argument("--contract", default="")
    promote.add_argument("--contract-validated", default="false")
    promote.add_argument("--docs-validated", default="false")
    promote.add_argument("--tag", default="")
    promote.add_argument("--immutable", default="{}")
    promote.add_argument("--aliases", default="{}")
    promote.add_argument("--previous-commit", default="")
    promote.add_argument("--previous-digest", default="")
    promote.add_argument("--previous-binding", default="false")
    promote.add_argument("--bootstrap", action="store_true")
    promote.add_argument("--apply", action="store_true")
    observe = commands.add_parser("channel-observe")
    observe.add_argument("--reference", required=True)
    stable_alias = commands.add_parser("promote-stable-alias")
    stable_alias.add_argument("--ci-verified", default="false")
    stable_alias.add_argument("--tag", default="")
    stable_alias.add_argument("--immutable", default="{}")
    stable_alias.add_argument("--aliases", default="{}")
    stable_alias.add_argument("--apply", action="store_true")
    stable_alias.add_argument("--soft", action="store_true")
    transaction = commands.add_parser("channel-transaction")
    for name in ("candidate", "previous", "live"):
        transaction.add_argument("--" + name, default="{}")
    transaction.add_argument("--event", default="workflow_run")
    active = commands.add_parser("channel-active-receipt")
    active.add_argument("--channel", required=True)
    active.add_argument("--aliases", default="{}")
    active.add_argument("--docs-commit", required=True)
    active.add_argument("--docs-digest", required=True)
    active.add_argument("--seq", default="0")
    active.add_argument("--timestamp", default="")
    return parser


def main():
    args = build_parser().parse_args()
    # module-version is a CLI display command; the bare module_version() helper
    # validates a string and must never be used as its handler.
    handlers = {"module-version": module_version_info,
                "product-version": product_version_info,
                "verify-release-history": verify_release_history_cli,
                "verify-publication-ref": verify_publication_ref,
                "indexable-urls": indexable_urls_cli,
                "validate-channel-contract": validate_channel_contract_cli,
                "channel-source-gate": channel_source_gate_cli,
                "channel-promote": channel_promote_cli,
                "channel-observe": channel_observe_cli,
                "promote-stable-alias": promote_stable_alias_cli,
                "channel-transaction": channel_transaction_cli,
                "channel-active-receipt": channel_active_receipt_cli}
    handler = handlers.get(args.command,
                           globals().get(args.command.replace("-", "_")))
    if handler is None:
        raise ValueError("Unknown release command: " + args.command)
    handler(args)


if __name__ == "__main__":
    main()
