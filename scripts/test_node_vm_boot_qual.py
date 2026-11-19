# SPDX-License-Identifier: MPL-2.0
import json
import os
import tempfile
import unittest
from pathlib import Path

try:
    from scripts import node_vm_boot_qual as boot_qual
except ImportError:  # pragma: no cover
    import node_vm_boot_qual as boot_qual


class SerialScanTests(unittest.TestCase):
    def test_missing_log(self):
        r = boot_qual.scan_serial_log("/nonexistent/serial.log")
        self.assertFalse(r["exists"])
        self.assertFalse(r["cloud_init_finished"])
        self.assertFalse(r["enroll_stdout_json"])

    def test_detects_cloud_init_and_enroll_stdout_and_reboot(self):
        fp = "a" * 64
        node_id = "node-" + "b" * 32
        enroll = json.dumps({
            "approved_key_fingerprint": fp,
            "bundle_path": "/var/lib/carbon-node/enrollment/enrollment-bundle.json",
            "contract_ref": "ADR0019 / proto/carbonpanel/v1/node_control.proto",
            "node_id": node_id,
        }, sort_keys=True, separators=(",", ":"))
        text = "\n".join([
            "Booting...",
            "[   10.0] Cloud-init v. 24.1 finished at Thu, 19 Nov 2026 12:00:00 +0000. Datasource DataSourceNoCloud [seed=/dev/vdb]",
            enroll,
            "reboot: Restarting system",
        ]) + "\n"
        with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as f:
            f.write(text)
            path = f.name
        try:
            r = boot_qual.scan_serial_log(path)
        finally:
            os.unlink(path)
        self.assertTrue(r["exists"])
        self.assertTrue(r["cloud_init_finished"])
        self.assertTrue(r["enroll_stdout_json"])
        self.assertEqual(r["node_id"], node_id)
        self.assertEqual(r["fingerprint"], fp)
        self.assertTrue(r["reboot_or_poweroff_signal"])
        self.assertEqual(r["simple_secret_markers_found"], [])

    def test_secret_marker_detection_is_high_signal_only(self):
        text = "BEGIN PRIVATE KEY\n-----END PRIVATE KEY-----\n"
        with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as f:
            f.write(text)
            path = f.name
        try:
            r = boot_qual.scan_serial_log(path)
        finally:
            os.unlink(path)
        self.assertIn("BEGIN PRIVATE KEY", r["simple_secret_markers_found"])


class IsoHelperTests(unittest.TestCase):
    def test_make_seed_iso_requires_files(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(boot_qual.BootQualError):
                boot_qual.make_seed_iso(d, os.path.join(d, "seed.iso"))


class CommandTests(unittest.TestCase):
    def test_qemu_command_includes_no_reboot_and_seed(self):
        # avoid depending on qemu being installed: call internal builder logic via
        # monkeypatching which_any only for the binary lookup
        orig = boot_qual.which_any
        try:
            boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
            cmd = boot_qual.qemu_command("/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log", accel="tcg")
        finally:
            boot_qual.which_any = orig
        self.assertIn("-no-reboot", cmd)
        self.assertTrue(any("seed.iso" in c for c in cmd))
        self.assertTrue(any(c.startswith("file:/tmp/serial.log") or c == "file:/tmp/serial.log" for c in cmd))
        self.assertIn("-accel", cmd)
        self.assertIn("tcg", cmd)


if __name__ == "__main__":
    unittest.main()
