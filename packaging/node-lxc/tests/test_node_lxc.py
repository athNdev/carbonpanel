"""Offline fixture qualification for the unprivileged LXC artifact lane.

No host, no network, no root, no live import/boot. Every assertion is made
against synthetic fixtures and an ephemeral RSA key generated inside a temp
directory. This suite qualifies refusals and state transitions only.
"""
from __future__ import annotations

import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
sys.path.insert(0, str(ROOT))
TOOL = ROOT / "node_lxc_artifact.py"
FIXTURES = ROOT / "fixtures"
ARTIFACT_PROVENANCE = ROOT / "config" / "provenance.json"
PROVENANCE = ROOT / "config" / "provenance.fixture.json"
V1 = "runtime-v1.0.0"
V2 = "runtime-v2.0.0"


def run(*args: str) -> tuple[int, dict]:
    proc = subprocess.run(
        [sys.executable, str(TOOL), *map(str, args)],
        capture_output=True, text=True,
    )
    try:
        result = json.loads(proc.stdout)
    except json.JSONDecodeError:
        raise AssertionError(f"non-JSON output (rc={proc.returncode})\nstdout={proc.stdout}\nstderr={proc.stderr}")
    return proc.returncode, result


def ok(*args: str) -> dict:
    rc, result = run(*args)
    assert rc == 0, f"expected ok, got {result}"
    assert result.get("status") == "ok", result
    return result


def refused(code: str, *args: str) -> dict:
    rc, result = run(*args)
    assert rc == 1, f"expected refusal, got {result}"
    assert result.get("status") == "refused", result
    assert result.get("reason_code") == code, f"expected {code}, got {result.get('reason_code')}: {result}"
    return result


class NodeLxCFixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory(prefix="cp-lxc-fixtures-")
        cls.work = Path(cls._tmp.name)
        cls.priv = cls.work / "signing.key.pem"
        cls.pub = cls.work / "signing.pub.pem"
        subprocess.run(
            ["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", str(cls.priv)],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["openssl", "pkey", "-in", str(cls.priv), "-pubout", "-out", str(cls.pub)],
            check=True, capture_output=True,
        )
        cls.other_priv = cls.work / "other.key.pem"
        cls.other_pub = cls.work / "other.pub.pem"
        subprocess.run(
            ["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", str(cls.other_priv)],
            check=True, capture_output=True,
        )
        subprocess.run(
            ["openssl", "pkey", "-in", str(cls.other_priv), "-pubout", "-out", str(cls.other_pub)],
            check=True, capture_output=True,
        )
        cls.art_v1 = cls.work / "artifact-v1"
        cls.art_v2 = cls.work / "artifact-v2"
        ok("build", "--source-dir", str(FIXTURES / "rootfs-minimal"),
           "--out-dir", str(cls.art_v1), "--provenance", str(PROVENANCE),
           "--version", V1, "--platform", "amd64", "--sign-key", str(cls.priv), "--fixture")
        ok("build", "--source-dir", str(FIXTURES / "rootfs-minimal"),
           "--out-dir", str(cls.art_v2), "--provenance", str(PROVENANCE),
           "--version", V2, "--platform", "amd64", "--sign-key", str(cls.priv), "--fixture")

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    # ------------------------------------------------------------------ build
    def test_01_build_and_verify_fixture_artifact(self) -> None:
        for name in ("rootfs", "manifest.json", "SHA256SUMS", "sbom.cdx.json",
                     "signature.json", "provenance.json", "BUILD_INFO.json"):
            self.assertTrue((self.art_v1 / name).exists(), name)
        sbom = json.loads((self.art_v1 / "sbom.cdx.json").read_text())
        self.assertEqual(sbom["bomFormat"], "CycloneDX")
        self.assertEqual(sbom["specVersion"], "1.5")
        self.assertEqual(len(sbom["components"]), 5)
        build_info = json.loads((self.art_v1 / "BUILD_INFO.json").read_text())
        self.assertEqual(build_info["build_mode"], "offline-fixture")
        self.assertIn("offline fixture only", build_info["qualification"])
        result = ok("verify", "--artifact-dir", str(self.art_v1), "--public-key", str(self.pub),
                    "--expect-version", V1)
        self.assertEqual(result["status_detail"], "VERIFY_OK")
        self.assertEqual(len(result["artifact_digest_sha256"]), 64)

    def test_02_unsigned_artifact_refused(self) -> None:
        unsigned = self.work / "artifact-unsigned"
        ok("build", "--source-dir", str(FIXTURES / "rootfs-minimal"), "--out-dir", str(unsigned),
           "--provenance", str(PROVENANCE), "--version", "runtime-v3.0.0", "--platform", "amd64", "--fixture")
        refused("UNSIGNED_ARTIFACT", "verify", "--artifact-dir", str(unsigned), "--public-key", str(self.pub))

    def test_03_tampered_payload_refused(self) -> None:
        tampered = self.work / "artifact-tampered"
        shutil.copytree(self.art_v1, tampered)
        (tampered / "rootfs" / "opt" / "carbon-panel" / "VERSION").write_text("runtime-v9.9.9-tampered\n")
        refused("CHECKSUM_MISMATCH", "verify", "--artifact-dir", str(tampered), "--public-key", str(self.pub))

    def test_04_wrong_public_key_refused(self) -> None:
        refused("SIGNATURE_INVALID", "verify", "--artifact-dir", str(self.art_v1),
                "--public-key", str(self.other_pub))

    def test_05_secret_material_refused(self) -> None:
        from node_lxc_artifact import scan_secrets
        secrets_dir = self.work / "scan-secrets"
        secrets_dir.mkdir(exist_ok=True)
        (secrets_dir / "key.pem").write_text("-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n")
        (secrets_dir / "meta.json").write_text('{"token": "abc"}')
        findings = scan_secrets(secrets_dir)
        self.assertTrue(any("key.pem" in f for f in findings))
        self.assertTrue(any("meta.json" in f for f in findings))

        seeded = self.work / "seeded-source"
        if seeded.exists():
            shutil.rmtree(seeded)
        shutil.copytree(FIXTURES / "rootfs-minimal", seeded)
        (seeded / "leak.json").write_text('{"password": "hunter2"}\n')
        import node_lxc_artifact as tool
        digest = tool.digest_of_files(tool.canonical_files(seeded))
        prov = json.loads(PROVENANCE.read_text())
        prov["fixture"]["manifest_sha256"] = digest
        prov_path = self.work / "seeded-provenance.json"
        prov_path.write_text(json.dumps(prov, indent=2, sort_keys=True))
        refused("SECRET_MATERIAL_IN_SOURCE", "build", "--source-dir", str(seeded),
                "--out-dir", str(self.work / "artifact-leak"), "--provenance", str(prov_path),
                "--version", "runtime-v4.0.0", "--platform", "amd64", "--fixture")

    def test_06_provenance_and_input_gates(self) -> None:
        changed = self.work / "changed-source"
        if changed.exists():
            shutil.rmtree(changed)
        shutil.copytree(FIXTURES / "rootfs-minimal", changed)
        (changed / "etc" / "os-release").write_text("ID=changed\n")
        refused("PROVENANCE_DIGEST_MISMATCH", "build", "--source-dir", str(changed),
                "--out-dir", str(self.work / "artifact-changed"), "--provenance", str(PROVENANCE),
                "--version", V1, "--platform", "amd64", "--fixture")
        refused("INVALID_VERSION", "build", "--source-dir", str(FIXTURES / "rootfs-minimal"),
                "--out-dir", str(self.work / "artifact-badver"), "--provenance", str(PROVENANCE),
                "--version", "v1", "--platform", "amd64", "--fixture")
        refused("UNSUPPORTED_PLATFORM", "build", "--source-dir", str(FIXTURES / "rootfs-minimal"),
                "--out-dir", str(self.work / "artifact-badplat"), "--provenance", str(PROVENANCE),
                "--version", V1, "--platform", "riscv64", "--fixture")

    def test_06b_line_ending_independent_digest(self) -> None:
        """A CRLF checkout must produce the same pinned digest as an LF source."""
        from node_lxc_artifact import canonical_file_digest, canonical_files, digest_of_files
        lf = self.work / "digest-lf"
        crlf = self.work / "digest-crlf"
        for root in (lf, crlf):
            if root.exists():
                shutil.rmtree(root)
            shutil.copytree(FIXTURES / "rootfs-minimal", root)
        # Normalize the source to LF first, then make an explicit CRLF variant,
        # so the comparison is genuinely LF vs CRLF regardless of checkout policy.
        for root in (lf, crlf):
            for p in root.rglob("*"):
                if p.is_file():
                    data = p.read_bytes().replace(b"\r\n", b"\n")
                    p.write_bytes(data)
        sample = crlf / "etc" / "os-release"
        sample.write_bytes(sample.read_bytes().replace(b"\n", b"\r\n"))
        self.assertEqual(canonical_file_digest(sample), canonical_file_digest(lf / "etc" / "os-release"))
        self.assertEqual(digest_of_files(canonical_files(crlf)), digest_of_files(canonical_files(lf)))
        # The CRLF tree still builds against the same pin.
        ok("build", "--source-dir", str(crlf),
           "--out-dir", str(self.work / "artifact-crlf"), "--provenance", str(PROVENANCE),
           "--version", "runtime-v5.0.0", "--platform", "amd64", "--sign-key", str(self.priv), "--fixture")
        ok("verify", "--artifact-dir", str(self.work / "artifact-crlf"), "--public-key", str(self.pub))

    def _prov_copy(self, mutate) -> Path:
        prov = json.loads(ARTIFACT_PROVENANCE.read_text())
        mutate(prov)
        path = self.work / f"prov-{len(list(self.work.glob('prov-*.json')))}.json"
        path.write_text(json.dumps(prov, indent=2, sort_keys=True))
        return path

    def test_06c_artifact_profile_accepts_real_pins(self) -> None:
        result = ok("validate-provenance", "--provenance", str(ARTIFACT_PROVENANCE),
                    "--expect-profile", "artifact")
        self.assertEqual(result["profile"], "artifact")
        self.assertIs(result["synthetic"], False)
        self.assertEqual(result["pin_count"], 2)
        for pin in result["pins"]:
            self.assertEqual(pin["digest_status"], "verified")
            self.assertEqual(len(pin["expected_sha256"]), 64)
            self.assertTrue(pin["image_url"].startswith("https://cloud-images.ubuntu.com/"))
            self.assertIn("/release-20261017/", pin["image_url"])
            self.assertEqual(pin["signing_key_fingerprint"], "D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81")
        fixture = ok("validate-provenance", "--provenance", str(PROVENANCE),
                     "--expect-profile", "fixture")
        self.assertIs(fixture["synthetic"], True)

    def test_06d_artifact_profile_refuses_synthetic_and_placeholder_pins(self) -> None:
        refused("SYNTHETIC_PROVENANCE", "validate-provenance",
                "--provenance", str(self._prov_copy(lambda d: d.update(synthetic=True))))
        refused("PLACEHOLDER_DIGEST", "validate-provenance",
                "--provenance", str(self._prov_copy(
                    lambda d: d["images"][0].update(expected_sha256="0" * 64))))
        refused("UNVERIFIED_DIGEST", "validate-provenance",
                "--provenance", str(self._prov_copy(
                    lambda d: d["images"][0].update(digest_status="unverified"))))
        refused("MISSING_SIGNING_EVIDENCE", "validate-provenance",
                "--provenance", str(self._prov_copy(lambda d: d["images"][0].pop("signature_url"))))
        refused("INVALID_PROVENANCE_PROFILE", "validate-provenance",
                "--provenance", str(PROVENANCE), "--expect-profile", "artifact")

    def test_06e_artifact_profile_refuses_moving_urls_and_profile_mixup(self) -> None:
        refused("MOVING_URL", "validate-provenance",
                "--provenance", str(self._prov_copy(lambda d: d["images"][0].update(
                    image_url="https://cloud-images.ubuntu.com/releases/noble/release/"
                              "ubuntu-24.04-server-cloudimg-amd64-root.tar.xz"))))
        refused("URL_PROVENANCE_MISMATCH", "validate-provenance",
                "--provenance", str(self._prov_copy(lambda d: d["images"][0].update(
                    checksum_url="https://cloud-images.ubuntu.com/releases/noble/release/SHA256SUMS"))))
        # A build in artifact mode refuses the synthetic fixture profile, and a
        # fixture build refuses the real artifact profile.
        refused("INVALID_PROVENANCE_PROFILE", "build",
                "--source-dir", str(FIXTURES / "rootfs-minimal"),
                "--out-dir", str(self.work / "artifact-mixup-a"),
                "--provenance", str(PROVENANCE), "--version", V1, "--platform", "amd64")
        refused("INVALID_FIXTURE_PROFILE", "build",
                "--source-dir", str(FIXTURES / "rootfs-minimal"),
                "--out-dir", str(self.work / "artifact-mixup-b"),
                "--provenance", str(ARTIFACT_PROVENANCE), "--version", V1,
                "--platform", "amd64", "--fixture")

    # -------------------------------------------------------- capability gate
    def test_07_capability_gate_supported_and_refusals(self) -> None:
        ok("capability-gate", "--capability-file", str(FIXTURES / "capability-supported.json"))
        cases = {
            "capability-unsupported-privileged.json": "PRIVILEGED_LXC_REFUSED",
            "capability-unsupported-userns.json": "USERNS_DISABLED",
            "capability-unsupported-cgroup.json": "CGROUP_V2_REQUIRED",
            "capability-unsupported-nesting.json": "NESTING_NOT_ATTESTED",
            "capability-unsupported-host-socket.json": "HOST_DOCKER_SOCKET_VISIBLE",
            "capability-unsupported-fuse.json": "FUSE_DEVICE_MISSING",
            "capability-unsupported-mac.json": "MAC_PROFILE_MISSING",
        }
        for filename, code in cases.items():
            with self.subTest(filename=filename):
                refused(code, "capability-gate", "--capability-file", str(FIXTURES / filename))
        refused("AUTODETECT_UNAVAILABLE", "capability-gate")

    # ------------------------------------------------------- first-boot identity
    def test_08_first_boot_identity_unique_and_scoped(self) -> None:
        vol_a = self.work / "identity-vol-a"
        vol_b = self.work / "identity-vol-b"
        a = ok("first-boot-identity", "--volume", str(vol_a), "--node-label", "lxc-a")
        b = ok("first-boot-identity", "--volume", str(vol_b), "--node-label", "lxc-b")
        self.assertNotEqual(a["node_id"], b["node_id"])
        self.assertEqual(len(a["node_id"]), 32)
        self.assertEqual(len(a["fingerprint"]), 64)

        bundle = json.loads((vol_a / "enrollment" / "pre-registration.json").read_text())
        secret_hex = (vol_a / "secrets" / "enrollment-secret.hex").read_text().strip()
        self.assertEqual(len(bundle["enrollment_secret_digest"]), 64)
        self.assertNotIn(secret_hex, json.dumps(bundle))
        self.assertIn("csr_pre_signature_b64", bundle)
        self.assertIn("ADR 0019", bundle["contract"])
        self.assertFalse((vol_a / "enrollment" / "node.key.pem").exists())
        self.assertTrue((vol_a / "secrets" / "node.key.pem").is_file())

        # CSR_PRE signature must verify with the node's own public key.
        derived_pub = self.work / "derived-node.pub.pem"
        subprocess.run(["openssl", "pkey", "-in", str(vol_a / "secrets" / "node.key.pem"),
                        "-pubout", "-out", str(derived_pub)], check=True, capture_output=True)
        stmt_bytes = (json.dumps(bundle["csr_pre"], sort_keys=True, separators=(",", ":")) + "\n").encode()
        stmt_file = self.work / "csr-pre-verify.json"
        stmt_file.write_bytes(stmt_bytes)
        sig_file = self.work / "csr-pre-verify.sig"
        sig_file.write_bytes(base64.b64decode(bundle["csr_pre_signature_b64"]))
        self.assertEqual(bundle["csr_pre_signature_algorithm"], "Ed25519-raw")
        verify = subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(derived_pub),
                                 "-rawin", "-in", str(stmt_file), "-sigfile", str(sig_file)],
                                capture_output=True, text=True)
        self.assertEqual(verify.returncode, 0, verify.stderr)

        refused("IDENTITY_ALREADY_EXISTS", "first-boot-identity", "--volume", str(vol_a), "--node-label", "lxc-a")

    # ------------------------------------------------------------ volume flows
    def test_09_volume_import_backup_restore(self) -> None:
        vol = self.work / "volume-live"
        ok("volume", "import", "--source", str(self.art_v1), "--volume", str(vol), "--public-key", str(self.pub))
        world = vol / "world" / "level.dat"
        world.parent.mkdir(parents=True, exist_ok=True)
        world.write_bytes(b"carbon-panel-lxc-world-fixture\n")
        ok("first-boot-identity", "--volume", str(vol), "--node-label", "lxc-live")

        backup = self.work / "backup-v1"
        ok("volume", "backup", "--volume", str(vol), "--out", str(backup),
           "--sign-key", str(self.priv), "--backup-version", "backup-v1")
        self.assertTrue((backup / "backup.tar.gz").is_file())
        self.assertTrue(json.loads((backup / "backup.json").read_text())["signed"])

        tampered_backup = self.work / "backup-tampered"
        shutil.copytree(backup, tampered_backup)
        (tampered_backup / "rootfs" / "world" / "level.dat").write_bytes(b"corrupted\n")
        refused("CHECKSUM_MISMATCH", "volume", "restore", "--backup", str(tampered_backup),
                "--volume", str(self.work / "volume-tampered"), "--public-key", str(self.pub))

        restored = self.work / "volume-restored"
        ok("volume", "restore", "--backup", str(backup), "--volume", str(restored), "--public-key", str(self.pub))
        self.assertEqual((restored / "world" / "level.dat").read_bytes(), b"carbon-panel-lxc-world-fixture\n")
        self.assertTrue((restored / "identity" / "identity.json").is_file())
        self.assertTrue((restored / ".carbon-panel" / "restore-receipt.json").is_file())
        refused("VOLUME_NOT_EMPTY", "volume", "restore", "--backup", str(backup),
                "--volume", str(restored), "--public-key", str(self.pub))

    # ---------------------------------------------------------- upgrade/rollback
    def test_10_upgrade_and_rollback(self) -> None:
        vol = self.work / "upgrade-vol"
        first = ok("upgrade", "apply", "--volume", str(vol), "--artifact", str(self.art_v1),
                   "--public-key", str(self.pub))
        self.assertEqual(first["current"], "a")
        self.assertEqual(first["version"], V1)
        second = ok("upgrade", "apply", "--volume", str(vol), "--artifact", str(self.art_v2),
                    "--public-key", str(self.pub))
        self.assertEqual(second["current"], "b")
        self.assertEqual(second["previous"], "a")
        status = ok("upgrade", "status", "--volume", str(vol))
        self.assertEqual(status["current"], "b")
        rolled = ok("upgrade", "rollback", "--volume", str(vol), "--public-key", str(self.pub))
        self.assertEqual(rolled["current"], "a")
        self.assertEqual(rolled["version"], V1)

        state_before = (vol / ".carbon-panel" / "upgrade-state.json").read_text()
        unsigned = self.work / "artifact-unsigned"
        refused("UNSIGNED_ARTIFACT", "upgrade", "apply", "--volume", str(vol),
                "--artifact", str(unsigned), "--public-key", str(self.pub))
        self.assertEqual((vol / ".carbon-panel" / "upgrade-state.json").read_text(), state_before)
        corrupt = self.work / "artifact-corrupt"
        shutil.copytree(self.art_v2, corrupt)
        (corrupt / "rootfs" / "opt" / "carbon-panel" / "VERSION").write_text("bad\n")
        refused("CHECKSUM_MISMATCH", "upgrade", "apply", "--volume", str(vol),
                "--artifact", str(corrupt), "--public-key", str(self.pub))
        self.assertEqual((vol / ".carbon-panel" / "upgrade-state.json").read_text(), state_before)


