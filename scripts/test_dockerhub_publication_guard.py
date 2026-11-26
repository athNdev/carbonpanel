#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for the OSS-433 public Stable publication guard."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts import dockerhub_description as description
from scripts import dockerhub_description as hub
from scripts import release as release_contract

ROOT = Path(__file__).resolve().parent.parent

LEGACY = {
    "version": "1.0.40", "tag": "v1.0.40",
    "commit": "f88748be6a216b187e675bbb1f110df07e544148",
    "digest": "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001",
    "image_tag": "latest", "source": "fixture",
}
NEXT = {
    "version": "2.0.1", "tag": "carbonpanel-v2.0.1",
    "commit": "1" * 40, "digest": "sha256:" + "2" * 64,
    "image_tag": "latest", "source": "fixture",
}
ADVANCED = {
    "version": "3.0.0", "tag": "carbonpanel-v3.0.0",
    "commit": "3" * 40, "digest": "sha256:" + "4" * 64,
    "image_tag": "latest", "source": "fixture",
}


def manifest_for(stable):
    return {
        "version": release_contract.version_from_release_tag(stable["tag"]),
        "commit": stable["commit"],
        "revision": stable["commit"],
        "image_digest": stable["digest"],
        "git_tag": stable["tag"],
    }


def releases_pages(stable, draft=False, include=True, prerelease=False, manifest=None):
    if not include:
        return [[]]
    record = {
        "tag_name": stable["tag"],
        "draft": draft,
        "prerelease": prerelease,
        "assets": [{"name": "release-manifest.json", "id": 42}],
    }
    if manifest is not None:
        record["manifest"] = manifest
    return [[record]]


def observations(stable, digest=None, commit=None):
    info = {
        "digest": digest or stable["digest"],
        "revision": commit or stable["commit"],
        "platforms": [["linux", "amd64"], ["linux", "arm64"]],
    }
    return {"docker_hub": ["present", dict(info)], "ghcr": ["present", dict(info)]}


def write_candidate_index(path, stable):
    """Offline published docs index for an exact Stable candidate."""
    path.write_text(json.dumps({
        "stable": {"tag": stable["tag"], "version": stable["version"],
                   "commit": stable["commit"], "image_tag": "latest",
                   "digest": stable["digest"]},
        "channels": {"stable": {
            "tag": stable["tag"], "version": stable["version"],
            "commit": stable["commit"],
            "promotion": {"state": "published", "promoted": True},
            "image": {"tag": "latest", "digest": stable["digest"],
                      "revision": stable["commit"]},
        }},
    }), encoding="utf-8")
    return path


def fake_github(payload):
    def call(*args):
        if any("/releases/assets/" in str(arg) for arg in args):
            return json.dumps(payload)
        return "[]"
    return call


