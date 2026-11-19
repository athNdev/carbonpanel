#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Build homepage plus stable and immutable per-version docs from exact commits."""
import argparse
from datetime import datetime, timezone
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
import xml.etree.ElementTree as ET
from html.parser import HTMLParser

_ROOT = Path(__file__).resolve().parent.parent
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))
from scripts import openapi_version, version_contract

ROOT = _ROOT
SCHEMA_CACHE = ROOT / ".website-build/schema-cache"
SOURCE_PATHS = version_contract.SOURCE_PATHS
SHELL_FILES = ("package.json", "package-lock.json", "tsconfig.json")
PAGE_LABELS = {
    "": "Documentation home", "introduction": "Introduction",
    "getting-started/docker-compose": "Docker Compose",
    "getting-started/proxmox": "Proxmox (existing guest)",
    "getting-started/prebuilt-binaries": "Prebuilt Binaries",
    "getting-started/build-from-source": "Building from Source",
    "configuration": "Configuration",
    "guides/modpack-studio": "Modpack Studio",
    "guides/server-datapacks": "Server datapacks",
    "guides/staged-updates": "Staged updates",
    "guides/sleep-and-wake": "Sleep and wake",
    "guides/proxy-routing": "Proxy routing",
    "guides/backups": "Server Backups",
    "guides/oidc/keycloak": "Keycloak", "guides/oidc/authelia": "Authelia",
    "guides/oidc/google": "Google", "guides/oidc/discord": "Discord",
    "faq": "FAQ", "troubleshooting": "Troubleshooting", "api": "API Reference",
    "changelog": "Changelog", "versions": "Documentation version",
    "contributing": "Contributing",
}
GROUP_LABELS = {"getting-started": "Getting Started", "guides": "Guides"}
SHELL_DIRS = ("public", "src/styles", "src/components")


class SitemapPageMetadata(HTMLParser):
    def __init__(self):
        super().__init__()
        self.noindex = False

    def handle_starttag(self, tag, attrs):
        if tag != "meta":
            return
        attrs = dict(attrs)
        if attrs.get("name", "").lower() != "robots":
            return
        directives = {item.strip().lower() for item in attrs.get("content", "").split(",")}
        if "noindex" in directives:
            self.noindex = True


def build_homepage(root):
    """Compile and prerender shared Svelte fragments without starting the panel."""
    bun = shutil.which("bun")
    if not bun:
        raise RuntimeError("Bun is required for the homepage's shared UI fragments.")
    project = root / "web/carbon-panel"
    subprocess.run([bun, "install", "--frozen-lockfile"], cwd=project, check=True)
    subprocess.run([bun, "run", "build:homepage"], cwd=project, check=True)
    return root / ".website-build/landing"


def export_snapshot(root, revision, destination):
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("The documentation pin must be a full commit SHA derived from a tag.")
    present = []
    for path in SOURCE_PATHS:
        probe = subprocess.run(["git", "cat-file", "-e", f"{revision}:{path}"],
                               cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if probe.returncode == 0:
            present.append(path)
    if not present:
        return
    archive = subprocess.check_output(
        ["git", "archive", "--format=tar", revision, "--", *present], cwd=root)
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        source.extractall(destination, filter="data")


def scoped_links(text, docs_base, revision):
    # Rebase prose and HTML links without changing shell commands or external URLs.
    text = re.sub(r"(\]\()/(?!/)", lambda m: m[1] + docs_base, text)
    text = re.sub(r"""((?:href|src)=["'])/(?!/)""", lambda m: m[1] + docs_base, text)
    # Starlight hero actions declare links in YAML frontmatter.
    text = re.sub(r"(?m)^(\s*link:\s*)/(?!/)", lambda m: m[1] + docs_base, text)
    return re.sub(
        r"(https://github.com/athNdev/(?:carbonpanel|carbon-panel)/(?:blob|tree)/)main/",
        lambda m: m[1] + revision + "/", text)


def normalize_mdx(text):
    """Make legacy HTML license headers compile under the current MDX parser.

    This is a presentation-only compatibility rewrite of the exact pinned text;
    it never changes authored prose, examples or version identity.
    """
    return re.sub(
        r"<!--(?=(?:(?!<!--|-->).)*SPDX-License-Identifier)((?:(?!-->).)*)-->",
        lambda match: "{/*" + match.group(1) + "*/}", text, flags=re.DOTALL)


def publish_design_system(root, output):
    """Publish the current design-system guide and its canonical tokens."""
    source = root / "docs/design-system"
    destination = output / "design-system"
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source / "site.js", destination / "site.js")
    stylesheet = (source / "site.css").read_text(encoding="utf-8")
    (destination / "site.css").write_text(
        stylesheet.replace("../../landing/assets/", "../assets/"), encoding="utf-8")
    tokens = "web/carbon-panel/src/lib/design-system/tokens.css"
    shutil.copy2(root / tokens, destination / "tokens.css")
    page = (source / "index.html").read_text(encoding="utf-8")
    source_link = "../../" + tokens
    if page.count(source_link) != 2 or page.count("./README.md") != 2:
        raise ValueError("Design-system publication links no longer match the source page.")
    page = page.replace(source_link, "./tokens.css").replace(
        "./README.md", "https://github.com/athNdev/carbonpanel/blob/main/docs/design-system/README.md")
    page = page.replace("../../landing/index.html", "../").replace(
        "../../landing/assets/", "../assets/").replace(
        "../carbon-panel/src/content/docs/index.mdx", "../docs/").replace(
        "../carbon-panel/src/content/docs/getting-started/docker-compose.mdx",
        "../docs/getting-started/docker-compose/")
    (destination / "index.html").write_text(page, encoding="utf-8")


