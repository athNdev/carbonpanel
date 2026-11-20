#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Read-only gates for the docs-only maintained-guide publication lane.

It qualifies the LIVE two-channel docs receipt before a build, proves that a
rebuilt site reproduces that receipt exactly, verifies the real rendered guide
content, and resolves the exact prior Pages deployment artifact for rollback.
No command here writes a registry, tag, release, alias or tracked main file.
"""
import argparse
import hashlib
import html
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
import urllib.error
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
from scripts import release

SCHEMA_VERSION = 3
CHANNELS = ("stable", "nightly")
SELECTOR_LABELS = {"stable": "Stable", "nightly": "Nightly"}
CHANNEL_ROUTES = {"stable": "/docs/", "nightly": "/docs/nightly/"}
LEGACY_DOC_SNAPSHOTS = ("1.0.39", "3.0.0-alpha.1")
STABLE_BASELINE = {
    "commit": "f88748be6a216b187e675bbb1f110df07e544148",
    "digest": "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001",
}
NIGHTLY_BASELINE = {
    "commit": "d8597d4e953522d2eb2fa6d4764965ae8e58686f",
    "digest": "sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce5e96d00fa1c64239c965a9302d",
}
PAGES_ARTIFACT_NAME = "github-pages"
# A rollback becomes the latest successful Pages deployment and serves this
# narrowly named artifact; it is a valid baseline once its bytes are validated.
PAGES_ARTIFACT_NAMES = ("github-pages", "github-pages-prior")
PAGES_ENVIRONMENT = "github-pages"
LIVE_INDEX_URL = "https://carbonpanel.dev/version-index.json"
DEPLOYMENT_STATES = frozenset({
    "success", "in_progress", "queued", "waiting", "pending",
    "failure", "error", "inactive",
})
SHA = re.compile(r"[0-9a-f]{40}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")


def valid_commit(value):
    return isinstance(value, str) and bool(SHA.fullmatch(value))


def valid_digest(value):
    return isinstance(value, str) and bool(DIGEST.fullmatch(value))


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def write_output(values):
    """Emit step outputs only when running inside Actions."""
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
            output.writelines(f"{key}={value}\n" for key, value in values.items())


def load_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def stable_identity(document):
    stable = document["channels"]["stable"]
    return {"tag": stable.get("tag"), "version": stable.get("version"),
            "commit": stable.get("commit"), "digest": (stable.get("image") or {}).get("digest")}


def validate_channel_contract(document, stable_source=None):
    """Fail-closed validation of the schema 3 two-channel contract.

    Accepts either the tracked source contract (which names a catalog) or the
    built public index (which inlines the classified versions).
    """
    problems = []
    if not isinstance(document, dict):
        return False, ["channel contract must be a JSON object"]
    if document.get("schema_version") != SCHEMA_VERSION:
        problems.append("schema_version must be 3")
    if not isinstance(document.get("repository"), str) \
            or not document["repository"].startswith("https://github.com/"):
        problems.append("repository must be an https GitHub URL")
    if not isinstance(document.get("catalog"), str) or not document["catalog"]:
        if not isinstance(document.get("versions"), list):
            problems.append("catalog path or an inlined versions list is required")
    channels = document.get("channels")
    if not isinstance(channels, dict) or set(channels) != set(CHANNELS):
        problems.append("channels must contain exactly stable and nightly")
        return False, problems
    blob = json.dumps(channels, sort_keys=True)
    for stale in LEGACY_DOC_SNAPSHOTS:
        if stale in blob:
            problems.append(f"legacy doc snapshot {stale} must not appear in channels")
    for name in CHANNELS:
        entry = channels[name]
        if not isinstance(entry, dict):
            problems.append(f"{name} channel must be an object")
            continue
        if entry.get("display") != SELECTOR_LABELS[name]:
            problems.append(f"{name} display must be {SELECTOR_LABELS[name]}")
        if not valid_commit(entry.get("commit")):
            problems.append(f"{name} commit must be a full 40-hex SHA")
        image = entry.get("image")
        if not isinstance(image, dict):
            problems.append(f"{name} image is required")
            continue
        if not valid_digest(image.get("digest")):
            problems.append(f"{name} image digest must be a valid sha256 digest")
        if image.get("revision") != entry.get("commit"):
            problems.append(f"{name} image revision must equal its commit")
        if entry.get("route") != CHANNEL_ROUTES[name]:
            problems.append(f"{name} route must be {CHANNEL_ROUTES[name]}")
        for key in ("commit_url", "pinned_link", "pinned_url"):
            if key in entry and entry[key] != f"{document['repository']}/commit/{entry.get('commit')}":
                problems.append(f"{name}.{key} must be the full-SHA commit URL")
    stable = channels["stable"]
    simage = stable.get("image", {}) if isinstance(stable, dict) else {}
    if simage.get("tag") != "latest" or simage.get("stable_alias") != "stable":
        problems.append("stable must pin latest and the stable alias")
    expected = stable_identity(stable_source) if stable_source is not None else STABLE_BASELINE
    actual = {"tag": stable.get("tag"), "version": stable.get("version"),
              "commit": stable.get("commit"), "digest": simage.get("digest")}
    if any(actual[field] != value for field, value in expected.items()):
        problems.append("stable must stay on the verified stable identity")
    nightly = channels["nightly"]
    nimage = nightly.get("image", {}) if isinstance(nightly, dict) else {}
    if nightly.get("tag") is not None or nimage.get("tag") != "nightly":
        problems.append("nightly must carry only the nightly image tag")
    return (not problems), problems


def resolve_live_docs_state(document, stable_source=None):
    """Classify the live state and return the exact render inputs.

    final -> render with the OBSERVED promoted revision/digest/receipt.
    baseline -> render the explicitly unpromoted matched baseline.
    Candidate, unknown, mismatched or invalid states fail closed.
    """
    ok, problems = validate_channel_contract(document, stable_source)
    if not ok:
        return None, "invalid live channel contract: " + "; ".join(problems)
    nightly = (document.get("channels") or {}).get("nightly") or {}
    promotion = nightly.get("promotion") or {}
    state = str(promotion.get("state") or "")
    image = nightly.get("image") or {}
    commit, digest = nightly.get("commit"), image.get("digest")
    if not valid_commit(commit) or not valid_digest(digest):
        return None, "live nightly must carry a full-SHA commit and verified digest"
    if image.get("revision") != commit:
        return None, "live nightly image revision does not match its commit"
    if state == "final":
        if str(promotion.get("promoted")).lower() != "true":
            return None, "an active final nightly must be promoted"
        receipt = promotion.get("receipt")
        if not isinstance(receipt, str) or not receipt:
            return None, "an active final nightly must carry its promotion receipt"
        return {"state": "final", "revision": commit, "digest": digest,
                "receipt": receipt}, ""
    if state == "baseline":
        if str(promotion.get("promoted")).lower() != "false":
            return None, "an unpromoted baseline must not claim promotion"
        if (commit, digest) != (NIGHTLY_BASELINE["commit"], NIGHTLY_BASELINE["digest"]):
            return None, "baseline markers do not match the explicitly observed baseline"
        if not image.get("bootstrap"):
            return None, "an unpromoted baseline must explicitly carry its bootstrap marker"
        return {"state": "baseline", "revision": commit, "digest": digest,
                "receipt": ""}, ""
    return None, f"unsupported live nightly promotion state {state!r}"


def qualify_public_stable(document, repository):
    """Bind the public Stable marker to the published release and both registries."""
    ok, problems = validate_channel_contract(document, stable_source=document)
    if not ok:
        raise ValueError("Invalid public channel contract: " + "; ".join(problems))
    stable = stable_identity(document)
    tag, commit, digest = stable["tag"], stable["commit"], stable["digest"]
    product = release.product_release_or_none(tag)
    legacy = tag == release.LEGACY_STABLE_TAG and stable["version"] == tag[1:]
    if not legacy and not (product and not product["prerelease"]
                           and stable["version"] == product["semver"]):
        raise ValueError("Public Stable marker needs a recognized stable release tag and version")
    if document["channels"]["stable"].get("promotion") != {"state": "published", "promoted": True}:
        raise ValueError("Stable docs do not describe a published release")
    read, endpoint = github_api(repository)
    latest = json.loads(read(f"{endpoint}/releases/latest"))
    if (latest.get("tag_name") != tag or latest.get("draft") is not False
            or latest.get("prerelease") is not False or not latest.get("published_at")):
        raise ValueError("The published GitHub Latest release disagrees with Stable docs")
    if json.loads(read(f"{endpoint}/commits/{tag}")).get("sha") != commit:
        raise ValueError("The GitHub release tag does not peel to the Stable revision")
    verified, reason, _ = release.release_provenance(f"v{stable['version']}", commit, digest,
                                                       [[latest]], git_tag=tag)
    if not verified:
        raise ValueError("Stable release provenance failed: " + reason)
    for reference in ("latest", "stable", stable["version"]):
        observations = release.channel_observe(reference)
        if legacy and reference == "stable" and all(
                observations.get(registry, [None])[0] == "absent"
                for registry in release.CHANNEL_REGISTRIES):
            continue
        verified, reason = release.channel_oci_parity(observations, digest, commit)
        if not verified:
            raise ValueError(f"Stable {reference!r} registry evidence failed: {reason}")
    return stable


def stable_render_inputs(stable, root=ROOT):
    """Pass the verified product pins, preserving only an exact tracked legacy identity."""
    if stable["tag"] == release.LEGACY_STABLE_TAG:
        tracked = stable_identity(load_json(Path(root) / "docs/website-source.json"))
        if tracked != stable:
            raise ValueError("The verified legacy Stable identity differs from the tracked renderer source")
        return {"stable_tag": "", "stable_revision": "", "stable_digest": ""}
    return {"stable_tag": stable["tag"], "stable_revision": stable["commit"],
            "stable_digest": stable["digest"]}


def verify_docs_receipt(built, live):
    """A docs-only rebuild must preserve the exact live product/image receipt."""
    problems = []
    for label, document in (("built", built), ("live", live)):
        if not isinstance(document, dict):
            return False, [f"{label} docs index is not an object"]
        if document.get("schema_version") != SCHEMA_VERSION:
            problems.append(f"{label} docs index must be schema_version 3")
        if set((document.get("channels") or {})) != set(CHANNELS):
            problems.append(f"{label} channels must be exactly stable and nightly")
    if problems:
        return False, problems
    for name in CHANNELS:
        before, after = live["channels"][name], built["channels"][name]
        if after != before:
            problems.append(f"{name} product/image receipt changed from the live deployment")
        if (after.get("image") or {}).get("revision") != after.get("commit"):
            problems.append(f"{name} image revision does not equal its channel commit")
    if built.get("promotion") != live.get("promotion"):
        problems.append("top-level promotion state changed from the live deployment")
    return (not problems), problems


def verify_maintained_guides(site, channels, expected_revision):
    """Prove each rendered channel carries the trusted guide provenance."""
    if not valid_commit(expected_revision):
        return False, ["maintained guide source revision must be a full 40-hex commit"]
    problems = []
    for name in CHANNELS:
        entry = (channels or {}).get(name) or {}
        route = str(entry.get("route") or "").strip("/")
        manifest = Path(site) / route / "maintained-guides.json"
        if not manifest.is_file():
            problems.append(f"{name} channel has no maintained guide provenance")
            continue
        document = json.loads(manifest.read_text(encoding="utf-8"))
        source = document.get("source") or {}
        if source.get("revision") != expected_revision:
            problems.append(f"{name} maintained guide revision is not the tested checkout")
        if source.get("origin") not in ("git HEAD", "approved verified CI revision"):
            problems.append(f"{name} maintained guide revision origin is not trusted")
        records = document.get("channels") or []
        if not isinstance(records, list) or not records:
            problems.append(f"{name} maintained guide manifest has no per-guide provenance")
        for record in records:
            if not re.fullmatch(r"[0-9a-f]{64}", str(record.get("source_sha256") or "")):
                problems.append(f"{name} guide {record.get('slug')} has no exact content digest")
    return (not problems), problems


def verify_public_maintained_guides(evidence, revision):
    """Prove the live stamped guide sources come from the tested commit."""
    if not valid_commit(revision):
        return False, ["guide source revision must be a full 40-hex commit"]
    problems = []
    for channel in CHANNELS:
        manifest = (evidence or {}).get(channel)
        if not isinstance(manifest, dict):
            problems.append(f"{channel} has no live maintained-guide receipt")
            continue
        if (manifest.get("source") or {}).get("revision") != revision:
            problems.append(f"{channel} live guide source is not the tested commit")
        records = manifest.get("channels") or []
        if not isinstance(records, list) or not records:
            problems.append(f"{channel} live guide has no per-guide provenance")
        for record in records:
            if not re.fullmatch(r"[0-9a-f]{64}", str(record.get("source_sha256") or "")):
                problems.append(f"{channel} live guide {record.get('slug')} has no exact digest")
    return (not problems), problems


def verify_channel_docs(index, channel, commit, digest, page_texts=(),
                        contains=(), excludes=(), stable_source=None):
    """Prove a rendered channel is the exact pinned revision with real content."""
    channels = (index or {}).get("channels") or {}
    entry = channels.get(channel) or {}
    problems = []
    if entry.get("commit") != commit:
        problems.append(f"{channel} commit is {entry.get('commit')!r}, expected {commit}")
    image = entry.get("image") or {}
    if image.get("digest") != digest:
        problems.append(f"{channel} digest is {image.get('digest')!r}, expected {digest}")
    if not str(entry.get("route") or "").startswith("/docs/"):
        problems.append(f"{channel} route must be a /docs/ path")
    for text in page_texts:
        for needle in contains:
            if needle not in text:
                problems.append(f"{channel} page is missing required content {needle!r}")
        for needle in excludes:
            if needle in text:
                problems.append(f"{channel} page still contains forbidden content {needle!r}")
    if stable_source is not None:
        if stable_identity(index) != stable_identity(stable_source):
            problems.append("stable identity no longer matches the live receipt")
    return (not problems), problems


GUIDE_PATH_PREFIX = "docs/carbon-panel/src/content/docs/"


def guide_fingerprint(declaration, revision, blobs, presentation=None):
    """Fingerprint the declaration plus the exact maintained source blobs.

    This is release-independent liveness: a later unrelated merge, a cancelled
    queue or a rolled-back commit cannot hide a guide change because the
    fingerprint is derived from the current trusted commit's real Git objects.
    """
    if not valid_commit(revision):
        raise ValueError("guide fingerprint needs the exact 40-hex trusted revision")
    if not isinstance(declaration, dict) or declaration.get("schema_version") != 1:
        raise ValueError("maintained guide declaration must be schema_version 1")
    guides = declaration.get("guides")
    if not isinstance(guides, list) or not guides:
        raise ValueError("maintained guide declaration must list at least one guide")
    records = []
    for guide in guides:
        path = guide.get("source_path")
        if not isinstance(path, str) or not path.startswith(GUIDE_PATH_PREFIX) \
                or "\\" in path or any(part in ("", ".", "..") for part in path.split("/")):
            raise ValueError(f"maintained source path is outside the allowlisted docs tree: {path!r}")
        content = blobs.get(path)
        if content is None:
            raise ValueError(f"maintained source blob is missing at the trusted revision: {path}")
        provenance = guide.get("provenance") or {}
        records.append({"slug": guide.get("slug"), "source_path": path,
                        "source_sha256": hashlib.sha256(content).hexdigest(),
                        "mode": guide.get("mode"),
                        "scope": provenance.get("scope", "")})
    declaration_hash = hashlib.sha256(
        json.dumps(declaration, sort_keys=True).encode("utf-8")).hexdigest()
    identity = {"schema_version": declaration.get("schema_version"),
                "repository": declaration.get("repository"),
                "compatibility": declaration.get("compatibility"),
                "guides": records}
    payload = {"declaration_sha256": declaration_hash,
               "revision": revision, "guides": records}
    presentation_hash = ""
    if presentation is not None:
        presentation_hash = str(presentation.get("fingerprint", "")) \
            if isinstance(presentation, dict) else str(presentation)
        if not re.fullmatch(r"[0-9a-f]{64}", presentation_hash):
            raise ValueError("presentation fingerprint must be a SHA-256")
        payload["presentation_fingerprint"] = presentation_hash
    return {"fingerprint": hashlib.sha256(
        json.dumps(payload, sort_keys=True).encode("utf-8")).hexdigest(),
        "revision": revision, "declaration_sha256": declaration_hash,
        "presentation_fingerprint": presentation_hash,
        "manifest_identity": identity,
        "manifest_identity_hash": manifest_identity_hash(identity),
        "guides": records}


def candidate_guide_fingerprint(declaration_path, revision, root, presentation=None):
    declaration = load_json(declaration_path)
    blobs = {}
    for guide in declaration.get("guides") or []:
        path = guide.get("source_path")
        blobs[path] = subprocess.check_output(
            ["git", "show", f"{revision}:{path}"], cwd=root)
    return guide_fingerprint(declaration, revision, blobs, presentation=presentation)


PUBLISHED_INDEX_ROUTES = ("/version-index.json", "/docs/version-index.json")


def published_route_liveness(get_bytes):
    """Narrow reusable check for the documented public metadata route.

    A missing or drifted `/docs/version-index.json` is a publication defect
    even when maintained guide bytes are unchanged. Network failures are not
    converted into 404s and therefore fail closed.
    """
    observed = live_bytes(get_bytes, PUBLISHED_INDEX_ROUTES)
    problems = []
    routes = {}
    for path, value in observed.items():
        if value is None:
            problems.append(f"published route {path} is missing")
            routes[path] = ""
        else:
            routes[path] = sha256_bytes(value)
    if all(observed.values()) and observed[PUBLISHED_INDEX_ROUTES[0]] != \
            observed[PUBLISHED_INDEX_ROUTES[1]]:
        problems.append("docs version index disagrees with the site root copy")
    return {"routes": routes, "problems": problems}


def fetch_live_guide_evidence(get_bytes):
    """Read the per-channel guide receipts and published-route liveness."""
    evidence = {}
    for channel in CHANNELS:
        path = "/" + CHANNEL_ROUTES[channel].strip("/") + "/maintained-guides.json"
        try:
            evidence[channel] = json.loads(get_bytes(path).decode("utf-8"))
        except (urllib.error.HTTPError, FileNotFoundError) as error:
            if isinstance(error, urllib.error.HTTPError) and error.code != 404:
                raise
            evidence[channel] = None
        if isinstance(evidence[channel], dict):
            presentation_path = "/" + CHANNEL_ROUTES[channel].strip("/") + "/presentation.json"
            try:
                evidence[channel]["presentation"] = json.loads(get_bytes(presentation_path).decode("utf-8"))
            except (urllib.error.HTTPError, FileNotFoundError) as error:
                if isinstance(error, urllib.error.HTTPError) and error.code != 404:
                    raise
                evidence[channel]["presentation"] = None
    route_state = published_route_liveness(get_bytes)
    evidence["published_routes"] = route_state["routes"]
    evidence["published_route_problems"] = route_state["problems"]
    return evidence


def _public_evidence(base_url):
    def get_bytes(path):
        request = urllib.request.Request(
            base_url.rstrip("/") + path,
            headers={"User-Agent": "carbonpanel-docs-gate"})
        with urllib.request.urlopen(request, timeout=60) as response:
            return response.read()
    return fetch_live_guide_evidence(get_bytes)


def _guide_identity(record):
    """Release-independent identity of one maintained guide."""
    return (record.get("source_path"), record.get("mode"),
            (record.get("provenance") or {}).get("scope", record.get("scope", "")),
            record.get("source_sha256"))


def guide_change_decision(candidate, live_evidence):
    """Publish when live guide bytes/paths/scopes are missing or differ.

    The exact source revision is a deployment qualification (the built site
    must stamp the tested commit), NOT a liveness trigger. An unrelated CI
    commit with identical maintained inputs is a safe no-op.
    """
    if not candidate or not candidate.get("fingerprint"):
        return {"publish": False, "reason": "no candidate guide fingerprint", "problems": []}
    expected = {record.get("slug"): _guide_identity(record)
                for record in candidate.get("guides") or []}
    declaration_hash = candidate.get("declaration_sha256", "")
    problems = []
    for channel in CHANNELS:
        manifest = (live_evidence or {}).get(channel)
        if not isinstance(manifest, dict):
            problems.append(f"{channel} has no live maintained-guide receipt")
            continue
        records = [record for record in (manifest.get("channels") or [])
                   if isinstance(record, dict)]
        actual = {record.get("slug"): _guide_identity(record) for record in records}
        if set(actual) != set(expected):
            problems.append(f"{channel} live maintained guide set differs from the candidate")
        elif any(actual[key] != value for key, value in expected.items()):
            problems.append(f"{channel} live maintained guide bytes, paths or scopes differ")
        # The canonical declaration hash is stamped into every published
        # channel record by the shared build, so selector-only edits are not
        # invisible; source HEAD movement with identical inputs stays a no-op.
        if manifest.get("declaration_sha256") != declaration_hash:
            problems.append(f"{channel} live maintained declaration hash is stale")
        # Changelog/banner presentation is a SEPARATE channel input, never a
        # release-independent maintained guide. A version-banner-only fix must
        # still reconcile Pages: its fingerprint is stamped into the built
        # presentation.json and compared here, not folded into the guide receipt.
        wanted_presentation = candidate.get("presentation_fingerprint", "")
        live_presentation = (manifest.get("presentation") or {}).get("fingerprint")
        if wanted_presentation and live_presentation != wanted_presentation:
            problems.append(f"{channel} live channel presentation is stale")
    problems.extend((live_evidence or {}).get("published_route_problems") or [])
    return {"publish": bool(problems),
            "reason": ("live maintained guide receipt is missing or stale"
                       if problems else "live maintained guide receipt is current"),
            "problems": problems}


def recheck_main_tip(repository, expected):
    """Recheck that trusted main still points at the exact tested commit."""
    api, root = github_api(repository)
    first = json.loads(api(f"{root}/commits/main")).get("sha", "")
    second = json.loads(api(f"{root}/commits/main")).get("sha", "")
    return {"verified": bool(first) and first == second == expected,
            "tip": first, "expected": expected}


def pages_deployment_identity(deployments, statuses_by_id, environment=PAGES_ENVIRONMENT):
    """Resolve the last SUCCESSFUL Pages deployment, independent of artifacts."""
    candidates = [item for item in (deployments or [])
                  if isinstance(item, dict) and item.get("environment") == environment]
    if not candidates:
        return None, "no github-pages deployment was observed"
    candidates.sort(key=lambda item: (str(item.get("created_at") or ""),
                                      int(item.get("id") or 0)), reverse=True)
    for deployment in candidates:
        statuses = statuses_by_id.get(str(deployment.get("id"))) or []
        if not statuses:
            return None, f"deployment {deployment.get('id')} has no observed statuses"
        state = str(statuses[0].get("state") or "").lower()
        if state not in DEPLOYMENT_STATES:
            return None, f"deployment {deployment.get('id')} has unknown state {state!r}"
        if state != "success":
            continue
        match = re.search(r"/actions/runs/(\d+)", str(statuses[0].get("target_url") or ""))
        if not match:
            return None, f"successful deployment {deployment.get('id')} has no resolvable run id"
        return {"deployment_id": str(deployment.get("id")), "run_id": match.group(1),
                "sha": str(deployment.get("sha") or ""),
                "environment": environment, "state": "success"}, ""
    return None, "no successful github-pages deployment was observed"


def pages_baseline_from_deployments(deployments, statuses_by_id, artifacts_by_run=None,
                                    environment=PAGES_ENVIRONMENT):
    """Resolve deployment identity, then name its candidate Pages artifacts.

    Artifact availability never hides the deployment SHA. A normal run may hold
    both `github-pages` and `github-pages-prior`; the fetch step disambiguates
    them by verifying exact current live bytes (source manifests included).
    """
    identity, reason = pages_deployment_identity(deployments, statuses_by_id, environment)
    if not identity:
        return None, reason
    artifacts = (artifacts_by_run or {}).get(identity["run_id"]) or []
    candidates = [artifact for artifact in artifacts
                  if isinstance(artifact, dict)
                  and artifact.get("name") in PAGES_ARTIFACT_NAMES
                  and not artifact.get("expired") and artifact.get("id")]
    if not candidates:
        return {**identity, "artifact_id": "", "artifact_name": "",
                "artifact_candidates": [], "artifact_verified": False}, (
                    "deployment resolved; its Pages artifact is unavailable or expired")
    return {**identity,
            "artifact_id": str(candidates[0]["id"]),
            "artifact_name": candidates[0].get("name", ""),
            "artifact_candidates": [str(item["id"]) for item in candidates],
            "artifact_verified": True}, ""


def github_api(repository):
    """Return an authenticated read-only GitHub API reader."""
    token = os.environ.get("GH_TOKEN", "")

    def api(url):
        request = urllib.request.Request(
            url, headers={"Accept": "application/vnd.github+json",
                          "X-GitHub-Api-Version": "2022-11-28",
                          "User-Agent": "carbonpanel-docs-gate"})
        if token:
            # A signed artifact host rejects a forwarded API token on redirect.
            request.add_unredirected_header("Authorization", f"Bearer {token}")
        with urllib.request.urlopen(request, timeout=120) as response:
            return response.read()

    return api, f"https://api.github.com/repos/{repository}"


def pages_baseline(repository):
    api, root = github_api(repository)
    deployments = json.loads(api(
        f"{root}/deployments?environment={PAGES_ENVIRONMENT}&per_page=20"))
    statuses_by_id = {}
    for deployment in deployments[:20]:
        identifier = str(deployment.get("id"))
        statuses_by_id[identifier] = json.loads(
            api(f"{root}/deployments/{identifier}/statuses?per_page=10"))
    artifacts_by_run = {}
    for statuses in statuses_by_id.values():
        if not statuses or str(statuses[0].get("state") or "").lower() != "success":
            continue
        match = re.search(r"/actions/runs/(\d+)", str(statuses[0].get("target_url") or ""))
        if not match or match.group(1) in artifacts_by_run:
            continue
        listing = json.loads(api(f"{root}/actions/runs/{match.group(1)}/artifacts?per_page=100"))
        artifacts_by_run[match.group(1)] = listing.get("artifacts") or []
    return pages_baseline_from_deployments(deployments, statuses_by_id, artifacts_by_run)


def _extract_pages_artifact(archive, destination):
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        bundle.extractall(destination)
    site = Path(destination)
    tarball = next((path for path in site.rglob("artifact.tar")), None)
    if tarball is not None:
        staging = site / "_artifact"
        staging.mkdir()
        with tarfile.open(tarball) as source:
            source.extractall(staging, filter="data")
        return staging
    if not (site / "version-index.json").is_file():
        nested = next((path.parent for path in site.rglob("version-index.json")), None)
        if nested is not None:
            return nested
    return site


def fetch_prior_pages(destination, artifacts, download, live_bytes,
                      artifact_id=None, run_id=None, live_url=LIVE_INDEX_URL):
    """Install the exact resolved artifact only when its bytes equal live."""
    live_hash = sha256_bytes(live_bytes)
    if artifact_id is not None:
        selected = [item for item in artifacts if str(item.get("id")) == str(artifact_id)]
        if len(selected) != 1:
            raise ValueError("The resolved Pages baseline artifact is not present exactly once")
        if selected[0].get("expired"):
            raise ValueError("The resolved Pages baseline artifact has expired")
        candidates = selected
    else:
        candidates = [item for item in artifacts
                      if not item.get("expired")
                      and item.get("name") in PAGES_ARTIFACT_NAMES]
    for artifact in candidates:
        with tempfile.TemporaryDirectory(prefix="carbon-prior-pages-") as temporary:
            staging = Path(temporary) / "artifact"
            staging.mkdir()
            site = _extract_pages_artifact(download(artifact), staging)
            index = site / "version-index.json"
            if not index.is_file() or sha256_bytes(index.read_bytes()) != live_hash:
                continue
            target = Path(destination)
            if target.exists():
                shutil.rmtree(target)
            shutil.copytree(site, target)
            # Disambiguate multiple narrowly named artifacts by ALL current
            # live bytes (source manifests and declared pages included).
            base_url = live_url.rsplit("/version-index.json", 1)[0] or live_url
            ok, _ = verify_prior_bytes(target, base_url)
            if not ok:
                continue
            workflow_run = artifact.get("workflow_run") or {}
            return {"verified": True,
                    "hash": "sha256:" + sha256_bytes((target / "version-index.json").read_bytes()),
                    "artifact_id": str(artifact.get("id", "")),
                    "artifact_commit": workflow_run.get("head_sha", ""),
                    "run_id": str(workflow_run.get("id") or run_id or ""),
                    "live_hash": "sha256:" + live_hash}
    raise ValueError("No Pages artifact matches the live version-index.json; "
                     "the exact prior bytes cannot be verified or restored")


def _http_get(base_url):
    def get_bytes(path):
        request = urllib.request.Request(
            base_url.rstrip("/") + path,
            headers={"User-Agent": "carbonpanel-docs-gate"})
        with urllib.request.urlopen(request, timeout=60) as response:
            return response.read()
    return get_bytes


def _local_get(site):
    def get_bytes(path):
        target = Path(site) / path.lstrip("/")
        if not target.is_file():
            raise ValueError(f"Missing built file: {path}")
        return target.read_bytes()
    return get_bytes


def normalized_manifest_identity(manifest):
    """The declaration-derived part of a built maintained-guide manifest."""
    if not isinstance(manifest, dict):
        return None
    guides = []
    for record in manifest.get("channels") or []:
        if not isinstance(record, dict):
            continue
        guides.append({
            "slug": record.get("slug"), "mode": record.get("mode"),
            "source_path": record.get("source_path"),
            "source_sha256": record.get("source_sha256"),
            "scope": (record.get("provenance") or {}).get("scope", ""),
        })
    return {"schema_version": manifest.get("schema_version"),
            "repository": manifest.get("repository"),
            "compatibility": manifest.get("compatibility"),
            "guides": guides}


def manifest_identity_hash(identity):
    if identity is None:
        return ""
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode("utf-8")).hexdigest()


def verify_manifest_identity(candidate, manifest):
    """Prove the rendered manifest carries the exact candidate inputs.

    Includes the normalized declaration identity so a fragment selector-only
    change (source/target summary) cannot pass on unchanged guide bytes.
    """
    problems = []
    actual = normalized_manifest_identity(manifest)
    expected = candidate.get("manifest_identity")
    if actual is None:
        return False, ["maintained guide manifest is missing"]
    if manifest_identity_hash(actual) != candidate.get("manifest_identity_hash"):
        problems.append("maintained manifest declaration identity differs from the candidate")
    if expected and {record.get("slug"): record for record in expected.get("guides", [])} != \
            {record.get("slug"): record for record in actual.get("guides", [])}:
        problems.append("maintained guide paths, digests or scopes differ from the candidate")
    return (not problems), problems


def channel_presentation_paths(live_index):
    """Presentation and changelog receipt paths for every published version.

    Both the canonical channel route and the immutable archive route carry the
    same generated presentation, so a banner/changelog-only change must be
    attested at each path, not merely at one canonical copy. A legacy index
    without `versions` falls back to the two canonical channel routes.
    """
    routes = []
    for version in live_index.get("versions") or []:
        if not isinstance(version, dict):
            continue
        for key in ("route", "immutable_route"):
            route = str(version.get(key) or "").strip("/")
            if route:
                routes.append(route)
    if not routes:
        routes = [value.strip("/") for value in CHANNEL_ROUTES.values()]
    routes = sorted(set(routes))
    return ([f"/{route}/presentation.json" for route in routes],
            [f"/{route}/changelog/index.html" for route in routes])


def _presentation_fingerprint(payload):
    """The fingerprint actually carried by a built presentation.json."""
    if not isinstance(payload, dict):
        return ""
    value = str(payload.get("fingerprint", ""))
    return value if re.fullmatch(r"[0-9a-f]{64}", value) else ""


def stamp_publication_receipt(site, candidate, live_index, source_revision):
    """Stamp a publication receipt into the built artifact (never main)."""
    if not valid_commit(source_revision):
        raise ValueError("publication receipt needs the full trusted source revision")
    channels = {}
    for name in CHANNELS:
        entry = (live_index.get("channels") or {}).get(name) or {}
        channels[name] = {"commit": entry.get("commit"),
                          "digest": (entry.get("image") or {}).get("digest"),
                          "state": (entry.get("promotion") or {}).get("state")}
    page_hashes = {}
    root = Path(site)
    paths = list(PUBLISHED_INDEX_ROUTES)
    for name in CHANNELS:
        entry = (live_index.get("channels") or {}).get(name) or {}
        route = str(entry.get("route") or "").strip("/")
        manifest_path = root / route / "maintained-guides.json"
        if not manifest_path.is_file():
            continue
        paths.append(f"/{route}/maintained-guides.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        for record in manifest.get("channels") or []:
            paths.append(_rendered_page_path(entry, record))
    for path in dict.fromkeys(paths):
        local = root / path.lstrip("/")
        if local.is_file():
            page_hashes[path] = sha256_bytes(local.read_bytes())
    # Channel presentation is a real built input, not an echoed candidate
    # string. When the candidate declares a presentation fingerprint, read the
    # ACTUAL presentation.json at canonical AND stable archive paths, require a
    # valid SHA-256 that matches the candidate, and attest its bytes together
    # with the generated changelog HTML. A wrong or missing file fails here
    # instead of passing on a hash copied from the candidate. Legacy candidates
    # without a presentation fingerprint keep the original path.
    presentation_hash = str(candidate.get("presentation_fingerprint") or "")
    if presentation_hash:
        if not re.fullmatch(r"[0-9a-f]{64}", presentation_hash):
            raise ValueError("candidate channel presentation fingerprint must be a SHA-256")
        presentation_paths, changelog_paths = channel_presentation_paths(live_index)
        for path in presentation_paths:
            local = root / path.lstrip("/")
            if not local.is_file():
                raise ValueError(f"channel presentation receipt is missing: {path}")
            try:
                payload = json.loads(local.read_text(encoding="utf-8"))
            except (ValueError, UnicodeDecodeError) as error:
                raise ValueError(f"channel presentation receipt is malformed: {path}: {error}")
            actual = _presentation_fingerprint(payload)
            if not actual:
                raise ValueError(f"channel presentation receipt has no valid fingerprint: {path}")
            if actual != presentation_hash:
                raise ValueError(f"channel presentation fingerprint disagrees with candidate: {path}")
            page_hashes[path] = sha256_bytes(local.read_bytes())
        for path in changelog_paths:
            local = root / path.lstrip("/")
            if not local.is_file():
                raise ValueError(f"generated changelog page is missing: {path}")
            page_hashes[path] = sha256_bytes(local.read_bytes())
    nightly = (live_index.get("channels") or {}).get("nightly") or {}
    receipt = {"schema_version": 1, "source_revision": source_revision,
               "nightly_promotion_receipt": (nightly.get("promotion") or {}).get("receipt", ""),
               "declaration_sha256": candidate.get("declaration_sha256", ""),
               "guide_fingerprint": candidate.get("fingerprint", ""),
               "presentation_fingerprint": candidate.get("presentation_fingerprint", ""),
               "manifest_identity_hash": candidate.get("manifest_identity_hash", ""),
               "channels": channels, "page_hashes": page_hashes,
               "baseline_mode": "exact-artifact"}
    path = Path(site) / "publication-receipt.json"
    path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return receipt


def verify_publication_receipt(receipt, candidate, live_index, source_revision,
                               get_bytes=None):
    problems = []
    if not isinstance(receipt, dict):
        return False, ["publication receipt is missing"]
    for key in ("declaration_sha256", "guide_fingerprint", "manifest_identity_hash"):
        field = {"guide_fingerprint": "fingerprint"}.get(key, key)
        if receipt.get(key) != candidate.get(field):
            problems.append(f"publication receipt {key} does not match the candidate")
    # Legacy receipts predate channel presentation and stay compatible. Once a
    # candidate declares presentation, the key is REQUIRED, valid, and equal:
    # a VersionBanner/changelog-only change must therefore reconcile Pages.
    presented = str(candidate.get("presentation_fingerprint") or "")
    if presented:
        if not re.fullmatch(r"[0-9a-f]{64}", presented):
            problems.append("candidate channel presentation fingerprint is not a SHA-256")
        if "presentation_fingerprint" not in receipt:
            problems.append("publication receipt is missing the presentation fingerprint")
        elif receipt["presentation_fingerprint"] != presented:
            problems.append("publication receipt presentation fingerprint does not match the candidate")
    elif receipt.get("presentation_fingerprint") not in (None, ""):
        problems.append("legacy publication receipt has an unexpected presentation fingerprint")
    if receipt.get("source_revision") != source_revision:
        problems.append("publication receipt source revision is not the tested commit")
    for name in CHANNELS:
        entry = (live_index.get("channels") or {}).get(name) or {}
        recorded = (receipt.get("channels") or {}).get(name) or {}
        if (recorded.get("commit") != entry.get("commit")
                or recorded.get("digest") != (entry.get("image") or {}).get("digest")):
            problems.append(f"publication receipt {name} channel receipt changed")
    if get_bytes is not None:
        attested = receipt.get("page_hashes") or {}
        if not attested:
            problems.append("publication receipt has no page byte hashes")
        if presented and re.fullmatch(r"[0-9a-f]{64}", presented):
            presentation_paths, changelog_paths = channel_presentation_paths(live_index)
            for path in presentation_paths + changelog_paths:
                if path not in attested:
                    problems.append(f"publication receipt does not attest channel bytes: {path}")
            # Read the real served presentation.json: a hash echoed from the
            # candidate on an absent or wrong file must never pass.
            for path in presentation_paths:
                try:
                    payload = json.loads(get_bytes(path).decode("utf-8"))
                except Exception as error:
                    problems.append(f"public channel presentation {path} is unavailable or malformed: {error}")
                    continue
                actual = _presentation_fingerprint(payload)
                if not actual:
                    problems.append(f"public channel presentation {path} has no valid fingerprint")
                elif actual != presented:
                    problems.append(f"public channel presentation fingerprint at {path} does not match the candidate")
        for path, digest in attested.items():
            try:
                observed = get_bytes(path)
            except Exception as error:
                problems.append(f"public page {path} is unavailable: {error}")
                continue
            if sha256_bytes(observed) != digest:
                problems.append(f"public page {path} bytes differ from the receipt")
    return (not problems), problems


def _rendered_page_path(entry, record):
    target = str(record.get("target_file") or "")
    if not target.endswith((".mdx", ".md")) or ".." in target or "\\" in target:
        raise ValueError("maintained target file is not an allowlisted docs page")
    route = str(entry.get("route") or "").strip("/")
    stem = target.rsplit(".", 1)[0]
    return f"/{route}/{stem}/index.html"


def _details_block(text, summary):
    pattern = re.compile(
        r"^[ \t]*<details>\s*<summary>\s*" + re.escape(summary)
        + r"\s*</summary>[\s\S]*?^[ \t]*</details>", re.MULTILINE)
    matches = pattern.findall(text)
    if len(matches) != 1:
        raise ValueError(f"maintained fragment summary {summary!r} matched {len(matches)} blocks")
    return matches[0]


def _normalized(text):
    return re.sub(r"\s+", " ", html.unescape(re.sub(r"<[^>]+>", " ", text))).strip()


def verify_rendered_guide_links(get_bytes, live_index, root=None):
    """Verify each declared guide against its exact maintained source.

    Page guides carry the renderer's precise source link. Fragment guides carry
    no such link, so they are verified by their declared `<details>` anchor and
    an exact content line from the source fragment at the attested revision.
    No hardcoded guide URL or word-token heuristic is used.
    """
    declarations = {}
    if root:
        path = Path(root) / "docs/maintained-guides.json"
        if path.is_file():
            for guide in load_json(path).get("guides") or []:
                declarations[guide.get("slug")] = guide
    problems = []
    for name in CHANNELS:
        entry = (live_index.get("channels") or {}).get(name) or {}
        route = str(entry.get("route") or "").strip("/")
        try:
            manifest = json.loads(get_bytes(f"/{route}/maintained-guides.json").decode("utf-8"))
        except Exception as error:
            problems.append(f"{name} maintained manifest is unreadable: {error}")
            continue
        source_revision = (manifest.get("source") or {}).get("revision", "")
        if not valid_commit(source_revision):
            problems.append(f"{name} maintained manifest has no trusted source revision")
        for record in manifest.get("channels") or []:
            path = _rendered_page_path(entry, record)
            try:
                rendered = get_bytes(path).decode("utf-8", "replace")
            except Exception:
                problems.append(f"{name} rendered guide {path} is missing")
                continue
            if not rendered.strip():
                problems.append(f"{name} rendered guide {path} is empty")
                continue
            if record.get("mode") == "fragment":
                declaration = declarations.get(record.get("slug"))
                if not declaration or not declaration.get("source_summary") \
                        or not declaration.get("target_summary"):
                    problems.append(f"{name} fragment {record.get('slug')} has no declared anchor")
                    continue
                rendered_normalized = _normalized(rendered)
                anchor = _normalized("<summary>" + declaration["source_summary"] + "</summary>")
                if anchor not in rendered_normalized:
                    problems.append(f"{name} fragment {path} lacks its declared source anchor")
                if _normalized(declaration["target_summary"]) in rendered_normalized:
                    problems.append(f"{name} fragment {path} still contains the retired locator")
                try:
                    source = subprocess.check_output(
                        ["git", "show", f"{source_revision}:{record.get('source_path')}"],
                        cwd=root, stderr=subprocess.DEVNULL).decode("utf-8", "replace")
                    block = _details_block(source, declaration["source_summary"])
                    body = _normalized(block.split("</summary>", 1)[-1])
                    lines = [line.strip() for line in re.split(r"(?<=[.!?])\s+", body)
                             if len(line.strip()) >= 20]
                    if not lines or not any(line in rendered_normalized for line in lines):
                        problems.append(
                            f"{name} fragment {path} lacks the exact maintained source content")
                except Exception as error:
                    problems.append(f"{name} fragment source is unavailable: {error}")
            else:
                link = ("https://github.com/athNdev/carbonpanel/blob/" + source_revision
                        + "/" + str(record.get("source_path")))
                if link not in rendered:
                    problems.append(f"{name} rendered guide {path} lacks its exact source link")
    return (not problems), problems


def live_bytes(get_bytes, paths):
    """Fetch exact URL paths; 404 is a fact, any other failure is an error.

    Returns {path: bytes} where a confirmed 404 is recorded as None. File keys
    are the real URL paths, never synthetic per-channel labels.
    """
    result = {}
    for path in paths:
        try:
            result[path] = get_bytes(path)
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            result[path] = None
    return result


def verify_prior_bytes(site, base_url):
    """Compare the artifact with live index, receipts and declared page bytes.

    A pre-receipt deployment legitimately has 404 for the optional receipt and
    per-channel guide manifests; that is accepted, while a network/5xx failure
    fails closed. A present receipt must match every byte it attests.
    """
    get_bytes = _http_get(base_url)
    index = json.loads(get_bytes("/version-index.json").decode("utf-8"))
    paths = list(PUBLISHED_INDEX_ROUTES) + ["/publication-receipt.json"]
    for name in CHANNELS:
        route = index["channels"][name]["route"].strip("/")
        paths.append(f"/{route}/maintained-guides.json")
    observed = live_bytes(get_bytes, paths)
    if observed["/version-index.json"] is None:
        return False, ["live version-index.json is unavailable"]
    problems = []
    for path, expected in observed.items():
        local = Path(site) / path.lstrip("/")
        if expected is None:
            if path == "/version-index.json":
                problems.append(f"{path} unexpectedly missing live")
            continue
        if not local.is_file() or local.read_bytes() != expected:
            problems.append(f"prior bytes for {path} differ from live")
    # Declared page paths come from the artifact's own manifests, so a
    # pre-receipt deployment (manifest/receipt 404) still verifies real pages.
    page_paths = []
    for name in CHANNELS:
        entry = index["channels"][name]
        manifest = Path(site) / entry["route"].strip("/") / "maintained-guides.json"
        if not manifest.is_file():
            continue
        for record in json.loads(manifest.read_text(encoding="utf-8")).get("channels") or []:
            page_paths.append(_rendered_page_path(entry, record))
    page_observed = live_bytes(get_bytes, page_paths)
    for path, live_value in page_observed.items():
        local = Path(site) / path.lstrip("/")
        if live_value is None:
            problems.append(f"declared live page {path} is unavailable")
        elif not local.is_file() or local.read_bytes() != live_value:
            problems.append(f"declared page {path} differs from live bytes")
    receipt_bytes = observed["/publication-receipt.json"]
    if receipt_bytes is not None:
        receipt = json.loads(receipt_bytes.decode("utf-8"))
        attested = receipt.get("page_hashes") or {}
        presented = str(receipt.get("presentation_fingerprint") or "")
        if presented:
            # After deploy the SAME required keys/paths must be present: a
            # legacy receipt (no presentation) keeps the old compatible path.
            if not re.fullmatch(r"[0-9a-f]{64}", presented):
                problems.append("live receipt presentation fingerprint is not a SHA-256")
            presentation_paths, changelog_paths = channel_presentation_paths(index)
            for path in presentation_paths + changelog_paths:
                if path not in attested:
                    problems.append(f"live receipt does not attest channel bytes: {path}")
            for path in presentation_paths:
                live_value = live_bytes(get_bytes, [path])[path]
                if live_value is None:
                    problems.append(f"live channel presentation is missing: {path}")
                    continue
                try:
                    payload = json.loads(live_value.decode("utf-8"))
                except (ValueError, UnicodeDecodeError):
                    problems.append(f"live channel presentation is malformed: {path}")
                    continue
                actual = _presentation_fingerprint(payload)
                if not actual:
                    problems.append(f"live channel presentation has no valid fingerprint: {path}")
                elif actual != presented:
                    problems.append(f"live channel presentation fingerprint disagrees with the receipt: {path}")
        for path, digest in attested.items():
            live_value = page_observed.get(path)
            if live_value is None:
                live_value = live_bytes(get_bytes, [path])[path]
            if live_value is None or sha256_bytes(live_value) != digest:
                problems.append(f"live bytes for {path} do not match the publication receipt")
            local = Path(site) / path.lstrip("/")
            if not local.is_file() or sha256_bytes(local.read_bytes()) != digest:
                problems.append(f"local bytes for {path} do not match the publication receipt")
    return (not problems), problems


BASELINE_GUIDE_SLUGS = ("getting-started/proxmox", "faq")


def _page_bytes(data):
    """Compare published bytes with platform line endings normalized."""
    return data.replace(b"\r\n", b"\n")


def recover_prior_pages(destination, base_url, deployment_sha, root,
                        trusted_declaration="", stable_source=None):
    """Rebuild ONLY from a deployment SHA confirmed on origin/main.

    The result is a RECONSTRUCTED baseline, never claimed as the exact artifact.
    The caller must have checked out that SHA in a separate worktree. A legacy
    deployment predating maintained guides gets no stamped receipt: its real
    pages are compared byte-for-byte against live instead.
    """
    if not valid_commit(deployment_sha):
        raise ValueError("recovery needs the exact deployment commit SHA")
    if subprocess.run(["git", "rev-parse", "HEAD"], cwd=root,
                      capture_output=True, text=True).stdout.strip() != deployment_sha:
        raise ValueError("recovery worktree HEAD is not the deployment SHA")
    if subprocess.run(["git", "merge-base", "--is-ancestor", deployment_sha, "origin/main"],
                      cwd=root).returncode != 0:
        raise ValueError("deployment SHA is not on origin/main")
    live = json.loads(_http_get(base_url)("/version-index.json").decode("utf-8"))
    resolved, reason = resolve_live_docs_state(live, stable_source)
    if not resolved:
        raise ValueError("live channel state is not qualified for recovery: " + reason)
    argv = [sys.executable, str(Path(root) / "scripts/build_website.py")]
    if resolved["state"] == "final":
        argv += ["--nightly-revision", resolved["revision"],
                 "--nightly-digest", resolved["digest"],
                 "--promotion-state", "final",
                 "--promotion-receipt", resolved["receipt"]]
    if stable_source is None:
        subprocess.check_call(argv, cwd=root)
    else:
        inputs = stable_render_inputs(stable_identity(stable_source), root)
        env = {**os.environ, **{f"CARBONPANEL_{key.upper()}": value for key, value in inputs.items()}}
        subprocess.check_call(argv, cwd=root, env=env)
    site = Path(root) / ".website-build/site"
    get_bytes = _http_get(base_url)
    receipt_bytes = live_bytes(get_bytes, ["/publication-receipt.json"])[
        "/publication-receipt.json"]
    if receipt_bytes is None:
        # Legacy deployment: no maintained guides, no receipt. Preserve that
        # state (do not stamp) and compare the real deployed guide routes.
        declaration_path = Path(trusted_declaration) if trusted_declaration else None
        slugs = tuple(guide.get("slug") for guide in (load_json(declaration_path).get("guides")
                      if declaration_path and declaration_path.is_file() else [])) \
            or BASELINE_GUIDE_SLUGS
        paths = [f"/{CHANNEL_ROUTES[channel].strip('/')}/{slug}/index.html"
                 for channel in CHANNELS for slug in slugs]
        observed = live_bytes(get_bytes, paths)
        local_index = site / "version-index.json"
        expected_index = get_bytes("/version-index.json")
        if not local_index.is_file() \
                or _page_bytes(local_index.read_bytes()) != _page_bytes(expected_index):
            raise ValueError("Reconstructed legacy index bytes do not match live")
        for path, live_value in observed.items():
            local = site / path.lstrip("/")
            if live_value is None or not local.is_file() \
                    or _page_bytes(local.read_bytes()) != _page_bytes(live_value):
                raise ValueError(f"Reconstructed legacy page {path} does not match live bytes")
        mode = "legacy-reconstructed"
    else:
        # The workflow stamps receipts separately, so reconstruct it from the
        # exact declaration and blobs at the deployment SHA, PLUS the channel
        # presentation fingerprint persisted by the trusted build. A legacy
        # artifact without presentation.json stays compatible (no field).
        presentation_path = site / CHANNEL_ROUTES["stable"].strip("/") / "presentation.json"
        presentation = load_json(presentation_path) if presentation_path.is_file() else None
        candidate = candidate_guide_fingerprint(
            Path(root) / "docs/maintained-guides.json", deployment_sha, root,
            presentation=presentation)
        stamp_publication_receipt(site, candidate, live, deployment_sha)
        ok, problems = verify_rendered_guide_links(_local_get(site), live, root)
        if not ok:
            raise ValueError("Reconstructed guide links are invalid: " + "; ".join(problems))
        ok, problems = verify_prior_bytes(site, base_url)
        if not ok:
            raise ValueError("Reconstructed bytes do not match live: " + "; ".join(problems))
        mode = "reconstructed"
    target = Path(destination)
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(site, target)
    return {"verified": True, "mode": mode,
            "source_revision": deployment_sha,
            "hash": "sha256:" + sha256_bytes(_page_bytes(
                (target / "version-index.json").read_bytes()))}


def emit(values):
    printable = {key: str(value).lower() if isinstance(value, bool) else value
                 for key, value in values.items()}
    print(json.dumps(printable, indent=2))
    write_output(printable)
    return printable


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    contract = commands.add_parser("validate-channel-contract")
    contract.add_argument("--path", required=True)
    contract.add_argument("--stable-source", help="already-qualified live version index")
    state = commands.add_parser("resolve-live-docs-state")
    state.add_argument("--path", required=True)
    state.add_argument("--verify-stable", action="store_true")
    state.add_argument("--require-nightly-parity", action="store_true")
    state.add_argument("--repository",
                       default=os.environ.get("GITHUB_REPOSITORY", "athNdev/carbonpanel"))
    receipt = commands.add_parser("verify-docs-receipt")
    receipt.add_argument("--built", required=True)
    receipt.add_argument("--live", required=True)
    guides = commands.add_parser("verify-maintained-guides")
    guides.add_argument("--site", default="")
    guides.add_argument("--base-url", default="")
    guides.add_argument("--revision", required=True)
    fingerprint = commands.add_parser("guide-fingerprint")
    fingerprint.add_argument("--declaration", required=True)
    fingerprint.add_argument("--revision", required=True)
    fingerprint.add_argument("--root", default=str(ROOT))
    fingerprint.add_argument("--presentation", default="",
                             help="path to the generated presentation fingerprint JSON")
    evidence = commands.add_parser("guide-evidence")
    evidence.add_argument("--base-url", default="https://carbonpanel.dev")
    decision = commands.add_parser("guide-change-decision")
    decision.add_argument("--candidate", required=True)
    decision.add_argument("--evidence", required=True)
    decision.add_argument("--require-publish", action="store_true")
    recheck = commands.add_parser("recheck-main-tip")
    recheck.add_argument("--repository",
                         default=os.environ.get("GITHUB_REPOSITORY", "athNdev/carbonpanel"))
    recheck.add_argument("--expected", required=True)
    stamp = commands.add_parser("stamp-publication-receipt")
    stamp.add_argument("--site", required=True)
    stamp.add_argument("--candidate", required=True)
    stamp.add_argument("--live", required=True)
    stamp.add_argument("--revision", required=True)
    receipt_guard = commands.add_parser("verify-publication-receipt")
    receipt_guard.add_argument("--site", default="")
    receipt_guard.add_argument("--base-url", default="")
    receipt_guard.add_argument("--candidate", required=True)
    receipt_guard.add_argument("--live", required=True)
    receipt_guard.add_argument("--revision", required=True)
    rendered = commands.add_parser("verify-rendered-guides")
    rendered.add_argument("--site", default="")
    rendered.add_argument("--base-url", default="")
    rendered.add_argument("--live", required=True)
    rendered.add_argument("--root", required=True)
    prior_bytes = commands.add_parser("verify-prior-bytes")
    prior_bytes.add_argument("--site", required=True)
    prior_bytes.add_argument("--base-url", default="https://carbonpanel.dev")
    recover = commands.add_parser("recover-prior-pages")
    recover.add_argument("--site", required=True)
    recover.add_argument("--base-url", default="https://carbonpanel.dev")
    recover.add_argument("--root", required=True)
    recover.add_argument("--deployment-sha", required=True)
    recover.add_argument("--trusted-declaration", default="")
    recover.add_argument("--stable-source", help="already-qualified live version index")
    docs = commands.add_parser("verify-channel-docs")
    docs.add_argument("--site", default="")
    docs.add_argument("--base-url", default="")
    docs.add_argument("--channel", required=True)
    docs.add_argument("--commit", required=True)
    docs.add_argument("--digest", required=True)
    docs.add_argument("--page", action="append", default=[])
    docs.add_argument("--contains", action="append", default=[])
    docs.add_argument("--excludes", action="append", default=[])
    docs.add_argument("--stable-source", default="")
    live_receipt = commands.add_parser("live-publication-receipt")
    live_receipt.add_argument("--base-url", default="https://carbonpanel.dev")
    baseline = commands.add_parser("pages-baseline")
    baseline.add_argument("--repository",
                          default=os.environ.get("GITHUB_REPOSITORY", "athNdev/carbonpanel"))
    prior = commands.add_parser("fetch-prior-pages")
    prior.add_argument("--site", required=True)
    prior.add_argument("--live-url", default=LIVE_INDEX_URL)
    prior.add_argument("--repository", required=True)
    prior.add_argument("--artifact-id", default="")
    prior.add_argument("--run-id", default="")
    return parser


def main():
    args = build_parser().parse_args()
    if args.command == "validate-channel-contract":
        stable_source = load_json(args.stable_source) if args.stable_source else None
        ok, problems = validate_channel_contract(load_json(args.path), stable_source)
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Channel contract validation failed: " + "; ".join(problems))
    elif args.command == "resolve-live-docs-state":
        live = load_json(args.path)
        stable = qualify_public_stable(live, args.repository) if args.verify_stable else None
        resolved, reason = resolve_live_docs_state(live, live if args.verify_stable else None)
        if not resolved:
            emit({"verified": False, "reason": reason})
            raise ValueError("Live docs state resolution failed: " + reason)
        if args.require_nightly_parity:
            ok, reason = release.channel_oci_parity(
                release.channel_observe("nightly"), resolved["digest"], resolved["revision"])
            if not ok:
                raise ValueError("Public Nightly is not qualified for a site-only deployment: " + reason)
        stable_inputs = stable_render_inputs(stable) if stable else {}
        emit({**resolved, **stable_inputs, "verified": True, "reason": ""})
    elif args.command == "verify-docs-receipt":
        ok, problems = verify_docs_receipt(load_json(args.built), load_json(args.live))
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Docs-only receipt changed: " + "; ".join(problems))
    elif args.command == "verify-maintained-guides":
        if bool(args.site) == bool(args.base_url):
            raise ValueError("Supply exactly one of --site or --base-url")
        if args.site:
            index = load_json(Path(args.site) / "version-index.json")
            ok, problems = verify_maintained_guides(
                args.site, index.get("channels") or {}, args.revision)
        else:
            ok, problems = verify_public_maintained_guides(_public_evidence(args.base_url),
                                                           args.revision)
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Maintained guide provenance failed: " + "; ".join(problems))
    elif args.command == "guide-fingerprint":
        presentation = load_json(args.presentation) if args.presentation else None
        emit({"verified": True,
              **candidate_guide_fingerprint(args.declaration, args.revision, args.root,
                                            presentation=presentation)})
    elif args.command == "guide-evidence":
        evidence = _public_evidence(args.base_url)
        emit({"verified": True, **evidence})
    elif args.command == "guide-change-decision":
        decision = guide_change_decision(load_json(args.candidate), load_json(args.evidence))
        emit({"verified": decision["publish"], **decision})
        if args.require_publish and not decision["publish"]:
            raise ValueError("Live guide receipt became current before write; "
                             "this run is now a safe no-op")
    elif args.command == "recheck-main-tip":
        result = recheck_main_tip(args.repository, args.expected)
        emit({"verified": result["verified"], **result})
        if not result["verified"]:
            raise ValueError("Trusted main no longer points at the tested revision")
    elif args.command == "stamp-publication-receipt":
        receipt = stamp_publication_receipt(
            args.site, load_json(args.candidate), load_json(args.live), args.revision)
        emit({"verified": True, **receipt})
    elif args.command == "verify-publication-receipt":
        if bool(args.site) == bool(args.base_url):
            raise ValueError("Supply exactly one of --site or --base-url")
        receipt_file = (Path(args.site) / "publication-receipt.json" if args.site
                        else None)
        if receipt_file is not None:
            receipt = json.loads(receipt_file.read_text(encoding="utf-8"))
        else:
            receipt = json.loads(_http_get(args.base_url)("/publication-receipt.json").decode("utf-8"))
        get_bytes = _local_get(args.site) if args.site else _http_get(args.base_url)
        ok, problems = verify_publication_receipt(
            receipt, load_json(args.candidate), load_json(args.live), args.revision,
            get_bytes)
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Publication receipt verification failed: " + "; ".join(problems))
    elif args.command == "verify-rendered-guides":
        if bool(args.site) == bool(args.base_url):
            raise ValueError("Supply exactly one of --site or --base-url")
        index = load_json(args.live)
        get_bytes = _local_get(args.site) if args.site else _http_get(args.base_url)
        ok, problems = verify_rendered_guide_links(get_bytes, index, args.root)
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Rendered guide verification failed: " + "; ".join(problems))
    elif args.command == "verify-prior-bytes":
        ok, problems = verify_prior_bytes(args.site, args.base_url)
        emit({"verified": ok, "problems": problems,
              "hash": "sha256:" + sha256_bytes(
                  (Path(args.site) / "version-index.json").read_bytes())})
        if not ok:
            raise ValueError("Prior bytes differ from live: " + "; ".join(problems))
    elif args.command == "recover-prior-pages":
        stable_source = load_json(args.stable_source) if args.stable_source else None
        result = recover_prior_pages(args.site, args.base_url, args.deployment_sha,
                                     args.root, args.trusted_declaration, stable_source)
        emit(result)
    elif args.command == "verify-channel-docs":
        if bool(args.site) == bool(args.base_url):
            raise ValueError("Supply exactly one of --site or --base-url")

        def get_bytes(path):
            if args.site:
                target = Path(args.site) / path.lstrip("/")
                if not target.is_file():
                    raise ValueError(f"Missing built file: {path}")
                return target.read_bytes()
            request = urllib.request.Request(
                args.base_url.rstrip("/") + path,
                headers={"User-Agent": "carbonpanel-docs-gate"})
            with urllib.request.urlopen(request, timeout=60) as response:
                return response.read()

        index = json.loads(get_bytes("/version-index.json").decode("utf-8"))
        route = index["channels"][args.channel]["route"].strip("/")
        source = json.loads(get_bytes(f"/{route}/source.json").decode("utf-8"))
        if source.get("revision") != args.commit:
            raise ValueError(f"{args.channel} source.json revision does not match the live commit")
        page_texts = [get_bytes(f"/{route}/{page.strip('/')}/index.html").decode("utf-8", "replace")
                      for page in args.page]
        stable_source = load_json(args.stable_source) if args.stable_source else None
        ok, problems = verify_channel_docs(
            index, args.channel, args.commit, args.digest, page_texts,
            tuple(args.contains), tuple(args.excludes), stable_source)
        emit({"verified": ok, "problems": problems})
        if not ok:
            raise ValueError("Channel docs verification failed: " + "; ".join(problems))
    elif args.command == "live-publication-receipt":
        receipt = json.loads(_http_get(args.base_url)("/publication-receipt.json").decode("utf-8"))
        emit({"verified": True, **receipt})
    elif args.command == "pages-baseline":
        baseline, reason = pages_baseline(args.repository)
        emit({**(baseline or {}), "verified": bool(baseline), "reason": reason})
        if not baseline:
            raise ValueError("Pages baseline resolution failed: " + reason)
    elif args.command == "fetch-prior-pages":
        api, _ = github_api(args.repository)
        live = urllib.request.urlopen(args.live_url, timeout=60).read()
        if args.run_id and not args.artifact_id:
            listing = json.loads(api(
                f"https://api.github.com/repos/{args.repository}/actions/runs"
                f"/{args.run_id}/artifacts?per_page=100"))
        else:
            listing = json.loads(api(
                f"https://api.github.com/repos/{args.repository}/actions/artifacts"
                f"?name={PAGES_ARTIFACT_NAME}&per_page=40"))
        result = fetch_prior_pages(
            args.site, listing.get("artifacts") or [],
            lambda artifact: api(artifact["archive_download_url"]), live,
            artifact_id=args.artifact_id or None, run_id=args.run_id or None,
            live_url=args.live_url)
        emit(result)


if __name__ == "__main__":
    main()
