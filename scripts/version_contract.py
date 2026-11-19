#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Single version contract shared by the website build, docs UI and drift checks.

``docs/website-source.json`` is the authoritative *build* contract. Schema 3
publishes exactly two channels: ``stable`` (the verified release) and ``nightly``
(the revision whose manifest digest is identical in Docker Hub and GHCR). The
classified catalog (``docs/release-history.json``) stays intact; it is only used
to re-derive and verify the stable entry. Channel identity, registry refs, image
digest and revision are checked fail-closed, so an extra channel, legacy
snapshot, stale marker or mismatched registry pair aborts before packaging.
"""
import copy
import json
import os
import re
from pathlib import Path
import subprocess
import sys

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))

ROOT = _ROOT
CONTRACT_PATH = ROOT / "docs/website-source.json"
CATALOG_PATH = ROOT / "docs/release-history.json"
INDEX_PATH = ROOT / "docs/version-index.json"
SOURCE_PATHS = ("docs/carbon-panel/src/content/docs", "docs/carbon-panel/public",
                 "docker-compose.yml", "config.example.yaml", "oidc", "proto",
                 "buf.yaml", "buf.lock")

SHA = re.compile(r"[0-9a-f]{40}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
SCHEMA_VERSION = 3
CHANNEL_NAMES = ("stable", "nightly")
CHANNEL_DISPLAY = {"stable": "Stable", "nightly": "Nightly"}
CHANNEL_ROUTES = {"stable": "/docs/", "nightly": "/docs/nightly/"}
REGISTRY_REFS = {
    "stable": {
        "docker_hub": "atharvnaphade/carbonpanel:latest",
        "ghcr": "ghcr.io/athndev/carbonpanel:latest",
        "tag": "latest",
    },
    "nightly": {
        "docker_hub": "atharvnaphade/carbonpanel:nightly",
        "ghcr": "ghcr.io/athndev/carbonpanel:nightly",
        "tag": "nightly",
    },
}

# Bounded backwards-compatible bootstrap only. This verified Docker Hub baseline
# is NOT a mandatory first nightly revision: it is acceptable solely when the
# same digest/revision is mirrored in GHCR and recorded with bootstrap=true.
BOOTSTRAP_BASELINE = {
    "commit": "d8597d4e953522d2eb2fa6d4764965ae8e58686f",
    "digest": "sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce5e96d00fa1c64239c965a9302d",
}

# Kept for the legacy tag-driven bump helper; it never adds a published channel.
CHANNELS = {
    "carbonpanel-published-stable": {
        "channel": "stable", "availability": "published-release",
        "cta": True, "download_kind": "github-release",
    },
    "carbonpanel-legacy-preview": {
        "channel": "preview", "availability": "ghcr-only-legacy-preview",
        "cta": True, "download_kind": "ghcr",
    },
    "carbonpanel-source-only": {
        "channel": "historical", "availability": "source-only",
        "cta": False, "download_kind": "source",
    },
}

# Tags that are explicitly excluded from the documented version family.
EXCLUDED = {
    "v1.0.38": "local and authoritative remote tag ancestry differs",
    "v3.0.0-beta.4": "public label is misbound to an upstream name collision",
}

# Labels and doc routes retired from the published docs selector. Source tags
# and historical catalog records are deliberately untouched.
RETIRED_DISPLAYS = ("1.0.39", "3.0.0-alpha.1", "v1.0.39", "v3.0.0-alpha.1")
RETIRED_ROUTES = ("/docs/versions/1.0.39/", "/docs/versions/3.0.0-alpha.1/")

# Promotion inputs are exact external data from the qualified gate. They are
# never written to a tracked main file; only the generated artifact carries them.
ENV_REVISION = "CARBONPANEL_PROMOTED_NIGHTLY_REVISION"
ENV_DIGEST = "CARBONPANEL_PROMOTED_NIGHTLY_DIGEST"
ENV_STATE = "CARBONPANEL_PROMOTION_STATE"
ENV_RECEIPT = "CARBONPANEL_PROMOTION_RECEIPT"
PROMOTION_STATES = ("candidate", "final")


def promotion_from_env(environ=None):
    """Return the exact external promotion input, or None for an ordinary build."""
    environ = os.environ if environ is None else environ
    revision = environ.get(ENV_REVISION)
    digest = environ.get(ENV_DIGEST)
    if not revision and not digest:
        return None
    if not revision or not digest:
        raise ValueError(f"{ENV_REVISION} and {ENV_DIGEST} must be supplied together")
    return {
        "revision": revision,
        "digest": digest,
        "state": environ.get(ENV_STATE, "candidate"),
        "receipt": environ.get(ENV_RECEIPT, ""),
    }


def apply_promotion(contract, promotion):
    """Return an in-memory contract with the exact promoted nightly marker."""
    if promotion is None:
        return contract
    revision = promotion.get("revision")
    digest = promotion.get("digest")
    if not isinstance(revision, str) or not SHA.fullmatch(revision):
        raise ValueError("promoted nightly revision must be a full 40-hex commit")
    if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
        raise ValueError("promoted nightly digest must be a sha256 digest")
    state = promotion.get("state", "candidate")
    if state not in PROMOTION_STATES:
        raise ValueError(f"promotion state must be one of {', '.join(PROMOTION_STATES)}")
    if not promotion.get("receipt"):
        raise ValueError("a promotion receipt id is required for an exact channel promotion")
    updated = copy.deepcopy(contract)
    nightly = updated["channels"]["nightly"]
    nightly["commit"] = revision
    nightly["image"]["revision"] = revision
    nightly["image"]["digest"] = digest
    nightly["image"].pop("bootstrap", None)
    nightly["image"].pop("bootstrap_note", None)
    nightly["promotion"] = {
        "state": state,
        "promoted": True,
        "receipt": promotion["receipt"],
    }
    return updated


def _problems(problems):
    if problems:
        raise ValueError("version contract drift:\n- " + "\n- ".join(sorted(set(problems))))


def git_peel(root, tag):
    """Resolve a tag to its exact commit from Git (external derivation)."""
    return subprocess.check_output(
        ["git", "rev-parse", f"{tag}^{{commit}}"], cwd=root, text=True).strip()


def doc_slugs(root, commit):
    """List the documentation slugs present at an exact commit."""
    names = subprocess.check_output(
        ["git", "ls-tree", "-r", "--name-only", commit,
         "--", *SOURCE_PATHS[:1]], cwd=root, text=True).splitlines()
    slugs = []
    for name in names:
        if not name.endswith((".md", ".mdx")):
            continue
        slug = name[len(SOURCE_PATHS[0]) + 1:]
        slug = re.sub(r"\.mdx?\Z", "", slug)
        if slug.endswith("/index"):
            slug = slug[: -len("/index")]
        if slug == "index":
            slug = ""
        slugs.append(slug)
    return sorted(set(slugs))


def load(root=ROOT):
    contract = json.loads((Path(root) / "docs/website-source.json").read_text(encoding="utf-8"))
    catalog = json.loads((Path(root) / "docs/release-history.json").read_text(encoding="utf-8"))
    return contract, catalog


def _download_url(repository, tag, kind):
    if kind == "github-release":
        return f"{repository}/releases/tag/{tag}"
    if kind == "ghcr":
        return f"{repository}/pkgs/container/carbonpanel"
    if kind == "source":
        return f"{repository}/tree/{tag}"
    raise ValueError(f"unsupported download kind: {kind}")


def validate(contract, catalog, root=ROOT, peel=None):
    """Fail closed on any channel/catalog/Git disagreement. Returns nothing."""
    problems = []
    peel = peel or (lambda tag: git_peel(root, tag))
    if contract.get("schema_version") != SCHEMA_VERSION:
        problems.append(f"contract schema_version must be {SCHEMA_VERSION}")
    repository = contract.get("repository")
    if not repository or not repository.startswith("https://"):
        problems.append("contract needs a public https repository URL")
    if contract.get("catalog") != "docs/release-history.json":
        problems.append("contract must reference the classified catalog")

    channels = contract.get("channels")
    if not isinstance(channels, dict) or set(channels) != set(CHANNEL_NAMES):
        _problems(problems + ["channels must contain exactly stable and nightly"])

    # Reuse the release lane's catalog verification instead of restating it.
    try:
        from scripts.release import verify_release_history
        ok, history_problems = verify_release_history(catalog)
        if not ok:
            problems.extend(f"catalog: {item}" for item in history_problems)
    except ImportError:
        pass

    tags = {item["name"]: item for item in catalog.get("tags", [])}
    canonical = catalog.get("canonical_stable", {})

    for name in CHANNEL_NAMES:
        channel = channels[name]
        refs = REGISTRY_REFS[name]
        label = f"channels.{name}"
        if not isinstance(channel, dict):
            problems.append(f"{label} must be an object")
            continue
        if channel.get("display") != CHANNEL_DISPLAY[name]:
            problems.append(f"{label}.display must be {CHANNEL_DISPLAY[name]!r}")
        if channel.get("route") != CHANNEL_ROUTES[name]:
            problems.append(f"{label}.route must be {CHANNEL_ROUTES[name]!r}")
        if channel.get("default") is not (name == "stable"):
            problems.append(f"{label}.default is incorrect for {name}")
        commit = channel.get("commit")
        if not isinstance(commit, str) or not SHA.fullmatch(commit):
            problems.append(f"{label}.commit must be a full 40-hex commit")
        serialized = json.dumps(channel)
        for retired in RETIRED_DISPLAYS:
            if retired in serialized:
                problems.append(f"{label} contains retired snapshot {retired!r}")
        for retired_route in RETIRED_ROUTES:
            if retired_route in serialized:
                problems.append(f"{label} contains retired doc route {retired_route!r}")

        image = channel.get("image")
        if not isinstance(image, dict):
            problems.append(f"{label}.image is required")
            continue
        for key in ("docker_hub", "ghcr"):
            if image.get(key) != refs[key]:
                problems.append(f"{label}.image.{key} must be {refs[key]!r}")
        if image.get("tag") != refs["tag"]:
            problems.append(f"{label}.image.tag must be {refs['tag']!r}")
        digest = image.get("digest")
        revision = image.get("revision")
        if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
            problems.append(f"{label}.image.digest must be a sha256 digest")
        if not isinstance(revision, str) or not SHA.fullmatch(revision):
            problems.append(f"{label}.image.revision must be a full 40-hex commit")
        elif revision != commit:
            problems.append(f"{label}.image.revision {revision} != commit {commit}")

    stable = channels["stable"]
    nightly = channels["nightly"]
    if stable.get("tag") != canonical.get("git_tag"):
        problems.append(
            f"stable tag {stable.get('tag')!r} is not catalog canonical {canonical.get('git_tag')!r}")
    if stable.get("version") != stable.get("tag", "").removeprefix("v"):
        problems.append("stable.version must be the tag without the v prefix")
    if stable["image"].get("stable_alias") != "stable":
        problems.append("stable.image.stable_alias must be 'stable'")
    if stable["image"].get("digest") != canonical.get("image_digest"):
        problems.append("stable digest != catalog canonical verified image digest")
    if nightly.get("tag") is not None:
        problems.append("nightly.tag must be null (the rolling channel has no release tag)")
    if nightly.get("version") is not None:
        problems.append("nightly.version must be absent (no version terminology)")

    stable_promotion = stable.get("promotion") or {}
    if stable_promotion.get("state") != "published" or stable_promotion.get("promoted") is not True:
        problems.append("stable promotion must be published and promoted")

    nightly_promotion = nightly.get("promotion") or {}
    promoted = nightly_promotion.get("promoted")
    state = nightly_promotion.get("state")
    if promoted is True:
        if state not in PROMOTION_STATES:
            problems.append("promoted nightly state must be candidate or final")
        if not nightly_promotion.get("receipt"):
            problems.append("promoted nightly requires a promotion receipt id")
        if nightly.get("image", {}).get("bootstrap") is True:
            problems.append("promoted nightly must not keep the unqualified bootstrap marker")
    elif promoted is False:
        if state != "baseline":
            problems.append("unpromoted nightly state must be baseline")
    else:
        problems.append("nightly.promotion.promoted must be a boolean")

    nightly_image = nightly.get("image", {})
    if promoted is True:
        if nightly_image.get("digest") == BOOTSTRAP_BASELINE["digest"] and \
                nightly.get("commit") != BOOTSTRAP_BASELINE["commit"]:
            problems.append("stale nightly baseline digest must not be relabeled to a new revision")
    elif nightly_image.get("bootstrap") is True:
        # Bounded bootstrap: only the verified baseline pair is acceptable, and
        # it must be recorded as bootstrap-only pending GHCR qualification.
        if nightly.get("commit") != BOOTSTRAP_BASELINE["commit"]:
            problems.append("bootstrap nightly commit must be the verified baseline revision")
        if nightly_image.get("digest") != BOOTSTRAP_BASELINE["digest"]:
            problems.append("bootstrap nightly digest must be the verified baseline digest")
        if not nightly_image.get("bootstrap_note"):
            problems.append("bootstrap nightly needs a recorded GHCR qualification note")
    elif nightly_image.get("digest") == BOOTSTRAP_BASELINE["digest"] and \
            nightly.get("commit") != BOOTSTRAP_BASELINE["commit"]:
        problems.append("stale nightly baseline digest must not be relabeled to a new revision")

    entry = tags.get(stable.get("tag"))
    if entry is None:
        problems.append(f"stable tag {stable.get('tag')!r} is not in the classified catalog")
    else:
        if entry.get("classification") != "carbonpanel-published-stable":
            problems.append("stable tag must be classified as a published stable release")
        if entry.get("peeled_commit") != stable.get("commit"):
            problems.append("stable commit != catalog peeled commit")
        try:
            actual = peel(stable["tag"])
        except Exception as error:  # noqa: BLE001 - report as a drift failure
            problems.append(f"stable tag cannot be resolved by Git: {error}")
        else:
            if actual != stable.get("commit"):
                problems.append(f"stable Git tag peels to {actual} != {stable.get('commit')}")

    artifacts = contract.get("artifacts", {})
    for key in ("protocol", "openapi", "module", "mcp", "node_runtime", "cli"):
        if key not in artifacts:
            problems.append(f"artifacts.{key} is missing")
    bounds = artifacts.get("compatibility", {})
    if bounds.get("rollback_version") != canonical.get("version", "").lstrip("v"):
        problems.append("compatibility.rollback_version must follow the canonical stable")
    if bounds.get("next_stable_boundary") != catalog.get("next_stable", {}).get("boundary"):
        problems.append("compatibility.next_stable_boundary must follow the catalog")

    _problems(problems)


def version_entries(contract, catalog=None, root=ROOT):
    """Derive the two published versions from the channel contract."""
    channels = contract["channels"]
    repository = contract["repository"]
    stable = channels["stable"]
    nightly = channels["nightly"]
    catalog = catalog or json.loads(CATALOG_PATH.read_text(encoding="utf-8"))
    classification = next(
        (item.get("classification") for item in catalog.get("tags", [])
         if item.get("name") == stable["tag"]), "carbonpanel-published-stable")

    entries = [
        {
            "tag": stable["tag"],
            "version": stable["version"],
            "display": stable["version"],
            "channel_display": stable["display"],
            "classification": classification,
            "channel": "stable",
            "availability": "published-release",
            "commit": stable["commit"],
            "source_commit": stable["commit"],
            "cta": True,
            "download_kind": "github-release",
            "download_url": _download_url(repository, stable["tag"], "github-release"),
            "default": True,
            "image": stable["image"],
        },
        {
            "tag": None,
            "version": None,
            "display": nightly["display"],
            "channel_display": nightly["display"],
            "classification": "carbonpanel-rolling-nightly",
            "channel": "nightly",
            "availability": "rolling-nightly-image",
            "commit": nightly["commit"],
            "source_commit": nightly["commit"],
            "cta": False,
            "download_kind": "rolling-image",
            "download_url": None,
            "default": False,
            "image": nightly["image"],
        },
    ]
    for entry, channel in zip(entries, (stable, nightly)):
        route = channel["route"]
        entry["immutable_route"] = (
            f"/docs/versions/{entry['version']}/" if entry["version"] is not None else route)
        entry["route"] = route
        entry["home_route"] = route
        entry["schema_version_label"] = (
            entry["version"] if entry["version"] else
            f"0.0.0-nightly.{entry['commit'][:12]}")
        entry["pages"] = doc_slugs(root, entry["commit"])
    entries.sort(key=lambda item: (not item.get("default"), item["display"]))
    return entries


def switcher_href(version, slug):
    """Canonical option href: carry a real page, never a version prefix or //."""
    home = version["route"].rstrip("/") + "/"
    if slug and slug in version.get("pages", []):
        return home.rstrip("/") + "/" + slug + "/"
    return home


