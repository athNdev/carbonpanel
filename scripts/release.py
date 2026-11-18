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
import tarfile
import tempfile
from datetime import datetime, timezone
import zipfile

ROOT = Path(__file__).resolve().parent.parent
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
    if ref_name.startswith("v"):
        return "prerelease" if "-" in ref_name else "stable"
    return "other"


def publication_allowed(event_name, publish_requested, version_value):
    """The single auditable publication boundary.

    Only an explicit operator `workflow_dispatch` that requests publication may
    write to a registry or GitHub release. Branch pushes, tag pushes, pull
    requests, workflow_run and every other automatic event only stage, build and
    verify. A version/tag ref is therefore always safe to stage.
    """
    if event_name != "workflow_dispatch":
        return False, f"{event_name} events stage only; publication needs an explicit workflow_dispatch"
    if str(publish_requested).strip().lower() != "true":
        return False, "publication needs publish=true on workflow_dispatch"
    if is_verification_tag(version_value):
        return False, "verification and test tags are never published"
    if SEMVER.fullmatch(version_value) is None:
        return False, "only SemVer panel versions publish from this workflow"
    return True, "explicit workflow_dispatch publish opt-in"


def promotion_plan(version_value):
    """Ordered tag destinations. The immutable full version always precedes a
    moving alias; prereleases publish no stable alias at all.
    """
    tag = version(version_value)
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
AMBIGUOUS_FAILURE = ("unauthorized", "denied", "forbidden", "connection",
                    "timeout", "temporar", "server error", "unavailable")


def classify_lookup_failure(error, strict=False):
    """Classify a failed image lookup as 'absent' or 'unknown'.

    Non-strict callers keep the historical broad matching; publication paths
    must pass strict=True so only a definitive missing manifest becomes absent.
    """
    message = ((getattr(error, "stderr", "") or "")
               + " " + (getattr(error, "output", "") or "")).lower()
    if strict:
        if any(token in message for token in AMBIGUOUS_FAILURE):
            return "unknown"
        return "absent" if any(token in message for token in DEFINITIVE_MISSING) else "unknown"
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
        return classify_lookup_failure(error, strict), None
    except OSError as error:
        return classify_lookup_failure(error, strict), None
    try:
        return "present", parse_inspect(text)
    except RegistryError:
        return "unknown", None


def platform_manifest_revisions(reference, runner=None):
    """Read OCI revision labels from the amd64 and arm64 platform manifests.

    Returns {(os, arch): revision}. Missing or unreadable labels yield an empty
    map, so callers fail closed. `runner` is injectable for offline fixtures.
    """
    try:
        raw = (runner or inspect_runner)(
            "docker", "buildx", "imagetools", "inspect", reference,
            "--format", "{{json .}}")
        document = json.loads(raw)
    except (subprocess.CalledProcessError, OSError, ValueError, TypeError):
        return {}
    revisions = {}
    for item in document.get("Manifests", []):
        platform = item.get("Platform", {}) or {}
        key = (platform.get("OS"), platform.get("Architecture"))
        if key not in REQUIRED_PLATFORMS:
            continue
        annotations = item.get("Annotations") or {}
        revision = (annotations.get("org.opencontainers.image.revision")
                    or item.get("Revision"))
        if revision:
            revisions[key] = revision
    return revisions


def trusted_pinned_evidence(commit, digest, ci_verified, observation, revisions):
    """Verify independent pinned-build evidence before moving a shared alias.

    Requires exact-commit CI confirmation, the observed index digest, both
    target platforms and their OCI revision labels equal to the pinned commit.
    """
    if not ci_verified or str(ci_verified).strip().lower() != "true":
        return False, "exact successful CI for the pinned commit is required"
    if not re.fullmatch(r"[0-9a-f]{40}", commit) or not valid_digest(digest):
        return False, "invalid pinned commit or digest"
    try:
        verify_tag_digest(observation, digest)
    except ValueError as error:
        return False, "immutable image verification failed: " + str(error)
    for platform in sorted(REQUIRED_PLATFORMS):
        if revisions.get(platform) != commit:
            return False, f"OCI revision on {platform[0]}/{platform[1]} does not match the pinned commit"
    return True, "trusted pinned build evidence verified"


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
                try:
                    tags.append(version(release.get("tag_name", "")))
                except ValueError:
                    continue
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


def release_provenance(tag, commit, expected_digest, releases_json=None):
    """Prove from a verified release manifest that an existing image digest is
    the build of this exact version and source commit. Returns (ok, reason,
    manifest). Unknown or mismatched provenance always fails closed.
    """
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        for page in data:
            for item in page:
                if item.get("tag_name") != tag:
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
                tag = item.get("tag_name", "")
                if valid_digest(digest) and SEMVER.fullmatch(tag):
                    records[digest] = tag
        return records
    except (subprocess.CalledProcessError, ValueError, TypeError, AttributeError, KeyError):
        return {}


