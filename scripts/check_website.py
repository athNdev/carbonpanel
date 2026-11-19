#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Reject broken routes, missing assets, social previews and version drift."""
import argparse
import copy
from html.parser import HTMLParser
import json
from pathlib import Path
import re
from urllib.parse import unquote, urljoin, urlsplit
import xml.etree.ElementTree as ET

_ROOT = Path(__file__).resolve().parent.parent
import sys  # noqa: E402
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))
from scripts import version_contract as vc  # noqa: E402
from scripts.openapi_version import verify_text  # noqa: E402


class VersionUI(HTMLParser):
    """Read the real banner control: one labelled select, options, pinned link."""

    def __init__(self):
        super().__init__()
        self.selects = []
        self.pinned = []
        self.labels = []
        self.banner_markers = []
        self._select = None
        self._option = None
        self._anchor = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "div" and "data-channel-revision" in attrs:
            self.banner_markers.append(attrs)
        if tag == "label" and attrs.get("for"):
            self.labels.append(attrs["for"])
        if tag == "select" and "data-version-switcher" in attrs:
            self._select = {"attrs": attrs, "options": []}
            self.selects.append(self._select)
        elif tag == "option" and self._select is not None:
            self._option = {"value": attrs.get("value", ""), "text": ""}
            self._select["options"].append(self._option)
        elif tag == "a":
            self._anchor = {"href": attrs.get("href", ""), "text": ""}

    def handle_data(self, data):
        if self._option is not None:
            self._option["text"] += data
        if self._anchor is not None:
            self._anchor["text"] += data

    def handle_endtag(self, tag):
        if tag == "option":
            self._option = None
        elif tag == "select":
            self._select = None
        elif tag == "a" and self._anchor is not None:
            if "Pinned to" in self._anchor["text"]:
                self.pinned.append(self._anchor)
            self._anchor = None


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


def check_versions(root, index, errors, staging_route=None):
    """Direct URL validation, identity agreement and negative cases."""
    excluded = {"v1.0.38", "v3.0.0-beta.4"}
    documented = set()
    for version in index.get("versions", []):
        if staging_route and version["route"] != staging_route:
            continue  # canonical routes remain on the active verified receipt
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
        schema = target / "schemav1.yaml"
        if not schema.is_file():
            errors.append(f"{version['display']}: missing generated OpenAPI schema")
        else:
            try:
                verify_text(schema.read_text(encoding="utf-8"), version["schema_version_label"])
            except ValueError as error:
                errors.append(f"{version['display']}: OpenAPI schema drift: {error}")
        page = (target / "index.html").read_text(encoding="utf-8")
        if "data-version-switcher" not in page:
            errors.append(f"{version['display']}: common version switcher is missing")
        for candidate in index.get("versions", []):
            if candidate["channel_display"] not in page:
                errors.append(f"{version['display']}: switcher omits {candidate['channel_display']}")
    stable = index.get("stable", {})
    default = [v for v in index.get("versions", []) if v.get("default")]
    if len(default) != 1 or default[0]["tag"] != stable.get("tag"):
        errors.append("version index stable identity disagrees with the default route")
    if not (root / "version-index.json").is_file():
        errors.append("version-index.json: missing shared published index")


