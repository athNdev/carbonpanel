# SPDX-License-Identifier: MPL-2.0
"""Disposable QEMU (TCG/KVM) boot qualification helper for the node VM artifact.

This driver automates an owned, disposable boot attempt of a verified QCOW2 base
image with a deterministic NoCloud seed. It is intended for use on a capable,
isolated runner (qemu-system with TCG is sufficient; KVM accelerates but is not
required). It never publishes artifacts, never touches live hosts or registries,
and never mutates the source image.

Evidence produced is explicit and gate-scoped:
  - image/seed/ISO preparation (offline, deterministic inputs)
  - firmware and QEMU availability; UEFI is the declared artifact target and a
    missing OVMF/edk2 install is reported BLOCKED rather than falling back to BIOS
  - boot attempt (optionally on an expanded, GPT-normalized standalone disk)
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
    from scripts import node_vm_gpt as gpt
    from scripts import node_vm_verify_image as verify_image
except ImportError:  # pragma: no cover - CLI path
    import node_vm_build as build
    import node_vm_gpt as gpt
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

# Signatures of a disk whose virtual size grew but whose GPT did not. The
# retained arm64 expanded-profile run recorded the alternate-header mismatch and
# the resulting BOOT/UEFI label timeouts before growpart could run.
GPT_BACKUP_MISMATCH_RE = re.compile(
    r"GPT: the backup GPT header is not at the end of the disk"
    r"|the alternate GPT header is not at the end of the disk"
    r"|GPT PMBR size mismatch"
    r"|backup GPT table is not on the end of the disk",
    re.IGNORECASE,
)
BOOT_LABEL_FAILURE_RE = re.compile(
    r"Timed out waiting for device[^\n]*/dev/disk/by-label/(?:BOOT|UEFI)"
    r"|Failed to mount[^\n]*/boot(?:/efi)?"
    r"|Dependency failed for[^\n]*(?:boot|efi)\.mount",
    re.IGNORECASE,
)
EMERGENCY_MODE_RE = re.compile(
    r"You are in emergency mode|Reached target Emergency Mode", re.IGNORECASE)


class BootQualError(RuntimeError):
    pass


EXIT_QUALIFICATION_FAILURE = 1
EXIT_USAGE = 2

# The helper only models targets the checked-in artifact configuration declares.
# BIOS is deliberately absent for aarch64: the declared arm64 target is UEFI on
# QEMU's virt machine.
SUPPORTED_TARGETS = {
    "x86_64": {
        "firmware": ("bios", "uefi"),
        "machines": ("pc", "q35", "isapc", "microvm"),
    },
    "aarch64": {
        "firmware": ("uefi",),
        "machines": ("virt",),
    },
}

ATTEMPT_MARKER = "boot-qual-attempt.json"
# Every path the helper may create in an attempt work directory. A prior attempt
# owns these bytes; refusing before the first write keeps its disk, seed and
# report intact.
ATTEMPT_OUTPUT_NAMES = (
    "boot-qual-report.json",
    "boot-disk.qcow2",
    "expanded.raw",
    "seed.iso",
    "seed",
    "uefi-vars.fd",
    "serial-console.log",
    "qemu.stderr.log",
    "qemu-command.json",
)


def _machine_matches(machine, names):
    return any(machine == name or machine.startswith(name + "-") for name in names)


def validate_target(arch, firmware, machine=None):
    """Reject unsupported architecture/firmware/machine combinations.

    This runs before any output is created so an invalid target cannot leave a
    misleading PASS firmware gate, a boot overlay or an overwritten report.
    """
    target = SUPPORTED_TARGETS.get(arch)
    if target is None:
        raise BootQualError(
            "unsupported architecture %r; supported: %s"
            % (arch, ", ".join(sorted(SUPPORTED_TARGETS))))
    if firmware not in ("bios", "uefi"):
        raise BootQualError("unknown firmware mode: %s" % firmware)
    if firmware not in target["firmware"]:
        if arch == "aarch64" and firmware == "bios":
            raise BootQualError("BIOS firmware is not defined for aarch64; use UEFI")
        raise BootQualError(
            "firmware %s is not supported for architecture %s" % (firmware, arch))
    if machine and not _machine_matches(machine, target["machines"]):
        raise BootQualError(
            "machine %r is not supported for %s; use one of: %s"
            % (machine, arch, ", ".join(target["machines"])))


def reserve_attempt_outputs(work_dir, extra_outputs=(), ignore=()):
    """Reserve a fresh output set before seed, disk or report mutations.

    Raises BootQualError without writing anything when a previous attempt owns
    any output path, so prior report/disk/seed bytes are preserved. Callers can
    pass an existing prebuilt seed ISO through ``ignore`` because it is an input,
    not an output.
    """
    work_dir = Path(work_dir)
    ignored = {Path(path).resolve() for path in ignore}
    candidates = [work_dir / name for name in ATTEMPT_OUTPUT_NAMES]
    candidates.extend(Path(path) for path in extra_outputs)

    conflicts = []
    seen = set()
    for candidate in candidates:
        resolved = candidate.resolve()
        if resolved in ignored or resolved in seen:
            continue
        seen.add(resolved)
        if candidate.exists() or candidate.is_symlink():
            conflicts.append(str(candidate))
    if conflicts:
        raise BootQualError(
            "work directory already contains a previous qualification attempt "
            "(%s); refusing to overwrite existing outputs; use a fresh --work-dir"
            % ", ".join(sorted(conflicts)))

    try:
        work_dir.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise BootQualError(
            "cannot create work directory %s: %s" % (work_dir, exc)) from exc

    marker = work_dir / ATTEMPT_MARKER
    try:
        descriptor = os.open(
            str(marker), os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError as exc:
        raise BootQualError(
            "work directory already reserved a qualification attempt (%s); "
            "refusing to reuse it; use a fresh --work-dir" % marker) from exc
    except OSError as exc:
        raise BootQualError(
            "cannot reserve qualification outputs in %s: %s"
            % (work_dir, exc)) from exc
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        json.dump(
            {"schema_version": 1, "purpose": "fresh per-attempt output reservation"},
            handle, sort_keys=True)
        handle.write("\n")
    return str(marker)


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


# OVMF/edk2 locations differ across distributions. The declared artifact target
# is UEFI, so a qualification run must not silently fall back to SeaBIOS.
UEFI_FIRMWARE_CANDIDATES = {
    "x86_64": (
        ("/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"),
        ("/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"),
        ("/usr/share/edk2/ovmf/OVMF_CODE.fd", "/usr/share/edk2/ovmf/OVMF_VARS.fd"),
        ("/usr/share/edk2/x64/OVMF_CODE.fd", "/usr/share/edk2/x64/OVMF_VARS.fd"),
        ("/usr/share/qemu/OVMF_CODE.fd", "/usr/share/qemu/OVMF_VARS.fd"),
    ),
    "aarch64": (
        ("/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"),
        ("/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
         "/usr/share/qemu-efi-aarch64/QEMU_VARS.fd"),
        ("/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw",
         "/usr/share/edk2/aarch64/QEMU_VARS.fd"),
        ("/usr/share/edk2/aarch64/QEMU_EFI.fd",
         "/usr/share/edk2/aarch64/QEMU_VARS.fd"),
    ),
}


def find_uefi_firmware(arch="x86_64"):
    """Resolve OVMF code and variable templates without touching system files.

    CARBON_OVMF_CODE/CARBON_OVMF_VARS override discovery for non-standard hosts.
    Returns (code_path, vars_template_path); raises BootQualError when the
    declared UEFI target cannot be satisfied.
    """
    env_code = os.environ.get("CARBON_OVMF_CODE")
    env_vars = os.environ.get("CARBON_OVMF_VARS")
    if env_code or env_vars:
        if not (env_code and env_vars):
            raise BootQualError(
                "set both CARBON_OVMF_CODE and CARBON_OVMF_VARS, or neither")
        candidates = ((env_code, env_vars),)
    else:
        candidates = UEFI_FIRMWARE_CANDIDATES.get(arch, ())
    for code, variables in candidates:
        if Path(code).is_file() and Path(variables).is_file():
            return str(code), str(variables)
    raise BootQualError(
        "no UEFI firmware for %s (set CARBON_OVMF_CODE/CARBON_OVMF_VARS or "
        "install OVMF/edk2); refusing to fall back to BIOS" % arch)


def prepare_uefi_vars(work_dir, vars_template, filename="uefi-vars.fd"):
    """Copy the OVMF variable template to a fresh private per-run file."""
    dest = Path(work_dir) / filename
    dest.parent.mkdir(parents=True, exist_ok=True)
    try:
        dest.touch(mode=0o600, exist_ok=False)
    except FileExistsError as exc:
        raise BootQualError(
            "UEFI variable store already exists; use a new work directory") from exc
    try:
        shutil.copyfile(vars_template, dest)
        os.chmod(dest, 0o600)
    except Exception:
        dest.unlink(missing_ok=True)
        raise
    return str(dest)


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


def _command_detail(stream):
    text = " ".join(str(stream or "").split())
    return (": " + text[:300]) if text else ""


def _qemu_img_info(qemu_img, path, label):
    proc = run([qemu_img, "info", "--output=json", str(path)])
    if proc.returncode != 0:
        raise BootQualError("%s: qemu-img info failed%s"
                           % (label, _command_detail(proc.stderr)))
    try:
        info = json.loads(proc.stdout)
    except (TypeError, ValueError) as exc:
        raise BootQualError("%s: qemu-img info returned invalid JSON: %s"
                           % (label, exc)) from exc
    if not isinstance(info, dict):
        raise BootQualError("%s: qemu-img info returned no image record" % label)
    return info


def prepare_expanded_disk(image_path, work_dir, size):
    """Create a standalone expanded qcow2 whose GPT describes its own size.

    Extending a qcow2 overlay's virtual size leaves the base image's GPT backup
    header at the old end and keeps ``alternate_lba``/``last_usable_lba`` at the
    old geometry. The guest then sees a 3.5 GiB table on a 20 GiB device and the
    early ``LABEL=BOOT``/``LABEL=UEFI`` mounts can fail before ``growpart`` runs.

    The pinned base is opened read-only by qemu-img and flattened into a raw copy
    in the owned work directory. Only that copy's protective MBR, primary header,
    relocated backup entry array and relocated backup header are rewritten; the
    base image is never opened for writing and its size/mtime are checked after
    the run. The guest still owns partition and filesystem growth.
    """
    image_path = Path(image_path).resolve()
    work_dir = Path(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    overlay = work_dir / "boot-disk.qcow2"
    raw = work_dir / "expanded.raw"
    if overlay.exists() or overlay.is_symlink():
        raise BootQualError("boot disk already exists; use a new work directory")
    if raw.exists() or raw.is_symlink():
        raise BootQualError("expanded raw disk already exists; use a new work directory")
    try:
        target_size = gpt.parse_disk_size(size)
    except gpt.GptError as exc:
        raise BootQualError(str(exc)) from exc
    qemu_img = which_any("qemu-img")
    if not qemu_img:
        raise BootQualError(
            "qemu-img is required to create an expanded boot disk of %s" % size)
    info = _qemu_img_info(qemu_img, image_path, "base image")
    if info.get("format") != "qcow2":
        raise BootQualError(
            "expanded boot requires a qcow2 base image, got %r" % info.get("format"))
    try:
        base_size = int(info["virtual-size"])
    except (KeyError, TypeError, ValueError) as exc:
        raise BootQualError("base image has no usable virtual size") from exc
    if target_size <= base_size:
        raise BootQualError(
            "requested disk size %s is not larger than the base image (%d bytes)"
            % (size, base_size))
    before = image_path.stat()
    try:
        proc = run([qemu_img, "convert", "-f", "qcow2", "-O", "raw",
                    str(image_path), str(raw)])
        if proc.returncode != 0 or not raw.is_file():
            raise BootQualError("qemu-img convert to raw failed%s"
                               % _command_detail(proc.stderr))
        try:
            expansion = gpt.expand_disk(raw, target_size)
        except gpt.GptError as exc:
            raise BootQualError("expanded GPT normalization failed: %s" % exc) from exc
        proc = run([qemu_img, "convert", "-f", "raw", "-O", "qcow2", "-S", "4k",
                    str(raw), str(overlay)])
        if proc.returncode != 0 or not overlay.is_file():
            raise BootQualError("qemu-img convert to qcow2 failed%s"
                               % _command_detail(proc.stderr))
        proc = run([qemu_img, "check", str(overlay)])
        if proc.returncode != 0:
            raise BootQualError("qemu-img check reported errors in the expanded disk%s"
                               % _command_detail(proc.stdout or proc.stderr))
        overlay_info = _qemu_img_info(qemu_img, overlay, "expanded disk")
        try:
            virtual_size = int(overlay_info["virtual-size"])
        except (KeyError, TypeError, ValueError) as exc:
            raise BootQualError("expanded disk has no usable virtual size") from exc
        if virtual_size != target_size:
            raise BootQualError(
                "expanded disk virtual size %d does not match the requested %d"
                % (virtual_size, target_size))
        after = image_path.stat()
        if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
            raise BootQualError("base image changed during expansion; refusing to boot")
        return {
            "path": str(overlay),
            "mode": "expanded-gpt",
            "tool": "qemu-img",
            "size": str(size),
            "virtual_size": target_size,
            "base_virtual_size": base_size,
            "base_image_unchanged": True,
            "gpt": expansion,
        }
    except Exception:
        raw.unlink(missing_ok=True)
        overlay.unlink(missing_ok=True)
        raise
    finally:
        raw.unlink(missing_ok=True)


def prepare_overlay(image_path, work_dir, size=None):
    """Create a disposable boot disk; never mutate the source image.

    Without a size, a qcow2 backing overlay keeps the base 3.5 GiB geometry and
    cost. With a size, the disk is flattened and its GPT is normalized for the
    new virtual size before the guest boots (see prepare_expanded_disk). A plain
    copy cannot change the virtual size, and silently booting the base size would
    hide the gate being qualified.
    """
    image_path = Path(image_path)
    work_dir = Path(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    if size:
        return prepare_expanded_disk(image_path, work_dir, size)
    overlay = work_dir / "boot-disk.qcow2"
    try:
        # qemu-img create can truncate its output. Reserve a new file before
        # invoking it so a rerun cannot replace an existing boot disk.
        overlay.touch(mode=0o600, exist_ok=False)
    except FileExistsError as exc:
        raise BootQualError("boot disk already exists; use a new work directory") from exc
    try:
        qemu_img = which_any("qemu-img")
        if qemu_img:
            cmd = [qemu_img, "create", "-f", "qcow2", "-F", "qcow2",
                   "-b", str(image_path.resolve()), str(overlay)]
            proc = run(cmd)
            if proc.returncode == 0 and overlay.stat().st_size > 0:
                return {"path": str(overlay), "mode": "backing", "tool": "qemu-img"}
            # fallback to full copy if backing fails (e.g. format quirks)
        shutil.copy2(image_path, overlay)
        os.chmod(overlay, 0o600)
        return {"path": str(overlay), "mode": "copy",
                "tool": "copy-fallback" if qemu_img else "copy"}
    except Exception:
        overlay.unlink(missing_ok=True)
        raise


def default_cpu(arch, accel=None):
    """Return the CPU model required by the declared target.

    QEMU's ``virt`` machine defaults to a 32-bit ARM CPU, so an aarch64 guest
    only boots when a 64-bit model is selected explicitly. ``max`` is the
    portable TCG choice; KVM requires ``host``.
    """
    if arch != "aarch64":
        return None
    use_kvm = accel == "kvm" or (accel is None and os.path.exists("/dev/kvm"))
    return "host" if use_kvm else "max"


def qemu_command(disk_path, seed_iso, serial_log, arch="x86_64", memory_mib=2048,
                 cpus=2, accel=None, firmware="bios", uefi_code=None,
                 uefi_vars=None, machine=None, cpu=None,
                 restrict_user_network=False):
    validate_target(arch, firmware, machine)
    bin_name = "qemu-system-%s" % arch
    qemu = which_any(bin_name)
    if not qemu:
        raise BootQualError("%s not found on PATH" % bin_name)

    cmd = [
        qemu,
        "-m", str(memory_mib),
        "-smp", str(cpus),
    ]
    if cpu is None:
        cpu = default_cpu(arch, accel)
    if cpu:
        cmd.extend(["-cpu", cpu])
    if machine:
        cmd.extend(["-machine", machine])
    # Restricted user-mode networking adds no host forward and gives the guest
    # no route beyond the QEMU NAT. The generic helper keeps plain user-mode
    # networking unless the caller explicitly asks for the isolated mode.
    netdev = "user,id=net0"
    if restrict_user_network:
        netdev += ",restrict=on"
    cmd.extend([
        "-drive", "file=%s,if=virtio,format=qcow2" % disk_path,
        "-drive", "file=%s,if=virtio,format=raw,readonly=on,media=cdrom" % seed_iso,
        "-nographic",
        "-serial", "file:%s" % serial_log,
        "-monitor", "none",
        "-no-reboot",  # capture first-boot-to-reboot boundary as shutdown
        "-device", "virtio-net-pci,netdev=net0",
        "-netdev", netdev,
    ])
    if firmware == "uefi":
        if arch not in ("x86_64", "aarch64"):
            raise BootQualError("UEFI boot is not defined for arch %s" % arch)
        if not (uefi_code and uefi_vars):
            raise BootQualError("UEFI boot requires OVMF code and variable files")
        for label, path in (("code", uefi_code), ("vars", uefi_vars)):
            if not Path(path).is_file():
                raise BootQualError("UEFI %s firmware file missing: %s" % (label, path))
        cmd.extend([
            "-drive", "if=pflash,format=raw,readonly=on,file=%s" % uefi_code,
            "-drive", "if=pflash,format=raw,file=%s" % uefi_vars,
        ])
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
        "gpt_backup_header_mismatch": False,
        "boot_label_failure": False,
        "emergency_mode": False,
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
        re.search(r"^\s*(?:\[\s*\d+\.\d+\]\s*)?"
                  r"reboot: (?:Restarting system|Power down)\s*$",
                  text, re.MULTILINE)
    )
    result["gpt_backup_header_mismatch"] = bool(GPT_BACKUP_MISMATCH_RE.search(text))
    result["boot_label_failure"] = bool(BOOT_LABEL_FAILURE_RE.search(text))
    result["emergency_mode"] = bool(EMERGENCY_MODE_RE.search(text))
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
                     sha256sums_text=None, seed_iso_path=None, firmware="uefi",
                     disk_size=None, machine=None, cpu=None,
                     restrict_user_network=False, output_reservation=None):
    work_dir = Path(work_dir)
    validate_target(arch, firmware, machine)

    iso_path = Path(seed_iso_path) if seed_iso_path else (work_dir / "seed.iso")
    prebuilt_iso = bool(seed_iso_path and iso_path.is_file())
    if output_reservation is None:
        output_reservation = reserve_attempt_outputs(
            work_dir,
            extra_outputs=() if prebuilt_iso else (iso_path,),
            ignore=(iso_path,) if prebuilt_iso else (),
        )
    report = {
        "schema_version": 1,
        "image": str(image_path),
        "work_dir": str(work_dir),
        "arch": arch,
        "gates": {},
        "notes": [],
    }

    def _finish():
        # Write evidence for blocked/inconclusive runs too, not only full boots.
        (work_dir / "boot-qual-report.json").write_text(
            json.dumps(report, indent=2, sort_keys=True) + "\n")
        return report

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
            return _finish()
    else:
        report["gates"]["image_bytes_verified"] = {
            "status": "NOT_RUN", "reason": "no --verify-ref provided"}

    seed_nocloud_dir = Path(seed_nocloud_dir)
    if not seed_nocloud_dir.is_dir():
        report["gates"]["seed"] = {"status": "FAIL", "error": "seed nocloud dir missing"}
        return _finish()
    report["gates"]["seed"] = {"status": "PASS", "path": str(seed_nocloud_dir)}

    if prebuilt_iso:
        iso_info = {"tool": "prebuilt", "path": str(iso_path)}
        report["gates"]["seed_iso"] = {"status": "PASS", **iso_info}
    else:
        try:
            iso_info = make_seed_iso(seed_nocloud_dir, iso_path)
            report["gates"]["seed_iso"] = {"status": "PASS", **iso_info}
        except Exception as e:
            report["gates"]["seed_iso"] = {"status": "FAIL", "error": str(e)}
            return _finish()

    qemu_bin = which_any("qemu-system-%s" % arch)
    if not qemu_bin:
        report["gates"]["qemu_available"] = {
            "status": "BLOCKED",
            "reason": "qemu-system-%s not found; cannot attempt TCG/KVM boot" % arch,
        }
        report["gates"]["boot_attempt"] = {"status": "BLOCKED", "reason": "qemu unavailable"}
        return _finish()
    report["gates"]["qemu_available"] = {"status": "PASS", "path": qemu_bin}

    if (work_dir / "boot-disk.qcow2").exists():
        report["gates"]["boot_disk"] = {
            "status": "FAIL",
            "error": "boot disk already exists; use a new work directory",
        }
        return _finish()

    uefi_code = uefi_vars = None
    if firmware == "uefi":
        try:
            uefi_code, vars_template = find_uefi_firmware(arch)
            uefi_vars = prepare_uefi_vars(work_dir, vars_template)
            report["gates"]["firmware"] = {
                "status": "PASS",
                "mode": "uefi",
                "code": uefi_code,
                "vars_template": vars_template,
                "vars_copy": uefi_vars,
                "note": "fresh private variable store per run; system firmware is never written",
            }
        except Exception as e:
            report["gates"]["firmware"] = {
                "status": "BLOCKED", "mode": "uefi", "reason": str(e)}
            report["gates"]["boot_attempt"] = {
                "status": "BLOCKED", "reason": "UEFI firmware unavailable"}
            return _finish()
    elif firmware == "bios":
        report["gates"]["firmware"] = {
            "status": "PASS",
            "mode": "bios",
            "note": "BIOS is not the declared artifact target; use it only for diagnosis",
        }
    else:
        report["gates"]["firmware"] = {
            "status": "FAIL", "error": "unknown firmware mode: %s" % firmware}
        return _finish()

    try:
        disk_info = prepare_overlay(image_path, work_dir, size=disk_size)
        report["gates"]["boot_disk"] = {"status": "PASS", **disk_info}
    except Exception as e:
        report["gates"]["boot_disk"] = {"status": "FAIL", "error": str(e)}
        return _finish()

    serial_log = work_dir / "serial-console.log"
    try:
        cmd = qemu_command(disk_info["path"], iso_info["path"], str(serial_log),
                           arch=arch, memory_mib=memory_mib, cpus=cpus, accel=accel,
                           firmware=firmware, uefi_code=uefi_code,
                           uefi_vars=uefi_vars, machine=machine, cpu=cpu,
                           restrict_user_network=restrict_user_network)
    except Exception as e:
        report["gates"]["boot_attempt"] = {"status": "FAIL", "error": str(e)}
        return _finish()

    report["gates"]["boot_attempt"] = {
        "status": "ATTEMPTED",
        "command": cmd,
        "timeout_seconds": timeout_seconds,
        "accel": "kvm" if os.path.exists("/dev/kvm") and not accel else (accel or "tcg"),
        "cpu": cpu or default_cpu(arch, accel),
        "firmware": firmware,
        "disk_size": disk_size,
        "network_restrict": restrict_user_network,
        "network_host_forward": False,
        "serial_log": str(serial_log),
        "note": "qemu started; -no-reboot captures first boot-to-reboot as process exit",
    }
    (work_dir / "qemu-command.json").write_text(
        json.dumps(cmd, indent=2) + "\n")

    started = time.time()
    try:
        with open(work_dir / "qemu.stderr.log", "w",
                  encoding="utf-8", errors="replace") as err:
            proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=err)
    except Exception as e:
        # A launch error (missing executable, permission, bad format) used to
        # escape without a report. Persist the attempt evidence first and let
        # main() return nonzero.
        report["gates"]["boot_attempt"].update({
            "status": "FAIL",
            "error": "failed to launch qemu: %s" % e,
            "elapsed_seconds": round(time.time() - started, 2),
        })
        report["notes"].append("qemu launch failed before a guest process started")
        return _finish()
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

    if disk_size:
        failure_markers = [
            name for name in ("gpt_backup_header_mismatch", "boot_label_failure",
                              "emergency_mode")
            if serial_scan.get(name)
        ]
        if failure_markers:
            status = "FAIL"
            detail = {"failure_markers": failure_markers}
        elif not serial_scan.get("exists"):
            status = "NOT_OBSERVED"
            detail = {"reason": "no serial console was captured"}
        else:
            status = "PASS"
            detail = {
                "note": "no stale-GPT, BOOT/UEFI label or emergency-mode marker "
                        "was seen; this does not prove growpart or resize2fs ran",
            }
        report["gates"]["expanded_disk_guest"] = {
            "status": status,
            "disk_size": disk_size,
            "gpt": report["gates"].get("boot_disk", {}).get("gpt"),
            **detail,
        }

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

    return _finish()


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
    parser.add_argument("--cpu", help="qemu CPU model (aarch64 defaults to max, or host with KVM)")
    parser.add_argument("--accel", help="qemu accel (tcg or kvm); default: kvm if /dev/kvm else tcg")
    parser.add_argument("--firmware", choices=("uefi", "bios"), default="uefi",
                        help="guest firmware; default uefi matches the declared artifact target")
    parser.add_argument("--disk-size", help="expanded standalone disk virtual "
                        "size (for example 20G); requires qemu-img and normalizes "
                        "the GPT backup header before boot")
    parser.add_argument("--machine", help="qemu machine type (for example q35, pc or virt)")
    parser.add_argument(
        "--restrict-user-network", action="store_true",
        help="render -netdev user,id=net0,restrict=on with no host forward "
             "(isolated acceptance mode; the default keeps plain user-mode NAT)")
    parser.add_argument("--no-offline-inspect", action="store_true")
    parser.add_argument("--verify-ref", help="base image ref to verify bytes against pinned provenance before boot")
    parser.add_argument("--profile", choices=("artifact", "fixture"), default="artifact")
    parser.add_argument("--sha256sums", help="path to published SHA256SUMS for byte verification")
    parser.add_argument("--generate-seed", action="store_true", help="generate deterministic seed into work-dir/seed using node_vm_build")
    parser.add_argument("--instance-id", default="oss377-boot-qual-0001")
    parser.add_argument("--node-label", default="oss377boot")
    args = parser.parse_args(argv)

    work_dir = Path(args.work_dir)
    try:
        validate_target(args.arch, args.firmware, args.machine)
    except BootQualError as e:
        print("node_vm_boot_qual: error: %s" % e, file=sys.stderr)
        return EXIT_USAGE

    boot_iso_path = Path(args.seed_iso) if args.seed_iso else (work_dir / "seed.iso")
    prebuilt_iso = bool(args.seed_iso and boot_iso_path.is_file())
    # A supplied seed directory or an existing prebuilt ISO is an input. Every
    # other attempt path is an output and must not exist yet.
    ignore = [boot_iso_path] if prebuilt_iso else []
    if args.seed_dir:
        ignore.append(Path(args.seed_dir))
    try:
        output_reservation = reserve_attempt_outputs(
            work_dir,
            extra_outputs=() if prebuilt_iso else (boot_iso_path,),
            ignore=ignore,
        )
    except BootQualError as e:
        print("node_vm_boot_qual: error: %s" % e, file=sys.stderr)
        return EXIT_USAGE

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
        if prebuilt_iso:
            report["gates"]["seed_iso"] = {
                "status": "PASS", "tool": "prebuilt", "path": str(boot_iso_path)}
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
        if args.firmware == "uefi":
            try:
                code, vars_template = find_uefi_firmware(args.arch)
                report["gates"]["firmware"] = {
                    "status": "PASS", "mode": "uefi",
                    "code": code, "vars_template": vars_template,
                }
            except Exception as e:
                report["gates"]["firmware"] = {
                    "status": "BLOCKED", "mode": "uefi", "reason": str(e)}
        else:
            report["gates"]["firmware"] = {
                "status": "PASS", "mode": "bios",
                "note": "BIOS is not the declared artifact target; use it only for diagnosis",
            }
        if args.disk_size:
            report["gates"]["disk_size"] = {
                "status": "CONFIGURED", "size": args.disk_size,
                "note": "expanded-disk boot prepares a GPT-normalized standalone "
                        "disk with qemu-img; no boot was attempted here",
            }
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
        seed_iso_path=args.seed_iso, firmware=args.firmware,
        disk_size=args.disk_size, machine=args.machine, cpu=args.cpu,
        restrict_user_network=args.restrict_user_network,
        output_reservation=output_reservation,
    )
    print(json.dumps(report, sort_keys=True))
    # Exit non-zero for hard preparation/verification failures and for a boot
    # attempt that could not be launched or constructed. Blocked/unobserved
    # gates remain reported in JSON as evidence.
    hard_fail = any(
        g.get("status") == "FAIL"
        for k, g in report.get("gates", {}).items()
        if k in ("image_bytes_verified", "seed", "seed_iso", "boot_disk",
                 "firmware", "boot_attempt", "expanded_disk_guest")
    )
    return EXIT_QUALIFICATION_FAILURE if hard_fail else 0


if __name__ == "__main__":
    raise SystemExit(main())
