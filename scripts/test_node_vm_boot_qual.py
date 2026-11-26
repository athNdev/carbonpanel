# SPDX-License-Identifier: MPL-2.0
import json
import os
import subprocess
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

    def test_detects_enroll_stdout_with_default_json_spacing(self):
        # Exact first-boot marker line from the 2026-11-19 boot-attempt-3
        # serial console. enroll.py uses json.dumps defaults (spaces after ':'
        # and ','), so a compact-only regex falsely reported INCONCLUSIVE for
        # a real successful boot where the marker was present in the log.
        line = (
            '[  377.903394] cloud-init[964]: {"approved_key_fingerprint": '
            '"244f267e2a63caed203d3218001f38cef356622a47b10f78fb8bdce9b90ca167", '
            '"bundle_path": "/var/lib/carbon-node/enrollment/enrollment-bundle.json", '
            '"contract_ref": "ADR0019 / proto/carbonpanel/v1/node_control.proto", '
            '"node_id": "node-94ed0fec32c64d9fb5236be6d57ec629"}'
        )
        with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as f:
            f.write(line + "\n")
            path = f.name
        try:
            r = boot_qual.scan_serial_log(path)
        finally:
            os.unlink(path)
        self.assertTrue(r["enroll_stdout_json"])
        self.assertEqual(r["node_id"], "node-94ed0fec32c64d9fb5236be6d57ec629")
        self.assertEqual(
            r["fingerprint"],
            "244f267e2a63caed203d3218001f38cef356622a47b10f78fb8bdce9b90ca167",
        )

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
        self.assertNotIn("-machine", cmd)

    def test_qemu_command_uefi_uses_pflash_and_machine(self):
        with tempfile.TemporaryDirectory() as directory:
            code = Path(directory) / "OVMF_CODE.fd"
            variables = Path(directory) / "OVMF_VARS.fd"
            code.write_bytes(b"code")
            variables.write_bytes(b"vars")
            orig = boot_qual.which_any
            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
                cmd = boot_qual.qemu_command(
                    "/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                    accel="tcg", firmware="uefi", uefi_code=str(code),
                    uefi_vars=str(variables), machine="q35",
                )
            finally:
                boot_qual.which_any = orig
        self.assertIn("-machine", cmd)
        self.assertIn("q35", cmd)
        self.assertIn("if=pflash,format=raw,readonly=on,file=%s" % code, cmd)
        self.assertIn("if=pflash,format=raw,file=%s" % variables, cmd)
        self.assertNotIn("-bios", cmd)

    def test_qemu_command_uefi_requires_firmware_files(self):
        orig = boot_qual.which_any
        try:
            boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
            with self.assertRaisesRegex(boot_qual.BootQualError, "requires OVMF"):
                boot_qual.qemu_command("/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                                       accel="tcg", firmware="uefi")
            with self.assertRaisesRegex(boot_qual.BootQualError, "firmware file missing"):
                boot_qual.qemu_command("/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                                       accel="tcg", firmware="uefi",
                                       uefi_code="/nonexistent/CODE.fd",
                                       uefi_vars="/nonexistent/VARS.fd")
        finally:
            boot_qual.which_any = orig

    def test_qemu_command_refuses_bios_on_aarch64(self):
        orig = boot_qual.which_any
        try:
            boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-aarch64" if "qemu-system-aarch64" in names else None
            with self.assertRaisesRegex(boot_qual.BootQualError, "not defined for aarch64"):
                boot_qual.qemu_command("/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                                       arch="aarch64", accel="tcg", firmware="bios")
        finally:
            boot_qual.which_any = orig

    def test_overlay_preserves_existing_run_disk(self):
        with tempfile.TemporaryDirectory() as directory:
            work_dir = Path(directory)
            source = work_dir / "base.qcow2"
            source.write_bytes(b"fixture base image")
            existing = work_dir / "boot-disk.qcow2"
            existing.write_bytes(b"prior first-boot state")
            with self.assertRaisesRegex(boot_qual.BootQualError, "boot disk already exists"):
                boot_qual.prepare_overlay(source, work_dir)
            self.assertEqual(source.read_bytes(), b"fixture base image")
            self.assertEqual(existing.read_bytes(), b"prior first-boot state")

    def test_expanded_overlay_requires_qemu_img(self):
        with tempfile.TemporaryDirectory() as directory:
            work_dir = Path(directory)
            source = work_dir / "base.qcow2"
            source.write_bytes(b"fixture base image")
            orig = boot_qual.which_any
            try:
                boot_qual.which_any = lambda *names: None
                with self.assertRaisesRegex(boot_qual.BootQualError, "qemu-img is required"):
                    boot_qual.prepare_overlay(source, work_dir, size="20G")
            finally:
                boot_qual.which_any = orig
            self.assertFalse((work_dir / "boot-disk.qcow2").exists())

    def test_expanded_overlay_passes_size_to_qemu_img(self):
        with tempfile.TemporaryDirectory() as directory:
            work_dir = Path(directory)
            source = work_dir / "base.qcow2"
            source.write_bytes(b"fixture base image")
            calls = []
            orig_which, orig_run = boot_qual.which_any, boot_qual.run

            def fake_run(cmd, **kwargs):
                calls.append(cmd)
                Path(cmd[-2]).write_bytes(b"qcow2 metadata")
                return subprocess.CompletedProcess(cmd, 0, "", "")

            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-img" if "qemu-img" in names else None
                boot_qual.run = fake_run
                info = boot_qual.prepare_overlay(source, work_dir, size="20G")
            finally:
                boot_qual.which_any, boot_qual.run = orig_which, orig_run
            self.assertEqual(info["size"], "20G")
            self.assertEqual(calls[0][-1], "20G")
            self.assertEqual(source.read_bytes(), b"fixture base image")


class FirmwareTests(unittest.TestCase):
    def test_env_override_resolves_firmware(self):
        with tempfile.TemporaryDirectory() as directory:
            code = Path(directory) / "CODE.fd"
            variables = Path(directory) / "VARS.fd"
            code.write_bytes(b"code")
            variables.write_bytes(b"vars")
            old_code = os.environ.get("CARBON_OVMF_CODE")
            old_vars = os.environ.get("CARBON_OVMF_VARS")
            try:
                os.environ["CARBON_OVMF_CODE"] = str(code)
                os.environ["CARBON_OVMF_VARS"] = str(variables)
                self.assertEqual(boot_qual.find_uefi_firmware("x86_64"),
                                 (str(code), str(variables)))
            finally:
                if old_code is None:
                    os.environ.pop("CARBON_OVMF_CODE", None)
                else:
                    os.environ["CARBON_OVMF_CODE"] = old_code
                if old_vars is None:
                    os.environ.pop("CARBON_OVMF_VARS", None)
                else:
                    os.environ["CARBON_OVMF_VARS"] = old_vars

    def test_partial_env_override_is_refused(self):
        old_code = os.environ.get("CARBON_OVMF_CODE")
        old_vars = os.environ.get("CARBON_OVMF_VARS")
        try:
            os.environ["CARBON_OVMF_CODE"] = "/tmp/CODE.fd"
            os.environ.pop("CARBON_OVMF_VARS", None)
            with self.assertRaisesRegex(boot_qual.BootQualError, "set both"):
                boot_qual.find_uefi_firmware("x86_64")
        finally:
            if old_code is None:
                os.environ.pop("CARBON_OVMF_CODE", None)
            else:
                os.environ["CARBON_OVMF_CODE"] = old_code
            if old_vars is None:
                os.environ.pop("CARBON_OVMF_VARS", None)
            else:
                os.environ["CARBON_OVMF_VARS"] = old_vars

    def test_prepare_uefi_vars_copies_template(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            template = root / "VARS.fd"
            template.write_bytes(b"template bytes")
            dest = boot_qual.prepare_uefi_vars(root / "work", template)
            self.assertEqual(Path(dest).read_bytes(), b"template bytes")
            self.assertEqual(template.read_bytes(), b"template bytes")
            if os.name == "posix":
                self.assertEqual(Path(dest).stat().st_mode & 0o777, 0o600)

    def test_prepare_uefi_vars_refuses_existing_store(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            template = root / "VARS.fd"
            template.write_bytes(b"template bytes")
            boot_qual.prepare_uefi_vars(root / "work", template)
            with self.assertRaisesRegex(boot_qual.BootQualError, "already exists"):
                boot_qual.prepare_uefi_vars(root / "work", template)

    def test_boot_attempt_blocks_uefi_without_firmware(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = root / "seed"
            seed.mkdir()
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            orig_which, orig_iso, orig_firmware = (
                boot_qual.which_any, boot_qual.make_seed_iso,
                boot_qual.find_uefi_firmware)

            def fake_iso(seed_dir, iso_path):
                Path(iso_path).write_bytes(b"iso")
                return {"tool": "fixture", "path": str(iso_path)}

            def no_firmware(arch="x86_64"):
                raise boot_qual.BootQualError("no UEFI firmware for %s" % arch)

            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
                boot_qual.make_seed_iso = fake_iso
                boot_qual.find_uefi_firmware = no_firmware
                report = boot_qual.run_boot_attempt(
                    image, seed, root / "work", firmware="uefi",
                    do_offline_inspect=False)
            finally:
                boot_qual.which_any = orig_which
                boot_qual.make_seed_iso = orig_iso
                boot_qual.find_uefi_firmware = orig_firmware
            self.assertEqual(report["gates"]["firmware"]["status"], "BLOCKED")
            self.assertEqual(report["gates"]["boot_attempt"]["status"], "BLOCKED")
            self.assertFalse((root / "work" / "boot-disk.qcow2").exists())
            written = json.loads((root / "work" / "boot-qual-report.json").read_text())
            self.assertEqual(written["gates"]["firmware"]["status"], "BLOCKED")

    def test_boot_attempt_refuses_prior_disk_before_firmware_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = root / "seed"
            seed.mkdir()
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            work.mkdir()
            (work / "boot-disk.qcow2").write_bytes(b"prior boot state")
            orig_which, orig_iso = boot_qual.which_any, boot_qual.make_seed_iso

            def fake_iso(seed_dir, iso_path):
                Path(iso_path).write_bytes(b"iso")
                return {"tool": "fixture", "path": str(iso_path)}

            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
                boot_qual.make_seed_iso = fake_iso
                report = boot_qual.run_boot_attempt(
                    image, seed, work, firmware="uefi",
                    do_offline_inspect=False)
            finally:
                boot_qual.which_any = orig_which
                boot_qual.make_seed_iso = orig_iso
            self.assertEqual(report["gates"]["boot_disk"]["status"], "FAIL")
            self.assertNotIn("firmware", report["gates"])
            self.assertEqual((work / "boot-disk.qcow2").read_bytes(),
                             b"prior boot state")
            self.assertFalse((work / "uefi-vars.fd").exists())
            written = json.loads((work / "boot-qual-report.json").read_text())
            self.assertEqual(written["gates"]["boot_disk"]["status"], "FAIL")


if __name__ == "__main__":
    unittest.main()
