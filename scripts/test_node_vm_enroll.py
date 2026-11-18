# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for first-boot identity and scoped enrollment material.

Synthetic keys/RNG only. No openssl, no VM, no controller, no real credential.
"""
import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
ENROLL_PATH = ROOT / "packaging" / "node-vm" / "cloud-init" / "firstboot" / "enroll.py"


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
            "fixture01", salt, secret, key, policy,
        )
        self.assertEqual(secret, returned_secret)
        self.assertEqual(digest, hashlib.sha256(salt + secret).hexdigest())
        blob = json.dumps(bundle, sort_keys=True)
        self.assertNotIn(base64.b64encode(secret).decode(), blob)
        self.assertNotIn(secret.hex(), blob)
        self.assertNotIn("private_key", bundle)
        self.assertNotIn("enrollment_secret", bundle)
        self.assertEqual(bundle["bootstrap_scopes"], ["BeginEnrollmentChallenge", "ClaimEnrollment"])
        self.assertTrue(bundle["contract_is_shared"])
        self.assertFalse(bundle["fork_allowed"])

    def test_secret_leak_is_refused(self):
        key = enroll.FakeKeyPair("fixture-key")
        policy = json.loads((ROOT / "packaging/node-vm/config/enrollment-policy.json").read_text())
        secret = enroll.generate_secret(256, repeating(b"\x33"))
        salt = enroll.generate_salt(16, repeating(b"\x44"))
        bundle, _, _ = enroll.build_preregistration_bundle("fixture01", salt, secret, key, policy)
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
        bundle, _, _ = enroll.build_preregistration_bundle("fixture01", salt, secret, key, policy)
        with tempfile.TemporaryDirectory() as tmp:
            bundle_path, secret_path = enroll.write_identity(bundle, secret, tmp)
            bundle_data = Path(bundle_path).read_text()
            self.assertNotIn(secret.hex(), bundle_data)
            self.assertEqual(Path(secret_path).read_bytes(), secret)

    def test_claim_context_and_signature(self):
        key = enroll.FakeKeyPair("fixture-key")
        ctx = enroll.canonical_context("node-1", "enr-1", "ch-1", "nonce-1",
                                       "2026-01-01T00:00:00Z", ["CAP_A"], "channel-1")
        self.assertTrue(ctx.startswith(enroll.CONTEXT_PREFIX + b"\x00"))
        self.assertIn(b'"server_nonce":"nonce-1"', ctx)
        sig = enroll.sign_claim(key, ctx)
        self.assertEqual(sig, hashlib.sha256(b"SYNTHETIC-SIG::fixture-key::" + ctx).digest())
        with self.assertRaises(ValueError):
            enroll.sign_claim(key, b"bad-context")

    def test_guest_script_compiles(self):
        compile(ENROLL_PATH.read_text(), str(ENROLL_PATH), "exec")


if __name__ == "__main__":
    unittest.main()
