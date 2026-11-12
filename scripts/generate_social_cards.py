#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

"""Render website Open Graph cards and the repository social preview.

Uses the homepage's approved logo, terrain and Plex fonts. Requires a
Chromium-family browser; set CHROME_PATH to override its executable. Use
--html-only --html-dir <directory> for standalone HTML previews without
starting a browser. Run from the repo root:

    python3 scripts/generate_social_cards.py
"""

import argparse
import base64
from html import escape
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from string import Template

ROOT = Path(__file__).resolve().parent.parent
FONTS = ROOT / "landing/assets/fonts"
ASSETS = ROOT / "landing/assets"
BRAND = ROOT / "assets/branding"
TEMPLATE = BRAND / "social-preview.html"

CARDS = {
    "og": {
        "eyebrow": "SELF-HOSTED. OPEN SOURCE. MINECRAFT.",
        "title": ["Your worlds.", "Yours to run."],
        "subtitle": ["Your Minecraft servers. Fully equipped."],
        "url": "carbonpanel.dev",
        "variant": "home",
    },
    "og-docs": {
        "eyebrow": "DOCUMENTATION",
        "title": ["Carbon Panel", "Documentation"],
        "subtitle": ["Install, configure, and run the panel on", "infrastructure you manage."],
        "url": "carbonpanel.dev/docs/",
    },
    "og-introduction": {
        "eyebrow": "INTRODUCTION",
        "title": ["Understand", "Carbon Panel"],
        "subtitle": ["What the panel does and how it fits", "your Minecraft setup."],
        "url": "carbonpanel.dev/docs/introduction/",
    },
    "og-docker-compose": {
        "eyebrow": "GETTING STARTED",
        "title": ["Docker", "Compose"],
        "subtitle": ["Requirements, the Compose file, data paths,", "and first startup."],
        "url": "carbonpanel.dev/docs/getting-started/docker-compose/",
    },
    "og-api": {
        "eyebrow": "REFERENCE",
        "title": ["API", "Reference"],
        "subtitle": ["Every Connect RPC, generated from the", "protobuf definitions."],
        "url": "carbonpanel.dev/docs/api/",
    },
    "og-design-system": {
        "eyebrow": "DESIGN SYSTEM",
        "title": ["Carbon", "Design System"],
        "subtitle": ["Foundations, components and patterns shared", "by Carbon Panel and Carbon Cloud."],
        "url": "carbonpanel.dev/design-system/",
    },
    "github-social-preview": {
        "eyebrow": "SELF-HOSTED. OPEN SOURCE. MINECRAFT.",
        "title": ["Your worlds.", "Yours to run."],
        "subtitle": ["Your Minecraft servers. Fully equipped."],
        "url": "github.com/athNdev/carbonpanel",
        "variant": "home",
        "width": 1280,
        "height": 640,
    },
}


def font_face(family, weight, path):
    data = base64.b64encode(path.read_bytes()).decode("ascii")
    return (
        "@font-face{font-family:'%s';font-style:normal;font-weight:%s;font-display:block;"
        "src:url(data:font/woff2;base64,%s) format('woff2');}" % (family, weight, data)
    )


def page_style():
    return font_face("IBM Plex Sans", "400 600", FONTS / "ibm-plex-sans-0.woff2") + font_face(
        "IBM Plex Mono", "400", FONTS / "ibm-plex-mono-0.woff2"
    )


def card_html(card):
    def data_uri(path, mime):
        return f"data:{mime};base64," + base64.b64encode(path.read_bytes()).decode("ascii")

    return Template(TEMPLATE.read_text(encoding="utf-8")).substitute(
        page_title=escape("Carbon Panel · " + " ".join(card["title"])),
        font_faces=page_style(),
        width=card.get("width", 1200), height=card.get("height", 630),
        variant=card.get("variant", "document"),
        logo=data_uri(BRAND / "horizontal-light.svg", "image/svg+xml"),
        world=data_uri(ASSETS / "graphite-world.webp", "image/webp"),
        eyebrow=escape(card["eyebrow"]),
        title="".join(f"<span>{escape(line)}</span>" for line in card["title"]),
        subtitle="<br />".join(escape(line) for line in card["subtitle"]),
        url=escape(card["url"]),
        footer_label="MINECRAFT SERVER MANAGEMENT" if card.get("variant") == "home" else "CARBON PANEL / OPEN SOURCE",
    )


def card_output(name):
    return BRAND / "social-preview.png" if name == "github-social-preview" else ASSETS / f"{name}.png"


def find_browser():
    override = os.environ.get("CHROME_PATH")
    candidates = [
        Path(override) if override else None,
        Path(r"C:\Program Files\Google\Chrome\Application\chrome.exe"),
        Path(r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe"),
        Path(r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"),
        Path(r"C:\Program Files\Microsoft\Edge\Application\msedge.exe"),
        Path("/usr/bin/google-chrome"),
        Path("/usr/bin/chromium"),
        Path("/usr/bin/chromium-browser"),
        Path("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
    ]
    for candidate in candidates:
        if candidate and candidate.is_file():
            return candidate
    for name in ("chrome", "chromium", "chromium-browser", "google-chrome", "msedge"):
        found = shutil.which(name)
        if found:
            return Path(found)
    raise SystemExit("No Chromium-family browser found. Set CHROME_PATH.")


def render(browser, html_path, output, width=1200, height=630):
    with tempfile.TemporaryDirectory(prefix="carbon-og-") as profile:
        subprocess.run(
            [str(browser), "--headless=new", "--disable-gpu", "--hide-scrollbars",
             "--no-first-run", "--no-default-browser-check", "--force-device-scale-factor=1",
             f"--window-size={width},{height}", f"--user-data-dir={profile}",
             f"--screenshot={output}", html_path.as_uri()],
            check=True, capture_output=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only", choices=sorted(CARDS))
    parser.add_argument("--html-dir", type=Path, help="Keep standalone HTML render inputs here.")
    parser.add_argument("--html-only", action="store_true", help="Write HTML without launching a browser.")
    args = parser.parse_args()
    if args.html_only and not args.html_dir:
        parser.error("--html-only requires --html-dir")
    browser = None if args.html_only else find_browser()
    if args.html_dir:
        args.html_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="carbon-og-html-") as temporary:
        for name, card in CARDS.items():
            if args.only and args.only != name:
                continue
            html_path = (args.html_dir or Path(temporary)) / (name + ".html")
            html_path.write_text(card_html(card), encoding="utf-8")
            if args.html_only:
                print(f"wrote {html_path}")
                continue
            output = card_output(name)
            render(browser, html_path.resolve(), output, card.get("width", 1200), card.get("height", 630))
            print(f"wrote {output.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
