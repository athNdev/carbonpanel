#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for the shared README release-channel contract gate."""
import json
from pathlib import Path
import tempfile
import unittest

from scripts import version_bump as bump

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
CONFIG = ROOT / "docs" / "dockerhub-description.json"


def fixture_root(folder, readme, config=None):
    root = Path(folder)
    (root / "docs").mkdir(exist_ok=True)
    (root / "README.md").write_text(readme, encoding="utf-8")
    (root / "docs" / "dockerhub-description.json").write_text(
        json.dumps(config or json.loads(CONFIG.read_text(encoding="utf-8"))),
        encoding="utf-8")
    return root


class VersionBumpChannelContractTests(unittest.TestCase):
    def test_current_readme_and_generator_config_satisfy_the_gate(self):
        # Offline only: the real `prepare --check` entry point (which needs full
        # Git refs) is exercised by the hosted Website Build and Links job.
        self.assertEqual(bump.verify_readme_channels(ROOT), [])

    def test_renamed_channel_link_is_refused(self):
        readme = README.read_text(encoding="utf-8")
        renamed = readme.replace("https://carbonpanel.dev/docs/nightly/",
                                 "https://carbonpanel.dev/docs/preview/")
        with tempfile.TemporaryDirectory() as folder:
            errors = bump.verify_readme_channels(fixture_root(folder, renamed))
            self.assertTrue(any("Nightly documentation" in error for error in errors))

    def test_removed_stable_docs_link_is_refused(self):
        readme = README.read_text(encoding="utf-8")
        changed = readme.replace("[Stable documentation](https://carbonpanel.dev/docs/)",
                                 "[Stable documentation](https://carbonpanel.dev/docs/stable/)")
        self.assertNotEqual(changed, readme)
        with tempfile.TemporaryDirectory() as folder:
            errors = bump.verify_readme_channels(fixture_root(folder, changed))
            self.assertTrue(any("Stable item must link" in error for error in errors))

    def test_missing_channel_alias_is_refused(self):
        readme = README.read_text(encoding="utf-8").replace("`:nightly`", "`:preview`")
        with tempfile.TemporaryDirectory() as folder:
            errors = bump.verify_readme_channels(fixture_root(folder, readme))
            self.assertTrue(any(":nightly" in error for error in errors))

    def test_missing_channel_section_is_refused(self):
        readme = README.read_text(encoding="utf-8").split("## Release channels")[0] + \
            "## Quick start\n\nSee the release channels.\n"
        self.assertNotIn("## Release channels", readme)
        with tempfile.TemporaryDirectory() as folder:
            errors = bump.verify_readme_channels(fixture_root(folder, readme))
            self.assertTrue(any("Release channels" in error for error in errors))

    def test_generator_must_share_the_section(self):
        config = json.loads(CONFIG.read_text(encoding="utf-8"))
        config["sections"] = [name for name in config["sections"]
                              if name != "Release channels"]
        with tempfile.TemporaryDirectory() as folder:
            errors = bump.verify_readme_channels(
                fixture_root(folder, README.read_text(encoding="utf-8"), config))
            self.assertTrue(any("generator config" in error for error in errors))

    def test_historical_version_outside_the_section_is_not_banned(self):
        readme = README.read_text(encoding="utf-8").replace(
            "## Features", "## Features\n\nHistorical rollback reference: 1.0.40.\n", 1)
        with tempfile.TemporaryDirectory() as folder:
            self.assertEqual(bump.verify_readme_channels(
                fixture_root(folder, readme)), [])

    def test_legacy_development_snapshot_intro_is_absent(self):
        self.assertNotIn("development snapshot", README.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
