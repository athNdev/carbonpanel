# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for first-boot identity and scoped enrollment material.

Includes guest CLI tests with throwaway OpenSSL keys when OpenSSL is available.
"""
import base64
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
ENROLL_PATH = ROOT / "packaging" / "node-vm" / "cloud-init" / "firstboot" / "enroll.py"
SHARED_GOLDEN = ROOT / "packaging" / "node-vm" / "qualification" / "fixtures" / "claim-context-shared.json"
POLICY_PATH = ROOT / "packaging" / "node-vm" / "config" / "enrollment-policy.json"


def load_enroll():
    spec = importlib.util.spec_from_file_location("node_vm_enroll_fixture", ENROLL_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


enroll = load_enroll()


def repeating(pattern):
    """Deterministic fake CSPRNG that emits a repeated byte pattern."""
    def rng(n):
        return (pattern * ((n // len(pattern)) + 1))[:n]
    return rng


def snapshot_identity(out):
    """Read every file in an enrollment directory for no-write assertions."""
    return {path.name: path.read_bytes() for path in Path(out).iterdir()}


def rewrite_bundle(out, bundle):
    """Rewrite a saved bundle while keeping the fixture private."""
    path = Path(out) / "enrollment-bundle.json"
    path.write_text(json.dumps(bundle, sort_keys=True, indent=2) + "\n")
    if os.name == "posix":
        path.chmod(0o600)
        Path(out).chmod(0o700)


def read_tlv(buf, offset):
    """Read one DER tag/length/value header; return tag, content offset, length."""
    if offset + 2 > len(buf):
        raise ValueError("truncated DER header")
    tag = buf[offset]
    first = buf[offset + 1]
    offset += 2
    if first & 0x80:
        count = first & 0x7f
        if count == 0 or offset + count > len(buf):
            raise ValueError("unsupported DER length")
        length = int.from_bytes(buf[offset:offset + count], "big")
        offset += count
    else:
        length = first
    if offset + length > len(buf):
        raise ValueError("truncated DER value")
    return tag, offset, length


def replace_signature_algorithm_oid(der, oid):
    """Replace the PKCS#10 signatureAlgorithm OID without touching the signed body."""
    outer_tag, content, _ = read_tlv(der, 0)
    info_tag, info_content, info_length = read_tlv(der, content)
    alg_tag, alg_content, _ = read_tlv(der, info_content + info_length)
    oid_tag, oid_content, oid_length = read_tlv(der, alg_content)
    if outer_tag != 0x30 or info_tag != 0x30 or alg_tag != 0x30 or oid_tag != 0x06:
        raise ValueError("unexpected PKCS#10 DER layout")
    if oid_length != len(oid):
        raise ValueError("replacement OID length differs")
    return der[:oid_content] + oid + der[oid_content + oid_length:]


class SavedFixtureKeyPair(enroll.FakeKeyPair):
    """Stand-in for the guest OpenSSL key so saved-identity tests stay offline."""

    def __init__(self, private_key_path):
        super().__init__("fixture-key")
        self.path = Path(private_key_path)

    @classmethod
    def generate(cls, private_key_path):
        Path(private_key_path).write_bytes(b"SYNTHETIC-ED25519-PRIVATE-KEY")
        os.chmod(private_key_path, 0o600)
        return cls(private_key_path)