def check_channels(index, errors, expect=None):
    """Published channels must equal the source contract, with both registries."""
    if index.get("schema_version") != vc.SCHEMA_VERSION:
        errors.append(f"published index schema_version must be {vc.SCHEMA_VERSION}")
    channels = index.get("channels")
    if not isinstance(channels, dict) or set(channels) != set(vc.CHANNEL_NAMES):
        errors.append("published index must contain exactly stable and nightly channels")
        return
    source, _ = vc.load(vc.ROOT)
    source_channels = source.get("channels", {})
    for name in vc.CHANNEL_NAMES:
        channel = channels[name]
        expected = source_channels.get(name, {})
        qualified = expect and expect.get("revision") and name == "nightly"
        if qualified:
            expected = copy.deepcopy(expected)
            if expect.get("state") == "candidate":
                expected["route"] = vc.candidate_route(expect["revision"])
            expected["commit"] = expect["revision"]
            expected["image"] = dict(expected["image"],
                                     revision=expect["revision"],
                                     digest=expect.get("digest", expected["image"]["digest"]))
            expected["image"].pop("bootstrap", None)
            expected["image"].pop("bootstrap_note", None)
            expected["promotion"] = {
                "state": expect.get("state", "candidate"),
                "promoted": True,
                "receipt": expect.get("receipt", ""),
            }
        if channel.get("commit") != expected.get("commit"):
            errors.append(f"stale {name} revision in the published index")
        if channel != expected:
            errors.append(f"published {name} channel marker disagrees with the source contract")
        image = channel.get("image") or {}
        for key in ("docker_hub", "ghcr", "digest", "revision"):
            if not image.get(key):
                errors.append(f"{name}: missing image.{key}")
        if image.get("revision") != channel.get("commit"):
            errors.append(f"{name}: mismatched image revision marker")
        if channel.get("display") != vc.CHANNEL_DISPLAY[name]:
            errors.append(f"{name}: public selector may only use {vc.CHANNEL_DISPLAY[name]!r}")
        expected_route = expected.get("route", vc.CHANNEL_ROUTES[name])
        if channel.get("route") != expected_route:
            errors.append(f"{name}: route must be {expected_route!r}")
        promotion = channel.get("promotion") or {}
        if promotion.get("promoted") is not expected.get("promotion", {}).get("promoted"):
            errors.append(f"{name}: promotion claim disagrees with the expected channel")
        state = promotion.get("state")
        if name == "stable" and state != "published":
            errors.append("stable promotion state must be published")
        if name == "nightly" and state not in ("baseline", *vc.PROMOTION_STATES):
            errors.append("nightly promotion state must be baseline, candidate or final")
    serialized = json.dumps(index)
    for retired in vc.RETIRED_DISPLAYS:
        if retired in serialized:
            errors.append(f"published index contains retired snapshot {retired!r}")
    for route in vc.RETIRED_ROUTES:
        if route in serialized:
            errors.append(f"published index contains retired doc route {route!r}")


def check_marker(root, index, expect=None):
    """Verify the public per-channel marker against the index and exact input."""
    errors = []
    path = root / "channel-marker.json"
    if not path.is_file():
        return errors + ["channel-marker.json: missing published promotion marker"]
    try:
        marker = json.loads(path.read_text(encoding="utf-8"))
    except ValueError as error:
        return errors + [f"channel-marker.json: invalid JSON: {error}"]
    if marker != vc.channel_marker(index):
        errors.append("channel-marker.json disagrees with the published version index")
    channels = marker.get("channels", {})
    if set(channels) != set(vc.CHANNEL_NAMES):
        errors.append("channel-marker.json must contain exactly stable and nightly")
        return errors
    for name in vc.CHANNEL_NAMES:
        entry = channels[name]
        if not vc.SHA.fullmatch(str(entry.get("revision", ""))):
            errors.append(f"{name}: marker revision is not a full 40-hex commit")
        if not vc.DIGEST.fullmatch(str(entry.get("digest", ""))):
            errors.append(f"{name}: marker digest is not a sha256 digest")
        reference = index["channels"][name]
        if entry.get("revision") != reference["commit"]:
            errors.append(f"{name}: marker revision disagrees with the channel commit")
        if entry.get("digest") != reference["image"]["digest"]:
            errors.append(f"{name}: marker digest disagrees with the image digest")
        if entry.get("docker_hub") != reference["image"]["docker_hub"] or \
                entry.get("ghcr") != reference["image"]["ghcr"]:
            errors.append(f"{name}: marker registry refs disagree with the channel")
        if entry.get("pinned_url") != \
                f"{index['repository'].rstrip('/')}/commit/{entry.get('revision')}":
            errors.append(f"{name}: marker pinned URL is not the full canonical commit")
        state = entry.get("state")
        if entry.get("promoted") is True:
            if name == "nightly":
                if state not in vc.PROMOTION_STATES:
                    errors.append(f"{name}: promoted marker state must be candidate or final")
                if not entry.get("receipt"):
                    errors.append(f"{name}: promoted marker needs a receipt id")
            elif state != "published":
                errors.append(f"{name}: promoted marker state must be published")
        elif name == "nightly":
            if state != "baseline" or entry.get("promoted") is not False:
                errors.append("nightly baseline marker must not claim a promotion")
    nightly = channels["nightly"]
    if nightly.get("digest") == vc.BOOTSTRAP_BASELINE["digest"] and \
            nightly.get("revision") != vc.BOOTSTRAP_BASELINE["commit"]:
        errors.append("nightly marker relabels the stale baseline digest")
    if expect:
        for key, field in (("revision", "revision"), ("digest", "digest"),
                           ("state", "state"), ("receipt", "receipt")):
            wanted = expect.get(key)
            if wanted and nightly.get(field) != wanted:
                errors.append(f"nightly marker {field} is not the qualified {wanted}")
        if expect.get("revision") and expect["revision"] != vc.BOOTSTRAP_BASELINE["commit"] and \
                nightly.get("digest") == vc.BOOTSTRAP_BASELINE["digest"]:
            errors.append("qualified nightly marker still carries the stale baseline digest")
    return errors


