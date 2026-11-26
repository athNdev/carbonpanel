# SPDX-License-Identifier: MPL-2.0
import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from scripts import node_vm_arm64_acceptance as acceptance

ROOT = Path(__file__).resolve().parent.parent
INSTANCE_A = "oss377-arm64-accept-0001"
INSTANCE_B = "oss377-arm64-accept-0002"
SEED_ISO_SHA = "c" * 64


def node_id_for(instance_id):
    return acceptance.derived_node_id(instance_id)


def seed_evidence(instance_id=INSTANCE_A):
    return {
        "instance_id": instance_id,
        "instance_id_sha256": hashlib.sha256(instance_id.encode("utf-8")).hexdigest(),
        "instance_id_source": "seed/nocloud/meta-data",
        "seed_iso": {"sha256": SEED_ISO_SHA, "bytes": 2048},
    }


def report(instance_id=INSTANCE_A, node_id=None, status="PASS"):
    return {
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
                "command": [
                    "qemu-system-aarch64", "-machine", "virt",
                    "-device", "virtio-net-pci,netdev=net0",
                    "-netdev", acceptance.RESTRICTED_NETDEV,
                ],
            },
            "serial_console": {
                "status": "SCANNED", "size": 1234,
                "cloud_init_finished": True,
            },
            "first_boot_identity_marker": {
                "status": status,
                "node_id": node_id or node_id_for(instance_id),
                "fingerprint": "a" * 64,
            },
            "serial_secret_scan": {"status": "NO_HIGH_SIGNAL_MARKERS"},
            "reboot_boundary": {"status": "OBSERVED"},
        },
    }


class ParseTests(unittest.TestCase):
    def test_parses_last_json_report(self):
        stdout = "noise\n" + json.dumps(report()) + "\n"
        self.assertEqual(acceptance.parse_helper_output(stdout)["gates"]["seed"]["status"], "PASS")

    def test_rejects_output_without_report(self):
        with self.assertRaises(acceptance.AcceptanceError):
            acceptance.parse_helper_output("not json\n")


class SeedEvidenceTests(unittest.TestCase):
    def _write_seed(self, root, instance_id=INSTANCE_A, iso=b"seed iso"):
        meta = root / "seed" / "nocloud" / "meta-data"
        meta.parent.mkdir(parents=True)
        meta.write_text(
            "# public metadata\ninstance-id: %s\nlocal-hostname: cp-node\n"
            % instance_id, encoding="utf-8")
        (root / "seed.iso").write_bytes(iso)

    def test_reads_instance_id_and_seed_iso_digest_from_the_generated_seed(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            self._write_seed(work)
            evidence = acceptance.collect_seed_evidence(work, INSTANCE_A)
        self.assertEqual(evidence["instance_id"], INSTANCE_A)
        self.assertEqual(evidence["instance_id_sha256"],
                         hashlib.sha256(INSTANCE_A.encode("utf-8")).hexdigest())
        self.assertEqual(evidence["instance_id_source"], "seed/nocloud/meta-data")
        self.assertEqual(evidence["seed_iso"]["sha256"],
                         hashlib.sha256(b"seed iso").hexdigest())
        self.assertEqual(evidence["seed_iso"]["bytes"], 8)

    def test_rejects_seed_instance_id_that_does_not_match_the_request(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            self._write_seed(work, instance_id=INSTANCE_B)
            with self.assertRaisesRegex(
                    acceptance.AcceptanceError, "does not match the requested"):
                acceptance.collect_seed_evidence(work, INSTANCE_A)

    def test_rejects_missing_seed_meta_data(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            (work / "seed.iso").write_bytes(b"seed iso")
            with self.assertRaisesRegex(
                    acceptance.AcceptanceError, "meta-data is missing"):
                acceptance.collect_seed_evidence(work, INSTANCE_A)

    def test_rejects_missing_seed_iso(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            self._write_seed(work)
            (work / "seed.iso").unlink()
            with self.assertRaisesRegex(
                    acceptance.AcceptanceError, "seed ISO is missing"):
                acceptance.collect_seed_evidence(work, INSTANCE_A)


class ValidationTests(unittest.TestCase):
    def test_accepts_completed_first_boot_with_restricted_network(self):
        result = acceptance.validate_instance_report(
            report(), "instance-1", seed_evidence())
        self.assertEqual(result["node_id"], node_id_for(INSTANCE_A))
        self.assertEqual(result["boot_attempt"]["status"], "COMPLETED")
        self.assertTrue(result["boot_attempt"]["network_restrict"])
        self.assertEqual(result["first_boot"]["reboot_boundary"], "OBSERVED")
        self.assertEqual(result["seed_iso"]["sha256"], SEED_ISO_SHA)

    def test_rejects_inconclusive_identity(self):
        with self.assertRaisesRegex(acceptance.AcceptanceError, "first-boot identity"):
            acceptance.validate_instance_report(
                report(status="INCONCLUSIVE"), "instance-1", seed_evidence())

    def test_rejects_bad_node_id(self):
        with self.assertRaisesRegex(acceptance.AcceptanceError, "invalid public node ID"):
            acceptance.validate_instance_report(
                report(node_id="node-NOT-HEX"), "instance-1", seed_evidence())

    def test_rejects_node_id_that_does_not_derive_from_the_seed_instance_id(self):
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "does not derive from the seed"):
            acceptance.validate_instance_report(
                report(node_id=node_id_for(INSTANCE_B)), "instance-1",
                seed_evidence(INSTANCE_A))

    def test_rejects_unclean_boot(self):
        payload = report()
        payload["gates"]["boot_attempt"]["returncode"] = 1
        with self.assertRaisesRegex(acceptance.AcceptanceError, "did not complete cleanly"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())

    def test_rejects_unrestricted_user_networking(self):
        payload = report()
        payload["gates"]["boot_attempt"]["command"] = [
            "qemu-system-aarch64", "-netdev", "user,id=net0"]
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "did not use restricted user networking"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())

    def test_rejects_a_host_forward(self):
        payload = report()
        payload["gates"]["boot_attempt"]["command"] = [
            "qemu-system-aarch64", "-netdev",
            "user,id=net0,restrict=on,hostfwd=tcp::2222-:22"]
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "host or guest forward"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())

    def test_rejects_missing_reboot_boundary(self):
        payload = report()
        payload["gates"]["reboot_boundary"]["status"] = "NOT_OBSERVED"
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "reboot boundary was not observed"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())

    def test_rejects_incomplete_first_boot(self):
        payload = report()
        payload["gates"]["serial_console"]["cloud_init_finished"] = False
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "did not complete cloud-init"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())

    def test_rejects_secret_marker_in_serial_scan(self):
        payload = report()
        payload["gates"]["serial_secret_scan"]["status"] = "FAIL"
        with self.assertRaisesRegex(acceptance.AcceptanceError, "serial secret scan"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence())


