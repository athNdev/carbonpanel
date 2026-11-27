# SPDX-License-Identifier: MPL-2.0
import contextlib
import hashlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

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


def expanded_report(instance_id=INSTANCE_A, disk_size="20G"):
    payload = report(instance_id=instance_id)
    size = acceptance.gpt.parse_disk_size(disk_size)
    payload["gates"]["boot_disk"] = {
        "status": "PASS",
        "mode": "expanded-gpt",
        "size": disk_size,
        "virtual_size": size,
        "base_virtual_size": 3_758_096_384,
        "base_image_unchanged": True,
        "gpt": {
            "status": "EXPANDED",
            "old_last_lba": 7_340_031,
            "new_last_lba": size // acceptance.gpt.SECTOR_SIZE - 1,
            "new_last_usable_lba": size // acceptance.gpt.SECTOR_SIZE - 34,
            "partition_count": 4,
        },
    }
    payload["gates"]["expanded_disk_guest"] = {
        "status": "PASS", "disk_size": disk_size, "failure_markers": [],
    }
    return payload


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

    def test_accepts_expanded_profile_with_normalized_gpt(self):
        result = acceptance.validate_instance_report(
            expanded_report(), "instance-1", seed_evidence(), disk_size="20G")
        self.assertEqual(result["expanded_disk"]["mode"], "expanded-gpt")
        self.assertEqual(result["expanded_disk"]["gpt_status"], "EXPANDED")
        self.assertEqual(result["expanded_disk"]["new_last_lba"], 41_943_039)
        self.assertEqual(result["expanded_disk"]["guest_observation"], "PASS")
        self.assertTrue(result["expanded_disk"]["base_image_unchanged"])

    def test_rejects_expanded_profile_without_the_guest_gate(self):
        payload = expanded_report()
        del payload["gates"]["expanded_disk_guest"]
        with self.assertRaisesRegex(acceptance.AcceptanceError, "missing gates"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")

    def test_rejects_an_unnormalized_expanded_boot_disk(self):
        payload = expanded_report()
        payload["gates"]["boot_disk"] = {"status": "PASS", "mode": "backing"}
        with self.assertRaisesRegex(acceptance.AcceptanceError, "not GPT-normalized"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")

    def test_rejects_an_expanded_disk_whose_base_changed(self):
        payload = expanded_report()
        payload["gates"]["boot_disk"]["base_image_unchanged"] = False
        with self.assertRaisesRegex(acceptance.AcceptanceError, "proven unchanged"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")

    def test_rejects_an_expanded_disk_with_the_wrong_virtual_size(self):
        payload = expanded_report()
        payload["gates"]["boot_disk"]["virtual_size"] = 3_758_096_384
        with self.assertRaisesRegex(acceptance.AcceptanceError, "virtual size"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")

    def test_rejects_a_gpt_backup_header_that_is_not_at_the_end(self):
        payload = expanded_report()
        payload["gates"]["boot_disk"]["gpt"]["new_last_lba"] = 7_340_031
        with self.assertRaisesRegex(acceptance.AcceptanceError, "expanded disk end"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")

    def test_rejects_expanded_guest_failure_markers(self):
        payload = expanded_report()
        payload["gates"]["expanded_disk_guest"] = {
            "status": "FAIL", "failure_markers": ["boot_label_failure"]}
        with self.assertRaisesRegex(
                acceptance.AcceptanceError, "expanded-disk guest observation"):
            acceptance.validate_instance_report(
                payload, "instance-1", seed_evidence(), disk_size="20G")


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

    def test_failure_receipt_preserves_completed_image_verification(self):
        text = acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64, "verified": True},
            {}, [], failed_phase="first_boot_1")
        receipt = json.loads(text)
        self.assertEqual(receipt["failed_phase"], "first_boot_1")
        self.assertTrue(receipt["checks"]["image_bytes_verified"])
        self.assertEqual(receipt["checks"]["first_boot_identity"], "FAIL")
        self.assertFalse(receipt["checks"]["first_boot_completed"])
        self.assertFalse(receipt["checks"]["restricted_user_network"])
        self.assertEqual(receipt["disk"]["profile"], "base")
        self.assertEqual(receipt["checks"]["expanded_disk_gpt"], "NOT_RUN")

    def test_expanded_receipt_records_the_gpt_profile_and_limits(self):
        first = acceptance.validate_instance_report(
            expanded_report(INSTANCE_A), "instance", seed_evidence(INSTANCE_A),
            disk_size="20G")
        second = acceptance.validate_instance_report(
            expanded_report(INSTANCE_B), "instance", seed_evidence(INSTANCE_B),
            disk_size="20G")
        receipt = json.loads(acceptance.build_receipt(
            "a" * 40, "aarch64", {"sha256": "b" * 64, "verified": True},
            {}, [first, second], disk_size="20G"))
        self.assertEqual(receipt["disk"],
                         {"profile": "expanded", "requested_size": "20G"})
        self.assertEqual(receipt["checks"]["expanded_disk_gpt"], "PASS")
        self.assertIn("growpart", receipt["limits"][0])
        self.assertEqual(receipt["instances"][0]["expanded_disk"]["new_last_lba"],
                         41_943_039)
        self.assertNotIn("serial-console.log", json.dumps(receipt))
        self.assertNotIn("PRIVATE KEY", json.dumps(receipt))

class ShaTests(unittest.TestCase):
    def test_sha256_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "blob"
            path.write_bytes(b"abc")
            self.assertEqual(
                acceptance.sha256_file(path),
                "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")


class InstanceCommandTests(unittest.TestCase):
    def _run(self, disk_size):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "run" / "instance-1"
            work.mkdir(parents=True)
            (work / "serial-console.log").write_bytes(b"serial evidence")
            args = SimpleNamespace(
                image=root / "image", sha256sums=root / "sums",
                workspace=root / "run", head="a" * 40,
                receipt=root / "receipt.json", boot_timeout=30,
                disk_size=disk_size)
            proc = subprocess.CompletedProcess(
                [], 0, json.dumps(report()) + "\n", "")
            with patch.object(acceptance.subprocess, "run",
                              return_value=proc) as run, \
                    patch.object(acceptance, "collect_seed_evidence",
                                 return_value=seed_evidence()), \
                    patch.object(acceptance, "validate_instance_report",
                                 return_value={"node_id": node_id_for(INSTANCE_A)}) as validate:
                result = acceptance.run_instance(args, INSTANCE_A, 1)
            return run.call_args.args[0], validate.call_args, result

    def test_base_profile_does_not_pass_a_disk_size(self):
        command, validate, result = self._run(None)
        self.assertNotIn("--disk-size", command)
        self.assertIsNone(validate.kwargs["disk_size"])
        self.assertIn("serial_console", result)

    def test_expanded_profile_forwards_the_disk_size(self):
        command, validate, _result = self._run("20G")
        self.assertIn("--disk-size", command)
        self.assertEqual(command[command.index("--disk-size") + 1], "20G")
        self.assertEqual(validate.kwargs["disk_size"], "20G")


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
            self.assertEqual(data["failed_phase"], "image_verification")
            self.assertEqual(data["checks"]["first_boot_identity"], "NOT_RUN")
            self.assertNotIn(str(work), proc.stderr)

    def test_unexpected_helper_error_never_reaches_outputs(self):
        opaque = "opaque-value-9424"
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            image = work / "firmware"
            sums = work / "sums"
            receipt = work / "receipt.json"
            image.write_bytes(b"fixture")
            sums.write_text("", encoding="utf-8")
            verified = {"sha256_actual": "a" * 64, "bytes": 7, "bytes_verified": True}
            stdout, stderr = io.StringIO(), io.StringIO()
            with (
                patch("scripts.node_vm_verify_image.verify_pinned_image", return_value=verified),
                patch("scripts.node_vm_boot_qual.find_uefi_firmware", return_value=(image, image)),
                patch.object(acceptance, "run_instance", side_effect=RuntimeError(opaque)),
                patch.object(acceptance.subprocess, "run", side_effect=FileNotFoundError),
                contextlib.redirect_stdout(stdout),
                contextlib.redirect_stderr(stderr),
            ):
                status = acceptance.main([
                    "--image", str(image), "--sha256sums", str(sums),
                    "--workspace", str(work / "runs"), "--head", "a" * 40,
                    "--receipt", str(receipt),
                ])
            self.assertEqual(status, 1)
            artifact = receipt.read_text(encoding="utf-8")
            self.assertEqual(json.loads(artifact)["failed_phase"], "first_boot_1")
            self.assertNotIn(opaque, stdout.getvalue() + stderr.getvalue() + artifact)


if __name__ == "__main__":
    unittest.main()
