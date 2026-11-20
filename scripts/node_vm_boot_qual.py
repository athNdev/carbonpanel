# SPDX-License-Identifier: MPL-2.0
"""Disposable QEMU (TCG/KVM) boot qualification helper for the node VM artifact.

This driver automates an owned, disposable boot attempt of a verified QCOW2 base
image with a deterministic NoCloud seed. It is intended for use on a capable,
isolated runner (qemu-system with TCG is sufficient; KVM accelerates but is not
required). It never publishes artifacts, never touches live hosts or registries,
and never mutates the source image.

Evidence produced is explicit and gate-scoped:
  - image/seed/ISO preparation (offline, deterministic inputs)
  - QEMU availability and boot attempt
  - serial-console markers (cloud-init progress, first-boot enrollment stdout,
    reboot/power-state signals) without claiming secrets are absent unless the
    captured log is actually scanned
  - optional offline disk inspection after shutdown (guestfish/virt-cat/virt-ls)
    to verify first-boot bundle persistence and root-only secret separation

A gate that cannot be exercised is reported as BLOCKED or NOT_RUN with a reason;
it is never reported as passed.
"""
import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

try:  # works for `python scripts/...` and as an import target in tests
    from scripts import node_vm_build as build
    from scripts import node_vm_verify_image as verify_image
except ImportError:  # pragma: no cover - CLI path
    import node_vm_build as build
    import node_vm_verify_image as verify_image

# enroll.py prints this JSON object to stdout on success (see enroll.py main()).
# enroll.py prints with json.dumps defaults (spaces after ':' and ','), and the
# serial console prefixes each line with a kernel/cloud-init timestamp. Tolerate
# that whitespace so a real successful first boot is not reported INCONCLUSIVE.
ENROLL_STDOUT_RE = re.compile(
    r'\{\s*"approved_key_fingerprint"\s*:\s*"[0-9a-f]{64}"\s*,'
    r'\s*"bundle_path"\s*:\s*"[^"]+"\s*,'
    r'\s*"contract_ref"\s*:\s*"[^"]+"\s*,'
    r'\s*"node_id"\s*:\s*"node-[0-9a-f]{32}"\s*\}'
)
SECRET_MARKERS = (
    "BEGIN PRIVATE KEY",
    "BEGIN CERTIFICATE",
    "enrollment_secret:",
    "enrollment-secret.bin",  # filename may appear in logs; raw contents must not
)


class BootQualError(RuntimeError):
    pass


def which_any(*names):
    for name in names:
        path = shutil.which(name)
        if path:
            return path
    return None


def run(cmd, **kwargs):
    kwargs.setdefault("check", False)
    kwargs.setdefault("capture_output", True)
    kwargs.setdefault("text", True)
    return subprocess.run(cmd, **kwargs)


def make_seed_iso(seed_nocloud_dir, iso_path):
    """Create a CIDATA NoCloud ISO from a nocloud directory. Returns tool used."""
    nocloud = Path(seed_nocloud_dir)
    required = ["user-data", "meta-data", "network-config"]
    for name in required:
        if not (nocloud / name).is_file():
            raise BootQualError("missing seed file: %s" % (nocloud / name))
    iso_path = Path(iso_path)
    iso_path.parent.mkdir(parents=True, exist_ok=True)

    cloud_localds = which_any("cloud-localds")
    if cloud_localds:
        cmd = [cloud_localds, str(iso_path), str(nocloud / "user-data"),
               str(nocloud / "meta-data"), "--network-config",
               str(nocloud / "network-config")]
        proc = run(cmd)
        if proc.returncode == 0 and iso_path.is_file():
            return {"tool": "cloud-localds", "path": str(iso_path)}

    xorriso = which_any("xorriso")
    if xorriso:
        cmd = [xorriso, "-as", "mkisofs", "-output", str(iso_path),
               "-volid", "CIDATA", "-joliet", "-rock",
               str(nocloud / "user-data"), str(nocloud / "meta-data"),
               str(nocloud / "network-config")]
        proc = run(cmd)
        if proc.returncode == 0 and iso_path.is_file():
            return {"tool": "xorriso", "path": str(iso_path)}

    genisoimage = which_any("genisoimage", "mkisofs")
    if genisoimage:
        cmd = [genisoimage, "-output", str(iso_path), "-volid", "CIDATA",
               "-joliet", "-rock",
               str(nocloud / "user-data"), str(nocloud / "meta-data"),
               str(nocloud / "network-config")]
        proc = run(cmd)
        if proc.returncode == 0 and iso_path.is_file():
            return {"tool": Path(genisoimage).name, "path": str(iso_path)}

    raise BootQualError(
        "no ISO tool found (need cloud-localds, xorriso, or genisoimage/mkisofs)")