class IdentityTests(unittest.TestCase):
    def test_unique_node_id_per_instance(self):
        a = enroll.derive_node_id("fixture-instance-0001")
        b = enroll.derive_node_id("fixture-instance-0002")
        self.assertNotEqual(a, b)
        self.assertEqual(a, "node-24031fb03274b0b47214207802f7d224")
        self.assertEqual(b, "node-bf72c1a4fee3ee9368e8c55df18bf639")
        with self.assertRaises(ValueError):
            enroll.derive_node_id("")

    def test_secret_strength_and_digest(self):
        secret = enroll.generate_secret(256, repeating(b"\xa5"))
        self.assertEqual(len(secret), 32)
        salt = enroll.generate_salt(16, repeating(b"\x5a"))
        digest = enroll.compute_digest(salt, secret)
        self.assertEqual(digest, hashlib.sha256(salt + secret).hexdigest())
        with self.assertRaises(ValueError):
            enroll.generate_secret(128, repeating(b"\x01"))
        with self.assertRaises(ValueError):
            enroll.generate_salt(8, repeating(b"\x01"))

    def test_bundle_is_digest_only(self):
        key = enroll.FakeKeyPair("fixture-key")
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        secret = enroll.generate_secret(256, repeating(b"\x11"))
        salt = enroll.generate_salt(16, repeating(b"\x22"))
        bundle, digest, returned_secret = enroll.build_preregistration_bundle(
            "node-fixture", "fixture01", salt, secret, key, policy,
        )
        self.assertEqual(secret, returned_secret)
        self.assertEqual(digest, hashlib.sha256(salt + secret).hexdigest())
        blob = json.dumps(bundle, sort_keys=True)
        self.assertNotIn(base64.b64encode(secret).decode(), blob)
        self.assertNotIn(secret.hex(), blob)
        self.assertNotIn("private_key", bundle)
        self.assertNotIn("enrollment_secret", bundle)
        self.assertEqual(bundle["bootstrap_scopes"], ["BeginEnrollmentChallenge", "ClaimEnrollment"])
        self.assertEqual(bundle["contract_ref"], policy["contract_ref"])
        self.assertEqual(
            bundle["object_id_binding"], policy["policy"]["object_id_binding"],
        )
        self.assertTrue(bundle["contract_is_shared"])
        self.assertFalse(bundle["fork_allowed"])

    def test_secret_leak_is_refused(self):
        key = enroll.FakeKeyPair("fixture-key")
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        secret = enroll.generate_secret(256, repeating(b"\x33"))
        salt = enroll.generate_salt(16, repeating(b"\x44"))
        bundle, _, _ = enroll.build_preregistration_bundle("node-fixture", "fixture01", salt, secret, key, policy)
        enroll.assert_bundle_has_no_secret(bundle, secret, salt)  # safe case
        bad = dict(bundle)
        bad["enrollment_secret"] = "leak"
        with self.assertRaises(ValueError):
            enroll.assert_bundle_has_no_secret(bad, secret)
        bad2 = dict(bundle)
        bad2["csr_pre"] = base64.b64encode(secret).decode()
        with self.assertRaises(ValueError):
            enroll.assert_bundle_has_no_secret(bad2, secret)

    def test_first_boot_identity_deterministic_with_injected_deps(self):
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        policy["artifact_revision"] = "fixture-rev"
        kwargs = dict(rng=repeating(b"\x77"), rng_salt=repeating(b"\x88"),
                      keypair=enroll.FakeKeyPair("fixture-key"))
        first = enroll.first_boot_identity("fixture-instance-0001", "fixture01", policy, **kwargs)
        second = enroll.first_boot_identity("fixture-instance-0001", "fixture01", policy, **kwargs)
        self.assertEqual(first, second)
        self.assertEqual(first["node_id"], "node-24031fb03274b0b47214207802f7d224")
        blob = json.dumps(first["bundle"], sort_keys=True)
        self.assertNotIn(base64.b64encode(first["secret"]).decode(), blob)

    def test_write_identity_separates_bundle_and_secret(self):
        key = enroll.FakeKeyPair("fixture-key")
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        secret = enroll.generate_secret(256, repeating(b"\x99"))
        salt = enroll.generate_salt(16, repeating(b"\xaa"))
        bundle, _, _ = enroll.build_preregistration_bundle("node-fixture", "fixture01", salt, secret, key, policy)
        with tempfile.TemporaryDirectory() as tmp:
            bundle_path, secret_path = enroll.write_identity(bundle, secret, tmp)
            bundle_data = Path(bundle_path).read_text()
            self.assertNotIn(secret.hex(), bundle_data)
            self.assertEqual(Path(secret_path).read_bytes(), secret)

    def test_shared_golden_context_bytes(self):
        """Byte-for-byte conformance with shared internal/noderuntime/enroll.

        Golden produced by running enroll.ClaimContextRaw at OSS-385 PR532 head
        fc998875 in an owned temporary worktree (no shared writer files edited).
        """
        fixture = json.loads(SHARED_GOLDEN.read_text())
        golden = bytes.fromhex(fixture["golden_context_hex"])
        inputs = fixture["inputs"]
        ctx = enroll.canonical_context(
            inputs["node_id"], inputs["enrollment_id"], inputs["challenge_id"],
            bytes.fromhex(inputs["server_nonce_hex"]), inputs["expires_at"],
            inputs["canonical_capabilities"], inputs["channel_context"].encode(),
        )
        self.assertEqual(ctx, golden)
        self.assertEqual(fixture["source"]["commit"],
                         "fc998875e18e92712427f5ecfb4dfd0696c4f723")
        self.assertEqual(fixture["source"]["function"], "ClaimContextRaw")
        self.assertEqual(len(fixture["golden_signature_hex"]), 128)
        fields = ctx.split(b"\x00")
        self.assertEqual(len(fields), 8)
        self.assertEqual(fields[0], b"carbon-panel-enroll-v1")
        self.assertEqual(fields[4], bytes.fromhex("deadbeef"))
        self.assertEqual(fields[5], b"2026-11-19T12:00:00.12345Z")
        self.assertEqual(fields[7], b"controller-derived:cn=carbon-panel")

    def test_canonical_json_claim_is_refused(self):
        key = enroll.FakeKeyPair("fixture-key")
        good = enroll.canonical_context(
            "node-1", "enr-1", "ch-1", b"server-nonce", "2026-01-01T00:00:00Z",
            ["NODE_CAPABILITY_PRIVATE_SOCKET"], b"channel",
        )
        sig = enroll.sign_claim(key, good)
        self.assertTrue(enroll.verify_claim(key, good, sig))
        self.assertNotIn(b'"', good)

        # The previous canonical-JSON encoding is refused, signed or not.
        old = enroll.SIGNING_DOMAIN + b"\x00" + json.dumps({
            "node_id": "node-1", "enrollment_id": "enr-1", "challenge_id": "ch-1",
            "server_nonce": "server-nonce", "expires_at": "2026-01-01T00:00:00Z",
            "capabilities": ["NODE_CAPABILITY_PRIVATE_SOCKET"],
            "server_observed_channel_context": "channel",
        }, sort_keys=True, separators=(",", ":")).encode()
        with self.assertRaises(ValueError):
            enroll.sign_claim(key, old)
        with self.assertRaises(ValueError):
            enroll.verify_claim(key, old, key.sign(old))
        with self.assertRaises(ValueError):
            enroll.sign_claim(key, b"bad-context")

    def test_claim_from_server_challenge(self):
        key = enroll.FakeKeyPair("fixture-key")
        challenge = {
            "challenge_id": "challenge-enroll-vm-1",
            "server_nonce": bytes.fromhex("cafe00babe"),
            "expires_at": "2026-11-20T00:00:00Z",
            "channel_context": b'controller-derived:\x00{"node_id"}',
        }
        claim = enroll.claim_from_challenge(
            key, "node-vm-1", "enroll-vm-1", challenge,
            ["NODE_CAPABILITY_PRIVATE_SOCKET", "NODE_CAPABILITY_DEDICATED_ROOTS"],
        )
        expected = enroll.canonical_context(
            "node-vm-1", "enroll-vm-1", challenge["challenge_id"],
            challenge["server_nonce"], challenge["expires_at"],
            claim["capabilities"], challenge["channel_context"],
        )
        self.assertEqual(claim["context"], expected)
        self.assertTrue(enroll.verify_claim(key, expected, claim["fresh_claim_signature"]))
        self.assertEqual(claim["channel_binding_evidence"], challenge["channel_context"])
        self.assertNotIn("client_nonce", claim)

    def test_ed25519_only(self):
        p256 = enroll.FakeKeyPair("p256", algorithm="P-256")
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        with self.assertRaises(ValueError):
            enroll.build_preregistration_bundle("node-fixture", "fixture01", b"s" * 16, b"x" * 32, p256, policy)
        with self.assertRaises(ValueError):
            enroll.sign_claim(p256, enroll.SIGNING_DOMAIN + b"\x00node")
        with self.assertRaises(ValueError):
            enroll.canonical_context("n", "e", "c", b"n", "2026-01-01T00:00:00Z",
                                     ["CAP_A"], b"ch")
        with self.assertRaises(ValueError):
            enroll.canonical_context("n", "e", "c", b"n", "2026-01-01T00:00:00Z",
                                     ["NODE_CAPABILITY_DRAIN",
                                      "NODE_CAPABILITY_PRIVATE_SOCKET"], b"ch")

    def test_guest_script_compiles(self):
        compile(ENROLL_PATH.read_text(), str(ENROLL_PATH), "exec")


