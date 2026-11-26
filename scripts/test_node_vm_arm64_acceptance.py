# SPDX-License-Identifier: MPL-2.0
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from scripts import node_vm_arm64_acceptance as acceptance

ROOT = Path(__file__).resolve().parent.parent


def report(node_id="node-0123456789abcdef0123456789abcdef", status="PASS"):
    return {
        "instance_id": "oss377-arm64-accept-0001",
        "gates": {
            "image_bytes_verified": {"status": "PASS"},
            "seed": {"status": "PASS"},
            "seed_iso": {"status": "PASS"},
            "firmware": {"status": "PASS"},
            "qemu_available": {"status": "PASS"},
            "boot_disk": {"status": "PASS"},
            "boot_attempt": {
                "status": "COMPLETED", "returncode": 0,
                "elapsed_seconds": 12.5, "cpu": "max", "accel": "tcg",
                "firmware": "uefi",
            },
            "first_boot_identity_marker": {
                "status": status,
                "node_id": node_id,
                "fingerprint": "a" * 64,
            },
            "serial_console": {"size": 1234},
            "serial_secret_scan": {"status": "NO_HIGH_SIGNAL_MARKERS"},
        },
    }


class ParseTests(unittest.TestCase):
    def test_parses_last_json_report(self):
        stdout = "noise\n" + json.dumps(report()) + "\n"
        self.assertEqual(acceptance.parse_helper_output(stdout)["gates"]["seed"]["status"], "PASS")

    def test_rejects_output_without_report(self):
        with self.assertRaises(acceptance.AcceptanceError):
            acceptance.parse_helper_output("not json\n")


class ValidationTests(unittest.TestCase):
    def test_accepts_completed_first_boot(self):
        result = acceptance.validate_instance_report(report(), "instance-1")
        self.assertEqual(result["node_id"], "node-0123456789abcdef0123456789abcdef")
        self.assertEqual(result["boot_attempt"]["status"], "COMPLETED")

    def test_rejects_inconclusive_identity(self):
        with self.assertRaisesRegex(acceptance.AcceptanceError, "first-boot identity"):
            acceptance.validate_instance_report(report(status="INCONCLUSIVE"), "instance-1")

    def test_rejects_bad_node_id(self):
        with self.assertRaisesRegex(acceptance.AcceptanceError, "invalid public node ID"):
            acceptance.validate_instance_report(report(node_id="node-NOT-HEX"), "instance-1")

    def test_rejects_unclean_boot(self):
        payload = report()
        payload["gates"]["boot_attempt"]["returncode"] = 1
        with self.assertRaisesRegex(acceptance.AcceptanceError, "did not complete cleanly"):
            acceptance.validate_instance_report(payload, "instance-1")

    def test_rejects_secret_marker_in_serial_scan(self):
        payload = report()
        payload["gates"]["serial_secret_scan"]["status"] = "FAIL"
        with self.assertRaisesRegex(acceptance.AcceptanceError, "serial secret scan"):
            acceptance.validate_instance_report(payload, "instance-1")


class ReceiptTests(unittest.TestCase):
    def test_distinct_node_ids_are_required(self):
        first = acceptance.validate_instance_report(report(), "instance-1")
        second = dict(first, instance_id="oss377-arm64-accept-0002")
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [first, second])
        receipt = json.loads(text)
        self.assertFalse(receipt["checks"]["distinct_node_ids"])
        self.assertEqual(receipt["checks"]["reboot_persistence"], "NOT_RUN")

    def test_receipt_is_allowlisted_and_rejects_secret_markers(self):
        first = acceptance.validate_instance_report(report(), "instance-1")
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [first])
        receipt = json.loads(text)
        self.assertNotIn("serial-console.log", text)
        self.assertNotIn("PRIVATE KEY", text)
        with self.assertRaisesRegex(acceptance.AcceptanceError, "secret markers"):
            acceptance.build_receipt(
                "a" * 40, "aarch64", {"sha256": "b" * 64}, {},
                [{"node_id": "BEGIN PRIVATE KEY"}])

    def test_failure_receipt_is_still_written_with_zero_checks(self):
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [], error="image mismatch")
        receipt = json.loads(text)
        self.assertEqual(receipt["error"], "image mismatch")
        self.assertFalse(receipt["checks"]["image_bytes_verified"])


class ShaTests(unittest.TestCase):
    def test_sha256_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "blob"
            path.write_bytes(b"abc")
            self.assertEqual(
                acceptance.sha256_file(path),
                "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")


class CliTests(unittest.TestCase):
    def test_help_runs_as_a_script_from_the_repository_root(self):
        proc = subprocess.run(
            [sys.executable, "scripts/node_vm_arm64_acceptance.py", "--help"],
            cwd=str(ROOT), capture_output=True, text=True, check=False)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("--receipt", proc.stdout)

    def test_mismatched_image_writes_a_failure_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "image").write_bytes(b"not the pinned image")
            (work / "SHA256SUMS").write_text("", encoding="utf-8")
            receipt = work / "receipt.json"
            proc = subprocess.run(
                [sys.executable, "scripts/node_vm_arm64_acceptance.py",
                 "--image", str(work / "image"),
                 "--sha256sums", str(work / "SHA256SUMS"),
                 "--workspace", str(work / "run"),
                 "--head", "0" * 40,
                 "--receipt", str(receipt)],
                cwd=str(ROOT), capture_output=True, text=True, check=False)
            self.assertEqual(proc.returncode, 1)
            data = json.loads(receipt.read_text(encoding="utf-8"))
            self.assertFalse(data["checks"]["image_bytes_verified"])
            self.assertIn("pinned SHA256", data["error"])


if __name__ == "__main__":
    unittest.main()