def expected_switcher_hrefs(root, index, page, bases):
    """Same canonical rule as VersionBanner.astro, derived from the built page."""
    relative = page.relative_to(root).as_posix()
    _, remainder = match_base(relative, bases)
    slug = page_route(remainder).strip("/")
    return [vc.switcher_href(version, slug) for version in index.get("versions", [])]


def check_version_ui(root, page, index, bases, errors, marker=None):
    """The banner must ship a real, labelled dropdown and an immutable pin."""
    relative = page.relative_to(root).as_posix()
    if not any(relative.startswith(version["route"].lstrip("/"))
               for version in index.get("versions", [])):
        return  # homepage/design-system pages have no version banner
    ui = VersionUI()
    ui.feed(page.read_text(encoding="utf-8"))
    if len(ui.selects) != 1:
        errors.append(f"{relative}: expected exactly one real version dropdown")
        return
    select = ui.selects[0]
    attrs = select["attrs"]
    if not attrs.get("aria-label") and attrs.get("id") not in ui.labels:
        errors.append(f"{relative}: version dropdown has no accessible name")
    onchange = attrs.get("onchange", "")
    if "location" not in onchange or "this.value" not in onchange:
        errors.append(f"{relative}: version dropdown does not navigate on change")
    texts = sorted(option["text"].strip() for option in select["options"])
    if texts != ["Nightly", "Stable"]:
        errors.append(f"{relative}: selector options must be Stable and Nightly, got {texts}")
    values = [option["value"] for option in select["options"]]
    expected = expected_switcher_hrefs(root, index, page, bases)
    if sorted(values) != sorted(expected):
        errors.append(f"{relative}: switcher hrefs {values} are not canonical {expected}")
    for value in values:
        if "//" in value or not value.startswith("/docs/"):
            errors.append(f"{relative}: non-canonical switcher href {value!r}")
        for retired in vc.RETIRED_DISPLAYS:
            if retired in value:
                errors.append(f"{relative}: retired snapshot in switcher href {value!r}")

    if len(ui.pinned) != 1:
        errors.append(f"{relative}: expected exactly one full-SHA pinned link")
        return
    href = ui.pinned[0]["href"]
    pattern = re.escape(index["repository"].rstrip("/")) + r"/commit/([0-9a-f]{40})\Z"
    match = re.fullmatch(pattern, href)
    if not match:
        errors.append(f"{relative}: non-canonical pinned commit URL {href!r}")
        return
    content_base, _ = match_base(relative, bases)
    source = root / content_base.strip("/") / "source.json"
    if source.is_file():
        pin = json.loads(source.read_text(encoding="utf-8"))
        if pin.get("revision") != match.group(1):
            errors.append(f"{relative}: pinned link revision does not match the route source.json")
        if marker is not None:
            channel = pin.get("channel")
            reference = marker.get("channels", {}).get(channel)
            if reference is None:
                errors.append(f"{relative}: source.json has no published channel marker")
            else:
                if match.group(1) != reference["revision"]:
                    errors.append(f"{relative}: pinned link revision disagrees with the marker")
                if pin.get("image_digest") != reference["digest"]:
                    errors.append(f"{relative}: source.json digest disagrees with the marker")
        if len(ui.banner_markers) != 1:
            errors.append(f"{relative}: expected one data-channel marker on the banner")
        else:
            banner = ui.banner_markers[0]
            if marker is not None and channel in marker.get("channels", {}):
                reference = marker["channels"][channel]
                for attr, key in (("data-channel-revision", "revision"),
                                  ("data-channel-digest", "digest"),
                                  ("data-channel-promotion", "state"),
                                  ("data-channel-promoted", "promoted")):
                    expected_value = str(reference[key]).lower() if isinstance(reference[key], bool) \
                        else reference[key]
                    if banner.get(attr) != expected_value:
                        errors.append(f"{relative}: banner {attr} disagrees with the marker")


