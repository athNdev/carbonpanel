#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Atomic prepare/bump for the version contract.

One command derives or validates EVERY affected surface on the SAME commit.
It never creates a Git tag, publishes a release, writes a registry or marks a
candidate published. Registry/Pages/GitHub have no atomic cross-provider
transaction; see docs/website-publishing.md for the deterministic retry order.
"""
import argparse
import copy
import json
from pathlib import Path
import re
import sys

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))
from scripts import version_contract as vc
from scripts import dockerhub_description as hub_description

ROOT = _ROOT
BANNER = ROOT / "docs/carbon-panel/src/components/VersionBanner.astro"
BUILDER = ROOT / "scripts/build_website.py"
PROTO = ROOT / "proto/carbonpanel/v1/auth.proto"
CHANGELOG = ROOT / "docs/carbon-panel/src/content/docs/changelog.md"
# The README and the derived Docker Hub body share one release-channel section.
# The gate checks its aliases and docs links, never a hardcoded Stable version:
# a promotion changes the verified identity without invalidating this copy.
CHANNEL_SECTION = "Release channels"
CHANNEL_ITEMS = {
    "Stable": {"alias": "`:latest`", "docs": "https://carbonpanel.dev/docs/"},
    "Nightly": {"alias": "`:nightly`", "docs": "https://carbonpanel.dev/docs/nightly/"},
}
MARKDOWN_LINK_RE = re.compile(r"\]\(([^)]+)\)")


def channel_item(section, channel):
    """Return one channel bullet, including its wrapped continuation lines."""
    match = re.search(r"^-\s+\*\*" + channel + r"\*\*.*?(?=^-\s+\*\*|\Z)",
                      section, re.MULTILINE | re.DOTALL)
    return match.group(0) if match else ""


def link_destinations(text):
    """Exact inline Markdown link destinations, without optional titles."""
    return {target.split()[0] for target in MARKDOWN_LINK_RE.findall(text)}


def verify_readme_channels(root):
    """Check each shared channel bullet without banning historical versions."""
    errors = []
    readme = (root / "README.md").read_text(encoding="utf-8")
    section = hub_description.split_sections(readme).get(CHANNEL_SECTION, "")
    if not section.strip():
        return ["README must contain a '## {}' section describing Stable and Nightly".format(
            CHANNEL_SECTION)]
    for channel, requirement in CHANNEL_ITEMS.items():
        item = channel_item(section, channel)
        if not item:
            errors.append("README {} must describe the {} channel".format(
                CHANNEL_SECTION, channel))
            continue
        if requirement["alias"] not in item:
            errors.append("README {} item must use the exact inline alias {}".format(
                channel, requirement["alias"]))
        if requirement["docs"] not in link_destinations(item):
            errors.append("README {} item must link the {} documentation at {}".format(
                channel, channel, requirement["docs"]))
    config_path = root / "docs" / "dockerhub-description.json"
    if not config_path.is_file():
        errors.append("docs/dockerhub-description.json is missing")
    else:
        config = json.loads(config_path.read_text(encoding="utf-8"))
        if CHANNEL_SECTION not in config.get("sections", []):
            errors.append("Docker Hub generator config must include the shared '{}' section".format(
                CHANNEL_SECTION))
    return errors


def verify_surfaces(root, contract, index):
    """Return every disagreement; an empty list means all surfaces agree."""
    errors = []
    generated = vc.render_index(index)
    if vc.INDEX_PATH.is_file() and vc.INDEX_PATH.read_text(encoding="utf-8") != generated:
        errors.append("docs/version-index.json is stale; run 'version_bump.py prepare'")
    proto = PROTO.read_text(encoding="utf-8")
    match = re.search(r'^\s*version:\s*"([^"]+)"', proto, re.MULTILINE)
    openapi = contract["artifacts"]["openapi"]
    if not match or match.group(1) != openapi["schema_family"]:
        errors.append(f"proto info.version must equal artifacts.openapi.schema_family ({openapi['schema_family']})")
    if openapi.get("product_version_stamped") is not True:
        errors.append("shipped OpenAPI copies must stamp the exact product version, never the 2.x family placeholder")
    if contract["artifacts"]["protocol"]["version"] != "v1":
        errors.append("the independent protocol version must stay v1")
    if set(contract.get("channels", {})) != set(vc.CHANNEL_NAMES):
        errors.append(f"contract must publish exactly {', '.join(vc.CHANNEL_NAMES)}")
    if contract["channels"]["nightly"].get("promotion", {}).get("promoted") is not False:
        errors.append("tracked nightly default must never claim a promotion")
    banner = BANNER.read_text(encoding="utf-8")
    if "../generated/version-index.json" not in banner or "data-version-switcher" not in banner:
        errors.append("VersionBanner must render the common switcher from the generated index")
    if "/commit/" not in banner or 'onchange="location.assign(this.value)"' not in banner:
        errors.append("VersionBanner must pin full commits and navigate on select change")
    builder = BUILDER.read_text(encoding="utf-8")
    if "render_versions_page" not in builder or "export_snapshot(root, version[\"commit\"]" not in builder:
        errors.append("builder must generate the identity page and snapshot each exact peeled commit")
    if "openapi_version.stamp_text" not in builder:
        errors.append("builder must stamp website OpenAPI copies with the exact product version")
    release_workflow = (root / ".github/workflows/release.yml").read_text(encoding="utf-8")
    if "openapi_version.py stamp" not in release_workflow or \
            "web/carbon-panel/build/schemav1.yaml" not in release_workflow:
        errors.append("release workflow must stamp and stage the served schema")
    release_tool = (root / "scripts/release.py").read_text(encoding="utf-8")
    if "verify_text(" not in release_tool or "frontend / \"schemav1.yaml\"" not in release_tool:
        errors.append("release packaging must verify and embed the stamped served schema")
    changelog = CHANGELOG.read_text(encoding="utf-8")
    if "3.0.0-beta.4" in changelog and "name collision" not in changelog:
        errors.append("docs changelog must mark v3.0.0-beta.4 as an excluded name collision")
    errors.extend(verify_readme_channels(root))
    for version in index["versions"]:
        if version["tag"] in vc.EXCLUDED:
            errors.append(f"contract documents excluded tag {version['tag']}")
        if version["display"] in vc.RETIRED_DISPLAYS:
            errors.append(f"contract publishes retired snapshot {version['display']}")
    return errors


def prepare(root, check_index, site):
    contract, catalog = vc.load(root)
    vc.validate(contract, catalog, root)
    index = vc.version_index(contract, catalog, root)
    errors = verify_surfaces(root, contract, index)
    if site:
        from scripts.check_website import check
        check(Path(site))
    if errors:
        raise ValueError("version surface drift:\n- " + "\n- ".join(sorted(set(errors))))
    if check_index:
        if not vc.INDEX_PATH.is_file() or vc.INDEX_PATH.read_text(encoding="utf-8") != vc.render_index(index):
            raise ValueError("docs/version-index.json is stale; run 'version_bump.py prepare'")
        print("prepare --check: contract, catalog, Git, index and rendered surfaces agree.")
        return
    vc.write_index(index)
    print(f"prepare: regenerated {vc.INDEX_PATH.relative_to(root)} from {len(index['versions'])} versions.")


def bump(tag, root, apply):
    """Verify the stable channel against its tag/catalog; never publish or retag.

    Schema 3 has exactly two channels, so a legacy tag can never add a third
    published snapshot. Nightly has no tag and is changed only by the qualified
    promotion gate, not by this helper.
    """
    contract, catalog = vc.load(root)
    vc.validate(contract, catalog, root)
    stable_tag = contract["channels"]["stable"]["tag"]
    if tag != stable_tag:
        raise ValueError(f"{tag}: only the stable channel tag {stable_tag} can be bumped here")
    entry = next((item for item in catalog["tags"] if item["name"] == tag), None)
    if entry is None:
        raise ValueError(f"{tag}: not in the classified catalog")
    if tag in vc.EXCLUDED:
        raise ValueError(f"{tag}: excluded ({vc.EXCLUDED[tag]})")
    channel = vc.CHANNELS.get(entry["classification"])
    if channel is None or channel["channel"] != "stable":
        raise ValueError(f"{tag}: not classified as the published stable channel")
    commit = vc.git_peel(root, tag)
    if commit != entry["peeled_commit"]:
        raise ValueError(f"{tag}: Git peel {commit} != catalog {entry['peeled_commit']}")
    stable = contract["channels"]["stable"]
    candidate = {
        **stable,
        "version": tag.removeprefix("v"),
        "commit": commit,
        "image": {**stable["image"], "revision": commit},
    }
    updated = copy.deepcopy(contract)
    if updated["channels"]["stable"] != candidate:
        if not apply:
            raise ValueError(f"{tag}: stable channel disagrees with the derived candidate")
        updated["channels"]["stable"] = candidate
    vc.validate(updated, catalog, root)
    index = vc.version_index(updated, catalog, root)
    errors = verify_surfaces(root, updated, index)
    if errors:
        raise ValueError("bump blocked by surface drift:\n- " + "\n- ".join(sorted(set(errors))))
    if apply:
        vc.CONTRACT_PATH.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")
        vc.write_index(index)
        print(f"bump: staged candidate {tag} @ {commit} across contract and index (no publication).")
    else:
        print(f"bump --check: {tag} @ {commit} derives consistently across every surface.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prep = commands.add_parser("prepare")
    prep.add_argument("--check", action="store_true", help="fail if the checked-in index is stale")
    prep.add_argument("--site", help="validate an already built packaged site")
    bumpp = commands.add_parser("bump")
    bumpp.add_argument("tag")
    bumpp.add_argument("--check", action="store_true", help="validate without writing (the default)")
    bumpp.add_argument("--apply", action="store_true", help="write contract and index (never tags/publishes)")
    args = parser.parse_args()
    if args.command == "prepare":
        prepare(ROOT, args.check, args.site)
    elif args.command == "bump":
        bump(args.tag, ROOT, args.apply)
    else:
        parser.error("unknown command")


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