def render_versions_page(index, current, site="https://carbonpanel.dev"):
    """Generate the identity page from the contract, not from a pinned prose file."""
    origin = site.rstrip("/")
    parts = [
        "---",
        "title: Documentation version",
        "description: The application snapshot used to build this documentation.",
        "---",
        "{/* SPDX-License-Identifier: MPL-2.0 */}",
        "",
        f"## Current documentation: {html.escape(current['display'])}",
        "",
        f"**{html.escape(current['channel'])}** / {html.escape(current['availability'])}. "
        f"Application commit: [{current['commit'][:12]}]({index['repository']}/commit/{current['commit']}).",
        "",
        "The guides, configuration examples, Compose files, identity-provider examples, and generated "
        "API schema are taken from this exact commit. The website navigation and visual theme can "
        "evolve independently. Use the version switcher at the top of every page to move between "
        "documented versions.",
        "",
        f"Channel and availability are derived from the classified release catalog, not from the "
        f"version label. {html.escape(current['display'])} is a "
        f"**{html.escape(current['channel'])}** release with availability "
        f"**{html.escape(current['availability'])}**.",
        "",
        "## Documented versions",
        "",
        "| Version | Channel | Availability | Commit |",
        "| --- | --- | --- | --- |",
    ]
    for version in index["versions"]:
        parts.append(
            f"| [{version['display']}]({origin + version['route']}) | {version['channel']} | "
            f"{version['availability']} | `{version['commit'][:12]}` |")
    parts += ["", "## Match your installation", "",
              "Check the version or commit used to build your installation before following a guide. "
              "Later development changes may differ from the snapshot documented here. The API "
              "reference describes the pinned schema; your running panel remains the authority for "
              "the API it actually serves.", ""]
    for version in index["versions"]:
        if version["download_kind"] == "github-release":
            parts += [f"- **{version['display']}**: [download]({version['download_url']}) "
                      f"({version['download_kind']}).", ""]
        elif version["download_kind"] == "rolling-image":
            parts += [f"- **{version['display']}**: rolling `{version['image']['tag']}` image at "
                      f"`{html.escape(version['image']['digest'])}`; no GitHub release.", ""]
        else:
            parts += [f"- **{version['display']}**: source only, no download. "
                      f"Browse the [source tree]({version['download_url']}).", ""]
    parts += ["## Updating the pin", "",
              "Maintainers regenerate `docs/version-index.json` from `docs/website-source.json` and "
              "the classified catalog, review the guides against the exact pinned commit, and rebuild "
              "the complete website. The fail-closed drift checks reject a channel, image digest, "
              "revision, tag or commit disagreement before a package is produced.", ""]
    return "\n".join(parts) + "\n"


def label_for(slug):
    return PAGE_LABELS.get(slug, slug.replace("-", " ").replace("/", " / ").title())


