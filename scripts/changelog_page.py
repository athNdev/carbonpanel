#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Generate the channel-correct public changelog from the version contract.

The old pinned ``changelog.md`` was development-preview prose: a second H1, a
working-tree 1.0.40 note, and inherited tag names that are not Carbon Panel
releases. This module renders presentation from the SAME generated version
contract that drives the switcher, with the exact published GitHub release
notes as the checked-in source. No product pin, image digest or registry state
is read or changed here.
"""
import argparse
import hashlib
import json
from pathlib import Path
import sys

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))

ROOT = _ROOT
SOURCE_PATH = "docs/changelog-source.json"
NOTES_PREFIX = "docs/release-notes/"
GENERATED_PAGE = "src/content/docs/changelog.mdx"
PINNED_PAGE = "src/content/docs/changelog.md"
BANNER_SOURCE = "docs/carbon-panel/src/components/VersionBanner.astro"


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def load_source(root=ROOT):
    """Read and validate the narrow changelog presentation declaration."""
    root = Path(root)
    path = root / SOURCE_PATH
    try:
        declaration = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as error:
        raise ValueError(f"missing changelog source declaration: {path}") from error
    if declaration.get("schema_version") != 1:
        raise ValueError("changelog source must be schema_version 1")
    if declaration.get("repository") != "athNdev/carbonpanel":
        raise ValueError("changelog source repository is not the first-party panel")
    notes = declaration.get("release_notes")
    if not isinstance(notes, dict) or not notes:
        raise ValueError("changelog source needs at least one release-notes entry")
    for tag, record in notes.items():
        if not isinstance(tag, str) or not tag.startswith("carbonpanel-v") or not isinstance(record, dict):
            raise ValueError(f"invalid changelog release entry: {tag!r}")
        rel = record.get("path")
        if not isinstance(rel, str) or not rel.startswith(NOTES_PREFIX) \
                or "\\" in rel or any(p in ("", ".", "..") for p in rel.split("/")) \
                or not rel.endswith(".md"):
            raise ValueError(f"release notes path outside the allowlist: {rel!r}")
        digest = record.get("sha256")
        if not isinstance(digest, str) or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise ValueError(f"release notes need a lowercase SHA-256: {tag}")
    upgrades = declaration.get("upgrade_links")
    if not isinstance(upgrades, dict):
        raise ValueError("changelog source needs a tag-scoped upgrade_links map")
    for tag, link in upgrades.items():
        if not isinstance(tag, str) or not tag.startswith("carbonpanel-v") \
                or not isinstance(link, dict) \
                or not str(link.get("guide", "")).startswith("docs/") \
                or not str(link.get("anchor", "")).startswith("upgrade-notes-"):
            raise ValueError(f"invalid tag-scoped upgrade link: {tag!r}")
    if declaration.get("nightly_compare") != "stable":
        raise ValueError("nightly changelog must compare to the verified Stable")
    return declaration


def load_notes(root=ROOT, declaration=None):
    """Return {tag: exact release-notes body}, refusing drifted bytes."""
    root = Path(root)
    declaration = declaration or load_source(root)
    notes = {}
    for tag, record in declaration["release_notes"].items():
        data = (root / record["path"]).read_bytes()
        actual = _sha256(data)
        if actual != record["sha256"]:
            raise ValueError(f"release notes for {tag} disagree with the attested digest")
        notes[tag] = data.decode("utf-8")
    return notes


def presentation_fingerprint(root=ROOT):
    """Fingerprint ONLY the changelog presentation inputs.

    Maintained operational guides keep their own declaration and receipt; this
    is a separate channel-presentation identity so a changelog-only source edit
    can qualify a docs-only publication without masquerading as a guide.
    """
    root = Path(root)
    declaration = load_source(root)
    notes = load_notes(root, declaration)
    banner = root / BANNER_SOURCE
    if not banner.is_file():
        raise ValueError(f"channel banner source is missing: {banner}")
    payload = {
        "declaration": declaration,
        "notes": {tag: _sha256(body.encode("utf-8")) for tag, body in notes.items()},
        "generator_sha256": _sha256(Path(__file__).read_bytes()),
        "banner_sha256": _sha256(banner.read_bytes()),
    }
    return {"fingerprint": _sha256(json.dumps(payload, sort_keys=True).encode("utf-8")),
            "schema_version": 1, "inputs": payload}


def render(index, current, declaration, notes, site="https://carbonpanel.dev"):
    """Render one channel-correct changelog page; caller writes it as MDX."""
    origin = site.rstrip("/")
    stable = index["versions"][0]
    nightly = index["versions"][1]
    if stable["channel"] != "stable" or nightly["channel"] != "nightly":
        raise ValueError("version contract must carry Stable and Nightly in order")
    channel = current["channel"]
    entry = stable if channel == "stable" else nightly
    channel_label = index["channels"][channel]["display"]
    parts = [
        "---", "title: Changelog",
        f"description: {channel_label} release and documentation notes.",
        "---", "{/* SPDX-License-Identifier: MPL-2.0 */}", "",
    ]
    def release_block(tag, version_entry):
        """Canonical release link always; local exact body only when checked in."""
        lines = [
            f"Canonical GitHub release notes: "
            f"[{tag}]({index['repository']}/releases/tag/{tag}).",
            f"Application commit: [{version_entry['commit'][:12]}]"
            f"({index['repository']}/commit/{version_entry['commit']}).",
        ]
        upgrade = declaration["upgrade_links"].get(tag)
        if upgrade:
            lines.append(
                f"Tag-specific upgrade notes: [{upgrade['anchor']}]"
                f"({index['repository']}/blob/{tag}/{upgrade['guide']}#{upgrade['anchor']}).")
        if tag in notes:
            lines += ["", notes[tag].strip()]
        else:
            lines.append(
                "No local attested copy of these release notes exists yet; use the "
                "canonical GitHub release link above. This is not a retired-history "
                "or tag-collision claim.")
        return lines

    if channel == "stable":
        notes_tag = entry["tag"]
        parts += [
            "The channel selector above shows this Stable snapshot and its Nightly counterpart. "
            "This page describes the release that produced this documentation.", "",
            f"## Current Stable: {entry['display']}", "",
            f"- Product tag: `{notes_tag}`",
            f"- Image digest: `{index['channels']['stable']['image']['digest']}`",
            f"- Nightly counterpart: [{nightly['display']} changelog]({origin}{nightly['route']}changelog/)",
            "", "## Published release notes", "",
        ]
        parts += release_block(notes_tag, entry)
        parts.append("")
    else:
        stable_tag = stable["tag"]
        compare = (f"{index['repository']}/compare/{stable['commit']}...{entry['commit']}")
        parts += [
            "The channel selector above shows this Nightly snapshot and its Stable counterpart. "
            "Nightly is a rolling image, not a GitHub release; the pins below are the exact "
            "qualified inputs for this build.", "",
            "## Current Nightly", "",
            f"- Qualified revision: [{entry['commit'][:12]}]({index['repository']}/commit/{entry['commit']})",
            f"- Image digest: `{index['channels']['nightly']['image']['digest']}`",
            f"- Promotion state: `{index['channels']['nightly']['promotion']['state']}`",
            f"- Compare to Stable `{stable_tag}` (`{stable['commit'][:12]}`): [revision comparison]({compare})",
            f"- Stable counterpart: [{stable['display']} changelog]({origin}{stable['route']}changelog/)",
            "", "## Baseline Stable release notes", "",
            f"Nightly builds on `{stable_tag}`; the exact published notes for that release are "
            "linked and reproduced locally only when attested, without claiming any retired "
            "or colliding tag names.", "",
        ]
        parts += release_block(stable_tag, stable)
        parts.append("")
    return "\n".join(parts) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("presentation-fingerprint", "render-check"))
    parser.add_argument("--root", default=str(ROOT))
    args = parser.parse_args()
    if args.command == "presentation-fingerprint":
        print(json.dumps(presentation_fingerprint(args.root), indent=2, sort_keys=True))
        return
    root = Path(args.root)
    sys.path.insert(0, str(root))
    from scripts import version_contract as vc
    contract, catalog = vc.load(root)
    index = vc.version_index(vc.release_build_contract(vc.apply_promotion(contract, None)), catalog, root)
    declaration, notes = load_source(root), load_notes(root)
    for current in index["versions"]:
        text = render(index, current, declaration, notes)
        if text.count("\n# ") or text.startswith("# ") or "\n## Changelog" in text:
            raise ValueError("generated changelog must not add a second H1")
        print(f"{current['channel']}: {len(text)} chars, one H1 from frontmatter")


if __name__ == "__main__":
    main()
