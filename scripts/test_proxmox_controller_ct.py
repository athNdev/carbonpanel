#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""OSS-415 offline fixtures. No host CT, no downloads, no publication."""
import copy
import json
import os
import shutil
import tempfile
import types
import unittest
from pathlib import Path

from scripts import proxmox_controller_ct as ct


class ProvenanceTests(unittest.TestCase):
    def test_manifest_matches_trusted_sources(self):
        m = ct.load_manifest()
        result = ct.verify_manifest_against_repo(m)
        self.assertEqual(result["status"], "PASS")
        self.assertEqual(result["controller_digest"],
                         "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001")
        self.assertEqual(result["base_suite"], "trixie")

    def test_pin_drift_is_refused(self):
        m = ct.load_manifest()
        for field, value in (
            ("version", "9.9.9"),
            ("commit", "0" * 40),
            ("ghcr_digest", "sha256:" + "1" * 64),
        ):
            mutated = copy.deepcopy(m)
            mutated["controller"][field] = value
            with self.assertRaises(ct.CtError):
                ct.verify_manifest_against_repo(mutated)
        base = copy.deepcopy(m)
        base["base"]["suite"] = "bookworm"
        with self.assertRaises(ct.CtError):
            ct.verify_manifest_against_repo(base)

    def test_moving_tag_refused(self):
        m = ct.load_manifest()
        m["controller"]["image"] = "atharvnaphade/carbonpanel:latest"
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as f:
            json.dump(m, f)
            path = f.name
        try:
            with self.assertRaises(ct.CtError):
                ct.load_manifest(path)
        finally:
            os.unlink(path)


class ArchiveTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.staging = self.root / "staging"
        ct.build_rootfs(self.staging, fixture=True)

    def tearDown(self):
        self.tmp.cleanup()

    def _archive(self):
        return ct.make_archive(self.staging, self.root / "ct.tar.zst", "controller-ct-v1.0.0")

    def test_enabled_firstboot_wants_link_required(self):
        out = self._archive()
        _, _, _, links = ct.archive_members(out["archive"], out["checksum"])
        self.assertIn(ct.WANTS_LINK, links)
        self.assertIn(links[ct.WANTS_LINK], (
            "../carbon-panel-ct-firstboot.service",
            "/etc/systemd/system/carbon-panel-ct-firstboot.service"))
        ct.verify_archive(out["archive"], out["checksum"])
        # Negative: remove only the enable symlink.
        self.staging.joinpath(ct.WANTS_LINK.lstrip("./")).unlink()
        out = self._archive()
        with self.assertRaises(ct.CtError):
            ct.verify_archive(out["archive"], out["checksum"])

    def test_require_real_receipt_gates(self):
        out = self._archive()
        with self.assertRaises(ct.CtError):
            ct.verify_archive(out["archive"], out["checksum"], require_real=True)
        packages = json.loads(ct.PACKAGES.read_text(encoding="utf-8"))["packages"]
        real = {
            "schema": "carbonpanel.proxmox-controller-ct.docker-receipt/v1",
            "signed_repo_required": True,
            "key_fingerprint": "9DC858229FC7DD38854AE2D88D81803C0EBFCD88",
            "fixture": False,
            "publishable": True,
            "base_suite": "trixie",
            "base_release_sha256": "f" * 64,
            "packages": packages,
            "package_sha256": {p: "a" * 64 for p in packages},
            "package_versions": {p: "1.2.3-1" for p in packages},
        }
        receipt_source = self.staging.parent / "docker-package-receipt.json"
        receipt_source.write_text(json.dumps(real), encoding="utf-8")
        out = self._archive()
        result = ct.verify_archive(out["archive"], out["checksum"], require_real=True)
        self.assertEqual(result["status"], "PASS")
        real["publishable"] = False
        receipt_source.write_text(json.dumps(real), encoding="utf-8")
        out = self._archive()
        with self.assertRaises(ct.CtError):
            ct.verify_archive(out["archive"], out["checksum"], require_real=True)

    def test_exact_env_patterns_only(self):
        for bad in ("./.env", "./opt/app/.env.production"):
            with self.assertRaises(ct.CtError):
                ct.check_member_name(bad)
        for good in ("./usr/share/man/man7/systemd.environment-generator.7.gz",
                     "./etc/environment", "./usr/lib/systemd/system/docker.socket"):
            ct.check_member_name(good)

    def test_normal_debian_links_are_allowed(self):
        out = self._archive()
        members, _, seen, links = ct.archive_members(out["archive"], out["checksum"])
        self.assertIn("./usr/sbin/arptables", seen)
        self.assertIn(
            "./etc/systemd/system/multi-user.target.wants/carbon-panel-ct-firstboot.service",
            seen)
        result = ct.verify_archive(out["archive"], out["checksum"])
        self.assertEqual(result["status"], "PASS")

    def test_link_ancestor_and_absolute_hardlink_refused(self):
        with self.assertRaises(ct.CtError):
            ct.check_symlink_ancestors(["./etc/alternatives/arptables/evil"],
                                        {"./etc/alternatives/arptables"})
        hardlink = types.SimpleNamespace(name="./usr/bin/docker", linkname="/etc/passwd")
        with self.assertRaises(ct.CtError):
            ct.check_link_target(hardlink, is_symlink=False)

    def test_zstd_roundtrip(self):
        payload = b"carbon-panel controller ct" * 100
        self.assertEqual(ct.zstd_decompress(ct.zstd_compress(payload)), payload)

    def test_archive_success(self):
        out = self._archive()
        result = ct.verify_archive(out["archive"], out["checksum"])
        self.assertEqual(result["status"], "PASS")
        self.assertTrue(result["fixture"])
        self.assertIn("@sha256:", result["image"])

    def test_checksum_and_layout_refusals(self):
        out = self._archive()
        (self.root / "ct.tar.zst").write_bytes(b"tampered")
        with self.assertRaises(ct.CtError):
            ct.verify_archive(out["archive"], out["checksum"])
        out = self._archive()
        unit = self.staging / "etc" / "systemd" / "system" / "carbon-panel-ct-firstboot.service"
        unit.write_text(unit.read_text(encoding="utf-8").replace(
            "After=network-online.target docker.service", "After=network.target"))
        out = self._archive()
        with self.assertRaises(ct.CtError):
            ct.verify_archive(out["archive"], out["checksum"])

    def test_forbidden_and_wrong_pin_refusals(self):
        out = self._archive()
        members, _, seen, links = ct.archive_members(out["archive"], out["checksum"])
        self.assertIn("./etc/", "".join(seen))
        with self.assertRaises(ct.CtError):
            ct.check_socket_path("./var/run/docker.sock")
        ct.check_socket_path("./usr/lib/systemd/system/docker.socket")
        members["./etc/enrollment-secret.bin"] = b"secret"
        with self.assertRaises(ct.CtError):
            ct.check_no_forbidden(members)
        # A real tampered archive (moving image tag) must fail verification.
        bad_stage = self.root / "bad-stage"
        shutil.copytree(self.staging, bad_stage)
        (bad_stage / "usr" / "local" / "share" / "carbon-panel" / "controller-image.txt").write_text(
            "atharvnaphade/carbonpanel:latest\n", encoding="utf-8")
        bad = ct.make_archive(bad_stage, self.root / "bad.tar.zst", "controller-ct-v1.0.0")
        with self.assertRaises(ct.CtError):
            ct.verify_archive(bad["archive"], bad["checksum"])

    def test_non_root_system_ownership_preserved(self):
        if os.geteuid() != 0:
            self.skipTest("chown requires root in the owned fixture runner")
        target = self.staging / "etc" / "carbon-owner-check.conf"
        target.write_text("owned\n", encoding="utf-8")
        os.chown(target, 1000, 1000)
        out = self._archive()
        _, ownership, seen, _ = ct.archive_members(out["archive"], out["checksum"])
        self.assertIn("./bin", seen)
        uid, gid, mode, _, _ = ownership["./etc/carbon-owner-check.conf"]
        self.assertEqual(uid, 1000)
        self.assertEqual(gid, 1000)
        self.assertEqual(mode & 0o777, 0o644)

    def test_fixture_archive_is_not_publishable(self):
        out = self._archive()
        members, _, _, _ = ct.archive_members(out["archive"])
        receipt = json.loads(members[ct.RECEIPT])
        self.assertFalse(receipt["publishable"])
        self.assertTrue(receipt["signed_repo_required"])


