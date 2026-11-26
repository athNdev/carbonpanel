# SPDX-License-Identifier: MPL-2.0
import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

try:
    from scripts import node_vm_boot_qual as boot_qual
except ImportError:  # pragma: no cover
    import node_vm_boot_qual as boot_qual


def _write_seed(root):
    seed = Path(root) / "seed"
    seed.mkdir(parents=True, exist_ok=True)
    (seed / "user-data").write_text("#cloud-config\n", encoding="utf-8")
    (seed / "meta-data").write_text("instance-id: qual-fixture\n", encoding="utf-8")
    (seed / "network-config").write_text("version: 2\n", encoding="utf-8")
    return seed


def _fake_iso(seed_dir, iso_path):
    Path(iso_path).parent.mkdir(parents=True, exist_ok=True)
    Path(iso_path).write_bytes(b"CIDATA fixture")
    return {"tool": "fixture", "path": str(iso_path)}


class _FakeProc:
    def __init__(self, returncode=0):
        self.returncode = returncode

    def poll(self):
        return self.returncode

    def terminate(self):
        pass

    def kill(self):
        pass

    def wait(self, timeout=None):
        return self.returncode


def _run_main(argv):
    stdout = io.StringIO()
    stderr = io.StringIO()
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        rc = boot_qual.main(argv)
    return rc, stdout.getvalue(), stderr.getvalue()


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

    def test_reboot_boundary_requires_a_terminal_guest_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            serial = Path(directory) / "serial.log"
            serial.write_text(
                "[    1.234] systemd: reboot preparation service started\n"
                "Application is restarting system services\n",
                encoding="utf-8",
            )
            self.assertFalse(boot_qual.scan_serial_log(serial)["reboot_or_poweroff_signal"])
            with serial.open("a", encoding="utf-8") as handle:
                handle.write("[   41.012] reboot: Power down\r\n")
            self.assertTrue(boot_qual.scan_serial_log(serial)["reboot_or_poweroff_signal"])

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

    def test_qemu_command_default_user_network_keeps_generic_behavior(self):
        orig = boot_qual.which_any
        try:
            boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
            cmd = boot_qual.qemu_command(
                "/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                accel="tcg")
        finally:
            boot_qual.which_any = orig
        netdevs = [cmd[index + 1] for index, value in enumerate(cmd[:-1])
                   if value == "-netdev"]
        self.assertEqual(netdevs, ["user,id=net0"])
        self.assertFalse(any("hostfwd" in value for value in cmd))

    def test_qemu_command_restricted_user_network_has_no_forward(self):
        orig = boot_qual.which_any
        try:
            boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-x86_64" if "qemu-system-x86_64" in names else None
            cmd = boot_qual.qemu_command(
                "/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                accel="tcg", restrict_user_network=True)
        finally:
            boot_qual.which_any = orig
        netdevs = [cmd[index + 1] for index, value in enumerate(cmd[:-1])
                   if value == "-netdev"]
        self.assertEqual(netdevs, ["user,id=net0,restrict=on"])
        self.assertFalse(any("hostfwd" in value for value in cmd))
        self.assertFalse(any(value in ("-net", "-nic") for value in cmd))

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

    def test_aarch64_virt_uefi_selects_64bit_tcg_cpu(self):
        with tempfile.TemporaryDirectory() as directory:
            code = Path(directory) / "AAVMF_CODE.fd"
            variables = Path(directory) / "AAVMF_VARS.fd"
            code.write_bytes(b"code")
            variables.write_bytes(b"vars")
            orig = boot_qual.which_any
            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-aarch64" if "qemu-system-aarch64" in names else None
                cmd = boot_qual.qemu_command(
                    "/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                    arch="aarch64", accel="tcg", firmware="uefi",
                    uefi_code=str(code), uefi_vars=str(variables),
                    machine="virt")
            finally:
                boot_qual.which_any = orig
        self.assertIn("-cpu", cmd)
        self.assertEqual(cmd[cmd.index("-cpu") + 1], "max")
        self.assertIn("virt", cmd)

    def test_aarch64_kvm_cpu_defaults_to_host(self):
        self.assertEqual(boot_qual.default_cpu("aarch64", "kvm"), "host")
        self.assertEqual(boot_qual.default_cpu("aarch64", "tcg"), "max")
        self.assertIsNone(boot_qual.default_cpu("x86_64", "tcg"))

    def test_explicit_cpu_overrides_aarch64_default(self):
        with tempfile.TemporaryDirectory() as directory:
            code = Path(directory) / "AAVMF_CODE.fd"
            variables = Path(directory) / "AAVMF_VARS.fd"
            code.write_bytes(b"code")
            variables.write_bytes(b"vars")
            orig = boot_qual.which_any
            try:
                boot_qual.which_any = lambda *names: "/usr/bin/qemu-system-aarch64" if "qemu-system-aarch64" in names else None
                cmd = boot_qual.qemu_command(
                    "/tmp/disk.qcow2", "/tmp/seed.iso", "/tmp/serial.log",
                    arch="aarch64", accel="tcg", firmware="uefi",
                    uefi_code=str(code), uefi_vars=str(variables),
                    machine="virt", cpu="cortex-a72")
            finally:
                boot_qual.which_any = orig
        self.assertEqual(cmd[cmd.index("-cpu") + 1], "cortex-a72")

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

    def test_boot_attempt_refuses_reused_outputs_and_preserves_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            work.mkdir()
            prior = {
                "boot-disk.qcow2": b"prior boot state",
                "boot-qual-report.json": b'{"prior": true}\n',
                "seed.iso": b"prior seed iso",
            }
            for name, content in prior.items():
                (work / name).write_bytes(content)
            with self.assertRaisesRegex(boot_qual.BootQualError,
                                        "refusing to overwrite"):
                boot_qual.run_boot_attempt(
                    image, seed, work, firmware="bios",
                    do_offline_inspect=False)
            for name, content in prior.items():
                self.assertEqual((work / name).read_bytes(), content)
            self.assertFalse((work / "uefi-vars.fd").exists())
            self.assertFalse((work / boot_qual.ATTEMPT_MARKER).exists())


class ReservationTests(unittest.TestCase):
    def test_reserves_once_and_refuses_second_attempt(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory) / "work"
            marker = boot_qual.reserve_attempt_outputs(work)
            self.assertTrue(Path(marker).is_file())
            with self.assertRaisesRegex(boot_qual.BootQualError,
                                        "already reserved"):
                boot_qual.reserve_attempt_outputs(work)

    def test_refuses_prior_report_without_touching_it(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory) / "work"
            work.mkdir()
            report = work / "boot-qual-report.json"
            report.write_bytes(b'{"prior": true}\n')
            with self.assertRaisesRegex(boot_qual.BootQualError,
                                        "refusing to overwrite"):
                boot_qual.reserve_attempt_outputs(work)
            self.assertEqual(report.read_bytes(), b'{"prior": true}\n')
            self.assertFalse((work / boot_qual.ATTEMPT_MARKER).exists())


class TargetValidationTests(unittest.TestCase):
    def test_accepts_declared_targets(self):
        boot_qual.validate_target("x86_64", "uefi", "q35")
        boot_qual.validate_target("x86_64", "bios", "pc")
        boot_qual.validate_target("aarch64", "uefi", "virt")

    def test_rejects_bios_on_aarch64(self):
        with self.assertRaisesRegex(boot_qual.BootQualError,
                                    "not defined for aarch64"):
            boot_qual.validate_target("aarch64", "bios")

    def test_rejects_mismatched_machine(self):
        with self.assertRaisesRegex(boot_qual.BootQualError, "machine"):
            boot_qual.validate_target("aarch64", "uefi", "q35")
        with self.assertRaisesRegex(boot_qual.BootQualError, "machine"):
            boot_qual.validate_target("x86_64", "uefi", "virt")

    def test_rejects_unknown_architecture(self):
        with self.assertRaisesRegex(boot_qual.BootQualError,
                                    "unsupported architecture"):
            boot_qual.validate_target("riscv64", "uefi")


class CliTests(unittest.TestCase):
    def test_prepare_only_valid_fixture_succeeds(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            work = root / "work"
            with mock.patch.object(boot_qual, "make_seed_iso", new=_fake_iso):
                rc, _stdout, _stderr = _run_main([
                    "--seed-dir", str(seed), "--work-dir", str(work),
                    "--firmware", "bios",
                ])
            self.assertEqual(rc, 0)
            report = json.loads((work / "boot-qual-report.json").read_text())
            self.assertEqual(report["mode"], "prepare-only")
            self.assertEqual(report["gates"]["seed"]["status"], "PASS")
            self.assertEqual(report["gates"]["seed_iso"]["status"], "PASS")
            self.assertEqual(report["gates"]["firmware"]["status"], "PASS")
            self.assertEqual(report["gates"]["boot_attempt"]["status"], "NOT_RUN")
            self.assertTrue((work / boot_qual.ATTEMPT_MARKER).is_file())

    def test_prepare_only_accepts_prebuilt_seed_iso_as_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            work = root / "work"
            work.mkdir()
            iso = work / "seed.iso"
            iso.write_bytes(b"prebuilt CIDATA")
            rc, _stdout, _stderr = _run_main([
                "--seed-dir", str(seed), "--work-dir", str(work),
                "--seed-iso", str(iso), "--firmware", "bios",
            ])
            self.assertEqual(rc, 0)
            report = json.loads((work / "boot-qual-report.json").read_text())
            self.assertEqual(report["gates"]["seed_iso"]["tool"], "prebuilt")
            self.assertEqual(iso.read_bytes(), b"prebuilt CIDATA")

    def test_prepare_only_rejects_arm64_bios_before_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            work = root / "work"
            rc, _stdout, stderr = _run_main([
                "--seed-dir", str(seed), "--work-dir", str(work),
                "--arch", "aarch64", "--firmware", "bios",
            ])
            self.assertEqual(rc, boot_qual.EXIT_USAGE)
            self.assertIn("not defined for aarch64", stderr)
            self.assertFalse(work.exists())

    def test_boot_mode_rejects_arm64_bios_before_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            rc, _stdout, _stderr = _run_main([
                "--image", str(image), "--seed-dir", str(seed),
                "--work-dir", str(work), "--arch", "aarch64",
                "--firmware", "bios",
            ])
            self.assertEqual(rc, boot_qual.EXIT_USAGE)
            self.assertFalse(work.exists())

    def test_boot_mode_rejects_unsupported_machine_before_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            rc, _stdout, _stderr = _run_main([
                "--image", str(image), "--seed-dir", str(seed),
                "--work-dir", str(work), "--machine", "virt",
            ])
            self.assertEqual(rc, boot_qual.EXIT_USAGE)
            self.assertFalse(work.exists())

    def test_cli_refuses_reused_report_and_preserves_prior_seed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            work.mkdir()
            prior_report = b'{"prior": true}\n'
            (work / "boot-qual-report.json").write_bytes(prior_report)
            prior_seed = work / "seed"
            prior_seed.mkdir()
            prior_seed_file = prior_seed / "user-data"
            prior_seed_file.write_bytes(b"prior seed input")
            with mock.patch.object(
                    boot_qual.build, "offline_build",
                    side_effect=AssertionError("seed must not be regenerated")):
                rc, _stdout, stderr_text = _run_main([
                    "--work-dir", str(work), "--generate-seed",
                    "--firmware", "bios",
                ])
            self.assertEqual(rc, boot_qual.EXIT_USAGE)
            self.assertIn("refusing to overwrite", stderr_text)
            self.assertEqual((work / "boot-qual-report.json").read_bytes(),
                             prior_report)
            self.assertEqual(prior_seed_file.read_bytes(), b"prior seed input")
            self.assertFalse((work / boot_qual.ATTEMPT_MARKER).exists())

    def test_cli_refuses_reuse_before_replacing_seed_when_disk_exists(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            work.mkdir()
            prior_disk = b"prior boot state"
            prior_iso = b"prior seed iso"
            (work / "boot-disk.qcow2").write_bytes(prior_disk)
            (work / "seed.iso").write_bytes(prior_iso)
            with mock.patch.object(
                    boot_qual.build, "offline_build",
                    side_effect=AssertionError("seed must not be regenerated")):
                rc, _stdout, _stderr = _run_main([
                    "--image", str(image), "--work-dir", str(work),
                    "--generate-seed", "--firmware", "bios",
                ])
            self.assertEqual(rc, boot_qual.EXIT_USAGE)
            self.assertEqual((work / "boot-disk.qcow2").read_bytes(), prior_disk)
            self.assertEqual((work / "seed.iso").read_bytes(), prior_iso)
            self.assertFalse((work / "boot-qual-report.json").exists())

    def test_cli_qemu_launch_failure_persists_report_and_returns_nonzero(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            fake_qemu = root / "qemu-system-x86_64"
            fake_qemu.write_bytes(b"")

            def fake_which(*names):
                if "qemu-system-x86_64" in names:
                    return str(fake_qemu)
                return None

            with mock.patch.object(boot_qual, "which_any", new=fake_which), \
                    mock.patch.object(boot_qual, "make_seed_iso", new=_fake_iso), \
                    mock.patch.object(
                        boot_qual.subprocess, "Popen",
                        side_effect=OSError("fixture qemu refused to launch")):
                rc, _stdout, _stderr = _run_main([
                    "--image", str(image), "--seed-dir", str(seed),
                    "--work-dir", str(work), "--firmware", "bios",
                    "--no-offline-inspect",
                ])
            self.assertEqual(rc, boot_qual.EXIT_QUALIFICATION_FAILURE)
            report = json.loads((work / "boot-qual-report.json").read_text())
            attempt = report["gates"]["boot_attempt"]
            self.assertEqual(attempt["status"], "FAIL")
            self.assertIn("failed to launch qemu", attempt["error"])
            self.assertIn("fixture qemu refused to launch", attempt["error"])
            self.assertTrue((work / "qemu.stderr.log").is_file())

    def test_cli_restricted_network_is_recorded_for_a_uefi_boot(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            fake_qemu = root / "qemu-system-x86_64"
            fake_qemu.write_bytes(b"")
            code = root / "OVMF_CODE.fd"
            variables = root / "OVMF_VARS.fd"
            code.write_bytes(b"code")
            variables.write_bytes(b"vars")

            def fake_which(*names):
                if "qemu-system-x86_64" in names:
                    return str(fake_qemu)
                return None

            with mock.patch.object(boot_qual, "which_any", new=fake_which), \
                    mock.patch.object(boot_qual, "make_seed_iso", new=_fake_iso), \
                    mock.patch.object(
                        boot_qual, "find_uefi_firmware",
                        new=lambda arch: (str(code), str(variables))), \
                    mock.patch.object(boot_qual.subprocess, "Popen",
                                      return_value=_FakeProc(0)):
                rc, _stdout, _stderr = _run_main([
                    "--image", str(image), "--seed-dir", str(seed),
                    "--work-dir", str(work), "--firmware", "uefi",
                    "--restrict-user-network", "--no-offline-inspect",
                ])
            self.assertEqual(rc, 0)
            report = json.loads((work / "boot-qual-report.json").read_text())
            self.assertTrue(report["gates"]["boot_attempt"]["network_restrict"])
            self.assertFalse(
                report["gates"]["boot_attempt"]["network_host_forward"])
            command = json.loads((work / "qemu-command.json").read_text())
            netdevs = [command[index + 1]
                       for index, value in enumerate(command[:-1])
                       if value == "-netdev"]
            self.assertEqual(netdevs, ["user,id=net0,restrict=on"])
            self.assertFalse(any("hostfwd" in value for value in command))

    def test_cli_mocked_boot_completes_with_valid_semantics(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            seed = _write_seed(root)
            image = root / "base.qcow2"
            image.write_bytes(b"fixture")
            work = root / "work"
            fake_qemu = root / "qemu-system-x86_64"
            fake_qemu.write_bytes(b"")

            def fake_which(*names):
                if "qemu-system-x86_64" in names:
                    return str(fake_qemu)
                return None

            with mock.patch.object(boot_qual, "which_any", new=fake_which), \
                    mock.patch.object(boot_qual, "make_seed_iso", new=_fake_iso), \
                    mock.patch.object(boot_qual.subprocess, "Popen",
                                      return_value=_FakeProc(0)):
                rc, _stdout, _stderr = _run_main([
                    "--image", str(image), "--seed-dir", str(seed),
                    "--work-dir", str(work), "--firmware", "bios",
                    "--no-offline-inspect",
                ])
            self.assertEqual(rc, 0)
            report = json.loads((work / "boot-qual-report.json").read_text())
            self.assertEqual(report["gates"]["seed_iso"]["status"], "PASS")
            self.assertEqual(report["gates"]["boot_attempt"]["status"],
                             "COMPLETED")
            self.assertEqual(report["gates"]["boot_attempt"]["returncode"], 0)


if __name__ == "__main__":
    unittest.main()