def render_astro_config(shell_text, pages, route):
    """Build a sidebar containing only pages that exist at the exact commit."""
    design_link = "../" * (route.count("/") - 1) + "design-system/"
    top = [p for p in pages if "/" not in p]
    groups = {}
    for slug in pages:
        if "/" in slug:
            groups.setdefault(slug.split("/", 1)[0], []).append(slug)
    lines = ["\t\t\tsidebar: ["]
    for slug in top:
        lines.append(f"\t\t\t\t{{ label: {json.dumps(label_for(slug))}, slug: {json.dumps(slug)} }},")
    for group in sorted(groups):
        lines.append(f"\t\t\t\t{{")
        lines.append(f"\t\t\t\t\tlabel: {json.dumps(GROUP_LABELS.get(group, group.title()))},")
        lines.append("\t\t\t\t\titems: [")
        for slug in sorted(groups[group]):
            lines.append(f"\t\t\t\t\t\t{{ label: {json.dumps(label_for(slug))}, slug: {json.dumps(slug)} }},")
        lines.append("\t\t\t\t\t],")
        lines.append("\t\t\t\t},")
    lines.append("\t\t\t\t// Starlight prefixes local links with its docs base; this is a sibling route.")
    lines.append(f"\t\t\t\t{{ label: 'Design system', link: '{design_link}' }},")
    lines.append("\t\t\t],")
    replacement = "\n".join(lines)
    result, count = re.subn(r"sidebar: \[[\s\S]*?\n\t\t\t\],", replacement, shell_text, count=1)
    if count != 1:
        raise ValueError("Cannot locate the Starlight sidebar block to scope per version.")
    return result


def prepare_project(snapshot, shell, index, current, route, base, root_base, site):
    project = snapshot / "docs/carbon-panel"
    for name in SHELL_FILES:
        shutil.copy2(shell / name, project / name)
    (project / "astro.config.mjs").write_text(
        render_astro_config((shell / "astro.config.mjs").read_text(encoding="utf-8"),
                            current["pages"], route), encoding="utf-8")
    for name in SHELL_DIRS:
        shutil.copytree(shell / name, project / name, dirs_exist_ok=True)
    shutil.copy2(shell / "src/content.config.ts", project / "src/content.config.ts")
    # Authored content (including the docs home) comes from the exact commit.
    # versions.mdx is generated identity presentation, never copied prose.
    generated = project / "src/generated"
    generated.mkdir(parents=True, exist_ok=True)
    shared = version_contract.render_index(index)
    (generated / "version-index.json").write_text(shared, encoding="utf-8")
    (project / "src/content/docs/versions.mdx").write_text(
        render_versions_page(index, current, site), encoding="utf-8")
    channel = index["channels"][current["channel"]]
    source = {
        "revision": current["commit"],
        "label": current["display"],
        "channel_display": current["channel_display"],
        "repository": index["repository"],
        "tag": current["tag"],
        "channel": current["channel"],
        "availability": current["availability"],
        "source_commit": current["source_commit"],
        "image_digest": channel["image"]["digest"],
        "docker_hub": channel["image"]["docker_hub"],
        "ghcr": channel["image"]["ghcr"],
        "promotion_state": channel["promotion"]["state"],
        "promoted": channel["promotion"]["promoted"],
    }
    (project / "public/source.json").write_text(json.dumps(source, indent=2) + "\n", encoding="utf-8")
    for page in (project / "src/content/docs").rglob("*"):
        # versions.mdx is generated from absolute contract routes; rebasing it
        # again would double-prefix every link.
        if page.suffix in (".md", ".mdx") and page.name != "versions.mdx":
            text = scoped_links(page.read_text(encoding="utf-8"), base, current["commit"])
            page.write_text(normalize_mdx(text), encoding="utf-8")
    api_page = project / "src/content/docs/api.mdx"
    api_page.write_text(api_page.read_text(encoding="utf-8").replace(
        "This spec is auto-generated from the protobuf definitions and stays in sync with the latest release.",
        f"This API schema is generated from application commit {current['commit'][:12]} "
        f"({current['display']}, Connect-OpenAPI schema family "
        f"{index['artifacts']['openapi']['schema_family']})."),
        encoding="utf-8")
    # The proto at an exact commit is immutable, so cache its generated schema
    # across rebuilds; a fresh CI checkout still generates each one from source.
    cached = SCHEMA_CACHE / current["commit"] / "schemav1.yaml"
    generated_schema = project / "public/schemav1.yaml"
    if cached.is_file():
        shutil.copy2(cached, generated_schema)
    else:
        template = {"version": "v2", "plugins": [{
            "remote": "buf.build/community/sudorandom-connect-openapi:v0.21.3",
            "out": "docs/carbon-panel/public", "opt": ["path=schemav1.yaml"]}]}
        subprocess.run(["buf", "generate", "--template", json.dumps(template)],
                       cwd=snapshot, check=True)
        cached.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(generated_schema, cached)
    # The website schema copy carries this version's exact product identity.
    # Nightly has no product release number, so its schema uses an explicit
    # non-release semver derived from the pinned revision; the public selector
    # still says only "Nightly".
    schema = generated_schema.read_text(encoding="utf-8")
    generated_schema.write_text(
        openapi_version.stamp_text(schema, current["schema_version_label"]), encoding="utf-8")
    openapi_version.verify_text(
        generated_schema.read_text(encoding="utf-8"), current["schema_version_label"])
    environment = {**os.environ, "DOCS_BASE_PATH": base,
                   "WEBSITE_BASE_PATH": root_base, "WEBSITE_ORIGIN": site,
                   "ASTRO_TELEMETRY_DISABLED": "1"}
    npm = shutil.which("npm.cmd" if os.name == "nt" else "npm")
    if not npm:
        raise RuntimeError("Node.js and npm are required to build the website.")
    subprocess.run([npm, "ci", "--no-audit", "--no-fund"], cwd=project,
                   env=environment, check=True)
    subprocess.run([npm, "run", "build"], cwd=project, env=environment, check=True)
    return project / "dist"