def prepare_overlay(image_path, work_dir):
    """Create a disposable qcow2 overlay/copy for boot; never mutate source."""
    image_path = Path(image_path)
    work_dir = Path(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    overlay = work_dir / "boot-disk.qcow2"
    qemu_img = which_any("qemu-img")
    if qemu_img:
        proc = run([qemu_img, "create", "-f", "qcow2", "-F", "qcow2",
                    "-b", str(image_path.resolve()), str(overlay)])
        if proc.returncode == 0 and overlay.is_file():
            return {"path": str(overlay), "mode": "backing", "tool": "qemu-img"}
        # fallback to full copy if backing fails (e.g. format quirks)
        shutil.copy2(image_path, overlay)
        return {"path": str(overlay), "mode": "copy", "tool": "copy-fallback"}
    shutil.copy2(image_path, overlay)
    return {"path": str(overlay), "mode": "copy", "tool": "copy"}


def qemu_command(disk_path, seed_iso, serial_log, arch="x86_64", memory_mib=2048,
                 cpus=2, accel=None):
    bin_name = "qemu-system-%s" % arch
    qemu = which_any(bin_name)
    if not qemu:
        raise BootQualError("%s not found on PATH" % bin_name)

    cmd = [
        qemu,
        "-m", str(memory_mib),
        "-smp", str(cpus),
        "-drive", "file=%s,if=virtio,format=qcow2" % disk_path,
        "-drive", "file=%s,if=virtio,format=raw,readonly=on,media=cdrom" % seed_iso,
        "-nographic",
        "-serial", "file:%s" % serial_log,
        "-monitor", "none",
        "-no-reboot",  # capture first-boot-to-reboot boundary as shutdown
        "-device", "virtio-net-pci,netdev=net0",
        "-netdev", "user,id=net0",
    ]
    if accel:
        cmd.extend(["-accel", accel])
    elif os.path.exists("/dev/kvm"):
        cmd.extend(["-enable-kvm"])
    else:
        cmd.extend(["-accel", "tcg"])
    return cmd


def scan_serial_log(path):
    """Best-effort marker scan; never claims secret absence beyond simple markers."""
    result = {
        "exists": False,
        "size": 0,
        "cloud_init_finished": False,
        "enroll_stdout_json": False,
        "enroll_stdout_match": None,
        "node_id": None,
        "fingerprint": None,
        "reboot_or_poweroff_signal": False,
        "simple_secret_markers_found": [],
    }
    p = Path(path)
    if not p.is_file():
        return result
    data = p.read_bytes()
    result["exists"] = True
    result["size"] = len(data)
    # tolerate mixed encodings from serial console
    text = data.decode("utf-8", errors="replace")
    result["cloud_init_finished"] = bool(
        re.search(r"Cloud-init.*finished", text, re.IGNORECASE)
        or re.search(r"Reached target.*Cloud-init target", text, re.IGNORECASE)
        or "cloud-init status: done" in text.lower()
    )
    m = ENROLL_STDOUT_RE.search(text)
    if m:
        result["enroll_stdout_json"] = True
        result["enroll_stdout_match"] = m.group(0)
        try:
            obj = json.loads(m.group(0))
            result["node_id"] = obj.get("node_id")
            result["fingerprint"] = obj.get("approved_key_fingerprint")
        except Exception:
            pass
    result["reboot_or_poweroff_signal"] = bool(
        re.search(r"\b(reboot|powering off|restarting system|system reboot)\b", text, re.IGNORECASE)
    )
    # Only flag high-signal literal secret material markers; absence is not proven.
    for marker in SECRET_MARKERS:
        if marker in text and marker != "enrollment-secret.bin":
            result["simple_secret_markers_found"].append(marker)
    return result


def try_offline_disk_inspect(disk_path):
    """Attempt read-only inspection of first-boot artifacts if guestfs tools exist."""
    out = {
        "attempted": False,
        "tool": None,
        "bundle_present": None,
        "secret_present": None,
        "bundle_json": None,
        "bundle_safe_shape": None,
        "note": None,
    }
    disk_path = str(disk_path)
    virt_cat = which_any("virt-cat")
    virt_ls = which_any("virt-ls")
    guestfish = which_any("guestfish")
    if not (virt_cat or (guestfish and virt_ls)):
        out["note"] = "no guestfs inspection tool available (virt-cat/virt-ls/guestfish)"
        return out

    out["attempted"] = True
    bundle_path = "/var/lib/carbon-node/enrollment/enrollment-bundle.json"
    secret_path = "/var/lib/carbon-node/enrollment/enrollment-secret.bin"

    def cat_file(p):
        if virt_cat:
            proc = run([virt_cat, "-a", disk_path, p])
            if proc.returncode == 0:
                return proc.stdout
        if guestfish:
            # guestfish --ro -a disk -i cat path
            proc = run(["guestfish", "--ro", "-a", disk_path, "-i", "cat", p])
            if proc.returncode == 0:
                return proc.stdout
        return None

    def ls_dir(p):
        if virt_ls:
            proc = run([virt_ls, "-a", disk_path, p])
            if proc.returncode == 0:
                return proc.stdout.strip().splitlines()
        if guestfish:
            proc = run(["guestfish", "--ro", "-a", disk_path, "-i", "ls", p])
            if proc.returncode == 0:
                return proc.stdout.strip().splitlines()
        return None

    out["tool"] = "virt-cat/virt-ls" if virt_cat or virt_ls else "guestfish"
    listing = ls_dir("/var/lib/carbon-node/enrollment")
    if listing is None:
        out["note"] = "could not list enrollment directory (image may not be inspectable or boot incomplete)"
        return out
    out["bundle_present"] = "enrollment-bundle.json" in listing
    out["secret_present"] = "enrollment-secret.bin" in listing
    if out["bundle_present"]:
        content = cat_file(bundle_path)
        if content is not None:
            try:
                obj = json.loads(content)
                out["bundle_json"] = {
                    "keys": sorted(obj.keys()),
                    "node_id": obj.get("node_id"),
                    "approved_key_fingerprint": obj.get("approved_key_fingerprint"),
                    "contract_ref": obj.get("contract_ref"),
                    "bootstrap_scopes": obj.get("bootstrap_scopes"),
                }
                forbidden = {"enrollment_secret", "secret", "private_key", "token", "password"}
                out["bundle_safe_shape"] = not (forbidden & set(obj.keys()))
            except Exception as e:
                out["note"] = "bundle present but not parseable JSON: %s" % e
    return out


def wait_for_process(proc, timeout_seconds, poll=2.0):
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        rc = proc.poll()
        if rc is not None:
            return {"timed_out": False, "returncode": rc}
        time.sleep(poll)
    try:
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=15)
    except Exception:
        pass
    return {"timed_out": True, "returncode": proc.poll()}