class PreparedRootfsTests(unittest.TestCase):
    def test_real_overlay_with_missing_app_dirs(self):
        """The real debootstrap overlay has no /usr/local/share/carbon-panel."""
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            prepared = root / "prepared"
            (prepared / "etc" / "apt" / "trusted.gpg.d").mkdir(parents=True)
            (prepared / "etc" / "apt" / "trusted.gpg.d" / "debian-archive-key-13.gpg").write_bytes(b"key")
            key = root / "docker.gpg"
            key.write_bytes(b"binary-keyring")
            packages = json.loads((ct.PACKAGES).read_text(encoding="utf-8"))["packages"]
            receipt = root / "receipt.json"
            receipt.write_text(json.dumps({
                "schema": "carbonpanel.proxmox-controller-ct.docker-receipt/v1",
                "signed_repo_required": True,
                "key_fingerprint": "9DC858229FC7DD38854AE2D88D81803C0EBFCD88",
                "fixture": False,
                "publishable": True,
                "packages": packages,
                "package_sha256": {p: "a" * 64 for p in packages},
            }), encoding="utf-8")
            result = ct.build_rootfs(prepared, prepared_rootfs=prepared,
                                     docker_key=key, package_receipt=str(receipt))
            self.assertFalse(result["fixture"])
            self.assertTrue((prepared / "usr" / "local" / "share" / "carbon-panel" /
                             "install-carbon-panel.sh").is_file())
            self.assertTrue((prepared / "etc" / "systemd" / "system" /
                             "carbon-panel-ct-firstboot.service").is_file())
            self.assertTrue((prepared / "opt" / "carbon-panel").is_dir())
            self.assertTrue((prepared / "var" / "lib" / "carbon-panel-ct").is_dir())


class FirstBootTests(unittest.TestCase):
    def test_success_idempotence_and_refusals(self):
        m = ct.load_manifest()
        result = ct.run_firstboot_fixture(
            ct.FIRSTBOOT / "carbon-panel-ct-firstboot.sh",
            ct.ROOT / "scripts" / "proxmox" / "install-carbon-panel.sh",
            m["controller"]["image"])
        self.assertEqual(result["status"], "PASS")
        self.assertEqual(len(result["checks"]), 5)


class PublicationGateTests(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 40
        self.art = "b" * 64
        self.version = "controller-ct-v1.0.0"
        self.identity = f"oss-runtime {self.version} {self.sha[:12]} {self.art[:12]}"

    def test_stage_only_and_explicit_publish(self):
        stage = ct.publication_gate("workflow_dispatch", "refs/heads/main", self.sha,
                                    self.version, self.art, self.identity, False,
                                    main_sha=self.sha, ci_success=True)
        self.assertFalse(stage["allowed"])
        self.assertTrue(stage["stage_only"])
        ok = ct.publication_gate("workflow_dispatch", "refs/heads/main", self.sha,
                                 self.version, self.art, self.identity, True,
                                 main_sha=self.sha, ci_success=True)
        self.assertTrue(ok["allowed"])

    def test_every_guard_refuses(self):
        cases = (
            ("push", "refs/heads/main", self.sha, self.version, self.art, self.identity, True),
            ("workflow_dispatch", "refs/heads/codex", self.sha, self.version, self.art, self.identity, True),
            ("workflow_dispatch", "refs/heads/main", "xyz", self.version, self.art, self.identity, True),
            ("workflow_dispatch", "refs/heads/main", self.sha, "v1.0.0", self.art, self.identity, True),
            ("workflow_dispatch", "refs/heads/main", self.sha, self.version, "notsha", self.identity, True),
            ("workflow_dispatch", "refs/heads/main", self.sha, self.version, self.art, "someone else", True),
        )
        for case in cases:
            self.assertFalse(ct.publication_gate(*case, main_sha=self.sha, ci_success=True)["allowed"])
        self.assertFalse(ct.publication_gate("workflow_dispatch", "refs/heads/main", "b" * 40,
                                             self.version, self.art, self.identity, True,
                                             main_sha=self.sha, ci_success=True)["allowed"])
        self.assertFalse(ct.publication_gate("workflow_dispatch", "refs/heads/main", self.sha,
                                             self.version, self.art, self.identity, True,
                                             main_sha=self.sha, ci_success=False)["allowed"])


if __name__ == "__main__":
    unittest.main()