def version_index(contract, catalog=None, root=ROOT):
    """Build the one per-channel index consumed by every published surface."""
    stable = contract["channels"]["stable"]
    return {
        "schema_version": SCHEMA_VERSION,
        "repository": contract["repository"],
        "channels": {name: dict(contract["channels"][name]) for name in CHANNEL_NAMES},
        "stable": {
            "tag": stable["tag"],
            "version": stable["version"],
            "display": stable["display"],
            "commit": stable["commit"],
            "image_tag": stable["image"]["tag"],
            "digest": stable["image"]["digest"],
        },
        "promotion": {
            "state": contract["channels"]["nightly"]["promotion"]["state"],
            "promoted": contract["channels"]["nightly"]["promotion"]["promoted"],
        },
        "development": contract["development"],
        "artifacts": contract["artifacts"],
        "versions": version_entries(contract, catalog, root),
    }


def candidate_route(revision):
    """Non-active staging path for a candidate probe; never a canonical route."""
    if not isinstance(revision, str) or not SHA.fullmatch(revision):
        raise ValueError("candidate revision must be a full 40-hex commit")
    return f"/docs/candidate/{revision}/"


def candidate_index(index, revision, receipt):
    """Artifact-only index for an uncommitted candidate channel promotion."""
    updated = copy.deepcopy(index)
    route = candidate_route(revision)
    nightly = updated["channels"]["nightly"]
    nightly["route"] = route
    nightly["promotion"] = {"state": "candidate", "promoted": True, "receipt": receipt}
    for version in updated["versions"]:
        if version["channel"] == "nightly":
            version["route"] = route
            version["immutable_route"] = route
            version["home_route"] = route
    updated["promotion"] = {"state": "candidate", "promoted": True}
    return updated


