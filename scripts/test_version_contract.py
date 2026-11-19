#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Fail-closed contract, classification and negative-case checks."""
import copy
import json
from pathlib import Path
import tempfile
import unittest

from scripts import version_contract as vc
from scripts.build_website import render_versions_page
from scripts.check_website import check_versions

ROOT = Path(__file__).resolve().parent.parent


class VersionContractTests(unittest.TestCase):
    def setUp(self):
        self.contract, self.catalog = vc.load(ROOT)

    def test_contract_validates_and_derives_three_documented_versions(self):
        vc.validate(self.contract, self.catalog, ROOT)
        index = vc.version_index(self.contract, self.catalog, ROOT)
        self.assertEqual([v["display"] for v in index["versions"]],
                         ["1.0.40", "1.0.39", "3.0.0-alpha.1"])
        self.assertEqual(index["stable"]["commit"],
                         "f88748be6a216b187e675bbb1f110df07e544148")
        default = [v for v in index["versions"] if v["default"]]
        self.assertEqual(len(default), 1)
        self.assertEqual(default[0]["route"], "/docs/")
        for version in index["versions"]:
            self.assertTrue(version["pages"])

    def test_channel_is_classification_not_v_prefix(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        by_tag = {v["tag"]: v for v in index["versions"]}
        # A v-prefixed historical tag is source-only with NO download CTA.
        self.assertEqual(by_tag["v1.0.39"]["channel"], "historical")
        self.assertFalse(by_tag["v1.0.39"]["cta"])
        self.assertEqual(by_tag["v3.0.0-alpha.1"]["channel"], "preview")
        self.assertEqual(by_tag["v1.0.40"]["channel"], "stable")

    def test_tag_id_disagreement_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["versions"][0]["commit"] = "e1f23829115a9a7e4ddc308e3919c1920a123cc2"
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("catalog peel", str(raised.exception))

    def test_git_peel_disagreement_fails_closed(self):
        with self.assertRaises(ValueError) as raised:
            vc.validate(self.contract, self.catalog, ROOT,
                        peel=lambda tag: "0" * 40)
        self.assertIn("Git tag peels", str(raised.exception))

    def test_channel_mismatch_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["versions"][1]["channel"] = "prerelease"
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("channel must be derived", str(raised.exception))

    def test_excluded_tags_fail_closed(self):
        for tag in ("v1.0.38", "v3.0.0-beta.4"):
            contract = copy.deepcopy(self.contract)
            contract["versions"].append({
                "tag": tag, "display": tag.removeprefix("v"),
                "classification": "carbonpanel-source-only",
                "channel": "historical", "availability": "source-only",
                "commit": "0" * 40, "source_commit": "0" * 40,
                "cta": False, "download_kind": "source",
                "download_url": "", "default": False,
            })
            with self.assertRaises(ValueError) as raised:
                vc.validate(contract, self.catalog, ROOT)
            self.assertIn("excluded tag", str(raised.exception))

    def test_rendered_versions_page_has_no_cta_for_source_only(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        page = render_versions_page(index, index["versions"][0])
        self.assertIn("releases/tag/v1.0.40", page)
        self.assertIn("source only, no download", page)
        self.assertIn("pkgs/container/carbonpanel", page)
        self.assertNotIn("v3.0.0-beta.4", page)

    def test_packaged_index_rejects_tampered_published_source(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "version-index.json").write_text(vc.render_index(index), encoding="utf-8")
            for version in index["versions"]:
                for route in (version["route"], version["immutable_route"]):
                    target = root / route.strip("/")
                    target.mkdir(parents=True, exist_ok=True)
                (target / "index.html").write_text(
                    '<html><head><title>v</title></head><body>'
                    + " ".join(v["display"] for v in index["versions"])
                    + '<nav data-version-switcher></nav></body></html>', encoding="utf-8")
                source = {"revision": version["commit"],
                          "source_commit": version["source_commit"]}
                (target / "source.json").write_text(json.dumps(source), encoding="utf-8")
                (target / "schemav1.yaml").write_text("x" * 2000, encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertEqual(errors, [])
            # Tamper with the published source identity: must fail closed.
            pinned = root / "docs/source.json"
            data = json.loads(pinned.read_text(encoding="utf-8"))
            data["revision"] = "e1f23829115a9a7e4ddc308e3919c1920a123cc2"
            pinned.write_text(json.dumps(data), encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertTrue(any("source.json disagrees" in item for item in errors))


if __name__ == "__main__":
    unittest.main()
