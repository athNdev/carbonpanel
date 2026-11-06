#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

"""Render the 1200x630 Open Graph cards for the prominent website pages.

The landing card (landing/assets/og.png) is a designed artifact. The newer
per-page cards are generated from the same layout so a copy change does not
need a design round trip. Requires a Chromium-family browser; set
CHROME_PATH to override the discovered executable. Run from the repo root:

    python3 scripts/generate_social_cards.py
"""

import argparse
import base64
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
FONTS = ROOT / "web/carbon-panel/static/fonts"
ASSETS = ROOT / "landing/assets"

# The approved mark, already positioned for the 1200x630 canvas.
MARK = ("M145 65 L194.5 89.75 L194.5 147.5 L164.8 162.35 L164.8 134.85 "
        "L145 144.75 L145 172.25 L95.5 147.5 L95.5 89.75 Z")

CARDS = {
    "og-docs": {
        "eyebrow": "DOCUMENTATION",
        "title": ["Carbon Panel", "Documentation"],
        "subtitle": ["Install, configure, and run the panel on", "infrastructure you manage."],
        "url": "athn.dev/carbonpanel/docs/",
    },
    "og-introduction": {
        "eyebrow": "INTRODUCTION",
        "title": ["Understand", "Carbon Panel"],
        "subtitle": ["What the panel does and how it fits", "your Minecraft setup."],
        "url": "athn.dev/carbonpanel/docs/introduction/",
    },
    "og-docker-compose": {
        "eyebrow": "GETTING STARTED",
        "title": ["Docker Compose"],
        "subtitle": ["Requirements, the Compose file, data paths,", "and first startup."],
        "url": "athn.dev/carbonpanel/docs/getting-started/docker-compose/",
    },
    "og-api": {
        "eyebrow": "REFERENCE",
        "title": ["API Reference"],
        "subtitle": ["Every Connect RPC, generated from the", "protobuf definitions."],
        "url": "athn.dev/carbonpanel/docs/api/",
    },
    "og-design-system": {
        "eyebrow": "DESIGN SYSTEM",
        "title": ["Carbon", "Design System"],
        "subtitle": ["Foundations, components and patterns shared", "by Carbon Panel and Carbon Cloud."],
        "url": "athn.dev/carbonpanel/design-system/",
    },
}


def font_face(family, weight, path):
    data = base64.b64encode(path.read_bytes()).decode("ascii")
    return (
        "@font-face{font-family:'%s';font-style:normal;font-weight:%s;font-display:block;"
        "src:url(data:font/woff2;base64,%s) format('woff2');}" % (family, weight, data)
    )


def page_style():
    return font_face("Inter", "100 900", FONTS / "inter-latin-wght-normal.woff2") + font_face(
        "JetBrains Mono", "100 800", FONTS / "jetbrains-mono-latin-wght-normal.woff2"
    )


def card_html(card):
    lines = len(card["title"])
    title_top = 374 if lines == 1 else 356
    svg_title = "".join(
        f'<tspan x="96" y="{title_top + index * 82}">{line}</tspan>'
        for index, line in enumerate(card["title"])
    )
    subtitle_top = 468 if lines == 1 else 512
    svg_subtitle = "".join(
        f'<tspan x="96" y="{subtitle_top + index * 38}">{line}</tspan>'
        for index, line in enumerate(card["subtitle"])
    )
    return f"""<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><style>{page_style()}</style></head>
<body style="margin:0">
<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
  <rect width="1200" height="630" fill="#161616"/>
  <path d="{MARK}" fill="#f4f4f4"/>
  <text x="232" y="145" fill="#f4f4f4" font-family="Inter" font-weight="700"
        font-size="40" letter-spacing="1.5">CARBON PANEL</text>
  <text x="96" y="294" fill="#8d8d8d" font-family="'JetBrains Mono'" font-weight="400"
        font-size="18" letter-spacing="4">{card["eyebrow"]}</text>
  <text fill="#f4f4f4" font-family="Inter" font-weight="600" font-size="72"
        letter-spacing="-1">{svg_title}</text>
  <text fill="#c6c6c6" font-family="Inter" font-weight="400" font-size="27">{svg_subtitle}</text>
  <line x1="96" y1="584" x2="1104" y2="584" stroke="#2f2f2f" stroke-width="1"/>
  <text x="96" y="608" fill="#8d8d8d" font-family="'JetBrains Mono'" font-weight="400"
        font-size="17" letter-spacing="0.5">{card["url"]}</text>
</svg>
</body></html>"""


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


def render(browser, html_path, output):
    with tempfile.TemporaryDirectory(prefix="carbon-og-") as profile:
        subprocess.run(
            [str(browser), "--headless=new", "--disable-gpu", "--hide-scrollbars",
             "--no-first-run", "--no-default-browser-check", "--force-device-scale-factor=1",
             "--window-size=1200,630", f"--user-data-dir={profile}",
             f"--screenshot={output}", html_path.as_uri()],
            check=True, capture_output=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--only", choices=sorted(CARDS))
    args = parser.parse_args()

    browser = find_browser()
    with tempfile.TemporaryDirectory(prefix="carbon-og-html-") as temporary:
        for name, card in CARDS.items():
            if args.only and args.only != name:
                continue
            html_path = Path(temporary) / (name + ".html")
            html_path.write_text(card_html(card), encoding="utf-8")
            output = ASSETS / (name + ".png")
            render(browser, html_path, output)
            print(f"wrote {output.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
