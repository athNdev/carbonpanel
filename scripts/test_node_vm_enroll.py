# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for first-boot identity and scoped enrollment material.

Includes guest CLI tests with throwaway OpenSSL keys when OpenSSL is available.
"""
import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

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
            with self.assertRaises(FileNotFoundError):
                enroll.ensure_identity(out, "instance-a", "fixture-a", self.policy)
            self.assertEqual(list(out.iterdir()), [marker])
            self.assertEqual(marker.read_text(), "{}")


if __name__ == "__main__":
    unittest.main()