class SavedIdentityTests(unittest.TestCase):
    """Saved-identity validation and CLI reuse without guest OpenSSL."""

    def setUp(self):
        self.policy = json.loads(POLICY_PATH.read_text())

    def _save_identity(self, tmp, instance_id="instance-a", label="fixture-a"):
        out = Path(tmp) / "enrollment"
        out.mkdir(mode=0o700)
        key_path = out / "node.key"
        key_path.write_bytes(b"SYNTHETIC-ED25519-PRIVATE-KEY")
        if os.name == "posix":
            os.chmod(key_path, 0o600)
            os.chmod(out, 0o700)
        keypair = SavedFixtureKeyPair(key_path)
        secret = base64.urlsafe_b64encode(
            enroll.generate_secret(256, repeating(b"\x11")),
        ).rstrip(b"=")
        salt = enroll.generate_salt(16, repeating(b"\x22"))
        node_id = enroll.derive_node_id(instance_id)
        bundle, _, _ = enroll.build_preregistration_bundle(
            node_id, label, salt, secret, keypair, self.policy,
        )
        enroll.write_identity(bundle, secret, out)
        return out, node_id, bundle, secret

    @staticmethod
    def _snapshot(out):
        return {path.name: path.read_bytes() for path in out.iterdir()}

    @staticmethod
    def _rewrite_bundle(out, bundle):
        path = out / "enrollment-bundle.json"
        path.write_text(json.dumps(bundle, sort_keys=True, indent=2) + "\n")
        if os.name == "posix":
            # Keep the fixture private even on filesystems that reset modes.
            path.chmod(0o600)
            out.chmod(0o700)

    def _run_cli(self, out, instance_id="instance-a", label="fixture-a"):
        stdout = io.StringIO()
        argv = [
            "first-boot", "--policy", str(POLICY_PATH),
            "--instance-id", instance_id, "--node-label", label,
            "--out-dir", str(out),
        ]
        with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair), \
                contextlib.redirect_stdout(stdout):
            code = enroll.main(argv)
        return code, stdout.getvalue()

    def test_first_boot_then_reboot_reuses_saved_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            code, output = self._run_cli(out)
            self.assertEqual(code, 0)
            self.assertEqual(set(json.loads(output)), {
                "node_id", "approved_key_fingerprint", "bundle_path", "contract_ref",
            })
            first = self._snapshot(out)
            self.assertEqual(set(first), {
                "node.key", "enrollment-bundle.json", "enrollment-secret.bin",
            })

            code, reboot_output = self._run_cli(out)
            self.assertEqual(code, 0)
            self.assertEqual(self._snapshot(out), first)
            self.assertEqual(json.loads(output), json.loads(reboot_output))

    def test_cli_reuses_complete_saved_fixture_without_printing_secret(self):
        with tempfile.TemporaryDirectory() as tmp:
            out, node_id, bundle, secret = self._save_identity(tmp)
            before = self._snapshot(out)
            code, output = self._run_cli(out)
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output)["node_id"], node_id)
            self.assertEqual(json.loads(output)["bundle_path"],
                             str(out / "enrollment-bundle.json"))
            self.assertNotIn(secret.decode(), output)
            self.assertNotIn(base64.b64encode(secret).decode(), output)
            self.assertEqual(self._snapshot(out), before)

            with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                self.assertEqual(
                    enroll.load_identity(out, node_id, "fixture-a", self.policy), bundle,
                )

    def test_cli_refuses_saved_bundle_missing_csr_proof(self):
        for field in ("csr_pre", "csr_pre_signature"):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as tmp:
                out, _, _, _ = self._save_identity(tmp)
                path = out / "enrollment-bundle.json"
                bundle = json.loads(path.read_text())
                del bundle[field]
                self._rewrite_bundle(out, bundle)
                before = self._snapshot(out)
                with self.assertRaises(ValueError) as refused:
                    self._run_cli(out)
                self.assertIn(field, str(refused.exception))
                self.assertEqual(self._snapshot(out), before)

    def test_saved_record_requires_policy_for_reuse(self):
        with tempfile.TemporaryDirectory() as tmp:
            out, node_id, _, _ = self._save_identity(tmp)
            before = self._snapshot(out)
            with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                with self.assertRaises(ValueError) as refused:
                    enroll.load_identity(out, node_id, "fixture-a")
            self.assertIn("policy is required", str(refused.exception))
            self.assertEqual(self._snapshot(out), before)

    def test_saved_policy_fields_must_match_the_checked_in_policy(self):
        mutations = {
            "contract_ref": lambda b: b.update(contract_ref=b["contract_ref"] + " (fork)"),
            "object_id_binding": lambda b: b.update(object_id_binding="any node object id"),
            "bootstrap_scopes": lambda b: b.update(
                bootstrap_scopes=list(reversed(b["bootstrap_scopes"])),
            ),
            "contract_is_shared": lambda b: b.update(contract_is_shared=False),
            "fork_allowed": lambda b: b.update(fork_allowed=True),
        }
        for field, mutate in mutations.items():
            with self.subTest(field=field), tempfile.TemporaryDirectory() as tmp:
                out, node_id, _, secret = self._save_identity(tmp)
                path = out / "enrollment-bundle.json"
                bundle = json.loads(path.read_text())
                mutate(bundle)
                self._rewrite_bundle(out, bundle)
                before = self._snapshot(out)
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError) as refused:
                        enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertIn(field, str(refused.exception))
                self.assertNotIn(secret.decode(), str(refused.exception))
                self.assertEqual(self._snapshot(out), before)

    def test_policy_bootstrap_scopes_must_remain_the_bounded_pair(self):
        policy = json.loads(json.dumps(self.policy))
        policy["policy"]["bootstrap_scopes"] = ["ClaimEnrollment"]
        with self.assertRaises(ValueError) as refused:
            enroll.build_preregistration_bundle(
                "node-fixture", "fixture-a", b"\x22" * 16, b"secret",
                enroll.FakeKeyPair("fixture-key"), policy,
            )
        self.assertIn("bootstrap_scopes", str(refused.exception))

    def test_cli_refuses_a_record_that_changes_only_contract_ref(self):
        with tempfile.TemporaryDirectory() as tmp:
            out, _, _, _ = self._save_identity(tmp)
            path = out / "enrollment-bundle.json"
            bundle = json.loads(path.read_text())
            bundle["contract_ref"] = "ADR0019 / proto/carbonpanel/v1/other.proto"
            self._rewrite_bundle(out, bundle)
            before = self._snapshot(out)
            with self.assertRaises(ValueError) as refused:
                self._run_cli(out)
            self.assertIn("contract_ref", str(refused.exception))
            self.assertEqual(self._snapshot(out), before)

    def _write_pre_pr_legacy_identity(self, out, label="fixture-a"):
        """Write the pre-persisted-key record shape: raw token, no key, signed-JSON CSR."""
        out.mkdir(mode=0o700)
        legacy = enroll.FakeKeyPair("legacy-fixture")
        salt = enroll.generate_salt(16, repeating(b"\x22"))
        secret = enroll.generate_secret(256, repeating(b"\x11"))
        payload = {
            "node_label": label,
            "enrollment_salt": enroll._b64(salt),
            "enrollment_secret_digest": enroll.compute_digest(salt, secret),
            "approved_public_key_spki": enroll._b64(legacy.spki_der()),
            "approved_key_fingerprint": legacy.fingerprint(),
        }
        signed_json = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
        bundle = dict(payload)
        bundle["csr_pre"] = enroll._b64(signed_json)
        bundle["csr_pre_signature"] = enroll._b64(legacy.sign(signed_json))
        bundle["node_id"] = enroll.derive_node_id(
            label + "::" + str(self.policy.get("artifact_revision", "first-boot")),
        )
        bundle["bootstrap_scopes"] = ["BeginEnrollmentChallenge", "ClaimEnrollment"]
        bundle["object_id_binding"] = self.policy["policy"]["object_id_binding"]
        bundle["contract_ref"] = self.policy["contract_ref"]
        bundle["contract_is_shared"] = True
        bundle["fork_allowed"] = False
        rewrite_bundle(out, bundle)
        secret_path = out / "enrollment-secret.bin"
        secret_path.write_bytes(secret)
        if os.name == "posix":
            secret_path.chmod(0o600)
        return out

    def test_pre_pr_legacy_identity_is_refused_read_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = self._write_pre_pr_legacy_identity(Path(tmp) / "enrollment")
            self.assertFalse((out / "node.key").exists())
            before = snapshot_identity(out)
            with self.assertRaises(ValueError) as refused:
                self._run_cli(out)
            message = str(refused.exception)
            self.assertIn("node.key", message)
            self.assertIn("no automatic migration", message)
            self.assertEqual(snapshot_identity(out), before)
            self.assertFalse((out / "node.key").exists())

    def test_legacy_signed_json_csr_with_valid_proof_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            out, node_id, bundle, secret = self._save_identity(tmp)
            keypair = SavedFixtureKeyPair(out / "node.key")
            signed_json = json.dumps(
                {"node_label": "fixture-a"}, sort_keys=True, separators=(",", ":"),
            ).encode()
            bundle["csr_pre"] = enroll._b64(signed_json)
            bundle["csr_pre_signature"] = enroll._b64(keypair.sign(signed_json))
            rewrite_bundle(out, bundle)
            before = snapshot_identity(out)
            with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                with self.assertRaises(ValueError) as refused:
                    enroll.load_identity(out, node_id, "fixture-a", self.policy)
            self.assertIn("signed JSON", str(refused.exception))
            self.assertNotIn(secret.decode(), str(refused.exception))
            self.assertEqual(snapshot_identity(out), before)

    def test_oversized_identity_files_are_refused_without_writes(self):
        cases = {
            "bundle": lambda out: (out / "enrollment-bundle.json").write_bytes(
                b"x" * (enroll.MAX_BUNDLE_BYTES + 1),
            ),
            "key": lambda out: (out / "node.key").write_bytes(
                b"x" * (enroll.MAX_KEY_BYTES + 1),
            ),
        }
        for name, grow in cases.items():
            with self.subTest(file=name), tempfile.TemporaryDirectory() as tmp:
                out, node_id, _, _ = self._save_identity(tmp)
                grow(out)
                if os.name == "posix":
                    name_on_disk = "enrollment-bundle.json" if name == "bundle" else "node.key"
                    (out / name_on_disk).chmod(0o600)
                    out.chmod(0o700)
                before = self._snapshot(out)
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError) as refused:
                        enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertIn("size bound", str(refused.exception))
                self.assertEqual(self._snapshot(out), before)

    def test_missing_preregistration_fields_are_refused_without_writes(self):
        for field in sorted(enroll.REQUIRED_BUNDLE_FIELDS):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as tmp:
                out, node_id, _, secret = self._save_identity(tmp)
                path = out / "enrollment-bundle.json"
                bundle = json.loads(path.read_text())
                del bundle[field]
                self._rewrite_bundle(out, bundle)
                before = self._snapshot(out)
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError) as refused:
                        enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertIn(field, str(refused.exception))
                self.assertNotIn(secret.decode(), str(refused.exception))
                self.assertEqual(self._snapshot(out), before)

    def test_malformed_oversized_or_inconsistent_records_are_refused(self):
        cases = {
            "malformed base64": lambda b: b.update(csr_pre="not-base64!!"),
            "oversized base64": lambda b: b.update(
                csr_pre=base64.b64encode(b"x" * (enroll.MAX_CSR_PRE_BYTES + 1)).decode(),
            ),
            "short salt": lambda b: b.update(enrollment_salt=base64.b64encode(b"x").decode()),
            "oversized spki": lambda b: b.update(
                approved_public_key_spki=base64.b64encode(
                    b"x" * (enroll.MAX_SPKI_BYTES + 1),
                ).decode(),
            ),
            "wrong proof": lambda b: b.update(
                csr_pre_signature=base64.b64encode(b"\x00" * 64).decode(),
            ),
            "tampered csr": lambda b: b.update(
                csr_pre=base64.b64encode(base64.b64decode(b["csr_pre"]) + b"\x00").decode(),
            ),
        }
        for name, mutate in cases.items():
            with self.subTest(case=name), tempfile.TemporaryDirectory() as tmp:
                out, node_id, _, secret = self._save_identity(tmp)
                path = out / "enrollment-bundle.json"
                bundle = json.loads(path.read_text())
                mutate(bundle)
                self._rewrite_bundle(out, bundle)
                before = self._snapshot(out)
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError) as refused:
                        enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertNotIn(secret.decode(), str(refused.exception))
                self.assertEqual(self._snapshot(out), before)

    def test_malformed_secret_is_refused_without_writes(self):
        for value in (b"not-base64!!", b"A" * 100):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as tmp:
                out, node_id, _, _ = self._save_identity(tmp)
                path = out / "enrollment-secret.bin"
                path.write_bytes(value)
                if os.name == "posix":
                    path.chmod(0o600)
                before = self._snapshot(out)
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError):
                        enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertEqual(self._snapshot(out), before)