def bases_from_index(index, base):
    """Bases used for canonical URLs, sitemap and direct URL validation."""
    bases = {base.rstrip("/") or "/": base}
    for version in index["versions"]:
        route = version["route"].rstrip("/")
        bases[route or "/"] = route + "/" if route else "/"
    result = []
    for value in bases.values():
        if value not in result:
            result.append(value)
    return sorted(result, key=lambda item: (-len(item), item))


def match_base(relative, bases):
    """Return (base, route-from-base) for a page path relative to the site root."""
    best = "/"
    for candidate in bases:
        prefix = candidate.lstrip("/")
        if prefix and relative.startswith(prefix):
            best = candidate
            break
    remainder = relative[len(best.lstrip("/")):]
    return best, remainder


def build_sitemap(output, base, site, lastmod=None, bases=None):
    """Write a sitemap for every navigable HTML page under its own base."""
    if lastmod is None:
        lastmod = datetime.now(timezone.utc).date().isoformat()
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", lastmod):
        raise ValueError("The sitemap lastmod must be an ISO 8601 date.")
    bases = bases or [base]
    namespace = "http://www.sitemaps.org/schemas/sitemap/0.9"
    ET.register_namespace("", namespace)
    sitemap = ET.Element(f"{{{namespace}}}urlset")
    for page in sorted(output.rglob("*.html")):
        if page.name == "404.html":
            continue
        metadata = SitemapPageMetadata()
        metadata.feed(page.read_text(encoding="utf-8"))
        if metadata.noindex:
            continue
        relative = page.relative_to(output).as_posix()
        page_base, remainder = match_base(relative, bases)
        route = remainder.removesuffix("index.html")
        url = ET.SubElement(sitemap, f"{{{namespace}}}url")
        ET.SubElement(url, f"{{{namespace}}}loc").text = site.rstrip("/") + page_base + route
        ET.SubElement(url, f"{{{namespace}}}lastmod").text = lastmod
    ET.ElementTree(sitemap).write(output / "sitemap.xml", encoding="utf-8", xml_declaration=True)


