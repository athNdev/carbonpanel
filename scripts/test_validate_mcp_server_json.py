# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
"""Tests for the local MCP manifest validator (no network, no registry writes)."""

from __future__ import annotations

import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import validate_mcp_server_json as validator  # noqa: E402

BASELINE = json.loads((ROOT / "docker" / "mcp.server.json").read_text(encoding="utf-8"))


def published_listing() -> dict:
    document = copy.deepcopy(BASELINE)
    document["packages"][0]["identifier"] = document["packages"][0]["identifier"].replace(
        "0" * 64, "a" * 64
    )
    return {
        "server": document,
        "_meta": {validator.OFFICIAL_REGISTRY_METADATA: {"status": "active"}},
    }


def errors_for(document: dict, **kwargs) -> list[str]:
    errors, _warnings = validator.validate(document, **kwargs)
    return errors


class ManifestContractTests(unittest.TestCase):
    def test_checked_in_manifest_is_structurally_valid_with_placeholder_warning(self):
        errors, warnings = validator.validate(BASELINE)
        self.assertEqual(errors, [])
        self.assertTrue(any("placeholder" in warning for warning in warnings))

    def test_real_digest_is_accepted_in_strict_mode(self):
        document = copy.deepcopy(BASELINE)
        real = "sha256:" + "a" * 64
        document["packages"][0]["identifier"] = document["packages"][0][
            "identifier"
        ].replace("0" * 64, "a" * 64)
        errors, warnings = validator.validate(document, require_real_digest=True)
        self.assertEqual(errors, [])
        self.assertEqual(warnings, [])
        self.assertIn(real, document["packages"][0]["identifier"])

    def test_placeholder_digest_is_refused_in_strict_mode(self):
        self.assertTrue(
            any("placeholder" in error for error in errors_for(BASELINE, require_real_digest=True))
        )

    def test_latest_server_version_is_refused(self):
        document = copy.deepcopy(BASELINE)
        document["version"] = "latest"
        self.assertTrue(any("moving reference" in error for error in errors_for(document)))

    def test_latest_identifier_tag_is_refused(self):
        document = copy.deepcopy(BASELINE)
        identifier = document["packages"][0]["identifier"]
        document["packages"][0]["identifier"] = identifier.replace(":v0.1.0@", ":latest@")
        self.assertTrue(any("pinned" in error for error in errors_for(document)))

    def test_version_ranges_are_refused(self):
        for value in ("^1.0.0", ">=1.0.0", "1.x", "1.*"):
            document = copy.deepcopy(BASELINE)
            document["version"] = value
            self.assertTrue(any("range" in error for error in errors_for(document)), value)

    def test_missing_required_root_field_is_refused(self):
        for field in ("name", "description", "version", "repository"):
            document = copy.deepcopy(BASELINE)
            del document[field]
            self.assertTrue(
                any(f"missing required root field: {field}" in error for error in errors_for(document))
            )

    def test_description_limit_is_enforced(self):
        document = copy.deepcopy(BASELINE)
        document["description"] = "x" * 101
        self.assertTrue(any("1 to 100 characters" in error for error in errors_for(document)))

    def test_name_must_match_oci_ownership_label(self):
        self.assertEqual(errors_for(BASELINE, dockerfile_label="io.github.other/name"), [
            "OCI label 'io.github.other/name' does not match registry name "
            "'io.github.athNdev/carbon-panel'"
        ])

    def test_checked_in_dockerfile_label_matches(self):
        label = validator.read_dockerfile_label(ROOT / "docker" / "Dockerfile.mcp")
        self.assertEqual(label, validator.EXPECTED_NAME)
        self.assertEqual(errors_for(BASELINE, dockerfile_label=label), [])

    def test_non_oci_package_type_is_refused(self):
        document = copy.deepcopy(BASELINE)
        document["packages"][0]["registryType"] = "npm"
        self.assertTrue(any("registryType" in error for error in errors_for(document)))

    def test_registry_incompatible_legacy_oci_fields_are_refused(self):
        for field, value in (("registryBaseUrl", "https://ghcr.io"), ("version", "v0.1.0"),
                             ("fileSha256", "a" * 64)):
            with self.subTest(field=field):
                document = copy.deepcopy(BASELINE)
                document["packages"][0][field] = value
                self.assertTrue(any(field in error for error in errors_for(document)))

    def test_oci_image_and_tag_must_match_server_manifest(self):
        document = copy.deepcopy(BASELINE)
        package = document["packages"][0]
        package["identifier"] = package["identifier"].replace("carbonpanel-mcp", "other-package")
        self.assertTrue(any("identifier must be" in error for error in errors_for(document)))
        package["identifier"] = BASELINE["packages"][0]["identifier"].replace(":v0.1.0@", ":v0.1.1@")
        self.assertTrue(any("tag must equal" in error for error in errors_for(document)))

    def test_stdio_transport_and_docker_hint_are_required(self):
        document = copy.deepcopy(BASELINE)
        document["packages"][0]["transport"] = {"type": "streamable-http"}
        document["packages"][0]["runtimeHint"] = "node"
        joined = " ".join(errors_for(document))
        self.assertIn("stdio", joined)
        self.assertIn("runtimeHint", joined)

    def test_token_must_be_marked_secret(self):
        document = copy.deepcopy(BASELINE)
        for item in document["packages"][0]["environmentVariables"]:
            if item["name"] == "CARBONPANEL_TOKEN":
                item["isSecret"] = False
        self.assertTrue(any("isSecret" in error for error in errors_for(document)))

    def test_stable_repository_id_is_required(self):
        document = copy.deepcopy(BASELINE)
        document["repository"]["id"] = "1"
        self.assertTrue(any("repository.id" in error for error in errors_for(document)))

    def test_cli_accepts_valid_manifest_and_rejects_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "server.json"
            path.write_text(json.dumps(BASELINE), encoding="utf-8")
            self.assertEqual(validator.main(["--file", str(path)]), 0)
            self.assertEqual(validator.main(["--file", str(path), "--require-real-digest"]), 1)
            broken = copy.deepcopy(BASELINE)
            broken["version"] = "latest"
            path.write_text(json.dumps(broken), encoding="utf-8")
            self.assertEqual(validator.main(["--file", str(path)]), 1)


class PublishedListingTests(unittest.TestCase):
    def test_exact_active_version_and_digest_are_accepted(self):
        listing = published_listing()
        self.assertEqual(validator.verify_listing(listing, listing["server"]), [])

    def test_search_response_is_not_exact_version_evidence(self):
        listing = published_listing()
        self.assertTrue(validator.verify_listing({"servers": [listing]}, listing["server"]))

    def test_other_version_wrong_digest_and_inactive_entry_are_refused(self):
        expected = published_listing()["server"]
        for change in ("version", "digest", "status", "name"):
            with self.subTest(change=change):
                listing = published_listing()
                server = listing["server"]
                package = server["packages"][0]
                if change == "version":
                    server["version"] = "v0.1.1"
                    package["identifier"] = package["identifier"].replace(":v0.1.0@", ":v0.1.1@")
                elif change == "digest":
                    package["identifier"] = package["identifier"].replace("a" * 64, "b" * 64)
                elif change == "status":
                    listing["_meta"][validator.OFFICIAL_REGISTRY_METADATA]["status"] = "deprecated"
                else:
                    server["name"] = "io.github.other/carbon-panel"
                self.assertTrue(validator.verify_listing(listing, expected))


if __name__ == "__main__":
    unittest.main()