def channel_marker(index):
    """Public, verifiable per-channel marker for a candidate/final deployment."""
    channels = {}
    for name in CHANNEL_NAMES:
        channel = index["channels"][name]
        channels[name] = {
            "display": channel["display"],
            "route": channel["route"],
            "revision": channel["commit"],
            "digest": channel["image"]["digest"],
            "docker_hub": channel["image"]["docker_hub"],
            "ghcr": channel["image"]["ghcr"],
            "promoted": channel["promotion"]["promoted"],
            "state": channel["promotion"]["state"],
            "receipt": channel["promotion"].get("receipt"),
            "pinned_url": f"{index['repository'].rstrip('/')}/commit/{channel['commit']}",
        }
    return {
        "schema_version": 1,
        "repository": index["repository"],
        "promotion": dict(index["promotion"]),
        "channels": channels,
    }


def render_index(index):
    return json.dumps(index, indent=2, ensure_ascii=False) + "\n"


def write_index(index, path=INDEX_PATH):
    """Write the generated index with portable LF endings."""
    path.write_bytes(render_index(index).encode("utf-8"))


def main():
    contract, catalog = load()
    validate(contract, catalog)
    index = version_index(contract, catalog)
    write_index(index)
    print(f"Version contract validated; {len(index['versions'])} published channels.")


if __name__ == "__main__":
    main()
