#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Reject broken routes, missing assets, social previews and version drift."""
import argparse
from html.parser import HTMLParser
import json
from pathlib import Path
import re
from urllib.parse import unquote, urljoin, urlsplit
import xml.etree.ElementTree as ET


class Links(HTMLParser):
    def __init__(self, not_found=False):
        super().__init__()
        self.urls = []
        self.not_found = not_found
        self.metas = {}
        self.canonical = None
        self.noindex = False

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "meta":
            key = attrs.get("property") or attrs.get("name")
            if key:
                self.metas.setdefault(key, []).append(attrs.get("content", ""))
            if attrs.get("name", "").lower() == "robots":
                directives = {
                    item.strip().lower()
                    for item in attrs.get("content", "").split(",")
                }
                self.noindex = "noindex" in directives
        if tag == "link" and attrs.get("rel") == "canonical":
            # Starlight's synthetic 404 has a canonical /404/ URL but is emitted
            # as 404.html. It is not a navigable content route.
            if self.not_found:
                return
            self.canonical = attrs.get("href")
        for name in ("href", "src"):
            if attrs.get(name):
                self.urls.append(attrs[name])


REQUIRED_SOCIAL = (
    "og:type",
    "og:site_name",
    "og:title",
    "og:description",
    "og:url",
    "og:image",
    "og:image:type",
    "og:image:width",
    "og:image:height",
    "og:image:alt",
    "twitter:card",
    "twitter:title",
    "twitter:image",
    "twitter:image:alt",
)

# Keyed by the docs slug, so every version route uses its dedicated card.
PROMINENT_CARDS = {
    "docs": "assets/og-docs.png",
    "introduction": "assets/og-introduction.png",
    "getting-started/docker-compose": "assets/og-docker-compose.png",
    "api": "assets/og-api.png",
    "design-system": "assets/og-design-system.png",
}

EM_DASH = chr(0x2014)


def page_route(relative):
    if relative == "index.html":
        return ""
    if relative.endswith("/index.html"):
        return relative[: -len("index.html")]
    return relative


def card_key(route):
    return page_route(route).strip("/")


def match_base(relative, bases):
    best = "/"
    for candidate in bases:
        prefix = candidate.lstrip("/")
        if prefix and relative.startswith(prefix):
            best = candidate
            break
    return best, relative[len(best.lstrip("/")):]


def bases_from_index(root, base):
    index_path = root / "version-index.json"
    if not index_path.is_file():
        return [base]
    index = json.loads(index_path.read_text(encoding="utf-8"))
    bases = [base]
    for version in index.get("versions", []):
        for route in (version["route"], version.get("immutable_route", version["route"])):
            route = route.rstrip("/")
            candidate = route + "/" if route else "/"
            if candidate not in bases:
                bases.append(candidate)
    return sorted(bases, key=lambda item: (-len(item), item))


def single(parser, key, errors, relative):
    values = parser.metas.get(key, [])
    if len(values) != 1:
        errors.append(f"{relative}: expected one {key}, found {len(values)}")
        return ""
    if not values[0].strip():
        errors.append(f"{relative}: empty {key}")
    return values[0]


def check_social(parser, page, root, content_base, asset_base, site, errors):
    relative = page.relative_to(root).as_posix()
    _, remainder = match_base(relative, [content_base])
    route = page_route(remainder)
    expected = site.rstrip("/") + content_base + route

    values = {key: single(parser, key, errors, relative) for key in REQUIRED_SOCIAL}
    if parser.canonical != expected:
        errors.append(f"{relative}: canonical {parser.canonical!r} is not {expected!r}")
    if values["og:url"] != expected:
        errors.append(f"{relative}: og:url {values['og:url']!r} is not {expected!r}")
    if values["twitter:card"] != "summary_large_image":
        errors.append(f"{relative}: twitter:card must be summary_large_image")
    if values["og:image:type"] != "image/png":
        errors.append(f"{relative}: og:image:type must be image/png")
    if (values["og:image:width"], values["og:image:height"]) != ("1200", "630"):
        errors.append(f"{relative}: og:image must declare 1200x630")
    if values["twitter:image"] != values["og:image"]:
        errors.append(f"{relative}: twitter:image must match og:image")
    for key in ("og:title", "og:description", "og:image:alt", "twitter:title",
                "twitter:image:alt"):
        if EM_DASH in values[key]:
            errors.append(f"{relative}: {key} contains an em dash")

    image = urlsplit(values["og:image"])
    if not image.query:
        errors.append(f"{relative}: og:image needs a cache version query")
    if image.netloc != urlsplit(site).netloc or not image.path.startswith(asset_base):
        errors.append(f"{relative}: og:image must live under {site + asset_base}")
    else:
        target = root / unquote(image.path[len(asset_base):])
        if not target.is_file():
            errors.append(f"{relative}: missing og:image {image.path}")

    card = PROMINENT_CARDS.get(card_key(route))
    if card and image.path != asset_base + card:
        errors.append(f"{relative}: expected the dedicated card {card}")


