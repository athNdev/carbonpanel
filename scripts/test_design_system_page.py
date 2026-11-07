#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

"""Regression checks for the standalone design-system homepage."""

from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlsplit
import re
import unittest


ROOT = Path(__file__).resolve().parent.parent
PAGE = ROOT / "docs" / "design-system" / "index.html"
CSS = ROOT / "docs" / "design-system" / "site.css"
EXPORTS = ROOT / "web" / "carbon-panel" / "src" / "lib" / "components" / "carbon" / "index.ts"


def component_anchor(name):
    kebab = re.sub(r"(?<!^)(?=[A-Z])", "-", name.removeprefix("Carbon")).lower()
    return f"c-{kebab}"


class PreviewParser(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.metadata = {}
        self.canonical = None
        self.html_lang = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "html":
            self.html_lang = attrs.get("lang")
        if tag == "meta":
            key = attrs.get("property", attrs.get("name", ""))
            self.metadata[key] = attrs.get("content", "")
        if tag == "link" and attrs.get("rel") == "canonical":
            self.canonical = attrs.get("href")


class DesignSystemPageTests(unittest.TestCase):
    def test_type_section_names_the_finalized_faces(self):
        page = PAGE.read_text(encoding="utf-8")
        self.assertNotIn("IBM Plex", page)
        self.assertIn("Inter", page)
        self.assertIn("JetBrains Mono", page)

    def test_component_board_covers_the_button_kinds(self):
        page = PAGE.read_text(encoding="utf-8")
        for kind in ("primary", "secondary", "tertiary", "ghost"):
            self.assertIn(f'class="button {kind}"', page)

    def test_component_board_demonstrates_tabs_tiles_and_labels(self):
        page = PAGE.read_text(encoding="utf-8")
        self.assertIn("tabs-demo", page)
        self.assertIn("tab-demo active", page)
        self.assertIn("tile-demo", page)
        self.assertIn("eyebrow-demo", page)

    def test_demo_styles_exist_for_every_demo_hook(self):
        css = CSS.read_text(encoding="utf-8")
        for hook in (
            ".button.tertiary",
            ".button.ghost",
            ".tabs-demo",
            ".tab-demo.active",
            ".tile-demo",
            ".eyebrow-demo",
        ):
            self.assertIn(hook, css)

    def test_tokens_link_resolves_to_the_canonical_source(self):
        page = PAGE.read_text(encoding="utf-8")
        self.assertIn("../../web/carbon-panel/src/lib/design-system/tokens.css", page)
        target = (
            PAGE.parent
            / "../../web/carbon-panel/src/lib/design-system/tokens.css"
        ).resolve()
        self.assertTrue(target.is_file(), f"missing tokens source: {target}")

    def test_every_exported_component_has_a_documented_section(self):
        exports = re.findall(
            r"export \{ default as (\w+) \} from", EXPORTS.read_text(encoding="utf-8")
        )
        self.assertGreaterEqual(len(exports), 19, "component export list changed unexpectedly")
        page = PAGE.read_text(encoding="utf-8")
        for name in exports:
            anchor = component_anchor(name)
            self.assertIn(f'id="{anchor}"', page, f"missing section for {name}")
            section = page.split(f'id="{anchor}"', 1)[1].split("</article>", 1)[0]
            self.assertIn(name, section, f"{name} is not named in its section")
            self.assertIn("props-table", section, f"{name} has no prop table")

    def test_theme_preview_toggle_is_present(self):
        page = PAGE.read_text(encoding="utf-8")
        self.assertIn("data-theme-toggle", page)

    def test_page_declares_a_well_formed_html_element(self):
        page = PAGE.read_text(encoding="utf-8")
        parser = PreviewParser()
        parser.feed(page)
        self.assertEqual(parser.html_lang, "en")
        self.assertNotIn('<html lang="en"></html>', page)

    def test_social_preview_metadata_is_complete(self):
        parser = PreviewParser()
        parser.feed(PAGE.read_text(encoding="utf-8"))
        metadata = parser.metadata
        for key in ("og:type", "og:site_name", "og:title", "og:description",
                    "og:url", "og:image", "og:image:type", "og:image:width",
                    "og:image:height", "og:image:alt", "twitter:card",
                    "twitter:title", "twitter:description", "twitter:image",
                    "twitter:image:alt"):
            self.assertTrue(metadata.get(key), key)
            self.assertNotIn(chr(0x2014), metadata[key], key)
        self.assertEqual(metadata["og:url"],
                         "https://athn.dev/carbonpanel/design-system/")
        self.assertEqual(parser.canonical, metadata["og:url"])
        self.assertEqual(metadata["og:image"], metadata["twitter:image"])
        self.assertEqual(metadata["twitter:card"], "summary_large_image")
        self.assertEqual((metadata["og:image:width"], metadata["og:image:height"]),
                         ("1200", "630"))
        image = urlsplit(metadata["og:image"])
        self.assertEqual(image.netloc, "athn.dev")
        self.assertTrue(image.query, "A replaced preview needs a cache version")
        target = PAGE.parent / ".." / ".." / "landing" / image.path.removeprefix("/carbonpanel/")
        self.assertTrue(target.resolve().is_file(), f"missing card: {target}")


if __name__ == "__main__":
    unittest.main()
