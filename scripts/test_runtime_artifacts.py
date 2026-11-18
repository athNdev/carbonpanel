# SPDX-License-Identifier: MPL-2.0
"""Offline contract tests for the isolated runtime-artifact publication lane.

Every test is fixture-driven and runs without git, GitHub, a registry, a
hypervisor or host provisioning. No test writes outside a temporary directory.
"""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

from scripts import runtime_artifacts as ra

FIXTURES = Path(__file__).resolve().parent / "testdata" / "runtime-artifacts"
SHA = "a" * 40
VERSION = "runtime-v0.1.0"
ARTIFACT = "installer"


def fixture(name):
    return (FIXTURES / name).read_text(encoding="utf-8")


class VersionAndOwnershipTests(unittest.TestCase):
    def test_accepts_only_independent_runtime_versions(self):
        self.assertTrue(ra.valid_runtime_version(VERSION)[0])
        for bad in ("v1.0.0", "mod-v1.0.0", "runtime-v01.2.3",
                    "runtime-v0.0.0-verify.1", "runtime-v1.2", None, 12):
            with self.subTest(bad=bad):
                self.assertFalse(ra.valid_runtime_version(bad)[0])

    def test_owner_is_derived_from_repository_not_a_host_path(self):
        self.assertEqual(ra.project_owner("athNdev/carbonpanel"), "athndev")
        self.assertEqual(ra.project_owner("SomeOrg/Repo"), "someorg")
        with self.assertRaises(ValueError):
            ra.project_owner("bad owner/name")

    def test_no_app_release_domain(self):
        ra.assert_no_app_release(VERSION, ARTIFACT)
        for bad in ("v1.0.0", "mod-v2.0.0", "runtime-v0.0.0-verify.1"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                ra.assert_no_app_release(bad, ARTIFACT)
        with self.assertRaises(ValueError):
            ra.assert_no_app_release(VERSION, "vhdx")

    def test_build_script_allowlist(self):
        self.assertEqual(ra.build_script_for("qcow2"), "scripts/build-runtime-qcow2.sh")
        self.assertEqual(ra.build_script_for("installer"), "scripts/build-runtime-installer.sh")
        for bad in ("vhdx", "vdi", "vmdk", "ova", "bhyve"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                ra.build_script_for(bad)


class IdentityAssertionTests(unittest.TestCase):
    def test_valid_fixture_parses(self):
        fields = ra.parse_identity_assertion(fixture("identity-valid.txt"))
        self.assertEqual(fields["version"], VERSION)
        self.assertEqual(fields["artifact"], ARTIFACT)
        self.assertEqual(fields["commit"], SHA)

    def test_malformed_assertions_are_refused(self):
        base = fixture("identity-valid.txt")
        cases = {
            "unknown-header": base.replace(ra.IDENTITY_HEADER, "identity-v9"),
            "duplicate": base + "version: runtime-v9.9.9\n",
            "missing": base.replace("statement: Dispatched by the release operator for the verified installer bundle.\n", ""),
            "unknown-field": base + "secret_token: nope\n",
            "no-colon": base + "garbage-line\n",
            "empty-publisher": base.replace("publisher: release-bot", "publisher: "),
        }
        for name, text in cases.items():
            with self.subTest(case=name), self.assertRaises(ValueError):
                ra.parse_identity_assertion(text)

    def test_assertion_must_bind_dispatch_inputs(self):
        ok, reason, _ = ra.check_identity_assertion(
            fixture("identity-valid.txt"), VERSION, ARTIFACT, SHA, "athndev")
        self.assertTrue(ok, reason)
        cases = [
            (fixture("identity-valid.txt"), "runtime-v9.9.9", ARTIFACT, SHA, "athndev"),
            (fixture("identity-valid.txt"), VERSION, "qcow2", SHA, "athndev"),
            (fixture("identity-wrong-commit.txt"), VERSION, ARTIFACT, SHA, "athndev"),
            (fixture("identity-wrong-owner.txt"), VERSION, ARTIFACT, SHA, "athndev"),
        ]
        for text, version, artifact, commit, owner in cases:
            with self.subTest(case=(version, artifact, commit, owner)):
                self.assertFalse(ra.check_identity_assertion(
                    text, version, artifact, commit, owner)[0])


class TrustedMainTests(unittest.TestCase):
    def test_exact_sha_with_green_ci(self):
        ok, reason = ra.verify_main_ci(
            SHA, runs_json=[{"status": "completed", "conclusion": "success"}],
            ancestor_checker=lambda _: True, ci_reader=lambda _: [])
        self.assertTrue(ok, reason)

    def test_fail_closed_cases(self):
        ancestor = lambda _: True
        green = lambda _: [{"status": "completed", "conclusion": "success"}]
        broken_gh = lambda _: (_ for _ in ()).throw(OSError("no gh"))
        cases = [
            ("short-sha", "abc1234", ancestor, green),
            ("not-ancestor", SHA, lambda _: False, green),
            ("failed-ci", SHA, ancestor, lambda _: json.loads(fixture("ci-failed.json"))),
            ("missing-ci", SHA, ancestor, lambda _: []),
            ("ci-unavailable", SHA, ancestor, broken_gh),
        ]
        for name, commit, anc, reader in cases:
            with self.subTest(case=name):
                self.assertFalse(ra.verify_main_ci(
                    commit, ancestor_checker=anc, ci_reader=reader)[0])


class PublicationGateTests(unittest.TestCase):
    def gate(self, **overrides):
        args = {
            "event_name": "workflow_dispatch",
            "publish_requested": "true",
            "version": VERSION,
            "artifact": ARTIFACT,
            "trusted_sha": SHA,
            "identity_assertion": fixture("identity-valid.txt"),
            "ci_verified": "true",
            "ref": ra.TRUSTED_REF,
            "owner": "athndev",
        }
        args.update(overrides)
        return ra.publication_gate(**args)

    def test_happy_path_decision_is_actionable(self):
        allowed, reason, decision = self.gate()
        self.assertTrue(allowed, reason)
        self.assertEqual(decision["build_script"], ra.BUILD_SCRIPTS[ARTIFACT])
        self.assertEqual(decision["identity"]["publisher"], "release-bot")

    def test_every_write_condition_fails_closed(self):
        cases = {
            "push-event": {"event_name": "push"},
            "tag-ref": {"ref": "refs/tags/runtime-v0.1.0"},
            "feature-ref": {"ref": "refs/heads/codex/node-artifacts"},
            "no-opt-in": {"publish_requested": "false"},
            "app-version": {"version": "v1.0.0"},
            "module-version": {"version": "mod-v1.0.0"},
            "vhdx": {"artifact": "vhdx"},
            "vmdk": {"artifact": "vmdk"},
            "short-sha": {"trusted_sha": "abc1234"},
            "no-ci": {"ci_verified": "false"},
            "checkbox-ci": {"ci_verified": "maybe"},
            "bad-identity": {"identity_assertion": fixture("identity-wrong-commit.txt")},
        }
        for name, overrides in cases.items():
            with self.subTest(case=name):
                self.assertFalse(self.gate(**overrides)[0])

    def test_small_adr0033_shaped_boundary(self):
        self.assertTrue(ra.runtime_publication_allowed(
            "workflow_dispatch", "true", VERSION)[0])
        self.assertFalse(ra.runtime_publication_allowed("push", "true", VERSION)[0])
        self.assertFalse(ra.runtime_publication_allowed(
            "workflow_dispatch", "false", VERSION)[0])


class IntegrityRecordTests(unittest.TestCase):
    def test_checksum_manifest_is_deterministic_and_verifiable(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "b.bin").write_bytes(b"second artifact")
            (root / "a.bin").write_bytes(b"first artifact")
            paths = [root / "b.bin", root / "a.bin"]
            manifest = ra.checksum_manifest(paths, root=root)
            self.assertEqual(manifest, ra.checksum_manifest(paths[::-1], root=root))
            self.assertEqual(
                [line.split("  ")[1] for line in manifest.splitlines()],
                ["a.bin", "b.bin"])
            ok, problems = ra.verify_checksum_manifest(manifest, root)
            self.assertTrue(ok, problems)
            (root / "c.bin").write_bytes(b"unlisted")
            self.assertFalse(ra.verify_checksum_manifest(manifest, root)[0])
            (root / "c.bin").unlink()
            (root / "a.bin").write_bytes(b"tampered")
            ok, problems = ra.verify_checksum_manifest(manifest, root)
            self.assertFalse(ok)
            self.assertTrue(any("mismatch" in p for p in problems))

    def test_manifest_parser_refuses_malformed_rows(self):
        for bad in ("deadbeef  a.bin\n", "a" * 64 + " a.bin\n",
                    "a" * 64 + "  a.bin\n" + "b" * 64 + "  a.bin\n", ""):
            with self.subTest(bad=bad[:20]), self.assertRaises(ValueError):
                ra.parse_checksum_manifest(bad)

    def test_sbom_is_reproducible_and_schema_shaped(self):
        schema = json.loads((FIXTURES / "sbom.schema.json").read_text(encoding="utf-8"))
        self.assertEqual(schema["required"], [
            "bomFormat", "specVersion", "serialNumber", "version", "metadata", "components"])
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "disk.qcow2").write_bytes(b"qcow2-bytes")
            files = [root / "disk.qcow2"]
            doc = ra.build_sbom("runtime-v0.2.0", "qcow2", SHA, files, root=root)
            self.assertEqual(ra.sbom_json(doc), ra.sbom_json(ra.build_sbom(
                "runtime-v0.2.0", "qcow2", SHA, files, root=root)))
            for key in schema["required"]:
                self.assertIn(key, doc)
            self.assertEqual(doc["components"][0]["hashes"][0]["alg"], "SHA-256")
            ok, reason = ra.validate_sbom_document(doc, "runtime-v0.2.0", "qcow2", SHA)
            self.assertTrue(ok, reason)
            self.assertFalse(ra.validate_sbom_document(
                doc, "runtime-v0.3.0", "qcow2", SHA)[0])

    def test_signature_binding_fails_closed(self):
        manifest = "abc  file.bin\n"
        identity = {"publisher": "release-bot"}
        doc = ra.signature_binding(VERSION, ARTIFACT, SHA, manifest,
                                   "release-bot", "c2lnbmF0dXJl")
        self.assertTrue(ra.verify_signature_binding(doc, manifest, identity)[0])
        self.assertFalse(ra.verify_signature_binding(
            doc, "changed  file.bin\n", identity)[0])
        self.assertFalse(ra.verify_signature_binding(
            doc, manifest, {"publisher": "someone-else"})[0])
        empty = dict(doc, signature="")
        self.assertFalse(ra.verify_signature_binding(empty, manifest, identity)[0])

    def test_full_bundle_precondition_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "disk.qcow2").write_bytes(b"image-bytes")
            files = [root / "disk.qcow2"]
            manifest = ra.checksum_manifest(files, root=root)
            (root / "SHA256SUMS").write_text(manifest, encoding="utf-8")
            doc = ra.build_sbom(VERSION, "qcow2", SHA, files, root=root)
            (root / "sbom.cdx.json").write_text(ra.sbom_json(doc), encoding="utf-8")
            sig = ra.signature_binding(VERSION, "qcow2", SHA, manifest,
                                       "release-bot", "c2lnbmF0dXJl")
            (root / "signature.json").write_text(
                json.dumps(sig), encoding="utf-8")
            identity = {"publisher": "release-bot"}
            ok, problems = ra.verify_bundle(root, VERSION, "qcow2", SHA, identity)
            self.assertTrue(ok, problems)
            (root / "disk.qcow2").write_bytes(b"tampered")
            ok, problems = ra.verify_bundle(root, VERSION, "qcow2", SHA, identity)
            self.assertFalse(ok)
            self.assertTrue(any("mismatch" in p for p in problems))

    def test_no_host_paths_or_credentials_in_fixtures(self):
        for fixture_file in FIXTURES.glob("*"):
            text = fixture_file.read_text(encoding="utf-8", errors="replace")
            for forbidden in ("C:\\", "/home/", "/opt/carbon-panel", "password", "token="):
                with self.subTest(file=fixture_file.name, forbidden=forbidden):
                    self.assertNotIn(forbidden, text)


if __name__ == "__main__":
    unittest.main()