class SignedArchiveFixtureTests(unittest.TestCase):
    """Offline fixtures for end-to-end signed-archive verification.

    Uses a REAL ephemeral OpenPGP key and REAL gpg verification (no mocked
    signature success), but synthetic archive bytes and local staged files so
    the suite stays network-free and host-free.
    """

    @classmethod
    def setUpClass(cls) -> None:
        cls._tmp = tempfile.TemporaryDirectory(prefix="cp-lxc-signed-archive-")
        cls.work = Path(cls._tmp.name)
        cls.gpg_home = cls.work / "gpg-home"
        cls.key = cls.work / "signing-key.asc"
        cls.other_key = cls.work / "other-key.asc"
        # Separate homes guarantee the two exported keys have distinct fingerprints.
        for home, label, dest in ((cls.gpg_home, "Fixture Signer <fixture@example.invalid>", cls.key),
                                  (cls.work / "other-gpg-home", "Other Signer <other@example.invalid>", cls.other_key)):
            home.mkdir()
            subprocess.run(
                ["gpg", "--homedir", home.name, "--batch", "--passphrase", "",
                 "--quick-generate-key", label, "rsa2048", "sign", "0"],
                cwd=str(cls.work), check=True, capture_output=True, text=True,
            )
            listing = subprocess.run(
                ["gpg", "--homedir", home.name, "--batch", "--with-colons", "--fingerprint"],
                cwd=str(cls.work), check=True, capture_output=True, text=True,
            )
            fpr = next(line.split(":")[9].strip() for line in listing.stdout.splitlines()
                       if line.startswith("fpr:"))
            if home == cls.gpg_home:
                cls.fpr = fpr
            exported = subprocess.run(
                ["gpg", "--homedir", home.name, "--batch", "--armor", "--export", fpr],
                cwd=str(cls.work), check=True, capture_output=True,
            )
            dest.write_bytes(exported.stdout)

    @classmethod
    def tearDownClass(cls) -> None:
        cls._tmp.cleanup()

    def _make_archive(self, tag: str, *, arch: str = "amd64", version: str = "24.04",
                      crlf: bool = False) -> tuple[Path, Path]:
        import hashlib as _hashlib
        import io
        import tarfile
        import node_lxc_artifact as tool

        base = self.work / tag
        base.mkdir()
        shutil.copyfile(self.key, base / "signing-key.asc")
        src = base / "rootfs-src"
        for d in ("etc", "usr/lib", "bin", "sbin", "lib", "var"):
            (src / d).mkdir(parents=True, exist_ok=True)
        os_release = (f'NAME="Ubuntu"\nVERSION_ID="{version}"\n'
                      f'VERSION_CODENAME="noble"\nID=ubuntu\n')
        (src / "etc" / "os-release").write_text(os_release)
        (src / "usr" / "lib" / "os-release").write_text(os_release)
        root_name = f"ubuntu-24.04-server-cloudimg-{arch}-root.tar.xz"
        lxd_name = f"ubuntu-24.04-server-cloudimg-{arch}-lxd.tar.xz"
        root_tar = base / root_name
        if crlf:
            # OSS-402: produce a real xz tarball whose *raw compressed bytes*
            # contain a literal CRLF byte sequence. The pinned upstream digest
            # is the raw SHA-256 of these bytes, so verify-signed-archive and a
            # production build must compare the same raw bytes. The CRLF-normalized
            # digest differs, which is exactly the disagreement this fixture pins.
            for nonce in range(200):
                pad = bytearray()
                block = _hashlib.sha256(f"crlf-fixture-{nonce}".encode()).digest()
                while len(pad) < 16384:
                    block = _hashlib.sha256(block).digest()
                    pad.extend(block)
                with tarfile.open(root_tar, "w:xz") as tf:
                    for p in sorted(src.rglob("*")):
                        tf.add(p, arcname=p.relative_to(src).as_posix())
                    pad_info = tarfile.TarInfo("usr/lib/crlf-pad.bin")
                    pad_info.size = len(pad)
                    tf.addfile(pad_info, io.BytesIO(bytes(pad)))
                if b"\r\n" in root_tar.read_bytes():
                    break
            else:
                self.fail("could not construct an xz tarball with CRLF bytes")
        else:
            with tarfile.open(root_tar, "w:xz") as tf:
                for p in sorted(src.rglob("*")):
                    tf.add(p, arcname=p.relative_to(src).as_posix())

        meta_dir = base / "meta"
        meta_dir.mkdir()
        arch_alias = "x86_64" if arch == "amd64" else "aarch64"
        (meta_dir / "metadata.yaml").write_text(
            f'architecture: "{arch_alias}"\nproperties:\n    os: "ubuntu"\n'
            f'    release: "noble"\n    description: "Ubuntu 24.04 LTS server (20261017)"\n'
        )
        lxd_tar = base / lxd_name
        with tarfile.open(lxd_tar, "w:xz") as tf:
            tf.add(meta_dir / "metadata.yaml", arcname="metadata.yaml")

        sums = base / "SHA256SUMS"
        sums.write_text(f"{tool.sha256_file(root_tar)}  {root_name}\n"
                        f"{tool.sha256_file(lxd_tar)}  {lxd_name}\n")
        sig = base / "SHA256SUMS.gpg"
        rel = base.relative_to(self.work).as_posix()
        signed = subprocess.run(
            ["gpg", "--homedir", self.gpg_home.name, "--batch", "--yes", "--detach-sign",
             "-o", f"{rel}/SHA256SUMS.gpg", f"{rel}/SHA256SUMS"],
            cwd=str(self.work), check=True, capture_output=True, text=True,
        )
        self.assertEqual(signed.returncode, 0)

        prov = json.loads(ARTIFACT_PROVENANCE.read_text())
        pin = next(i for i in prov["images"] if i["arch"] == arch)
        prefix = "https://cloud-images.ubuntu.com/releases/noble/release-20261017/"
        pin.update({
            "image_url": prefix + root_name,
            "lxd_metadata_url": prefix + lxd_name,
            "checksum_url": prefix + "SHA256SUMS",
            "signature_url": prefix + "SHA256SUMS.gpg",
            "image_content_length": root_tar.stat().st_size,
            "expected_sha256": tool.sha256_file(root_tar),
            "lxd_metadata_sha256": tool.sha256_file(lxd_tar),
            "checksum_file_sha256": tool.sha256_file(sums),
            "signature_file_sha256": tool.sha256_file(sig),
            "signing_key_fingerprint": self.fpr,
            "signing_key_source": f"https://keyserver.ubuntu.com/pks/lookup?op=get&search=0x{self.fpr}",
        })
        prov["retrieval_evidence"]["key_get"] = (
            f"GET signing key -> HTTP 200, {self.key.stat().st_size} bytes, "
            f"sha256 {tool.sha256_file(self.key)}"
        )
        prov_path = base / "provenance.json"
        prov_path.write_text(json.dumps(prov, indent=2, sort_keys=True))
        return base, prov_path

    def _verify(self, work: Path, prov: Path, arch: str = "amd64") -> dict:
        return ok("verify-signed-archive", "--provenance", str(prov),
                  "--arch", arch, "--work-dir", str(work))

    def _prov_copy(self, work: Path, mutate) -> Path:
        prov = json.loads((work / "provenance.json").read_text())
        mutate(prov)
        path = work / "provenance-mutated.json"
        path.write_text(json.dumps(prov, indent=2, sort_keys=True))
        return path

    def test_11_signed_archive_real_gpg_and_bytes_ok(self) -> None:
        work, prov = self._make_archive("signed-ok")
        result = self._verify(work, prov)
        self.assertEqual(result["gpg_signature"], "GOOD")
        self.assertEqual(result["signing_key_fingerprint"], self.fpr.upper())
        self.assertEqual(len(result["bytes_checked"]), 2)
        self.assertEqual(result["layout"]["os_release"]["ID"], "ubuntu")
        self.assertEqual(result["layout"]["os_release"]["VERSION_ID"], "24.04")
        self.assertEqual(result["lxd_metadata"]["architecture"], "x86_64")
        self.assertIn("NOT live import/boot evidence", result["evidence_scope"])

    def test_12_signed_archive_tampered_tarball_refused(self) -> None:
        work, prov = self._make_archive("signed-tampered")
        root = next(work.glob("*-root.tar.xz"))
        with root.open("ab") as fh:
            fh.write(b"tamper")
        refused("UPSTREAM_DIGEST_MISMATCH", "verify-signed-archive",
                "--provenance", str(prov), "--arch", "amd64", "--work-dir", str(work))

    def test_13_signed_archive_tampered_checksums_refused(self) -> None:
        import node_lxc_artifact as tool
        work, prov = self._make_archive("signed-sums")
        sums = work / "SHA256SUMS"
        sums.write_text(sums.read_text() + "deadbeef  extra\n")
        refused("CONTROL_FILE_DIGEST_MISMATCH", "verify-signed-archive",
                "--provenance", str(prov), "--arch", "amd64", "--work-dir", str(work))
        changed = self._prov_copy(work, lambda d: d["images"][0].update(
            checksum_file_sha256=tool.sha256_file(sums)))
        refused("SIGNATURE_INVALID", "verify-signed-archive", "--provenance", str(changed),
                "--arch", "amd64", "--work-dir", str(work))

    def test_14_signed_archive_wrong_key_refused(self) -> None:
        import node_lxc_artifact as tool
        work, prov = self._make_archive("signed-key")
        shutil.copyfile(self.other_key, work / "signing-key.asc")
        # Keep the control-file digest consistent so verification reaches the
        # fingerprint/gpg check rather than stopping at the staged-byte pin.
        changed = self._prov_copy(work, lambda d: d["retrieval_evidence"].update(
            key_get=f"GET signing key -> HTTP 200, {self.other_key.stat().st_size} bytes, "
                    f"sha256 {tool.sha256_file(self.other_key)}"))
        refused("SIGNATURE_INVALID", "verify-signed-archive", "--provenance", str(changed),
                "--arch", "amd64", "--work-dir", str(work))

    def test_15_signed_archive_refuses_synthetic_placeholder_and_moving(self) -> None:
        work, prov = self._make_archive("signed-refusals")
        cases = (
            ("SYNTHETIC_PROVENANCE", lambda d: d.update(synthetic=True)),
            ("PLACEHOLDER_DIGEST", lambda d: d["images"][0].update(expected_sha256="0" * 64)),
            ("UNVERIFIED_DIGEST", lambda d: d["images"][0].update(digest_status="unverified")),
            ("MOVING_URL", lambda d: d["images"][0].update(
                image_url="https://cloud-images.ubuntu.com/releases/noble/release/"
                          "ubuntu-24.04-server-cloudimg-amd64-root.tar.xz")),
            ("MISSING_SIGNING_EVIDENCE", lambda d: d["images"][0].pop("signature_url")),
        )
        for code, mutate in cases:
            with self.subTest(code=code):
                changed = self._prov_copy(work, mutate)
                refused(code, "verify-signed-archive", "--provenance", str(changed),
                        "--arch", "amd64", "--work-dir", str(work))

    def test_16_signed_archive_metadata_version_refused(self) -> None:
        work, prov = self._make_archive("signed-version", version="22.04")
        refused("METADATA_VERSION_MISMATCH", "verify-signed-archive", "--provenance", str(prov),
                "--arch", "amd64", "--work-dir", str(work))

    def test_17_signed_archive_metadata_arch_refused(self) -> None:
        work, prov = self._make_archive("signed-arch")
        changed = self._prov_copy(work, lambda d: d["images"][0].update(arch="arm64"))
        refused("METADATA_ARCH_MISMATCH", "verify-signed-archive", "--provenance", str(changed),
                "--arch", "arm64", "--work-dir", str(work))

    def test_18_signed_archive_missing_files_refused(self) -> None:
        work, prov = self._make_archive("signed-missing")
        empty = self.work / "signed-empty"
        empty.mkdir()
        refused("MISSING_ARCHIVE_FILE", "verify-signed-archive", "--provenance", str(prov),
                "--arch", "amd64", "--work-dir", str(empty))

    def test_19_crlf_tarball_raw_digest_consistent_between_verify_and_build(self) -> None:
        """OSS-402 regression: build and verify-signed-archive must pin the SAME bytes.

        A pinned upstream tarball whose raw compressed bytes contain a CRLF
        sequence has a raw SHA-256 different from its CRLF-normalized digest.
        Both commands must accept the raw-pinned bytes and reject a byte flip.
        """
        import node_lxc_artifact as tool
        work, prov = self._make_archive("signed-crlf", crlf=True)
        root = next(work.glob("*-root.tar.xz"))
        raw = root.read_bytes()
        self.assertIn(b"\r\n", raw, "fixture must contain a literal CRLF sequence")
        raw_digest = tool.sha256_file(root)
        canonical_digest = tool.canonical_file_digest(root)
        self.assertNotEqual(raw_digest, canonical_digest,
                            "the CRLF sequence must make the normalized digest diverge")
        pin = json.loads(prov.read_text())
        amd = next(i for i in pin["images"] if i["arch"] == "amd64")
        self.assertEqual(amd["expected_sha256"], raw_digest)
        self.assertNotEqual(amd["expected_sha256"], canonical_digest)

        # verify-signed-archive accepts the raw-pinned bytes (real gpg check).
        result = self._verify(work, prov)
        self.assertEqual(result["gpg_signature"], "GOOD")
        self.assertEqual(result["bytes_checked"][0]["sha256"], raw_digest)

        # build accepts the SAME bytes against the SAME raw pin.
        built = self.work / "crlf-build"
        ok("build", "--source-dir", str(FIXTURES / "rootfs-minimal"),
           "--out-dir", str(built), "--provenance", str(prov),
           "--version", "runtime-v6.0.0", "--platform", "amd64",
           "--base-tarball", str(root))
        build_info = json.loads((built / "BUILD_INFO.json").read_text())
        self.assertEqual(build_info["build_mode"], "pinned-upstream")

        # A one-byte flip is rejected by BOTH commands against the same pin.
        tampered_work = self.work / "signed-crlf-tampered"
        if tampered_work.exists():
            shutil.rmtree(tampered_work)
        shutil.copytree(work, tampered_work)
        tampered_root = next(tampered_work.glob("*-root.tar.xz"))
        with tampered_root.open("ab") as fh:
            fh.write(b"X")
        refused("UPSTREAM_DIGEST_MISMATCH", "verify-signed-archive",
                "--provenance", str(prov), "--arch", "amd64", "--work-dir", str(tampered_work))
        refused("UPSTREAM_DIGEST_MISMATCH", "build",
                "--source-dir", str(FIXTURES / "rootfs-minimal"),
                "--out-dir", str(self.work / "crlf-build-refuse"),
                "--provenance", str(prov), "--version", "runtime-v6.0.0",
                "--platform", "amd64", "--base-tarball", str(tampered_root))

    # ----------------------------------------------------- boot qualification
    def test_20_boot_qualify_fail_closed_with_real_signed_archive_evidence(self) -> None:
        work, prov = self._make_archive("boot-qual-blocked")
        boot_work = self.work / "boot-qual-blocked-run"
        result = refused(
            "BOOT_QUALIFICATION_BLOCKED", "boot-qualify",
            "--provenance", str(prov), "--arch", "amd64",
            "--work-dir", str(boot_work), "--signed-archive-dir", str(work),
        )
        self.assertEqual(result["schema"], "carbonpanel.node-lxc.boot-qualification/v1")
        self.assertEqual(result["signed_archive_verification"]["status"], "ok")
        self.assertEqual(result["signed_archive_verification"]["gpg_signature"], "GOOD")
        self.assertIn("NOT live import/boot evidence", result["signed_archive_verification"]["evidence_scope"])
        boot = result["boot_qualification"]
        self.assertEqual(boot["status"], "blocked")
        self.assertFalse(boot["attempted"])
        self.assertIsInstance(boot["prerequisite_checks"], list)
        self.assertGreater(len(boot["prerequisite_checks"]), 0)
        self.assertIsInstance(boot["blockers"], list)
        self.assertGreater(len(boot["blockers"]), 0)
        report = Path(result.get("report_path") or (boot_work / "boot-qualification-report.json"))
        self.assertTrue(report.is_file(), report)
        saved = json.loads(report.read_text())
        self.assertEqual(saved["reason_code"], "BOOT_QUALIFICATION_BLOCKED")
        self.assertEqual(saved["signed_archive_verification"]["status"], "ok")

    def test_21_boot_qualify_refuses_when_signed_archive_not_verified(self) -> None:
        work, prov = self._make_archive("boot-qual-nosig")
        # Tamper the staged root tarball so signed archive verification fails closed.
        root = next(work.glob("*-root.tar.xz"))
        with root.open("ab") as fh:
            fh.write(b"tamper")
        boot_work = self.work / "boot-qual-nosig-run"
        result = refused(
            "SIGNED_ARCHIVE_NOT_VERIFIED", "boot-qualify",
            "--provenance", str(prov), "--arch", "amd64",
            "--work-dir", str(boot_work), "--signed-archive-dir", str(work),
        )
        self.assertEqual(result["signed_archive_verification"]["status"], "refused")
        self.assertEqual(result["signed_archive_verification"]["reason_code"], "UPSTREAM_DIGEST_MISMATCH")
        self.assertEqual(result["boot_qualification"]["status"], "blocked")
        self.assertEqual(result["boot_qualification"]["reason"], "signed archive verification did not pass")


if __name__ == "__main__":
    unittest.main()

