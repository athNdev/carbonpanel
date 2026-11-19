#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Fail-closed overlay for release-independent, maintained operational guides.

Product documentation (guides, configuration examples and the generated schema)
is pinned to the exact application commit of each channel. A small set of
operational pages - first-party install/update procedures - is maintained on the
first-party docs source instead, because a retired external helper must not stay
published just because an application release is immutable.

``docs/maintained-guides.json`` is a narrow *classification/path/compatibility*
declaration. It deliberately contains no revision and no content hashes: those
would become the same stale pin that caused the original defect. The exact
source revision is the checked-out ``git HEAD`` of the trusted website build,
and the content digests are computed from the exact Git blobs. Both are attested in the
built guide manifest. Editing a guide in a later commit therefore refreshes the
published output and its attested digest on the next build with no manifest
edit at all.

Nothing here mutates the tracked docs source, the product pin, the channel
marker or any generated identity file.
"""
import hashlib
import json
import re
import subprocess
from pathlib import Path
import sys

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))

DECLARATION_PATH = _ROOT / "docs/maintained-guides.json"
DOCS_PREFIX = "docs/carbon-panel/src/content/docs/"
DOCS_SUFFIXES = (".mdx", ".md")
MODES = ("page", "fragment")
SHA = re.compile(r"[0-9a-f]{40}\Z")
SLUG = re.compile(r"[a-z0-9][a-z0-9/_-]*\Z")
SUMMARY = re.compile(r"[^<>\r\n]{1,160}\Z")
DETAILS_BLOCK = re.compile(
    r"^[ \t]*<details>\s*<summary>\s*{summary}\s*</summary>[\s\S]*?^[ \t]*</details>",
    re.MULTILINE,
)


def _problems(problems):
    if problems:
        raise ValueError("maintained guides declaration drift:\n- " + "\n- ".join(sorted(set(problems))))


def _git(root, *args):
    return subprocess.check_output(["git", *args], cwd=root)


def _git_ok(root, *args):
    return subprocess.run(["git", *args], cwd=root,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def load(root=_ROOT):
    """Read the declaration from its tracked path; a missing file is fatal."""
    path = Path(root) / "docs/maintained-guides.json"
    try:
        declaration = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as error:
        raise ValueError(f"missing maintained guides declaration: {path}") from error
    validate(declaration, root)
    return declaration


def head_revision(root=_ROOT):
    """The exact commit of the trusted website checkout."""
    revision = _git(root, "rev-parse", "HEAD").decode().strip()
    if not SHA.fullmatch(revision):
        raise ValueError("checked-out HEAD is not a full 40-hex commit")
    return revision


def derive_revision(root=_ROOT):
    """The maintained source is the exact checked-out HEAD, and nothing else.

    There is no floating ref and no secondary revision input: a caller cannot
    silently disclose an older or foreign guide. A different source must first
    become the trusted checkout that builds the site.
    """
    return head_revision(root), "git HEAD"


def validate(declaration, root=_ROOT, revision=None, pinned_pages=None):
    """Validate the declaration's structure and, when given, exact Git state."""
    problems = []
    if not isinstance(declaration, dict):
        raise ValueError("maintained guides declaration must be a JSON object")
    if declaration.get("schema_version") != 1:
        problems.append("maintained guides schema_version must be 1")
    if declaration.get("repository") != "https://github.com/athNdev/carbonpanel":
        problems.append("maintained guides repository must be the public first-party repository")

    compatibility = declaration.get("compatibility")
    if not isinstance(compatibility, dict) \
            or compatibility.get("require_slug_in_every_pinned_channel") is not True \
            or compatibility.get("preserve_product_pin_and_schema") is not True:
        problems.append("maintained guides compatibility guarantees must all be enabled")

    if "source" in declaration:
        problems.append("maintained guides declaration must not pin a source revision or hashes")
    if any("sha" in key or "revision" in key or "commit" in key for guide in _guides(declaration)
           for key in guide):
        problems.append("maintained guide entries must not pin revisions or content hashes")

    guides = declaration.get("guides")
    if not isinstance(guides, list) or not guides:
        _problems(problems + ["maintained guides must declare at least one guide"])

    slugs = set()
    source_paths = set()
    for index, guide in enumerate(guides):
        label = f"guides[{index}]"
        if not isinstance(guide, dict):
            problems.append(f"{label} must be an object")
            continue
        slug = guide.get("slug")
        if not isinstance(slug, str) or not SLUG.fullmatch(slug):
            problems.append(f"{label}.slug is not a valid docs slug")
        elif slug in slugs:
            problems.append(f"duplicate maintained guide slug {slug!r}")
        else:
            slugs.add(slug)

        if guide.get("mode") not in MODES:
            problems.append(f"{label}.mode must be one of {', '.join(MODES)}")

        path = guide.get("source_path")
        if not isinstance(path, str) or not path.startswith(DOCS_PREFIX) \
                or not path.endswith(DOCS_SUFFIXES) or "\\" in path \
                or any(part in ("", ".", "..") for part in path.split("/")):
            problems.append(f"{label}.source_path is outside the allowlisted docs content tree")
        elif path in source_paths:
            problems.append(f"duplicate maintained source path {path!r}")
        else:
            source_paths.add(path)

        provenance = guide.get("provenance")
        if not isinstance(provenance, dict) or not all(
                provenance.get(key) for key in ("title", "scope", "reason")):
            problems.append(f"{label}.provenance needs title, scope and reason")

        if guide.get("mode") == "fragment":
            for key in ("source_summary", "target_summary"):
                value = guide.get(key)
                if not isinstance(value, str) or not SUMMARY.fullmatch(value):
                    problems.append(f"{label}.{key} must be a single safe <summary> label")
            if guide.get("source_summary") == guide.get("target_summary"):
                problems.append(f"{label} fragment summaries must differ")

    if revision is not None:
        if not SHA.fullmatch(str(revision)):
            problems.append("maintained source revision must be a full 40-hex commit")
        else:
            for guide in _guides(declaration):
                path = guide.get("source_path")
                if isinstance(path, str) and not _git_ok(root, "cat-file", "-e", f"{revision}:{path}"):
                    problems.append(f"source path {path!r} is missing at revision {revision}")
                elif isinstance(path, str) and \
                        _git(root, "cat-file", "-t", f"{revision}:{path}").decode().strip() != "blob":
                    problems.append(f"source path {path!r} is not a Git blob")

    if pinned_pages is not None:
        for slug in slugs:
            for channel, pages in pinned_pages.items():
                if slug not in pages:
                    problems.append(f"maintained slug {slug!r} is absent from {channel} pinned pages")

    _problems(problems)