class ReceiptTests(unittest.TestCase):
    def _instance(self, instance_id=INSTANCE_A):
        return acceptance.validate_instance_report(
            report(instance_id=instance_id), "instance", seed_evidence(instance_id))

    def test_distinct_node_ids_are_required(self):
        first = self._instance(INSTANCE_A)
        second = dict(first, instance_id=INSTANCE_B,
                      instance_id_sha256=hashlib.sha256(
                          INSTANCE_B.encode("utf-8")).hexdigest())
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [first, second])
        receipt = json.loads(text)
        self.assertFalse(receipt["checks"]["distinct_node_ids"])
        self.assertTrue(receipt["checks"]["distinct_instance_ids"])
        self.assertEqual(receipt["checks"]["reboot_persistence"], "NOT_RUN")

    def test_receipt_records_seed_iso_and_first_boot_evidence(self):
        first = self._instance(INSTANCE_A)
        second = self._instance(INSTANCE_B)
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [first, second])
        receipt = json.loads(text)
        self.assertTrue(receipt["checks"]["distinct_node_ids"])
        self.assertTrue(receipt["checks"]["distinct_instance_ids"])
        self.assertTrue(receipt["checks"]["first_boot_completed"])
        self.assertTrue(receipt["checks"]["reboot_boundary_observed"])
        self.assertTrue(receipt["checks"]["restricted_user_network"])
        self.assertTrue(receipt["checks"]["seed_iso_sha256_recorded"])
        for instance in receipt["instances"]:
            self.assertEqual(len(instance["seed_iso"]["sha256"]), 64)
            self.assertEqual(instance["first_boot"]["reboot_boundary"], "OBSERVED")
            self.assertTrue(instance["first_boot"]["cloud_init_finished"])
            self.assertEqual(instance["instance_id_source"],
                             "seed/nocloud/meta-data")
        self.assertNotIn("serial-console.log", text)
        self.assertNotIn("PRIVATE KEY", text)

    def test_receipt_is_allowlisted_and_rejects_secret_markers(self):
        first = self._instance(INSTANCE_A)
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64}, {}, [first])
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
        self.assertFalse(receipt["checks"]["first_boot_completed"])
        self.assertFalse(receipt["checks"]["restricted_user_network"])


class ErrorSanitizationTests(unittest.TestCase):
    def test_sanitizes_newlines_paths_secrets_and_length(self):
        text = acceptance.sanitize_error(RuntimeError(
            "helper failed\nBEGIN PRIVATE KEY\nC:\\secret\\state\\key.pem\n"
            + "x" * 400))
        self.assertNotIn("\n", text)
        self.assertNotIn("PRIVATE KEY", text)
        self.assertNotIn("secret", text)
        self.assertLessEqual(len(text), acceptance.ERROR_MAX_CHARS)

    def test_keeps_a_useful_bounded_summary(self):
        self.assertEqual(
            acceptance.sanitize_error(RuntimeError("image mismatch")),
            "image mismatch")


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
            self.assertNotIn(str(work), proc.stderr)


if __name__ == "__main__":
    unittest.main()