def check_sitemap(root, pages, base, site, errors, bases):
    sitemap_path = root / "sitemap.xml"
    if not sitemap_path.is_file():
        errors.append("sitemap.xml: missing generated sitemap")
        return
    try:
        sitemap = ET.parse(sitemap_path)
    except ET.ParseError as error:
        errors.append(f"sitemap.xml: invalid XML: {error}")
        return

    namespace = {"sitemap": "http://www.sitemaps.org/schemas/sitemap/0.9"}
    entries = sitemap.findall("sitemap:url", namespace)
    actual = []
    for entry in entries:
        loc = entry.findtext("sitemap:loc", default="", namespaces=namespace)
        lastmod = entry.findtext("sitemap:lastmod", default="", namespaces=namespace)
        if not lastmod or not re.fullmatch(r"\d{4}-\d{2}-\d{2}", lastmod):
            errors.append(f"sitemap.xml: invalid lastmod for {loc or '<missing loc>'}")
        actual.append(loc)

    expected = []
    for page in pages:
        if page.name == "404.html":
            continue
        parser = Links(not_found=False)
        parser.feed(page.read_text(encoding="utf-8"))
        if parser.noindex:
            continue
        relative = page.relative_to(root).as_posix()
        content_base, remainder = match_base(relative, bases)
        expected.append(site.rstrip("/") + content_base + page_route(remainder))
    if len(actual) != len(set(actual)):
        errors.append("sitemap.xml: duplicate URL entries")
    missing = sorted(set(expected) - set(actual))
    extra = sorted(set(actual) - set(expected))
    if missing or extra:
        details = []
        if missing:
            details.append(f"missing {', '.join(missing)}")
        if extra:
            details.append(f"unexpected {', '.join(extra)}")
        errors.append("sitemap.xml: " + "; ".join(details))


def check_versions(root, index, errors):
    """Direct URL validation, identity agreement and negative cases."""
    excluded = {"v1.0.38", "v3.0.0-beta.4"}
    documented = set()
    for version in index.get("versions", []):
        tag = version["tag"]
        documented.add(tag)
        if tag in excluded:
            errors.append(f"version index documents excluded tag {tag}")
        target = root / version["route"].strip("/")
        immutable = root / version.get("immutable_route", version["route"]).strip("/")
        if not (target / "index.html").is_file():
            errors.append(f"{version['display']}: missing direct route {version['route']}")
            continue
        if not (immutable / "index.html").is_file():
            errors.append(f"{version['display']}: missing immutable route {version['immutable_route']}")
            continue
        pin = json.loads((target / "source.json").read_text(encoding="utf-8"))
        if pin.get("revision") != version["commit"] or pin.get("source_commit") != version["source_commit"]:
            errors.append(f"{version['display']}: published source.json disagrees with the index")
        if not (target / "schemav1.yaml").is_file():
            errors.append(f"{version['display']}: missing generated OpenAPI schema")
        page = (target / "index.html").read_text(encoding="utf-8")
        if "data-version-switcher" not in page:
            errors.append(f"{version['display']}: common version switcher is missing")
        for candidate in index.get("versions", []):
            if candidate["display"] not in page:
                errors.append(f"{version['display']}: switcher omits {candidate['display']}")
    stable = index.get("stable", {})
    default = [v for v in index.get("versions", []) if v.get("default")]
    if len(default) != 1 or default[0]["tag"] != stable.get("tag"):
        errors.append("version index stable identity disagrees with the default route")
    if not (root / "version-index.json").is_file():
        errors.append("version-index.json: missing shared published index")


def check(root, base="/", site="https://carbonpanel.dev"):
    errors = []
    bases = bases_from_index(root, base)
    pages = list(root.rglob("*.html"))
    index_path = root / "version-index.json"
    index = json.loads(index_path.read_text(encoding="utf-8")) if index_path.is_file() else None
    for page in pages:
        parser = Links(not_found=page.name == "404.html")
        parser.feed(page.read_text(encoding="utf-8"))
        page_url = site + base + page.relative_to(root).as_posix()
        for link in parser.urls:
            target = urlsplit(urljoin(page_url, link))
            if target.netloc != urlsplit(site).netloc or target.scheme not in ("http", "https"):
                continue
            if target.path.startswith("/docs/versions/") and index is not None:
                allowed = {v["route"] for v in index["versions"]}
                if not any(target.path.startswith(route) for route in allowed):
                    errors.append(f"{page.relative_to(root)}: unsupported version URL {link}")
            if not any(target.path.startswith(b) for b in bases):
                errors.append(f"{page.relative_to(root)}: outside website base: {link}")
                continue
            # All routes are served below the root site base; the per-version
            # bases only select canonical URLs, not filesystem locations.
            path = root / unquote(target.path[len(base):])
            if path.is_dir():
                path = path / "index.html"
            if not path.is_file():
                errors.append(f"{page.relative_to(root)}: missing {link}")
        if page.name != "404.html" and not parser.noindex:
            content_base, _ = match_base(page.relative_to(root).as_posix(), bases)
            check_social(parser, page, root, content_base, base, site, errors)
    check_sitemap(root, pages, base, site, errors, bases)
    if index is not None:
        check_versions(root, index, errors)
    if errors:
        raise ValueError("\n".join(sorted(set(errors))))
    print(f"Website routes, assets, versions and social previews valid across {len(pages)} HTML pages.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path, nargs="?", default=Path(".website-build/site"))
    parser.add_argument("--base", default="/")
    parser.add_argument("--site", default="https://carbonpanel.dev")
    args = parser.parse_args()
    check(args.root, args.base, args.site)