class DerRefusalTests(unittest.TestCase):
    """Direct benign DER refusals; no crypto operations or service use."""

    def test_long_form_length_rejects_redundant_leading_zero(self):
        value = b"x" * 128
        canonical = b"\x04\x81\x80" + value
        tag, content, length, end = enroll._der_tlv(canonical, 0, len(canonical))
        self.assertEqual((tag, content, length, end), (0x04, 3, 128, len(canonical)))

        redundant = b"\x04\x82\x00\x80" + value
        with self.assertRaises(ValueError) as refused:
            enroll._der_tlv(redundant, 0, len(redundant))
        self.assertIn("non-minimal DER length", str(refused.exception))

        redundant_wide = b"\x04\x83\x00\x01\x00" + b"x" * 256
        with self.assertRaises(ValueError) as refused:
            enroll._der_tlv(redundant_wide, 0, len(redundant_wide))
        self.assertIn("non-minimal DER length", str(refused.exception))

    def test_long_form_length_is_refused_below_short_form_threshold(self):
        for encoded in (b"\x04\x81\x7f", b"\x04\x82\x00\x7f"):
            with self.subTest(encoded=encoded):
                data = encoded + b"x" * 127
                with self.assertRaises(ValueError) as refused:
                    enroll._der_tlv(data, 0, len(data))
                self.assertIn("non-minimal DER length", str(refused.exception))

    @staticmethod
    def _minimal_csr(algorithm_identifier):
        info = b"\x30\x00"  # empty CertificationRequestInfo; only framing matters
        signature = b"\x03\x41\x00" + b"\x00" * enroll.ED25519_SIGNATURE_BYTES
        body = info + algorithm_identifier + signature
        return b"\x30" + bytes([len(body)]) + body

    def test_csr_signature_algorithm_accepts_absent_ed25519_parameters(self):
        csr = self._minimal_csr(b"\x30\x05\x06\x03\x2b\x65\x70")
        tbs, oid, signature = enroll._parse_csr_pre(csr)
        self.assertEqual(tbs, b"\x30\x00")
        self.assertEqual(oid, enroll.ED25519_OID)
        self.assertEqual(len(signature), enroll.ED25519_SIGNATURE_BYTES)

    def test_csr_signature_algorithm_rejects_remaining_parameters(self):
        # Ed25519 AlgorithmIdentifier with a redundant NULL parameters field.
        csr = self._minimal_csr(b"\x30\x07\x06\x03\x2b\x65\x70\x05\x00")
        with self.assertRaises(ValueError) as refused:
            enroll._parse_csr_pre(csr)
        self.assertIn("parameters are not permitted", str(refused.exception))