def release_record_decision(tag, commit, digest, releases_json=None):
    """Decide how to reach a durable release record before any alias moves.
    Returns (action, reason) with action publish / verified / refuse.
    """
    try:
        data = (releases_json if releases_json is not None else json.loads(
            github("api", "--paginate", "--slurp",
                   "/repos/{owner}/{repo}/releases?per_page=100")))
        found = any(item.get("tag_name") == tag for page in data for item in page)
        if not found:
            return "publish", "no release record for this version"
        ok, reason, _ = release_provenance(tag, commit, digest, data)
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


def resume_provenance(tag, commit, expected_digest, releases_json=None):
    """RESUME needs the exact tag identity plus verified release provenance,
    not merely equal tag digests and matching platform counts."""
    if not valid_digest(expected_digest):
        return False, "a verified expected digest is required to resume"
    try:
        local = run("git", "tag", "--list", tag)
        if local and run("git", "rev-parse", f"{tag}^{{commit}}") != commit:
            return False, "version tag points at a different commit"
    except subprocess.CalledProcessError:
        return False, "version tag identity cannot be determined"
    return release_provenance(tag, commit, expected_digest, releases_json)[:2]


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
    """Nightly writes require an explicit trusted-main dispatch opt-in and a
    read-only exact-commit main CI verification. Every automatic event — main
    push, CI workflow_run, PR, schedule, tag — stages only.
    """
    allowed, reason = developer_channel_decision(
        event_name, opt_in, run_event, conclusion, ref, ci_verified)
    if not allowed:
        return False, "nightly " + reason
    return True, "nightly opt-in with verified exact main CI"


def nightly_immutable_reference(commit):
    """Nightly reuses the existing per-commit immutable image identity."""
    return f"{DOCKER_HUB_IMAGE}:sha-{commit}"


def sha_tag_decision(commit, observation, expected_digest=None, evidence=None,
                     revisions=None, ci_verified=False):
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
    if evidence is not None and evidence.get("digest") != expected_digest:
        return "refuse", "", "expected digest is not backed by the pinned build evidence"
    ok, reason = trusted_pinned_evidence(
        commit, expected_digest, ci_verified, observation, revisions or {})
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
                             revisions=None, ci_verified=False, evidence=None):
    """Bind a previous-commit claim to the LIVE sha-<previous_commit> identity.

    Binds to the SAME evidence required for the candidate: exact-commit CI, the
    observed index digest, both platforms and matching OCI revision labels on
    both platform manifests. Returns a digest→commit map only for that fully
    verified identity; otherwise {} so the monotonicity check refuses.
    """
    if not previous_commit or not previous_digest or not valid_digest(previous_digest):
        return {}
    state, info = (observer or observe_reference)(
        nightly_immutable_reference(previous_commit), strict=True)
    if state != "present" or info["digest"] != previous_digest:
        return {}
    if evidence is not None and evidence.get("digest") != previous_digest:
        return {}
    ok, _ = trusted_pinned_evidence(
        previous_commit, previous_digest, ci_verified, (state, info), revisions or {})
    if not ok:
        return {}
    return {previous_digest: previous_commit}


def nightly_promotion_decision(commit, digest, immutable_observation,
                               alias_observation, previous_commit=None, ancestor=None,
                               identity_binding=None, ci_verified=False,
                               revisions=None, evidence=None):
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
    if evidence is not None and evidence.get("digest") != digest:
        return False, "expected digest is not backed by pinned build evidence"
    ok, reason = trusted_pinned_evidence(
        commit, digest, ci_verified, immutable_observation, revisions or {})
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


def guard_publishing(tag, commit):
    if SEMVER.fullmatch(tag) is None or is_verification_tag(tag):
        raise ValueError("Only immutable SemVer panel versions can be published")
    try:
        subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    except subprocess.CalledProcessError as error:
        raise ValueError("Publishing requires an exact main-ancestor commit") from error
    runs = json.loads(github("run", "list", "--workflow", "ci.yml", "--event", "push",
                              "--branch", "main", "--commit", commit,
                              "--json", "status,conclusion", "--limit", "1"))
    if not runs or runs[0] != {"conclusion": "success", "status": "completed"}:
        raise ValueError("Publishing requires successful main CI for this exact commit")
    tags = run("git", "tag", "--list", tag)
    if tags and run("git", "rev-parse", f"{tag}^{{commit}}") != commit:
        raise ValueError("The version tag already points at a different commit")
    releases = json.loads(github("api", "--paginate", "--slurp", "/repos/{owner}/{repo}/releases?per_page=100"))
    if any(release["tag_name"] == tag and not release["draft"]
           for page in releases for release in page):
        raise ValueError("A published release is immutable; choose a new version")


