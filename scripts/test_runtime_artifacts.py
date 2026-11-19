# SPDX-License-Identifier: MPL-2.0
"""Offline contract tests for the isolated runtime-artifact publication lane.

Every test is fixture-driven and runs without git, GitHub, a registry, a
hypervisor or host provisioning. No test writes outside a temporary directory.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import shutil
import subprocess
import sys
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


def make_bundle(root, artifact, *, payload_bytes=8192, marker=None,
                provenance=True, synthetic_provenance=False, fixture_only=False):
    """Build a synthetic, fully-integrated staged bundle for predicate tests.

    Everything here is a temporary record: no host, hypervisor, network, cosign
    or release operation is performed. ``marker`` is an optional
    BUILD-PREFLIGHT.json object; ``provenance``/``synthetic_provenance`` control
    the provenance record kind.
    """
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    suffix = {"qcow2": ".qcow2", "lxc-rootfs": ".tar.xz", "installer": ".sh"}[artifact]
    payload = root / ("disk" + suffix)
    payload.write_bytes(b"Q" * payload_bytes)
    files = [payload]
    if provenance:
        document = {
            "schema": "carbonpanel.node.provenance/v1",
            "profile": "artifact",
            "synthetic": synthetic_provenance,
            "images": [{"arch": "amd64", "expected_sha256": "a" * 64}],
        }
        (root / "provenance.json").write_text(
            json.dumps(document), encoding="utf-8")
        files.append(root / "provenance.json")
    if marker is not None:
        (root / "BUILD-PREFLIGHT.json").write_text(
            json.dumps(marker), encoding="utf-8")
        files.append(root / "BUILD-PREFLIGHT.json")
    if fixture_only:
        (root / "build-plan.json").write_text(
            json.dumps({"mode": "offline", "fixture_only": True, "no_qcow2_built": True}),
            encoding="utf-8")
        files.append(root / "build-plan.json")
    manifest = ra.checksum_manifest(files, root=root)
    (root / "SHA256SUMS").write_text(manifest, encoding="utf-8")
    sbom = ra.build_sbom(VERSION, artifact, SHA, files, root=root)
    (root / "sbom.cdx.json").write_text(ra.sbom_json(sbom), encoding="utf-8")
    oidc = ra.expected_certificate_identity()
    signature = ra.signature_binding(VERSION, artifact, SHA, manifest, oidc, "c2lnbmF0dXJl")
    (root / "signature.json").write_text(json.dumps(signature), encoding="utf-8")
    (root / "signature.sig").write_bytes(b"c2lnbmF0dXJl")
    (root / "signature.bundle").write_bytes(b"{}")
    return files


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


class BuildStepReachabilityTests(unittest.TestCase):
    """Offline fixture: the build step resolves real scripts and fails closed.

    Nothing here executes a wrapper, QEMU, LXC, systemd, a network call or a
    release. Resolution is a read-only file-exists check against the repository
    (or an injected temporary root for the negative cases).
    """

    def setUp(self):
        self.fixture = json.loads(
            fixture("build-delegates.fixture.json"))
        self.assertNotEqual(
            set(self.fixture["wrappers"]), set())
        self.assertEqual(
            set(self.fixture["wrappers"]), set(ra.VERIFIED_ARTIFACTS))
        self.assertEqual(
            set(self.fixture["delegates"]), set(ra.VERIFIED_ARTIFACTS))
        self.assertEqual(set(ra.BUILD_SCRIPTS), set(ra.VERIFIED_ARTIFACTS))
        self.assertEqual(set(ra.BUILD_DELEGATES), set(ra.VERIFIED_ARTIFACTS))

    def test_allowlisted_wrappers_resolve_to_real_main_entry_points(self):
        for artifact, wrapper in self.fixture["wrappers"].items():
            delegate = self.fixture["delegates"][artifact]
            with self.subTest(artifact=artifact):
                self.assertEqual(ra.build_script_for(artifact), wrapper)
                self.assertEqual(ra.resolve_build_delegate(artifact), delegate)
                self.assertTrue((ra.ROOT / wrapper).is_file(), wrapper)
                self.assertTrue((ra.ROOT / delegate).is_file(), delegate)

    def test_preflight_proves_reachability_without_execution(self):
        for artifact in ra.VERIFIED_ARTIFACTS:
            with self.subTest(artifact=artifact):
                ok, reason, decision = ra.preflight_build(artifact)
                self.assertTrue(ok, reason)
                self.assertTrue(decision["build_script_exists"])
                self.assertTrue(decision["delegate_exists"])
                self.assertEqual(decision["build_script"],
                                 self.fixture["wrappers"][artifact])
                self.assertEqual(decision["delegate"],
                                 self.fixture["delegates"][artifact])

    def test_cli_preflight_is_reachable_offline(self):
        for artifact in ra.VERIFIED_ARTIFACTS:
            with self.subTest(artifact=artifact):
                out = io.StringIO()
                with contextlib.redirect_stdout(out):
                    code = ra.main(["preflight-build", "--artifact", artifact])
                self.assertEqual(code, 0, out.getvalue())
                payload = json.loads(out.getvalue())
                self.assertEqual(payload["reachable"], "true")
                self.assertEqual(payload["decision"]["delegate"],
                                 self.fixture["delegates"][artifact])

    def test_unknown_artifact_fails_closed(self):
        for bad in ("vhdx", "vdi", "vmdk", "ova", "bhyve", "vhd", "", None):
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError):
                    ra.build_script_for(bad)
                with self.assertRaises(ValueError):
                    ra.resolve_build_delegate(bad)
                ok, _, _ = ra.preflight_build(bad)
                self.assertFalse(ok)
                out = io.StringIO()
                with contextlib.redirect_stdout(out):
                    code = ra.main(["preflight-build", "--artifact", str(bad)])
                self.assertEqual(code, 1)

    def test_missing_wrapper_or_delegate_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            # Neither file present: wrapper resolution must refuse first.
            self.assertFalse(ra.preflight_build("qcow2", root=root)[0])
            with self.assertRaises(ValueError):
                ra.build_script_for("qcow2", root=root)
            # Wrapper present but real delegate absent: still fail closed.
            wrapper = root / ra.BUILD_SCRIPTS["qcow2"]
            wrapper.parent.mkdir(parents=True, exist_ok=True)
            wrapper.write_text("#!/bin/sh\n", encoding="utf-8")
            self.assertEqual(ra.build_script_for("qcow2", root=root),
                             ra.BUILD_SCRIPTS["qcow2"])
            ok, reason, _ = ra.preflight_build("qcow2", root=root)
            self.assertFalse(ok)
            self.assertIn("delegate", reason)

    @unittest.skipIf(os.name == "nt" or shutil.which("bash") is None,
                     "shell entry-point check runs on POSIX with bash")
    def test_wrapper_preflight_shell_entry_points_are_reachable(self):
        for artifact, wrapper in self.fixture["wrappers"].items():
            with self.subTest(artifact=artifact):
                result = subprocess.run(
                    ["bash", str(ra.ROOT / wrapper), "--preflight"],
                    cwd=str(ra.ROOT), text=True, capture_output=True,
                    timeout=30, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(self.fixture["delegates"][artifact],
                              result.stdout)


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

    def test_certificate_identity_is_the_workflow_ref(self):
        identity = ra.expected_certificate_identity("athNdev/carbonpanel")
        self.assertEqual(
            identity,
            "https://github.com/athndev/carbonpanel/.github/workflows/"
            "runtime-artifacts.yml@refs/heads/main")
        self.assertEqual(ra.OIDC_ISSUER, "https://token.actions.githubusercontent.com")
        with self.assertRaises(ValueError):
            ra.expected_certificate_identity("athNdev/carbonpanel", "refs/heads/feature")

    def test_signature_binding_fails_closed(self):
        manifest = "abc  file.bin\n"
        oidc = ra.expected_certificate_identity()
        doc = ra.signature_binding(VERSION, ARTIFACT, SHA, manifest,
                                   oidc, "c2lnbmF0dXJl", publisher_label="release-bot")
        self.assertEqual(doc["signer_identity"], oidc)
        self.assertEqual(doc["publisher_label"], "release-bot")
        self.assertTrue(ra.verify_signature_binding(doc, manifest, oidc)[0])
        self.assertFalse(ra.verify_signature_binding(
            doc, "changed  file.bin\n", oidc)[0])
        # The operator's free-form publisher label is NOT a certificate identity.
        self.assertFalse(ra.verify_signature_binding(
            doc, manifest, "release-bot")[0])
        # A label cannot be used to construct a binding record either.
        with self.assertRaises(ValueError):
            ra.signature_binding(VERSION, ARTIFACT, SHA, manifest,
                                 "release-bot", "c2lnbmF0dXJl")
        # A wrong (but well-formed) workflow ref refuses.
        self.assertFalse(ra.verify_signature_binding(
            doc, manifest,
            "https://github.com/other/repo/.github/workflows/runtime-artifacts.yml@refs/heads/main")[0])
        empty = dict(doc, signature="")
        self.assertFalse(ra.verify_signature_binding(empty, manifest, oidc)[0])

    def test_full_bundle_precondition_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            make_bundle(root, "qcow2", marker={"mode": "apply", "live_build": True})
            ok, problems = ra.verify_bundle(root, VERSION, "qcow2", SHA)
            self.assertTrue(ok, problems)
            (root / "disk.qcow2").write_bytes(b"tampered")
            ok, problems = ra.verify_bundle(root, VERSION, "qcow2", SHA)
            self.assertFalse(ok)
            self.assertTrue(any("mismatch" in p for p in problems))

    def test_integrity_records_are_not_checksummed_or_flagged(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "disk.qcow2").write_bytes(b"image-bytes")
            files = [root / "disk.qcow2"]
            for record in ra.INTEGRITY_RECORDS:
                (root / record).write_bytes(b"record")
            manifest = ra.checksum_manifest(files, root=root)
            (root / "SHA256SUMS").write_text(manifest, encoding="utf-8")
            ok, problems = ra.verify_checksum_manifest(manifest, root)
            self.assertTrue(ok, problems)
            self.assertNotIn("disk.qcow2", ra.INTEGRITY_RECORDS)
            for record in ("signature.sig", "signature.bundle"):
                self.assertIn(record, ra.INTEGRITY_RECORDS)
                self.assertNotIn(record, manifest)

    def test_no_host_paths_or_credentials_in_fixtures(self):
        for fixture_file in FIXTURES.glob("*"):
            text = fixture_file.read_text(encoding="utf-8", errors="replace")
            for forbidden in ("C:\\", "/home/", "/opt/carbon-panel", "password", "token="):
                with self.subTest(file=fixture_file.name, forbidden=forbidden):
                    self.assertNotIn(forbidden, text)


class PublicationPredicateTests(unittest.TestCase):
    """OSS-400: synthetic-record tests for the fail-closed publication predicate.

    Every record is written to a temp directory. No dispatch, signing, release,
    registry, hypervisor, LXC or host operation is executed.
    """

    def test_qualified_bundle_is_accepted_for_every_kind(self):
        for artifact in ra.VERIFIED_ARTIFACTS:
            with self.subTest(artifact=artifact), tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, artifact, marker={"mode": "apply", "live_build": True})
                ok, problems = ra.publication_predicate(tmp, artifact)
                self.assertTrue(ok, problems)

    def test_offline_wrapper_markers_are_refused_even_when_integrated(self):
        for mode in ("offline-plan", "offline-fixture", "delegate-preflight-only"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": mode, "live_build": False})
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok)
                self.assertTrue(any("offline" in p for p in problems), problems)

    def test_live_build_false_marker_alone_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": False})
            ok, problems = ra.publication_predicate(tmp, "qcow2")
            self.assertFalse(ok)
            self.assertTrue(any("live_build" in p for p in problems), problems)

    def test_completed_live_build_marker_is_not_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
            ok, problems = ra.publication_predicate(tmp, "qcow2")
            self.assertTrue(ok, problems)

    def test_fixture_only_plan_record_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True},
                        fixture_only=True)
            ok, problems = ra.publication_predicate(tmp, "qcow2")
            self.assertFalse(ok)
            self.assertTrue(any("fixture-only" in p for p in problems), problems)

    def test_synthetic_or_missing_provenance_is_refused(self):
        with self.subTest("synthetic"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", synthetic_provenance=True,
                            marker={"mode": "apply", "live_build": True})
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok)
                self.assertTrue(any("provenance" in p for p in problems), problems)
        with self.subTest("missing"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", provenance=False,
                            marker={"mode": "apply", "live_build": True})
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok)
                self.assertTrue(any("provenance" in p for p in problems), problems)

    def test_missing_or_wrong_kind_payload_is_refused(self):
        with self.subTest("stub"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", payload_bytes=64,
                            marker={"mode": "apply", "live_build": True})
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok)
                self.assertTrue(any("payload" in p for p in problems), problems)
        with self.subTest("wrong kind"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
                ok, problems = ra.publication_predicate(tmp, "lxc-rootfs")
                self.assertFalse(ok)
                self.assertTrue(any("payload" in p for p in problems), problems)
        with self.subTest("unverified kind"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2")
                ok, problems = ra.publication_predicate(tmp, "vhdx")
                self.assertFalse(ok)
                self.assertTrue(any("unverified" in p for p in problems), problems)

    def test_bypass_shapes_are_refused(self):
        """Fail closed on re-encoded/nested/misnamed/malformed rehearsal markers."""
        with self.subTest("non-boolean flags"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": 0})
                (Path(tmp) / "plan.json").write_text(
                    json.dumps({"fixture_only": 1, "no_qcow2_built": 1}), encoding="utf-8")
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok, problems)
        with self.subTest("case-variant marker filename"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
                (Path(tmp) / "BUILD-PREFLIGHT.json").unlink()
                (Path(tmp) / "build-preflight.JSON").write_text(
                    json.dumps({"mode": "offline-plan", "live_build": False}), encoding="utf-8")
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok, problems)
        with self.subTest("nested marker"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
                nested = Path(tmp) / "nested"
                nested.mkdir()
                (nested / "BUILD-PREFLIGHT.json").write_text(
                    json.dumps({"mode": "offline-plan", "live_build": False}), encoding="utf-8")
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok, problems)
        with self.subTest("malformed marker"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
                (Path(tmp) / "BUILD-PREFLIGHT.json").write_text("{not json", encoding="utf-8")
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok, problems)
        with self.subTest("marker in integrity record filename"):
            with tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2", marker={"mode": "apply", "live_build": True})
                (Path(tmp) / "sbom.cdx.json").write_text(
                    json.dumps({"fixture_only": True}), encoding="utf-8")
                ok, problems = ra.publication_predicate(tmp, "qcow2")
                self.assertFalse(ok, problems)

    def test_verify_bundle_inherits_the_predicate_refusal(self):
        with tempfile.TemporaryDirectory() as tmp:
            make_bundle(tmp, "qcow2", marker={"mode": "offline-plan", "live_build": False})
            ok, problems = ra.verify_bundle(tmp, VERSION, "qcow2", SHA)
            self.assertFalse(ok)
            self.assertTrue(any("offline" in p for p in problems), problems)

    def test_cli_publication_predicate_refuses_offline_output(self):
        for offline, mode in ((True, "offline-plan"), (False, "apply")):
            with self.subTest(offline=offline), tempfile.TemporaryDirectory() as tmp:
                make_bundle(tmp, "qcow2",
                            marker={"mode": mode, "live_build": not offline})
                out = io.StringIO()
                with contextlib.redirect_stdout(out):
                    code = ra.main(["publication-predicate", "--dir", tmp,
                                    "--artifact", "qcow2"])
                payload = json.loads(out.getvalue())
                self.assertEqual(code, 0 if not offline else 1, out.getvalue())
                self.assertEqual(payload["verified"], "false" if offline else "true")
                if offline:
                    self.assertTrue(any("offline" in p for p in payload["problems"]))


class WorkflowContractTests(unittest.TestCase):
    """The predicate is wired before signing/publishing; reachability is kept."""

    def test_workflow_gates_signature_and_release_on_the_predicate(self):
        import yaml
        document = yaml.safe_load(
            (ra.ROOT / ".github/workflows/runtime-artifacts.yml").read_text(encoding="utf-8"))
        steps = document["jobs"]["publish"]["steps"]
        names = [step.get("name", "") for step in steps]

        def index(fragment):
            return next(i for i, name in enumerate(names) if fragment in name)

        build = index("Build the verified artifact")
        predicate = index("publication predicate")
        checksum = index("deterministic checksum manifest")
        sign = index("Keyless-sign")
        bundle_verify = index("Verify the staged bundle")
        publish = index("DRAFT release")
        self.assertLess(build, predicate)
        self.assertLess(predicate, checksum)
        self.assertLess(predicate, sign)
        self.assertLess(predicate, bundle_verify)
        self.assertLess(predicate, publish)
        self.assertIn("publication-predicate", steps[predicate]["run"])
        self.assertEqual(steps[publish]["if"], "inputs.publish")
        triggers = document.get("on") or document.get(True)
        self.assertIn("workflow_dispatch", triggers)
        self.assertNotIn("schedule", triggers)

    def test_offline_rehearsal_reachability_and_wrappers_are_preserved(self):
        for artifact in ra.VERIFIED_ARTIFACTS:
            with self.subTest(artifact=artifact):
                ok, reason, decision = ra.preflight_build(artifact)
                self.assertTrue(ok, reason)
                wrapper = (ra.ROOT / ra.BUILD_SCRIPTS[artifact]).read_text(encoding="utf-8")
                self.assertIn("--preflight", wrapper)
                self.assertIn("BUILD-PREFLIGHT.json", wrapper)
                self.assertIn('"live_build": false', wrapper)


if __name__ == "__main__":
    unittest.main()