def run_boot_attempt(image_path, seed_nocloud_dir, work_dir, timeout_seconds=1800,
                     memory_mib=2048, cpus=2, arch="x86_64", accel=None,
                     do_offline_inspect=True, verify_ref=None, profile="artifact",
                     sha256sums_text=None, seed_iso_path=None):
    work_dir = Path(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    report = {
        "schema_version": 1,
        "image": str(image_path),
        "work_dir": str(work_dir),
        "arch": arch,
        "gates": {},
        "notes": [],
    }

    # Precondition: verify image bytes if requested
    if verify_ref:
        try:
            v = verify_image.verify_pinned_image(
                ROOT, image_path, verify_ref, profile=profile,
                checksums_text=sha256sums_text)
            report["gates"]["image_bytes_verified"] = {
                "status": "PASS", "detail": v}
        except Exception as e:
            report["gates"]["image_bytes_verified"] = {
                "status": "FAIL", "error": str(e)}
            return report
    else:
        report["gates"]["image_bytes_verified"] = {
            "status": "NOT_RUN", "reason": "no --verify-ref provided"}

    seed_nocloud_dir = Path(seed_nocloud_dir)
    if not seed_nocloud_dir.is_dir():
        report["gates"]["seed"] = {"status": "FAIL", "error": "seed nocloud dir missing"}
        return report
    report["gates"]["seed"] = {"status": "PASS", "path": str(seed_nocloud_dir)}

    iso_path = Path(seed_iso_path) if seed_iso_path else (work_dir / "seed.iso")
    if seed_iso_path and iso_path.is_file():
        iso_info = {"tool": "prebuilt", "path": str(iso_path)}
        report["gates"]["seed_iso"] = {"status": "PASS", **iso_info}
    else:
        try:
            iso_info = make_seed_iso(seed_nocloud_dir, iso_path)
            report["gates"]["seed_iso"] = {"status": "PASS", **iso_info}
        except Exception as e:
            report["gates"]["seed_iso"] = {"status": "FAIL", "error": str(e)}
            return report

    qemu_bin = which_any("qemu-system-%s" % arch)
    if not qemu_bin:
        report["gates"]["qemu_available"] = {
            "status": "BLOCKED",
            "reason": "qemu-system-%s not found; cannot attempt TCG/KVM boot" % arch,
        }
        report["gates"]["boot_attempt"] = {"status": "BLOCKED", "reason": "qemu unavailable"}
        return report
    report["gates"]["qemu_available"] = {"status": "PASS", "path": qemu_bin}

    try:
        disk_info = prepare_overlay(image_path, work_dir)
        report["gates"]["boot_disk"] = {"status": "PASS", **disk_info}
    except Exception as e:
        report["gates"]["boot_disk"] = {"status": "FAIL", "error": str(e)}
        return report

    serial_log = work_dir / "serial-console.log"
    try:
        cmd = qemu_command(disk_info["path"], iso_info["path"], str(serial_log),
                           arch=arch, memory_mib=memory_mib, cpus=cpus, accel=accel)
    except Exception as e:
        report["gates"]["boot_attempt"] = {"status": "FAIL", "error": str(e)}
        return report

    report["gates"]["boot_attempt"] = {
        "status": "ATTEMPTED",
        "command": cmd,
        "timeout_seconds": timeout_seconds,
        "accel": "kvm" if os.path.exists("/dev/kvm") and not accel else (accel or "tcg"),
        "serial_log": str(serial_log),
        "note": "qemu started; -no-reboot captures first boot-to-reboot as process exit",
    }
    (work_dir / "qemu-command.json").write_text(
        json.dumps(cmd, indent=2) + "\n")

    started = time.time()
    with open(work_dir / "qemu.stderr.log", "w", encoding="utf-8", errors="replace") as err:
        proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=err)
    wait_result = wait_for_process(proc, timeout_seconds)
    elapsed = round(time.time() - started, 2)
    report["gates"]["boot_attempt"].update({
        "elapsed_seconds": elapsed,
        **wait_result,
    })
    if wait_result["timed_out"]:
        report["gates"]["boot_attempt"]["status"] = "BLOCKED"
        report["gates"]["boot_attempt"]["reason"] = (
            "boot did not reach shutdown/reboot boundary within timeout (common under TCG)")
    elif wait_result["returncode"] == 0:
        report["gates"]["boot_attempt"]["status"] = "COMPLETED"
    else:
        report["gates"]["boot_attempt"]["status"] = "ATTEMPTED"
        report["gates"]["boot_attempt"]["reason"] = (
            "qemu exited non-zero (may still contain useful console evidence)")

    serial_scan = scan_serial_log(serial_log)
    report["gates"]["serial_console"] = {"status": "SCANNED", **serial_scan}

    # Interpret gates conservatively from markers
    if serial_scan.get("enroll_stdout_json"):
        report["gates"]["first_boot_identity_marker"] = {
            "status": "PASS", "node_id": serial_scan.get("node_id"),
            "fingerprint": serial_scan.get("fingerprint"),
            "evidence": "enroll.py stdout JSON observed in serial console",
        }
    elif serial_scan.get("cloud_init_finished"):
        report["gates"]["first_boot_identity_marker"] = {
            "status": "INCONCLUSIVE",
            "reason": "cloud-init finished marker seen but enroll stdout JSON not found in serial log; inspect cloud-init-output.log via offline disk inspection if available",
        }
    else:
        report["gates"]["first_boot_identity_marker"] = {
            "status": "NOT_OBSERVED",
            "reason": "no enroll stdout JSON or cloud-init-finished marker in captured serial log",
        }

    if serial_scan.get("simple_secret_markers_found"):
        report["gates"]["serial_secret_scan"] = {
            "status": "FAIL",
            "markers": serial_scan["simple_secret_markers_found"],
        }
    else:
        report["gates"]["serial_secret_scan"] = {
            "status": "NO_HIGH_SIGNAL_MARKERS",
            "note": "no PEM/assignment markers found in serial log; absence of all secret material not proven",
        }

    if do_offline_inspect:
        inspect = try_offline_disk_inspect(disk_info["path"])
        status = "INSPECTED" if inspect.get("attempted") else "NOT_RUN"
        report["gates"]["offline_disk_inspection"] = {"status": status, **inspect}
        if inspect.get("bundle_present") and inspect.get("secret_present") and inspect.get("bundle_safe_shape"):
            report["gates"]["bundle_persistence"] = {
                "status": "PASS",
                "evidence": "bundle and secret files present via offline inspection; bundle shape has no forbidden secret fields",
            }
        elif inspect.get("attempted") and inspect.get("bundle_present") is False:
            report["gates"]["bundle_persistence"] = {
                "status": "NOT_OBSERVED",
                "reason": "enrollment-bundle.json not found in offline inspection (boot may be incomplete or path differs)",
            }
        else:
            report["gates"]["bundle_persistence"] = {
                "status": "NOT_RUN",
                "reason": inspect.get("note") or "offline inspection unavailable/inconclusive",
            }
    else:
        report["gates"]["offline_disk_inspection"] = {"status": "SKIPPED"}
        report["gates"]["bundle_persistence"] = {"status": "NOT_RUN", "reason": "inspection skipped"}

    # Reboot persistence: with -no-reboot we only capture first boot boundary.
    # A full persistence proof requires a second boot phase without -no-reboot and
    # post-reboot inspection; this driver records that boundary conservatively.
    if wait_result.get("returncode") == 0 and serial_scan.get("reboot_or_poweroff_signal"):
        report["gates"]["reboot_boundary"] = {
            "status": "OBSERVED",
            "note": "qemu exited cleanly at first reboot/poweroff boundary (-no-reboot); second-boot persistence not automatically verified",
        }
        report["gates"]["reboot_persistence"] = {
            "status": "NOT_RUN",
            "reason": "requires second boot phase and post-reboot state inspection; not performed automatically",
        }
    else:
        report["gates"]["reboot_boundary"] = {"status": "NOT_OBSERVED"}
        report["gates"]["reboot_persistence"] = {
            "status": "NOT_RUN",
            "reason": "reboot boundary not observed or boot incomplete",
        }

    # Network/health: user-mode networking is configured; cloud-init DHCP and
    # general network health cannot be fully asserted from serial markers alone.
    report["gates"]["network_config"] = {
        "status": "CONFIGURED",
        "evidence": "seed network-config is DHCP-only; qemu user-mode netdev configured",
        "note": "effective network connectivity and daemon health not proven by this driver",
    }
    report["gates"]["service_health"] = {
        "status": "NOT_RUN",
        "reason": "no in-guest health probe; daemon installation/health depends on image contents beyond first-boot enrollment script",
    }

    (work_dir / "boot-qual-report.json").write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n")
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", help="path to a verified QCOW2 image")
    parser.add_argument("--seed-dir", help="path to nocloud seed directory (contains user-data, meta-data, network-config)")
    parser.add_argument("--seed-iso", help="use an existing CIDATA seed ISO instead of creating one (requires seed dir for gate reporting)")
    parser.add_argument("--work-dir", required=True, help="owned disposable work directory for disk overlay, ISO, logs, report")
    parser.add_argument("--timeout", type=int, default=1800, help="boot attempt timeout in seconds (default: 1800)")
    parser.add_argument("--memory-mib", type=int, default=2048)
    parser.add_argument("--cpus", type=int, default=2)
    parser.add_argument("--arch", default="x86_64")
    parser.add_argument("--accel", help="qemu accel (tcg or kvm); default: kvm if /dev/kvm else tcg")
    parser.add_argument("--no-offline-inspect", action="store_true")
    parser.add_argument("--verify-ref", help="base image ref to verify bytes against pinned provenance before boot")
    parser.add_argument("--profile", choices=("artifact", "fixture"), default="artifact")
    parser.add_argument("--sha256sums", help="path to published SHA256SUMS for byte verification")
    parser.add_argument("--generate-seed", action="store_true", help="generate deterministic seed into work-dir/seed using node_vm_build")
    parser.add_argument("--instance-id", default="oss377-boot-qual-0001")
    parser.add_argument("--node-label", default="oss377boot")
    args = parser.parse_args(argv)

    work_dir = Path(args.work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)

    seed_dir = args.seed_dir
    if args.generate_seed or not seed_dir:
        seed_out = work_dir / "seed"
        build.offline_build(ROOT, out_dir=str(seed_out), instance_id=args.instance_id,
                            node_label=args.node_label, check=True, profile="artifact")
        seed_dir = str(seed_out / "nocloud")

    if not args.image:
        # Preparation-only mode: produce seed/ISO/report without boot attempt.
        report = {
            "schema_version": 1,
            "mode": "prepare-only",
            "work_dir": str(work_dir),
            "gates": {"seed": {"status": "PASS", "path": seed_dir}},
        }
        iso_arg = Path(args.seed_iso) if args.seed_iso else None
        if iso_arg and iso_arg.is_file():
            report["gates"]["seed_iso"] = {"status": "PASS", "tool": "prebuilt", "path": str(iso_arg)}
        else:
            try:
                iso_info = make_seed_iso(seed_dir, work_dir / "seed.iso")
                report["gates"]["seed_iso"] = {"status": "PASS", **iso_info}
            except Exception as e:
                report["gates"]["seed_iso"] = {"status": "BLOCKED", "error": str(e)}
        qemu_bin = which_any("qemu-system-%s" % args.arch)
        report["gates"]["qemu_available"] = (
            {"status": "PASS", "path": qemu_bin} if qemu_bin else
            {"status": "BLOCKED", "reason": "qemu-system-%s not found" % args.arch}
        )
        report["gates"]["boot_attempt"] = {
            "status": "NOT_RUN", "reason": "no --image provided (prepare-only)"}
        (work_dir / "boot-qual-report.json").write_text(
            json.dumps(report, indent=2, sort_keys=True) + "\n")
        print(json.dumps(report, sort_keys=True))
        return 0

    sums = Path(args.sha256sums).read_text() if args.sha256sums else None
    report = run_boot_attempt(
        args.image, seed_dir, str(work_dir), timeout_seconds=args.timeout,
        memory_mib=args.memory_mib, cpus=args.cpus, arch=args.arch,
        accel=args.accel, do_offline_inspect=not args.no_offline_inspect,
        verify_ref=args.verify_ref, profile=args.profile, sha256sums_text=sums,
        seed_iso_path=args.seed_iso,
    )
    print(json.dumps(report, sort_keys=True))
    # Exit non-zero only for hard failures in preparation/verification inputs;
    # blocked/unobserved boot gates are reported in JSON for evidence.
    hard_fail = any(
        g.get("status") == "FAIL"
        for k, g in report.get("gates", {}).items()
        if k in ("image_bytes_verified", "seed", "seed_iso", "boot_disk")
    )
    return 1 if hard_fail else 0


if __name__ == "__main__":
    raise SystemExit(main())