def plan(args):
    tag = version(args.version)
    commit = run("git", "rev-parse", "HEAD")
    epoch = int(run("git", "show", "-s", "--format=%ct", "HEAD"))
    allowed, reason = publication_allowed(args.event, args.publish_input, tag)
    if allowed:
        guard_publishing(tag, commit)
    values = {"version": tag, "commit": commit, "epoch": str(epoch),
              "date": datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
              "prerelease": str("-" in tag).lower(), "channel": ref_channel(tag),
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
    tag = version(args.version)
    target = (args.goos, args.goarch)
    if target not in TARGETS:
        raise ValueError(f"Unsupported platform: {target}")
    commit = run("git", "rev-parse", "HEAD")
    if subprocess.run(["git", "diff", "--quiet", "HEAD", "--"], cwd=ROOT).returncode:
        raise ValueError("Release builds require a clean tracked tree")
    frontend = ROOT / "web/carbon-panel/build"
    if not (frontend / "index.html").is_file() or not (frontend / "_app").is_dir():
        raise ValueError("Build the complete, versioned frontend before packaging")
    if not any(tag.encode() in asset.read_bytes() for asset in (frontend / "_app").rglob("*.js")):
        raise ValueError("The embedded web UI does not contain the release version")
    epoch = int(args.epoch)
    date = datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    info = {"version": tag, "commit": commit, "built": date,
            "os": args.goos, "arch": args.goarch, "go": run("go", "version")}
    environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": args.goos, "GOARCH": args.goarch}
    flags = f"-s -w -X github.com/athNdev/carbon-panel/internal/buildinfo.Version={tag} -X github.com/athNdev/carbon-panel/internal/buildinfo.Commit={commit} -X github.com/athNdev/carbon-panel/internal/buildinfo.Date={date}"
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
            if not reported.startswith(f"{program} {tag} ") or commit not in reported:
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
    tag = version(args.version)
    source, output = Path(args.source), Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    image = output.parent / "image"
    image.mkdir(parents=True, exist_ok=True)
    manifest = {"version": tag, "commit": args.commit, "image": IMAGE + ":" + tag[1:],
                "github_image": github_image(tag), "artifacts": []}
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
    shutil.copyfile(ROOT / ".website-build/release-schema/openapi.yaml", output / "openapi.yaml")
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
            else promotion_plan(args.version))
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
    tag = version(args.version)
    if args.expected_digest and not valid_digest(args.expected_digest):
        raise ValueError("Invalid expected digest")
    observations = {name: observe_reference(ref)
                    for name, ref in immutable_references(tag).items()}
    provenance = (resume_provenance(tag, args.commit, args.expected_digest)
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
    tag = version(args.version)
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
    tag = version(args.version)
    allowed, reason = alias_promotion_decision(tag, published_stable_versions())
    # A durable release record must exist before any alias can move.
    if allowed:
        verified, detail, _ = release_provenance(tag, args.commit, args.digest)
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
    tag = version(args.version)
    if not valid_digest(args.digest):
        raise ValueError("Missing or invalid verified digest")
    minor = ".".join(tag[1:].split("-", 1)[0].split(".")[:2])
    if args.alias not in (minor, "latest"):
        raise ValueError("Unsupported moving alias: " + args.alias)
    verified, detail, _ = release_provenance(tag, args.commit, args.digest)
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
    revisions = platform_manifest_revisions(reference)
    evidence = json_argument(args.pinned_evidence, {})
    action, digest, reason = sha_tag_decision(
        args.commit, observation, args.expected_digest, evidence,
        revisions, args.ci_verified)
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
    previous_revisions = json_argument(args.previous_revisions, {})
    evidence = json_argument(args.pinned_evidence, {})
    binding = nightly_identity_binding(
        args.previous_commit, args.previous_digest,
        revisions=previous_revisions, ci_verified=args.ci_verified,
        evidence=evidence)
    candidate_revisions = platform_manifest_revisions(reference)
    allowed, reason = nightly_promotion_decision(
        args.commit, args.digest, immutable, alias_observation,
        args.previous_commit, identity_binding=binding,
        ci_verified=args.ci_verified, revisions=candidate_revisions,
        evidence=evidence)
    values = {"alias": alias, "allowed": str(allowed).lower(), "reason": reason,
              "digest": args.digest, "immutable_state": immutable[0],
              "alias_state": alias_observation[0],
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
        version(args.version), args.commit, args.digest)
    values = {"action": action, "reason": reason}
    print(json.dumps(values, indent=2))
    write_output(values)
    if action == "refuse":
        raise ValueError("Release record refused: " + reason)
    return action


def verify_release_record(args):
    """Re-verify the durable record immediately before moving an alias."""
    ok, reason, _ = release_provenance(
        version(args.version), args.commit, args.digest)
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
    tag = version(args.version)
    guard_publishing(tag, args.commit)
    output = Path(args.output)
    manifest = json.loads((output / "release-manifest.json").read_text(encoding="utf-8"))
    if manifest["version"] != tag or manifest["commit"] != args.commit or not manifest.get("image_digest"):
        raise ValueError("Release artifacts and published image must match the release commit")
    expected = {archive_name(tag, *target) for target in TARGETS} | {"openapi.yaml", "release-manifest.json"}
    lines = (output / "checksums.txt").read_text(encoding="utf-8").splitlines()
    recorded = dict((line.split("  ", 1)[1], line.split("  ", 1)[0]) for line in lines)
    if set(recorded) != expected or any(sha256(output / name) != digest for name, digest in recorded.items()):
        raise ValueError("Release files do not match their checksums")
    if not run("git", "tag", "--list", tag):
        github("api", "repos/{owner}/{repo}/git/refs", "-f", f"ref=refs/tags/{tag}", "-f", f"sha={args.commit}")
    generated = json.loads(github("api", "repos/{owner}/{repo}/releases/generate-notes", "-f", f"tag_name={tag}", "-f", f"target_commitish={args.commit}"))
    repository = os.environ.get("GITHUB_REPOSITORY") or github("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
    channel = "This is an alpha/prerelease build for early testing.\n\n" if "-alpha." in tag else (
        "This is a prerelease build.\n\n" if "-" in tag else "")
    body = (f"{channel}## Downloads\n\nChoose the archive for your operating system and CPU. Each includes the panel with its web UI, the storage helper, configuration example and licenses.\n\n"
            f"Verify downloads against **checksums.txt**. **release-manifest.json** records the exact source commit and Docker image digest. Back up your database and configuration before upgrading; preserve server worlds and existing data.\n\n"
            f"Container images (Linux amd64 and arm64):\n\n- Docker Hub: `{manifest['image']}`\n- GitHub Packages: `{manifest['github_image']}`\n\n"
            f"[Installation and verification](https://github.com/{repository}/blob/{tag}/docs/releases.md)\n\n" + generated["body"])
    with tempfile.TemporaryDirectory(prefix="carbon-release-notes-") as temporary:
        notes = Path(temporary) / "notes.md"
        notes.write_text(body, encoding="utf-8")
        # Only drafts are reusable; the guard above refuses published versions.
        exists = subprocess.run(["gh", "release", "view", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
        if not exists:
            github("release", "create", tag, "--verify-tag", "--draft", "--title", f"Carbon Panel {tag}", "--notes-file", str(notes))
        github("release", "edit", tag, "--notes-file", str(notes))
        github("release", "upload", tag, *(str(output / name) for name in sorted(expected | {"checksums.txt"})), "--clobber")
        uploaded = get_release(tag)
        remote = {asset["name"]: asset.get("digest") for asset in uploaded["assets"]}
        complete = expected | {"checksums.txt"}
        if not uploaded["draft"] or set(remote) != complete or any(
                remote[name] != "sha256:" + sha256(output / name) for name in complete):
            raise ValueError("Uploaded draft assets do not match the verified local release")
    github("release", "edit", tag, "--draft=false", f"--prerelease={str('-' in tag).lower()}", f"--latest={str('-' not in tag).lower()}")


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
    for command_name in ("nightly-promote", "edge-promote"):
        promote_cmd = commands.add_parser(command_name)
        promote_cmd.add_argument("--commit", required=True)
        promote_cmd.add_argument("--digest", required=True)
        promote_cmd.add_argument("--previous-commit", default="")
        promote_cmd.add_argument("--previous-digest", default="")
        promote_cmd.add_argument("--previous-revisions", default="")
        promote_cmd.add_argument("--ci-verified", default="")
        promote_cmd.add_argument("--pinned-evidence", default="")
        promote_cmd.add_argument("--apply", action="store_true")
    module_resolver = commands.add_parser("module-version")
    module_resolver.add_argument("--version", required=True)
    ci_checker = commands.add_parser("verify-main-ci")
    ci_checker.add_argument("--commit", required=True)
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
    return parser


def main():
    args = build_parser().parse_args()
    # module-version is a CLI display command; the bare module_version() helper
    # validates a string and must never be used as its handler.
    handlers = {"module-version": module_version_info,
                "indexable-urls": indexable_urls_cli}
    handler = handlers.get(args.command,
                           globals().get(args.command.replace("-", "_")))
    if handler is None:
        raise ValueError("Unknown release command: " + args.command)
    handler(args)


if __name__ == "__main__":
    main()
