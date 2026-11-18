# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Offline contract tests for strict GHCR preflight and anchored identity."""

from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import release  # noqa: E402
import mcp_ghcr_identity as identity  # noqa: E402
import mcp_ghcr_preflight as preflight  # noqa: E402

DIGEST = "sha256:" + "a" * 64
OTHER = "sha256:" + "b" * 64
IMAGE = "ghcr.io/athndev/carbonpanel-mcp"
REVISION = "f" * 40
VERSION = "v0.1.0"
NAME = "io.github.athNdev/carbon-panel"


def labels(revision: str = REVISION, version: str = VERSION, name: str = NAME):
    return {
        "io.modelcontextprotocol.server.name": name,
        "org.opencontainers.image.revision": revision,
        "org.opencontainers.image.version": version,
    }


class StrictObservationTests(unittest.TestCase):
    def test_ambiguous_errors_are_unknown_not_absent(self):
        reference = f"{IMAGE}:{VERSION}"
        for message in ("unauthorized", "connection reset", "internal server error",
                        "404 not found", "error: some-other/repo:v1: not found"):
            with self.subTest(message=message):
                self.assertEqual(
                    release.classify_lookup_failure(
                        subprocess.CalledProcessError(1, "docker", stderr=message),
                        strict=True, reference=reference),
                    "unknown",
                )

    def test_scoped_definitive_not_found_is_absent(self):
        reference = f"{IMAGE}:{VERSION}"
        message = f"error: {reference}: not found"
        self.assertEqual(
            release.classify_lookup_failure(
                subprocess.CalledProcessError(1, "docker", stderr=message),
                strict=True, reference=reference),
            "absent",
        )

    def test_preflight_decisions(self):
        original = release.observe_reference
        try:
            release.observe_reference = lambda ref, strict=False: ("absent", None)
            self.assertEqual(preflight.decide(IMAGE, VERSION, "", "present")["action"], "publish")
            self.assertEqual(preflight.decide(IMAGE, VERSION, DIGEST, "present")["action"], "refuse")

            release.observe_reference = lambda ref, strict=False: ("present", {"digest": DIGEST})
            self.assertEqual(preflight.decide(IMAGE, VERSION, "", "present")["action"], "refuse")
            self.assertEqual(preflight.decide(IMAGE, VERSION, DIGEST, "present")["action"], "resume")
            self.assertEqual(preflight.decide(IMAGE, VERSION, OTHER, "present")["action"], "refuse")

            release.observe_reference = lambda ref, strict=False: ("unknown", None)
            self.assertEqual(preflight.decide(IMAGE, VERSION, "", "present")["action"], "refuse")
        finally:
            release.observe_reference = original

    def test_definitive_package_absence_and_ambiguous_states(self):
        # First tag: authoritative 404 Package not found means publish is safe;
        # a requested resume digest cannot exist and must refuse.
        published = preflight.decide(IMAGE, VERSION, "", "absent")
        self.assertEqual(published["action"], "publish")
        self.assertEqual(preflight.decide(IMAGE, VERSION, DIGEST, "absent")["action"], "refuse")
        unknown = preflight.decide(IMAGE, VERSION, "", "unknown")
        self.assertEqual(unknown["action"], "refuse")
        self.assertIn("unknown", unknown["reason"])

    def test_package_state_parser_uses_definitive_404(self):
        original = preflight.subprocess.run
        try:
            class Result:
                stdout = ("HTTP/2.0 404 Not Found\n\n"
                          '{"message":"Package not found.","status":"404"}')
            preflight.subprocess.run = lambda *a, **k: Result()
            self.assertEqual(preflight.github_package_state(IMAGE),
                             ("absent", "definitive 404 Package not found from GitHub Packages API"))
            Result.stdout = "HTTP/2.0 403 Forbidden\n\n"
            self.assertEqual(preflight.github_package_state(IMAGE)[0], "unknown")
            Result.stdout = "HTTP/2.0 200 OK\n\n[]"
            self.assertEqual(preflight.github_package_state(IMAGE)[0], "present")
        finally:
            preflight.subprocess.run = original


class AnchoredIdentityTests(unittest.TestCase):
    def setUp(self):
        self.original_platforms = release.manifest_platforms
        self.original_labels = identity.platform_labels
        release.manifest_platforms = lambda image, anchor_digest=None: (
            DIGEST, {("linux", "amd64"): DIGEST, ("linux", "arm64"): OTHER})
        identity.platform_labels = lambda image, child: labels()

    def tearDown(self):
        release.manifest_platforms = self.original_platforms
        identity.platform_labels = self.original_labels

    def test_matching_both_platforms_verifies(self):
        ok, reason, evidence = identity.verify(IMAGE, DIGEST, REVISION, VERSION, NAME)
        self.assertTrue(ok, reason)
        self.assertEqual(len(evidence["platforms"]), 2)

    def test_missing_platform_refuses(self):
        release.manifest_platforms = lambda image, anchor_digest=None: (
            DIGEST, {("linux", "amd64"): DIGEST})
        ok, _reason, _evidence = identity.verify(IMAGE, DIGEST, REVISION, VERSION, NAME)
        self.assertFalse(ok)

    def test_wrong_revision_version_or_label_refuses(self):
        for overrides in ({"revision": "0" * 40}, {"version": "latest"}, {"name": "io.github.other/x"}):
            with self.subTest(overrides=overrides):
                identity.platform_labels = lambda image, child, o=overrides: labels(**o)
                ok, _reason, _evidence = identity.verify(IMAGE, DIGEST, REVISION, VERSION, NAME)
                self.assertFalse(ok)

    def test_index_digest_mismatch_refuses(self):
        release.manifest_platforms = lambda image, anchor_digest=None: (
            OTHER, {("linux", "amd64"): DIGEST, ("linux", "arm64"): OTHER})
        ok, _reason, _evidence = identity.verify(IMAGE, DIGEST, REVISION, VERSION, NAME)
        self.assertFalse(ok)


if __name__ == "__main__":
    unittest.main()