class PublicationGuardTests(unittest.TestCase):
    def prove(self, stable, releases=None, observed=None, draft=False, include=True,
              prerelease=False):
        payload = manifest_for(stable)
        with patch.object(release_contract, "github", fake_github(payload)):
            return description.prove_stable_publication(
                stable,
                releases_json=releases if releases is not None else releases_pages(
                    stable, draft=draft, include=include, prerelease=prerelease,
                    manifest=payload),
                observations=observed if observed is not None else observations(stable))

    def test_matching_legacy_1_0_40_baseline_is_verified(self):
        ok, reason, evidence = self.prove(LEGACY)
        self.assertTrue(ok, reason)
        self.assertEqual(evidence["manifest_version"], "v1.0.40")
        self.assertIn("both registries", reason)

    def test_fully_promoted_product_release_is_verified(self):
        ok, reason, _ = self.prove(NEXT)
        self.assertTrue(ok, reason)

    def test_candidate_docs_while_aliases_are_old_refuses(self):
        ok, reason, _ = self.prove(NEXT, observed=observations(LEGACY))
        self.assertFalse(ok)
        self.assertIn("channel parity", reason)

    def test_matching_aliases_with_draft_release_refuses(self):
        ok, reason, _ = self.prove(NEXT, draft=True)
        self.assertFalse(ok)
        self.assertIn("draft", reason)

    def test_prerelease_record_with_final_tag_refuses(self):
        ok, reason, _ = self.prove(NEXT, prerelease=True)
        self.assertFalse(ok)
        self.assertIn("prerelease", reason)

    def test_missing_draft_or_prerelease_flag_is_refused(self):
        with tempfile.TemporaryDirectory() as folder:
            payload = manifest_for(NEXT)
            for field in ("draft", "prerelease"):
                pages = releases_pages(NEXT, manifest=payload)
                del pages[0][0][field]
                with patch.object(release_contract, "github", fake_github(payload)):
                    ok, reason, _ = description.prove_stable_publication(
                        NEXT, releases_json=pages, observations=observations(NEXT))
                self.assertFalse(ok, field + " must not be assumed false")
                self.assertIn(field + " flag", reason)

    def test_rc_tag_marked_public_is_refused(self):
        rc = dict(NEXT, tag="carbonpanel-v2.0.1-rc.1", version="2.0.1-rc.1")
        ok, reason, _ = self.prove(rc)
        self.assertFalse(ok)
        self.assertIn("prerelease", reason)

    def test_generator_emits_stable_identity_to_workflow_outputs(self):
        import os
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            outputs = root / "outputs"
            legacy_index = write_candidate_index(root / "legacy-index.json", LEGACY)
            args = hub.build_parser().parse_args([
                "generate", "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                "--readme", str(ROOT / "README.md"),
                "--version-index", str(legacy_index),
                "--skip-live-index", "--skip-publication-guard",
                "--output", str(root / "body.md"),
            ])
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(outputs)}):
                hub.command_generate(args)
            written = dict(line.split("=", 1) for line in
                           outputs.read_text(encoding="utf-8").splitlines() if "=" in line)
            for key, expected in (("stable_version", LEGACY["version"]),
                                  ("stable_commit", LEGACY["commit"]),
                                  ("stable_digest", LEGACY["digest"])):
                self.assertTrue(written.get(key), key + " must be emitted and nonempty")
                self.assertEqual(written[key], expected)

    def test_workflow_write_guards_use_generator_stable_outputs(self):
        import yaml
        workflow = ROOT / ".github" / "workflows" / "dockerhub-description.yml"
        document = yaml.safe_load(workflow.read_text(encoding="utf-8"))
        steps = document["jobs"]["update"]["steps"]
        guarded = [step for step in steps if "assert-publication-identity" in
                   " ".join(step.get("run", "").split())]
        self.assertEqual(len(guarded), 2)
        for step in guarded:
            self.assertIn("${{ steps.generated.outputs.stable_version }}", step["run"])
            self.assertIn("${{ steps.generated.outputs.stable_commit }}", step["run"])
            self.assertIn("${{ steps.generated.outputs.stable_digest }}", step["run"])

    def test_manifest_fixture_is_truly_offline(self):
        payload = manifest_for(NEXT)
        def no_network(*args):
            raise AssertionError("fixture guard must not call gh: " + " ".join(map(str, args)))
        with patch.object(release_contract, "github", no_network):
            ok, reason, _ = description.prove_stable_publication(
                NEXT,
                releases_json=releases_pages(NEXT, manifest=payload),
                observations=observations(NEXT))
        self.assertTrue(ok, reason)

    def test_write_boundary_rejects_changed_live_identity_or_alias_parity(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            live = write_candidate_index(root / "live.json", NEXT)
            baseline = write_candidate_index(root / "baseline.json", LEGACY)
            releases = root / "releases.json"
            releases.write_text(json.dumps(releases_pages(
                NEXT, manifest=manifest_for(NEXT))), encoding="utf-8")
            observed = root / "observations.json"
            observed.write_text(json.dumps(observations(NEXT)), encoding="utf-8")
            def boundary(expected_version="2.0.1"):
                return hub.build_parser().parse_args([
                    "assert-publication-identity",
                    "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                    "--version-index", str(baseline),
                    "--live-index", str(live),
                    "--releases-fixture", str(releases),
                    "--observations-fixture", str(observed),
                    "--expected-version", expected_version,
                    "--expected-commit", NEXT["commit"],
                    "--expected-digest", NEXT["digest"],
                ])
            with patch.object(release_contract, "github",
                              fake_github(manifest_for(NEXT))):
                self.assertEqual(description.command_assert_publication_identity(
                    boundary()), 0)
                with self.assertRaises(hub.DescriptionError) as raised:
                    description.command_assert_publication_identity(boundary("1.0.40"))
                self.assertIn("write boundary", str(raised.exception))
            stale = root / "stale-observations.json"
            stale.write_text(json.dumps(observations(LEGACY)), encoding="utf-8")
            with patch.object(release_contract, "github",
                              fake_github(manifest_for(NEXT))):
                args = boundary()
                args.observations_fixture = str(stale)
                with self.assertRaises(hub.DescriptionError) as raised:
                    description.command_assert_publication_identity(args)
                self.assertIn("parity", str(raised.exception))

    def test_skip_guard_requires_offline_source_before_any_live_fetch(self):
        with tempfile.TemporaryDirectory() as folder:
            args = hub.build_parser().parse_args([
                "generate", "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                "--readme", str(ROOT / "README.md"),
                "--skip-publication-guard", "--output", str(Path(folder) / "body.md"),
            ])
            with patch.object(description, "fetch_live_index",
                              side_effect=AssertionError("live fetch must not occur")):
                with self.assertRaises(hub.DescriptionError) as raised:
                    description.command_generate(args)
            self.assertIn("offline-only", str(raised.exception))

    def test_matching_aliases_without_release_refuses(self):
        ok, reason, _ = self.prove(NEXT, include=False)
        self.assertFalse(ok)
        self.assertIn("no public release record", reason)

    def test_provider_failure_refuses(self):
        ok, reason, _ = self.prove(NEXT, observed={
            "docker_hub": ["present", observations(NEXT)["docker_hub"][1]],
            "ghcr": ["unknown", None],
        })
        self.assertFalse(ok)
        self.assertIn("ghcr", reason)
        ok, reason, _ = self.prove(NEXT, observed=observations(LEGACY, commit=NEXT["commit"]))
        self.assertFalse(ok)
        self.assertIn("digest", reason)

    def test_real_generate_preserves_existing_body_on_draft_release(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            candidate = write_candidate_index(root / "candidate-index.json", NEXT)
            baseline = write_candidate_index(root / "baseline.json", LEGACY)
            releases = root / "releases.json"
            observed = root / "observations.json"
            releases.write_text(json.dumps(releases_pages(
                NEXT, draft=True, manifest=manifest_for(NEXT))), encoding="utf-8")
            observed.write_text(json.dumps(observations(NEXT)), encoding="utf-8")
            output = root / "body.md"
            sentinel = "last published body preserved\n"
            output.write_text(sentinel, encoding="utf-8")
            args = hub.build_parser().parse_args([
                "generate", "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                "--readme", str(ROOT / "README.md"),
                "--version-index", str(baseline),
                "--live-index", str(candidate),
                "--releases-fixture", str(releases),
                "--observations-fixture", str(observed),
                "--output", str(output),
            ])
            with patch.object(release_contract, "github",
                              fake_github(manifest_for(NEXT))):
                with self.assertRaises(hub.DescriptionError) as raised:
                    hub.command_generate(args)
            self.assertIn("release record: release draft flag is not explicitly false",
                          str(raised.exception))
            self.assertIn("preserving the last published description", str(raised.exception))
            self.assertEqual(output.read_text(encoding="utf-8"), sentinel)

    def test_explicit_offline_fixture_mode_bypasses_guard(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            output = root / "body.md"
            legacy_index = write_candidate_index(root / "legacy-index.json", LEGACY)
            result = description.command_generate(hub.build_parser().parse_args([
                "generate", "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                "--readme", str(ROOT / "README.md"),
                "--version-index", str(legacy_index),
                "--skip-live-index", "--skip-publication-guard",
                "--output", str(output),
            ]))
            self.assertIsNone(result)
            self.assertIn("`" + LEGACY["version"] + "`", output.read_text(encoding="utf-8"))

    def test_synthetic_advanced_checkout_stable_generates_and_verifies(self):
        """Prove fixtures work when the checkout index has advanced well beyond legacy."""
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            advanced_index = write_candidate_index(root / "advanced.json", ADVANCED)
            releases = root / "releases.json"
            observed = root / "observations.json"
            releases.write_text(json.dumps(releases_pages(
                ADVANCED, manifest=manifest_for(ADVANCED))), encoding="utf-8")
            observed.write_text(json.dumps(observations(ADVANCED)), encoding="utf-8")
            outputs = root / "outputs"
            import os
            args = hub.build_parser().parse_args([
                "generate", "--config", str(ROOT / "docs" / "dockerhub-description.json"),
                "--readme", str(ROOT / "README.md"),
                "--version-index", str(advanced_index),
                "--live-index", str(advanced_index),
                "--releases-fixture", str(releases),
                "--observations-fixture", str(observed),
                "--output", str(root / "body.md"),
            ])
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(outputs)}), \
                    patch.object(release_contract, "github",
                                 fake_github(manifest_for(ADVANCED))):
                self.assertIsNone(hub.command_generate(args))
            written = dict(line.split("=", 1) for line in
                           outputs.read_text(encoding="utf-8").splitlines() if "=" in line)
            self.assertEqual(written["stable_version"], ADVANCED["version"])
            self.assertEqual(written["stable_commit"], ADVANCED["commit"])
            self.assertEqual(written["stable_digest"], ADVANCED["digest"])
            body = (root / "body.md").read_text(encoding="utf-8")
            self.assertIn("`" + ADVANCED["version"] + "`", body)
            self.assertNotIn("`" + LEGACY["version"] + "`", body)


if __name__ == "__main__":
    unittest.main()