def check(root, base="/", site="https://carbonpanel.dev", expect=None):
    errors = []
    pages = list(root.rglob("*.html"))
    index_path = root / "version-index.json"
    index = json.loads(index_path.read_text(encoding="utf-8")) if index_path.is_file() else None
    staging_route = None
    if index is not None:
        staging_route = next(
            (channel["route"] for channel in index.get("channels", {}).values()
             if channel.get("promotion", {}).get("state") == "candidate"
             and channel.get("promotion", {}).get("promoted")), None)
        if staging_route and not staging_route.startswith("/docs/candidate/"):
            errors.append(f"candidate route {staging_route} is not a non-active staging path")
            staging_route = None
    bases = [staging_route] if staging_route else bases_from_index(root, base)
    marker_errors = check_marker(root, index, expect) if index is not None else []
    errors.extend(marker_errors)
    marker = None
    if index is not None and (root / "channel-marker.json").is_file():
        try:
            marker = json.loads((root / "channel-marker.json").read_text(encoding="utf-8"))
        except ValueError:
            pass
    for page in pages:
        parser = Links(not_found=page.name == "404.html")
        parser.feed(page.read_text(encoding="utf-8"))
        page_url = site + base + page.relative_to(root).as_posix()
        for link in parser.urls:
            if link.startswith("#"):
                continue  # in-page anchor, not a version route
            target = urlsplit(urljoin(page_url, link))
            if target.netloc != urlsplit(site).netloc or target.scheme not in ("http", "https"):
                continue
            if staging_route and not target.path.startswith(staging_route):
                continue  # canonical paths belong to the active receipt, not this candidate
            if any(f"/docs/versions/{retired}/" in target.path for retired in ("1.0.39", "3.0.0-alpha.1")):
                errors.append(f"{page.relative_to(root)}: retired snapshot URL {link}")
            if target.path.startswith("/docs/versions/") and target.path != "/docs/versions/" and index is not None:
                allowed = {v["immutable_route"] for v in index["versions"]}
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
            in_staging = bool(staging_route and page.relative_to(root).as_posix().startswith(
                staging_route.lstrip("/")))
            check_social(parser, page, root, content_base,
                         staging_route if in_staging else base, site, errors)
            if index is not None:
                check_version_ui(root, page, index, bases, errors, marker)
    check_sitemap(root, pages, base, site, errors, bases)
    if index is not None:
        check_channels(index, errors, expect)
        check_versions(root, index, errors, staging_route)
    if errors:
        raise ValueError("\n".join(sorted(set(errors))))
    print(f"Website routes, assets, versions and social previews valid across {len(pages)} HTML pages.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path, nargs="?", default=Path(".website-build/site"))
    parser.add_argument("--base", default="/")
    parser.add_argument("--site", default="https://carbonpanel.dev")
    parser.add_argument("--expect-nightly-revision", help="exact qualified nightly commit to verify")
    parser.add_argument("--expect-nightly-digest", help="exact sha256 digest to verify in the marker")
    parser.add_argument("--expect-promotion-state", choices=vc.PROMOTION_STATES)
    parser.add_argument("--expect-promotion-receipt")
    parser.add_argument("--expect-marker", type=Path,
                        help="promotion receipt JSON with revision/digest/state/receipt")
    args = parser.parse_args()
    expect = {}
    if args.expect_marker:
        record = json.loads(args.expect_marker.read_text(encoding="utf-8"))
        for key in ("revision", "digest", "state", "receipt"):
            if key in record:
                expect[key] = record[key]
    for key, value in (("revision", args.expect_nightly_revision),
                       ("digest", args.expect_nightly_digest),
                       ("state", args.expect_promotion_state),
                       ("receipt", args.expect_promotion_receipt)):
        if value:
            expect[key] = value
    check(args.root, args.base, args.site, expect or None)
