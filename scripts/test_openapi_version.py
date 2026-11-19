#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Fail-closed OpenAPI product-version stamping tests."""
import tempfile
from pathlib import Path
import unittest

from scripts.openapi_version import (
    stamp_text,
    verify_protocol_v1,
    verify_text,
    verify_ui,
)

SCHEMA = """openapi: 3.1.0
info:
  title: Carbon Panel API
  description: |-
    Something with an indented key
    version: not-the-info-version
  version: 2.x
paths:
  /carbonpanel.v1.ActivityService/ListActivityLogs:
    post:
      summary: List
"""


class OpenApiVersionTests(unittest.TestCase):
    def test_placeholder_is_stamped_to_the_exact_product_version(self):
        stamped = stamp_text(SCHEMA, "2.0.0")
        self.assertIn("  version: 2.0.0\n", stamped)
        self.assertNotIn("  version: 2.x\n", stamped)
        self.assertIn("version: not-the-info-version", stamped)
        verify_text(stamped, "2.0.0")

    def test_stamping_is_idempotent(self):
        stamped = stamp_text(SCHEMA, "1.0.40")
        self.assertEqual(stamp_text(stamped, "1.0.40"), stamped)

    def test_prerelease_version_is_exact(self):
        stamped = stamp_text(SCHEMA, "3.0.0-alpha.1")
        verify_text(stamped, "3.0.0-alpha.1")

    def test_non_placeholder_is_not_overwritten(self):
        with self.assertRaises(ValueError) as raised:
            stamp_text(SCHEMA.replace("version: 2.x", "version: 1.2.3"), "2.0.0")
        self.assertIn("Refusing to overwrite", str(raised.exception))

    def test_placeholder_and_mismatch_fail_verification(self):
        with self.assertRaises(ValueError) as raised:
            verify_text(SCHEMA, "2.0.0")
        self.assertIn("is not", str(raised.exception))
        stamped = stamp_text(SCHEMA, "2.0.0")
        with self.assertRaises(ValueError) as raised:
            verify_text(stamped, "2.0.1")
        self.assertIn("is not", str(raised.exception))

    def test_protocol_identity_stays_v1(self):
        stamped = stamp_text(SCHEMA, "2.0.0")
        verify_protocol_v1(stamped)
        with self.assertRaises(ValueError):
            verify_protocol_v1(stamped.replace("carbonpanel.v1.", "carbonpanel.v2."))

    def test_only_exact_semver_is_accepted(self):
        with self.assertRaises(ValueError):
            stamp_text(SCHEMA, "2.x")
        with self.assertRaises(ValueError):
            stamp_text(SCHEMA, "v2.0.0")

    def test_ui_version_uses_authoritative_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "version.json").write_text('{"version": "2.0.0"}\n', encoding="utf-8")
            verify_ui(root, "2.0.0")
            with self.assertRaises(ValueError):
                verify_ui(root, "2.0.1")

    def test_version_string_in_an_unrelated_asset_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "schemav1.yaml").write_text("  version: 2.0.0\n", encoding="utf-8")
            (root / "index.html").write_text("<p>2.0.0</p>", encoding="utf-8")
            with self.assertRaises(ValueError) as raised:
                verify_ui(root, "2.0.0")
            self.assertIn("version.json", str(raised.exception))
            (root / "version.json").write_text('{"version": "1.0.40"}\n', encoding="utf-8")
            with self.assertRaises(ValueError) as raised:
                verify_ui(root, "2.0.0")
            self.assertIn("UI runtime version", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
