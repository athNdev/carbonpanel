# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for real-image-byte verification (synthetic tiny files only).

No network, no ~600 MiB image, no hypervisor, no host or deployment is touched.
"""
import hashlib
from pathlib import Path
import tempfile
import unittest

from scripts import node_vm_build as build
from scripts import node_vm_verify_image as verify

ROOT = Path(__file__).resolve().parent.parent


def synthetic_pin(digest, size, name="ubuntu-24.04-server-cloudimg-amd64.img"):
    return {
        "ref": "ubuntu-24.04-amd64",
        "arch": "amd64",
        "expected_sha256": digest,
        "image_content_length": size,
        "image_url": "https://cloud-images.ubuntu.com/releases/noble/release-20261017/" + name,
        "digest_status": "verified",
    }


class ImageByteVerificationTests(unittest.TestCase):
    def test_streaming_sha256_and_size(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tiny.bin"
            payload = b"carbon-panel-oss377-byte-verification" * 7
            path.write_bytes(payload)
            digest, size = verify.sha256_file(path, chunk_size=3)
            self.assertEqual(digest, hashlib.sha256(payload).hexdigest())
            self.assertEqual(size, len(payload))

    def test_parse_sha256sums(self):
        text = (
            "AAAA  *ubuntu-24.04-server-cloudimg-amd64.img\n"
            "\n"
            "bbbb  ubuntu-24.04-server-cloudimg-arm64.img\n"
        )
        sums = verify.parse_sha256sums(text)
        self.assertEqual(sums["ubuntu-24.04-server-cloudimg-amd64.img"], "aaaa")
        self.assertEqual(sums["ubuntu-24.04-server-cloudimg-arm64.img"], "bbbb")

    def test_verify_image_bytes_ok(self):
        payload = b"synthetic-qcow2-bytes"
        digest = hashlib.sha256(payload).hexdigest()
        pin = synthetic_pin(digest, len(payload))
        sums = "%s *%s\n" % (digest, pin["image_url"].rsplit("/", 1)[-1])
        result = verify.verify_image_bytes(digest, len(payload), pin, sums)
        self.assertTrue(result["bytes_verified"])
        self.assertEqual(result["sha256_published"], digest)
        self.assertEqual(result["bytes"], len(payload))

    def test_wrong_bytes_or_size_refused(self):
        payload = b"synthetic-qcow2-bytes"
        digest = hashlib.sha256(payload).hexdigest()
        pin = synthetic_pin(digest, len(payload))
        with self.assertRaises(verify.VerificationError):
            verify.verify_image_bytes("0" * 64, len(payload), pin)
        with self.assertRaises(verify.VerificationError):
            verify.verify_image_bytes(digest, len(payload) + 1, pin)

    def test_published_checksum_mismatch_refused(self):
        payload = b"synthetic-qcow2-bytes"
        digest = hashlib.sha256(payload).hexdigest()
        pin = synthetic_pin(digest, len(payload))
        name = pin["image_url"].rsplit("/", 1)[-1]
        with self.assertRaises(verify.VerificationError):
            verify.verify_image_bytes(digest, len(payload), pin, "f" * 64 + " *" + name + "\n")
        with self.assertRaises(verify.VerificationError):
            verify.verify_image_bytes(digest, len(payload), pin, "d" * 64 + " *other.img\n")

    def test_fixture_profile_placeholder_digest_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "image.bin"
            path.write_bytes(b"real bytes cannot match a placeholder pin")
            with self.assertRaises(verify.VerificationError):
                verify.verify_pinned_image(ROOT, path, "ubuntu-24.04-amd64", profile="fixture")

    def test_checksum_file_chain(self):
        sums = b"synthetic SHA256SUMS bytes"
        sig = b"synthetic detached signature bytes"
        provenance = {"images": [{
            "checksum_file_sha256": hashlib.sha256(sums).hexdigest(),
            "signature_file_sha256": hashlib.sha256(sig).hexdigest(),
            "signing_key_fingerprint": "D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81",
        }]}
        result = verify.verify_checksum_file(provenance, sums, sig)
        self.assertTrue(result["file_bytes_verified"])
        with self.assertRaises(verify.VerificationError):
            verify.verify_checksum_file(provenance, sums + b"tampered", sig)
        # The real artifact pin must still pass its own fail-closed checks.
        _, _, _, real, _, _ = build.load_inputs(ROOT, "artifact")
        for image in real["images"]:
            self.assertTrue(build.verify_artifact_provenance(real, image))


if __name__ == "__main__":
    unittest.main()