def _guides(declaration):
    guides = declaration.get("guides") if isinstance(declaration, dict) else None
    return guides if isinstance(guides, list) else []


def source_bytes(root, revision, path):
    """Return the exact maintained source blob at the derived revision."""
    return _git(root, "show", f"{revision}:{path}")


GUIDE_SOURCE_LABEL = "**Guide source:**"


def _guide_source_note(declaration, revision, guide):
    """Visible provenance for a whole maintained page, distinct from the app pin."""
    url = (f"{declaration['repository'].rstrip('/')}/blob/{revision}/"
           f"{guide['source_path']}")
    return (
        "\n\n## Guide source\n\n"
        f"{GUIDE_SOURCE_LABEL} [exact authored file at `{revision[:12]}`]({url}). "
        "This operational guide is maintained with the website source, independently of the "
        "pinned application release. The `Pinned to` link above identifies the application "
        "build, not this guide.\n"
    )


def _details_block(text, summary):
    pattern = re.compile(DETAILS_BLOCK.pattern.format(summary=re.escape(summary)),
                         DETAILS_BLOCK.flags)
    matches = pattern.findall(text)
    if len(matches) != 1:
        raise ValueError(
            f"maintained fragment <summary> {summary!r} matched {len(matches)} blocks, expected exactly one")
    return matches[0]


def _target_page(docs_dir, slug):
    candidates = [docs_dir / f"{slug}{suffix}" for suffix in DOCS_SUFFIXES]
    candidates += [docs_dir / slug / f"index{suffix}" for suffix in DOCS_SUFFIXES]
    present = [candidate for candidate in candidates if candidate.is_file()]
    if len(present) != 1:
        raise ValueError(
            f"maintained guide {slug!r} matched {len(present)} pinned page files, expected exactly one")
    return present[0]


def apply(declaration, docs_dir, public_dir, channel_commit, revision=None,
          revision_origin=None, root=_ROOT):
    """Overlay declared guides onto an exported snapshot; return provenance records.

    The caller applies link rebasing afterwards, exactly as for pinned prose.
    Channel identity (``source.json``, marker, schema label) is intentionally
    untouched: only the declared authored files and a separate provenance file
    are written. The attested revision and per-guide SHA-256 are derived from
    exact Git objects at build time, never read from the declaration.
    """
    if revision is None:
        revision, revision_origin = derive_revision(root)
    revision_origin = revision_origin or "build-derived"
    validate(declaration, root, revision)
    docs_dir = Path(docs_dir)
    public_dir = Path(public_dir)
    records = []
    for guide in declaration["guides"]:
        content = source_bytes(root, revision, guide["source_path"])
        source = content.decode("utf-8")
        target = _target_page(docs_dir, guide["slug"])
        if guide["mode"] == "page":
            source_text_value = source
            note = _guide_source_note(declaration, revision, guide)
            if GUIDE_SOURCE_LABEL not in source_text_value:
                source_text_value += note
            rendered = source_text_value.encode("utf-8")
        else:
            replacement = _details_block(source, guide["source_summary"])
            current = target.read_bytes().decode("utf-8")
            # A newer channel snapshot can already carry the maintained block
            # (post-retirement commit) and no longer have the retired anchor.
            # Refresh that block in place rather than aborting, so the overlay
            # is idempotent across old and current pinned snapshots.
            try:
                stale = _details_block(current, guide["target_summary"])
            except ValueError:
                stale = _details_block(current, guide["source_summary"])
            rendered = current if stale == replacement else current.replace(stale, replacement, 1)
            rendered = rendered.encode("utf-8")
        target.write_bytes(rendered)
        records.append({
            "slug": guide["slug"],
            "mode": guide["mode"],
            "source_revision": revision,
            "source_sha256": hashlib.sha256(content).hexdigest(),
            "source_path": guide["source_path"],
            "target_file": target.relative_to(docs_dir).as_posix(),
            "channel_commit": channel_commit,
            "provenance": dict(guide["provenance"]),
        })
    public_dir.mkdir(parents=True, exist_ok=True)
    document = {
        "schema_version": declaration["schema_version"],
        "repository": declaration["repository"],
        "compatibility": dict(declaration["compatibility"]),
        "source": {
            "revision": revision,
            "origin": revision_origin,
            "pinned_url": f"{declaration['repository'].rstrip('/')}/commit/{revision}",
        },
        "channels": records,
    }
    (public_dir / "maintained-guides.json").write_text(
        json.dumps(document, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return records
