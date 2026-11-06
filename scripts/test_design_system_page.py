#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

"""Regression checks for the standalone design-system homepage."""

from pathlib import Path
import unittest


PAGE = Path(__file__).resolve().parent.parent / "docs" / "design-system" / "index.html"
CSS = Path(__file__).resolve().parent.parent / "docs" / "design-system" / "site.css"


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

    def test_theme_preview_toggle_is_present(self):
        page = PAGE.read_text(encoding="utf-8")
        self.assertIn("data-theme-toggle", page)


if __name__ == "__main__":
    unittest.main()