class IdentityPolicyTests(unittest.TestCase):
    """Offline first-boot policy bounds and no-write refusal."""

    def setUp(self):
        self.policy = json.loads(POLICY_PATH.read_text())

    @staticmethod
    def _bad_size_cases():
        return {
            "secret below minimum": ("generate_secret_bits", 128),
            "secret above maximum": ("generate_secret_bits", enroll.MAX_SECRET_BYTES * 8 + 8),
            "secret not byte aligned": ("generate_secret_bits", 257),
            "salt below minimum": ("generate_salt_bytes", 8),
            "salt above maximum": ("generate_salt_bytes", enroll.MAX_SALT_BYTES + 1),
        }

    def test_checked_in_and_qualified_identity_sizes_are_accepted(self):
        self.assertEqual(enroll._identity_sizes(self.policy), (32, 16))
        policy = json.loads(json.dumps(self.policy))
        policy["policy"]["first_boot"].update(
            generate_secret_bits=enroll.MAX_SECRET_BYTES * 8,
            generate_salt_bytes=enroll.MAX_SALT_BYTES,
        )
        self.assertEqual(
            enroll._identity_sizes(policy),
            (enroll.MAX_SECRET_BYTES, enroll.MAX_SALT_BYTES),
        )

    def test_first_boot_refuses_invalid_policy_sizes_before_entropy(self):
        for name, (field, value) in self._bad_size_cases().items():
            with self.subTest(case=name):
                policy = json.loads(json.dumps(self.policy))
                policy["policy"]["first_boot"][field] = value
                calls = []

                def rng(n):
                    calls.append(n)
                    return b"\x00" * n

                with self.assertRaises(ValueError):
                    enroll.first_boot_identity(
                        "instance-a", "fixture-a", policy,
                        enroll.FakeKeyPair("fixture-key"), rng=rng,
                    )
                self.assertEqual(calls, [])

    def test_invalid_policy_sizes_refuse_before_writing_identity(self):
        for name, (field, value) in self._bad_size_cases().items():
            with self.subTest(case=name), tempfile.TemporaryDirectory() as tmp:
                out = Path(tmp) / "enrollment"
                policy = json.loads(json.dumps(self.policy))
                policy["policy"]["first_boot"][field] = value
                with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                    with self.assertRaises(ValueError):
                        enroll.ensure_identity(out, "instance-a", "fixture-a", policy)
                self.assertFalse(out.exists())
                self.assertEqual(list(Path(tmp).iterdir()), [])

    def test_offline_first_boot_writes_reusable_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                bundle = enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            first = snapshot_identity(out)
            self.assertEqual(set(first), {
                "node.key", "enrollment-bundle.json", "enrollment-secret.bin",
            })
            node_id = enroll.derive_node_id("instance-a")
            with mock.patch.object(enroll, "OpenSSLKeyPair", SavedFixtureKeyPair):
                self.assertEqual(
                    enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy),
                    bundle,
                )
                self.assertEqual(
                    enroll.load_identity(out, node_id, "fixture-a", self.policy),
                    bundle,
                )
            self.assertEqual(snapshot_identity(out), first)