def build(root, base, site, promotion=None):
    if not base.startswith("/") or not base.endswith("/") or ".." in base:
        raise ValueError("The website base must be an absolute path ending in /.")
    contract, catalog = version_contract.load(root)
    contract = version_contract.apply_promotion(contract, promotion)
    version_contract.validate(contract, catalog, root)
    index = version_contract.version_index(contract, catalog, root)
    candidate = bool(promotion and promotion.get("state") == "candidate")
    staging_route = None
    if candidate:
        # A candidate is staged on a NON-ACTIVE path. Canonical channel paths
        # stay on the active verified receipt until the transaction commits.
        staging_route = version_contract.candidate_route(promotion["revision"])
        index = version_contract.candidate_index(index, promotion["revision"],
                                                 promotion["receipt"])
    homepage = build_homepage(root)
    output = root / ".website-build/site"
    generated_root = (root / ".website-build").resolve()
    if not generated_root.is_relative_to(root.resolve()) or not output.resolve().is_relative_to(generated_root):
        raise ValueError("Build output escaped its generated directory.")
    shell = root / "docs/carbon-panel"
    bases = [staging_route] if candidate else bases_from_index(index, base)
    with tempfile.TemporaryDirectory(prefix="carbon-website-") as temporary:
        work = Path(temporary)
        staged = work / "site"
        shutil.copytree(homepage, staged)
        targets = []
        for version in index["versions"]:
            if candidate and version["channel"] != "nightly":
                continue  # only the promoted channel is staged off-path
            targets.append((version, version["route"]))
            if not candidate and version["immutable_route"] != version["route"]:
                targets.append((version, version["immutable_route"]))
        for position, (version, route) in enumerate(targets):
            snapshot = work / f"v{position}"
            snapshot.mkdir()
            export_snapshot(root, version["commit"], snapshot)
            version_base = staging_route if candidate else base + route.lstrip("/")
            project_base = staging_route if candidate else base
            dist = prepare_project(snapshot, shell, index, version, route,
                                   version_base, project_base, site)
            target = staged / route.strip("/")
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(dist, target, dirs_exist_ok=True)
        publish_design_system(root, staged)
        if output.exists():
            shutil.rmtree(output)
        shutil.copytree(staged, output)
    # Index and marker are shared by the switcher, banner, direct URL validation,
    # public candidate verification and metadata. The marker is only written to
    # the generated artifact; promotion inputs never touch a tracked main file.
    (output / "version-index.json").write_text(version_contract.render_index(index), encoding="utf-8")
    marker = version_contract.channel_marker(index)
    marker_text = json.dumps(marker, indent=2, ensure_ascii=False) + "\n"
    (output / "channel-marker.json").write_text(marker_text, encoding="utf-8")
    if candidate:
        staging = output / staging_route.strip("/")
        staging.mkdir(parents=True, exist_ok=True)
        assets = output / "assets"
        if assets.is_dir():
            shutil.copytree(assets, staging / "assets", dirs_exist_ok=True)
        (staging / "channel-marker.json").write_text(marker_text, encoding="utf-8")
        (staging / "version-index.json").write_text(
            version_contract.render_index(index), encoding="utf-8")
    build_sitemap(output, staging_route if candidate else base, site, bases=bases)
    print(f"Website built at {output}; {len(index['versions'])} channels from exact pinned commits."
          + (f" Candidate probe: {staging_route}" if candidate else ""), flush=True)
    print("Channel marker: " + " ".join(
        f"{name}={entry['revision']}@{entry['digest']} ({entry['state']}, promoted={str(entry['promoted']).lower()})"
        for name, entry in marker["channels"].items()), flush=True)
    return output


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default="/")
    parser.add_argument("--site", default="https://carbonpanel.dev")
    parser.add_argument("--nightly-revision", help="exact promoted nightly commit (external input)")
    parser.add_argument("--nightly-digest", help="sha256 digest verified in BOTH registries")
    parser.add_argument("--promotion-state", default="candidate",
                        choices=version_contract.PROMOTION_STATES)
    parser.add_argument("--promotion-receipt", help="promotion receipt id from the gate")
    args = parser.parse_args()
    promotion = version_contract.promotion_from_env()
    if args.nightly_revision or args.nightly_digest:
        if args.nightly_revision and args.nightly_digest:
            promotion = {
                "revision": args.nightly_revision,
                "digest": args.nightly_digest,
                "state": args.promotion_state,
                "receipt": args.promotion_receipt or "",
            }
        else:
            raise ValueError("--nightly-revision and --nightly-digest must be supplied together")
    build(ROOT, args.base, args.site, promotion)
