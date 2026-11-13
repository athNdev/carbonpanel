#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Reject broken local routes, missing assets and incomplete social previews."""
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

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "meta":
            key = attrs.get("property") or attrs.get("name")
            if key:
                self.metas.setdefault(key, []).append(attrs.get("content", ""))
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

# Pages that are directly shared get their own card instead of the default.
PROMINENT_CARDS = {
    "docs/": "assets/og-docs.png",
    "docs/introduction/": "assets/og-introduction.png",
    "docs/getting-started/docker-compose/": "assets/og-docker-compose.png",
    "docs/api/": "assets/og-api.png",
    "design-system/": "assets/og-design-system.png",
}

EM_DASH = chr(0x2014)


def page_route(relative):
    if relative == "index.html":
        return ""
    if relative.endswith("/index.html"):
        return relative[: -len("index.html")]
    return relative


def single(parser, key, errors, relative):
    values = parser.metas.get(key, [])
    if len(values) != 1:
        errors.append(f"{relative}: expected one {key}, found {len(values)}")
        return ""
    if not values[0].strip():
        errors.append(f"{relative}: empty {key}")
    return values[0]


def check_social(parser, page, root, base, site, errors):
    relative = page.relative_to(root).as_posix()
    route = page_route(relative)
    expected = site.rstrip("/") + base + route

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
    if image.netloc != urlsplit(site).netloc or not image.path.startswith(base):
        errors.append(f"{relative}: og:image must live under {site + base}")
    else:
        target = root / unquote(image.path[len(base):])
        if not target.is_file():
            errors.append(f"{relative}: missing og:image {image.path}")

    card = PROMINENT_CARDS.get(route)
    if card and image.path != base + card:
        errors.append(f"{relative}: expected the dedicated card {card}")


def check_sitemap(root, pages, base, site, errors):
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
        relative = page.relative_to(root).as_posix()
        route = page_route(relative)
        expected.append(site.rstrip("/") + base + route)
    if actual != sorted(expected):
        errors.append("sitemap.xml: entries do not exactly match published HTML routes")


def check(root, base="/", site="https://carbonpanel.dev"):
    errors = []
    pages = list(root.rglob("*.html"))
    for page in pages:
        parser = Links(not_found=page.name == "404.html")
        parser.feed(page.read_text(encoding="utf-8"))
        page_url = site + base + page.relative_to(root).as_posix()
        for link in parser.urls:
            target = urlsplit(urljoin(page_url, link))
            if target.netloc != urlsplit(site).netloc or target.scheme not in ("http", "https"):
                continue
            if not target.path.startswith(base):
                errors.append(f"{page.relative_to(root)}: outside website base: {link}")
                continue
            path = root / unquote(target.path[len(base):])
            if path.is_dir():
                path = path / "index.html"
            if not path.is_file():
                errors.append(f"{page.relative_to(root)}: missing {link}")
        if page.name != "404.html":
            check_social(parser, page, root, base, site, errors)
    check_sitemap(root, pages, base, site, errors)
    if errors:
        raise ValueError("\n".join(sorted(set(errors))))
    pin = json.loads((root / "docs/source.json").read_text(encoding="utf-8"))
    assert len(pin["revision"]) == 40
    assert (root / "docs/schemav1.yaml").stat().st_size > 1000
    assert not list(root.glob("qa-*.html")), "QA harnesses must not be published"
    print(f"Website routes, assets and social previews valid across {len(pages)} HTML pages.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path, nargs="?", default=Path(".website-build/site"))
    parser.add_argument("--base", default="/")
    parser.add_argument("--site", default="https://carbonpanel.dev")
    args = parser.parse_args()
    check(args.root, args.base, args.site)
