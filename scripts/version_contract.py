#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Single version contract shared by the website build, docs UI and drift checks.

``docs/website-source.json`` is the authoritative *build* contract. Channel and
availability are **derived** from the classified catalog
(``docs/release-history.json``), never from a ``v`` prefix. Every version's
commit is checked against both the catalog peel and the real Git tag, so the
candidate commit is derived externally and a tag-ID disagreement fails closed.
"""
import json
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

# Classification -> published channel. This mapping is the only place that
# converts a catalog classification into a user-visible channel. A classification
# outside this map is unsupported and fails closed.
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
    """Fail closed on any contract/catalog/Git disagreement. Returns nothing."""
    problems = []
    peel = peel or (lambda tag: git_peel(root, tag))
    if contract.get("schema_version") != 2:
        problems.append("contract schema_version must be 2")
    repository = contract.get("repository")
    if not repository or not repository.startswith("https://"):
        problems.append("contract needs a public https repository URL")

    tags = {item["name"]: item for item in catalog.get("tags", [])}

    # Reuse the release lane's catalog verification instead of restating it.
    try:
        from scripts.release import verify_release_history
        ok, history_problems = verify_release_history(catalog)
        if not ok:
            problems.extend(f"catalog: {item}" for item in history_problems)
    except ImportError:
        pass

    stable_tag = contract.get("stable_tag")
    canonical = catalog.get("canonical_stable", {})
    if stable_tag != canonical.get("git_tag"):
        problems.append(f"stable_tag {stable_tag!r} is not catalog canonical {canonical.get('git_tag')!r}")
    stable = next((v for v in contract.get("versions", []) if v.get("default")), None)
    if stable is None:
        problems.append("contract has no default stable version")
    elif stable.get("tag") != stable_tag:
        problems.append("default version is not the canonical stable tag")

    seen = set()
    for version in contract.get("versions", []):
        tag = version.get("tag", "?")
        seen.add(tag)
        if tag in EXCLUDED:
            problems.append(f"excluded tag {tag} is documented: {EXCLUDED[tag]}")
        entry = tags.get(tag)
        if entry is None:
            problems.append(f"{tag}: not in the classified catalog")
            continue
        classification = version.get("classification")
        if classification != entry.get("classification"):
            problems.append(
                f"{tag}: classification {classification!r} != catalog {entry.get('classification')!r}")
        channel = CHANNELS.get(entry.get("classification"))
        if channel is None:
            problems.append(f"{tag}: unsupported catalog classification {entry.get('classification')!r}")
            continue
        if version.get("channel") != channel["channel"]:
            problems.append(f"{tag}: channel must be derived as {channel['channel']!r}")
        if version.get("availability") != channel["availability"]:
            problems.append(f"{tag}: availability must be derived as {channel['availability']!r}")
        if version.get("cta") is not channel["cta"]:
            problems.append(f"{tag}: download CTA must be {channel['cta']!r}")
        expected_commit = entry.get("peeled_commit")
        if not expected_commit or not SHA.fullmatch(expected_commit):
            problems.append(f"{tag}: catalog has no valid peeled commit")
            continue
        if version.get("commit") != expected_commit:
            problems.append(f"{tag}: commit {version.get('commit')!r} != catalog peel {expected_commit!r}")
        if version.get("source_commit") != expected_commit:
            problems.append(f"{tag}: source_commit must equal the peeled commit")
        try:
            actual = peel(tag)
        except Exception as error:  # noqa: BLE001 - report as a drift failure
            problems.append(f"{tag}: cannot resolve Git tag: {error}")
            continue
        if actual != expected_commit:
            problems.append(f"{tag}: Git tag peels to {actual} != catalog {expected_commit}")
        if version.get("display") != tag.removeprefix("v"):
            problems.append(f"{tag}: display must be the version without the v prefix")
        if version.get("download_kind") != channel["download_kind"]:
            problems.append(f"{tag}: download_kind must be {channel['download_kind']!r}")
        expected_url = _download_url(repository, tag, channel["download_kind"])
        if version.get("download_url") != expected_url:
            problems.append(f"{tag}: download_url must be {expected_url!r}")

    defaults = [v for v in contract.get("versions", []) if v.get("default")]
    if len(defaults) != 1:
        problems.append("exactly one version must be the default")
    if contract.get("catalog") != "docs/release-history.json":
        problems.append("contract must reference the classified catalog")

    artifacts = contract.get("artifacts", {})
    for key in ("protocol", "openapi", "module", "mcp", "node_runtime", "cli"):
        if key not in artifacts:
            problems.append(f"artifacts.{key} is missing")
    bounds = artifacts.get("compatibility", {})
    if bounds.get("rollback_version") != canonical.get("version", "").lstrip("v"):
        problems.append("compatibility.rollback_version must follow the canonical stable")
    if bounds.get("next_stable_boundary") != catalog.get("next_stable", {}).get("boundary"):
        problems.append("compatibility.next_stable_boundary must follow the catalog")

    if problems:
        raise ValueError("version contract drift:\n- " + "\n- ".join(sorted(set(problems))))


def version_index(contract, catalog=None, root=ROOT):
    """Build the one per-version index consumed by every published surface."""
    entries = []
    for version in contract.get("versions", []):
        tag = version["tag"]
        display = version["display"]
        entry = dict(version)
        entry["immutable_route"] = f"/docs/versions/{display}/"
        entry["route"] = "/docs/" if version.get("default") else entry["immutable_route"]
        entry["home_route"] = entry["route"]
        entry["pages"] = doc_slugs(root, version["commit"])
        entries.append(entry)
    entries.sort(key=lambda item: (not item.get("default"), item["display"]))
    development = contract["development"]
    return {
        "schema_version": 2,
        "repository": contract["repository"],
        "stable": {
            "tag": contract["stable_tag"],
            "display": contract["stable_tag"].removeprefix("v"),
            "commit": next(v["commit"] for v in entries if v.get("default")),
        },
        "development": development,
        "artifacts": contract["artifacts"],
        "versions": entries,
    }


def render_index(index):
    return json.dumps(index, indent=2, ensure_ascii=False) + "\n"


def main():
    contract, catalog = load()
    validate(contract, catalog)
    index = version_index(contract, catalog)
    INDEX_PATH.write_text(render_index(index), encoding="utf-8")
    print(f"Version contract validated; {len(index['versions'])} documented versions.")


if __name__ == "__main__":
    main()
