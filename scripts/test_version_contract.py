#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Fail-closed channel, marker and negative-case checks."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from scripts import version_contract as vc
from scripts.build_website import render_versions_page
from scripts.check_website import check_channels, check_marker, check_versions

ROOT = Path(__file__).resolve().parent.parent
STABLE_SHA = "f88748be6a216b187e675bbb1f110df07e544148"
NIGHTLY_SHA = "d8597d4e953522d2eb2fa6d4764965ae8e58686f"


class VersionContractTests(unittest.TestCase):
    def setUp(self):
        self.contract, self.catalog = vc.load(ROOT)

    def test_exact_stable_render_input_sets_version_source_download_and_schema(self):
        env = {"CARBONPANEL_STABLE_TAG": "carbonpanel-v2.0.1",
               "CARBONPANEL_STABLE_REVISION": NIGHTLY_SHA,
               "CARBONPANEL_STABLE_DIGEST": "sha256:" + "1" * 64}
        qualified = vc.release_build_contract(self.contract, env)
        index = vc.version_index(qualified, self.catalog)
        stable = index["versions"][0]
        self.assertEqual(stable["schema_version_label"], "2.0.1")
        self.assertEqual(stable["immutable_route"], "/docs/versions/2.0.1/")
        self.assertEqual(stable["commit"], NIGHTLY_SHA)
        self.assertTrue(stable["download_url"].endswith("/tag/carbonpanel-v2.0.1"))
        self.assertEqual(index["channels"]["nightly"], self.contract["channels"]["nightly"])
        self.assertEqual(self.contract["channels"]["stable"]["commit"], STABLE_SHA)
        for invalid in ({"CARBONPANEL_STABLE_TAG": "carbonpanel-v2.0.0"},
                        {"CARBONPANEL_STABLE_TAG": "carbonpanel-v2.0.1-beta.1"},
                        {"CARBONPANEL_STABLE_DIGEST": ""}):
            with self.assertRaises(ValueError):
                vc.release_build_contract(self.contract, {**env, **invalid})

    def test_contract_publishes_exactly_two_channels(self):
        vc.validate(self.contract, self.catalog, ROOT)
        index = vc.version_index(self.contract, self.catalog, ROOT)
        self.assertEqual(list(index["channels"]), ["stable", "nightly"])
        self.assertEqual([v["display"] for v in index["versions"]], ["1.0.40", "Nightly"])
        self.assertEqual([v["channel_display"] for v in index["versions"]], ["Stable", "Nightly"])
        self.assertEqual([v["route"] for v in index["versions"]],
                         ["/docs/", "/docs/nightly/"])
        self.assertEqual(index["channels"]["stable"]["image"]["digest"],
                         "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001")
        self.assertEqual(index["channels"]["nightly"]["image"]["digest"],
                         "sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce5e96d00fa1c64239c965a9302d")
        defaults = [v for v in index["versions"] if v["default"]]
        self.assertEqual(len(defaults), 1)
        self.assertEqual(defaults[0]["route"], "/docs/")
        for version in index["versions"]:
            self.assertTrue(version["pages"])

    def test_tracked_default_never_claims_a_promoted_nightly(self):
        nightly = self.contract["channels"]["nightly"]
        self.assertFalse(nightly["promotion"]["promoted"])
        self.assertEqual(nightly["promotion"]["state"], "baseline")
        index = vc.version_index(self.contract, self.catalog, ROOT)
        self.assertFalse(index["promotion"]["promoted"])
        marker = vc.channel_marker(index)
        self.assertFalse(marker["channels"]["nightly"]["promoted"])
        self.assertEqual(marker["channels"]["nightly"]["revision"], NIGHTLY_SHA)
        for name, entry in marker["channels"].items():
            self.assertEqual(entry["pinned_url"],
                             f"{index['repository']}/commit/{entry['revision']}")

    def test_external_promotion_input_is_exact_and_in_memory(self):
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        digest = "sha256:" + "b" * 64
        promotion = {"revision": revision, "digest": digest,
                     "state": "candidate", "receipt": "OSS-413-test-1"}
        promoted = vc.apply_promotion(self.contract, promotion)
        self.assertEqual(self.contract["channels"]["nightly"]["commit"], NIGHTLY_SHA)
        vc.validate(promoted, self.catalog, ROOT)
        index = vc.version_index(promoted, self.catalog, ROOT)
        marker = vc.channel_marker(index)
        nightly = marker["channels"]["nightly"]
        self.assertEqual(nightly["revision"], revision)
        self.assertEqual(nightly["digest"], digest)
        self.assertEqual(nightly["state"], "candidate")
        self.assertTrue(nightly["promoted"])
        self.assertEqual(nightly["pinned_url"], f"{index['repository']}/commit/{revision}")
        self.assertEqual(vc.version_index(self.contract, self.catalog, ROOT)["promotion"]["promoted"],
                         False)
        for bad in ({"revision": "main", "digest": digest, "state": "candidate", "receipt": "r"},
                    {"revision": revision, "digest": "sha256:xyz", "state": "candidate", "receipt": "r"},
                    {"revision": revision, "digest": digest, "state": "candidate", "receipt": ""},
                    {"revision": revision, "digest": digest, "state": "unknown", "receipt": "r"}):
            with self.assertRaises(ValueError):
                vc.apply_promotion(self.contract, bad)

    def test_promoted_nightly_requires_receipt_and_fresh_digest(self):
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        contract = vc.apply_promotion(self.contract, {
            "revision": revision,
            "digest": "sha256:" + "b" * 64,
            "state": "final", "receipt": "OSS-413-test-2"})
        vc.validate(contract, self.catalog, ROOT)
        missing = copy.deepcopy(contract)
        missing["channels"]["nightly"]["promotion"].pop("receipt")
        with self.assertRaises(ValueError) as raised:
            vc.validate(missing, self.catalog, ROOT)
        self.assertIn("receipt", str(raised.exception))
        stale = copy.deepcopy(contract)
        stale["channels"]["nightly"]["image"]["digest"] = vc.BOOTSTRAP_BASELINE["digest"]
        with self.assertRaises(ValueError) as raised:
            vc.validate(stale, self.catalog, ROOT)
        self.assertIn("stale nightly baseline digest", str(raised.exception))

    def test_candidate_artifact_uses_non_active_staging_path(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        staging = vc.candidate_route(NIGHTLY_SHA)
        self.assertEqual(staging, f"/docs/candidate/{NIGHTLY_SHA}/")
        with self.assertRaises(ValueError):
            vc.candidate_route("main")
        expect = {"revision": NIGHTLY_SHA, "digest": "sha256:" + "c" * 64,
                  "state": "candidate", "receipt": "OSS-413-candidate"}
        promoted = vc.apply_promotion(self.contract, expect)
        candidate = vc.candidate_index(
            vc.version_index(promoted, self.catalog, ROOT), NIGHTLY_SHA, expect["receipt"])
        marker = vc.channel_marker(candidate)
        self.assertEqual(marker["channels"]["nightly"]["route"], staging)
        self.assertEqual(marker["channels"]["nightly"]["revision"], NIGHTLY_SHA)
        self.assertEqual(marker["promotion"]["state"], "candidate")
        self.assertEqual(marker["channels"]["stable"]["route"], "/docs/")
        self.assertEqual(marker["channels"]["stable"]["pinned_url"],
                         f"{index['repository']}/commit/{STABLE_SHA}")
        nightly_versions = [v for v in candidate["versions"] if v["channel"] == "nightly"]
        self.assertEqual(nightly_versions[0]["route"], staging)
        self.assertNotIn(vc.CHANNEL_ROUTES["nightly"],
                         [v["route"] for v in candidate["versions"] if v["channel"] == "nightly"])

    def test_candidate_marker_fails_closed_without_exact_input(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        expect = {"revision": NIGHTLY_SHA, "digest": "sha256:" + "c" * 64,
                  "state": "candidate", "receipt": "OSS-413-candidate"}
        promoted = vc.apply_promotion(self.contract, expect)
        candidate = vc.candidate_index(
            vc.version_index(promoted, self.catalog, ROOT), NIGHTLY_SHA, expect["receipt"])
        errors = []
        check_channels(candidate, errors, expect)
        self.assertEqual(errors, [])
        errors = []
        check_channels(candidate, errors)
        self.assertTrue(any("route must be" in item and "/docs/nightly/" in item
                            for item in errors))
        self.assertTrue(any("promotion claim disagrees" in item for item in errors))
        with tempfile.TemporaryDirectory() as temporary:
            site = Path(temporary)
            (site / "channel-marker.json").write_text(
                json.dumps(vc.channel_marker(candidate)), encoding="utf-8")
            self.assertEqual(check_marker(site, candidate, expect), [])
            self.assertTrue(any("state is not the qualified final" in item
                                for item in check_marker(site, candidate, {
                                    "revision": NIGHTLY_SHA,
                                    "digest": expect["digest"], "state": "final"})))

    def test_registry_refs_are_recorded_for_both_channels(self):
        for name in vc.CHANNEL_NAMES:
            image = self.contract["channels"][name]["image"]
            self.assertEqual(image["docker_hub"],
                             f"atharvnaphade/carbonpanel:{image['tag']}")
            self.assertEqual(image["ghcr"],
                             f"ghcr.io/athndev/carbonpanel:{image['tag']}")
            self.assertEqual(image["revision"], self.contract["channels"][name]["commit"])

    def test_legacy_snapshots_are_not_published(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        serialized = json.dumps(index)
        for retired in ("1.0.39", "3.0.0-alpha.1"):
            self.assertNotIn(retired, serialized)
        # The classified catalog and its historical records stay intact.
        catalog_text = (ROOT / "docs/release-history.json").read_text(encoding="utf-8")
        for tag in ("v1.0.39", "v3.0.0-alpha.1"):
            self.assertIn(tag, catalog_text)

    def test_extra_channel_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["channels"]["preview"] = copy.deepcopy(contract["channels"]["nightly"])
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("exactly stable and nightly", str(raised.exception))

    def test_missing_digest_or_revision_fails_closed(self):
        for field in ("digest", "revision"):
            contract = copy.deepcopy(self.contract)
            contract["channels"]["nightly"]["image"].pop(field)
            with self.assertRaises(ValueError) as raised:
                vc.validate(contract, self.catalog, ROOT)
            self.assertIn(field, str(raised.exception))

    def test_mismatched_marker_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["channels"]["nightly"]["image"]["revision"] = "0" * 40
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("revision", str(raised.exception))

    def test_stale_nightly_revision_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["channels"]["nightly"]["commit"] = "1" * 40
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("bootstrap nightly commit", str(raised.exception))

    def test_stale_baseline_digest_cannot_be_relabeled(self):
        contract = copy.deepcopy(self.contract)
        nightly = contract["channels"]["nightly"]
        nightly["image"].pop("bootstrap")
        nightly["commit"] = "1" * 40
        nightly["image"]["revision"] = nightly["commit"]
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("stale nightly baseline digest", str(raised.exception))

    def test_non_hex_commit_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["channels"]["stable"]["commit"] = "main"
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("40-hex", str(raised.exception))

    def test_retired_display_and_route_fail_closed(self):
        for field, value in (("display", "1.0.39"), ("route", "/docs/versions/1.0.39/")):
            contract = copy.deepcopy(self.contract)
            contract["channels"]["nightly"][field] = value
            with self.assertRaises(ValueError) as raised:
                vc.validate(contract, self.catalog, ROOT)
            self.assertIn("retired", str(raised.exception))

    def test_tag_id_disagreement_fails_closed(self):
        contract = copy.deepcopy(self.contract)
        contract["channels"]["stable"]["commit"] = "e1f23829115a9a7e4ddc308e3919c1920a123cc2"
        with self.assertRaises(ValueError) as raised:
            vc.validate(contract, self.catalog, ROOT)
        self.assertIn("catalog", str(raised.exception))

    def test_git_peel_disagreement_fails_closed(self):
        with self.assertRaises(ValueError) as raised:
            vc.validate(self.contract, self.catalog, ROOT,
                        peel=lambda tag: "0" * 40)
        self.assertIn("Git tag peels", str(raised.exception))

    def test_excluded_tags_fail_closed(self):
        for tag in ("v1.0.38", "v3.0.0-beta.4"):
            contract = copy.deepcopy(self.contract)
            contract["channels"]["nightly"]["tag"] = tag
            with self.assertRaises(ValueError) as raised:
                vc.validate(contract, self.catalog, ROOT)
            self.assertIn("nightly.tag must be null", str(raised.exception))
        # The legacy bump helper must not reintroduce a retired snapshot.
        for tag in ("v1.0.39", "v3.0.0-alpha.1"):
            self.assertNotIn(tag, [self.contract["channels"]["stable"]["tag"],
                                   self.contract["channels"]["nightly"]["tag"]])

    def test_switcher_hrefs_are_canonical(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        stable, nightly = index["versions"]
        self.assertEqual(vc.switcher_href(stable, ""), "/docs/")
        self.assertEqual(vc.switcher_href(nightly, ""), "/docs/nightly/")
        slug = "getting-started/docker-compose"
        self.assertEqual(vc.switcher_href(stable, slug), f"/docs/{slug}/")
        self.assertEqual(vc.switcher_href(nightly, slug), f"/docs/nightly/{slug}/")
        self.assertEqual(vc.switcher_href(stable, "not-a-page"), "/docs/")

    def test_rendered_versions_page_has_no_legacy_snapshots(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        page = render_versions_page(index, index["versions"][0])
        self.assertIn("releases/tag/v1.0.40", page)
        self.assertIn(f"/commit/{STABLE_SHA}", page)
        self.assertIn("rolling `nightly` image", page)
        self.assertNotIn("1.0.39", page)
        self.assertNotIn("3.0.0-alpha.1", page)
        self.assertNotIn("v3.0.0-beta.4", page)

    def test_packaged_index_rejects_tampered_source_and_stale_channel(self):
        index = vc.version_index(self.contract, self.catalog, ROOT)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            shared = vc.render_index(index)
            (root / "version-index.json").write_text(shared, encoding="utf-8")
            (root / "docs").mkdir(parents=True, exist_ok=True)
            (root / "docs" / "version-index.json").write_text(shared, encoding="utf-8")
            for version in index["versions"]:
                for route in (version["route"], version["immutable_route"]):
                    target = root / route.strip("/")
                    target.mkdir(parents=True, exist_ok=True)
                    (target / "index.html").write_text(
                        '<html><head><title>v</title></head><body>'
                        + " ".join(v["channel_display"] for v in index["versions"])
                        + '<select data-version-switcher></select></body></html>', encoding="utf-8")
                    source = {"revision": version["commit"],
                              "source_commit": version["source_commit"]}
                    (target / "source.json").write_text(json.dumps(source), encoding="utf-8")
                    (target / "schemav1.yaml").write_text(
                        "openapi: 3.1.0\ninfo:\n  title: Carbon Panel API\n"
                        f"  version: {version['schema_version_label']}\npaths:\n"
                        "  /carbonpanel.v1.ActivityService/List:\n    post: {}\n",
                        encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertEqual(errors, [])
            check_channels(index, errors)
            self.assertEqual(errors, [])
            # Tamper with the published source identity: must fail closed.
            pinned = root / "docs/source.json"
            data = json.loads(pinned.read_text(encoding="utf-8"))
            data["revision"] = "e1f23829115a9a7e4ddc308e3919c1920a123cc2"
            pinned.write_text(json.dumps(data), encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertTrue(any("source.json disagrees" in item for item in errors))
            # A stale nightly revision in the published index must fail closed.
            stale = copy.deepcopy(index)
            stale["channels"]["nightly"]["commit"] = "1" * 40
            errors = []
            check_channels(stale, errors)
            self.assertTrue(any("stale nightly revision" in item for item in errors))
            # Restore the pin, then prove the documented /docs/ index URL is
            # fail-closed: a missing or drifted copy is not a valid publication.
            pinned.write_text(json.dumps({"revision": index["stable"]["commit"],
                                         "source_commit": index["stable"]["commit"]}),
                              encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertEqual(errors, [])
            docs_index = root / "docs" / "version-index.json"
            docs_index.unlink()
            errors = []
            check_versions(root, index, errors)
            self.assertTrue(any("docs/version-index.json: missing" in item for item in errors))
            docs_index.write_text("{}", encoding="utf-8")
            errors = []
            check_versions(root, index, errors)
            self.assertTrue(any("docs/version-index.json: disagrees" in item for item in errors))


if __name__ == "__main__":
    unittest.main()