@unittest.skipUnless(sys.platform.startswith("linux") and shutil.which("openssl"),
                     "requires Linux guest OpenSSL")
class GuestIdentityTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads(POLICY_PATH.read_text())

    def test_persists_private_key_and_registerable_proof(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            bundle = enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            identity_bytes = {path.name: path.read_bytes() for path in out.iterdir()}
            self.assertEqual(set(identity_bytes), {
                "node.key", "enrollment-bundle.json", "enrollment-secret.bin",
            })
            self.assertEqual(
                enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy), bundle,
            )
            self.assertEqual({path.name: path.read_bytes() for path in out.iterdir()}, identity_bytes)
            with self.assertRaises(ValueError):
                enroll.ensure_identity(out, "instance-b", "fixture-a", self.policy)
            self.assertEqual({path.name: path.read_bytes() for path in out.iterdir()}, identity_bytes)

            secret = identity_bytes["enrollment-secret.bin"]
            self.assertEqual(len(base64.urlsafe_b64decode(secret + b"=")), 32)
            salt = base64.b64decode(bundle["enrollment_salt"])
            self.assertEqual(enroll.compute_digest(salt, secret), bundle["enrollment_secret_digest"])
            self.assertNotIn(secret, identity_bytes["enrollment-bundle.json"])
            self.assertNotIn(b"PRIVATE KEY", identity_bytes["enrollment-bundle.json"])

            csr = base64.b64decode(bundle["csr_pre"])
            subprocess.run(
                ["openssl", "req", "-inform", "DER", "-noout", "-verify"],
                input=csr, check=True, capture_output=True,
            )
            csr_pem_key = subprocess.run(
                ["openssl", "req", "-inform", "DER", "-noout", "-pubkey"],
                input=csr, check=True, capture_output=True,
            ).stdout
            csr_spki = subprocess.run(
                ["openssl", "pkey", "-pubin", "-outform", "DER"],
                input=csr_pem_key, check=True, capture_output=True,
            ).stdout
            key = enroll.OpenSSLKeyPair(out / "node.key")
            self.assertEqual(csr_spki, key.spki_der())
            self.assertEqual(csr_spki, base64.b64decode(bundle["approved_public_key_spki"]))
            message = b"carbon-panel-enroll-v1\x00node\x00enroll\x00claim\x00nonce\x00expiry\x00caps\x00channel"
            self.assertTrue(enroll.verify_claim(key, message, enroll.sign_claim(key, message)))

    def test_refuses_incomplete_preexisting_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            out.mkdir(mode=0o700)
            marker = out / "enrollment-bundle.json"
            marker.write_text("{}")
            with self.assertRaises(ValueError) as refused:
                enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            self.assertIn("missing node.key", str(refused.exception))
            self.assertIn("no automatic migration", str(refused.exception))
            self.assertEqual(list(out.iterdir()), [marker])
            self.assertEqual(marker.read_text(), "{}")

    def test_reuses_previously_qualified_complete_record_unchanged(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            bundle = enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            before = snapshot_identity(out)
            self.assertEqual(
                enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy), bundle,
            )
            self.assertEqual(snapshot_identity(out), before)
            self.assertEqual(
                enroll.load_identity(
                    out, enroll.derive_node_id("instance-a"), "fixture-a", self.policy,
                ),
                bundle,
            )
            self.assertEqual(snapshot_identity(out), before)

    def test_refuses_inconsistent_saved_csr_with_valid_detached_proof(self):
        def other_key_csr(csr, node_id, tmp):
            key_path = Path(tmp) / "other.key"
            subprocess.run(
                ["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(key_path)],
                check=True, capture_output=True,
            )
            return subprocess.run(
                ["openssl", "req", "-new", "-key", str(key_path), "-subj", "/CN=" + node_id,
                 "-outform", "DER"],
                check=True, capture_output=True,
            ).stdout

        cases = {
            "tampered self-signature": (
                lambda csr, node_id, tmp: csr[:-1] + bytes([csr[-1] ^ 1]),
                "self-signature does not verify with the approved key",
            ),
            "valid csr from another key": (
                other_key_csr,
                "does not match the approved key",
            ),
            "unrecognized signature algorithm": (
                lambda csr, node_id, tmp: replace_signature_algorithm_oid(
                    csr, bytes.fromhex("2b6571"),
                ),
                "signature algorithm is not Ed25519",
            ),
            "trailing der bytes": (
                lambda csr, node_id, tmp: csr + b"\x00",
                "not a single valid DER PKCS#10 request",
            ),
            "legacy signed json": (
                lambda csr, node_id, tmp: json.dumps(
                    {"node_label": "fixture-a"}, sort_keys=True, separators=(",", ":"),
                ).encode(),
                "legacy signed JSON",
            ),
        }
        for name, (mutate, expected) in cases.items():
            with self.subTest(case=name), tempfile.TemporaryDirectory() as tmp:
                out = Path(tmp) / "enrollment"
                bundle = enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
                node_id = bundle["node_id"]
                key = enroll.OpenSSLKeyPair(out / "node.key")
                csr = mutate(base64.b64decode(bundle["csr_pre"]), node_id, tmp)
                saved = json.loads((out / "enrollment-bundle.json").read_text())
                saved["csr_pre"] = base64.b64encode(csr).decode()
                # The detached proof is valid for these bytes; only the CSR
                # assertions or the approved-key binding can refuse them.
                saved["csr_pre_signature"] = base64.b64encode(key.sign(csr)).decode()
                rewrite_bundle(out, saved)
                before = snapshot_identity(out)
                with self.assertRaises(ValueError) as refused:
                    enroll.load_identity(out, node_id, "fixture-a", self.policy)
                self.assertIn(expected, str(refused.exception))
                self.assertEqual(snapshot_identity(out), before)

    def test_refuses_stored_key_that_is_not_ed25519(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "enrollment"
            bundle = enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            key_path = out / "node.key"
            key_path.unlink()
            subprocess.run(
                ["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout",
                 "-out", str(key_path)],
                check=True, capture_output=True,
            )
            os.chmod(key_path, 0o600)
            before = snapshot_identity(out)
            with self.assertRaises(ValueError) as refused:
                enroll.load_identity(out, bundle["node_id"], "fixture-a", self.policy)
            self.assertIn("not an Ed25519 key", str(refused.exception))
            self.assertEqual(snapshot_identity(out), before)


if __name__ == "__main__":
    unittest.main()
